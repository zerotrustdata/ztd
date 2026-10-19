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
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// CampaignAssignmentVerificationRequest carries persisted assignment state for
// read-only independent verification.
type CampaignAssignmentVerificationRequest struct {
	Assignment                *evalv1.EvaluationAssignment
	Result                    *evalv1.EvaluationAssignmentResult
	ScenarioInput             ScenarioInputFixture
	ScenarioGold              ScenarioGoldCriteria
	ScenarioTools             ScenarioToolExpectations
	GradingMethod             evalv1.EvaluationGradingMethod
	AgentTrace                *AgentTrace
	CatalogRef                *compliancev1.VersionedReference
	ProviderObservationReader *CampaignProviderObservationReader
	ProviderObservationPolicy ProviderObservationPolicy
	ModelProvenanceReader     *CampaignModelProvenanceReader
	ModelProvenancePolicy     ModelProvenancePolicy
	Binding                   CampaignExecutionBinding
	FormationStackSet         *HeterogeneousStackSet
}

// CampaignAssignmentVerifier independently verifies one persisted assignment
// result without invoking inference, providers, tools, or mutation.
type CampaignAssignmentVerifier struct {
	now func() time.Time
}

func NewCampaignAssignmentVerifier(now func() time.Time) *CampaignAssignmentVerifier {
	if now == nil {
		now = time.Now
	}
	return &CampaignAssignmentVerifier{now: now}
}

// Verify recomputes assignment digests and deterministic grades from declared
// evidence and returns a typed verification report.
func (v *CampaignAssignmentVerifier) Verify(ctx context.Context, req CampaignAssignmentVerificationRequest) (*evalv1.EvaluationVerificationReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	verifiedAt := v.now().UTC()
	report := &evalv1.EvaluationVerificationReport{
		SchemaVersion: CampaignSchemaVersion,
		ReportId:      req.Result.GetAssignmentId(),
		RunId:         req.Result.GetRunId(),
		AssignmentId:  req.Result.GetAssignmentId(),
		VerifiedAt:    timestamppb.New(verifiedAt),
	}
	failures := make([]string, 0)
	if req.Assignment == nil || req.Result == nil {
		failures = append(failures, "assignment and result are required")
		return finalizeCampaignVerificationReport(report, failures), nil
	}
	if req.Assignment.GetAssignmentId() != req.Result.GetAssignmentId() || req.Assignment.GetRunId() != req.Result.GetRunId() || req.Assignment.GetCampaignId() != req.Result.GetCampaignId() || req.Assignment.GetLane() != req.Result.GetLane() {
		failures = append(failures, "assignment and result binding mismatch")
	}
	if err := ValidateAssignmentResultDigest(req.Result); err != nil {
		failures = append(failures, "result digest validation failed: "+err.Error())
	}
	failures = append(failures, v.verifyHomogeneousAssignment(req)...)
	if req.ProviderObservationReader != nil && len(scoredModelInferences(req.Result)) > 0 {
		observationFailures, _ := req.ProviderObservationReader.VerifyAssignmentProviderObservations(ctx, req.Result, req.ProviderObservationPolicy)
		failures = append(failures, observationFailures...)
	}
	if req.ModelProvenanceReader != nil && len(scoredModelInferences(req.Result)) > 0 {
		provenanceFailures, _ := req.ModelProvenanceReader.VerifyAssignmentModelProvenance(ctx, req.Result, req.ModelProvenancePolicy)
		failures = append(failures, provenanceFailures...)
	}
	return finalizeCampaignVerificationReport(report, failures), nil
}

