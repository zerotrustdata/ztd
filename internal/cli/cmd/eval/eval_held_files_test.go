// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"log/slog"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// heldVaultEnv is a runtime root with an initialized evidence vault, and an
// evidence factory whose key store answers only while available is true, the
// way an ssh-agent answers only while its key is loaded.
type heldVaultEnv struct {
	root      string
	factory   func(string, *slog.Logger) (fs.RuntimeFileService, error)
	available bool
	opens     int
}

func newHeldVaultEnv(t *testing.T) *heldVaultEnv {
	t.Helper()
	root := testutil.TempDir(t)
	files, err := fs.NewRuntimeFileService(root, slog.Default())
	require.NoError(t, err)
	require.NoError(t, files.CreateRuntimeTree(testutil.Context(t)))
	keyring := keystoretest.NewMemoryKeyring()
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, files)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())

	env := &heldVaultEnv{root: root, available: true}
	env.factory = func(baseDir string, logger *slog.Logger) (fs.RuntimeFileService, error) {
		local, err := fs.NewRuntimeFileService(baseDir, logger)
		if err != nil {
			return nil, err
		}
		return evaluation.NewEvidenceFileService(local, func(keys fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
			if !env.available {
				return nil, constants.ErrKeyStoreSSHAgentKeyMissing
			}
			env.opens++
			store, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, keys)
			if err != nil {
				return nil, err
			}
			return store.OpenVault()
		})
	}
	return env
}

var heldProbePath = path.Join(constants.EvaluationDataPath, "held-probe.json")

func TestHeldRuntimeFiles_KeepsTheOpenedVaultAfterTheKeyStoreStopsAnswering(t *testing.T) {
	env := newHeldVaultEnv(t)
	factory := heldRuntimeFiles(env.factory)
	ctx := testutil.Context(t)

	first, err := factory(env.root, slog.Default())
	require.NoError(t, err)
	require.NoError(t, first.WriteFile(ctx, heldProbePath, []byte(`{"step":1}`), constants.PermFilePrivate))

	// The agent key expires while the rollout runs; every later run in the
	// same process still reads and writes evidence.
	env.available = false
	for step := 0; step < 3; step++ {
		files, err := factory(env.root, slog.Default())
		require.NoError(t, err)
		got, err := files.ReadFile(ctx, heldProbePath)
		require.NoError(t, err)
		assert.JSONEq(t, `{"step":1}`, string(got))
	}
	assert.Equal(t, 1, env.opens, "the vault is unlocked once per process")
}

func TestHeldRuntimeFiles_DoesNotHoldAFailedOpen(t *testing.T) {
	env := newHeldVaultEnv(t)
	factory := heldRuntimeFiles(env.factory)
	ctx := testutil.Context(t)

	env.available = false
	files, err := factory(env.root, slog.Default())
	require.NoError(t, err)
	err = files.WriteFile(ctx, heldProbePath, []byte(`{}`), constants.PermFilePrivate)
	require.ErrorIs(t, err, constants.ErrEvaluationEvidenceVaultRequired)

	env.available = true
	require.NoError(t, files.WriteFile(ctx, heldProbePath, []byte(`{}`), constants.PermFilePrivate))
	assert.Equal(t, 1, env.opens)
}

func TestHeldRuntimeFiles_KeepsOneServicePerRuntimeRoot(t *testing.T) {
	calls := map[string]int{}
	factory := heldRuntimeFiles(func(baseDir string, logger *slog.Logger) (fs.RuntimeFileService, error) {
		calls[baseDir]++
		return fs.NewRuntimeFileService(baseDir, logger)
	})
	gateway, operator := testutil.TempDir(t), testutil.TempDir(t)

	a, err := factory(gateway, slog.Default())
	require.NoError(t, err)
	b, err := factory(gateway, slog.Default())
	require.NoError(t, err)
	c, err := factory(operator, slog.Default())
	require.NoError(t, err)

	assert.Same(t, a, b)
	assert.NotSame(t, a, c)
	assert.Equal(t, map[string]int{gateway: 1, operator: 1}, calls)
}

func TestHeldRuntimeFiles_DoesNotHoldAFailedFactory(t *testing.T) {
	failures := 1
	factory := heldRuntimeFiles(func(baseDir string, logger *slog.Logger) (fs.RuntimeFileService, error) {
		if failures > 0 {
			failures--
			return nil, constants.ErrFileServiceInit
		}
		return fs.NewRuntimeFileService(baseDir, logger)
	})
	root := testutil.TempDir(t)

	_, err := factory(root, slog.Default())
	require.Error(t, err)
	files, err := factory(root, slog.Default())
	require.NoError(t, err)
	assert.NotNil(t, files)
}
