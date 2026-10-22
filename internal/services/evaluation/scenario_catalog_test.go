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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestBuildScenarioCatalog_HasTwentySevenScenariosWithExpectedCategoryCounts(t *testing.T) {
	catalog, artifacts, err := BuildScenarioCatalog()
	require.NoError(t, err)
	require.NotNil(t, catalog)
	require.Len(t, catalog.Scenarios, 27)
	require.Len(t, artifacts, 27)
	require.NoError(t, ValidateScenarioCatalog(catalog, artifacts))
	assert.Equal(t, uint64(41), ComputeHomogeneousMatrixSize(1))

	counts := map[evalv1.EvaluationScenarioCategory]int{}
	for _, scenario := range catalog.Scenarios {
		counts[scenario.GetCategory()]++
	}
	assert.Equal(t, 4, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE])
	assert.Equal(t, 4, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TOOL_SELECTION])
	assert.Equal(t, 3, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TOOL_ARGUMENT])
	assert.Equal(t, 4, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TECHNICAL_ANALYSIS])
	assert.Equal(t, 3, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_ROUTING_DELEGATION])
	assert.Equal(t, 2, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_VERIFICATION])
	assert.Equal(t, 3, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_SECURITY_POLICY])
	assert.Equal(t, 3, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_RECOVERY])
	assert.Equal(t, 1, counts[evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_FINAL_RESPONSE])

	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == "final-response-diagnosis" {
			assert.Equal(t, finalResponseDiagnosisScenarioVersion, scenario.GetScenarioVersion())
		}
	}
}

func TestBuildScenarioCatalog_PreservesWorkflowOrder(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	var ids []string
	for _, scenario := range catalog.GetScenarios() {
		ids = append(ids, scenario.GetScenarioId())
	}
	assert.Equal(t, "instruction-exact-format", ids[0])
	assert.Equal(t, "final-response-diagnosis", ids[len(ids)-1])
	// Recovery belongs before the final synthesis, including the guided retry.
	assert.Equal(t, "recovery-error-guided-retry", ids[len(ids)-2])
}

func TestBuiltinCatalogVersionsTrackGradingSemantics(t *testing.T) {
	defaultCatalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	smokeCatalog, _, err := BuildSmokeGateScenarioCatalog()
	require.NoError(t, err)

	assert.Equal(t, DefaultSuiteVersion, defaultCatalog.GetCatalogRef().GetVersion())
	assert.Equal(t, DefaultSuiteVersion, smokeCatalog.GetCatalogRef().GetVersion())
	assert.Equal(t, "1.10.0", DefaultSuiteVersion)
	assert.Equal(t, "1.5.0", GraderVersion)
}

func TestValidateScenarioContract_AcceptsEveryAuthoredBlueprint(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	for _, blueprint := range scenarioBlueprints() {
		assert.NoError(t, validateScenarioContract(blueprint, catalog), blueprint.ScenarioID)
	}
}

func TestScenarioDiagnosisRubric_IsBoundIntoFrozenGoldArtifact(t *testing.T) {
	blueprint := finalResponseDiagnosis()
	selected := blueprint.Gold
	selected.ScenarioID = blueprint.ScenarioID
	unselected := selected
	plainCheck := *selected.ContentCheck
	plainCheck.DiagnosisRubric = ""
	unselected.ContentCheck = &plainCheck

	selectedArtifact, err := buildScenarioGoldArtifact(DefaultSuiteID, selected)
	require.NoError(t, err)
	unselectedArtifact, err := buildScenarioGoldArtifact(DefaultSuiteID, unselected)
	require.NoError(t, err)

	assert.NotEqual(t, selectedArtifact.Reference.GetArtifactId(), unselectedArtifact.Reference.GetArtifactId())
	assert.NotEqual(t, selectedArtifact.Body, unselectedArtifact.Body)
}

// tribunalInvestigationBlueprint is a planted-fault investigation scenario,
// which declares the intent-only run_shell_command through TribunalCommands.
func tribunalInvestigationBlueprint() ScenarioBlueprint {
	blueprints, err := InvestigationScenarioBlueprints()
	if err != nil {
		panic(err)
	}
	for _, blueprint := range blueprints {
		if blueprint.TribunalCommands {
			return blueprint
		}
	}
	panic("no investigation scenario declares tribunal commands")
}

