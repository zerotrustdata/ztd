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

	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestMaterializeCampaignInventory_WritesInventoryFile(t *testing.T) {
	root := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(root, nil)
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	freeze, relPath, err := MaterializeCampaignInventory(MaterializeCampaignInventoryRequest{
		Context:     context.Background(),
		FileService: fileSvc,
		CampaignID:  "eval-init-gemma3-4b",
		Variants:    []*evalv1.ModelVariant{testModelVariant()},
	})
	require.NoError(t, err)
	require.NotNil(t, freeze)
	assert.Equal(t, "eval-init-gemma3-4b", freeze.CampaignID)
	assert.Contains(t, relPath, "eval-init-gemma3-4b.json")
}
