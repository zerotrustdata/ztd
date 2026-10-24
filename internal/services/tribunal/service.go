// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// MemberDispatcher runs one member call as a governed INFERENCE dispatch. A
// call that did not complete returns its typed failure, carrying the
// Inference Operator's FAILED receipt when one was signed.
type MemberDispatcher interface {
	DispatchMember(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, *operatorv1.InferenceDispatchFailure)
}

// Service is the Gateway's Tribunal: it deliberates one shell command intent with
// every member call dispatched as governed inference. It returns the audited
// typed arguments; dispatching them is the caller's ordinary governed MCP_CALL.
type Service struct {
	members  MemberDispatcher
	doctrine *governance.L1Doctrine
}

// NewService builds the Tribunal over governed inference and the active
// Gateway doctrine. Neither dependency may be omitted.
func NewService(members MemberDispatcher, doctrine *governance.L1Doctrine) (*Service, error) {
	if members == nil {
		return nil, fmt.Errorf("tribunal: member dispatcher: %w", constants.ErrMissingRequiredField)
	}
	if doctrine == nil {
		return nil, fmt.Errorf("tribunal: %w", constants.ErrDoctrineRequired)
	}
	return &Service{members: members, doctrine: doctrine}, nil
}

// Deliberate runs one intent through the Tribunal. The result never carries an
// execution outcome; the caller sets it after dispatching approved arguments.
// An error means the request itself is invalid; a deliberation that ends
// without approved arguments, including on a failed member inference, is a result.
func (s *Service) Deliberate(ctx context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error) {
	if err := ValidateIntentRequest(req); err != nil {
		return nil, err
	}
	template := req.GetMemberInference()
	inferer := &memberInferer{members: s.members, template: template, prefix: req.GetProviderAttemptPrefix()}
	cfg := Config{
		Inferer:     inferer,
		Doctrine:    s.doctrine,
		Tier:        req.GetTier(),
		ModelTag:    template.GetModel(),
		ModelDigest: template.GetModelDigest(),
	}
	run, err := Run(ctx, cfg, Request{
		Request:          req.GetRequest(),
		Guidelines:       req.GetGuidelines(),
		WorkingDirectory: req.GetWorkingDirectory(),
		TimeoutSeconds:   int(req.GetTimeoutSeconds()),
		OS:               req.GetOs(),
		Shell:            req.GetShell(),
	}, req.GetStepStartIndex())
	result := &tribunalv1.TribunalIntentResult{MemberCalls: inferer.calls}
	if err != nil {
		if errors.Is(err, constants.ErrTribunalInferenceFailed) {
			result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED
			return result, nil
		}
		return nil, fmt.Errorf("tribunal: deliberate: %w", err)
	}
	result.Outcome = run.Outcome.Proto()
	result.Arguments = run.Arguments.Proto()
	result.ArgumentsJson = run.ArgumentsJSON
	result.Steps = run.Steps
	if run.Outcome == OutcomeBlocked && run.Risk != nil {
		result.RiskRationale = run.Risk.Rationale
		result.RiskFeedback = run.Risk.Feedback
	}
	if run.Outcome == OutcomeAuditFailed && run.Audit != nil {
		result.AuditFailure = run.Audit.Failure
	}
	return result, nil
}

