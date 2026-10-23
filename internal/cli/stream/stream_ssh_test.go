// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package stream

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/pkg/ssh"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func testutil_GenerateRSAPrivateKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return keyPEM
}

func TestIsTransientError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"timeout error", fmt.Errorf("i/o timeout"), true},
		{"connection refused", fmt.Errorf("connection refused"), true},
		{"network unreachable", fmt.Errorf("network is unreachable"), true},
		{"no route to host", fmt.Errorf("no route to host"), true},
		{"broken pipe", fmt.Errorf("broken pipe"), true},
		{"connection reset", fmt.Errorf("connection reset"), true},
		{"auth failure", fmt.Errorf("permission denied"), false},
		{"nil error", nil, false},
		{"unknown error", fmt.Errorf("something else"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isTransientError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseConfig_ProxyCommand(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	configPath := filepath.Join(dir, "config")
	configContent := `Host bastion
  ProxyCommand ssh -W %h:%p jumpuser@jumphost
  User bastionuser
  Port 2222

Host *.internal
  ProxyCommand nc -X 5 -x proxy.example.com:1080 %h %p
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0600))

	blocks, err := ssh.ParseConfig(configPath)
	require.NoError(t, err)
	assert.NotEmpty(t, blocks)

	// Test bastion host
	bastionBlock := ssh.MatchBlock(blocks, "bastion")
	assert.NotNil(t, bastionBlock)
	assert.Equal(t, "ssh -W %h:%p jumpuser@jumphost", bastionBlock.ProxyCommand)
	assert.Equal(t, "bastionuser", bastionBlock.User)
	assert.Equal(t, "2222", bastionBlock.Port)

	// Test wildcard host
	internalBlock := ssh.MatchBlock(blocks, "server.internal")
	assert.NotNil(t, internalBlock)
	assert.Equal(t, "nc -X 5 -x proxy.example.com:1080 %h %p", internalBlock.ProxyCommand)
}

func TestResolveHost_ProxyCommand(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	configPath := filepath.Join(dir, "config")
	configContent := `Host proxyhost
  ProxyCommand ssh -W %h:%p jump@bastion
  User proxyuser
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0600))

	r, err := ssh.ResolveHost("proxyhost", configPath, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "ssh -W %h:%p jump@bastion", r.ProxyCommand)
	assert.Equal(t, "proxyuser", r.User)
}

// ---------------------------------------------------------------------------
// preFlightCheck
// ---------------------------------------------------------------------------
