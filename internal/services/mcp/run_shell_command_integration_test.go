//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestRunShellCommandTool_Execute_SimpleCommand(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command: "echo",
		Args:    []string{"hello"},
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	result, err := tool.Execute(ctx, reqJSON)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	var shellResult RunShellCommandResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &shellResult)
	require.NoError(t, err)
	require.Equal(t, 0, shellResult.ExitCode)
	require.Contains(t, shellResult.Stdout, "hello")
	require.False(t, shellResult.TimedOut)
}

func TestRunShellCommandTool_Execute_WithWorkingDir(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()
	tmpDir := testutil.TempDir(t)

	pwdCmd := "pwd"
	if runtime.GOOS == "windows" {
		pwdCmd = "cmd.exe"
	}

	req := models.RunShellCommandRequest{
		Command:    pwdCmd,
		WorkingDir: tmpDir,
	}
	if runtime.GOOS == "windows" {
		req.Args = []string{"/c", "cd"}
	}

	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	result, err := tool.Execute(ctx, reqJSON)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	var shellResult RunShellCommandResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &shellResult)
	require.NoError(t, err)
	require.Equal(t, 0, shellResult.ExitCode)
	// On Windows, the output might have a trailing \r\n or different capitalization
	normalizedStdout := strings.TrimSpace(strings.ToLower(shellResult.Stdout))
	normalizedTmpDir := strings.TrimSpace(strings.ToLower(tmpDir))
	require.Contains(t, normalizedStdout, normalizedTmpDir)
}

func TestRunShellCommandTool_Execute_WithTimeout(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command: "echo",
		Args:    []string{"test"},
		Timeout: 10,
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	result, err := tool.Execute(ctx, reqJSON)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	var shellResult RunShellCommandResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &shellResult)
	require.NoError(t, err)
	require.Equal(t, 0, shellResult.ExitCode)
}

func TestRunShellCommandTool_Execute_TimeoutExceedsMax(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command: "echo",
		Args:    []string{"test"},
		Timeout: 301, // Exceeds max of 300
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	_, err = tool.Execute(ctx, reqJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout cannot exceed")
}

func TestRunShellCommandTool_Execute_MissingCommand(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command: "",
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	_, err = tool.Execute(ctx, reqJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "command is required")
}

func TestRunShellCommandTool_Execute_InvalidJSON(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	invalidJSON := json.RawMessage(`{invalid json`)

	_, err := tool.Execute(ctx, invalidJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unmarshal arguments")
}

func TestRunShellCommandTool_Execute_NonexistentCommand(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command: "nonexistent_command_12345",
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	result, err := tool.Execute(ctx, reqJSON)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	var shellResult RunShellCommandResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &shellResult)
	require.NoError(t, err)
	require.NotEqual(t, 0, shellResult.ExitCode)
	require.NotEmpty(t, shellResult.Error)
}

func TestRunShellCommandTool_Execute_CommandRejected(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command: "rm",
		Args:    []string{"-rf", "/"},
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	result, err := tool.Execute(ctx, reqJSON)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	var shellResult RunShellCommandResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &shellResult)
	require.NoError(t, err)
	require.Equal(t, -1, shellResult.ExitCode)
	require.Contains(t, strings.ToLower(shellResult.Stderr), "blocked by safety policy")
	require.Contains(t, strings.ToLower(shellResult.Error), "rejected by safety policy")
}

func TestRunShellCommandTool_Execute_MultiHost(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command:   "echo",
		Args:      []string{"test"},
		Hostnames: []string{"localhost", "127.0.0.1"},
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	result, err := tool.Execute(ctx, reqJSON)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	// Multi-host execution returns an array
	var results []map[string]interface{}
	err = json.Unmarshal([]byte(result.Content[0].Text), &results)
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Check each result has hostname field
	for _, r := range results {
		hostname, ok := r["hostname"].(string)
		require.True(t, ok)
		require.NotEmpty(t, hostname)
	}
}

func TestRunShellCommandTool_Execute_SingleHost(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command:   "echo",
		Args:      []string{"test"},
		Hostnames: []string{"localhost"},
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	result, err := tool.Execute(ctx, reqJSON)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	// Single-host execution returns a single object
	var shellResult RunShellCommandResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &shellResult)
	require.NoError(t, err)
	require.Equal(t, 0, shellResult.ExitCode)
	require.Equal(t, "localhost", shellResult.Hostname)
}

func TestRunShellCommandTool_Execute_DefaultHostname(t *testing.T) {
	tool := &RunShellCommandTool{}
	ctx := context.Background()

	req := models.RunShellCommandRequest{
		Command: "echo",
		Args:    []string{"test"},
		// No hostnames specified - should default to localhost
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(t, err)

	result, err := tool.Execute(ctx, reqJSON)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	var shellResult RunShellCommandResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &shellResult)
	require.NoError(t, err)
	require.Equal(t, 0, shellResult.ExitCode)
	require.Equal(t, "localhost", shellResult.Hostname)
}
