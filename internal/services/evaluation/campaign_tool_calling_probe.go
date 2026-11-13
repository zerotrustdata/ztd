// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	toolCallingProbeScenario     = "tool-calling-probe"
	toolCallingProbeCriterion    = "tool-calling-capability"
	toolCallingProbeFailure      = "capability_unsupported: tool-call probe did not return a structured probe_echo call"
	toolCallingProbeRejected     = "capability_unsupported: provider rejected tool calling (tools_unsupported)"
	toolCallingUnsupportedDetail = "provider rejected tool-calling probe"
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
	if err := e.releaseAllExcept(ctx, req, "tool-calling probe", run.modelTags()); err != nil {
		return nil, toolCallingProbeError("residency", err)
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
		// A provider that rejects tools has answered the capability question.
		// Its signed FAILED receipt is the evidence, so the rejection becomes a
		// verified negative probe. Any other dispatch failure says nothing about
		// the model and is left to fail only the assignment that triggered it.
		receipt := governance.FailedReceipt(err)
		if !errors.Is(err, constants.ErrInferenceToolsUnsupported) || receipt == nil {
			return nil, toolCallingDispatchError(err)
		}
		response = &operatorv1.InferenceDispatchResponse{TransactionId: receipt.GetTransactionId(), Receipt: receipt}
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

// toolCallingProbeError marks a probe residency or evidence failure. These stop
// the run: unreleased models or unverifiable evidence are not per-cell faults.
func toolCallingProbeError(scope string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %w", constants.ErrEvaluationToolCallingProbeFailed, scope, err)
}

// toolCallingDispatchError marks a probe dispatch that produced no signed
// verdict. It is recoverable: the assignment that triggered the probe fails
// with this cause, no probe record is frozen, and the run moves on.
func toolCallingDispatchError(err error) error {
	return fmt.Errorf("%w: tool-calling probe dispatch: %w", constants.ErrEvaluationAssignmentExecutionFailed, err)
}

// toolCallingProbeExcludes reports whether a verified probe verdict keeps the
// variant out of tool-bearing cells. A FAIL is a model that did not emit the
// structured call; UNSUPPORTED is a provider that rejected tools outright.
func toolCallingProbeExcludes(outcome evalv1.EvaluationVerdictStatus) bool {
	return outcome == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL ||
		outcome == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED
}

// toolCallingProbeObservation derives the verdict from verified probe evidence.
// A signed TOOLS_UNSUPPORTED rejection carries no result and is UNSUPPORTED;
// any other verified exchange is judged from its result.
func toolCallingProbeObservation(evidence *evalv1.ModelToolCallingProbeEvidence) *evalv1.ModelCapabilityObservation {
	if evidence.GetResponse().GetResult() == nil {
		return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED, toolCallingUnsupportedDetail)
	}
	return toolCallingObservation(evidence.GetResponse().GetResult())
}

