// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// IsHeterogeneousAssignment reports whether one assignment targets a frozen
// heterogeneous stack instead of a single homogeneous model-role cell.
func IsHeterogeneousAssignment(assignment *evalv1.EvaluationAssignment) bool {
	if assignment == nil {
		return false
	}
	heterogeneous, ok := assignment.GetTarget().(*evalv1.EvaluationAssignment_Heterogeneous)
	return ok && heterogeneous.Heterogeneous != nil && heterogeneous.Heterogeneous.GetStack() != nil
}

// HeterogeneousStackFromAssignment returns the frozen stack binding for one
// system-lane assignment.
func HeterogeneousStackFromAssignment(assignment *evalv1.EvaluationAssignment) (*evalv1.HeterogeneousStackDefinition, error) {
	if !IsHeterogeneousAssignment(assignment) {
		return nil, fmt.Errorf("evaluation: heterogeneous stack: %w", constants.ErrMissingRequiredField)
	}
	stack := assignment.GetHeterogeneous().GetStack()
	if err := ValidateHeterogeneousStackDigest(stack); err != nil {
		return nil, err
	}
	return stack, nil
}

// CampaignFormationBindingRequest resolves one heterogeneous assignment and the
// frozen registry into a governed formation binding request.
func CampaignFormationBindingRequest(assignment *evalv1.EvaluationAssignment, variants []*evalv1.ModelVariant) (FormationBindingRequest, error) {
	stack, err := HeterogeneousStackFromAssignment(assignment)
	if err != nil {
		return FormationBindingRequest{}, fmt.Errorf("evaluation: campaign formation binding: %w", err)
	}
	return FormationBindingRequest{
		FormationID: stack.GetStackId(),
		Stack:       stack,
		Variants:    variants,
	}, nil
}

// CampaignFormationRunner executes one bound heterogeneous stack through the
// governed FormationRunner production seam.
type CampaignFormationRunner interface {
	RunHeterogeneousFormation(ctx context.Context, binding FormationBindingRequest, runContext FormationRunContext, initialState []byte) (*FormationRunResult, error)
}

type campaignFormationProductionRunner struct {
	deps FormationProductionDependencies
}

// NewCampaignFormationProductionRunner wires governed production dependencies
// into heterogeneous campaign execution.
func NewCampaignFormationProductionRunner(deps FormationProductionDependencies) CampaignFormationRunner {
	return &campaignFormationProductionRunner{deps: deps}
}

func (r *campaignFormationProductionRunner) RunHeterogeneousFormation(ctx context.Context, binding FormationBindingRequest, runContext FormationRunContext, initialState []byte) (*FormationRunResult, error) {
	deps := r.deps
	deps.RunContext = runContext
	return RunHeterogeneousFormationProduction(ctx, binding, deps, initialState)
}

// CampaignFormationExecutor runs heterogeneous system-lane assignments through
// Lite → Assistant → Primary governed formation execution.
type CampaignFormationExecutor struct {
	variants       []*evalv1.ModelVariant
	runner         CampaignFormationRunner
	formationStore CampaignFormationRunStore
	witnessReader  *CampaignFormationWitnessReader
	now            func() time.Time
	newID          func(string) string
}

// NewCampaignFormationExecutor constructs the heterogeneous campaign executor.
func NewCampaignFormationExecutor(variants []*evalv1.ModelVariant, runner CampaignFormationRunner, formationStore CampaignFormationRunStore, now func() time.Time, newID func(string) string) *CampaignFormationExecutor {
	return NewCampaignFormationExecutorWithWitness(variants, runner, formationStore, nil, now, newID)
}

// NewCampaignFormationExecutorWithWitness constructs the heterogeneous campaign
// executor with optional witness evidence persistence.
func NewCampaignFormationExecutorWithWitness(variants []*evalv1.ModelVariant, runner CampaignFormationRunner, formationStore CampaignFormationRunStore, witnessReader *CampaignFormationWitnessReader, now func() time.Time, newID func(string) string) *CampaignFormationExecutor {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return prefix }
	}
	return &CampaignFormationExecutor{
		variants:       variants,
		runner:         runner,
		formationStore: formationStore,
		witnessReader:  witnessReader,
		now:            now,
		newID:          newID,
	}
}

