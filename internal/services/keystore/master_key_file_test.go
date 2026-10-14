// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestProvisionMasterKeyFile_WritesPrivateBase64Key(t *testing.T) {
	dir := testutil.TempDir(t)

	path, err := ProvisionMasterKeyFile(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, constants.SecretsFileMasterKey), path)

	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(constants.PermFilePrivate), info.Mode().Perm())
	}

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	decoded, err := base64.StdEncoding.DecodeString(string(raw[:len(raw)-1]))
	require.NoError(t, err)
	assert.Len(t, decoded, 32)
}

func TestProvisionMasterKeyFile_FreshKeyEachCall(t *testing.T) {
	first, err := ProvisionMasterKeyFile(testutil.TempDir(t))
	require.NoError(t, err)
	second, err := ProvisionMasterKeyFile(testutil.TempDir(t))
	require.NoError(t, err)

	a, err := os.ReadFile(first)
	require.NoError(t, err)
	b, err := os.ReadFile(second)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestProvisionMasterKeyFile_RefusesToOverwrite(t *testing.T) {
	dir := testutil.TempDir(t)
	path, err := ProvisionMasterKeyFile(dir)
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, err = ProvisionMasterKeyFile(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrExist)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
