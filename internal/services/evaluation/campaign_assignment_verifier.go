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
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
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
	failures = append(failures, verifyAssignmentJudge(req, req.Result)...)
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

// verifyAssignmentJudge binds every stored semantic grade and grader call to
// the campaign's frozen judge, so a result graded by any other model, the
// subject included, does not verify, and checks each grader call's signed
// evidence. A judged result from a campaign without a frozen judge does not
// verify.
func verifyAssignmentJudge(req CampaignAssignmentVerificationRequest, result *evalv1.EvaluationAssignmentResult) []string {
	if req.Binding.JudgeVariant == nil {
		if len(result.GetSemanticGrades()) > 0 || len(result.GetGraderCalls()) > 0 {
			return []string{"judged result has no frozen campaign judge"}
		}
		return nil
	}
	judgeID := req.Binding.JudgeVariant.GetVariantId()
	var failures []string
	for _, grade := range result.GetSemanticGrades() {
		if grade.GetJudgeVariantId() != judgeID {
			failures = append(failures, fmt.Sprintf("semantic grade %s judged by %q, not the frozen judge %q", grade.GetGradeId(), grade.GetJudgeVariantId(), judgeID))
		}
	}
	for _, call := range result.GetGraderCalls() {
		if call.GetJudgeVariantId() != judgeID {
			failures = append(failures, fmt.Sprintf("grader call %s made by %q, not the frozen judge %q", call.GetGraderCallId(), call.GetJudgeVariantId(), judgeID))
		}
	}
	return append(failures, verifyGraderCalls(req, result)...)
}

// verifyGraderCalls proves each grader call is the governed judge inference
// behind exactly one evidence-bound semantic grade: its receipt verifies
// against the trace's pinned Inference anchors, its signed result answers the
// recorded dispatch, and the verdict decoded from that result is the grade's.
func verifyGraderCalls(req CampaignAssignmentVerificationRequest, result *evalv1.EvaluationAssignmentResult) []string {
	grades := make(map[string]*evalv1.SemanticGrade)
	for _, grade := range result.GetSemanticGrades() {
		if grade.GetGraderCallRef() != nil {
			grades[grade.GetCriterionId()] = grade
		}
	}
	if len(result.GetGraderCalls()) == 0 && len(grades) == 0 {
		return nil
	}
	if req.AgentTrace == nil {
		return []string{"grader calls require the persisted agent trace"}
	}
	cfg, err := campaignJudgeConfig(AssignmentExecutionRequest{Assignment: req.Assignment, Binding: req.Binding}, nil)
	if err != nil {
		return []string{"judge config: " + err.Error()}
	}
	receipts := newJudgeReceiptCheck(req.AgentTrace)
	var failures []string
	seen := make(map[string]bool, len(grades))
	for _, call := range result.GetGraderCalls() {
		grade := grades[call.GetCriterionId()]
		if grade == nil || seen[call.GetCriterionId()] {
			failures = append(failures, fmt.Sprintf("grader call %s names no evidence-bound semantic grade or repeats %q", call.GetGraderCallId(), call.GetCriterionId()))
			continue
		}
		seen[call.GetCriterionId()] = true
		if err := verifyGraderCall(cfg, receipts, req.AgentTrace, call, grade); err != nil {
			failures = append(failures, fmt.Sprintf("grader call %s: %v", call.GetGraderCallId(), err))
		}
	}
	for criterionID, grade := range grades {
		if !seen[criterionID] {
			failures = append(failures, fmt.Sprintf("semantic grade %s cites a grader call it does not have", grade.GetGradeId()))
		}
	}
	return failures
}

func verifyGraderCall(cfg judge.Config, receipts judgeReceiptCheck, trace *AgentTrace, call *evalv1.GraderModelCallRecord, grade *evalv1.SemanticGrade) error {
	if call.GetProviderAttemptId() == "" || call.GetTransactionId() == "" || call.GetInferenceResult() == nil || call.GetReceipt() == nil {
		return fmt.Errorf("signed judge evidence: %w", constants.ErrMissingRequiredField)
	}
	if err := receipts.verify(judge.Score{Status: judge.StatusScored, TransactionID: call.GetTransactionId(), Receipt: call.GetReceipt(), Result: call.GetInferenceResult()}); err != nil {
		return err
	}
	if criterionID, ok := strings.CutPrefix(call.GetCriterionId(), contentCriterionPrefix); ok {
		return verifyCriterionGraderCall(cfg, trace, criterionID, call, grade)
	}
	return verifyQualityGraderCall(cfg, call, grade)
}

