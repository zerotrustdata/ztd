// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCampaignsList_EmptyProject(t *testing.T) {
	env := newStoreRunEnv(t)

	out := env.mustRun(t, "campaigns", "list")
	assert.Contains(t, out, "No campaigns found")
}

func TestCampaignsList_AfterCreate(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	out := env.mustRun(t, "campaigns", "list")
	assert.Contains(t, out, "eval-a")
	assert.Contains(t, out, campaignLaneModelRole)
	assert.Equal(t, []string{"CAMPAIGN", "RELEASE", "LANE", "MODELS", "SCENARIOS", "REPS", "RUNS", "STATUS", "ARCHIVED"}, strings.Fields(strings.Split(out, "\n")[0]))

	var payload campaignListJSON
	require.NoError(t, env.runJSON(t, &payload, "campaigns", "list"))
	require.Len(t, payload.Campaigns, 1)
	row := payload.Campaigns[0]
	assert.Equal(t, "eval-a", row.CampaignID)
	assert.Equal(t, campaignLaneModelRole, row.Lane)
	assert.Equal(t, 1, row.ModelCount)
	assert.Equal(t, []string{"run-a-1"}, row.RunIDs)
	assert.False(t, row.Archived)
	assert.Equal(t, "scheduled", row.Status)
}

func TestCampaignsList_ShowsCampaignWithNoRuns(t *testing.T) {
	env := newStoreRunEnv(t)
	env.createCampaign(t, "eval-idle")

	var payload campaignListJSON
	require.NoError(t, env.runJSON(t, &payload, "campaigns", "list"))
	require.Len(t, payload.Campaigns, 1)
	assert.Equal(t, campaignStatusNoRuns, payload.Campaigns[0].Status)
	assert.Empty(t, payload.Campaigns[0].RunIDs)
}

func TestCampaignsList_StatusFilter(t *testing.T) {
	env := newStoreRunEnv(t)
	env.createCampaign(t, "eval-idle")
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	var payload campaignListJSON
	require.NoError(t, env.runJSON(t, &payload, "campaigns", "list", "--status", campaignStatusNoRuns))
	require.Len(t, payload.Campaigns, 1)
	assert.Equal(t, "eval-idle", payload.Campaigns[0].CampaignID)
}

func TestCampaignsShow_ReportsSpecAndRuns(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	out := env.mustRun(t, "campaigns", "show", "eval-a")
	assert.Contains(t, out, "Campaign: eval-a")
	assert.Contains(t, out, "qwen3:4b")
	assert.Contains(t, out, "run-a-1")

	var payload campaignShowJSON
	require.NoError(t, env.runJSON(t, &payload, "campaigns", "show", "eval-a"))
	assert.Equal(t, "eval-a", payload.CampaignID)
	assert.Equal(t, []string{"qwen3:4b"}, payload.Models)
	require.Len(t, payload.Runs, 1)
	assert.Equal(t, "run-a-1", payload.Runs[0].RunID)
	assert.Equal(t, uint64(testModelRoleCells(t)), payload.CellsPer)
}

func TestCampaignsShow_RejectsUnknownCampaign(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "campaigns", "show", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "campaigns show")
}

func TestCampaignsCreate_FromModelSelector(t *testing.T) {
	env := newStoreRunEnv(t)

	out := env.mustRun(t, "campaigns", "create", "eval-a", "qwen3:4b", "--reps", "2")
	assert.Contains(t, out, "Campaign created")
	assert.Contains(t, out, "ztd eval runs start eval-a")

	var payload campaignShowJSON
	require.NoError(t, env.runJSON(t, &payload, "campaigns", "show", "eval-a"))
	assert.Equal(t, uint32(2), payload.RepetitionCount)
	assert.Equal(t, []string{"qwen3:4b"}, payload.Models)
	assert.True(t, payload.HoldContext, "repetitions hold the investigation by default")
}

