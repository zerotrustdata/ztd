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
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// ExecutionFailureClass is the fixed category recorded for an assignment whose
// executor failed before it sealed a trace. It replaces the error text, which
// can echo prompt, provider, or tool content, so a failed result persists only
// the category (the same rule as inference.AttemptFailureClass).
type ExecutionFailureClass string

const (
	ExecutionFailureProvider     ExecutionFailureClass = "provider_failure"
	ExecutionFailureTarget       ExecutionFailureClass = "target_unavailable"
	ExecutionFailurePosture      ExecutionFailureClass = "posture_unsupported"
	ExecutionFailureEvidence     ExecutionFailureClass = "evidence_unavailable"
	ExecutionFailureVerification ExecutionFailureClass = "verification_failed"
	ExecutionFailureDispatch     ExecutionFailureClass = "dispatch_failure"
	ExecutionFailureInvalidInput ExecutionFailureClass = "invalid_input"
	ExecutionFailureCanceled     ExecutionFailureClass = "canceled"
	ExecutionFailureTimeout      ExecutionFailureClass = "timeout"
	ExecutionFailureInternal     ExecutionFailureClass = "internal_error"
)

// ClassifyExecutionFailure maps an executor error to its fixed failure
// category. Unrecognized errors are ExecutionFailureInternal; their text is
// never stored.
func ClassifyExecutionFailure(err error) ExecutionFailureClass {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ExecutionFailureTimeout
	case errors.Is(err, context.Canceled):
		return ExecutionFailureCanceled
	case errors.Is(err, constants.ErrMissingRequiredField):
		return ExecutionFailureInvalidInput
	case errors.Is(err, constants.ErrEvaluationDispatchFailed):
		return ExecutionFailureDispatch
	case errors.Is(err, constants.ErrEvaluationTargetUnavailable), errors.Is(err, constants.ErrEvaluationTargetAmbiguous):
		return ExecutionFailureTarget
	case errors.Is(err, constants.ErrEvaluationPostureUnsupported):
		return ExecutionFailurePosture
	case errors.Is(err, constants.ErrEvalRunVerificationFailed):
		return ExecutionFailureVerification
	case errors.Is(err, constants.ErrEvaluationObservationUnavailable):
		return ExecutionFailureEvidence
	case errors.Is(err, constants.ErrEvaluationModelUnserved), errors.Is(err, constants.ErrInferenceOutcomeUnknown), errors.Is(err, constants.ErrInferenceReceiptFailed),
		inference.ClassifyAttemptFailure(err) != inference.AttemptFailureInternal:
		return ExecutionFailureProvider
	case errors.Is(err, constants.ErrFormationRegistryBinding), errors.Is(err, constants.ErrFormationStackMismatch), errors.Is(err, constants.ErrFormationInvalid),
		errors.Is(err, constants.ErrFormationAttestationRequired):
		return ExecutionFailureInvalidInput
	default:
		return ExecutionFailureInternal
	}
}

// ProviderFailureClass returns the inference attempt failure class an error
// carries, or "" when the provider did not classify it.
func ProviderFailureClass(err error) inference.AttemptFailureClass {
	class := inference.ClassifyAttemptFailure(err)
	if class == inference.AttemptFailureInternal {
		return ""
	}
	return class
}

// BuildExecutorFailureAssignmentResult materializes one terminal assignment
// result when production chat execution fails before a canonical trace import.
// The grade detail is the failure class, never the error text.
func BuildExecutorFailureAssignmentResult(req AssignmentExecutionRequest, execErr error, now time.Time) (*evalv1.EvaluationAssignmentResult, error) {
	if req.Assignment == nil || execErr == nil {
		return nil, fmt.Errorf("evaluation: build executor failure assignment result: %w", constants.ErrMissingRequiredField)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	class := ClassifyExecutionFailure(execErr)
	failureReason := ""
	if class == ExecutionFailureProvider {
		failureReason = providerFailureReason(ProviderFailureClass(execErr))
	}
	result := executorFailureAssignmentResult(req.Assignment, class, failureReason, timestamppb.New(now))
	digest, err := ComputeAssignmentResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return result, nil
}

// executorFailureAssignmentResult is the undigested traceless failure result
// for one failure class. The builder and the run verifier share it, so a
// traceless result verifies only in exactly this shape.
func executorFailureAssignmentResult(assignment *evalv1.EvaluationAssignment, class ExecutionFailureClass, failureReason string, completedAt *timestamppb.Timestamp) *evalv1.EvaluationAssignmentResult {
	lifecycle := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED
	if class == ExecutionFailureProvider {
		lifecycle = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED
	}
	return &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    assignment.GetAssignmentId(),
		RunId:           assignment.GetRunId(),
		CampaignId:      assignment.GetCampaignId(),
		Lane:            assignment.GetLane(),
		LifecycleStatus: lifecycle,
		DeterministicGrades: []*evalv1.DeterministicGrade{
			{
				GradeId:     assignment.GetAssignmentId() + ":" + executionFailureCriterion,
				CriterionId: executionFailureCriterion,
				Status:      evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
				Detail:      executionFailureDetailPrefix + string(class),
				Basis:       basisStructural,
			},
		},
		CompletedAt:         completedAt,
		FailureReason:       failureReason,
		PublicFailureReason: failureReason,
	}
}