// verifyCriterionGraderCall binds a criterion grader call to the verdict the
// trace sealed for that criterion and to the grade read from it.
func verifyCriterionGraderCall(cfg judge.Config, trace *AgentTrace, criterionID string, call *evalv1.GraderModelCallRecord, grade *evalv1.SemanticGrade) error {
	judgment, ok := trace.contentJudgment(criterionID)
	if !ok || judgment.ProviderAttemptID != call.GetProviderAttemptId() || judgment.TransactionID != call.GetTransactionId() || judgment.RubricVersion != call.GetRubricVersion() {
		return fmt.Errorf("criterion %q is not the judgment sealed in the trace: %w", criterionID, constants.ErrEvaluationContentJudgmentMismatch)
	}
	met, rationale, err := judge.VerifyCriterionResult(cfg, call.GetProviderAttemptId(), call.GetInferenceResult(), call.GetReceipt())
	if err != nil {
		return err
	}
	passed := grade.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
	if met != passed || rationale != grade.GetDetail() {
		return fmt.Errorf("criterion %q verdict does not match the signed result: %w", criterionID, constants.ErrEvaluationContentJudgmentMismatch)
	}
	return nil
}

// verifyQualityGraderCall checks a quality grade against the signed result: a
// decoded score is the grade's score and verdict, and a result that does not
// answer as the rubric requires is a failed judgment.
func verifyQualityGraderCall(cfg judge.Config, call *evalv1.GraderModelCallRecord, grade *evalv1.SemanticGrade) error {
	if call.GetCriterionId() != diagnosisQualityCriterionID && call.GetCriterionId() != memoryQualityCriterionID {
		return fmt.Errorf("unknown judged criterion %q: %w", call.GetCriterionId(), constants.ErrEvaluationContentJudgmentMismatch)
	}
	if call.GetRubricVersion() != judge.RubricVersion {
		return fmt.Errorf("rubric %q is not %q: %w", call.GetRubricVersion(), judge.RubricVersion, constants.ErrEvaluationContentJudgmentMismatch)
	}
	value, rationale, err := judge.VerifyQualityResult(cfg, call.GetProviderAttemptId(), call.GetInferenceResult(), call.GetReceipt())
	status := grade.GetStatus()
	switch {
	case err != nil:
		if status != evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL || !strings.HasPrefix(grade.GetDetail(), "judge failed: ") {
			return fmt.Errorf("a failed judgment is graded %s: %w", status, constants.ErrEvaluationContentJudgmentMismatch)
		}
	case !strings.HasPrefix(grade.GetDetail(), qualityGradeDetail(value, call.GetRubricVersion(), rationale)):
		return fmt.Errorf("grade detail does not record the signed score: %w", constants.ErrEvaluationContentJudgmentMismatch)
	case value < 3 && status != evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		value >= 3 && status == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		status == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE && call.GetCriterionId() != diagnosisQualityCriterionID:
		return fmt.Errorf("grade %s does not follow from the signed score %d: %w", status, value, constants.ErrEvaluationContentJudgmentMismatch)
	}
	return nil
}