// ExecuteAssignment executes one heterogeneous assignment and materializes the
// terminal assignment result from formation role telemetry.
func (e *CampaignFormationExecutor) ExecuteAssignment(ctx context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	if e == nil || e.runner == nil {
		return nil, fmt.Errorf("evaluation: execute heterogeneous assignment: formation executor: %w", constants.ErrFormationRunnerDependency)
	}
	if !IsHeterogeneousAssignment(req.Assignment) {
		return nil, fmt.Errorf("evaluation: execute heterogeneous assignment: heterogeneous target: %w", constants.ErrEvaluationTargetKindMismatch)
	}
	binding, err := CampaignFormationBindingRequest(req.Assignment, e.variants)
	if err != nil {
		return nil, err
	}
	initialState, err := BuildFormationInitialState(req.ScenarioInput)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: execute heterogeneous assignment: build initial state", err)
	}
	runContext := FormationRunContext{
		CampaignID:                   req.Assignment.GetCampaignId(),
		RunID:                        req.Assignment.GetRunId(),
		AssignmentID:                 req.Assignment.GetAssignmentId(),
		EvaluationAttemptID:          req.AttemptID,
		ScenarioID:                   req.Assignment.GetScenarioId(),
		ModelRegistryDigest:          req.Binding.ModelRegistryDigest,
		InferenceSessionID:           req.Binding.InferenceOperatorSessionID,
		DataSessionID:                req.Binding.DataOperatorSessionID,
		DataOperatorWorkingDirectory: req.Binding.DataOperatorWorkingDirectory,
	}
	if req.OnFormationRoleStarting != nil {
		runContext.OnRoleStarting = func(startCtx context.Context, role FormationRole) error {
			return req.OnFormationRoleStarting(startCtx, role)
		}
	}
	if req.OnFormationRoleProgress != nil {
		runContext.OnRoleProgress = func(progressCtx context.Context, formationResult *FormationRunResult) error {
			return req.OnFormationRoleProgress(progressCtx, formationResult)
		}
	}
	formationResult, err := e.runner.RunHeterogeneousFormation(ctx, binding, runContext, initialState)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: execute heterogeneous assignment", err)
	}
	if e.witnessReader != nil {
		if err := e.witnessReader.PersistFormationWitnessEvidence(ctx, formationResult); err != nil {
			return nil, assignmentExecutionError("evaluation: execute heterogeneous assignment: persist witness evidence", err)
		}
	}
	result, err := ImportAssignmentResultFromFormationRun(req, formationResult, e.now().UTC(), e.newID)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: import formation assignment result", err)
	}
	if e.witnessReader != nil {
		if err := e.witnessReader.BindFormationWitnessRefs(ctx, result); err != nil {
			return nil, assignmentExecutionError("evaluation: execute heterogeneous assignment: bind witness refs", err)
		}
	}
	if formationResult != nil {
		if persistErr := e.persistFormationRunEvidence(ctx, req, runContext, formationResult, result); persistErr != nil {
			return nil, assignmentExecutionError("evaluation: execute heterogeneous assignment: persist formation run evidence", persistErr)
		}
	}
	return result, nil
}

func (e *CampaignFormationExecutor) persistFormationRunEvidence(ctx context.Context, req AssignmentExecutionRequest, runContext FormationRunContext, formationResult *FormationRunResult, result *evalv1.EvaluationAssignmentResult) error {
	if e == nil || e.formationStore == nil || formationResult == nil {
		return nil
	}
	body, evidence, err := BuildFormationRunEvidence(req, runContext, formationResult)
	if err != nil {
		return err
	}
	if err := e.formationStore.SaveAssignmentFormationRun(ctx, req.Assignment.GetRunId(), req.Assignment.GetAssignmentId(), body); err != nil {
		return fmt.Errorf("persist formation run evidence: %w", err)
	}
	if result != nil && evidence != nil {
		ref, err := BuildAssignmentFormationRunEvidenceReference(req.Assignment.GetRunId(), req.Assignment.GetAssignmentId(), req.AttemptID, evidence, e.now().UTC())
		if err != nil {
			return err
		}
		result.EvidenceRefs = appendUniqueReference(result.EvidenceRefs, ref)
		digest, err := ComputeAssignmentResultDigest(result)
		if err != nil {
			return err
		}
		result.ResultDigest = digest
	}
	return nil
}

