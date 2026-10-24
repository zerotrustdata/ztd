// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// consensusVoteInferer runs one member's vote as governed inference.
// *dispatch.DispatchService satisfies it.
type consensusVoteInferer interface {
	DispatchInference(ctx context.Context, req dispatch.DispatchInferenceRequest) (*dispatch.DispatchInferenceResult, error)
}

// consensusBindingReader reads the bound consensus policy.
// *ConsensusStoreService satisfies it.
type consensusBindingReader interface {
	GetBoundConsensusApp(ctx context.Context) (*models.ConsensusPolicy, error)
}

// consensusMemberPolicies reads a member application's policy for its owner.
// *AppPolicyStoreService satisfies it.
type consensusMemberPolicies interface {
	GetAppPolicy(ctx context.Context, appID string) (*models.AppPolicy, error)
}

// consensusVoteRecorder appends member vote records to the Gateway audit
// chain. *storage.SQLAuditStore satisfies it.
type consensusVoteRecorder interface {
	RecordEvent(event *storage.Event) (int64, error)
}

// consensusVoteFeed publishes one live event onto the public mirror feed.
// *PublicPublisherService satisfies it. The mirror's /stream SSE is the
// delivery path the Evaluation Explorer already consumes.
type consensusVoteFeed interface {
	PublishLiveEvent(ctx context.Context, body []byte) error
}

// tribunalVoteStagePrefix marks a public live event that carries one recorded
// Tribunal vote. The bytes after the newline are the audit record, unchanged.
const tribunalVoteStagePrefix = "tribunal vote\n"

// TribunalConsensusDeliberator produces L2 votes from the Gateway-hosted
// Tribunal panel. Each bound member votes through one governed INFERENCE on
// the binding's pinned model, under its own app identity, and signs
// "<tx_hash>|<decision>" with its sealed app signing key. INFERENCE changes no
// state, so a member's vote inference is never itself voted on. The envelope it returns differs from the input in L2 only.
type TribunalConsensusDeliberator struct {
	store     consensusBindingReader
	policies  consensusMemberPolicies
	inference consensusVoteInferer
	votes     consensusVoteRecorder
	signer    ConsensusMemberSigner
	logger    *slog.Logger
	voteFeed  func() consensusVoteFeed
}

// NewTribunalConsensusDeliberator returns the Tribunal vote producer. signer
// may be nil until the Gateway host supplies it; without it no member votes
// and L4 rejects the envelope for quorum.
func NewTribunalConsensusDeliberator(store consensusBindingReader, policies consensusMemberPolicies, inference consensusVoteInferer, votes consensusVoteRecorder, signer ConsensusMemberSigner, logger *slog.Logger) (*TribunalConsensusDeliberator, error) {
	if store == nil {
		return nil, fmt.Errorf("tribunal consensus: store: %w", constants.ErrMissingRequiredField)
	}
	if policies == nil {
		return nil, fmt.Errorf("tribunal consensus: member policies: %w", constants.ErrMissingRequiredField)
	}
	if inference == nil {
		return nil, fmt.Errorf("tribunal consensus: inference dispatcher: %w", constants.ErrMissingRequiredField)
	}
	if votes == nil {
		return nil, fmt.Errorf("tribunal consensus: vote recorder: %w", constants.ErrMissingRequiredField)
	}
	if logger == nil {
		return nil, fmt.Errorf("tribunal consensus: logger: %w", constants.ErrMissingRequiredField)
	}
	return &TribunalConsensusDeliberator{store: store, policies: policies, inference: inference, votes: votes, signer: signer, logger: logger}, nil
}