// verifyHomogeneousAssignment binds a model-role result to its persisted agent
// trace and recomputes lifecycle, trajectory, and grades from that trace.
func (v *CampaignAssignmentVerifier) verifyHomogeneousAssignment(req CampaignAssignmentVerificationRequest) []string {
	trace := req.AgentTrace
	if trace == nil {
		return verifyExecutorFailureResult(req.Assignment, req.Result)
	}
	failures := make([]string, 0)
	if err := ValidateAgentTraceDigest(trace); err != nil {
		return append(failures, "agent trace digest validation failed: "+err.Error())
	}
	if trace.RunID != req.Assignment.GetRunId() || trace.AssignmentID != req.Assignment.GetAssignmentId() {
		return append(failures, fmt.Sprintf("agent trace binding mismatch: %v", constants.ErrEvaluationEvidenceTraceMismatch))
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
	if trace.CarriesModelIdentity() {
		expected, _, err := modelInferenceRecordsFromAgentTrace(execReq, trace, func(string) string { return "" })
		if err != nil {
			return append(failures, "agent trace model inference records failed: "+err.Error())
		}
		if len(expected) != len(req.Result.GetModelInferences()) {
			failures = append(failures, "result inference count does not match agent trace")
		} else {
			for i, record := range req.Result.GetModelInferences() {
				if record == nil {
					failures = append(failures, fmt.Sprintf("result inference %d is missing", i))
					continue
				}
				// Record IDs and governed receipt references are assigned after
				// import. The execution identity and telemetry must be reproducible.
				actual := proto.Clone(record).(*evalv1.ModelInferenceRecord)
				actual.InferenceRecordId = ""
				actual.GovernedReceiptRef = nil
				if !proto.Equal(actual, expected[i]) {
					failures = append(failures, fmt.Sprintf("result inference %d does not match agent trace", i))
				}
			}
		}
	}
	// The lifecycle is recomputed from the evidence rules and grader verdict the
	// live import applies, so a stored INVALID_EVIDENCE from any of them is
	// recomputed and accepted here, and a trace that fails them under any other
	// stored lifecycle does not verify. The verdict is recomputed under any
	// catalog: only the grade comparison depends on CatalogRecomputesGrades.
	lifecycle := agentTraceLifecycle(execReq, trace)
	assessment := assessAgentTrace(execReq, trace, lifecycle, agentTraceEvidenceError(execReq, trace))
	if assessment.invalid != nil {
		lifecycle = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE
	}
	if req.Result.GetLifecycleStatus() != lifecycle {
		failures = append(failures, fmt.Sprintf("stored lifecycle %s does not match agent trace lifecycle %s", req.Result.GetLifecycleStatus(), lifecycle))
	}
	if assessment.invalid != nil || !CatalogRecomputesGrades(req.CatalogRef) {
		return failures
	}
	if assessment.failed != nil {
		return append(failures, "homogeneous grade recomputation failed: "+assessment.failed.Error())
	}
	grading := assessment.grading
	if !gradesEquivalent(req.Result.GetDeterministicGrades(), grading.DeterministicGrades) {
		failures = append(failures, "stored deterministic grades do not match agent trace recomputation")
	}
	if !contentGradesMatch(req.Result.GetSemanticGrades(), grading.SemanticGrades) {
		failures = append(failures, "stored judged content grades do not match the verdicts sealed in the agent trace")
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
	if !trace.CarriesModelIdentity() {
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
	designated, err := AssignmentDesignatedRole(req.Assignment)
	if err != nil {
		return err
	}
	variant := resolved[designated]
	label, err := modelCampaignRoleLabel(designated)
	if err != nil || variant == nil || trace.DesignatedRole != label || trace.ServedModelTag != variant.GetServedModelTag() || trace.ModelDigest != variant.GetModelDigest() {
		return constants.ErrFormationRegistryBinding
	}
	for _, call := range trace.ModelCalls {
		role := designated
		switch {
		case call.Purpose == agentModelCallPurposeInterrogation:
			if system {
				role = formationInterrogatorRole
			}
		case !call.IsTurn():
			if !strings.HasPrefix(call.Purpose, agentModelCallPurposeTribunalPrefix) {
				return fmt.Errorf("unknown model call purpose %q: %w", call.Purpose, constants.ErrEvaluationEvidenceTraceMismatch)
			}
			if system {
				role = formationTribunalRoleForTrace(trace, call.Purpose)
				if role == evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED {
					return fmt.Errorf("unknown Tribunal stage purpose %q: %w", call.Purpose, constants.ErrEvaluationEvidenceTraceMismatch)
				}
			}
		}
		model := resolved[role]
		inferenceRole, err := tribunal.InferenceRoleForTier(role)
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
