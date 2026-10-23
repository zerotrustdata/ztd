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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/execution"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newSearchToolHandler returns a native tool handler whose search tools run
// against workDir, the way an executing runtime wires its own WorkDir.
func newSearchToolHandler(t *testing.T, workDir string) *NativeToolHandler {
	t.Helper()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)
	handler.SetFsGrepService(newTestFsGrep(t, workDir))
	return handler
}

// newTestFsGrep returns a file search service rooted at workDir.
func newTestFsGrep(t *testing.T, workDir string) *execution.FsGrepService {
	t.Helper()
	grep, err := execution.NewFsGrepService(workDir, testutil.NewTestLogger())
	require.NoError(t, err)
	return grep
}

func TestSearchFileTool(t *testing.T) {
	workDir := testutil.TempDir(t)
	logsDir := filepath.Join(workDir, "logs")
	require.NoError(t, os.Mkdir(logsDir, 0755))
	logFile := filepath.Join(logsDir, "app.log")
	require.NoError(t, os.WriteFile(logFile, []byte("INFO start\nERROR AUTH_FAILURE password=hunter2\n"), 0644))
	handler := newSearchToolHandler(t, workDir)

	t.Run("returns scrubbed matching lines with line numbers", func(t *testing.T) {
		args, err := json.Marshal(SearchFileRequest{Path: filepath.Join("logs", "app.log"), Pattern: "AUTH_FAILURE"})
		require.NoError(t, err)
		result, err := handler.HandleTool(context.Background(), constants.MCPToolSearchFile, args)
		require.NoError(t, err)

		var got SearchFileResult
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &got))
		assert.Equal(t, logFile, got.Path)
		require.Len(t, got.Matches, 1)
		assert.Equal(t, 2, got.Matches[0].LineNumber)
		assert.Equal(t, "ERROR AUTH_FAILURE password=REDACTED", got.Matches[0].Content)
		assert.NotContains(t, result.Content[0].Text, "hunter2")
	})

	t.Run("scrubs every secret kind", func(t *testing.T) {
		secretsFile := filepath.Join(logsDir, "secrets.log")
		require.NoError(t, os.WriteFile(secretsFile, []byte("password=secret123\napi_key=sk-12345\ntoken=abc123\nsecret=mysecret\n"), 0644))
		args, err := json.Marshal(SearchFileRequest{Path: filepath.Join("logs", "secrets.log"), Pattern: "="})
		require.NoError(t, err)
		result, err := handler.HandleTool(context.Background(), constants.MCPToolSearchFile, args)
		require.NoError(t, err)

		var got SearchFileResult
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &got))
		require.Len(t, got.Matches, 4)
		for _, secret := range []string{"secret123", "sk-12345", "abc123", "mysecret"} {
			assert.NotContains(t, result.Content[0].Text, secret)
		}
	})

	t.Run("rejects a directory and names the directory tool", func(t *testing.T) {
		args, err := json.Marshal(SearchFileRequest{Path: "logs", Pattern: "AUTH_FAILURE"})
		require.NoError(t, err)
		_, err = handler.HandleTool(context.Background(), constants.MCPToolSearchFile, args)
		require.ErrorIs(t, err, constants.ErrPathIsDirectory)
		assert.Contains(t, err.Error(), constants.MCPToolSearchDirectory)
	})

	t.Run("requires path and pattern", func(t *testing.T) {
		_, err := handler.HandleTool(context.Background(), constants.MCPToolSearchFile, json.RawMessage(`{"path":"logs/app.log"}`))
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		_, err = handler.HandleTool(context.Background(), constants.MCPToolSearchFile, json.RawMessage(`{"pattern":"x"}`))
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	})

	t.Run("fails closed without the runtime search service", func(t *testing.T) {
		unwired, err := NewNativeToolHandler(nil)
		require.NoError(t, err)
		args, err := json.Marshal(SearchFileRequest{Path: logFile, Pattern: "AUTH_FAILURE"})
		require.NoError(t, err)
		_, err = unwired.HandleTool(context.Background(), constants.MCPToolSearchFile, args)
		require.ErrorIs(t, err, constants.ErrMCPFsGrepNotConfigured)
	})
}
