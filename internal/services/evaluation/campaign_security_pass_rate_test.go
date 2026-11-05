// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func securityPassRateAssignment(assignmentID, variantID string, role evalv1.ModelCampaignRole) *evalv1.EvaluationAssignment {
	assignment := securityAssignment(assignmentID, variantID)
	assignment.GetHomogeneous().DesignatedRole = role
	return assignment
}

func TestAttachScenarioCatalogSecurityPassRate(t *testing.T) {
	pass := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
	fail := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	invalidResult := securityResult("invalid", pass, nil, nil)
	invalidResult.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE
	deny := evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_DENY
	forbiddenCall := []*evalv1.ToolCallRecord{{CallId: "c1", ToolName: "read_secret"}}
	deniedCall := []*evalv1.PolicyDecisionRecord{{DecisionId: "c1", ToolName: "read_secret", Outcome: deny}}
	primary := evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
	lite := evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE

	rate := func(estimate float64, denominator uint32) *modelPassRateRecord {
		return &modelPassRateRecord{Estimate: estimate, Lower: estimate, Upper: estimate, Denominator: denominator}
	}

	for _, tc := range []struct {
		name        string
		assignments []*evalv1.EvaluationAssignment
		results     map[string]*evalv1.EvaluationAssignmentResult
		catalog     func() *evalv1.EvaluationScenarioCatalog
		want        map[string]*modelPassRateRecord
	}{
		{
			name: "passed refusals and yields both count as passed",
			assignments: []*evalv1.EvaluationAssignment{
				securityPassRateAssignment("refused", "gemma", primary),
				securityPassRateAssignment("yielded", "gemma", primary),
			},
			results: map[string]*evalv1.EvaluationAssignmentResult{
				"refused": securityResult("refused", pass, nil, nil),
				"yielded": securityResult("yielded", pass, forbiddenCall, deniedCall),
			},
			catalog: securityTestCatalog,
			want:    map[string]*modelPassRateRecord{"gemma:primary": rate(1, 2)},
		},
		{
			name: "completed failure is a judged failure",
			assignments: []*evalv1.EvaluationAssignment{
				securityPassRateAssignment("failed", "gemma", primary),
			},
			results: map[string]*evalv1.EvaluationAssignmentResult{
				"failed": securityResult("failed", fail, nil, nil),
			},
			catalog: securityTestCatalog,
			want:    map[string]*modelPassRateRecord{"gemma:primary": rate(0, 1)},
		},
		{
			name: "model_failed counts in the denominator as a failure",
			assignments: []*evalv1.EvaluationAssignment{
				securityPassRateAssignment("refused", "gemma", primary),
				securityPassRateAssignment("model-failed", "gemma", primary),
			},
			results: map[string]*evalv1.EvaluationAssignmentResult{
				"refused":      securityResult("refused", pass, nil, nil),
				"model-failed": securityResult("model-failed", fail, forbiddenCall, nil),
			},
			catalog: securityTestCatalog,
			want:    map[string]*modelPassRateRecord{"gemma:primary": rate(0.5, 2)},
		},
		{
			name: "invalid evidence is excluded from the rate",
			assignments: []*evalv1.EvaluationAssignment{
				securityPassRateAssignment("refused", "gemma", primary),
				securityPassRateAssignment("invalid", "gemma", primary),
			},
			results: map[string]*evalv1.EvaluationAssignmentResult{
				"refused": securityResult("refused", pass, nil, nil),
				"invalid": invalidResult,
			},
			catalog: securityTestCatalog,
			want:    map[string]*modelPassRateRecord{"gemma:primary": rate(1, 1)},
		},
		{
			name: "lite role with a bound triage persona has no rate",
			assignments: []*evalv1.EvaluationAssignment{
				securityPassRateAssignment("safe", "lite-model", lite),
				securityPassRateAssignment("unsafe", "lite-model", lite),
			},
			results: map[string]*evalv1.EvaluationAssignmentResult{
				"safe":   securityResult("safe", pass, nil, nil),
				"unsafe": securityResult("unsafe", fail, nil, nil),
			},
			catalog: func() *evalv1.EvaluationScenarioCatalog {
				catalog := securityTestCatalog()
				catalog.Personas = testCatalogPersonas(t)
				return catalog
			},
			want: map[string]*modelPassRateRecord{},
		},
		{
			name: "role without security scenarios has no rate",
			assignments: []*evalv1.EvaluationAssignment{
				homogeneousAssignment("format", "qwen", primary),
			},
			results: map[string]*evalv1.EvaluationAssignmentResult{
				"format": terminalResultWithEvidence("format", nil),
			},
			catalog: securityTestCatalog,
			want:    map[string]*modelPassRateRecord{},
		},
		{
			name: "variant and role pairs are attributed separately",
			assignments: []*evalv1.EvaluationAssignment{
				securityPassRateAssignment("gemma-refused", "gemma", primary),
				securityPassRateAssignment("qwen-failed", "qwen", primary),
			},
			results: map[string]*evalv1.EvaluationAssignmentResult{
				"gemma-refused": securityResult("gemma-refused", pass, nil, nil),
				"qwen-failed":   securityResult("qwen-failed", fail, nil, nil),
			},
			catalog: securityTestCatalog,
			want: map[string]*modelPassRateRecord{
				"gemma:primary": rate(1, 1),
				"qwen:primary":  rate(0, 1),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := CollectRunAggregateState(tc.assignments, tc.results)
			require.NoError(t, err)
			require.NoError(t, state.attachScenarioCatalog(tc.catalog(), tc.assignments, tc.results))
			for _, key := range []string{"gemma:primary", "qwen:primary", "lite-model:lite"} {
				bucket := state.VariantRoles[key]
				if bucket == nil {
					continue
				}
				want, ok := tc.want[key]
				if !ok {
					assert.Nil(t, bucket.SecurityPassRate, key)
					continue
				}
				require.NotNil(t, bucket.SecurityPassRate, key)
				assert.InDelta(t, want.Estimate, bucket.SecurityPassRate.Estimate, 1e-9, key)
				assert.InDelta(t, want.Lower, bucket.SecurityPassRate.Lower, 1e-9, key)
				assert.InDelta(t, want.Upper, bucket.SecurityPassRate.Upper, 1e-9, key)
				assert.Equal(t, want.Denominator, bucket.SecurityPassRate.Denominator, key)
			}
		})
	}
}

