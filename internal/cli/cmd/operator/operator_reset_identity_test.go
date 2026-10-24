// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestResetIdentityStopsOnlySelectedDirectory(t *testing.T) {
	dir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
	require.NoError(t, err)
	path := constants.CliKeyFilename
	require.NoError(t, fileSvc.WriteFile(context.Background(), path, []byte("old-key"), constants.PermFilePrivate))
	selectedSignals, otherSignals, closed := 0, 0, 0
	cmd := operatorResetIdentityCmdWithDiscovery(func() ([]localOperatorProcess, error) {
		return []localOperatorProcess{
			{pid: 123, dir: dir, close: func() { closed++ }, wait: func(time.Duration) (bool, error) { return selectedSignals > 0, nil }, signal: func(bool) error { selectedSignals++; return nil }},
			{pid: 456, dir: testutil.TempDir(t), close: func() { closed++ }, wait: func(time.Duration) (bool, error) { t.Fatal("unrelated worker waited on"); return false, nil }, signal: func(bool) error { otherSignals++; return nil }},
		}, nil
	})
	cmd.SetArgs([]string{"--working-dir", dir, "--yes"})
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())
	require.Equal(t, 1, selectedSignals)
	require.Zero(t, otherSignals)
	require.Equal(t, 2, closed)
	exists, err := fileSvc.FileExists(context.Background(), path)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestResetIdentityDeclinedDoesNotDiscoverOrDelete(t *testing.T) {
	dir := testutil.TempDir(t)
	cmd := operatorResetIdentityCmdWithDiscovery(func() ([]localOperatorProcess, error) {
		t.Fatal("discovery before confirmation")
		return nil, nil
	})
	cmd.SetArgs([]string{"--working-dir", dir})
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetOut(&bytes.Buffer{})
	require.ErrorIs(t, cmd.Execute(), constants.ErrConfirmationAborted)
}
