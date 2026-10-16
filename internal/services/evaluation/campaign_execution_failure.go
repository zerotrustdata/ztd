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
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
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
	case errors.Is(err, constants.ErrFormationRegistryBinding), errors.Is(err, constants.ErrFormationStackMismatch), errors.Is(err, constants.ErrFormationInvalid):
		return ExecutionFailureInvalidInput
	case errors.Is(err, constants.ErrFormationAttestationFailed), errors.Is(err, constants.ErrFormationAttestationRequired):
		return ExecutionFailureVerification
	case errors.Is(err, constants.ErrFormationWitnessUnavailable):
		return ExecutionFailureEvidence
	case errors.Is(err, constants.ErrFormationOutOfMemory):
		return ExecutionFailureProvider
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
	lifecycle := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED
	if class == ExecutionFailureProvider {
		lifecycle = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED
	}
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    req.Assignment.GetAssignmentId(),
		RunId:           req.Assignment.GetRunId(),
		CampaignId:      req.Assignment.GetCampaignId(),
		Lane:            req.Assignment.GetLane(),
		LifecycleStatus: lifecycle,
		DeterministicGrades: []*evalv1.DeterministicGrade{
			{
				GradeId:     req.Assignment.GetAssignmentId() + ":execution-failure",
				CriterionId: "execution-failure",
				Status:      evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
				Detail:      "assignment execution failed: " + string(class),
				Basis:       basisStructural,
			},
		},
		CompletedAt: timestamppb.New(now),
	}
	if class == ExecutionFailureProvider {
		result.FailureReason = providerFailureReason(ProviderFailureClass(execErr))
		result.PublicFailureReason = result.FailureReason
	}
	digest, err := ComputeAssignmentResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return result, nil
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
	if IsHeterogeneousAssignment(req.Assignment) {
		evidence, err := c.store.LoadAssignmentFormationRun(ctx, req.Assignment.GetRunId(), req.Assignment.GetAssignmentId())
		if err == nil && evidence != nil {
			result, importErr := importAssignmentResultFromFormationRunEvidence(req, evidence, c.now().UTC(), c.newID)
			if importErr == nil {
				return result, nil
			}
		}
	} else if result, ok := c.importPersistedAgentTrace(ctx, req); ok {
		return result, nil
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
	return c.publishAssignmentTerminal(persistCtx, assignment, result)
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
		result, err := c.recoverTerminalAssignmentResult(ctx, assignment)
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

func (c *CampaignController) recoverTerminalAssignmentResult(ctx context.Context, assignment *evalv1.EvaluationAssignment) (*evalv1.EvaluationAssignmentResult, error) {
	if IsHeterogeneousAssignment(assignment) {
		evidence, err := c.store.LoadAssignmentFormationRun(ctx, assignment.GetRunId(), assignment.GetAssignmentId())
		if err == nil && evidence != nil {
			req := AssignmentExecutionRequest{Assignment: assignment, AttemptID: evidence.EvaluationAttemptID}
			result, importErr := importAssignmentResultFromFormationRunEvidence(req, evidence, c.now().UTC(), c.newID)
			if importErr == nil {
				return result, nil
			}
		}
	}
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
	result, err := ImportAssignmentResultFromAgentTrace(req, trace, ref, c.now().UTC(), c.newID)
	if err != nil {
		return nil, false
	}
	return result, true
}