func TestCampaignsCreate_HoldContextFlag(t *testing.T) {
	env := newStoreRunEnv(t)

	var fresh campaignCreateJSON
	require.NoError(t, env.runJSON(t, &fresh, "campaigns", "create", "eval-fresh", "qwen3:4b", "--reps", "3", "--hold-context=false"))
	assert.False(t, fresh.HoldContext)
	var single campaignCreateJSON
	require.NoError(t, env.runJSON(t, &single, "campaigns", "create", "eval-single", "qwen3:4b"))
	assert.False(t, single.HoldContext, "one repetition has nothing to hold")

	_, err := env.run(t, "campaigns", "create", "eval-h", "qwen3:4b", "--hold-context")
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestCampaignsCreate_FreezesNoGovernancePosture(t *testing.T) {
	env := newStoreRunEnv(t)

	env.mustRun(t, "campaigns", "create", "eval-any-posture", "qwen3:4b")
	spec, err := env.store(t).LoadCampaignSpec(context.Background(), "eval-any-posture")
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED, spec.GetGovernancePosture(), "each run freezes the live Gateway posture")
	require.NoError(t, evaluation.ValidateCampaignSpecDigest(spec))

	out, err := env.run(t, "campaigns", "create", "eval-flag", "qwen3:4b", "--posture", "ratify")
	require.Error(t, err, "campaigns create no longer takes a posture: %s", out)
}

func TestCampaignsCreate_FreezesPersistentInvestigationAndDataOperator(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "campaigns", "create", "eval-persistent", "qwen3:4b",
		"--investigation", "persistent:checkout-incident",
		"--data-operator", "operator-stable",
	)
	require.NoError(t, err)

	spec, err := env.store(t).LoadCampaignSpec(context.Background(), "eval-persistent")
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT, spec.GetInvestigation().GetMode())
	assert.Equal(t, "checkout-incident", spec.GetInvestigation().GetInvestigationId())
	assert.Equal(t, "operator-stable", spec.GetInvestigation().GetDataOperatorId())

	var show campaignShowJSON
	require.NoError(t, env.runJSON(t, &show, "campaigns", "show", "eval-persistent"))
	assert.Equal(t, "persistent", show.InvestigationMode)
	assert.Equal(t, "checkout-incident", show.InvestigationID)
	assert.Equal(t, "operator-stable", show.DataOperatorID)
}

func TestCampaignsCreate_RejectsInvalidInvestigationFlagCombinations(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "persistent requires a Data Operator", args: []string{"--investigation", "persistent:checkout-incident"}},
		{name: "fresh rejects a Data Operator", args: []string{"--data-operator", "operator-stable"}},
		{name: "unknown investigation mode", args: []string{"--investigation", "shared:checkout-incident"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newStoreRunEnv(t)
			args := append([]string{"campaigns", "create", "eval-invalid", "qwen3:4b"}, test.args...)
			_, err := env.run(t, args...)
			require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
		})
	}
}

func TestCampaignsCreate_JSONReportsFrozenSpec(t *testing.T) {
	env := newStoreRunEnv(t)

	var payload campaignCreateJSON
	require.NoError(t, env.runJSON(t, &payload, "campaigns", "create", "eval-a", testQwenVariant().GetServedModelTag()))
	assert.Equal(t, "eval-a", payload.CampaignID)
	assert.Equal(t, campaignLaneModelRole, payload.Lane)
	assert.Equal(t, 1, payload.ModelCount)
	catalog, _, err := evaluation.LoadScenarioCatalog()
	require.NoError(t, err)
	assert.Equal(t, evaluation.ModelRoleMatrixSize(catalog, 1, 1, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE), payload.CellsPerRun)
	assert.Equal(t, evaluation.ModelRoleMatrixSize(catalog, 1, 1, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS), payload.CellsPerRunL2)
	assert.Equal(t, evaluation.DefaultJudgeServedModelTag, payload.Judge, "the default judge is frozen without a --judge flag")
	assert.NotEmpty(t, payload.ModelRegistryDigest)
	assert.NotEmpty(t, payload.CatalogDigest)
}

