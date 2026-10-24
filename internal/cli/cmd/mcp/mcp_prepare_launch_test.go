// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// createFakeAgentBinary creates a fake executable named agentName in a temp dir
// and prepends that dir to PATH so exec.LookPath finds it.
// newAgentScratchFiles returns a file service over an isolated runtime root,
// which owns the scratch directories throwaway agent configs are written into.
func newAgentScratchFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	return files
}

// agentScratchFileSvcFactory is a command file service factory over the same
// kind of isolated runtime root.
func agentScratchFileSvcFactory(t *testing.T) func(string, *slog.Logger) (fs.RuntimeFileService, error) {
	t.Helper()
	files := newAgentScratchFiles(t)
	return func(string, *slog.Logger) (fs.RuntimeFileService, error) { return files, nil }
}

func createFakeAgentBinary(t *testing.T, agentName string) {
	t.Helper()
	binDir := testutil.TempDir(t)
	if runtime.GOOS == "windows" {
		agentName += ".exe"
	}
	binPath := filepath.Join(binDir, agentName)
	require.NoError(t, os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0755))

	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+originalPath)
}

// ─── prepareAgentLaunch: verify=true for each supported agent ────────────────

func TestPrepareAgentLaunch_Claude_VerifyTrue(t *testing.T) {
	tmpHome := testutil.TempDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	createFakeAgentBinary(t, "claude")

	configPath, cleanup, launchArgs, err := prepareAgentLaunch(context.Background(), newAgentScratchFiles(t), "claude", "claude", true)
	require.NoError(t, err)
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	assert.NotEmpty(t, configPath)
	assert.FileExists(t, configPath)
	assert.Contains(t, launchArgs, "--strict-mcp-config")
	assert.Contains(t, launchArgs, "--disallowed-tools")
	assert.Contains(t, strings.Join(launchArgs, " "), "--mcp-config")
}

func TestPrepareAgentLaunch_Codex_VerifyTrue(t *testing.T) {
	tmpHome := testutil.TempDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	createFakeAgentBinary(t, "codex")

	configPath, cleanup, launchArgs, err := prepareAgentLaunch(context.Background(), newAgentScratchFiles(t), "codex", "codex", true)
	require.NoError(t, err)
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	assert.NotEmpty(t, configPath)
	assert.FileExists(t, configPath)
	assert.Contains(t, launchArgs, "--strict-mcp-config")
	assert.Contains(t, launchArgs, "--disallowed-tools")
}

func TestPrepareAgentLaunch_Goose_VerifyTrue(t *testing.T) {
	tmpHome := testutil.TempDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	createFakeAgentBinary(t, "goose")

	configPath, cleanup, launchArgs, err := prepareAgentLaunch(context.Background(), newAgentScratchFiles(t), "goose", "goose", true)
	require.NoError(t, err)
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	assert.NotEmpty(t, configPath)
	assert.FileExists(t, configPath)
	assert.Contains(t, launchArgs, "--no-profile")
	assert.Contains(t, launchArgs, "session")
	assert.Contains(t, launchArgs, "--with-extension")

	// Verify goose config.yaml was written to the fake HOME
	gooseConfig := filepath.Join(tmpHome, ".config", "goose", "config.yaml")
	assert.FileExists(t, gooseConfig)
}

func TestPrepareAgentLaunch_Gemini_VerifyTrue(t *testing.T) {
	tmpHome := testutil.TempDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	createFakeAgentBinary(t, "gemini")

	configPath, cleanup, launchArgs, err := prepareAgentLaunch(context.Background(), newAgentScratchFiles(t), "gemini", "gemini", true)
	require.NoError(t, err)
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	assert.NotEmpty(t, configPath)
	assert.FileExists(t, configPath)
	assert.Empty(t, launchArgs)

	// Verify gemini settings.json was written to the fake HOME
	geminiSettings := filepath.Join(tmpHome, ".gemini", "settings.json")
	assert.FileExists(t, geminiSettings)

	// Verify tools.core is set to empty array
	data, err := os.ReadFile(geminiSettings)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"core"`)
}

// ─── prepareAgentLaunch: verify=false skips verification ─────────────────────

func TestPrepareAgentLaunch_Devin_VerifyTrue(t *testing.T) {
	tmpHome := testutil.TempDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	createFakeAgentBinary(t, "devin")

	configPath, cleanup, launchArgs, err := prepareAgentLaunch(context.Background(), newAgentScratchFiles(t), "devin", "devin", true)
	require.NoError(t, err)
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	assert.NotEmpty(t, configPath)
	assert.FileExists(t, configPath)
	assert.Empty(t, launchArgs)

	// Verify devin config.json was written to the fake HOME
	devinConfig := filepath.Join(tmpHome, ".config", "devin", "config.json")
	assert.FileExists(t, devinConfig)

	// Verify ztd MCP server is in the config
	data, err := os.ReadFile(devinConfig)
	require.NoError(t, err)
	assert.Contains(t, string(data), "ztd")
}

// ─── prepareAgentLaunch: verify=false skips verification ─────────────────────

func TestPrepareAgentLaunch_VerifyFalse_SkipsVerification(t *testing.T) {
	tmpHome := testutil.TempDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	createFakeAgentBinary(t, "claude")

	configPath, cleanup, launchArgs, err := prepareAgentLaunch(context.Background(), newAgentScratchFiles(t), "claude", "claude", false)
	require.NoError(t, err)
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	assert.NotEmpty(t, configPath)
	assert.FileExists(t, configPath)
	assert.Contains(t, launchArgs, "--strict-mcp-config")
}

// ─── prepareAgentLaunch: unsupported agent returns error ─────────────────────

func TestPrepareAgentLaunch_UnsupportedAgent(t *testing.T) {
	tmpHome := testutil.TempDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	createFakeAgentBinary(t, "cursor")

	_, _, _, err := prepareAgentLaunch(context.Background(), newAgentScratchFiles(t), "cursor", "cursor", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAgentNotFound)
}

// ─── prepareAgentLaunch: agent binary not in PATH ────────────────────────────

func TestPrepareAgentLaunch_AgentNotInPath(t *testing.T) {
	tmpHome := testutil.TempDir(t)
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	// A registered agent whose binary is not installed fails closed before any
	// config is written.
	t.Setenv("PATH", "")

	_, _, _, err := prepareAgentLaunch(context.Background(), newAgentScratchFiles(t), "claude", "claude", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAgentNotInPath)
}
