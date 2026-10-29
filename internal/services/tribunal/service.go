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
	"github.com/zerotrustdata/ztd/internal/services/agentpersona"
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
	personas *agentpersona.Registry
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
	personas, err := agentpersona.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("tribunal: personas: %w", err)
	}
	return &Service{members: members, doctrine: doctrine, personas: personas}, nil
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
	templates := tribunalTemplates(req)
	inferer := &memberInferer{members: s.members, templates: templates, prefix: req.GetProviderAttemptPrefix()}
	cfg := Config{
		Inferer:     inferer,
		Personas:    s.personas,
		Doctrine:    s.doctrine,
		Tier:        req.GetTier(),
		ModelTag:    template.GetModel(),
		ModelDigest: template.GetModelDigest(),
		StageModels: stageModels(templates),
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
	templates := tribunalTemplates(req)
	inferer := &memberInferer{members: s.members, templates: templates, prefix: req.GetProviderAttemptPrefix(), calls: result.GetMemberCalls()}
	defer func() { result.MemberCalls = inferer.calls }()
	cfg := Config{Inferer: inferer, Personas: s.personas, Doctrine: s.doctrine, Tier: req.GetTier(), ModelTag: req.GetMemberInference().GetModel(), ModelDigest: req.GetMemberInference().GetModelDigest(), StageModels: stageModels(templates)}
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
// templates that set what the Gateway owns per call, and freezes every
// authoring and vote persona so a stale Gateway fails closed before dispatch.
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
	case req.GetMarshalInference() == nil:
		return fmt.Errorf("tribunal: marshal_inference: %w", constants.ErrMissingRequiredField)
	case req.GetAuditorInference() == nil:
		return fmt.Errorf("tribunal: auditor_inference: %w", constants.ErrMissingRequiredField)
	}
	if _, err := InferenceRoleForTier(req.GetTier()); err != nil {
		return fmt.Errorf("tribunal: tier: %w", err)
	}
	for stage, template := range tribunalTemplates(req) {
		if models.InferenceModelRoleFromProto(template.GetRole()) == models.InferenceModelRoleUnspecified {
			return fmt.Errorf("tribunal: %s inference.role: %w", stage, constants.ErrMissingRequiredField)
		}
		if len(template.GetMessages()) > 0 || len(template.GetTools()) > 0 || template.GetProviderAttemptId() != "" || template.GetActingAppId() != "" || template.GetStream() || template.GetResponseFormat() != nil {
			return fmt.Errorf("tribunal: %s template has gateway-owned per-call fields: %w", stage, constants.ErrTribunalTemplateInvalid)
		}
	}
	if err := validateStageRoles(req); err != nil {
		return err
	}
	return validatePersonaBindings(req.GetPersonas())
}

func validateStageRoles(req *tribunalv1.TribunalIntentRequest) error {
	registry, err := agentpersona.NewRegistry()
	if err != nil {
		return fmt.Errorf("tribunal: personas: %w", err)
	}
	stagePersonas := map[Stage]string{StageGenerate: string(MemberAxiom), StageMarshal: PersonaMarshal, StageAudit: PersonaAuditor}
	templates := tribunalTemplates(req)
	if req.GetHomogeneous() {
		wantRole, err := InferenceRoleForTier(req.GetTier())
		if err != nil {
			return fmt.Errorf("tribunal: tier: %w", err)
		}
		candidate := templates[StageGenerate]
		candidateIdentity := proto.Clone(candidate).(*operatorv1.InferenceDispatchRequest)
		for stage, template := range templates {
			stageIdentity := proto.Clone(template).(*operatorv1.InferenceDispatchRequest)
			stageIdentity.Role = candidateIdentity.GetRole()
			if models.InferenceModelRoleFromProto(template.GetRole()) != wantRole || !proto.Equal(stageIdentity, candidateIdentity) {
				return fmt.Errorf("tribunal: homogeneous %s template differs from the designated candidate: %w", stage, constants.ErrTribunalTierMismatch)
			}
		}
		return nil
	}
	for stage, template := range templates {
		personaIDs := []string{stagePersonas[stage]}
		if stage == StageGenerate {
			personaIDs = make([]string, 0, len(AllMembers))
			for _, member := range AllMembers {
				personaIDs = append(personaIDs, string(member))
			}
		}
		for _, personaID := range personaIDs {
			persona, err := registry.Get(personaID)
			if err != nil || models.InferenceModelRoleFromProto(template.GetRole()) != persona.Tier {
				return fmt.Errorf("tribunal: %s template role does not match persona %s tier: %w", stage, personaID, constants.ErrTribunalTierMismatch)
			}
		}
	}
	return nil
}