// verifyHomogeneousAssignment binds a model-role result to its persisted agent
// trace and recomputes lifecycle, trajectory, and grades from that trace.
func (v *CampaignAssignmentVerifier) verifyHomogeneousAssignment(req CampaignAssignmentVerificationRequest) []string {
	trace := req.AgentTrace
	if trace == nil {
		return []string{"agent trace is required for homogeneous verification"}
	}
	failures := make([]string, 0)
	if err := ValidateAgentTraceDigest(trace); err != nil {
		return append(failures, "agent trace digest validation failed: "+err.Error())
	}
	if trace.RunID != req.Assignment.GetRunId() || trace.AssignmentID != req.Assignment.GetAssignmentId() {
		return append(failures, fmt.Sprintf("agent trace binding mismatch: %v", constants.ErrEvaluationEvidenceTraceMismatch))
	}
	if err := VerifyAgentTraceEvidence(trace); err != nil {
		failures = append(failures, "agent trace evidence validation failed: "+err.Error())
	}
	body, err := MarshalCanonicalJSONObject(trace)
	if err != nil {
		return append(failures, "agent trace canonicalization failed: "+err.Error())
	}
	if !resultReferencesEvidenceBody(req.Result, body) {
		failures = append(failures, "result does not reference the persisted agent trace evidence")
	}
	if len(req.Result.GetToolCalls()) != len(trace.ToolCalls) {
		failures = append(failures, fmt.Sprintf("result records %d tool calls, agent trace records %d", len(req.Result.GetToolCalls()), len(trace.ToolCalls)))
	} else {
		for i, call := range trace.ToolCalls {
			record := req.Result.GetToolCalls()[i]
			if record.GetCallId() != call.CallID || record.GetToolName() != call.ToolName || record.GetArgumentsHash() != agentArgumentsHash(call.ArgumentsJSON) {
				failures = append(failures, fmt.Sprintf("tool call %d does not match agent trace", i))
			}
		}
	}
	execReq := AssignmentExecutionRequest{
		Assignment:        req.Assignment,
		AttemptID:         trace.EvaluationAttemptID,
		GradingMethod:     req.GradingMethod,
		ScenarioInput:     req.ScenarioInput,
		ScenarioGold:      req.ScenarioGold,
		ScenarioTools:     req.ScenarioTools,
		Binding:           req.Binding,
		FormationStackSet: req.FormationStackSet,
	}
	if err := verifyAssignmentTraceModels(execReq, trace); err != nil {
		failures = append(failures, "agent trace model binding failed: "+err.Error())
	}
	if trace.SchemaVersion == agentTraceSchemaVersion {
		expected, _ := modelInferenceRecordsFromAgentTrace(execReq, trace, func(string) string { return "" })
		if len(expected) != len(req.Result.GetModelInferences()) {
			failures = append(failures, "result inference count does not match agent trace")
		} else {
			for i, record := range req.Result.GetModelInferences() {
				if record == nil {
					failures = append(failures, fmt.Sprintf("result inference %d is missing", i))
					continue
				}
				// Record IDs and external observation references are assigned after
				// import. The execution identity and telemetry must be reproducible.
				actual := proto.Clone(record).(*evalv1.ModelInferenceRecord)
				actual.InferenceRecordId = ""
				actual.GovernedReceiptRef = nil
				actual.ProviderBoundaryObservationRef = nil
				if !proto.Equal(actual, expected[i]) {
					failures = append(failures, fmt.Sprintf("result inference %d does not match agent trace", i))
				}
			}
		}
	}
	lifecycle := agentTraceLifecycle(execReq, trace)
	if req.Result.GetLifecycleStatus() != lifecycle {
		failures = append(failures, fmt.Sprintf("stored lifecycle %s does not match agent trace lifecycle %s", req.Result.GetLifecycleStatus(), lifecycle))
	}
	if !CatalogRecomputesGrades(req.CatalogRef) {
		return failures
	}
	grading, err := GradeHomogeneousScenario(execReq, trace, lifecycle)
	if err != nil {
		return append(failures, "homogeneous grade recomputation failed: "+err.Error())
	}
	if !gradesEquivalent(req.Result.GetDeterministicGrades(), grading.DeterministicGrades) {
		failures = append(failures, "stored deterministic grades do not match agent trace recomputation")
	}
	if req.Result.GetTrajectoryOutcome() != grading.Trajectory.Outcome {
		failures = append(failures, fmt.Sprintf("stored trajectory outcome %s does not match agent trace recomputation %s", req.Result.GetTrajectoryOutcome(), grading.Trajectory.Outcome))
	}
	return failures
}

