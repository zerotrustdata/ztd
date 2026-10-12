// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestCleanupReportsRecordedIdentitiesWithoutScanningHome(t *testing.T) {
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	known := filepath.Join(home, "custom-operator")
	unlisted := filepath.Join(home, "unlisted-operator")
	for _, dir := range []string{known, unlisted} {
		path := filepath.Join(dir, ".ztd/pki/trust/ztdg-ca-bundle.pem")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte("old-ca"), 0600))
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
	retained, err := os.ReadFile(filepath.Join(known, ".ztd/pki/trust/ztdg-ca-bundle.pem"))
	require.NoError(t, err)
	require.Equal(t, "old-ca", string(retained))
}

func TestCleanupReportsDefaultDataIdentityWithoutRegistry(t *testing.T) {
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, constants.OllamaZTDWorkloadsDirPath, "data")
	path := filepath.Join(dir, ".ztd/pki/trust/ztdg-ca-bundle.pem")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("old-ca"), 0600))
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	reportWorkloadIdentities(cmd)
	require.Contains(t, out.String(), "data: "+dir)
	require.Contains(t, out.String(), "ztd operator reset-identity --working-dir")
	retained, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old-ca", string(retained))
}
