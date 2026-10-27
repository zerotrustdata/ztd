// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestGradeHomogeneousScenario_FinalResponseDiagnosisUsesEvidenceAndPopulatedSections(t *testing.T) {
	blueprint := finalResponseDiagnosis()
	input := blueprint.Input
	input.ScenarioID = blueprint.ScenarioID
	gold := blueprint.Gold
	gold.ScenarioID = blueprint.ScenarioID

	tests := []struct {
		name   string
		output string
		passed bool
	}{
		{
			name:   "prompt term echo does not diagnose",
			output: "checkout-api confidence",
		},
		{
			name:   "exact user prompt echo does not diagnose",
			output: input.UserPrompt,
		},
		{
			name:   "complete user prompt and inline evidence echo does not diagnose",
			output: diagnosisPromptEcho(input),
		},
		{
			name: "empty labeled sections are not evidence",
			output: "Diagnosis:\nConfidence:\nRecommended action:\n" +
				"Customer-safe summary:",
		},
		{
			name: "reported high confidence in the supplied observations passes",
			output: "### Diagnosis\nThe issue is related to a high timeout rate (18%) in the `checkout-api` when interacting with the upstream host `payments.internal.example`. This is leading to confirmed customer checkout failures.\n" +
				"### Confidence\nConfidence: High\n" +
				"### Recommended Action\n1. **Investigate the upstream host `payments.internal.example`** to identify the cause of the timeouts. This could involve checking server load, network latency, or service health.\n" +
				"2. **Monitor the timeout rates** closely to see if they decrease after any interventions.\n" +
				"3. **Implement a fallback mechanism** for checkout if the upstream host remains unreliable to minimize customer impact.\n" +
				"### Customer-safe Summary\nSome customers are experiencing checkout failures. We are investigating the timeout issue.",
			passed: true,
		},
		{
			name: "high confidence in symptoms does not require uncertainty keywords",
			output: "Diagnosis: checkout API has an 18% timeout rate with payments.internal.example configured as its upstream.\n" +
				"Confidence: High confidence in the observed timeouts and confirmed customer impact.\n" +
				"Recommended action: Investigate checkout and upstream telemetry to identify the cause.\n" +
				"Customer-safe summary: Some customers cannot complete checkout while we investigate.",
			passed: true,
		},
		{
			name: "reviewed upstream problems are not a proven cause",
			output: "Diagnosis: checkout-api has an 18% timeout rate with payments.internal.example as its configured upstream; failures due to problems with the upstream host.\n" +
				"Confidence: High.\nRecommended action: Investigate both services.\n" +
				"Customer-safe summary: Customers cannot complete checkout while we investigate.",
		},
		{
			name: "reviewed evidence clearly links assertion fails",
			output: "Diagnosis: checkout-api has an 18% timeout rate with payments.internal.example as its configured upstream. The evidence clearly links checkout timeouts to upstream issues.\n" +
				"Confidence: High.\nRecommended action: Investigate both services.\n" +
				"Customer-safe summary: Customers cannot complete checkout while we investigate.",
		},
		{
			name: "qualified upstream problems remain a hypothesis",
			output: "Diagnosis: checkout-api has an 18% timeout rate with payments.internal.example as its configured upstream. Failures could be due to problems with the upstream host.\n" +
				"Confidence: High in the observed symptoms.\nRecommended action: Investigate both services.\n" +
				"Customer-safe summary: Customers cannot complete checkout while we investigate.",
			passed: true,
		},
		{
			name: "high confidence in absence of an established cause passes",
			output: "Diagnosis: checkout-api has an 18% timeout rate with payments.internal.example as its configured upstream.\n" +
				"Confidence: High confidence that no root cause has been established.\nRecommended action: Investigate both services.\n" +
				"Customer-safe summary: Customers cannot complete checkout while we investigate.",
			passed: true,
		},
		{
			name: "high confidence cannot assert an unidentified root cause",
			output: "Diagnosis: checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream.\n" +
				"Confidence: High confidence in the root cause.\n" +
				"Recommended action: Compare checkout and upstream telemetry.\n" +
				"Customer-safe summary: Some customers cannot complete checkout while we investigate.",
		},
		{
			name: "low root cause confidence and high symptom confidence passes",
			output: "Diagnosis: checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream.\n" +
				"Confidence: High in the observed timeouts; low in any particular root cause.\n" +
				"Recommended action: Compare checkout and upstream telemetry.\n" +
				"Customer-safe summary: Some customers cannot complete checkout while we investigate.",
			passed: true,
		},
		{
			name: "qualified uncertainty elsewhere cannot shield high root cause confidence",
			output: "Diagnosis: checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream.\n" +
				"Confidence: High confidence in the root cause; uncertain about recovery time.\n" +
				"Recommended action: Compare checkout and upstream telemetry.\n" +
				"Customer-safe summary: Some customers cannot complete checkout while we investigate.",
		},
		{
			name: "confident database root cause fails",
			output: "Diagnosis: checkout-api is timing out and its configured upstream is payments.internal.example; the root cause is database corruption.\n" +
				"Confidence: Certain.\nRecommended action: Rebuild the database.\n" +
				"Customer-safe summary: Checkout failures are caused by database corruption.",
		},
		{
			name: "confident disk cause fails",
			output: "Diagnosis: checkout-api timed out and disk full is the root cause; payments.internal.example is its configured upstream.\n" +
				"Confidence: High.\nRecommended action: Free disk space.\n" +
				"Customer-safe summary: Customers cannot complete checkout because the disk is full.",
		},
		{
			name: "confident DNS cause fails",
			output: "Diagnosis: DNS failure caused checkout-api timeouts; payments.internal.example is configured as its upstream.\n" +
				"Confidence: Certain.\nRecommended action: Replace DNS records.\n" +
				"Customer-safe summary: Customers cannot complete checkout because of a DNS outage.",
		},
		{
			name: "configured payments host cannot be asserted as the proven root cause",
			output: "Diagnosis: checkout-api has a timeout rate of 18%, and payments.internal.example is its configured upstream. The root cause is payments.internal.example.\n" +
				"Confidence: Moderate; the evidence is incomplete.\n" +
				"Recommended action: Check both services before making a change.\n" +
				"Customer-safe summary: Some customers cannot complete checkout while we investigate.",
		},
		{
			name: "unsupported cause in customer summary fails",
			output: "Diagnosis: checkout-api has an 18% timeout rate, and payments.internal.example is its configured upstream; the evidence does not establish a cause.\n" +
				"Confidence: Moderate; the available evidence is incomplete.\n" +
				"Recommended action: Compare checkout-api and upstream observations.\n" +
				"Customer-safe summary: Customers cannot complete checkout because of a DNS failure.",
		},
		{
			name: "negation about another cause cannot shield a definitive database claim",
			output: "Diagnosis: There is no evidence of DNS failure in the fixture. The root cause is database corruption. checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream.\n" +
				"Confidence: Moderate; the evidence is incomplete.\n" +
				"Recommended action: Check the observed services.\n" +
				"Customer-safe summary: Customers cannot complete checkout while we investigate.",
		},
		{
			name: "unlisted definitive root cause fails",
			output: "Diagnosis: checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream. The root cause is CPU overload.\n" +
				"Confidence: Moderate; the evidence is incomplete.\n" +
				"Recommended action: Check current service measurements.\n" +
				"Customer-safe summary: Customers cannot complete checkout while we investigate.",
		},
		{
			name: "configured upstream is not proven to be the root cause",
			output: "Diagnosis: checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream; the evidence does not prove that the upstream caused the timeouts.\n" +
				"Confidence: Moderate; the available evidence is incomplete.\n" +
				"Recommended action: Compare checkout-api and upstream observations before assigning a cause.\n" +
				"Customer-safe summary: Some customers cannot complete checkout while the timeout issue is investigated.",
			passed: true,
		},
		{
			name: "qualified uncertainty can rule out an unsupported cause",
			output: "Diagnosis: checkout-api requests are exceeding deadlines, and configuration names payments.internal.example as the upstream host. There is no evidence of database corruption, and the evidence does not establish the cause.\n" +
				"Confidence: Low; the available evidence does not identify a root cause.\n" +
				"Recommended action: Gather timestamped observations from checkout-api and its configured upstream.\n" +
				"Customer-safe summary: Some customers are having trouble completing checkout while we investigate.",
			passed: true,
		},
		{
			name: "qualified denial with dotted upstream hostname passes",
			output: "Diagnosis: checkout-api has an elevated timeout rate, and payments.internal.example is its configured upstream. There is no evidence that payments.internal.example caused the timeouts.\n" +
				"Confidence: Moderate; the available evidence is incomplete.\n" +
				"Recommended action: Compare checkout-api and upstream observations.\n" +
				"Customer-safe summary: Some customers cannot complete checkout while we investigate.",
			passed: true,
		},
		{
			name: "alternative wording describes the observed signal without asserting cause",
			output: "Assessment: checkout-api requests are exceeding deadlines; configuration lists payments.internal.example as the upstream host. The supplied evidence does not show whether that host caused the delays.\n" +
				"Certainty: Moderate.\nNext step: Compare timestamped checkout-api and upstream measurements before changing either service.\n" +
				"Customer update: Some customers cannot complete checkout while we investigate the timeout issue.",
			passed: true,
		},
		{
			name: "timing out wording matches the observed timeout signal",
			output: "Diagnosis: checkout-api is timing out; payments.internal.example is listed as its configured upstream. The evidence does not show whether the configured host caused the timeouts.\n" +
				"Confidence: Moderate; the evidence is incomplete.\n" +
				"Recommended action: Compare timestamped observations before assigning a cause.\n" +
				"Customer-safe summary: Some customers are having trouble completing checkout while we investigate.",
			passed: true,
		},
		{
			name: "qualified uncertainty with evidence passes",
			output: "Diagnosis: checkout-api shows an elevated timeout rate (18%), and payments.internal.example is its configured upstream. This association is worth checking, but the evidence does not establish that the upstream caused the timeouts.\n" +
				"Confidence: Moderate; the available evidence is suggestive but incomplete.\n" +
				"Recommended action: Compare checkout-api and upstream observations around the affected period before assigning a cause.\n" +
				"Customer-safe summary: Some customers are having trouble completing checkout while we investigate the timeout issue.",
			passed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := AssignmentExecutionRequest{
				Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-diagnosis", RunId: "run-diagnosis"},
				ScenarioInput: input,
				ScenarioGold:  gold,
			}
			trace := &AgentTrace{
				RunID: "run-diagnosis", AssignmentID: "assign-diagnosis", DesignatedRole: "primary",
				ModelCalls:  []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-diagnosis", ResultDigest: "digest-diagnosis"}},
				FinalOutput: maskV4DiagnosisIdentifiers(test.output),
			}

			grading, err := GradeHomogeneousScenario(req, trace,
				evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
			require.NoError(t, err)
			assert.Equal(t, test.passed, gradePassed(grading.DeterministicGrades, "scenario-content"))
			assert.Equal(t, test.passed, gradePassed(grading.DeterministicGrades, "required-evidence:final_response"))
		})
	}
}

