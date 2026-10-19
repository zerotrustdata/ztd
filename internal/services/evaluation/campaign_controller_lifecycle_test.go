// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func secondTestModelVariant() *evalv1.ModelVariant {
	return &evalv1.ModelVariant{
		VariantId:      "gemma3-4b",
		ProviderClass:  "ollama",
		ServedModelTag: "gemma3:4b",
		ModelDigest:    repeatHex('c', 64),
		ModelFamily:    "gemma3",
	}
}

func TestCampaignControllerCreateCampaignIsIdempotentForTheSameFrozenSpec(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	create := CampaignCreateRequest{
		Platform:          testPlatform,
		CampaignID:        f.req.CampaignID,
		Catalog:           f.req.Catalog,
		Inventory:         f.req.Inventory,
		ScenarioArtifacts: f.req.ScenarioArtifacts,
		RepetitionCount:   f.req.RepetitionCount,
	}

	first, err := f.controller.CreateCampaign(ctx, create)
	require.NoError(t, err)
	second, err := f.controller.CreateCampaign(ctx, create)
	require.NoError(t, err)
	assert.Equal(t, first.GetCampaignDigest(), second.GetCampaignDigest())
}

func TestCampaignControllerCreateCampaignRejectsADifferentSpecUnderTheSameID(t *testing.T) {
	f := newArchiveFixture(t)
	other, err := MaterializeModelRegistry(f.req.CampaignID, []*evalv1.ModelVariant{testModelVariant(), secondTestModelVariant()})
	require.NoError(t, err)

	_, err = f.controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		Platform:          testPlatform,
		CampaignID:        f.req.CampaignID,
		Catalog:           f.req.Catalog,
		Inventory:         other,
		ScenarioArtifacts: f.req.ScenarioArtifacts,
	})
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)
}

// newUnfrozenCampaignFixture is newArchiveFixture over an empty runtime tree, so
// the campaign ID is still free to freeze under a chosen posture.
func newUnfrozenCampaignFixture(t *testing.T) *archiveFixture {
	t.Helper()
	seeded := newArchiveFixture(t)
	raw, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	files := newVaultedEvidenceFiles(t, raw, keystoretest.NewMemoryKeyring())
	store := NewStore(files)
	controller := NewCampaignController(store, &stubCampaignExecutor{}, func() time.Time { return archiveTestNow }, func(prefix string) string { return prefix + "-1" })
	return &archiveFixture{files: files, store: store, controller: controller, req: seeded.req}
}

func postureCreateRequest(f *archiveFixture, posture evalv1.EvaluationGovernancePosture) CampaignCreateRequest {
	return CampaignCreateRequest{
		GovernancePosture: posture,
		Platform:          testPlatform,
		CampaignID:        f.req.CampaignID,
		Catalog:           f.req.Catalog,
		Inventory:         f.req.Inventory,
		ScenarioArtifacts: f.req.ScenarioArtifacts,
		RepetitionCount:   f.req.RepetitionCount,
	}
}

func TestCampaignControllerCreateCampaignFreezesTheRequestedPostureIntoSpecAndDigest(t *testing.T) {
	ctx := context.Background()
	unspecified := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY

	defaulted := newUnfrozenCampaignFixture(t)
	byDefault, err := defaulted.controller.CreateCampaign(ctx, postureCreateRequest(defaulted, unspecified))
	require.NoError(t, err)
	assert.Equal(t, doctrine, byDefault.GetGovernancePosture(), "an unspecified request keeps the default posture")

	requested := newUnfrozenCampaignFixture(t)
	frozen, err := requested.controller.CreateCampaign(ctx, postureCreateRequest(requested, ratify))
	require.NoError(t, err)
	assert.Equal(t, ratify, frozen.GetGovernancePosture())
	require.NoError(t, ValidateCampaignSpecDigest(frozen))
	assert.NotEqual(t, byDefault.GetCampaignDigest(), frozen.GetCampaignDigest(), "the posture is part of the campaign digest")

	persisted, err := requested.store.LoadCampaignSpec(ctx, requested.req.CampaignID)
	require.NoError(t, err)
	assert.Equal(t, ratify, persisted.GetGovernancePosture())
	assert.Equal(t, frozen.GetCampaignDigest(), persisted.GetCampaignDigest())
}

