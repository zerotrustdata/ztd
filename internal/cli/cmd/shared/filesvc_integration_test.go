// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package shared

import (
	"context"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const evidenceFileSvcMarker = "ZTD-EVIDENCE-FILESVC-MARKER"

// TestNewEvidenceFileSvc_SealsEvaluationRecordsWithTheRuntimeVault proves the
// factory every evaluation command uses encrypts .ztd/data/eval/ with the real
// runtime vault, and fails closed when the root has no vault.
func TestNewEvidenceFileSvc_SealsEvaluationRecordsWithTheRuntimeVault(t *testing.T) {
	ctx := context.Background()
	recordPath := path.Join(constants.EvaluationRunsPath, "run-1", constants.ConformanceRecordFilename)
	opts := keystore.Options{Keyring: keystoretest.NewMemoryKeyring()}
	newEvidenceFileSvc := EvidenceFileSvcFactory(opts)

	t.Run("an initialized vault encrypts and decrypts", func(t *testing.T) {
		base := testutil.TempDir(t)
		raw, err := fs.NewRuntimeFileService(base, testutil.NewTestLogger())
		require.NoError(t, err)
		require.NoError(t, raw.CreateRuntimeTree(ctx))
		ks, err := keystore.NewWithFS(raw, testutil.NewTestLogger(), opts)
		require.NoError(t, err)
		require.NoError(t, ks.Initialize())
		require.NoError(t, ks.InitVault())

		files, err := newEvidenceFileSvc(base, testutil.NewTestLogger())
		require.NoError(t, err)
		require.NoError(t, files.WriteFile(ctx, recordPath, []byte(evidenceFileSvcMarker), constants.PermFilePrivate))

		onDisk, err := raw.ReadFile(ctx, recordPath)
		require.NoError(t, err)
		assert.NotContains(t, string(onDisk), evidenceFileSvcMarker)
		decrypted, err := files.ReadFile(ctx, recordPath)
		require.NoError(t, err)
		assert.Equal(t, evidenceFileSvcMarker, string(decrypted))
	})

	t.Run("a root without a vault refuses the write and writes nothing", func(t *testing.T) {
		base := testutil.TempDir(t)
		raw, err := fs.NewRuntimeFileService(base, testutil.NewTestLogger())
		require.NoError(t, err)
		require.NoError(t, raw.CreateRuntimeTree(ctx))
		files, err := newEvidenceFileSvc(base, testutil.NewTestLogger())
		require.NoError(t, err)

		err = files.WriteFile(ctx, recordPath, []byte(evidenceFileSvcMarker), constants.PermFilePrivate)
		require.ErrorIs(t, err, constants.ErrEvaluationEvidenceVaultRequired)
		exists, existsErr := raw.FileExists(ctx, recordPath)
		require.NoError(t, existsErr)
		assert.False(t, exists)
	})
}
