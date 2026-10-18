// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestNewExecutionTopologies_ReturnsFiveValidTargetFormations(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)
	formations := topologies.Formations()
	require.Len(t, formations, 5)
	for _, formation := range formations {
		require.NoError(t, formation.Validate())
		assert.Less(t, formation.EstimatedVRAMMiB(), formation.MaxVRAMMiB)
		assert.Less(t, formation.EstimatedVRAMMiB(), FormationMaxVRAMMiB)
	}
}

func TestFormationValidateRejectsProviderAndFamilyOverlap(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)
	formation, err := topologies.Formation("deepseek-reasoning-tower")
	require.NoError(t, err)

	providerOverlap := formation
	providerOverlap.Assistant.Provider = providerOverlap.Primary.Provider
	err = providerOverlap.Validate()
	assert.ErrorIs(t, err, constants.ErrFormationProviderOverlap)

	familyOverlap := formation
	familyOverlap.Assistant.Family = familyOverlap.Primary.Family
	err = familyOverlap.Validate()
	assert.ErrorIs(t, err, constants.ErrFormationFamilyOverlap)
}

func newTestFormation(id string) Formation {
	return Formation{
		ID: id, DisplayName: "Test Formation", Description: "A formation used for CRUD tests.", MaxVRAMMiB: FormationMaxVRAMMiB,
		Primary:   formationModel("test-primary-9b", "Test Primary 9B", "TestProvider", "Test Family Primary", "test-primary:9b", 9_000_000_000, "Q4_K_M", 5632, 512),
		Assistant: formationModel("test-assistant-3b", "Test Assistant 3B", "OtherProvider", "Test Family Assistant", "test-assistant:3b", 3_800_000_000, "Q4_K_M", 2560, 512),
		Lite:      formationModel("test-lite-1b", "Test Lite 1B", "ThirdProvider", "Test Family Lite", "test-lite:1b", 999_890_000, "Q4_K_M", 768, 256),
	}
}

func TestAddOrUpdateFormationAddsNewEntry(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)

	updated, err := AddOrUpdateFormation(topologies, newTestFormation("test-new-formation"))
	require.NoError(t, err)
	require.Len(t, updated.Formations(), 6)

	formation, err := updated.Formation("test-new-formation")
	require.NoError(t, err)
	assert.Equal(t, "Test Formation", formation.DisplayName)

	require.Len(t, topologies.Formations(), 5, "original catalog must be unmodified")
}

func TestAddOrUpdateFormationReplacesExistingEntry(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)

	replacement := newTestFormation("qwen-powerhouse")
	updated, err := AddOrUpdateFormation(topologies, replacement)
	require.NoError(t, err)
	require.Len(t, updated.Formations(), 5)

	formation, err := updated.Formation("qwen-powerhouse")
	require.NoError(t, err)
	assert.Equal(t, "Test Formation", formation.DisplayName)
}

func TestAddOrUpdateFormationRejectsInvalidFormation(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)

	invalid := newTestFormation("test-invalid-formation")
	invalid.Assistant.Provider = invalid.Primary.Provider

	_, err = AddOrUpdateFormation(topologies, invalid)
	assert.ErrorIs(t, err, constants.ErrFormationProviderOverlap)
}

func TestAddOrUpdateFormationRejectsNilTopologies(t *testing.T) {
	_, err := AddOrUpdateFormation(nil, newTestFormation("test-new-formation"))
	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestRemoveFormationRemovesEntry(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)

	updated, removed, err := RemoveFormation(topologies, "gemma-cascade")
	require.NoError(t, err)
	require.NotNil(t, removed)
	assert.Equal(t, "gemma-cascade", removed.ID)
	require.Len(t, updated.Formations(), 4)

	_, err = updated.Formation("gemma-cascade")
	assert.ErrorIs(t, err, constants.ErrFormationInvalid)

	require.Len(t, topologies.Formations(), 5, "original catalog must be unmodified")
}

func TestRemoveFormationRejectsUnknownID(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)

	_, _, err = RemoveFormation(topologies, "does-not-exist")
	assert.ErrorIs(t, err, constants.ErrFormationInvalid)
}

func TestRemoveFormationRejectsRemovingLastEntry(t *testing.T) {
	topologies := &ExecutionTopologies{formations: []Formation{newTestFormation("only-formation")}}

	_, _, err := RemoveFormation(topologies, "only-formation")
	assert.ErrorIs(t, err, constants.ErrEvaluationLastEntryRemoval)
}

func TestUltraEfficientSpeedsterMeetsFrozenInventoryRequirements(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)
	formation, err := topologies.Formation("ultra-efficient-speedster")
	require.NoError(t, err)
	require.NoError(t, formation.Validate())

	localModels := make([]FormationModel, 0, 3)
	for _, role := range []FormationRole{FormationRolePrimary, FormationRoleAssistant, FormationRoleLite} {
		model, modelErr := formation.Model(role)
		require.NoError(t, modelErr)
		assert.Equal(t, FormationTrustSovereign, model.Trust)
		assert.Equal(t, "ollama", model.ProviderClass)
		assert.NotEmpty(t, model.ServedModelTag)
		assert.NotEmpty(t, model.Quantization)
		localModels = append(localModels, model)
	}
	assert.Len(t, localModels, 3)

	providers := make(map[string]struct{}, 3)
	families := make(map[string]struct{}, 3)
	for _, model := range localModels {
		providers[formationIdentity(model.Provider)] = struct{}{}
		families[formationIdentity(model.Family)] = struct{}{}
	}
	assert.Len(t, providers, 3)
	assert.Len(t, families, 3)
	assert.Equal(t, uint64(7552), formation.EstimatedVRAMMiB())
	assert.Less(t, formation.EstimatedVRAMMiB(), formation.MaxVRAMMiB)
}
