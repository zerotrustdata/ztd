// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// External test package: testutil imports config, so only config_test can use it.
func TestLoad_WorkDir_Flag(t *testing.T) {
	tmpDir := testutil.TempDir(t)

	cfg, err := config.Load(config.LoadOptions{
		OperatorEndpoint: constants.DefaultEndpoint,
		WorkDir:          tmpDir,
	})
	require.NoError(t, err)

	assert.Equal(t, tmpDir, cfg.WorkDir)
	assert.Equal(t, filepath.Join(tmpDir, constants.RuntimeDirname, constants.PkiDirname), cfg.PKIDir)
	assert.Equal(t, filepath.Join(tmpDir, constants.RuntimeDirname, constants.VaultDirname), cfg.VaultDir)
}