const (
	executionFailureCriterion    = "execution-failure"
	executionFailureDetailPrefix = "assignment execution failed: "
)

// executionFailureClasses is every class ClassifyExecutionFailure can return.
var executionFailureClasses = []ExecutionFailureClass{
	ExecutionFailureProvider, ExecutionFailureTarget, ExecutionFailurePosture, ExecutionFailureEvidence, ExecutionFailureVerification,
	ExecutionFailureDispatch, ExecutionFailureInvalidInput, ExecutionFailureCanceled, ExecutionFailureTimeout, ExecutionFailureInternal,
}

// verifyExecutorFailureResult structurally verifies a result the executor
// materialized before any trace was sealed: it carries no trace evidence, no
// inference or tool records, and must equal the shape
// BuildExecutorFailureAssignmentResult produces for its recorded class.
func verifyExecutorFailureResult(assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) []string {
	for _, ref := range result.GetEvidenceRefs() {
		if ref.GetArtifactType() == string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace) {
			return []string{"result references an agent trace that is not persisted"}
		}
	}
	grades := result.GetDeterministicGrades()
	if len(grades) != 1 || grades[0].GetCriterionId() != executionFailureCriterion {
		return []string{"agent trace is required for homogeneous verification"}
	}
	detail := grades[0].GetDetail()
	class := ExecutionFailureClass(strings.TrimPrefix(detail, executionFailureDetailPrefix))
	if !strings.HasPrefix(detail, executionFailureDetailPrefix) || !slices.Contains(executionFailureClasses, class) {
		return []string{fmt.Sprintf("execution failure grade records unknown class %q", detail)}
	}
	failures := make([]string, 0)
	if class == ExecutionFailureProvider && !isProviderFailureReason(result.GetFailureReason()) {
		failures = append(failures, "provider failure result records a non-categorical failure reason")
	}
	expected := executorFailureAssignmentResult(assignment, class, result.GetFailureReason(), result.GetCompletedAt())
	expected.ResultDigest = result.GetResultDigest()
	if !proto.Equal(expected, result) {
		failures = append(failures, "traceless result does not match the executor failure shape for class "+string(class))
	}
	if assignment.GetLifecycleStatus() != result.GetLifecycleStatus() {
		failures = append(failures, fmt.Sprintf("assignment lifecycle %s does not match result lifecycle %s", assignment.GetLifecycleStatus(), result.GetLifecycleStatus()))
	}
	return failures
}

// isProviderFailureReason reports whether reason is one of the fixed
// providerFailureReason sentences.
func isProviderFailureReason(reason string) bool {
	for _, class := range []inference.AttemptFailureClass{
		"", inference.AttemptFailureCapabilityRejected, inference.AttemptFailureContextOverflow, inference.AttemptFailureBackendTimeout,
		inference.AttemptFailureBackendUnavailable, inference.AttemptFailureModelNotFound, inference.AttemptFailureResponseInvalid,
		inference.AttemptFailureGenerateFailed, inference.AttemptFailureCanceled,
	} {
		if reason == providerFailureReason(class) {
			return true
		}
	}
	return false
}

// BuildRecoveredTerminalAssignmentResult materializes one terminal result for
// an assignment that reached a terminal lifecycle without a persisted result.
func BuildRecoveredTerminalAssignmentResult(assignment *evalv1.EvaluationAssignment, now time.Time) (*evalv1.EvaluationAssignmentResult, error) {
	if assignment == nil || !isTerminalAssignmentLifecycle(assignment.GetLifecycleStatus()) {
		return nil, fmt.Errorf("evaluation: build recovered terminal assignment result: %w", constants.ErrMissingRequiredField)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	completedAt := assignment.GetCompletedAt()
	if completedAt == nil {
		completedAt = timestamppb.New(now)
	}
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    assignment.GetAssignmentId(),
		RunId:           assignment.GetRunId(),
		CampaignId:      assignment.GetCampaignId(),
		Lane:            assignment.GetLane(),
		LifecycleStatus: assignment.GetLifecycleStatus(),
		DeterministicGrades: []*evalv1.DeterministicGrade{
			{
				GradeId:     assignment.GetAssignmentId() + ":recovered-terminal",
				CriterionId: "recovered-terminal",
				Status:      evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
				Detail:      "recovered terminal assignment lifecycle without a persisted result record",
				Basis:       basisStructural,
			},
		},
		CompletedAt: completedAt,
	}
	digest, err := ComputeAssignmentResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return result, nil
}

func assignmentExecutionError(scope string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %w", constants.ErrEvaluationAssignmentExecutionFailed, scope, err)
}

func isRecoverableAssignmentExecutionError(err error) bool {
	return errors.Is(err, constants.ErrEvaluationAssignmentExecutionFailed)
}