func importAssignmentResultFromFormationRunEvidence(req AssignmentExecutionRequest, evidence *FormationRunEvidence, now time.Time, newID func(string) string) (*evalv1.EvaluationAssignmentResult, error) {
	if evidence == nil {
		return nil, fmt.Errorf("evaluation: import formation assignment result from evidence: %w", constants.ErrMissingRequiredField)
	}
	if req.AttemptID == "" {
		req.AttemptID = evidence.EvaluationAttemptID
	}
	formationResult, err := FormationRunResultFromEvidence(evidence)
	if err != nil {
		return nil, err
	}
	return ImportAssignmentResultFromFormationRun(req, formationResult, now, newID)
}

// CampaignExecutorBuilder constructs one assignment executor on first use.
type CampaignExecutorBuilder func() (CampaignAssignmentExecutor, error)

// CampaignAssignmentRouter routes each assignment to the executor for its
// target kind: heterogeneous formations to the formation executor, model-role
// assignments to the homogeneous agent executor. Each executor is built on the
// first assignment of its kind, so a run never wires the other kind's
// dependencies.
type CampaignAssignmentRouter struct {
	buildFormation CampaignExecutorBuilder
	buildAgent     CampaignExecutorBuilder
	formation      CampaignAssignmentExecutor
	agent          CampaignAssignmentExecutor
}

// NewCampaignAssignmentRouter constructs a deferred, target-kind routing
// executor.
func NewCampaignAssignmentRouter(buildFormation, buildAgent CampaignExecutorBuilder) *CampaignAssignmentRouter {
	return &CampaignAssignmentRouter{buildFormation: buildFormation, buildAgent: buildAgent}
}

// ExecuteAssignment builds the executor for the assignment's target kind on
// first use and delegates to it.
func (r *CampaignAssignmentRouter) ExecuteAssignment(ctx context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	if r == nil {
		return nil, fmt.Errorf("evaluation: execute assignment: executor router: %w", constants.ErrMissingRequiredField)
	}
	build, inner := r.buildAgent, &r.agent
	if IsHeterogeneousAssignment(req.Assignment) {
		build, inner = r.buildFormation, &r.formation
	}
	if *inner == nil {
		if build == nil {
			return nil, fmt.Errorf("evaluation: execute assignment %s: no executor for target kind: %w", req.Assignment.GetAssignmentId(), constants.ErrEvaluationTargetKindMismatch)
		}
		executor, err := build()
		if err != nil {
			return nil, err
		}
		*inner = executor
	}
	return (*inner).ExecuteAssignment(ctx, req)
}

// ImportAssignmentResultFromFormationRun materializes one terminal heterogeneous
// assignment result from governed formation role telemetry.
func ImportAssignmentResultFromFormationRun(req AssignmentExecutionRequest, formationResult *FormationRunResult, now time.Time, newID func(string) string) (*evalv1.EvaluationAssignmentResult, error) {
	if req.Assignment == nil || formationResult == nil {
		return nil, fmt.Errorf("evaluation: import formation assignment result: %w", constants.ErrMissingRequiredField)
	}
	if newID == nil {
		newID = func(prefix string) string { return prefix }
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lifecycle, grades := formationAssignmentOutcome(req, formationResult)
	modelInferences, scoredSpan := modelInferenceRecordsFromFormationRun(req.Assignment, req.AttemptID, formationResult, newID)
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:            CampaignSchemaVersion,
		AssignmentId:             req.Assignment.GetAssignmentId(),
		RunId:                    req.Assignment.GetRunId(),
		CampaignId:               req.Assignment.GetCampaignId(),
		Lane:                     req.Assignment.GetLane(),
		LifecycleStatus:          lifecycle,
		ModelInferences:          modelInferences,
		DeterministicGrades:      grades,
		ScoredInferenceSpanNanos: scoredSpan,
		CompletedAt:              timestamppb.New(now),
	}
	digest, err := ComputeAssignmentResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return result, nil
}

func classifyFormationAssignmentOutcome(req AssignmentExecutionRequest, formationResult *FormationRunResult) (evalv1.EvaluationAssignmentLifecycleStatus, []*evalv1.DeterministicGrade) {
	grade := &evalv1.DeterministicGrade{
		GradeId:     req.Assignment.GetAssignmentId() + ":formation-roles",
		CriterionId: "formation-roles",
		Status:      evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		Detail:      "heterogeneous formation did not complete all three roles",
		Basis:       basisStructural,
	}
	if formationResult == nil {
		return evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, []*evalv1.DeterministicGrade{grade}
	}
	if formationResult.Passed && len(formationResult.Roles) == 3 {
		grade.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
		grade.Score = 1
		grade.Detail = "heterogeneous formation completed Lite → Assistant → Primary"
		return evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, []*evalv1.DeterministicGrade{grade}
	}
	if len(formationResult.Roles) == 0 {
		return evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, []*evalv1.DeterministicGrade{grade}
	}
	return evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PARTIAL, []*evalv1.DeterministicGrade{grade}
}

