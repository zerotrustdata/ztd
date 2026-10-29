// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestSecurityCatalogIncludesLiteInDefaultAndSmokeSuites(t *testing.T) {
	for name, build := range map[string]func() (*evalv1.EvaluationScenarioCatalog, map[string]ScenarioArtifacts, error){
		"default": BuildScenarioCatalog,
		"smoke":   BuildSmokeGateScenarioCatalog,
	} {
		t.Run(name, func(t *testing.T) {
			catalog, _, err := build()
			require.NoError(t, err)
			securityScenarios := 0
			for _, scenario := range catalog.GetScenarios() {
				if scenario.GetCategory() != evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_SECURITY_POLICY {
					continue
				}
				securityScenarios++
				assert.Contains(t, scenario.GetEligibleRoles(), evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, scenario.GetScenarioId())
			}
			require.Positive(t, securityScenarios)
		})
	}
}

func TestSecurityResistanceDoesNotRewardFailedToolFreeAnswers(t *testing.T) {
	result := securityResult("unsafe-answer", evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, nil, nil)
	assert.Equal(t, securityDispositionPersisted, securityDispositionFor(result, nil, false))
	assert.Equal(t, securityDispositionRefused, securityDispositionFor(result, nil, true))
}

func TestLiteSecurityScenariosSupplyContextAndSharedPromptsWithoutTools(t *testing.T) {
	catalog, artifacts, err := LoadScenarioCatalog()
	require.NoError(t, err)
	toolCatalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	executor := &CampaignAgentExecutor{catalog: toolCatalog, newID: func(prefix string) string { return prefix + "-security" }}
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetCategory() != evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_SECURITY_POLICY {
			continue
		}
		t.Run(scenario.GetScenarioId(), func(t *testing.T) {
			req := heterogeneousLiteRequest(t)
			req.Assignment.ScenarioId = scenario.GetScenarioId()
			req.ScenarioTools, err = scenarioToolsForAssignment(catalog, req.Assignment)
			require.NoError(t, err)
			require.True(t, req.ScenarioTools.ContextOnly)
			assert.Equal(t, scenario.GetForbiddenTools(), req.ScenarioTools.ForbiddenTools)
			require.NoError(t, json.Unmarshal(artifacts[scenario.GetScenarioId()].Input.Body, &req.ScenarioInput))
			require.NoError(t, json.Unmarshal(artifacts[scenario.GetScenarioId()].Gold.Body, &req.ScenarioGold))
			run, err := executor.newRun(req)
			require.NoError(t, err)
			assert.Empty(t, run.tools.Declarations())
			messages, err := run.conversation()
			require.NoError(t, err)
			system := messages[0].GetParts()[0].GetText()
			assert.True(t, strings.HasPrefix(system, constants.AgentSecurityPrompt))
			assert.Contains(t, system, "You are Triage,")
			user := messages[len(messages)-1].GetParts()[0].GetText()
			objective := contentJudgeObjective(req.ScenarioInput, run.trace, req.ScenarioGold.ContentCheck)
			for _, file := range req.ScenarioInput.WorkspaceFiles {
				assert.Contains(t, user, run.workspace.Render(file.Content))
				assert.NotContains(t, system, file.Content, "untrusted evidence must remain in the user message")
				assert.Contains(t, objective, file.Content, "the judge must see the evidence supplied to Lite")
			}
		})
	}
}

func TestSecurityEvidenceRequirementsKeepResponderToolsAndGradeLiteAnswers(t *testing.T) {
	for _, blueprint := range []ScenarioBlueprint{securityUntrustedRunbook(), securityReadOnlyControl()} {
		for _, role := range []string{"primary", "assistant", "lite"} {
			t.Run(blueprint.ScenarioID+"/"+role, func(t *testing.T) {
				grader := homogeneousGrader{
					req:  AssignmentExecutionRequest{ScenarioGold: blueprint.Gold},
					view: agentGradingView{Trace: &AgentTrace{DesignatedRole: role}},
				}
				grades := grader.gradeRequiredEvidenceTypes(trajectoryResult{Passed: true}, true)
				byID := make(map[string]*evalv1.DeterministicGrade)
				for _, grade := range grades {
					byID[grade.GetCriterionId()] = grade
				}
				if role == "lite" {
					assert.NotContains(t, byID, "required-evidence:tool_call")
					require.Contains(t, byID, "required-evidence:final_response")
					assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, byID["required-evidence:final_response"].GetStatus())
				} else {
					require.Contains(t, byID, "required-evidence:tool_call")
					assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, byID["required-evidence:tool_call"].GetStatus())
				}
			})
		}
	}
}

func TestLiteSecurityEvidenceOverridesRejectUnobservableRequirements(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		blueprint func() ScenarioBlueprint
		evidence  []string
		want      error
	}{
		{"tool requirement without override", securityReadOnlyControl, nil, constants.ErrEvaluationScenarioContractInvalid},
		{"unknown evidence", securityReadOnlyControl, []string{"invented-evidence"}, constants.ErrEvaluationScenarioContractInvalid},
		{"unsupported judge", securityReadOnlyControl, []string{"semantic_grade"}, constants.ErrEvaluationScenarioContractInvalid},
		{"policy evidence requires judged refusal", instructionExactFormat, []string{"policy_decision"}, constants.ErrEvaluationRefusalCriterionRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blueprint := tc.blueprint()
			blueprint.Gold.RoleCriteria[2].RequiredEvidenceTypes = tc.evidence
			require.ErrorIs(t, validateScenarioContract(blueprint, catalog), tc.want)
		})
	}
}

func TestLiteSecurityAggregatePublishesMeasuredSuccessAndFailure(t *testing.T) {
	assignments := []*evalv1.EvaluationAssignment{
		securityAssignment("safe", "lite-model"),
		securityAssignment("unsafe", "lite-model"),
	}
	for _, assignment := range assignments {
		assignment.GetHomogeneous().DesignatedRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	}
	results := map[string]*evalv1.EvaluationAssignmentResult{
		"safe":   securityResult("safe", evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, nil, nil),
		"unsafe": securityResult("unsafe", evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, nil, nil),
	}
	state, err := CollectRunAggregateState(assignments, results)
	require.NoError(t, err)
	require.NoError(t, state.attachScenarioCatalog(securityTestCatalog(), assignments, results))
	security := state.VariantRoles["lite-model:lite"].SecurityResistance
	require.NotNil(t, security.Value)
	assert.InDelta(t, 0.5, *security.Value, 1e-9)
	assert.Equal(t, uint32(1), security.Refused)
	assert.Equal(t, uint32(1), security.Persisted)
	assert.Empty(t, security.UnavailableReason)
	summary := buildModelSummaryRecord("security-run", "2026-10-29T12:00:00Z", state.VariantRoles["lite-model:lite"], CampaignRelease{})
	assert.Equal(t, "lite", summary.Role)
	require.NotNil(t, summary.SecurityResistance)
	require.NotNil(t, summary.SecurityResistance.Value)
	assert.InDelta(t, 0.5, *summary.SecurityResistance.Value, 1e-9)
}

func TestMixedRoleMemoryStillRequiresResponderRetrieval(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	blueprint := contextLiteMemory()
	blueprint.EligibleRoles = rolesAll
	require.ErrorIs(t, validateScenarioContract(blueprint, catalog), constants.ErrEvaluationPromptUnanswerable)
}
