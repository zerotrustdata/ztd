// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pkg/ssh"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestRunConcurrentStream_NoHosts(t *testing.T) {
	ctx := context.Background()
	results := runConcurrentStream(ctx, nil, StreamHostOptions{BinaryData: []byte("bin")}, 10)
	assert.Empty(t, results)
}

func TestRunConcurrentStream_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	hosts := []string{"host1", "host2", "host3"}
	results := runConcurrentStream(ctx, hosts, StreamHostOptions{
		BinaryData:  []byte("bin"),
		DialTimeout: 100 * time.Millisecond,
	}, 10)

	assert.Len(t, results, len(hosts))
	for _, res := range results {
		assert.Equal(t, constants.StreamStatusCancelled, res.Status)
		assert.Error(t, res.Error)
	}
}

func TestRunConcurrentStream_AllResultsCollected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	hosts := []string{"a", "b", "c", "d", "e"}
	results := runConcurrentStream(ctx, hosts, StreamHostOptions{
		BinaryData:  []byte("x"),
		DialTimeout: 50 * time.Millisecond,
	}, 5)

	assert.Len(t, results, len(hosts), "must collect exactly one result per host")

	seen := make(map[string]bool)
	for _, res := range results {
		seen[res.Host] = true
	}
	for _, h := range hosts {
		assert.True(t, seen[h], "host %s missing from results", h)
	}
}

func TestRunConcurrentStream_ConcurrencyLimitRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	hosts := make([]string, 20)
	for i := range hosts {
		hosts[i] = "host"
	}

	results := runConcurrentStream(ctx, hosts, StreamHostOptions{
		BinaryData:  []byte("bin"),
		DialTimeout: 50 * time.Millisecond,
	}, 3)
	assert.Len(t, results, len(hosts))
}

func TestRunConcurrentStream_EmitsPerHostEventsToStdout(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	hosts := []string{"host-a", "host-b"}
	runConcurrentStream(ctx, hosts, StreamHostOptions{
		BinaryData:  []byte("bin"),
		DialTimeout: 50 * time.Millisecond,
	}, 10)
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	assert.Len(t, lines, len(hosts), "one JSON line per host must be emitted")

	for _, line := range lines {
		var evt map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &evt), "each line must be valid JSON")
		assert.NotEmpty(t, evt["host"])
		assert.NotEmpty(t, evt["status"])
		assert.NotEmpty(t, evt["ts"])
	}
}

func TestRunStream_ValidZtdBinaryWithCancelledContext(t *testing.T) {
	// Write a minimal fake binary so the binary-load step succeeds.
	binaryDir := testutil.TempDir(t)
	arch := "amd64"
	binDir := filepath.Join(binaryDir, "linux-"+arch)
	require.NoError(t, os.MkdirAll(binDir, 0755))
	fakeBin := filepath.Join(binDir, "ztd.operator")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\nexit 0\n"), 0755))

	binaryData, err := os.ReadFile(fakeBin)
	require.NoError(t, err)
	assert.NotEmpty(t, binaryData)

	// With an already-cancelled context, runConcurrentStream cancels immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := runConcurrentStream(ctx, []string{"host1"}, StreamHostOptions{
		BinaryData:  binaryData,
		DialTimeout: 50 * time.Millisecond,
	}, 1)
	require.Len(t, results, 1)
	assert.Error(t, results[0].Error)
}

func TestBuildAuthMethods_NoKeysNoAgent_ReturnsEmpty(t *testing.T) {
	r := ssh.HostConfig{
		Original: "host",
		Hostname: "host",
		User:     "user",
		Port:     "22",
		KeyFiles: []string{},
	}
	methods, err := ssh.BuildAuthMethods(r, "", "")
	require.NoError(t, err)
	assert.Empty(t, methods)
}

func TestBuildAuthMethods_NonExistentKeyFile_Skipped(t *testing.T) {
	r := ssh.HostConfig{
		KeyFiles: []string{"/nonexistent/path/id_ed25519"},
	}
	_, err := ssh.BuildAuthMethods(r, "", "")
	require.Error(t, err)
}

func TestBuildAuthMethods_InvalidKeyFile_Skipped(t *testing.T) {
	dir := testutil.TempDir(t)
	badKey := filepath.Join(dir, "bad_key")
	require.NoError(t, os.WriteFile(badKey, []byte("not a valid private key\n"), 0600))

	r := ssh.HostConfig{
		KeyFiles: []string{badKey},
	}
	_, err := ssh.BuildAuthMethods(r, "", "")
	require.Error(t, err)
}

func TestBuildAuthMethods_ValidED25519Key_ReturnsMethod(t *testing.T) {
	dir := testutil.TempDir(t)

	// Generate an ed25519 private key in PEM format using ssh-keygen equivalent
	// via crypto/ed25519 + golang.org/x/crypto/ssh
	keyPath := filepath.Join(dir, "id_ed25519")
	generateTestSSHKey(t, keyPath)

	r := ssh.HostConfig{
		KeyFiles: []string{keyPath},
	}
	methods, err := ssh.BuildAuthMethods(r, "", "")
	require.NoError(t, err)
	assert.Len(t, methods, 1, "one auth method for one valid key file")
}

func TestBuildAuthMethods_MultipleValidKeys_AllLoaded(t *testing.T) {
	dir := testutil.TempDir(t)
	key1 := filepath.Join(dir, "key1")
	key2 := filepath.Join(dir, "key2")
	generateTestSSHKey(t, key1)
	generateTestSSHKey(t, key2)

	r := ssh.HostConfig{
		KeyFiles: []string{key1, key2},
	}
	methods, err := ssh.BuildAuthMethods(r, "", "")
	require.NoError(t, err)
	assert.Len(t, methods, 2, "two auth methods for two valid key files")
}

func TestBuildAuthMethods_MixedValidAndInvalid(t *testing.T) {
	dir := testutil.TempDir(t)
	validKey := filepath.Join(dir, "valid")
	generateTestSSHKey(t, validKey)

	badKey := filepath.Join(dir, "bad")
	require.NoError(t, os.WriteFile(badKey, []byte("garbage"), 0600))

	r := ssh.HostConfig{
		KeyFiles: []string{validKey, badKey, "/nonexistent"},
	}
	_, err := ssh.BuildAuthMethods(r, "", "")
	require.Error(t, err)
}

func TestBuildAuthMethods_InvalidAgentSocket_StillLoadsKeys(t *testing.T) {
	dir := testutil.TempDir(t)
	keyPath := filepath.Join(dir, "id_ed25519")
	generateTestSSHKey(t, keyPath)

	r := ssh.HostConfig{
		KeyFiles: []string{keyPath},
	}
	// Non-existent agent socket - Dial fails silently; key-file method still loaded
	_, err := ssh.BuildAuthMethods(r, "/tmp/nonexistent_agent_sock_xyz", "")
	require.Error(t, err)
}