// Deliberate collects the outcome of all bound Tribunal members before returning,
// subject to context cancellation and existing inference, signing, and audit failures.
// Members are asked in binding order and stop once the context ends. A member that
// cannot run its inference or sign casts no vote; an answer that is not a
// strict approve is a signed deny. Every
// member's outcome, with its stated reason, is recorded as a
// ztd.v1.consensus.vote.recorded audit event before its vote counts.
func (d *TribunalConsensusDeliberator) Deliberate(ctx context.Context, envelopeBytes []byte) (*governance.ConsensusDeliberation, error) {
	var env commonv1.GovernanceEnvelope
	if err := protojson.Unmarshal(envelopeBytes, &env); err != nil {
		return nil, fmt.Errorf("tribunal consensus: decode envelope: %w: %w", constants.ErrTxInvalidEnvelope, err)
	}
	computed, err := govpkg.GenerateMessageID(&env)
	if err != nil {
		return nil, fmt.Errorf("tribunal consensus: compute transaction hash: %w", err)
	}
	if env.GetTransactionHash() == "" || env.GetTransactionHash() != computed {
		return nil, fmt.Errorf("tribunal consensus: %w", constants.ErrTxTransactionHashMismatch)
	}
	bound, err := d.store.GetBoundConsensusApp(ctx)
	if err != nil {
		return nil, fmt.Errorf("tribunal consensus: load binding: %w", err)
	}
	if bound == nil || !bound.Enabled {
		return nil, constants.ErrTxL2ConsensusNotConfigured
	}
	provider, err := bound.ConsensusProvider()
	if err != nil {
		return nil, fmt.Errorf("tribunal consensus: binding provider: %w: %w", constants.ErrTxL2ConsensusNotConfigured, err)
	}
	if provider != constants.ConsensusProviderTribunal {
		return nil, fmt.Errorf("tribunal consensus: binding provider: %w", constants.ErrTxL2ConsensusNotConfigured)
	}

	if bound.EffectiveVoteKind() != models.ConsensusVoteKindGenerative {
		if _, err := models.ResolveConsensusBinding(models.ConsensusBindingSpec{Provider: constants.ConsensusProviderTribunal, AppID: bound.PrimaryAppID(), MemberAppIDs: bound.MemberAppIDs, Quorum: bound.Quorum, ModelTag: bound.ModelTag, ModelDigest: bound.ModelDigest, VoteKind: bound.VoteKind, InferenceOperatorID: bound.InferenceOperatorID, VoteThresholds: bound.VoteThresholds, AllowHosted: bound.AllowHosted}); err != nil {
			return nil, fmt.Errorf("tribunal consensus: decision binding: %w", err)
		}
	}

	subject, err := consensusSubject(&env)
	if err != nil {
		return nil, err
	}
	votes := make([]*commonv1.L2Vote, 0, len(bound.MemberAppIDs))
	var inferences []*operatorv1.ConsensusVoteInference
	batchSize := bound.Quorum
	if batchSize <= 0 {
		batchSize = len(bound.MemberAppIDs)
	}
	for i := 0; i < len(bound.MemberAppIDs); i += batchSize {
		if ctx.Err() != nil {
			break
		}
		end := min(i+batchSize, len(bound.MemberAppIDs))
		batch := bound.MemberAppIDs[i:end]
		for j, vote := range d.castVotes(ctx, bound, batch, computed, subject) {
			if vote.inference != nil {
				vote.inference.SubjectTransactionId = env.GetId()
				vote.inference.SubjectTransactionHash = computed
				inferences = append(inferences, vote.inference)
			}
			if _, signed := d.countVote(ctx, &env, bound, batch[j], computed, vote); signed != nil {
				votes = append(votes, signed)
			}
		}
	}

	if env.Governance == nil {
		env.Governance = &commonv1.GovernanceMetadata{}
	}
	env.Governance.L2 = &commonv1.L2Metadata{ConsensusSetId: bound.ID, Votes: votes}
	deliberated, err := protojson.Marshal(&env)
	if err != nil {
		return nil, fmt.Errorf("tribunal consensus: encode deliberated envelope: %w", err)
	}
	return &governance.ConsensusDeliberation{Envelope: deliberated, Inferences: inferences}, nil
}

// castVotes runs the batch's vote inferences concurrently. A batch is exactly
// the approvals still needed for quorum, so it never overshoots quorum; one
// member's deny or failure can leave the rest of its batch moot, and those
// votes are still recorded. Results return in batch order.
func (d *TribunalConsensusDeliberator) castVotes(ctx context.Context, bound *models.ConsensusPolicy, batch []string, txHash string, subject tribunal.ConsensusSubject) []memberVote {
	out := make([]memberVote, len(batch))
	var wg sync.WaitGroup
	for i, memberAppID := range batch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = d.castVote(ctx, bound, memberAppID, txHash, subject)
		}()
	}
	wg.Wait()
	return out
}

