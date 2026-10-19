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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func createBoundSystemCampaign(t *testing.T, fixture *archiveFixture, campaignID string) (*ModelInventoryFreeze, *HeterogeneousStackSet) {
	t.Helper()
	variants := testFormationCatalogVariants()
	inventory, err := MaterializeModelRegistry(campaignID, variants)
	require.NoError(t, err)
	stackSet, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   campaignID,
		Seed:         17,
		Variants:     inventory.Variants,
		FormationIDs: []string{"qwen-powerhouse"},
	})
	require.NoError(t, err)
	_, err = fixture.controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		Platform:          testPlatform,
		CampaignID:        campaignID,
		Catalog:           fixture.req.Catalog,
		Inventory:         inventory,
		ScenarioArtifacts: fixture.req.ScenarioArtifacts,
		FormationStackSet: stackSet,
	})
	require.NoError(t, err)
	return inventory, stackSet
}

func TestCampaignFormationBinding_RejectsMultipleModelsWithoutFormations(t *testing.T) {
	f := newArchiveFixture(t)
	inventory, err := MaterializeModelRegistry("invalid-multi", testFormationCatalogVariants())
	require.NoError(t, err)
	_, err = f.controller.CreateCampaign(t.Context(), CampaignCreateRequest{
		CampaignID: "invalid-multi", Catalog: f.req.Catalog, Inventory: inventory,
		Platform: testPlatform, ScenarioArtifacts: f.req.ScenarioArtifacts,
	})
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignSubjectInvalid)
	_, err = f.store.LoadCampaignSpec(t.Context(), "invalid-multi")
	require.ErrorIs(t, err, constants.ErrNotFound)
}

func cloneFormationStackSet(set *HeterogeneousStackSet) *HeterogeneousStackSet {
	clone := *set
	clone.VariantIDs = append([]string(nil), set.VariantIDs...)
	clone.Stacks = make([]*evalv1.HeterogeneousStackDefinition, len(set.Stacks))
	for i, stack := range set.Stacks {
		clone.Stacks[i] = proto.Clone(stack).(*evalv1.HeterogeneousStackDefinition)
	}
	if set.Coverage != nil {
		coverage := *set.Coverage
		coverage.VariantRoleCoverage = make(map[string][]string, len(set.Coverage.VariantRoleCoverage))
		for variantID, roles := range set.Coverage.VariantRoleCoverage {
			coverage.VariantRoleCoverage[variantID] = append([]string(nil), roles...)
		}
		clone.Coverage = &coverage
	}
	return &clone
}

func resealFormationStackSet(t *testing.T, set *HeterogeneousStackSet) {
	t.Helper()
	for _, stack := range set.Stacks {
		digest, err := ComputeHeterogeneousStackDigest(stack)
		require.NoError(t, err)
		stack.StackDigest = digest
	}
	digest, err := ComputeHeterogeneousStackSetDigest(set)
	require.NoError(t, err)
	set.SetDigest = digest
}

func tamperFormationStackRoles(t *testing.T, set *HeterogeneousStackSet) *HeterogeneousStackSet {
	t.Helper()
	tampered := cloneFormationStackSet(set)
	stack := tampered.Stacks[0]
	stack.PrimarySlot.VariantId, stack.AssistantSlot.VariantId = stack.AssistantSlot.GetVariantId(), stack.PrimarySlot.GetVariantId()
	resealFormationStackSet(t, tampered)
	return tampered
}