// formationAssignmentOutcome is the single grading rule for a heterogeneous
// assignment, shared by the write path and verification. The direct formation
// runner records role telemetry but no semantic judge evidence, so a completed
// formation under a semantic-judge catalog is a grader failure rather than a
// pass.
func formationAssignmentOutcome(req AssignmentExecutionRequest, formationResult *FormationRunResult) (evalv1.EvaluationAssignmentLifecycleStatus, []*evalv1.DeterministicGrade) {
	lifecycle, grades := classifyFormationAssignmentOutcome(req, formationResult)
	if lifecycle != evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED ||
		req.GradingMethod != evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE {
		return lifecycle, grades
	}
	return evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED, append(grades, newDeterministicGrade(
		req.Assignment.GetAssignmentId(),
		"semantic-judge-available",
		basisStructural,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE,
		"semantic judge is unavailable in the direct formation runner; no judge evidence was recorded for one or more roles",
		0,
	))
}

func modelInferenceRecordsFromFormationRun(assignment *evalv1.EvaluationAssignment, attemptID string, formationResult *FormationRunResult, newID func(string) string) ([]*evalv1.ModelInferenceRecord, *uint64) {
	if formationResult == nil || len(formationResult.Roles) == 0 {
		return nil, nil
	}
	records := make([]*evalv1.ModelInferenceRecord, 0, len(formationResult.Roles))
	var span uint64
	for _, role := range formationResult.Roles {
		if role.ProviderAttemptID == "" {
			continue
		}
		record := &evalv1.ModelInferenceRecord{
			InferenceRecordId:       newID("inference"),
			Classification:          evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
			ProviderAttemptId:       role.ProviderAttemptID,
			AssignmentId:            assignment.GetAssignmentId(),
			EvaluationAttemptId:     attemptID,
			ModelRole:               formationRoleToCampaignRole(role.Role),
			AgentPersona:            string(role.Role),
			CallSite:                "formation:" + string(role.Role),
			ModelVariant:            formationModelToVariant(role.Model),
			PrivacyAttested:         role.AttestationVerified || role.AttestationStatus == FormationAttestationNotNeeded,
			UsageAvailability:       role.UsageAvailability,
			PromptTokens:            role.PromptTokens,
			CompletionTokens:        role.GenerationTokens,
			GenerationDurationNanos: role.GenerationDurationNanos,
			FinishReason:            role.FinishReason,
			LoadState:               role.LoadState,
			RetryCount:              &role.RetryCount,
		}
		if role.ObserverEvidence != nil && role.ObserverEvidence.Window != nil {
			record.ProviderBoundaryObservationRef = providerBoundaryObservationRef(role.ObserverEvidence.Window)
		}
		if role.TTFTNanos > 0 {
			record.FirstTokenAtUnixNanos = role.TTFTNanos
		}
		if role.GenerationDurationNanos > 0 {
			span += role.GenerationDurationNanos
		}
		records = append(records, record)
	}
	if span == 0 {
		return records, nil
	}
	return records, &span
}

func formationRoleToCampaignRole(role FormationRole) evalv1.ModelCampaignRole {
	switch role {
	case FormationRolePrimary:
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
	case FormationRoleAssistant:
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT
	case FormationRoleLite:
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	default:
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED
	}
}

func formationModelToVariant(model FormationModel) *evalv1.ModelVariant {
	return &evalv1.ModelVariant{
		VariantId:      model.VariantID,
		ProviderClass:  model.ProviderClass,
		ServedModelTag: model.ServedModelTag,
		ModelDigest:    model.ModelDigest,
		ModelFamily:    model.Family,
		ParameterCount: model.ParameterCount,
		Quantization:   model.Quantization,
	}
}

// BuildFormationInitialState materializes the frozen scenario input fixture
// into opaque bytes passed through Lite → Assistant → Primary state handoff.
func BuildFormationInitialState(input ScenarioInputFixture) ([]byte, error) {
	if input.UserPrompt == "" {
		return nil, fmt.Errorf("evaluation: build formation initial state: scenario %s missing user prompt", input.ScenarioID)
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("evaluation: build formation initial state: %w", err)
	}
	return body, nil
}