// countVote signs and records memberAppID's vote. It returns the L2 vote only
// when the member signed and its record is on the Gateway audit chain.
func (d *TribunalConsensusDeliberator) countVote(ctx context.Context, env *commonv1.GovernanceEnvelope, bound *models.ConsensusPolicy, memberAppID, computed string, vote memberVote) (bool, *commonv1.L2Vote) {
	var signature []byte
	if vote.failure == nil {
		var err error
		if signature, err = d.signer.Sign(memberAppID, governance.L2VotePayload(computed, vote.answer.Approve())); err != nil {
			d.logger.Warn("tribunal consensus: member cannot sign", slog.String("member_app_id", memberAppID), logging.ErrorKind(err))
			vote.failure = fmt.Errorf("sign vote: %w", err)
		}
	}
	// A vote counts only once its record is on the Gateway audit chain.
	if err := d.recordVote(ctx, env, bound, memberAppID, vote); err != nil {
		d.logger.Warn("tribunal consensus: member vote not recorded", slog.String("member_app_id", memberAppID), logging.ErrorKind(err))
		return false, nil
	}
	if vote.failure != nil {
		return false, nil
	}
	return vote.answer.Approve(), &commonv1.L2Vote{SignerKeyId: memberAppID, ConsensusSignature: hex.EncodeToString(signature), Decision: vote.answer.Approve()}
}

// memberVote is one member's deliberation outcome. failure is set when the
// member casts no vote.
type memberVote struct {
	inference     *operatorv1.ConsensusVoteInference
	evidence      []models.ConsensusDecisionEvidence
	answer        tribunal.ConsensusVoteAnswer
	inferenceTxID string
	attemptID     string
	failure       error
}

// recordVote appends memberAppID's vote to the Gateway audit chain under the
// envelope's transaction ID, the ID its receipts carry.
func (d *TribunalConsensusDeliberator) recordVote(ctx context.Context, env *commonv1.GovernanceEnvelope, bound *models.ConsensusPolicy, memberAppID string, vote memberVote) error {
	record := models.ConsensusVoteRecord{
		TransactionID:          env.GetId(),
		TransactionHash:        env.GetTransactionHash(),
		ConsensusSetID:         bound.ID,
		MemberAppID:            memberAppID,
		Reason:                 vote.answer.Reason,
		InferenceTransactionID: vote.inferenceTxID,
		ProviderAttemptID:      vote.attemptID,
		ModelTag:               bound.ModelTag,
		Quorum:                 bound.Quorum,
		VoteKind:               bound.EffectiveVoteKind(), DecisionEvidence: vote.evidence,
		RecordedAt: time.Now().UTC(),
	}
	switch {
	case vote.failure != nil:
		record.Outcome = models.ConsensusVoteOutcomeNoVote
		record.Failure = vote.failure.Error()
	case vote.answer.Approve():
		record.Outcome = models.ConsensusVoteOutcomeApprove
	default:
		record.Outcome = models.ConsensusVoteOutcomeDeny
		if vote.answer.Err != nil {
			record.Failure = vote.answer.Err.Error()
		}
	}
	content, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("tribunal consensus: encode vote record: %w", err)
	}
	if _, err := d.votes.RecordEvent(&storage.Event{
		TransactionID:     record.TransactionID,
		OperatorSessionID: env.GetOperatorSessionId(),
		Timestamp:         record.RecordedAt,
		Type:              constants.EventConsensusVoteRecorded,
		ContentText:       string(content),
	}); err != nil {
		return fmt.Errorf("tribunal consensus: record vote: %w", err)
	}
	if err := d.publishVote(ctx, env, memberAppID, record.RecordedAt, content); err != nil {
		d.logger.Warn("tribunal consensus: vote not published", slog.String("member_app_id", memberAppID), logging.ErrorKind(err))
	}
	return nil
}

// bindTribunalVoteFeed reads the spectator publisher when a vote is recorded.
// PublicSpectatorRuntime creates that publisher in Start, so capturing
// Publisher() during Gateway build stores nil for the process lifetime.
// A nil *PublicPublisherService is returned as a nil interface. Boxing the
// nil pointer would pass a nil check and panic inside PublishLiveEvent.
func bindTribunalVoteFeed(d *TribunalConsensusDeliberator, spectator *PublicSpectatorRuntime) {
	if d == nil {
		return
	}
	d.voteFeed = func() consensusVoteFeed {
		if spectator == nil {
			return nil
		}
		publisher := spectator.Publisher()
		if publisher == nil {
			return nil
		}
		return publisher
	}
}