func (c *CampaignController) buildFailureAssignmentResult(ctx context.Context, req AssignmentExecutionRequest, execErr error) (*evalv1.EvaluationAssignmentResult, error) {
	if c == nil || c.store == nil || req.Assignment == nil {
		return nil, fmt.Errorf("evaluation: build failure assignment result: %w", constants.ErrMissingRequiredField)
	}
	if !IsHeterogeneousAssignment(req.Assignment) {
		if result, ok := c.importPersistedAgentTrace(ctx, req); ok {
			return result, nil
		}
	}
	return BuildExecutorFailureAssignmentResult(req, execErr, c.now().UTC())
}

func (c *CampaignController) persistTerminalAssignment(ctx context.Context, assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) error {
	persistCtx := assignmentPersistContext(ctx)
	if err := c.store.SaveAssignmentResult(persistCtx, result); err != nil {
		return err
	}
	assignment.LifecycleStatus = result.GetLifecycleStatus()
	if result.GetCompletedAt() != nil {
		assignment.CompletedAt = result.GetCompletedAt()
	} else {
		assignment.CompletedAt = timestamppb.New(c.now().UTC())
	}
	if err := c.store.SaveAssignment(persistCtx, assignment); err != nil {
		return err
	}
	// The result is durable here; a publication failure is reported but never
	// undoes or blocks the persisted terminal state.
	if err := c.publishAssignmentTerminal(persistCtx, assignment, result); err != nil {
		return fmt.Errorf("%w: assignment %s: %w", constants.ErrEvaluationPublicationFailed, assignment.GetAssignmentId(), err)
	}
	return nil
}

// assignmentPersistContext keeps terminal writes alive when the assignment
// execution context expired while waiting for a trace or provider response.
func assignmentPersistContext(ctx context.Context) context.Context {
	if ctx == nil || ctx.Err() == nil {
		return ctx
	}
	return context.WithoutCancel(ctx)
}

// RepairAssignmentsWithoutResults backfills persisted terminal results for
// assignments that already reached a terminal lifecycle.
func (c *CampaignController) RepairAssignmentsWithoutResults(ctx context.Context, runID string) (int, error) {
	if c == nil || c.store == nil || runID == "" {
		return 0, fmt.Errorf("evaluation: repair assignments without results: %w", constants.ErrMissingRequiredField)
	}
	assignments, err := c.store.ListAssignments(ctx, runID)
	if err != nil {
		return 0, err
	}
	repaired := 0
	for _, assignment := range assignments {
		if assignment == nil || !isTerminalAssignmentLifecycle(assignment.GetLifecycleStatus()) {
			continue
		}
		exists, err := c.store.AssignmentResultExists(ctx, runID, assignment.GetAssignmentId())
		if err != nil {
			return repaired, err
		}
		if exists {
			continue
		}
		result, err := c.recoverTerminalAssignmentResult(assignment)
		if err != nil {
			return repaired, err
		}
		if err := c.persistTerminalAssignment(ctx, assignment, result); err != nil {
			return repaired, err
		}
		repaired++
	}
	return repaired, nil
}

func (c *CampaignController) recoverTerminalAssignmentResult(assignment *evalv1.EvaluationAssignment) (*evalv1.EvaluationAssignmentResult, error) {
	recoveredErr := constants.ErrEvaluationRecoveredResultMissing
	req := AssignmentExecutionRequest{Assignment: assignment, AttemptID: "recovered"}
	result, err := BuildExecutorFailureAssignmentResult(req, recoveredErr, c.now().UTC())
	if err != nil {
		return BuildRecoveredTerminalAssignmentResult(assignment, c.now().UTC())
	}
	result.LifecycleStatus = assignment.GetLifecycleStatus()
	digest, err := ComputeAssignmentResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return result, nil
}

// importPersistedAgentTrace rebuilds a homogeneous assignment result from the
// agent trace the executor sealed before it failed, so a persisted run is
// graded rather than reported as an executor failure. req must carry the
// frozen scenario fixture the trace is graded against.
func (c *CampaignController) importPersistedAgentTrace(ctx context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, bool) {
	if _, ok := req.Assignment.GetTarget().(*evalv1.EvaluationAssignment_Homogeneous); !ok {
		return nil, false
	}
	trace, err := c.store.LoadAssignmentAgentTrace(ctx, req.Assignment.GetRunId(), req.Assignment.GetAssignmentId())
	if err != nil || trace == nil {
		return nil, false
	}
	// The loaded trace is digest-checked sealed evidence; its canonical body is
	// the persisted one, whatever schema version sealed it.
	body, err := MarshalCanonicalJSONObject(trace)
	if err != nil {
		return nil, false
	}
	ref, err := BuildAssignmentAgentTraceEvidenceReference(req.Assignment.GetRunId(), trace.EvaluationAttemptID, body, c.now().UTC())
	if err != nil {
		return nil, false
	}
	// Recovery is pinned to the current sealed version like the live path, so an
	// earlier-version persisted trace is INVALID_EVIDENCE.
	result, err := ImportAssignmentResultFromAgentTrace(req, trace, ref, c.now().UTC(), c.newID)
	if err != nil {
		return nil, false
	}
	return result, true
}