// verifyAssignmentTraceModels binds the signed identity of each v7 inference
// to the assignment's frozen candidate or formation. A formation cannot use
// older traces, which did not record independently verifiable per-call models.
func verifyAssignmentTraceModels(req AssignmentExecutionRequest, trace *AgentTrace) error {
	if trace == nil || req.Assignment == nil {
		return constants.ErrMissingRequiredField
	}
	if trace.CampaignID != req.Assignment.GetCampaignId() || trace.ScenarioID != req.Assignment.GetScenarioId() {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	system := IsHeterogeneousAssignment(req.Assignment)
	if trace.SchemaVersion != agentTraceSchemaVersion {
		if system {
			return fmt.Errorf("formation requires per-call model evidence: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
		return nil
	}
	var resolved map[evalv1.ModelCampaignRole]*evalv1.ModelVariant
	if system || len(req.Binding.ModelVariants) > 0 {
		var err error
		resolved, err = ResolveAssignmentModels(req)
		if err != nil {
			return err
		}
		if trace.ModelRegistryDigest != req.Binding.ModelRegistryDigest {
			return constants.ErrInferenceModelRegistryInvalid
		}
	} else {
		target := req.Assignment.GetHomogeneous()
		if target == nil || target.GetCandidateVariant() == nil {
			return constants.ErrEvaluationTargetKindMismatch
		}
		resolved = map[evalv1.ModelCampaignRole]*evalv1.ModelVariant{target.GetDesignatedRole(): target.GetCandidateVariant()}
	}
	designated := req.Assignment.GetHomogeneous().GetDesignatedRole()
	if system {
		designated = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
	}
	variant := resolved[designated]
	label, err := modelCampaignRoleLabel(designated)
	if err != nil || variant == nil || trace.DesignatedRole != label || trace.ServedModelTag != variant.GetServedModelTag() || trace.ModelDigest != variant.GetModelDigest() {
		return constants.ErrFormationRegistryBinding
	}
	for _, call := range trace.ModelCalls {
		role := designated
		if !call.IsTurn() {
			if !strings.HasPrefix(call.Purpose, agentModelCallPurposeTribunalPrefix) {
				return fmt.Errorf("unknown model call purpose %q: %w", call.Purpose, constants.ErrEvaluationEvidenceTraceMismatch)
			}
			if system {
				role = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT
			}
		}
		model := resolved[role]
		inferenceRole, err := campaignRoleToInferenceRole(role)
		if err != nil || model == nil || call.ModelVariantID != model.GetVariantId() || call.ModelRole != inferenceRole.ToProto().String() || call.ServedModelTag != model.GetServedModelTag() || call.ModelDigest != model.GetModelDigest() {
			return fmt.Errorf("model call %q differs from frozen role %s: %w", call.ProviderAttemptID, role, constants.ErrFormationRegistryBinding)
		}
	}
	return nil
}

func resultReferencesEvidenceBody(result *evalv1.EvaluationAssignmentResult, body []byte) bool {
	digest := models.SHA256Hex(body)
	for _, ref := range result.GetEvidenceRefs() {
		if ref.GetArtifactType() == string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace) && ref.GetSha256() == digest {
			return true
		}
	}
	return false
}

func finalizeCampaignVerificationReport(report *evalv1.EvaluationVerificationReport, failures []string) *evalv1.EvaluationVerificationReport {
	report.FailureReasons = failures
	report.FailureCount = uint32(len(failures))
	if len(failures) == 0 {
		report.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
		return report
	}
	report.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	return report
}