// TestCampaignsCreate_RejectsAJudgeAbsentFromTheRegistry: the judge is pinned
// from the model registry, so a tag the registry does not serve never freezes.
func TestCampaignsCreate_RejectsAJudgeAbsentFromTheRegistry(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "campaigns", "create", "eval-a", testQwenVariant().GetServedModelTag(), "--judge", "absent:1b")
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignJudgeInvalid)

	_, err = env.run(t, "campaigns", "show", "eval-a")
	require.Error(t, err, "a rejected create must not persist the campaign")
}

func TestCampaignsCreate_RequiresSelection(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "campaigns", "create", "eval-a")
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrEvaluationSelectionEmpty)

	_, err = env.run(t, "campaigns")
	require.NoError(t, err)
}

func TestCampaignsCreate_RejectsUnknownModel(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "campaigns", "create", "eval-a", "absent:1b")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
}

func TestCampaignsCreate_RejectsEmptyRegistry(t *testing.T) {
	env := newStoreRunEnv(t)
	require.NoError(t, env.fileSvc(t).Remove(context.Background(), evaluation.DefaultModelInventoryRelPath))

	_, err := env.run(t, "campaigns", "create", "eval-a", "--all")
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrEvaluationSelectionEmpty)
	assert.Contains(t, err.Error(), "registry is empty")
}