func validatePersonaBindings(bindings []*evalv1.AgentPersonaBinding) error {
	registry, err := agentpersona.NewRegistry()
	if err != nil {
		return fmt.Errorf("tribunal: personas: %w", err)
	}
	wantIDs := append([]Member(nil), AllMembers...)
	expected := make(map[string]bool, len(wantIDs)+2)
	for _, id := range wantIDs {
		expected[string(id)] = true
	}
	expected[PersonaMarshal], expected[PersonaAuditor] = true, true
	seen := make(map[string]bool, len(expected))
	for _, binding := range bindings {
		if binding == nil || !expected[binding.GetPersonaId()] || seen[binding.GetPersonaId()] {
			return fmt.Errorf("tribunal: invalid persona binding: %w", constants.ErrPersonaDigestMismatch)
		}
		p, err := registry.Get(binding.GetPersonaId())
		if err != nil || binding.GetVersion() != p.Version || binding.GetDigest() != p.Digest() || binding.GetVoteDigest() != p.VoteDigest() {
			return fmt.Errorf("tribunal: persona %s binding differs from Gateway registry: %w", binding.GetPersonaId(), constants.ErrPersonaDigestMismatch)
		}
		seen[binding.GetPersonaId()] = true
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("tribunal: incomplete persona bindings: %w", constants.ErrPersonaDigestMismatch)
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
	members   MemberDispatcher
	templates map[Stage]*operatorv1.InferenceDispatchRequest
	prefix    string
	calls     []*tribunalv1.TribunalMemberCall
}

func (m *memberInferer) Infer(ctx context.Context, call Call) (string, error) {
	template := m.templates[call.Stage]
	if template == nil {
		return "", fmt.Errorf("tribunal: no inference template for %s: %w", call.Stage, constants.ErrMissingRequiredField)
	}
	req := proto.Clone(template).(*operatorv1.InferenceDispatchRequest)
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
		record.TransactionId = failure.GetReceipt().GetTransactionId()
		record.ObservationReceipts = failure.GetObservationReceipts()
		return "", fmt.Errorf("%w: %s %s: %s", constants.ErrTribunalInferenceFailed, call.Stage, call.Persona, failure.GetError())
	}
	record.TransactionId = response.GetTransactionId()
	record.Result = response.GetResult()
	record.Receipt = response.GetReceipt()
	record.ObservationReceipts = response.GetObservationReceipts()
	return models.InferenceResultText(response.GetResult()), nil
}

func tribunalTemplates(req *tribunalv1.TribunalIntentRequest) map[Stage]*operatorv1.InferenceDispatchRequest {
	return map[Stage]*operatorv1.InferenceDispatchRequest{StageGenerate: req.GetMemberInference(), StageMarshal: req.GetMarshalInference(), StageAudit: req.GetAuditorInference()}
}

func stageModels(templates map[Stage]*operatorv1.InferenceDispatchRequest) map[Stage]StageModel {
	out := make(map[Stage]StageModel, len(templates))
	for stage, template := range templates {
		if template == nil {
			continue
		}
		role := models.InferenceModelRoleFromProto(template.GetRole())
		tier := evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED
		switch role {
		case models.InferenceModelRoleLite:
			tier = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
		case models.InferenceModelRoleAssistant:
			tier = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT
		case models.InferenceModelRolePrimary:
			tier = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
		}
		out[stage] = StageModel{Tier: tier, Tag: template.GetModel(), Digest: template.GetModelDigest()}
	}
	return out
}

func textMessage(role operatorv1.InferenceMessageRole, text string) *operatorv1.InferenceMessage {
	return &operatorv1.InferenceMessage{Role: role, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: text}}}}
}
