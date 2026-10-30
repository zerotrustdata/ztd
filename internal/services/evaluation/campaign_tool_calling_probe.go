// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	toolCallingProbeScenario  = "tool-calling-probe"
	toolCallingProbeCriterion = "tool-calling-capability"
	toolCallingProbeFailure   = "capability_unsupported: tool-call probe did not return a structured probe_echo call"
)

// CampaignToolCallingProber performs the run's non-scored, governed eligibility
// probe. Failed dispatches are errors, never proof of a missing capability.
type CampaignToolCallingProber interface {
	ProbeToolCalling(context.Context, AssignmentExecutionRequest) (*evalv1.ModelToolCallingProbeEvidence, error)
}

func (e *CampaignAgentExecutor) ProbeToolCalling(ctx context.Context, req AssignmentExecutionRequest) (*evalv1.ModelToolCallingProbeEvidence, error) {
	run, err := e.newRun(req)
	if err != nil {
		return nil, err
	}
	role, err := tribunal.InferenceRoleForTier(run.designated)
	if err != nil {
		return nil, err
	}
	probe := InferenceProbeRequest{
		ProviderAttemptID: e.newID(toolCallingProbeScenario), Role: role,
		Model: run.variant.GetServedModelTag(), ModelDigest: run.variant.GetModelDigest(),
		TargetOperatorSessionID: req.Binding.Topology.Inference.OperatorSessionID,
		CampaignID:              req.Assignment.GetCampaignId(), ModelRegistryDigest: req.Binding.ModelRegistryDigest,
		ModelRegistry: req.Binding.ModelRegistry,
	}
	configureToolCallingProbe(&probe)
	dispatch, err := BuildInferenceProbeDispatchRequest(probe)
	if err != nil {
		return nil, err
	}
	dispatch.RunId = req.Assignment.GetRunId()
	dispatch.AssignmentId = req.Assignment.GetAssignmentId()
	dispatch.EvaluationAttemptId = probe.ProviderAttemptID
	dispatch.ScenarioId = toolCallingProbeScenario
	response, err := e.inference.DispatchInference(ctx, dispatch)
	if err != nil {
		return nil, toolCallingProbeError("dispatch", err)
	}
	evidence := &evalv1.ModelToolCallingProbeEvidence{
		VariantId: run.variant.GetVariantId(), Request: dispatch, Response: response,
		Anchor:              req.Binding.Topology.Inference.Anchor,
		InferenceOperatorId: req.Binding.Topology.Inference.OperatorID,
		InferenceSessionId:  req.Binding.Topology.Inference.OperatorSessionID,
	}
	if err := verifyToolCallingProbeEvidence(req.Assignment.GetRunId(), req.Binding.ModelRegistryDigest, run.variant, evidence); err != nil {
		return nil, toolCallingProbeError("evidence", err)
	}
	return evidence, nil
}

// toolCallingProbeError marks a probe dispatch or evidence failure. It must
// not use ErrEvaluationAssignmentExecutionFailed: that sentinel is recoverable
// and would let the run continue without a verified probe.
func toolCallingProbeError(scope string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %w", constants.ErrEvaluationToolCallingProbeFailed, scope, err)
}

func assignmentToolCallingVariant(req AssignmentExecutionRequest) (*evalv1.ModelVariant, error) {
	if target := req.Assignment.GetHomogeneous(); target != nil {
		return target.GetCandidateVariant(), nil
	}
	stack := req.Assignment.GetHeterogeneous().GetStack()
	var id string
	switch req.Assignment.GetHeterogeneous().GetDesignatedRole() {
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY:
		id = stack.GetPrimarySlot().GetVariantId()
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT:
		id = stack.GetAssistantSlot().GetVariantId()
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE:
		id = stack.GetLiteSlot().GetVariantId()
	}
	for _, variant := range req.Binding.ModelVariants {
		if variant.GetVariantId() == id {
			return variant, nil
		}
	}
	return nil, fmt.Errorf("evaluation: tool-calling variant: %w", constants.ErrFormationRegistryBinding)
}

func assignmentDeclaresTools(req AssignmentExecutionRequest) bool {
	return !req.ScenarioTools.ContextOnly && len(req.ScenarioTools.AllowedTools) > 0
}