// ReviewAdmitted gives the final Auditor the actual L1 assessment, recorded
// L2 votes and their inference records, and L3 human decision. It is technical
// context, never proof verification. A changed command returns
// ErrTribunalCommandRevised so the caller obtains fresh decisions before review
// and dispatch; it must never alter the already-authorized envelope.
//
// A human denial is never reviewed: L4 records it as signed, whatever a model
// thinks. An envelope without L2 votes or an L3 proof carries nothing beyond
// the L1 check every authored argument already passed, so it is not reviewed
// again. The review must finish within the envelope's lifetime; a review that
// cannot fails closed before publish instead of dispatching an expired envelope.
func (s *Service) ReviewAdmitted(ctx context.Context, req *tribunalv1.TribunalIntentRequest, result *tribunalv1.TribunalIntentResult, env *commonv1.GovernanceEnvelope, votes []*operatorv1.ConsensusVoteInference) error {
	l3 := env.GetGovernance().GetL3()
	if l3.GetDenied() || (len(env.GetGovernance().GetL2().GetVotes()) == 0 && l3.GetProof() == nil) {
		return nil
	}
	reviewCtx, cancel := context.WithDeadline(ctx, env.GetExpiresAt().AsTime())
	defer cancel()
	// failed classifies a review that ended without a verdict: running out of
	// envelope lifetime is a failed audit, a failed member call is an
	// inference failure, and the caller's own cancellation stays an error.
	failed := func(err error) error {
		switch {
		case ctx.Err() == nil && reviewCtx.Err() != nil:
			result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED
			result.AuditFailure = "contextual review did not finish within the admitted envelope's lifetime"
		case errors.Is(err, constants.ErrTribunalInferenceFailed):
			result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED
		}
		return fmt.Errorf("tribunal: contextual review: %w", err)
	}
	arguments := models.RunShellCommandRequestFromProto(result.GetArguments())
	inferer := &memberInferer{members: s.members, template: req.GetMemberInference(), prefix: req.GetProviderAttemptPrefix(), calls: result.GetMemberCalls()}
	defer func() { result.MemberCalls = inferer.calls }()
	cfg := Config{Inferer: inferer, Doctrine: s.doctrine, Tier: req.GetTier(), ModelTag: req.GetMemberInference().GetModel(), ModelDigest: req.GetMemberInference().GetModelDigest()}
	index := req.GetStepStartIndex() + uint32(len(result.GetSteps()))
	steps := stepRecorder{cfg: cfg, index: index}
	defer func() { result.Steps = append(result.Steps, steps.steps...) }()
	reviewReq := Request{
		Request: req.GetRequest(), Guidelines: req.GetGuidelines(), WorkingDirectory: req.GetWorkingDirectory(),
		TimeoutSeconds: int(req.GetTimeoutSeconds()), OS: req.GetOs(), Shell: req.GetShell(),
		Governance: &AuditorGovernanceContext{
			Envelope: env, ConsensusInferences: votes, AuthoringSteps: result.GetSteps(),
			DoctrineBundleHash: s.doctrine.DoctrineBundleHash(), DoctrineBundleVersion: s.doctrine.DoctrineBundleVersion(),
		},
	}
	vote := Vote{Round: 1, Winner: arguments, Clusters: []Cluster{{ID: clusterID(0), Arguments: arguments}}}
	review, err := audit(reviewCtx, cfg, reviewReq, vote)
	if err != nil {
		return failed(err)
	}
	steps.review(review)
	if review.Failure != "" {
		result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED
		result.AuditFailure = review.Failure
		return fmt.Errorf("tribunal: contextual review: %w", constants.ErrTribunalAnswerInvalid)
	}
	if review.Arguments.CanonicalJSON() == arguments.CanonicalJSON() {
		return nil
	}
	risk, err := assessRisk(reviewCtx, cfg, reviewReq, review.Arguments)
	if err != nil {
		return failed(err)
	}
	steps.risk(review.Arguments, risk)
	if risk.Blocked() {
		result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED
		result.RiskRationale, result.RiskFeedback = risk.Rationale, risk.Feedback
		return fmt.Errorf("tribunal: contextual revision risk: %w", constants.ErrTribunalAnswerInvalid)
	}
	result.Arguments = review.Arguments.Proto()
	result.ArgumentsJson = review.Arguments.CanonicalJSON()
	return constants.ErrTribunalCommandRevised
}

// ValidateIntentRequest checks the fields the Tribunal needs, rejects a member
// template that sets what the Gateway owns per call, and requires the member
// template's role to be the inference role of the requested tier, so every
// recorded step is attributed to the role its calls actually run under.
func ValidateIntentRequest(req *tribunalv1.TribunalIntentRequest) error {
	switch {
	case strings.TrimSpace(req.GetRequest()) == "":
		return fmt.Errorf("tribunal: request: %w", constants.ErrMissingRequiredField)
	case req.GetTargetOperatorSessionId() == "":
		return fmt.Errorf("tribunal: target_operator_session_id: %w", constants.ErrMissingRequiredField)
	case req.GetProviderAttemptPrefix() == "":
		return fmt.Errorf("tribunal: provider_attempt_prefix: %w", constants.ErrMissingRequiredField)
	case req.GetTier() == evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED:
		return fmt.Errorf("tribunal: tier: %w", constants.ErrMissingRequiredField)
	case req.GetMemberInference() == nil:
		return fmt.Errorf("tribunal: member_inference: %w", constants.ErrMissingRequiredField)
	case models.InferenceModelRoleFromProto(req.GetMemberInference().GetRole()) == models.InferenceModelRoleUnspecified:
		return fmt.Errorf("tribunal: member_inference.role: %w", constants.ErrMissingRequiredField)
	}
	template := req.GetMemberInference()
	switch {
	case len(template.GetMessages()) > 0:
		return fmt.Errorf("tribunal: member_inference.messages are set per call: %w", constants.ErrTribunalTemplateInvalid)
	case len(template.GetTools()) > 0:
		return fmt.Errorf("tribunal: member_inference.tools must be empty: %w", constants.ErrTribunalTemplateInvalid)
	case template.GetProviderAttemptId() != "":
		return fmt.Errorf("tribunal: member_inference.provider_attempt_id is set per call: %w", constants.ErrTribunalTemplateInvalid)
	case template.GetActingAppId() != "":
		return fmt.Errorf("tribunal: member_inference.acting_app_id comes from the caller identity: %w", constants.ErrTribunalTemplateInvalid)
	case template.GetStream():
		return fmt.Errorf("tribunal: member_inference.stream must be false: %w", constants.ErrTribunalTemplateInvalid)
	case template.GetResponseFormat() != nil:
		return fmt.Errorf("tribunal: member_inference.response_format is set per call: %w", constants.ErrTribunalTemplateInvalid)
	}
	tierRole, err := InferenceRoleForTier(req.GetTier())
	if err != nil {
		return fmt.Errorf("tribunal: tier: %w", err)
	}
	if models.InferenceModelRoleFromProto(template.GetRole()) != tierRole {
		return fmt.Errorf("tribunal: member_inference.role %s does not run tier %s: %w", template.GetRole(), req.GetTier(), constants.ErrTribunalTierMismatch)
	}
	return nil
}