func formationOutcomeFromResult(result *evalv1.EvaluationAssignmentResult) *FormationRunResult {
	if result == nil {
		return nil
	}
	// A grader failure is a completed formation the semantic-judge rule
	// downgraded, so it recomputes from a passed formation outcome.
	lifecycle := result.GetLifecycleStatus()
	return &FormationRunResult{
		Passed: lifecycle == evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED ||
			lifecycle == evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED,
		Roles: make([]FormationRoleTelemetry, len(result.GetModelInferences())),
	}
}

// VerifyFormationAssignmentEvidence independently checks persisted heterogeneous
// assignment results from their recorded role inferences.
func VerifyFormationAssignmentEvidence(assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) error {
	if assignment == nil || result == nil {
		return fmt.Errorf("evaluation: verify formation assignment evidence: %w", constants.ErrMissingRequiredField)
	}
	if !IsHeterogeneousAssignment(assignment) {
		return fmt.Errorf("evaluation: verify formation assignment evidence: heterogeneous target: %w", constants.ErrEvaluationTargetKindMismatch)
	}
	inferences := result.GetModelInferences()
	switch result.GetLifecycleStatus() {
	case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED:
		if len(inferences) != 3 {
			return fmt.Errorf("completed heterogeneous assignment requires three model inferences: %w", constants.ErrEvalRunVerificationFailed)
		}
	case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PARTIAL:
		if len(inferences) == 0 || len(inferences) >= 3 {
			return fmt.Errorf("partial heterogeneous assignment requires one or two model inferences: %w", constants.ErrEvalRunVerificationFailed)
		}
	case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED:
		if len(inferences) != 0 {
			return fmt.Errorf("provider-failed heterogeneous assignment must not report model inferences: %w", constants.ErrEvalRunVerificationFailed)
		}
	}
	expectedRoles := []evalv1.ModelCampaignRole{
		evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE,
		evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT,
		evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
	}
	for index, record := range inferences {
		if record.GetProviderAttemptId() == "" {
			return fmt.Errorf("model inference %d provider attempt id: %w", index, constants.ErrMissingRequiredField)
		}
		if record.GetAssignmentId() != assignment.GetAssignmentId() {
			return fmt.Errorf("model inference %d assignment binding: %w", index, constants.ErrEvidenceScopeMismatch)
		}
		if index < len(expectedRoles) && record.GetModelRole() != expectedRoles[index] {
			return fmt.Errorf("model inference %d role order mismatch: %w", index, constants.ErrEvalRunVerificationFailed)
		}
		expectedCallSite := "formation:" + formationRoleToCampaignRoleLabel(record.GetModelRole())
		if record.GetCallSite() != expectedCallSite {
			return fmt.Errorf("model inference %d call site mismatch: %w", index, constants.ErrEvalRunVerificationFailed)
		}
	}
	if span := result.GetScoredInferenceSpanNanos(); span > 0 {
		var total uint64
		for _, record := range inferences {
			total += record.GetGenerationDurationNanos()
		}
		if total != span {
			return fmt.Errorf("scored inference span mismatch: %w", constants.ErrEvalRunVerificationFailed)
		}
	}
	return nil
}

func formationRoleToCampaignRoleLabel(role evalv1.ModelCampaignRole) string {
	switch role {
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY:
		return string(FormationRolePrimary)
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT:
		return string(FormationRoleAssistant)
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE:
		return string(FormationRoleLite)
	default:
		return "unspecified"
	}
}

// RecomputeFormationAssignmentGrades derives the deterministic grades for one
// persisted heterogeneous assignment result from its stored lifecycle and role
// inferences, applying the same rule the write path graded with.
func RecomputeFormationAssignmentGrades(req AssignmentExecutionRequest, result *evalv1.EvaluationAssignmentResult) ([]*evalv1.DeterministicGrade, error) {
	if req.Assignment == nil || result == nil {
		return nil, fmt.Errorf("evaluation: recompute formation assignment grades: %w", constants.ErrMissingRequiredField)
	}
	lifecycle, grades := formationAssignmentOutcome(req, formationOutcomeFromResult(result))
	if lifecycle != result.GetLifecycleStatus() {
		return nil, fmt.Errorf("evaluation: recompute formation assignment grades: stored lifecycle does not match formation outcome: %w", constants.ErrEvalRunVerificationFailed)
	}
	return grades, nil
}
