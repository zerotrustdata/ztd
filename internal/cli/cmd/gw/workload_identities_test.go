// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestCleanupReportsRecordedIdentitiesWithoutScanningHome(t *testing.T) {
	t.Chdir(testutil.TempDir(t))
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	known := filepath.Join(home, "custom-operator")
	unlisted := filepath.Join(home, "unlisted-operator")
	for _, dir := range []string{known, unlisted} {
		fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
		require.NoError(t, err)
		require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle), []byte("old-ca"), constants.PermFilePrivate))
	}
	data, err := json.Marshal([]map[string]string{{"capability": "observer", "directory": known}})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(constants.LocalDevFullWorkloadsRegistryPath), 0700))
	require.NoError(t, os.WriteFile(constants.LocalDevFullWorkloadsRegistryPath, data, 0600))
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	reportWorkloadIdentities(cmd)
	require.Contains(t, out.String(), known)
	require.NotContains(t, out.String(), unlisted)
	fileSvc, err := fs.NewRuntimeFileService(known, slog.Default())
	require.NoError(t, err)
	retained, err := fileSvc.ReadFile(context.Background(), filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle))
	require.NoError(t, err)
	require.Equal(t, "old-ca", string(retained))
}

func TestCleanupReportsDefaultDataIdentityWithoutRegistry(t *testing.T) {
	t.Chdir(testutil.TempDir(t))
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, constants.OllamaZTDWorkloadsDirPath, "data")
	fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
	require.NoError(t, err)
	bundlePath := filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)
	require.NoError(t, fileSvc.WriteFile(context.Background(), bundlePath, []byte("old-ca"), constants.PermFilePrivate))
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	reportWorkloadIdentities(cmd)
	require.Contains(t, out.String(), "data: "+dir)
	require.Contains(t, out.String(), "ztd operator reset-identity --working-dir")
	retained, err := fileSvc.ReadFile(context.Background(), bundlePath)
	require.NoError(t, err)
	require.Equal(t, "old-ca", string(retained))
}
