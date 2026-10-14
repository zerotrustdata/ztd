// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux && integration

package keystore

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// These tests open real Unix sockets, so they run in Tier 2 (INV-TEST-RUN-02).

func TestCheckSSHAgentPeer_AcceptsOwnUser(t *testing.T) {
	sock := filepath.Join(testutil.TempDir(t), "agent.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, checkSSHAgentPeer(conn))
}

func TestDialSSHAgent_UnsetAuthSockUsesUserAgentSocket(t *testing.T) {
	runtimeDir := testutil.TempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(runtimeDir, "gcr"), 0o700))
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(runtimeDir, constants.GcrSSHAgentSocketRelPath), Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	served := agent.NewKeyring()
	require.NoError(t, served.Add(agent.AddedKey{PrivateKey: newEd25519(t)}))
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = agent.ServeAgent(served, conn)
	}()
	t.Setenv(string(constants.EnvVar.SSHAuthSock), "")
	t.Setenv(string(constants.EnvVar.XDGRuntimeDir), runtimeDir)

	ag, closeAgent, err := dialSSHAgent()
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeAgent() })
	_, err = sealingKey(ag)
	require.NoError(t, err)
}

func TestDialSSHAgent_NoUserAgentFailsUnavailable(t *testing.T) {
	runtimeDir := testutil.TempDir(t)
	t.Setenv(string(constants.EnvVar.SSHAuthSock), "")
	t.Setenv(string(constants.EnvVar.XDGRuntimeDir), runtimeDir)

	_, _, err := dialSSHAgent()
	require.ErrorIs(t, err, constants.ErrKeyStoreSSHAgentUnavailable)
}