func TestCampaignsCreate_RejectsConflictingModes(t *testing.T) {
	env := newStoreRunEnv(t)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "the removed all-formations flag", args: []string{"campaigns", "create", "eval-f", "--all-formations"}, want: "unknown flag: --all-formations"},
		{name: "the removed lane flag", args: []string{"campaigns", "create", "eval-f", "--all", "--lane", "system"}, want: "unknown flag: --lane"},
		{name: "the removed seed flag", args: []string{"campaigns", "create", "eval-f", "--all", "--seed", "17"}, want: "unknown flag: --seed"},
		{name: "positional models with a filter", args: []string{"campaigns", "create", "eval-f", "qwen3:4b", "--family", "qwen"}, want: "cannot be combined"},
		{name: "formation and model selectors are exclusive", args: []string{"campaigns", "create", "eval-f", "--formations", "qwen-powerhouse", "--all"}, want: "--formations cannot be combined"},
		{name: "formation repetitions are currently unsupported", args: []string{"campaigns", "create", "eval-f", "--formations", "qwen-powerhouse", "--reps", "2"}, want: "formation campaigns currently require one repetition"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := env.run(t, test.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestCampaignsCreate_FromCatalogFormations(t *testing.T) {
	env := newStoreRunEnv(t)
	variants := evaluation.FormationCatalogFixtureVariants(func(string) string { return repeatTestHex('a') })
	writeTestFrozenInventory(t, env.root, variants...)

	var payload campaignCreateJSON
	require.NoError(t, env.runJSON(t, &payload, "campaigns", "create", "eval-system", "--formations", "ultra-efficient-speedster"))
	assert.Equal(t, campaignLaneSystem, payload.Lane)
	assert.Equal(t, 1, payload.StackCount)
	catalog, _, err := evaluation.LoadScenarioCatalog()
	require.NoError(t, err)
	assert.Equal(t, evaluation.FormationMatrixSize(catalog, 1, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE), payload.CellsPerRun)
	assert.Equal(t, evaluation.FormationMatrixSize(catalog, 1, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS), payload.CellsPerRunL2)
	assert.Greater(t, payload.ModelCount, 1)

	spec, err := env.store(t).LoadCampaignSpec(context.Background(), "eval-system")
	require.NoError(t, err)
	require.NotEmpty(t, spec.GetFormationStackSetDigest())
	stackSet, err := env.store(t).LoadHeterogeneousStackSet(context.Background(), "eval-system")
	require.NoError(t, err)
	assert.Equal(t, spec.GetFormationStackSetDigest(), stackSet.SetDigest)
	assert.Equal(t, []string{"ultra-efficient-speedster"}, []string{stackSet.Stacks[0].GetStackId()})
	assert.NoError(t, evaluation.ValidateCampaignSpecDigest(spec))

	var show campaignShowJSON
	require.NoError(t, env.runJSON(t, &show, "campaigns", "show", "eval-system"))
	assert.Equal(t, campaignLaneSystem, show.Lane)
	assert.Equal(t, 1, show.StackCount)
	assert.Equal(t, payload.CellsPerRun, show.CellsPer)
	assert.Equal(t, payload.CellsPerRunL2, show.CellsPerL2)
}

func TestCampaignsCreate_UsesProjectFormationOverlay(t *testing.T) {
	for _, change := range []string{"add", "replace", "remove"} {
		t.Run(change, func(t *testing.T) {
			env := newStoreRunEnv(t)
			variants := evaluation.FormationCatalogFixtureVariants(func(string) string { return repeatTestHex('a') })
			writeTestFrozenInventory(t, env.root, variants...)
			defaults, err := evaluation.NewExecutionTopologies()
			require.NoError(t, err)
			formation, err := defaults.Formation("gemma-cascade")
			require.NoError(t, err)
			formation.ID = "ultra-efficient-speedster"
			overlay := &evaluation.FormationCatalogOverlay{}
			switch change {
			case "add":
				formation.ID = "project-formation"
				overlay.Formations = []evaluation.Formation{formation}
			case "replace":
				overlay.Formations = []evaluation.Formation{formation}
			case "remove":
				overlay.RemovedFormationIDs = []string{formation.ID}
			}
			require.NoError(t, newFormationCatalog(env.root).save(overlay))
			var listed formationListJSON
			require.NoError(t, env.runJSON(t, &listed, "formations", "list"))
			var ids []string
			for _, entry := range listed.Formations {
				ids = append(ids, entry.ID)
			}
			_, err = env.run(t, "campaigns", "create", "eval-overlay", "--formations", formation.ID)
			if change == "remove" {
				assert.NotContains(t, ids, formation.ID)
				require.ErrorIs(t, err, constants.ErrFormationInvalid)
				return
			}
			assert.Contains(t, ids, formation.ID)
			require.NoError(t, err)
			set, err := env.store(t).LoadHeterogeneousStackSet(context.Background(), "eval-overlay")
			require.NoError(t, err)
			require.Len(t, set.Stacks, 1)
			assert.Equal(t, formation.ID, set.Stacks[0].GetStackId())
			spec, err := env.store(t).LoadCampaignSpec(context.Background(), "eval-overlay")
			require.NoError(t, err)
			bound, err := evaluation.BindFormation(evaluation.FormationBindingRequest{Stack: set.Stacks[0], Variants: spec.GetModelRegistry()})
			require.NoError(t, err)
			assert.Equal(t, formation.Primary.ServedModelTag, bound.Primary.ServedModelTag)
			assert.Equal(t, formation.Assistant.ServedModelTag, bound.Assistant.ServedModelTag)
			assert.Equal(t, formation.Lite.ServedModelTag, bound.Lite.ServedModelTag)
		})
	}
}

func TestCampaignsCreate_FormationsFreezeIdempotentlyAndRejectSetOrLaneChanges(t *testing.T) {
	variants := evaluation.FormationCatalogFixtureVariants(func(string) string { return repeatTestHex('b') })
	t.Run("same formation set is idempotent and a different set conflicts", func(t *testing.T) {
		env := newStoreRunEnv(t)
		writeTestFrozenInventory(t, env.root, variants...)
		args := []string{"campaigns", "create", "eval-system", "--formations", "ultra-efficient-speedster,qwen-powerhouse"}
		_, err := env.run(t, args...)
		require.NoError(t, err)
		first, err := env.store(t).LoadCampaignSpec(context.Background(), "eval-system")
		require.NoError(t, err)
		_, err = env.run(t, "campaigns", "create", "eval-system", "--formations", "qwen-powerhouse,ultra-efficient-speedster")
		require.NoError(t, err)
		same, err := env.store(t).LoadCampaignSpec(context.Background(), "eval-system")
		require.NoError(t, err)
		assert.Equal(t, first.GetCampaignDigest(), same.GetCampaignDigest())
		_, err = env.run(t, "campaigns", "create", "eval-system", "--formations", "qwen-powerhouse")
		require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)
	})

	t.Run("model-role campaign cannot become system lane", func(t *testing.T) {
		env := newStoreRunEnv(t)
		_, err := env.run(t, "campaigns", "create", "eval-collision", "qwen3:4b")
		require.NoError(t, err)
		writeTestFrozenInventory(t, env.root, variants...)
		_, err = env.run(t, "campaigns", "create", "eval-collision", "--formations", "ultra-efficient-speedster")
		require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)
	})

	t.Run("system campaign cannot become model-role lane", func(t *testing.T) {
		env := newStoreRunEnv(t)
		writeTestFrozenInventory(t, env.root, variants...)
		_, err := env.run(t, "campaigns", "create", "eval-collision", "--formations", "ultra-efficient-speedster")
		require.NoError(t, err)
		_, err = env.run(t, "campaigns", "create", "eval-collision", variants[0].GetServedModelTag())
		require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)
	})
}