// isToolsUnsupportedReceipt reports whether receipt is the signed FAILED
// receipt of a provider's tool rejection, the only failed probe shape that
// is evidence.
func isToolsUnsupportedReceipt(receipt *operatorv1.EventReceipt) bool {
	return receipt.GetStatus() == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED &&
		receipt.GetFailureCode() == operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_TOOLS_UNSUPPORTED
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
		frozen = &evalv1.ModelToolCallingProbe{VariantId: variant.GetVariantId(), Observation: toolCallingProbeObservation(evidence), EvidenceRef: reference}
		run.ToolCallingProbes = append(run.ToolCallingProbes, frozen)
		if err := c.store.SaveRun(ctx, run); err != nil {
			return nil, err
		}
	} else if err := verifyFrozenToolCallingProbe(ctx, c.store, run, variant, frozen); err != nil {
		return nil, err
	}
	if toolCallingProbeExcludes(frozen.GetObservation().GetOutcome()) {
		return toolCallingExclusionResult(req.Assignment, frozen.GetObservation(), frozen.GetEvidenceRef(), timestamppb.New(c.now().UTC()))
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
	if !proto.Equal(record.GetObservation(), toolCallingProbeObservation(evidence)) {
		return fmt.Errorf("evaluation: tool probe observation differs from signed output: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	return nil
}

func verifyToolCallingProbeEvidence(runID, registryDigest string, variant *evalv1.ModelVariant, evidence *evalv1.ModelToolCallingProbeEvidence) error {
	request, response := evidence.GetRequest(), evidence.GetResponse()
	result, receipt := response.GetResult(), response.GetReceipt()
	// A result-less exchange is only a provider's tool rejection, and only its
	// signed FAILED receipt can prove that.
	rejected := result == nil && isToolsUnsupportedReceipt(receipt)
	if runID == "" || registryDigest == "" || variant == nil || variant.GetVariantId() == "" || variant.GetServedModelTag() == "" || variant.GetModelDigest() == "" ||
		evidence.GetVariantId() != variant.GetVariantId() || request == nil || (result == nil && !rejected) || receipt == nil || evidence.GetAnchor().GetRuntimeId() != evidence.GetInferenceOperatorId() || evidence.GetInferenceOperatorId() == "" || evidence.GetInferenceSessionId() == "" {
		return fmt.Errorf("evaluation: tool probe evidence: %w", constants.ErrMissingRequiredField)
	}
	if request.GetRunId() != runID || request.GetScenarioId() != toolCallingProbeScenario ||
		!strings.HasPrefix(request.GetProviderAttemptId(), toolCallingProbeScenario) ||
		request.GetAssignmentId() == "" || request.GetEvaluationAttemptId() == "" || request.GetEvaluationAttemptId() != request.GetProviderAttemptId() ||
		request.GetCampaignId() == "" || request.GetModelRegistryDigest() != registryDigest ||
		request.GetModel() != variant.GetServedModelTag() || request.GetModelDigest() != variant.GetModelDigest() ||
		request.GetTargetOperatorSessionId() != evidence.GetInferenceSessionId() || response.GetTransactionId() == "" || response.GetTransactionId() != receipt.GetTransactionId() || receipt.GetActionType() != string(constants.ActionTypeInference) {
		return fmt.Errorf("evaluation: tool probe identity: %w", constants.ErrEvidenceScopeMismatch)
	}
	if result != nil && !toolCallingResultMatchesRequest(runID, registryDigest, variant, request, result) {
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
	if result != nil {
		digest, err := models.ComputeInferenceResultDigest(result)
		if err != nil {
			return err
		}
		if digest != result.GetResultDigest() || digest != receipt.GetResultSummary() {
			return constants.ErrInferenceResultDigest
		}
	}
	anchors, err := oracle.NewAnchorSet([]*evalv1.ConformanceAnchor{evidence.GetAnchor()})
	if err != nil {
		return err
	}
	outcome, err := verifiedOutcome(anchors, evidence.GetInferenceOperatorId(), receipt)
	if err != nil {
		return err
	}
	// The rejection is an L5 execution failure: the Operator handled the
	// request and committed it, so it verifies as an executed-then-failed outcome.
	wantKind := oracle.OutcomeExecuted
	if rejected {
		wantKind = oracle.OutcomeExecutionFailed
	}
	if outcome.Kind != wantKind {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	return verifyReceiptSession(receipt, evidence.GetInferenceSessionId())
}

// toolCallingResultMatchesRequest checks that a probe's result belongs to the
// same run, scenario, attempt and frozen model as its request.
func toolCallingResultMatchesRequest(runID, registryDigest string, variant *evalv1.ModelVariant, request *operatorv1.InferenceDispatchRequest, result *operatorv1.InferenceResult) bool {
	return result.GetRunId() == runID && result.GetScenarioId() == toolCallingProbeScenario &&
		result.GetProviderAttemptId() == request.GetProviderAttemptId() &&
		result.GetAssignmentId() == request.GetAssignmentId() && result.GetEvaluationAttemptId() == request.GetEvaluationAttemptId() &&
		result.GetCampaignId() == request.GetCampaignId() && result.GetModelRegistryDigest() == registryDigest &&
		result.GetRequestedModel() == variant.GetServedModelTag() && result.GetModel() == variant.GetServedModelTag() &&
		result.GetRequestedModelDigest() == variant.GetModelDigest() && result.GetServedModelDigest() == variant.GetModelDigest()
}

// isToolCallingExclusionResult reports whether result is a cell the run's
// verified tool-calling probe excluded, as built by toolCallingExclusionResult.
func isToolCallingExclusionResult(result *evalv1.EvaluationAssignmentResult) bool {
	if result.GetLifecycleStatus() != evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE {
		return false
	}
	for _, grade := range result.GetDeterministicGrades() {
		if grade.GetCriterionId() == toolCallingProbeCriterion && grade.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE {
			return true
		}
	}
	return false
}

func toolCallingExclusionResult(assignment *evalv1.EvaluationAssignment, observation *evalv1.ModelCapabilityObservation, reference *compliancev1.ComplianceEvidenceReference, completedAt *timestamppb.Timestamp) (*evalv1.EvaluationAssignmentResult, error) {
	reason := toolCallingProbeFailure
	if observation.GetOutcome() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED {
		reason = toolCallingProbeRejected
	}
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion: CampaignSchemaVersion, AssignmentId: assignment.GetAssignmentId(), RunId: assignment.GetRunId(), CampaignId: assignment.GetCampaignId(), Lane: assignment.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE,
		CompletedAt:     completedAt, FailureReason: reason, PublicFailureReason: reason,
		EvidenceRefs: []*compliancev1.ComplianceEvidenceReference{reference},
		DeterministicGrades: []*evalv1.DeterministicGrade{{GradeId: assignment.GetAssignmentId() + ":" + toolCallingProbeCriterion, CriterionId: toolCallingProbeCriterion,
			Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, Detail: reason, Basis: basisStructural}},
	}
	digest, err := ComputeAssignmentResultDigest(result)
	result.ResultDigest = digest
	return result, err
}
