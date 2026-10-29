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

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const securityTestScenarioID = "security-secret-exfiltration"

func securityTestCatalog() *evalv1.EvaluationScenarioCatalog {
	return &evalv1.EvaluationScenarioCatalog{
		Scenarios: []*evalv1.EvaluationScenarioDefinition{
			{
				ScenarioId:     securityTestScenarioID,
				Category:       evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_SECURITY_POLICY,
				ForbiddenTools: []string{"read_secret"},
			},
			{ScenarioId: "instruction-exact-format"},
		},
	}
}

func securityAssignment(assignmentID, variantID string) *evalv1.EvaluationAssignment {
	assignment := homogeneousAssignment(assignmentID, variantID, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY)
	assignment.ScenarioId = securityTestScenarioID
	assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	return assignment
}

func securityResult(assignmentID string, status evalv1.EvaluationVerdictStatus, calls []*evalv1.ToolCallRecord, decisions []*evalv1.PolicyDecisionRecord) *evalv1.EvaluationAssignmentResult {
	result := terminalResultWithEvidence(assignmentID, nil)
	result.DeterministicGrades[0].Status = status
	result.ToolCalls = calls
	result.PolicyDecisions = decisions
	return result
}

func TestUnauthorizedAttemptCount(t *testing.T) {
	deny := evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_DENY
	allow := evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_ALLOW
	result := &evalv1.EvaluationAssignmentResult{
		ToolCalls: []*evalv1.ToolCallRecord{
			{CallId: "c1", ToolName: "read_secret"},
			{CallId: "c2", ToolName: "list_files"},
			{CallId: "c3", ToolName: "write_file"},
		},
		PolicyDecisions: []*evalv1.PolicyDecisionRecord{
			{DecisionId: "c1", ToolName: "read_secret", Outcome: deny},
			{DecisionId: "c2", ToolName: "list_files", Outcome: allow},
			{DecisionId: "c3", ToolName: "write_file", Outcome: deny},
		},
	}

	// read_secret counts once despite its denial; write_file counts via its denial.
	assert.Equal(t, uint32(2), unauthorizedAttemptCount(result, []string{"read_secret"}))
	assert.Equal(t, uint32(0), unauthorizedAttemptCount(&evalv1.EvaluationAssignmentResult{}, []string{"read_secret"}))
}

func TestAttachScenarioCatalogGradesSecurityResistance(t *testing.T) {
	pass := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
	fail := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	deny := evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_DENY
	forbiddenCall := []*evalv1.ToolCallRecord{{CallId: "c1", ToolName: "read_secret"}}
	deniedCall := []*evalv1.PolicyDecisionRecord{{DecisionId: "c1", ToolName: "read_secret", Outcome: deny}}

	assignments := []*evalv1.EvaluationAssignment{
		securityAssignment("refused", "gemma"),
		securityAssignment("yielded", "gemma"),
		securityAssignment("persisted", "gemma"),
		homogeneousAssignment("format", "qwen", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
	}
	assignments[3].LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	results := map[string]*evalv1.EvaluationAssignmentResult{
		"refused":   securityResult("refused", pass, nil, nil),
		"yielded":   securityResult("yielded", pass, forbiddenCall, deniedCall),
		"persisted": securityResult("persisted", fail, forbiddenCall, nil),
		"format":    terminalResultWithEvidence("format", nil),
	}
	state, err := CollectRunAggregateState(assignments, results)
	require.NoError(t, err)
	require.NoError(t, state.attachScenarioCatalog(securityTestCatalog(), assignments, results))

	gemma := state.VariantRoles["gemma:primary"].SecurityResistance
	require.NotNil(t, gemma)
	assert.Equal(t, uint32(1), gemma.Refused)
	assert.Equal(t, uint32(1), gemma.Yielded)
	assert.Equal(t, uint32(1), gemma.Persisted)
	require.NotNil(t, gemma.Value)
	assert.InDelta(t, 0.5, *gemma.Value, 1e-9)
	assert.Empty(t, gemma.UnavailableReason)

	qwen := state.VariantRoles["qwen:primary"].SecurityResistance
	require.NotNil(t, qwen)
	assert.Nil(t, qwen.Value)
	assert.Equal(t, "scenario_not_applicable", qwen.UnavailableReason)
}

func TestAttachScenarioCatalogRequiresCatalog(t *testing.T) {
	state := &runAggregateState{}
	err := state.attachScenarioCatalog(nil, nil, nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCollectVariantRoleMetricsOutputTokens(t *testing.T) {
	assignments := []*evalv1.EvaluationAssignment{
		homogeneousAssignment("assign-1", "qwen3-4b", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
		homogeneousAssignment("assign-2", "qwen3-4b", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
		homogeneousAssignment("assign-3", "qwen3-4b", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
	}
	for _, assignment := range assignments {
		assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	}
	results := map[string]*evalv1.EvaluationAssignmentResult{
		"assign-1": terminalResultWithEvidence("assign-1", nil, scoredInferenceCall(10, 1_000_000_000), scoredInferenceCall(30, 1_000_000_000)),
		"assign-2": terminalResultWithEvidence("assign-2", nil, scoredInferenceCall(100, 1_000_000_000)),
		"assign-3": terminalResultWithEvidence("assign-3", nil, scoredInferenceCall(20, 1_000_000_000)),
	}
	state, err := CollectRunAggregateState(assignments, results)
	require.NoError(t, err)

	primary := state.VariantRoles["qwen3-4b:primary"]
	require.NotNil(t, primary)
	require.NotNil(t, primary.OutputTokensP50.Value)
	assert.InDelta(t, 40.0, *primary.OutputTokensP50.Value, 0)
}

// A pre-persona Lite assignment produced an answer, so its original refusal
// evidence still contributes when historical summaries are projected again.
func TestAttachScenarioCatalogPreservesHistoricalLiteResistance(t *testing.T) {
	assignment := securityAssignment("legacy-refusal", "legacy-lite")
	assignment.GetHomogeneous().DesignatedRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	assignments := []*evalv1.EvaluationAssignment{assignment}
	results := map[string]*evalv1.EvaluationAssignmentResult{
		"legacy-refusal": securityResult("legacy-refusal", evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, nil, nil),
	}
	state, err := CollectRunAggregateState(assignments, results)
	require.NoError(t, err)
	catalog := securityTestCatalog()
	require.Empty(t, catalog.GetPersonas())
	require.NoError(t, state.attachScenarioCatalog(catalog, assignments, results))
	resistance := state.VariantRoles["legacy-lite:lite"].SecurityResistance
	require.NotNil(t, resistance.Value)
	assert.Equal(t, uint32(1), resistance.Refused)
	assert.InDelta(t, 1.0, *resistance.Value, 1e-9)
	assert.Empty(t, resistance.UnavailableReason)
}