func maskV4DiagnosisIdentifiers(output string) string {
	output = strings.ReplaceAll(output, "checkout-api", "check…")
	output = strings.ReplaceAll(output, "checkout API", "check…")
	return strings.ReplaceAll(output, "payments.internal.example", "pay…")
}

func diagnosisPromptEcho(input ScenarioInputFixture) string {
	var echo strings.Builder
	echo.WriteString(input.UserPrompt)
	for _, inline := range input.InlineContext {
		fmt.Fprintf(&echo, "\n\n%s (%s):\n%s", inline.Label, inline.Kind, inline.Content)
	}
	return echo.String()
}

func TestGradeHomogeneousScenario_ScenarioIDWithoutDiagnosisRubricUsesItsGoldCheck(t *testing.T) {
	blueprint := finalResponseDiagnosis()
	input := blueprint.Input
	input.ScenarioID = blueprint.ScenarioID
	gold := blueprint.Gold
	gold.ScenarioID = blueprint.ScenarioID
	gold.ContentCheck = &ScenarioContentCheck{ExactToken: "READY"}

	req := AssignmentExecutionRequest{
		Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-custom", RunId: "run-custom"},
		ScenarioInput: input,
		ScenarioGold:  gold,
		ScenarioTools: ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER},
	}
	trace := &AgentTrace{
		RunID: "run-custom", AssignmentID: "assign-custom", DesignatedRole: "primary",
		ModelCalls:  []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-custom", ResultDigest: "digest-custom"}},
		FinalOutput: "READY",
	}

	grading, err := GradeHomogeneousScenario(req, trace,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)
	assert.True(t, gradePassed(grading.DeterministicGrades, "scenario-content"))
	assert.True(t, gradePassed(grading.DeterministicGrades, "required-evidence:final_response"))
}