// publishVote puts the audit record on the public feed when the Gateway is in
// consensus posture. The record bytes are the same JSON just written to the
// audit chain. A missing publisher or a publish failure leaves the recorded
// vote in place.
func (d *TribunalConsensusDeliberator) publishVote(ctx context.Context, env *commonv1.GovernanceEnvelope, memberAppID string, recordedAt time.Time, content []byte) error {
	if env.GetPosture() != string(constants.PostureConsensus) {
		return nil
	}
	var feed consensusVoteFeed
	if d.voteFeed != nil {
		feed = d.voteFeed()
	}
	if feed == nil {
		return nil
	}
	runID := env.GetInvestigationId()
	if runID == "" {
		runID = env.GetId()
	}
	completed, total := uint32(0), uint32(0)
	body, err := evalv1.MarshalCanonical(&evalv1.PublicLiveEvent{
		SchemaVersion:   "1.10.0",
		Kind:            "stage_updated",
		DatasetId:       "ds-live-" + runID,
		QualityState:    "live_in_progress",
		ObservedAt:      recordedAt.UTC().Format(time.RFC3339Nano),
		EventId:         env.GetId() + ":" + memberAppID,
		RunId:           runID,
		LifecycleStatus: "running",
		Completed:       &completed,
		Total:           &total,
		StageLabel:      tribunalVoteStagePrefix + string(content),
	})
	if err != nil {
		return fmt.Errorf("tribunal consensus: encode vote event: %w", err)
	}
	if err := feed.PublishLiveEvent(ctx, body); err != nil {
		return fmt.Errorf("tribunal consensus: publish vote: %w", err)
	}
	return nil
}

// castVote runs memberAppID's vote inference.
func (d *TribunalConsensusDeliberator) castVote(ctx context.Context, bound *models.ConsensusPolicy, memberAppID, txHash string, subject tribunal.ConsensusSubject) memberVote {
	member, found := tribunalMemberForAppID(memberAppID)
	if !found || d.signer == nil || !d.signer.HasKey(memberAppID) {
		d.logger.Warn("tribunal consensus: member key unavailable", slog.String("member_app_id", memberAppID), logging.ErrorKind(constants.ErrConsensusMemberKeyUnavailable))
		return memberVote{failure: constants.ErrConsensusMemberKeyUnavailable}
	}
	// App policies are keyed by the member's SPIFFE ID, as enrollment persists them.
	policy, err := d.policies.GetAppPolicy(ctx, protocol.NewWorkloadIdentity().AppSPIFFEID(memberAppID))
	if err == nil && (policy == nil || policy.OwnerUserID == "") {
		err = constants.ErrConsensusAppNotEnrolled
	}
	if err != nil {
		d.logger.Warn("tribunal consensus: member owner unavailable", slog.String("member_app_id", memberAppID), logging.ErrorKind(err))
		return memberVote{failure: fmt.Errorf("member owner: %w", err)}
	}
	attemptID := governance.ConsensusVoteAttemptID(txHash, memberAppID)
	system, user := tribunal.ConsensusVotePrompt(member, subject)
	request := dispatch.DispatchInferenceRequest{
		RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
		Role:                 models.InferenceModelRolePrimary,
		Messages: []*operatorv1.InferenceMessage{
			consensusVoteMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, system),
			consensusVoteMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, user),
		},
		ResponseFormat: &operatorv1.InferenceResponseFormat{
			MediaType:  "application/json",
			JsonSchema: tribunal.ConsensusVoteJSONSchema,
		},
		Model:             bound.ModelTag,
		ModelDigest:       bound.ModelDigest,
		ProviderAttemptID: attemptID,
		RequestorUserID:   policy.OwnerUserID,
		ActingAppID:       memberAppID,
	}
	request.TargetOperatorID = bound.InferenceOperatorID
	if bound.EffectiveVoteKind() == models.ConsensusVoteKindDecision {
		decision, err := tribunal.ConsensusDecisionRequest(member, subject, bound.AllowHosted)
		if err != nil {
			return memberVote{failure: err}
		}
		request.Decision = decision
		request.Messages = nil
		request.ResponseFormat = nil
	}
	result, err := d.inference.DispatchInference(ctx, request)
	if err == nil && (result == nil || result.Result == nil) {
		err = constants.ErrInferenceCompletionNoResult
	}
	inference := &operatorv1.ConsensusVoteInference{MemberAppId: memberAppID, ProviderAttemptId: attemptID}
	if result != nil {
		inference.TransactionId = result.TransactionID
		inference.Result = result.Result
		inference.Receipt = result.Receipt
		inference.ObservationReceipts = result.ObservationReceipts
	}
	if err != nil {
		_, inference.Failure = inferenceDispatchFailure(err)
		inference.Receipt = inference.Failure.GetReceipt()
		inference.TransactionId = inference.Receipt.GetTransactionId()
		inference.ObservationReceipts = inference.Failure.GetObservationReceipts()
	}
	if err != nil {
		d.logger.Warn("tribunal consensus: member inference failed", slog.String("member_app_id", memberAppID), logging.ErrorKind(err))
		return memberVote{inference: inference, inferenceTxID: inference.GetTransactionId(), attemptID: attemptID, failure: fmt.Errorf("member inference: %w", err)}
	}
	if bound.EffectiveVoteKind() == models.ConsensusVoteKindDecision {
		threshold := 0.0
		for _, t := range bound.VoteThresholds {
			if t.MemberAppID == memberAppID {
				threshold = t.Threshold
			}
		}
		if threshold < 0.5 || threshold > 1 {
			return memberVote{inference: inference, attemptID: attemptID, failure: constants.ErrConsensusDecisionBindingInvalid}
		}
		answer, evidence := tribunal.EvaluateConsensusDecision(member, request.Decision, result.Result.GetDecision(), threshold)
		return memberVote{inference: inference, answer: answer, evidence: evidence, inferenceTxID: result.TransactionID, attemptID: attemptID}
	}
	return memberVote{inference: inference,
		answer:        tribunal.ParseConsensusVote(models.InferenceResultText(result.Result)),
		inferenceTxID: result.TransactionID,
		attemptID:     attemptID,
	}
}