// toolCallingGate persists the first successful governed probe per variant
// before any tool-bearing cell executes. Resumes reuse the same sealed record.
func (c *CampaignController) toolCallingGate(ctx context.Context, run *evalv1.EvaluationRun, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	prober, enabled := c.executor.(CampaignToolCallingProber)
	if !enabled || !req.Binding.ToolCallingProbeRequired || !assignmentDeclaresTools(req) {
		return nil, nil
	}
	variant, err := assignmentToolCallingVariant(req)
	if err != nil {
		return nil, err
	}
	var frozen *evalv1.ModelToolCallingProbe
	for _, record := range run.GetToolCallingProbes() {
		if record.GetVariantId() == variant.GetVariantId() {
			if frozen != nil {
				return nil, fmt.Errorf("evaluation: duplicate tool probe: %w", constants.ErrEvidenceScopeMismatch)
			}
			frozen = record
		}
	}
	if frozen == nil {
		evidence, err := prober.ProbeToolCalling(ctx, req)
		if err != nil {
			return nil, err
		}
		if err := verifyToolCallingProbeEvidence(run.GetRunId(), req.Binding.ModelRegistryDigest, variant, evidence); err != nil {
			return nil, err
		}
		reference, err := c.store.SaveProtoArtifact(ctx, EvidenceScope{
			RunID: run.GetRunId(), ScenarioID: toolCallingProbeScenario,
			AttemptID: evidence.GetRequest().GetProviderAttemptId(), TransactionID: evidence.GetResponse().GetTransactionId(),
		}, complianceevidence.ArtifactTypeEvalExchange, evidence)
		if err != nil {
			return nil, err
		}
		frozen = &evalv1.ModelToolCallingProbe{VariantId: variant.GetVariantId(), Observation: toolCallingObservation(evidence.GetResponse().GetResult()), EvidenceRef: reference}
		run.ToolCallingProbes = append(run.ToolCallingProbes, frozen)
		if err := c.store.SaveRun(ctx, run); err != nil {
			return nil, err
		}
	} else if err := verifyFrozenToolCallingProbe(ctx, c.store, run, variant, frozen); err != nil {
		return nil, err
	}
	if frozen.GetObservation().GetOutcome() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL {
		return toolCallingExclusionResult(req.Assignment, frozen.GetEvidenceRef(), timestamppb.New(c.now().UTC()))
	}
	return nil, nil
}

type toolCallingProbeStore interface {
	LoadToolCallingProbeEvidence(context.Context, string, *compliancev1.ComplianceEvidenceReference) (*evalv1.ModelToolCallingProbeEvidence, error)
}

func (s *Store) LoadToolCallingProbeEvidence(ctx context.Context, runID string, ref *compliancev1.ComplianceEvidenceReference) (*evalv1.ModelToolCallingProbeEvidence, error) {
	if ref.GetArtifactType() != string(complianceevidence.ArtifactTypeEvalExchange) || ref.GetScenarioId() != toolCallingProbeScenario {
		return nil, fmt.Errorf("evaluation: tool probe reference: %w", constants.ErrEvidenceScopeMismatch)
	}
	evidence := &evalv1.ModelToolCallingProbeEvidence{}
	if err := s.loadProtoArtifact(ctx, runID, ref, evidence); err != nil {
		return nil, err
	}
	if ref.GetAttemptId() != evidence.GetRequest().GetProviderAttemptId() || ref.GetTransactionId() != evidence.GetResponse().GetTransactionId() {
		return nil, fmt.Errorf("evaluation: tool probe reference scope: %w", constants.ErrEvidenceScopeMismatch)
	}
	return evidence, nil
}

