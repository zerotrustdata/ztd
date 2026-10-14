// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux && integration

package keystore

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// startTestAgent starts an EphemeralAgent in a fresh runtime root and closes
// it at cleanup.
func startTestAgent(t *testing.T) (*EphemeralAgent, fs.RuntimeFileService) {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	a, err := StartEphemeralAgent(context.Background(), fileSvc, constants.EphemeralAgentDirname)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, a.Close()) })
	return a, fileSvc
}

func TestEphemeralAgent_SealedMasterKeyReopens(t *testing.T) {
	a, fileSvc := startTestAgent(t)
	key := bytes.Repeat([]byte{0x42}, 32)
	require.NoError(t, a.Keyring(fileSvc).StoreMasterKey(key))

	got, err := a.Keyring(fileSvc).RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, key, got)
}

// A harness child inherits Env(): the Secret Service is unreachable, so the
// platform selection seals under the ephemeral agent and reads back the key the
// harness stored in-process, never touching the host keyring.
func TestEphemeralAgent_ChildEnvSelectsTheAgent(t *testing.T) {
	a, fileSvc := startTestAgent(t)
	for _, kv := range a.Env() {
		k, v, ok := strings.Cut(kv, "=")
		require.True(t, ok)
		t.Setenv(k, v)
	}
	key := bytes.Repeat([]byte{0x24}, 32)
	require.NoError(t, a.Keyring(fileSvc).StoreMasterKey(key))

	kr, err := platformKeyring(fileSvc)
	require.NoError(t, err)
	got, err := kr.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, key, got)
}

func TestEphemeralAgent_RejectsOverlongSocketPath(t *testing.T) {
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	_, err = StartEphemeralAgent(context.Background(), fileSvc, strings.Repeat("d", constants.UnixSocketPathMax))
	require.Error(t, err)
}