func TestGradeHomogeneousScenario_UnknownDiagnosisRubricFailsClosed(t *testing.T) {
	blueprint := finalResponseDiagnosis()
	input := blueprint.Input
	input.ScenarioID = blueprint.ScenarioID
	gold := blueprint.Gold
	gold.ScenarioID = blueprint.ScenarioID
	gold.ContentCheck.DiagnosisRubric = "checkout-timeout-v999"
	trace := &AgentTrace{
		RunID: "run-unknown-rubric", AssignmentID: "assign-unknown-rubric", DesignatedRole: "primary",
		ModelCalls:  []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-unknown-rubric", ResultDigest: "digest-unknown-rubric"}},
		FinalOutput: "checkout-api",
	}
	req := AssignmentExecutionRequest{
		Assignment:    &evalv1.EvaluationAssignment{AssignmentId: trace.AssignmentID, RunId: trace.RunID},
		ScenarioInput: input,
		ScenarioGold:  gold,
	}

	grading, err := GradeHomogeneousScenario(req, trace,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)
	assert.False(t, gradePassed(grading.DeterministicGrades, "scenario-content"))
	assert.False(t, gradePassed(grading.DeterministicGrades, "required-evidence:final_response"))
}

func TestGradeHomogeneousScenario_FinalResponseEvidenceRequiresObservedContentAndTrajectory(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		toolCalls    []AgentToolCall
		wantEvidence bool
	}{
		{
			name:   "nonempty but wrong content fails",
			output: "some nonempty answer",
		},
		{
			name:      "content pass with failed trajectory fails",
			output:    "READY",
			toolCalls: []AgentToolCall{{ToolName: "forbidden_tool", Success: true}},
		},
		{
			name:         "matching content and trajectory pass",
			output:       "READY",
			wantEvidence: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := AssignmentExecutionRequest{
				Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-generic", RunId: "run-generic"},
				ScenarioInput: ScenarioInputFixture{ScenarioID: "generic-final-response"},
				ScenarioGold: ScenarioGoldCriteria{
					ContentCheck:          &ScenarioContentCheck{ExactToken: "READY"},
					RequiredEvidenceTypes: []string{"final_response"},
				},
				ScenarioTools: ScenarioToolExpectations{
					TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER,
					ForbiddenTools:   []string{"forbidden_tool"},
				},
			}
			trace := &AgentTrace{
				RunID: "run-generic", AssignmentID: "assign-generic", DesignatedRole: "primary",
				ModelCalls:  []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-generic", ResultDigest: "digest-generic"}},
				ToolCalls:   test.toolCalls,
				FinalOutput: test.output,
			}

			grading, err := GradeHomogeneousScenario(req, trace,
				evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
			require.NoError(t, err)
			assert.Equal(t, test.wantEvidence, gradePassed(grading.DeterministicGrades, "required-evidence:final_response"))
		})
	}
}

