// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCollectRunAggregateState_UnavailableObservationsDoNotLowerModelScores(t *testing.T) {
	t.Parallel()
	const completed = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	assignments := []*evalv1.EvaluationAssignment{
		homogeneousAssignment("pass", "model", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
		homogeneousAssignment("fail", "model", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
		homogeneousAssignment("unavailable", "model", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
	}
	results := map[string]*evalv1.EvaluationAssignmentResult{}
	for i, status := range []evalv1.EvaluationVerdictStatus{verdictPass, verdictFail, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE} {
		id := assignments[i].GetAssignmentId()
		results[id] = &evalv1.EvaluationAssignmentResult{
			AssignmentId: id, LifecycleStatus: completed,
			DeterministicGrades: []*evalv1.DeterministicGrade{scoringGrade("state-observation", basisObservation, status)},
		}
	}
	state, err := CollectRunAggregateState(assignments, results)
	require.NoError(t, err)
	assert.Equal(t, uint32(3), state.Terminal)
	assert.Equal(t, uint32(2), state.Judged)
	require.NotNil(t, state.Headline.PassRate.Value)
	assert.InDelta(t, 0.5, *state.Headline.PassRate.Value, 0)
	model := buildModelSummaryRecord("dataset", "2026-10-23T00:00:00Z", state.VariantRoles["model:primary"], CampaignRelease{})
	require.NotNil(t, model.PassRate)
	assert.Equal(t, uint32(2), model.PassRate.Denominator)
	assert.Equal(t, uint32(1), model.TerminalOutcomes[TerminalOutcomeInvalidEvidence])

	state, err = CollectRunAggregateState(assignments[2:], results)
	require.NoError(t, err)
	assert.Zero(t, state.Judged)
	assert.Nil(t, state.Headline.PassRate.Value, "missing evidence cannot become an observed zero score")
}

func TestFinalResponseDiagnosis_GradesClaimsInsteadOfIncidentalWording(t *testing.T) {
	t.Parallel()
	blueprint := finalResponseDiagnosis()
	const diagnosis = "checkout-api has an 18% timeout rate with payments.internal.example configured as its upstream."
	const summary = "Some customers cannot complete checkout while we investigate."
	for _, tc := range []struct {
		name, diagnosis, confidence, summary string
		passed                               bool
	}{
		{"direct customer address", diagnosis, "Moderate.", "We are experiencing checkout processing delays affecting approximately 1 in 5 transactions.", true},
		{"qualified upstream hypothesis", diagnosis + " The timeouts are likely caused by an upstream issue.", "Moderate.", summary, true},
		{"confidence evidence limitation", diagnosis, "Confidence is moderate, not 100% due to synthetic nature and no additional diagnostic details.", summary, true},
		{"observed timeout impact", diagnosis + " The checkout API timeout rate has resulted in confirmed customer checkout failures.", "High in the observed symptoms.", summary, true},
		{"definitive upstream cause", diagnosis + " The failures are due to problems with the upstream host.", "Moderate.", summary, false},
		{"unlisted definitive cause", diagnosis + " The root cause is CPU overload.", "Low.", summary, false},
		{"qualifier in another claim", diagnosis + " The upstream may be healthy; the root cause is CPU overload.", "Low.", summary, false},
		{"unrelated summary", diagnosis, "Moderate.", "Our customer support team is available for payment questions.", false},
		{"empty impact", diagnosis, "Moderate.", "We are investigating.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			output := "Diagnosis: " + tc.diagnosis + "\nConfidence: " + tc.confidence +
				"\nRecommended action: Investigate checkout and upstream telemetry before assigning a cause.\nCustomer-safe summary: " + tc.summary
			passed, detail := evaluateFinalResponseDiagnosis(output, blueprint.Input, blueprint.Gold.ContentCheck.DiagnosisRubric)
			assert.Equal(t, tc.passed, passed, detail)
		})
	}
}

func TestGradeHomogeneousScenario_FinalResponseRestatementDoesNotCountTwice(t *testing.T) {
	t.Parallel()
	blueprint := finalResponseDiagnosis()
	blueprint.Input.ScenarioID = blueprint.ScenarioID
	blueprint.Gold.ScenarioID = blueprint.ScenarioID
	trace := &AgentTrace{
		RunID: "run", AssignmentID: "assignment", DesignatedRole: "primary",
		ModelCalls:  []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx", ResultDigest: "digest"}},
		FinalOutput: "checkout-api",
	}
	grading, err := GradeHomogeneousScenario(AssignmentExecutionRequest{
		Assignment:    &evalv1.EvaluationAssignment{AssignmentId: trace.AssignmentID, RunId: trace.RunID},
		ScenarioInput: blueprint.Input, ScenarioGold: blueprint.Gold,
		ScenarioTools: ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER},
	}, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)
	tally, err := tallyDeterministicGrades(grading.DeterministicGrades)
	require.NoError(t, err)
	assert.Equal(t, 2, tally.Counted, "only content and trajectory are independent observations")
	assert.Equal(t, 1, tally.Passed)
	for _, grade := range grading.DeterministicGrades {
		if grade.GetCriterionId() == "required-evidence:final_response" {
			assert.Equal(t, basisDerived, grade.GetBasis())
		}
	}
	result := &evalv1.EvaluationAssignmentResult{DecomposedScores: grading.DecomposedScores}
	rate, ok := DeterministicPassRate(result)
	require.True(t, ok)
	assert.InDelta(t, 0.5, rate, 0)
}

func TestRecoveryToolFailure_DisclosesShellRestrictionAndAcceptsMissingFileWording(t *testing.T) {
	t.Parallel()
	blueprint := recoveryToolFailure()
	assert.Contains(t, blueprint.Input.UserPrompt, "Do not use run_shell_command")
	passed, detail := evaluateContentCheck("The requested file is not present. Next, inspect the deploy directory with fs_disk_profile.", *blueprint.Gold.ContentCheck, ScenarioWorkspace{})
	assert.True(t, passed, detail)
}

func TestDiagnosisRubricV3_RetainsFrozenSummaryAndEvidenceWeights(t *testing.T) {
	t.Parallel()
	blueprint := finalResponseDiagnosis()
	blueprint.Gold.ContentCheck.DiagnosisRubric = ScenarioDiagnosisRubricCheckoutTimeoutV3
	output := "Diagnosis: checkout-api has an 18% timeout rate with payments.internal.example configured as its upstream.\n" +
		"Confidence: Moderate.\nRecommended action: Investigate both services.\n" +
		"Customer-safe summary: We are experiencing checkout processing delays affecting approximately 1 in 5 transactions."
	passed, _ := evaluateFinalResponseDiagnosis(output, blueprint.Input, blueprint.Gold.ContentCheck.DiagnosisRubric)
	assert.False(t, passed, "the frozen v3 summary contract is unchanged")
	grader := homogeneousGrader{req: AssignmentExecutionRequest{ScenarioGold: blueprint.Gold}}
	assert.Equal(t, basisObservation, grader.requiredEvidenceBasis("final_response"))
}