func verifyFrozenToolCallingProbe(ctx context.Context, store toolCallingProbeStore, run *evalv1.EvaluationRun, variant *evalv1.ModelVariant, record *evalv1.ModelToolCallingProbe) error {
	evidence, err := store.LoadToolCallingProbeEvidence(ctx, run.GetRunId(), record.GetEvidenceRef())
	if err != nil {
		return err
	}
	if record.GetVariantId() != variant.GetVariantId() {
		return constants.ErrEvidenceScopeMismatch
	}
	if err := verifyToolCallingProbeEvidence(run.GetRunId(), run.GetCampaignBinding().GetModelRegistryDigest(), variant, evidence); err != nil {
		return err
	}
	if !proto.Equal(record.GetObservation(), toolCallingObservation(evidence.GetResponse().GetResult())) {
		return fmt.Errorf("evaluation: tool probe observation differs from signed output: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	return nil
}

func verifyToolCallingProbeEvidence(runID, registryDigest string, variant *evalv1.ModelVariant, evidence *evalv1.ModelToolCallingProbeEvidence) error {
	request, response := evidence.GetRequest(), evidence.GetResponse()
	result, receipt := response.GetResult(), response.GetReceipt()
	if runID == "" || registryDigest == "" || variant == nil || variant.GetVariantId() == "" || variant.GetServedModelTag() == "" || variant.GetModelDigest() == "" ||
		evidence.GetVariantId() != variant.GetVariantId() || request == nil || result == nil || receipt == nil || evidence.GetAnchor().GetRuntimeId() != evidence.GetInferenceOperatorId() || evidence.GetInferenceOperatorId() == "" || evidence.GetInferenceSessionId() == "" {
		return fmt.Errorf("evaluation: tool probe evidence: %w", constants.ErrMissingRequiredField)
	}
	if request.GetRunId() != runID || result.GetRunId() != runID || request.GetScenarioId() != toolCallingProbeScenario || result.GetScenarioId() != toolCallingProbeScenario ||
		!strings.HasPrefix(request.GetProviderAttemptId(), toolCallingProbeScenario) || request.GetProviderAttemptId() != result.GetProviderAttemptId() ||
		request.GetAssignmentId() == "" || request.GetAssignmentId() != result.GetAssignmentId() || request.GetEvaluationAttemptId() == "" || request.GetEvaluationAttemptId() != request.GetProviderAttemptId() || request.GetEvaluationAttemptId() != result.GetEvaluationAttemptId() ||
		request.GetCampaignId() == "" || request.GetCampaignId() != result.GetCampaignId() || request.GetModelRegistryDigest() != registryDigest || result.GetModelRegistryDigest() != registryDigest ||
		request.GetModel() != variant.GetServedModelTag() || request.GetModelDigest() != variant.GetModelDigest() || result.GetRequestedModel() != variant.GetServedModelTag() || result.GetModel() != variant.GetServedModelTag() || result.GetRequestedModelDigest() != variant.GetModelDigest() || result.GetServedModelDigest() != variant.GetModelDigest() ||
		request.GetTargetOperatorSessionId() != evidence.GetInferenceSessionId() || response.GetTransactionId() == "" || response.GetTransactionId() != receipt.GetTransactionId() || receipt.GetActionType() != string(constants.ActionTypeInference) {
		return fmt.Errorf("evaluation: tool probe identity: %w", constants.ErrEvidenceScopeMismatch)
	}
	probe := InferenceProbeRequest{}
	configureToolCallingProbe(&probe)
	expected, err := BuildInferenceProbeDispatchRequest(InferenceProbeRequest{
		ProviderAttemptID: request.GetProviderAttemptId(), Model: request.GetModel(), ModelDigest: request.GetModelDigest(),
		Role: models.InferenceModelRoleFromProto(request.GetRole()), TargetOperatorSessionID: request.GetTargetOperatorSessionId(),
		Prompt: probe.Prompt, Tools: probe.Tools, ToolChoice: probe.ToolChoice,
		CampaignID: request.GetCampaignId(), ModelRegistryDigest: registryDigest, ModelRegistry: request.GetModelRegistry(),
	})
	if err != nil {
		return err
	}
	expected.RunId, expected.AssignmentId, expected.EvaluationAttemptId, expected.ScenarioId = request.GetRunId(), request.GetAssignmentId(), request.GetEvaluationAttemptId(), toolCallingProbeScenario
	if !proto.Equal(request, expected) {
		return fmt.Errorf("evaluation: tool probe request: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	if err != nil {
		return err
	}
	if digest != result.GetResultDigest() || digest != receipt.GetResultSummary() {
		return constants.ErrInferenceResultDigest
	}
	anchors, err := oracle.NewAnchorSet([]*evalv1.ConformanceAnchor{evidence.GetAnchor()})
	if err != nil {
		return err
	}
	outcome, err := verifiedOutcome(anchors, evidence.GetInferenceOperatorId(), receipt)
	if err != nil {
		return err
	}
	if outcome.Kind != oracle.OutcomeExecuted {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	return verifyReceiptSession(receipt, evidence.GetInferenceSessionId())
}

func toolCallingExclusionResult(assignment *evalv1.EvaluationAssignment, reference *compliancev1.ComplianceEvidenceReference, completedAt *timestamppb.Timestamp) (*evalv1.EvaluationAssignmentResult, error) {
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion: CampaignSchemaVersion, AssignmentId: assignment.GetAssignmentId(), RunId: assignment.GetRunId(), CampaignId: assignment.GetCampaignId(), Lane: assignment.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE,
		CompletedAt:     completedAt, FailureReason: toolCallingProbeFailure, PublicFailureReason: toolCallingProbeFailure,
		EvidenceRefs: []*compliancev1.ComplianceEvidenceReference{reference},
		DeterministicGrades: []*evalv1.DeterministicGrade{{GradeId: assignment.GetAssignmentId() + ":" + toolCallingProbeCriterion, CriterionId: toolCallingProbeCriterion,
			Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, Detail: toolCallingProbeFailure, Basis: basisStructural}},
	}
	digest, err := ComputeAssignmentResultDigest(result)
	result.ResultDigest = digest
	return result, err
}
