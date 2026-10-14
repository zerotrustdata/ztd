// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func providerFailureGradeRequest() AssignmentExecutionRequest {
	return AssignmentExecutionRequest{
		Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-1", RunId: "run-1"},
		ScenarioInput: ScenarioInputFixture{ScenarioID: "scenario-1"},
	}
}

func TestProviderFailureClass_OnlyForClassifiedProviderErrors(t *testing.T) {
	assert.Equal(t, "capability_unsupported", string(ProviderFailureClass(fmt.Errorf("turn 1: %w", constants.ErrInferenceToolsUnsupported))))
	assert.Equal(t, "backend_timeout", string(ProviderFailureClass(constants.ErrInferenceBackendTimeout)))
	assert.Empty(t, ProviderFailureClass(constants.ErrHTTPStatusError))
}

// A provider failure leads the role-invoked grade with its attempt class, so
// an unserved model reads as unserved, not as a content or trajectory miss.
func TestGradeHomogeneousScenario_ProviderFailureLeadsWithAttemptClass(t *testing.T) {
	req := providerFailureGradeRequest()
	trace := &AgentTrace{
		RunID: "run-1", AssignmentID: "assign-1", DesignatedRole: "primary",
		ProviderFailure:      string(ExecutionFailureProvider),
		ProviderFailureClass: "capability_unsupported",
	}
	lifecycle := agentTraceLifecycle(req, trace)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, lifecycle)

	grading, err := GradeHomogeneousScenario(req, trace, lifecycle)
	require.NoError(t, err)
	require.NotEmpty(t, grading.DeterministicGrades)
	first := grading.DeterministicGrades[0]
	assert.Equal(t, "role-invoked", first.GetCriterionId())
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, first.GetStatus())
	assert.Equal(t, "designated model role was not invoked: provider_failure (capability_unsupported)", first.GetDetail())
}

// A trace sealed before the attempt class existed recomputes to the grade it
// was stored with, so verification of older runs is unchanged.
func TestGradeHomogeneousScenario_LegacyProviderFailureDetailUnchanged(t *testing.T) {
	req := providerFailureGradeRequest()
	trace := &AgentTrace{RunID: "run-1", AssignmentID: "assign-1", DesignatedRole: "primary", ProviderFailure: string(ExecutionFailureInternal)}
	grading, err := GradeHomogeneousScenario(req, trace, agentTraceLifecycle(req, trace))
	require.NoError(t, err)
	assert.Equal(t, "designated model role was not invoked or assignment did not complete", grading.DeterministicGrades[0].GetDetail())
}

// An empty reply is the served model's answer (D2): the governed call is on
// record, so the assignment completes and grades FAIL. It never ends
// PROVIDER_FAILED or INVALID_EVIDENCE, which would keep it out of the pass rate.
func TestGradeHomogeneousScenario_EmptyReplyGradesModelFail(t *testing.T) {
	req := providerFailureGradeRequest()
	req.ScenarioGold.ContentCheck = &ScenarioContentCheck{ExactToken: "READY"}
	trace := &AgentTrace{
		RunID: "run-1", AssignmentID: "assign-1", DesignatedRole: "primary",
		ModelCalls: []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-1", ResultDigest: "digest-1"}},
	}
	lifecycle := agentTraceLifecycle(req, trace)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, lifecycle)

	grading, err := GradeHomogeneousScenario(req, trace, lifecycle)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, verdictFromGrades(grading.DeterministicGrades))
}
