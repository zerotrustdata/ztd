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
	"fmt"
	"log/slog"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
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

// TribunalConsensusDeliberator produces L2 votes from the Gateway-hosted
// Tribunal panel. Each bound member votes through one governed INFERENCE on
// the binding's pinned model, under its own app identity and a signed
// deliberation attestation, and signs "<tx_hash>|<decision>" with its sealed
// app signing key. The envelope it returns differs from the input in L2 only.
type TribunalConsensusDeliberator struct {
	store     consensusBindingReader
	policies  consensusMemberPolicies
	inference consensusVoteInferer
	signer    ConsensusMemberSigner
	logger    *slog.Logger
}

// NewTribunalConsensusDeliberator returns the Tribunal vote producer. signer
// may be nil until the Gateway host supplies it; without it no member votes
// and L4 rejects the envelope for quorum.
func NewTribunalConsensusDeliberator(store consensusBindingReader, policies consensusMemberPolicies, inference consensusVoteInferer, signer ConsensusMemberSigner, logger *slog.Logger) *TribunalConsensusDeliberator {
	return &TribunalConsensusDeliberator{store: store, policies: policies, inference: inference, signer: signer, logger: logger}
}

// Deliberate collects member votes on the envelope until quorum is met or can
// no longer be met. A member that cannot run its inference or sign casts no
// vote; an answer that is not a strict approve is a signed deny.
func (d *TribunalConsensusDeliberator) Deliberate(ctx context.Context, envelopeBytes []byte) ([]byte, error) {
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
	if provider, err := bound.ConsensusProvider(); err != nil || provider != constants.ConsensusProviderTribunal {
		return nil, fmt.Errorf("tribunal consensus: binding provider: %w", constants.ErrTxL2ConsensusNotConfigured)
	}

	subject, err := consensusSubject(&env)
	if err != nil {
		return nil, err
	}
	votes := make([]*commonv1.L2Vote, 0, bound.Quorum)
	approvals := 0
	for i, memberAppID := range bound.MemberAppIDs {
		if approvals >= bound.Quorum || approvals+len(bound.MemberAppIDs)-i < bound.Quorum {
			break
		}
		approve, ok := d.memberDecision(ctx, bound, memberAppID, computed, subject)
		if !ok {
			continue
		}
		signature, err := d.signer.Sign(memberAppID, governance.L2VotePayload(computed, approve))
		if err != nil {
			d.logger.Warn("tribunal consensus: member cannot sign", slog.String("member_app_id", memberAppID), logging.ErrorKind(err))
			continue
		}
		votes = append(votes, &commonv1.L2Vote{SignerKeyId: memberAppID, ConsensusSignature: hex.EncodeToString(signature), Decision: approve})
		if approve {
			approvals++
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
	return deliberated, nil
}

// memberDecision runs memberAppID's vote inference. ok is false when the
// member casts no vote.
func (d *TribunalConsensusDeliberator) memberDecision(ctx context.Context, bound *models.ConsensusPolicy, memberAppID, txHash string, subject tribunal.ConsensusSubject) (approve, ok bool) {
	member, found := tribunalMemberForAppID(memberAppID)
	if !found || d.signer == nil || !d.signer.HasKey(memberAppID) {
		d.logger.Warn("tribunal consensus: member key unavailable", slog.String("member_app_id", memberAppID), logging.ErrorKind(constants.ErrConsensusMemberKeyUnavailable))
		return false, false
	}
	policy, err := d.policies.GetAppPolicy(ctx, memberAppID)
	if err != nil || policy == nil || policy.OwnerUserID == "" {
		d.logger.Warn("tribunal consensus: member owner unavailable", slog.String("member_app_id", memberAppID), logging.ErrorKind(err))
		return false, false
	}
	system, user := tribunal.ConsensusVotePrompt(member, subject)
	result, err := d.inference.DispatchInference(withConsensusDeliberation(ctx, memberAppID), dispatch.DispatchInferenceRequest{
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
		ProviderAttemptID: consensusVoteAttemptID(txHash, memberAppID),
		RequestorUserID:   policy.OwnerUserID,
		ActingAppID:       memberAppID,
	})
	if err != nil {
		d.logger.Warn("tribunal consensus: member inference failed", slog.String("member_app_id", memberAppID), logging.ErrorKind(err))
		return false, false
	}
	return tribunal.DecodeConsensusVote(models.InferenceResultText(result.Result)), true
}

// consensusSubject renders the envelope fields a member votes on.
func consensusSubject(env *commonv1.GovernanceEnvelope) (tribunal.ConsensusSubject, error) {
	intent := "{}"
	if env.GetIntentData() != nil {
		encoded, err := protojson.Marshal(env.GetIntentData())
		if err != nil {
			return tribunal.ConsensusSubject{}, fmt.Errorf("tribunal consensus: encode intent data: %w", err)
		}
		intent = string(encoded)
	}
	return tribunal.ConsensusSubject{
		ActionType:     env.GetActionType(),
		TargetResource: env.GetTargetResource(),
		EventType:      env.GetEventType(),
		IntentJSON:     intent,
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

// consensusVoteAttemptID names one member's vote attempt on one transaction.
func consensusVoteAttemptID(txHash, memberAppID string) string {
	prefix := txHash
	if len(prefix) > 32 {
		prefix = prefix[:32]
	}
	return "l2-" + prefix + "-" + strings.TrimPrefix(memberAppID, constants.TribunalConsensusAppIDPrefix)
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
func (r *consensusDeliberatorRouter) Deliberate(ctx context.Context, envelopeBytes []byte) ([]byte, error) {
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
