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

// testPlatform is the build identity campaign fixtures freeze under.
var testPlatform = PlatformIdentity{Release: "v2.3.0", SourceRevision: "0123abc"}

func TestMaterializeCampaignSpec_BindsCatalogAndInventory(t *testing.T) {
	t.Parallel()
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := MaterializeModelRegistry("smoke-campaign", []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)

	spec, err := MaterializeCampaignSpec("smoke-campaign", catalog, inventory, 0, true, testPlatform)
	require.NoError(t, err)
	assert.Equal(t, CampaignSchemaVersion, spec.GetSchemaVersion())
	assert.Equal(t, "smoke-campaign", spec.GetCampaignId())
	assert.Equal(t, uint32(1), spec.GetRepetitionCount())
	assert.False(t, spec.GetHoldContext(), "a single repetition has no later pass to hold context for")
	assert.Equal(t, catalog.GetCatalogDigest(), spec.GetCatalogDigest())
	assert.Equal(t, inventory.RegistryDigest, spec.GetModelRegistryDigest())
	assert.NotEmpty(t, spec.GetCampaignDigest())
	assert.Equal(t, testPlatform.Release, spec.GetPlatformRelease())
	assert.Equal(t, testPlatform.SourceRevision, spec.GetSourceRevision())
	assert.Equal(t, uint32(len(catalog.GetScenarios())), spec.GetScenarioCount())
}

func TestMaterializeCampaignSpec_RejectsMissingInputs(t *testing.T) {
	t.Parallel()
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := MaterializeModelRegistry("smoke-campaign", []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)

	tests := []struct {
		name       string
		campaignID string
		catalog    *evalv1.EvaluationScenarioCatalog
		inventory  *ModelInventoryFreeze
	}{
		{name: "empty campaign id", campaignID: "", catalog: catalog, inventory: inventory},
		{name: "nil catalog", campaignID: "smoke-campaign", catalog: nil, inventory: inventory},
		{name: "nil inventory", campaignID: "smoke-campaign", catalog: catalog, inventory: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := MaterializeCampaignSpec(test.campaignID, test.catalog, test.inventory, 1, false, testPlatform)
			require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		})
	}
}

func TestMemoryCampaignPublicationStateStore_LoadSaveCloneIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryCampaignPublicationStateStore()

	loaded, err := store.Load(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, campaignPublicationStateSchemaVersion, loaded.SchemaVersion)
	assert.Equal(t, "run-1", loaded.RunID)
	assert.Empty(t, loaded.PublishedIdempotency)

	state := &CampaignPublicationState{
		SchemaVersion:         campaignPublicationStateSchemaVersion,
		RunID:                 "run-1",
		PublishedIdempotency:  []string{"key-1"},
		LastPublishedSequence: 7,
	}
	require.NoError(t, store.Save(ctx, state))

	reloaded, err := store.Load(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, state.PublishedIdempotency, reloaded.PublishedIdempotency)
	reloaded.PublishedIdempotency[0] = "mutated"
	again, err := store.Load(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"key-1"}, again.PublishedIdempotency)
}

func TestMemoryCampaignPublicationStateStore_RejectsMissingFields(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryCampaignPublicationStateStore()

	_, err := store.Load(ctx, "")
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)

	err = store.Save(ctx, nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)

	err = store.Save(ctx, &CampaignPublicationState{SchemaVersion: campaignPublicationStateSchemaVersion})
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestMaterializeCampaignSpec_ReleaseIsPartOfTheDigest(t *testing.T) {
	t.Parallel()
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := MaterializeModelRegistry("smoke-campaign", []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)

	base, err := MaterializeCampaignSpec("smoke-campaign", catalog, inventory, 1, false, testPlatform)
	require.NoError(t, err)
	next, err := MaterializeCampaignSpec("smoke-campaign", catalog, inventory, 1, false, PlatformIdentity{Release: "v2.2.9", SourceRevision: testPlatform.SourceRevision})
	require.NoError(t, err)
	assert.NotEqual(t, base.GetCampaignDigest(), next.GetCampaignDigest())

	relabeled := proto.Clone(base).(*evalv1.EvaluationCampaignSpec)
	relabeled.PlatformRelease = "v2.2.9"
	require.ErrorIs(t, ValidateCampaignSpecDigest(relabeled), constants.ErrChecksumMismatch)
}

func TestMaterializeCampaignSpec_RequiresARelease(t *testing.T) {
	t.Parallel()
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := MaterializeModelRegistry("smoke-campaign", []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)

	_, err = MaterializeCampaignSpec("smoke-campaign", catalog, inventory, 1, false, PlatformIdentity{SourceRevision: "0123abc"})
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestMaterializeCampaignSpec_HoldContextIsPartOfTheDigest(t *testing.T) {
	t.Parallel()
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := MaterializeModelRegistry("smoke-campaign", []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)

	held, err := MaterializeCampaignSpec("smoke-campaign", catalog, inventory, 3, true, testPlatform)
	require.NoError(t, err)
	assert.True(t, held.GetHoldContext())
	fresh, err := MaterializeCampaignSpec("smoke-campaign", catalog, inventory, 3, false, testPlatform)
	require.NoError(t, err)
	assert.False(t, fresh.GetHoldContext())
	assert.NotEqual(t, held.GetCampaignDigest(), fresh.GetCampaignDigest())

	flipped := proto.Clone(held).(*evalv1.EvaluationCampaignSpec)
	flipped.HoldContext = false
	require.ErrorIs(t, ValidateCampaignSpecDigest(flipped), constants.ErrChecksumMismatch)
}

func TestCampaignCondition(t *testing.T) {
	t.Parallel()
	assert.Equal(t, CampaignConditionControl, CampaignCondition(nil))

	held := &evalv1.EvaluationCampaignSpec{HoldContext: true}
	assert.Equal(t, CampaignConditionHeldContext, CampaignCondition(held))

	persistent := &evalv1.EvaluationCampaignSpec{
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: "inv-1",
			DataOperatorId:  "op-1",
		},
	}
	assert.Equal(t, CampaignConditionLongitudinal, CampaignCondition(persistent))

	fresh := &evalv1.EvaluationCampaignSpec{
		Investigation: &evalv1.EvaluationInvestigation{
			Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH,
		},
	}
	assert.Equal(t, CampaignConditionControl, CampaignCondition(fresh))
}