func TestCampaignsCreate_InvalidCampaignID(t *testing.T) {
	env := newStoreRunEnv(t)

	// A colon is rejected at admission: it would pass here but fail every
	// later evidence write, stranding the run after its first assignment.
	for _, campaignID := range []string{"../escape", "eval-1022-13:30"} {
		_, err := env.run(t, "campaigns", "create", campaignID, "--all")
		require.Error(t, err, campaignID)
		assert.ErrorIs(t, err, constants.ErrEvidencePathElementInvalid, campaignID)
	}
}

func TestCampaignsCreate_IsIdempotentForTheSameFrozenSpec(t *testing.T) {
	env := newStoreRunEnv(t)

	env.mustRun(t, "campaigns", "create", "eval-a", "qwen3:4b")
	before, err := evaluation.NewStore(env.fileSvc(t)).LoadCampaignSpec(context.Background(), "eval-a")
	require.NoError(t, err)

	env.mustRun(t, "campaigns", "create", "eval-a", "qwen3:4b")
	after, err := evaluation.NewStore(env.fileSvc(t)).LoadCampaignSpec(context.Background(), "eval-a")
	require.NoError(t, err)
	assert.Equal(t, before.GetCampaignDigest(), after.GetCampaignDigest())
}

func TestCampaignsCreate_ConflictsWhenTheSpecDiffers(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "campaigns", "create", "eval-a", "qwen3:4b", "--reps", "1")

	_, err := env.run(t, "campaigns", "create", "eval-a", "qwen3:4b", "--reps", "3")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)
}

// A catalog change leaves the frozen campaign untouched. With versioning on, the
// next create freezes the new catalog under a digest-suffixed ID, once.
func TestCreateCampaign_VersionsOnCatalogChange(t *testing.T) {
	env := newStoreRunEnv(t)
	ctx := context.Background()
	store := evaluation.NewStore(env.fileSvc(t))
	env.createCampaign(t, "eval-a")
	frozen, err := store.LoadCampaignSpec(ctx, "eval-a")
	require.NoError(t, err)

	changedCatalog := campaignCreateSpec{
		Platform:    testPlatform,
		CampaignID:  "eval-a",
		Variants:    []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions: 1,
		SuiteID:     evaluation.SmokeSuiteID,
		Judge:       testJudgeVariant(),
	}
	_, err = createCampaign(ctx, env.deps, env.fileSvc(t), changedCatalog)
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict, "versioning is opt-in")

	changedCatalog.VersionOnCatalogChange = true
	first, err := createCampaign(ctx, env.deps, env.fileSvc(t), changedCatalog)
	require.NoError(t, err)
	versionedID := first.Spec.GetCampaignId()
	assert.Equal(t, "eval-a-"+first.Spec.GetCatalogDigest()[:8], versionedID)
	assert.NotEqual(t, frozen.GetCatalogDigest(), first.Spec.GetCatalogDigest())

	again, err := createCampaign(ctx, env.deps, env.fileSvc(t), changedCatalog)
	require.NoError(t, err)
	assert.Equal(t, versionedID, again.Spec.GetCampaignId(), "the same catalog resolves to the campaign already frozen for it")
	assert.Equal(t, first.Spec.GetCampaignDigest(), again.Spec.GetCampaignDigest())

	unchanged, err := store.LoadCampaignSpec(ctx, "eval-a")
	require.NoError(t, err)
	assert.Equal(t, frozen.GetCampaignDigest(), unchanged.GetCampaignDigest(), "the original frozen campaign is never rewritten")
}

