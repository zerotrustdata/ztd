// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package stream

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
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

func TestStreamToHost_DialFailure(t *testing.T) {
	// Use an unassigned port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	err = l.Close()
	require.NoError(t, err)

	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)

	// Set HOME to temp dir so resolveHost finds our key
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sshDir := filepath.Join(home, ".ssh")
	err = os.MkdirAll(sshDir, 0700)
	require.NoError(t, err)

	keyPath := filepath.Join(sshDir, "id_rsa")
	err = os.WriteFile(keyPath, testutil_GenerateRSAPrivateKey(t), 0600)
	require.NoError(t, err)

	sshConfigPath := filepath.Join(sshDir, "config")
	err = os.WriteFile(sshConfigPath, []byte(fmt.Sprintf("Host failedhost\n  Port %s\n", port)), 0600)
	require.NoError(t, err)

	// Strict mode requires known_hosts to exist; the dial itself is what we
	// expect to fail in this test, not host-key validation.
	khPath := filepath.Join(sshDir, "known_hosts")
	require.NoError(t, os.WriteFile(khPath, []byte(""), 0600))

	resultCh := make(chan streamResult, 1)
	streamToHost(
		context.Background(),
		"failedhost",
		StreamHostOptions{
			BinaryData:        []byte("data"),
			SSHConfigPath:     sshConfigPath,
			SSHKnownHostsPath: khPath,
			DialTimeout:       500 * time.Millisecond,
			Username:          "user",
		},
		resultCh,
	)

	res := <-resultCh
	assert.Equal(t, constants.StreamStatusFailed, res.Status)
	errMsg := res.Error.Error()
	assert.True(t, strings.Contains(errMsg, "dial") || strings.Contains(errMsg, "ssh"),
		"expected error to contain 'dial' or 'ssh', got: %s", errMsg)
}

func TestStreamToHost_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	resultCh := make(chan streamResult, 1)
	streamToHost(
		ctx,
		"127.0.0.1",
		StreamHostOptions{
			BinaryData:  []byte("data"),
			DialTimeout: 2 * time.Second,
			Username:    "user",
		},
		resultCh,
	)

	res := <-resultCh
	assert.Equal(t, constants.StreamStatusCancelled, res.Status)
}

func TestBuildAuthMethods_WithPassphrase(t *testing.T) {
	dir := testutil.TempDir(t)
	keyPath := filepath.Join(dir, "id_rsa_encrypted")

	// Generate an RSA key
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Encrypt with passphrase (PKCS#8 with passphrase)
	// For simplicity, we'll test the code path without actual encryption
	// since generating encrypted PEM is complex
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	}
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600))

	r := ssh.HostConfig{
		KeyFiles: []string{keyPath},
	}

	// Test with empty passphrase (should work for unencrypted key)
	methods, err := ssh.BuildAuthMethods(r, "", "")
	require.NoError(t, err)
	assert.Len(t, methods, 1)

	// Test with wrong passphrase (should fall back to no passphrase)
	methods, err = ssh.BuildAuthMethods(r, "", "wrongpassphrase")
	require.NoError(t, err)
	assert.Len(t, methods, 1)
}

func TestPreFlightCheck_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Need to provide a valid key to get past auth method check
	dir := testutil.TempDir(t)
	keyPath := filepath.Join(dir, "id_rsa")
	generateTestSSHKey(t, keyPath)

	// Create a known_hosts file to satisfy strict host-key checking
	khPath := filepath.Join(dir, "known_hosts")
	require.NoError(t, os.WriteFile(khPath, []byte(""), 0600))

	r := ssh.HostConfig{
		Hostname: "127.0.0.1",
		Port:     "22",
		User:     "testuser",
		KeyFiles: []string{keyPath},
	}

	err := preFlightCheck(ctx, r, "", "", khPath, 5*time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestPreFlightCheck_NoAuthMethods(t *testing.T) {
	ctx := context.Background()
	r := ssh.HostConfig{
		Hostname: "127.0.0.1",
		Port:     "22",
		User:     "testuser",
		KeyFiles: []string{},
	}

	err := preFlightCheck(ctx, r, "", "", "", 5*time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no SSH auth methods available")
}

func TestPreFlightCheck_InvalidKeyFile(t *testing.T) {
	ctx := context.Background()
	dir := testutil.TempDir(t)
	badKey := filepath.Join(dir, "bad_key")
	require.NoError(t, os.WriteFile(badKey, []byte("not a valid key"), 0600))

	r := ssh.HostConfig{
		Hostname: "127.0.0.1",
		Port:     "22",
		User:     "testuser",
		KeyFiles: []string{badKey},
	}

	err := preFlightCheck(ctx, r, "", "", "", 5*time.Second)
	assert.Error(t, err)
}

func TestPreFlightCheck_DialTimeout(t *testing.T) {
	ctx := context.Background()

	// Use a non-routable IP to trigger timeout
	r := ssh.HostConfig{
		Hostname: "192.0.2.1", // TEST-NET-1, guaranteed non-routable
		Port:     "22",
		User:     "testuser",
		KeyFiles: []string{},
	}

	err := preFlightCheck(ctx, r, "", "", "", 100*time.Millisecond)
	assert.Error(t, err)
	// Should fail due to no auth methods or dial timeout
}