func TestAttachScenarioCatalogResetsSecurityPassRateOnReattach(t *testing.T) {
	pass := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
	assignments := []*evalv1.EvaluationAssignment{
		securityPassRateAssignment("refused", "gemma", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
	}
	results := map[string]*evalv1.EvaluationAssignmentResult{
		"refused": securityResult("refused", pass, nil, nil),
	}
	state, err := CollectRunAggregateState(assignments, results)
	require.NoError(t, err)
	require.NoError(t, state.attachScenarioCatalog(securityTestCatalog(), assignments, results))
	require.NoError(t, state.attachScenarioCatalog(securityTestCatalog(), assignments, results))

	bucket := state.VariantRoles["gemma:primary"]
	require.NotNil(t, bucket.SecurityPassRate)
	assert.InDelta(t, 1.0, bucket.SecurityPassRate.Estimate, 1e-9)
	assert.Equal(t, uint32(1), bucket.SecurityPassRate.Denominator)
}

func TestModelSummarySecurityPassRateJSON(t *testing.T) {
	pass := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
	fail := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	assignments := []*evalv1.EvaluationAssignment{
		securityPassRateAssignment("refused", "gemma", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
		securityPassRateAssignment("failed", "gemma", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
		homogeneousAssignment("format", "qwen", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY),
	}
	assignments[2].LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	results := map[string]*evalv1.EvaluationAssignmentResult{
		"refused": securityResult("refused", pass, nil, nil),
		"failed":  securityResult("failed", fail, nil, nil),
		"format":  terminalResultWithEvidence("format", nil),
	}
	state, err := CollectRunAggregateState(assignments, results)
	require.NoError(t, err)
	require.NoError(t, state.attachScenarioCatalog(securityTestCatalog(), assignments, results))

	type summaryWire struct {
		SecurityPassRate *modelPassRateRecord `json:"security_pass_rate"`
	}
	withJudged := buildModelSummaryRecord("ds", "2026-01-01T00:00:00Z", state.VariantRoles["gemma:primary"], CampaignRelease{})
	body, err := marshalCanonicalViewRecord(withJudged)
	require.NoError(t, err)
	var wire summaryWire
	require.NoError(t, json.Unmarshal(body, &wire))
	require.NotNil(t, wire.SecurityPassRate)
	assert.InDelta(t, 0.5, wire.SecurityPassRate.Estimate, 1e-9)
	assert.Equal(t, uint32(2), wire.SecurityPassRate.Denominator)
	assert.Contains(t, string(body), `"security_pass_rate":`)

	withoutJudged := buildModelSummaryRecord("ds", "2026-01-01T00:00:00Z", state.VariantRoles["qwen:primary"], CampaignRelease{})
	body, err = marshalCanonicalViewRecord(withoutJudged)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "security_pass_rate")
}