func TestCreateCampaign_VersioningKeepsTheCampaignIDWhileTheCatalogIsUnchanged(t *testing.T) {
	env := newStoreRunEnv(t)
	env.createCampaign(t, "eval-a")

	result, err := createCampaign(context.Background(), env.deps, env.fileSvc(t), campaignCreateSpec{
		Platform:               testPlatform,
		CampaignID:             "eval-a",
		Variants:               []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions:            1,
		VersionOnCatalogChange: true,
		Judge:                  testJudgeVariant(),
	})
	require.NoError(t, err)
	assert.Equal(t, "eval-a", result.Spec.GetCampaignId())
}

// Versioning answers a catalog change only. Any other drift under the same
// catalog is still a conflict, not a reason to mint another campaign.
func TestCreateCampaign_VersioningStillConflictsOnNonCatalogDrift(t *testing.T) {
	env := newStoreRunEnv(t)
	env.createCampaign(t, "eval-a")

	_, err := createCampaign(context.Background(), env.deps, env.fileSvc(t), campaignCreateSpec{
		Platform:               testPlatform,
		CampaignID:             "eval-a",
		Variants:               []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions:            3,
		VersionOnCatalogChange: true,
		Judge:                  testJudgeVariant(),
	})
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)
}

func TestCampaignsCreate_RejectsMultiModelModelRoleCampaign(t *testing.T) {
	env := newStoreRunEnv(t)
	writeTestFrozenInventory(t, env.root, threeModelRegistry()...)

	_, err := env.run(t, "campaigns", "create", "eval-many", "--all")
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignSubjectInvalid)

	_, err = env.run(t, "campaigns", "show", "eval-many")
	require.Error(t, err, "a rejected create must not persist the campaign")
}

// A campaign frozen with catalog formations schedules its system-lane
// assignments from the persisted formation set.
func TestRunsStart_SchedulesSystemLaneCampaign(t *testing.T) {
	env := newGatewayRunEnv(t)
	ctx := context.Background()
	variants := evaluation.FormationCatalogFixtureVariants(func(string) string { return repeatTestHex('a') })
	catalog, artifacts, err := evaluation.LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := evaluation.MaterializeModelRegistry("eval-system", variants)
	require.NoError(t, err)
	stackSet, err := evaluation.GenerateFormationCatalogStackSet(evaluation.FormationCatalogStackGenerationRequest{
		CampaignID:   "eval-system",
		Variants:     inventory.Variants,
		FormationIDs: []string{"ultra-efficient-speedster"},
	})
	require.NoError(t, err)
	controller := evaluation.NewCampaignController(env.store(t), nil, env.deps.now, adaptNewID(env.deps.newID))
	_, err = controller.InitializeCampaign(ctx, evaluation.CampaignInitRequest{
		CampaignID: "eval-system", RunID: "run-system", Catalog: catalog, Inventory: inventory,
		ScenarioArtifacts: artifacts, RepetitionCount: 1, Platform: testPlatform, Governance: testGovernance,
		Lane: evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM, FormationStackSet: stackSet,
		Judge: testJudgeVariant(),
	})
	require.NoError(t, err)
	_, err = env.run(t, "runs", "start", "eval-system", "--prepare-only", "--publish=false")
	require.NoError(t, err)

	runIDs, err := env.store(t).ListCampaignRunIDs(ctx, "eval-system")
	require.NoError(t, err)
	require.Len(t, runIDs, 2)
	var scheduled int
	for _, runID := range runIDs {
		assignments, err := env.store(t).ListAssignments(ctx, runID)
		require.NoError(t, err)
		if len(assignments) > 0 {
			scheduled = len(assignments)
		}
	}
	assert.Equal(t, int(evaluation.FormationMatrixSize(catalog, 1, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)), scheduled)
}