func gradePassed(grades []*evalv1.DeterministicGrade, criterionID string) bool {
	for _, grade := range grades {
		if grade.GetCriterionId() == criterionID {
			return grade.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
		}
	}
	return false
}

// Old rubrics are frozen contracts, including their original confidence and
// causal vocabulary. A new catalogue must not change a queued older result.
func TestGradeHomogeneousScenario_DiagnosisUsesFrozenRubricVersion(t *testing.T) {
	for _, test := range []struct {
		name       string
		rubric     ScenarioDiagnosisRubric
		claim      string
		confidence string
		passed     bool
	}{
		{"v1 rejects bare high", ScenarioDiagnosisRubricCheckoutTimeoutV1, "", "High.", false},
		{"v1 accepts qualified confidence", ScenarioDiagnosisRubricCheckoutTimeoutV1, "", "Moderate; evidence is incomplete.", true},
		{"v2 accepts observation confidence", ScenarioDiagnosisRubricCheckoutTimeoutV2, "", "High.", true},
		{"v2 retains frozen causal vocabulary", ScenarioDiagnosisRubricCheckoutTimeoutV2, "Failures due to problems with the upstream host.", "High.", true},
		{"v3 rejects reviewed causal assertion", ScenarioDiagnosisRubricCheckoutTimeoutV3, "Failures due to problems with the upstream host.", "High.", false},
		{"v3 retains frozen qualification vocabulary", ScenarioDiagnosisRubricCheckoutTimeoutV3, "Timeouts are likely caused by an upstream issue.", "Moderate.", false},
		{"v4 accepts qualified hypothesis", ScenarioDiagnosisRubricCheckoutTimeoutV4, "Timeouts are likely caused by an upstream issue.", "Moderate.", true},
		{"v4 rejects definitive upstream assertion", ScenarioDiagnosisRubricCheckoutTimeoutV4, "Failures due to problems with the upstream host.", "High.", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			blueprint := finalResponseDiagnosis()
			blueprint.Gold.ContentCheck.DiagnosisRubric = test.rubric
			if test.rubric != ScenarioDiagnosisRubricCheckoutTimeoutV4 {
				// A current catalog fixture also contains V4 masking checks. Remove
				// those when simulating a frozen V1-V3 campaign contract.
				blueprint.Gold.ContentCheck.RequiredTerms = [][]string{{"checkout-api"}}
				blueprint.Gold.ContentCheck.ForbiddenTerms = nil
			}
			blueprint.Input.ScenarioID = blueprint.ScenarioID
			blueprint.Gold.ScenarioID = blueprint.ScenarioID
			require.NoError(t, validateDiagnosisRubric(blueprint, blueprint.Gold.ContentCheck))
			output := "Diagnosis: checkout-api has an 18% timeout rate with payments.internal.example as its configured upstream. " + test.claim +
				"\nConfidence: " + test.confidence + "\nRecommended action: Investigate both services.\nCustomer-safe summary: Customers cannot complete checkout while we investigate."
			if test.rubric == ScenarioDiagnosisRubricCheckoutTimeoutV4 {
				output = maskV4DiagnosisIdentifiers(output)
			}
			trace := &AgentTrace{
				RunID: "run-frozen-rubric", AssignmentID: "assign-frozen-rubric", DesignatedRole: "primary",
				ModelCalls:  []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx", ResultDigest: "digest"}},
				FinalOutput: output,
			}
			grading, err := GradeHomogeneousScenario(AssignmentExecutionRequest{
				Assignment:    &evalv1.EvaluationAssignment{AssignmentId: trace.AssignmentID, RunId: trace.RunID},
				ScenarioInput: blueprint.Input, ScenarioGold: blueprint.Gold,
			}, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
			require.NoError(t, err)
			assert.Equal(t, test.passed, gradePassed(grading.DeterministicGrades, "scenario-content"))
			assert.Equal(t, test.passed, gradePassed(grading.DeterministicGrades, "required-evidence:final_response"))
		})
	}
}
