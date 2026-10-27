// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type modelInventoryFreezePayload struct {
	CampaignID          string            `json:"campaign_id"`
	ModelRegistryDigest string            `json:"model_registry_digest"`
	Variants            []json.RawMessage `json:"variants"`
}

func TestLoadModelInventoryFreezeFile_RoundTrip(t *testing.T) {
	freeze, err := MaterializeModelRegistry("campaign-1", []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "inventory.json")
	rawVariants := make([]json.RawMessage, 0, len(freeze.Variants))
	for _, variant := range freeze.Variants {
		body, err := protojson.Marshal(variant)
		require.NoError(t, err)
		rawVariants = append(rawVariants, body)
	}
	payload, err := json.Marshal(modelInventoryFreezePayload{
		CampaignID:          freeze.CampaignID,
		ModelRegistryDigest: freeze.RegistryDigest,
		Variants:            rawVariants,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, payload, constants.PermFilePublic))

	loaded, err := LoadModelInventoryFreezeFile(path)
	require.NoError(t, err)
	assert.Equal(t, freeze.RegistryDigest, loaded.RegistryDigest)
	assert.Equal(t, freeze.HomogeneousCellCount, loaded.HomogeneousCellCount)
	require.NoError(t, ValidateModelRegistry(loaded))
}

func TestLoadModelInventoryFreezeFile_RejectsMissingDigest(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "inventory.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"campaign_id":"c1","variants":[]}`), constants.PermFilePublic))
	_, err := LoadModelInventoryFreezeFile(path)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestLoadModelInventoryFreezeFile_WrapsReadErrors(t *testing.T) {
	_, err := LoadModelInventoryFreezeFile(filepath.Join(testutil.TempDir(t), "missing.json"))
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
