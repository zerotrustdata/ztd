// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestSaveAndLoadCredentials(t *testing.T) {
	t.Parallel()
	fileSvc := newAuthTestFileSvc(t)
	runtimeDir := fileSvc.Resolve("")
	cfg := &config.Config{
		ProjectRoot: runtimeDir,
		RuntimeDir:  runtimeDir,
	}

	creds := &Credentials{
		OperatorSessionID: "op-sess-123",
		UserID:            "user-456",
		OperatorID:        "op-789",
		CLISessionID:      "cli-sess-abc",
	}

	err := SaveCredentials(fileSvc, cfg, creds)
	require.NoError(t, err)

	loaded, err := LoadCredentials(fileSvc, cfg)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, creds.OperatorSessionID, loaded.OperatorSessionID)
	assert.Equal(t, creds.UserID, loaded.UserID)
	assert.Equal(t, creds.OperatorID, loaded.OperatorID)
	assert.Equal(t, creds.CLISessionID, loaded.CLISessionID)
}

func TestLoadCredentials_NotFound(t *testing.T) {
	t.Parallel()
	fileSvc := newAuthTestFileSvc(t)
	runtimeDir := fileSvc.Resolve("")
	cfg := &config.Config{
		ProjectRoot: runtimeDir,
		RuntimeDir:  runtimeDir,
	}

	loaded, err := LoadCredentials(fileSvc, cfg)
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestLoadCredentials_InvalidJSON(t *testing.T) {
	t.Parallel()
	fileSvc := newAuthTestFileSvc(t)
	runtimeDir := fileSvc.Resolve("")
	cfg := &config.Config{
		ProjectRoot: runtimeDir,
		RuntimeDir:  runtimeDir,
	}

	credsRel, err := fileSvc.RelFromAbs(cfg.CredentialsFile())
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), credsRel, []byte("invalid-json{{{"), constants.PermFilePrivate))

	loaded, err := LoadCredentials(fileSvc, cfg)
	require.Error(t, err)
	assert.Nil(t, loaded)
	assert.Error(t, err)
}

func TestSaveCredentials_WriteError(t *testing.T) {
	t.Parallel()

	fileSvc := newAuthTestFileSvc(t)
	runtimeDir := fileSvc.Resolve("")
	cfg := &config.Config{
		ProjectRoot: runtimeDir,
		RuntimeDir:  runtimeDir,
		Paths:       &config.PathsConfig{},
	}

	// Create a directory at the credentials file path to block atomic write (rename fails)
	credsFile := cfg.CredentialsFile()
	require.NoError(t, os.MkdirAll(credsFile, constants.PermDirPrivate))

	creds := &Credentials{
		OperatorSessionID: "op-sess-123",
		UserID:            "user-456",
		OperatorID:        "op-789",
		CLISessionID:      "cli-sess-abc",
	}

	err := SaveCredentials(fileSvc, cfg, creds)
	require.Error(t, err)
	assert.Error(t, err)
}

func TestLoadCredentials_ReadError(t *testing.T) {
	t.Parallel()

	fileSvc := newAuthTestFileSvc(t)
	runtimeDir := fileSvc.Resolve("")
	cfg := &config.Config{
		ProjectRoot: runtimeDir,
		RuntimeDir:  runtimeDir,
		Paths:       &config.PathsConfig{},
	}

	// Create a directory at the credentials file path to cause read failure
	credsFile := cfg.CredentialsFile()
	require.NoError(t, os.MkdirAll(credsFile, constants.PermDirPrivate))

	_, err := LoadCredentials(fileSvc, cfg)
	require.Error(t, err)
	assert.Error(t, err)
}

func TestSaveCredentials_MkdirError(t *testing.T) {
	t.Parallel()

	// Create a file where we expect a directory
	tmpDir := testutil.TempDir(t)
	fileSvc := newAuthTestFileSvc(t)
	blockingFile := filepath.Join(tmpDir, "credentials")
	require.NoError(t, os.WriteFile(blockingFile, []byte("block"), constants.PermFilePrivate))

	cfg := &config.Config{
		ProjectRoot: tmpDir,
		RuntimeDir:  blockingFile, // This is a file, not a directory
		Paths:       &config.PathsConfig{},
	}

	creds := &Credentials{
		OperatorSessionID: "op-sess-123",
		UserID:            "user-456",
		OperatorID:        "op-789",
		CLISessionID:      "cli-sess-abc",
	}

	err := SaveCredentials(fileSvc, cfg, creds)
	require.Error(t, err)
	assert.Error(t, err)
}