// consensusSubject renders the envelope fields a member votes on, including the
// typed action payload. A payload that does not decode for its action type
// fails closed: no member is asked to vote on an action it cannot see.
func consensusSubject(env *commonv1.GovernanceEnvelope) (tribunal.ConsensusSubject, error) {
	decoded, err := governance.DecodePayloadForAction(constants.ActionType(env.GetActionType()), env.GetPayload())
	if err != nil {
		return tribunal.ConsensusSubject{}, fmt.Errorf("tribunal consensus: decode payload: %w", err)
	}
	payload, err := protojson.Marshal(decoded)
	if err != nil {
		return tribunal.ConsensusSubject{}, fmt.Errorf("tribunal consensus: encode payload: %w", err)
	}
	intent := "{}"
	if env.GetIntentData() != nil {
		encoded, err := protojson.Marshal(env.GetIntentData())
		if err != nil {
			return tribunal.ConsensusSubject{}, fmt.Errorf("tribunal consensus: encode intent data: %w", err)
		}
		intent = string(encoded)
	}
	return tribunal.ConsensusSubject{
		ActionType:        env.GetActionType(),
		TargetResource:    env.GetTargetResource(),
		EventType:         env.GetEventType(),
		OperatorID:        env.GetOperatorId(),
		OperatorSessionID: env.GetOperatorSessionId(),
		CaseID:            env.GetCaseId(),
		InvestigationID:   env.GetInvestigationId(),
		TaskID:            env.GetTaskId(),
		PayloadJSON:       string(payload),
		IntentJSON:        intent,
	}, nil
}

// tribunalMemberForAppID maps a canonical consensus app ID to its panel member.
func tribunalMemberForAppID(appID string) (tribunal.Member, bool) {
	for _, member := range tribunal.AllMembers {
		if tribunal.ConsensusMemberAppID(member) == appID {
			return member, true
		}
	}
	return "", false
}

func consensusVoteMessage(role operatorv1.InferenceMessageRole, text string) *operatorv1.InferenceMessage {
	return &operatorv1.InferenceMessage{Role: role, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: text}}}}
}

// consensusDeliberatorRouter picks the bound provider's vote producer for
// each envelope. The Tribunal producer is bound after inference dispatch
// exists, during Gateway construction, and before any request is served.
type consensusDeliberatorRouter struct {
	store       consensusBindingReader
	application mcp.L2ConsensusDeliberator
	tribunal    *TribunalConsensusDeliberator
}

// Deliberate routes envelopeBytes to the bound provider's deliberator.
func (r *consensusDeliberatorRouter) Deliberate(ctx context.Context, envelopeBytes []byte) (*governance.ConsensusDeliberation, error) {
	bound, err := r.store.GetBoundConsensusApp(ctx)
	if err != nil {
		return nil, err
	}
	if bound == nil || !bound.Enabled {
		return nil, constants.ErrTxL2ConsensusNotConfigured
	}
	provider, err := bound.ConsensusProvider()
	if err != nil {
		return nil, fmt.Errorf("consensus deliberation: %w", err)
	}
	if provider == constants.ConsensusProviderTribunal {
		if r.tribunal == nil {
			return nil, constants.ErrTxL2ConsensusNotConfigured
		}
		return r.tribunal.Deliberate(ctx, envelopeBytes)
	}
	return r.application.Deliberate(ctx, envelopeBytes)
}