// InferenceRoleForTier maps a campaign tier to the chat-tier inference role
// its governed calls run under. An unspecified or unknown tier is
// constants.ErrInferenceRoleInvalid.
func InferenceRoleForTier(tier evalv1.ModelCampaignRole) (models.InferenceModelRole, error) {
	switch tier {
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY:
		return models.InferenceModelRolePrimary, nil
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT:
		return models.InferenceModelRoleAssistant, nil
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE:
		return models.InferenceModelRoleLite, nil
	default:
		return models.InferenceModelRoleUnspecified, fmt.Errorf("tribunal: inference role for tier %s: %w", tier, constants.ErrInferenceRoleInvalid)
	}
}

// Proto converts the outcome to its wire form.
func (o Outcome) Proto() tribunalv1.TribunalOutcome {
	switch o {
	case OutcomeApproved:
		return tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED
	case OutcomeNoConsensus:
		return tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS
	case OutcomeBlocked:
		return tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED
	case OutcomeAuditFailed:
		return tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED
	default:
		return tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_UNSPECIFIED
	}
}

// Proto converts the stage to its wire form.
func (s Stage) Proto() tribunalv1.TribunalStage {
	switch s {
	case StageGenerate:
		return tribunalv1.TribunalStage_TRIBUNAL_STAGE_GENERATE
	case StageMarshal:
		return tribunalv1.TribunalStage_TRIBUNAL_STAGE_MARSHAL
	case StageAudit:
		return tribunalv1.TribunalStage_TRIBUNAL_STAGE_AUDIT
	default:
		return tribunalv1.TribunalStage_TRIBUNAL_STAGE_UNSPECIFIED
	}
}

// StageFromProto converts a wire stage back to its Stage; an unspecified or
// unknown stage is empty.
func StageFromProto(stage tribunalv1.TribunalStage) Stage {
	switch stage {
	case tribunalv1.TribunalStage_TRIBUNAL_STAGE_GENERATE:
		return StageGenerate
	case tribunalv1.TribunalStage_TRIBUNAL_STAGE_MARSHAL:
		return StageMarshal
	case tribunalv1.TribunalStage_TRIBUNAL_STAGE_AUDIT:
		return StageAudit
	default:
		return ""
	}
}

// memberInferer answers each deliberation call with one governed inference
// built from the request's template, and records every call it made.
type memberInferer struct {
	members  MemberDispatcher
	template *operatorv1.InferenceDispatchRequest
	prefix   string
	calls    []*tribunalv1.TribunalMemberCall
}

func (m *memberInferer) Infer(ctx context.Context, call Call) (string, error) {
	req := proto.Clone(m.template).(*operatorv1.InferenceDispatchRequest)
	req.ProviderAttemptId = fmt.Sprintf("%s-%d", m.prefix, len(m.calls)+1)
	req.Messages = []*operatorv1.InferenceMessage{
		textMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, call.System),
		textMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, call.User),
	}
	if call.JSONSchema != "" {
		req.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: constants.MediaTypeJSON, JsonSchema: call.JSONSchema}
	}
	record := &tribunalv1.TribunalMemberCall{
		Stage:             call.Stage.Proto(),
		Persona:           call.Persona,
		Round:             call.Round,
		ProviderAttemptId: req.GetProviderAttemptId(),
	}
	m.calls = append(m.calls, record)
	response, failure := m.members.DispatchMember(ctx, req)
	if failure != nil {
		record.Failure = failure
		record.Receipt = failure.GetReceipt()
		return "", fmt.Errorf("%w: %s %s: %s", constants.ErrTribunalInferenceFailed, call.Stage, call.Persona, failure.GetError())
	}
	record.TransactionId = response.GetTransactionId()
	record.Result = response.GetResult()
	record.Receipt = response.GetReceipt()
	record.ObservationReceipts = response.GetObservationReceipts()
	return models.InferenceResultText(response.GetResult()), nil
}

func textMessage(role operatorv1.InferenceMessageRole, text string) *operatorv1.InferenceMessage {
	return &operatorv1.InferenceMessage{Role: role, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: text}}}}
}