func TestValidateScenarioContract_RejectsContractViolations(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)

	tests := []struct {
		name     string
		base     func() ScenarioBlueprint
		mutate   func(*ScenarioBlueprint)
		sentinel error
	}{
		{"unspecified trajectory policy", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.TrajectoryPolicy = evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_UNSPECIFIED
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"empty case title", toolArgGrepPattern, func(b *ScenarioBlueprint) { b.Input.Seed.CaseTitle = "  " }, constants.ErrEvaluationScenarioContractInvalid},
		{"case title names the evaluation", toolArgGrepPattern, func(b *ScenarioBlueprint) { b.Input.Seed.CaseTitle = "Eval run 3" }, constants.ErrEvaluationScenarioContractInvalid},
		{"case title carries a template token", toolArgGrepPattern, func(b *ScenarioBlueprint) { b.Input.Seed.CaseTitle = "Sweep {{workspace}}" }, constants.ErrEvaluationScenarioContractInvalid},
		{"allowed tool not in registry", toolArgGrepPattern, func(b *ScenarioBlueprint) { b.AllowedTools = append(b.AllowedTools, "no_such_tool") }, constants.ErrEvaluationScenarioContractInvalid},
		{"expected tool not allowed", toolArgGrepPattern, func(b *ScenarioBlueprint) { b.AllowedTools = []string{"read_file"} }, constants.ErrEvaluationScenarioContractInvalid},
		{"tool both allowed and forbidden", toolArgGrepPattern, func(b *ScenarioBlueprint) { b.ForbiddenTools = []string{constants.MCPToolSearchDirectory} }, constants.ErrEvaluationScenarioContractInvalid},
		{"ANSWER scenario declares expected tools", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.TrajectoryPolicy = evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"GUIDED scenario has no prompt hint", toolArgGrepPattern, func(b *ScenarioBlueprint) { b.Gold.PromptHint = nil }, constants.ErrEvaluationScenarioContractInvalid},
		{"hinted tool is not an expected tool", toolSelectFileRead, func(b *ScenarioBlueprint) {
			b.Gold.PromptHint.HintedTools = []string{constants.MCPToolSearchFile}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"GOVERNED scenario has no forbidden tools", securityPolicyBlockRun, func(b *ScenarioBlueprint) { b.ForbiddenTools = nil }, constants.ErrEvaluationScenarioContractInvalid},
		{"deterministic scenario has no content check", techNetworkSummary, func(b *ScenarioBlueprint) { b.Gold.ContentCheck = nil }, constants.ErrEvaluationScenarioContractInvalid},
		{"unknown diagnosis rubric", finalResponseDiagnosis, func(b *ScenarioBlueprint) {
			b.Gold.ContentCheck.DiagnosisRubric = "checkout-timeout-v999"
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"validator constrains unknown argument", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators[0].Arguments[0].Name = "no_such_argument"
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"regex constraint has no reject samples", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators[0].Arguments[0].RegexRejects = nil
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"required argument has no hint source", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.PromptHint.Arguments = b.Gold.PromptHint.Arguments[:1]
		}, constants.ErrEvaluationPromptUnanswerable},
		{"required argument has two hint sources", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.PromptHint.Arguments = append(b.Gold.PromptHint.Arguments, hintArg(constants.MCPToolSearchDirectory, "pattern", sourcePrompt, "AUTH_FAILURE"))
		}, constants.ErrEvaluationPromptUnanswerable},
		{"prompt source value missing from prompt", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Input.UserPrompt = "Search " + ScenarioWorkspaceToken + " for the failures."
		}, constants.ErrEvaluationPromptUnanswerable},
		{"seed source value missing from seed", recoveryErrorGuidedRetry, func(b *ScenarioBlueprint) {
			b.Gold.PromptHint.Arguments[0].Value = "PAYMENT_TIMEOUT"
		}, constants.ErrEvaluationPromptUnanswerable},
		{"workspace source but prompt never names the workspace", toolArgGrepPattern, func(b *ScenarioBlueprint) { b.Input.UserPrompt = "Search for the exact pattern AUTH_FAILURE." }, constants.ErrEvaluationPromptUnanswerable},
		{"workspace source names no workspace path", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.PromptHint.Arguments[1].Value = ""
		}, constants.ErrEvaluationPromptUnanswerable},
		{"workspace source value outside the workspace", toolSelectFileRead, func(b *ScenarioBlueprint) {
			b.Gold.PromptHint.Arguments[0].Value = "/etc/retry-config.env"
		}, constants.ErrEvaluationPromptUnanswerable},
		{"operator context source on the wrong argument", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.PromptHint.Arguments[1].Source = sourceOperatorContext
		}, constants.ErrEvaluationPromptUnanswerable},
		{"model authored source on the wrong argument", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.PromptHint.Arguments[0].Source = sourceModelAuthored
		}, constants.ErrEvaluationPromptUnanswerable},
		{"workspace file escapes the workspace", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Input.WorkspaceFiles[0].RelPath = "../escape.log"
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"duplicate workspace file", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Input.WorkspaceFiles = append(b.Input.WorkspaceFiles, b.Input.WorkspaceFiles[0])
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"workspace has only decoy files", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Input.WorkspaceFiles = b.Input.WorkspaceFiles[1:]
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"retired handoff evidence type", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.RequiredEvidenceTypes = append(b.Gold.RequiredEvidenceTypes, "handoff")
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"retired deterministic_grade evidence type", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.RequiredEvidenceTypes = append(b.Gold.RequiredEvidenceTypes, "deterministic_grade")
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"triage expectation grades no label", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.Players = &ScenarioPlayerExpectations{Triage: &TriageExpectation{}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"triage complexity outside catalog vocabulary", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.Players = &ScenarioPlayerExpectations{Triage: &TriageExpectation{Complexity: []constants.TriageComplexity{"medium"}}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"triage intent outside catalog vocabulary", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.Players = &ScenarioPlayerExpectations{Triage: &TriageExpectation{Intent: []constants.TriageIntent{"request"}}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"triage posture outside catalog vocabulary", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.Players = &ScenarioPlayerExpectations{Triage: &TriageExpectation{Posture: []constants.TriagePosture{"angry"}}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"command expectation has no constraint", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.Players = &ScenarioPlayerExpectations{Command: &ScenarioContentCheck{}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"marshal expectation accepts no risk level", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.Players = &ScenarioPlayerExpectations{Marshal: &MarshalExpectation{}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"marshal risk level outside catalog vocabulary", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.Players = &ScenarioPlayerExpectations{Marshal: &MarshalExpectation{Risk: []MarshalRisk{"CRITICAL"}}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"codex expectation has no constraint", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.Players = &ScenarioPlayerExpectations{Codex: &ScenarioContentCheck{}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"gold evidence reference has empty id", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.GoldEvidenceReferences = []ScenarioGoldEvidenceReference{{ReferenceID: "", ExpectedContent: "error"}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"gold evidence reference has whitespace in id", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.GoldEvidenceReferences = []ScenarioGoldEvidenceReference{{ReferenceID: " ref-1 ", ExpectedContent: "error"}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"gold evidence reference has duplicate id", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.GoldEvidenceReferences = []ScenarioGoldEvidenceReference{
				{ReferenceID: "ref-1", ExpectedContent: "error"},
				{ReferenceID: "ref-1", ExpectedContent: "timeout"},
			}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"gold evidence reference has no criteria", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.GoldEvidenceReferences = []ScenarioGoldEvidenceReference{{ReferenceID: "ref-1"}}
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"tribunal commands without run_shell_command allowed", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.TribunalCommands = true
		}, constants.ErrEvaluationScenarioContractInvalid},
		{"tribunal hint omits the shell command argument", tribunalInvestigationBlueprint, func(b *ScenarioBlueprint) {
			b.TrajectoryPolicy = evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_GUIDED
			b.ExpectedTools = []string{constants.MCPToolRunShellCommand}
			b.Gold.PromptHint = hint([]string{constants.MCPToolRunShellCommand})
		}, constants.ErrEvaluationPromptUnanswerable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blueprint := tt.base()
			require.NoError(t, validateScenarioContract(blueprint, catalog), "base blueprint must be valid")
			tt.mutate(&blueprint)
			err := validateScenarioContract(blueprint, catalog)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.sentinel)
			assert.Contains(t, err.Error(), blueprint.ScenarioID)
		})
	}
}

func TestBuildScenarioCatalog_RejectsABlueprintThatBreaksTheContract(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	blueprint := toolArgGrepPattern()
	blueprint.Gold.PromptHint = nil

	_, _, err = materializeScenarioBlueprint(DefaultSuiteID, blueprint, catalog)

	require.ErrorIs(t, err, constants.ErrEvaluationScenarioContractInvalid)
}

// TestHintLint_RejectsCatalog100GrepPrompt reproduces finding E3: the 1.0.0
// prompt named no path although the search tool requires one, so the task
// was not answerable. The current prompt names the workspace and passes.
func TestHintLint_RejectsCatalog100GrepPrompt(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)

	blueprint := toolArgGrepPattern()
	blueprint.Input.UserPrompt = "Search for the exact pattern AUTH_FAILURE using " + constants.MCPToolSearchDirectory + "."
	err = validateScenarioContract(blueprint, catalog)
	require.ErrorIs(t, err, constants.ErrEvaluationPromptUnanswerable)
	assert.Contains(t, err.Error(), `"path"`)

	require.NoError(t, validateScenarioContract(toolArgGrepPattern(), catalog))
}

func TestBuildScenarioCatalog_SeedsCarryConversationTurns(t *testing.T) {
	catalog, artifacts, err := BuildScenarioCatalog()
	require.NoError(t, err)
	require.NotNil(t, catalog)

	var input ScenarioInputFixture
	require.NoError(t, json.Unmarshal(artifacts["recovery-error-guided-retry"].Input.Body, &input))

	require.Len(t, input.Seed.Turns, 2)
	assert.Equal(t, "user", input.Seed.Turns[0].Sender)
	assert.Equal(t, "primary", input.Seed.Turns[1].Sender)

	// The primary turn quotes the error the tool really returns for a missing path.
	_, toolErr := (&mcp.SearchDirectoryTool{}).Execute(context.Background(), json.RawMessage(`{"pattern":"AUTH_FAILURE"}`))
	require.ErrorIs(t, toolErr, constants.ErrMissingRequiredField)
	assert.Contains(t, input.Seed.Turns[1].Content, toolErr.Error())
}

func TestBuildScenarioCatalog_PublicHintNeverCarriesValues(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	byID := make(map[string]*evalv1.EvaluationScenarioDefinition, len(catalog.Scenarios))
	for _, scenario := range catalog.Scenarios {
		byID[scenario.GetScenarioId()] = scenario
	}

	hinted := 0
	for _, blueprint := range scenarioBlueprints() {
		scenario := byID[blueprint.ScenarioID]
		require.NotNil(t, scenario, blueprint.ScenarioID)
		assert.Equal(t, blueprint.TrajectoryPolicy, scenario.GetTrajectoryPolicy(), blueprint.ScenarioID)
		private := blueprint.Gold.PromptHint
		if private == nil {
			assert.Nil(t, scenario.GetPromptHint(), blueprint.ScenarioID)
			continue
		}
		hinted++
		public := scenario.GetPromptHint()
		require.NotNil(t, public, blueprint.ScenarioID)
		assert.Equal(t, private.HintedTools, public.GetHintedTools())
		require.Len(t, public.GetArguments(), len(private.Arguments))
		body, err := protojson.Marshal(scenario)
		require.NoError(t, err)
		for index, argument := range private.Arguments {
			assert.Equal(t, argument.Name, public.GetArguments()[index].GetArgumentName())
			assert.Equal(t, argument.Source, public.GetArguments()[index].GetSource())
			if argument.Value != "" {
				assert.NotContains(t, string(body), argument.Value, "scenario %s leaks hint value for %s", blueprint.ScenarioID, argument.Name)
			}
		}
	}
	assert.Equal(t, 9, hinted)
}

func TestBuildScenarioCatalog_DigestIsStableAndBound(t *testing.T) {
	first, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	second, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	assert.Equal(t, first.GetCatalogDigest(), second.GetCatalogDigest())
	assert.Len(t, first.GetCatalogDigest(), 64)
	require.NoError(t, ValidateScenarioCatalogDigest(first))
}

func TestBuildScenarioCatalog_FixtureReferencesMatchEmbeddedBodies(t *testing.T) {
	catalog, artifacts, err := BuildScenarioCatalog()
	require.NoError(t, err)
	for _, scenario := range catalog.Scenarios {
		pair, ok := artifacts[scenario.GetScenarioId()]
		require.True(t, ok, "missing artifacts for %s", scenario.GetScenarioId())
		assert.Equal(t, scenario.GetInputFixtureRef().GetSha256(), pair.Input.Reference.GetSha256())
		assert.Equal(t, scenario.GetGoldCriteriaRef().GetSha256(), pair.Gold.Reference.GetSha256())
	}
}

func TestBuildSmokeGateScenarioCatalog_MaterializesFiveDiscriminativeScenarios(t *testing.T) {
	catalog, artifacts, err := BuildSmokeGateScenarioCatalog()
	require.NoError(t, err)
	require.NotNil(t, catalog)
	require.Len(t, catalog.Scenarios, SmokeGateScenarioCount)
	require.Len(t, artifacts, SmokeGateScenarioCount)

	require.NoError(t, ValidateSmokeGateScenarioCatalog(catalog, artifacts))
	require.NoError(t, ValidateScenarioCatalogDigest(catalog))

	seenIDs := make(map[string]bool)
	for _, sc := range catalog.Scenarios {
		seenIDs[sc.GetScenarioId()] = true
	}
	for _, expectedID := range SmokeGateScenarioIDs {
		assert.True(t, seenIDs[expectedID], "missing expected smoke scenario %s", expectedID)
	}
}

func TestHomogeneousAssignmentMatrix_FiltersBySmokeGateScenarioIDs(t *testing.T) {
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	variant := &evalv1.ModelVariant{
		VariantId:      "granite-3-8b",
		ServedModelTag: "granite3.3:8b",
	}

	assignments, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID:  "smoke-test-campaign",
		RunID:       "smoke-test-run",
		Catalog:     catalog,
		Variants:    []*evalv1.ModelVariant{variant},
		ScenarioIDs: SmokeGateScenarioIDs,
	})
	require.NoError(t, err)
	// instruction-exact-format and tech-error-diagnosis are Lite-only; the
	// three tool/policy/recovery scenarios are Primary and Assistant.
	assert.Len(t, assignments, 8)
}