func TestCampaignControllerCreateCampaignIsIdempotentForTheSamePosture(t *testing.T) {
	f := newUnfrozenCampaignFixture(t)
	ctx := context.Background()
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY

	first, err := f.controller.CreateCampaign(ctx, postureCreateRequest(f, ratify))
	require.NoError(t, err)
	second, err := f.controller.CreateCampaign(ctx, postureCreateRequest(f, ratify))
	require.NoError(t, err)
	assert.Equal(t, first.GetCampaignDigest(), second.GetCampaignDigest())
	assert.Equal(t, ratify, second.GetGovernancePosture())
}

func TestCampaignControllerCreateCampaignRejectsAChangedPostureUnderTheSameID(t *testing.T) {
	f := newUnfrozenCampaignFixture(t)
	ctx := context.Background()
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY

	_, err := f.controller.CreateCampaign(ctx, postureCreateRequest(f, ratify))
	require.NoError(t, err)

	for _, changed := range []evalv1.EvaluationGovernancePosture{
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED,
	} {
		_, err = f.controller.CreateCampaign(ctx, postureCreateRequest(f, changed))
		require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict, changed.String())
	}

	frozen, err := f.store.LoadCampaignSpec(ctx, f.req.CampaignID)
	require.NoError(t, err)
	assert.Equal(t, ratify, frozen.GetGovernancePosture(), "a rejected recreation leaves the frozen posture untouched")
}

func TestCampaignControllerCreateCampaignRequiresInputs(t *testing.T) {
	f := newArchiveFixture(t)
	_, err := f.controller.CreateCampaign(context.Background(), CampaignCreateRequest{Platform: testPlatform, CampaignID: f.req.CampaignID})
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCampaignControllerStartRunBindsANewRunToAnExistingCampaign(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()

	run, err := f.controller.StartRun(ctx, RunStartRequest{
		PlatformRelease:            testPlatform.Release,
		CampaignID:                 f.req.CampaignID,
		RunID:                      "run-c",
		InferenceOperatorSessionID: "inf-2",
		DataOperatorSessionID:      "data-2",
	})
	require.NoError(t, err)
	assert.Equal(t, "run-c", run.GetRunId())
	assert.Equal(t, evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE, run.GetLane())
	assert.Equal(t, "inf-2", run.GetCampaignBinding().GetInferenceOperatorSessionId())
	assert.Equal(t, f.req.Inventory.RegistryDigest, run.GetCampaignBinding().GetModelRegistryDigest())

	runIDs, err := f.store.ListCampaignRunIDs(ctx, f.req.CampaignID)
	require.NoError(t, err)
	assert.Equal(t, []string{"run-a", "run-b", "run-c"}, runIDs)
}

func createMultiModelCampaign(t *testing.T, f *archiveFixture, campaignID string) {
	t.Helper()
	variants := testFormationCatalogVariants()
	inventory, err := MaterializeModelRegistry(campaignID, variants)
	require.NoError(t, err)
	formationSet, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID: campaignID,
		Seed:       17,
		Variants:   inventory.Variants,
	})
	require.NoError(t, err)
	_, err = f.controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		Platform:          testPlatform,
		CampaignID:        campaignID,
		Catalog:           f.req.Catalog,
		Inventory:         inventory,
		ScenarioArtifacts: f.req.ScenarioArtifacts,
		FormationStackSet: formationSet,
	})
	require.NoError(t, err)
}

func TestCampaignControllerStartRunRejectsAMultiModelModelRoleCampaign(t *testing.T) {
	f := newArchiveFixture(t)
	createMultiModelCampaign(t, f, "multi-model")

	for _, lane := range []evalv1.EvaluationLane{
		evalv1.EvaluationLane_EVALUATION_LANE_UNSPECIFIED,
		evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
	} {
		_, err := f.controller.StartRun(context.Background(), RunStartRequest{PlatformRelease: testPlatform.Release, CampaignID: "multi-model", RunID: "run-multi", Lane: lane})
		require.ErrorIs(t, err, constants.ErrEvaluationCampaignSubjectInvalid, lane.String())
	}
	exists, err := f.store.RunExists(context.Background(), "run-multi")
	require.NoError(t, err)
	assert.False(t, exists, "a rejected start must not persist a run")
}

func TestCampaignControllerStartRunAllowsAMultiModelSystemCampaign(t *testing.T) {
	f := newArchiveFixture(t)
	createMultiModelCampaign(t, f, "multi-system")

	run, err := f.controller.StartRun(context.Background(), RunStartRequest{
		PlatformRelease: testPlatform.Release,
		CampaignID:      "multi-system",
		RunID:           "run-system",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
	})
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM, run.GetLane())
}