func TestCampaignFormationBinding_ResealedRoleSwapFailsAtScheduleAndExecute(t *testing.T) {
	t.Run("schedule", func(t *testing.T) {
		fixture := newArchiveFixture(t)
		_, stackSet := createBoundSystemCampaign(t, fixture, "formation-tamper-schedule")
		_, err := fixture.controller.StartRun(context.Background(), RunStartRequest{ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM, ModelProvenancePolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
			PlatformRelease: testPlatform.Release,
			CampaignID:      "formation-tamper-schedule",
			RunID:           "formation-tamper-schedule-run",
			Lane:            evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
		})
		require.NoError(t, err)
		modified := tamperFormationStackRoles(t, stackSet)
		// The changed role mapping is structurally valid and every slot still
		// names a variant from the frozen registry; only the campaign-bound set
		// digest detects the substitution.
		require.NoError(t, ValidateFormationStackSetBinding(modified, "formation-tamper-schedule", testFormationCatalogVariants()))
		require.NoError(t, fixture.store.SaveHeterogeneousStackSet(context.Background(), "formation-tamper-schedule", modified))

		count, err := fixture.controller.ScheduleRun(context.Background(), "formation-tamper-schedule-run")
		require.ErrorIs(t, err, constants.ErrFormationStackMismatch)
		assert.Zero(t, count)
		assignments, err := fixture.store.ListAssignments(context.Background(), "formation-tamper-schedule-run")
		require.NoError(t, err)
		assert.Empty(t, assignments)
	})

	t.Run("execute", func(t *testing.T) {
		fixture := newArchiveFixture(t)
		inventory, stackSet := createBoundSystemCampaign(t, fixture, "formation-tamper-execute")
		_, err := fixture.controller.StartRun(context.Background(), RunStartRequest{ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM, ModelProvenancePolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
			PlatformRelease: testPlatform.Release,
			CampaignID:      "formation-tamper-execute",
			RunID:           "formation-tamper-execute-run",
			Lane:            evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
		})
		require.NoError(t, err)
		_, err = fixture.controller.ScheduleRun(context.Background(), "formation-tamper-execute-run")
		require.NoError(t, err)
		require.NoError(t, fixture.store.SaveHeterogeneousStackSet(context.Background(), "formation-tamper-execute", tamperFormationStackRoles(t, stackSet)))
		executor := fixture.controller.executor.(*stubCampaignExecutor)

		_, executed, err := fixture.controller.ExecuteNextAssignment(context.Background(), "formation-tamper-execute-run", CampaignExecutionBinding{
			ModelRegistryDigest: inventory.RegistryDigest,
			ModelRegistry:       inventory.ToModelRegistryFreeze().Variants,
		}, fixture.req.ScenarioArtifacts)
		require.ErrorIs(t, err, constants.ErrFormationStackMismatch)
		assert.False(t, executed)
		assert.Zero(t, executor.calls, "tampered formation must fail before invoking the executor")
	})
}

func TestCampaignFormationBinding_ValidatesFrozenHistoricalStackWithoutLiveCatalog(t *testing.T) {
	fixture := newArchiveFixture(t)
	campaignID := "formation-historical-freeze"
	variants := testFormationCatalogVariants()
	inventory, err := MaterializeModelRegistry(campaignID, variants)
	require.NoError(t, err)
	stackSet, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   campaignID,
		Seed:         23,
		Variants:     inventory.Variants,
		FormationIDs: []string{"qwen-powerhouse"},
	})
	require.NoError(t, err)
	historical := cloneFormationStackSet(stackSet)
	historical.Stacks[0].StackId = "retired-formation-v0"
	resealFormationStackSet(t, historical)
	require.NoError(t, ValidateFormationStackSetBinding(historical, campaignID, variants))

	spec, err := MaterializeCampaignSpec(campaignID, fixture.req.Catalog, inventory, 1, false, testPlatform)
	require.NoError(t, err)
	spec.FormationStackSetDigest = historical.SetDigest
	spec.CampaignDigest, err = ComputeCampaignSpecDigest(spec)
	require.NoError(t, err)
	require.NoError(t, ValidateCampaignSpecDigest(spec))
	require.NoError(t, validateCampaignFormationDigest(spec, historical), "a frozen snapshot must survive later catalog retirement")

	_, err = fixture.controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		Platform:          testPlatform,
		CampaignID:        campaignID,
		Catalog:           fixture.req.Catalog,
		Inventory:         inventory,
		ScenarioArtifacts: fixture.req.ScenarioArtifacts,
		FormationStackSet: historical,
	})
	require.ErrorIs(t, err, constants.ErrFormationInvalid, "new campaign creation must still use the current formation catalog")
}