func TestCampaignControllerStartRunRejectsExistingRunAndMissingCampaign(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()

	_, err := f.controller.StartRun(ctx, RunStartRequest{PlatformRelease: testPlatform.Release, CampaignID: f.req.CampaignID, RunID: "run-a"})
	require.ErrorContains(t, err, "run already exists")

	_, err = f.controller.StartRun(ctx, RunStartRequest{PlatformRelease: testPlatform.Release, CampaignID: "no-such-campaign", RunID: "run-x"})
	require.Error(t, err)

	_, err = f.controller.StartRun(ctx, RunStartRequest{PlatformRelease: testPlatform.Release, CampaignID: f.req.CampaignID})
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCampaignControllerCancelRunStopsEveryAssignmentWithoutAResult(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	binding := CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: f.req.Inventory.RegistryDigest,
		ModelRegistry:       f.req.Inventory.ToModelRegistryFreeze().Variants,
	}
	_, executed, err := f.controller.ExecuteNextAssignment(ctx, "run-a", binding, f.req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, executed)

	stopped, err := f.controller.CancelRun(ctx, "run-a")
	require.NoError(t, err)
	assert.Equal(t, 2, stopped)

	summary, err := f.controller.RunSummary(ctx, "run-a")
	require.NoError(t, err)
	assert.Zero(t, summary.QueuedCount)
	assert.Zero(t, summary.RunningCount)
	assert.Equal(t, uint32(3), summary.TerminalCount)
	assert.Equal(t, uint32(2), summary.StoppedCount)
	assert.Equal(t, "cancelled", CampaignRunStatus(summary, nil, false))

	next, ok, err := f.controller.ResumeNextAssignment(ctx, "run-a")
	require.NoError(t, err)
	assert.False(t, ok, "a cancelled run has nothing left to resume")
	assert.Nil(t, next)

	stoppedAgain, err := f.controller.CancelRun(ctx, "run-a")
	require.NoError(t, err)
	assert.Zero(t, stoppedAgain, "cancel is idempotent")

	other, err := f.controller.RunSummary(ctx, "run-b")
	require.NoError(t, err)
	assert.Equal(t, uint32(3), other.QueuedCount, "cancel is scoped to one run")
}

func TestCampaignControllerCancelRunStopsAnUnresolvedRunningAssignment(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	assignments, err := f.store.ListAssignments(ctx, "run-a")
	require.NoError(t, err)
	assignments[0].LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING
	require.NoError(t, f.store.SaveAssignment(ctx, assignments[0]))

	summary, err := f.controller.RunSummary(ctx, "run-a")
	require.NoError(t, err)
	assert.Equal(t, "interrupted", CampaignRunStatus(summary, nil, false), "a running assignment with no live holder is interrupted")
	assert.Equal(t, "running", CampaignRunStatus(summary, nil, true))

	stopped, err := f.controller.CancelRun(ctx, "run-a")
	require.NoError(t, err)
	assert.Equal(t, 3, stopped)

	summary, err = f.controller.RunSummary(ctx, "run-a")
	require.NoError(t, err)
	assert.Equal(t, "cancelled", CampaignRunStatus(summary, nil, false))
}

func TestCampaignControllerCancelRunRequiresAStoreAndKnownRun(t *testing.T) {
	_, err := (*CampaignController)(nil).CancelRun(context.Background(), "run-a")
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCampaignControllerStartRunRejectsABuildThatIsNotTheRecordedRelease(t *testing.T) {
	f := newArchiveFixture(t)

	_, err := f.controller.StartRun(context.Background(), RunStartRequest{CampaignID: f.req.CampaignID, RunID: "run-other-build", PlatformRelease: "v2.2.9"})
	require.ErrorIs(t, err, constants.ErrEvaluationPlatformReleaseMismatch)
	exists, err := f.store.RunExists(context.Background(), "run-other-build")
	require.NoError(t, err)
	assert.False(t, exists, "a rejected start must not persist a run")
}

func TestCampaignControllerStartRunEnforcesNoReleaseOnACampaignFrozenBeforeReleasesWereRecorded(t *testing.T) {
	f := newArchiveFixture(t)
	saveLegacyCampaign(t, f, "legacy-campaign")

	_, err := f.controller.StartRun(context.Background(), RunStartRequest{CampaignID: "legacy-campaign", RunID: "run-legacy", PlatformRelease: "v2.2.9"})
	require.NoError(t, err)
}
