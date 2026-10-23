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
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestSearchDirectoryTool(t *testing.T) {
	t.Parallel()
	workDir := testutil.TempDir(t)
	logsDir := filepath.Join(workDir, "logs")
	require.NoError(t, os.Mkdir(logsDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, "api.log"), []byte("ERROR PAYMENT_TIMEOUT order=1\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, "worker.log"), []byte("INFO ok\nERROR PAYMENT_TIMEOUT order=2\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, "notes.txt"), []byte("PAYMENT_TIMEOUT runbook\n"), 0644))
	handler := newSearchToolHandler(t, workDir)

	t.Run("searches every file under the directory", func(t *testing.T) {
		t.Parallel()
		args, err := json.Marshal(SearchDirectoryRequest{Path: "logs", Pattern: "PAYMENT_TIMEOUT"})
		require.NoError(t, err)
		result, err := handler.HandleTool(context.Background(), constants.MCPToolSearchDirectory, args)
		require.NoError(t, err)

		var got SearchDirectoryResult
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &got))
		assert.Equal(t, logsDir, got.Path)
		assert.Equal(t, 3, got.TotalMatches)
		assert.False(t, got.Truncated)
	})

	t.Run("include narrows matched file names", func(t *testing.T) {
		t.Parallel()
		args, err := json.Marshal(SearchDirectoryRequest{Path: "logs", Pattern: "PAYMENT_TIMEOUT", Include: []string{"*.log"}})
		require.NoError(t, err)
		result, err := handler.HandleTool(context.Background(), constants.MCPToolSearchDirectory, args)
		require.NoError(t, err)

		var got SearchDirectoryResult
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &got))
		require.Equal(t, 2, got.TotalMatches)
		for _, m := range got.Matches {
			assert.Equal(t, ".log", filepath.Ext(m.Path))
		}
	})

	t.Run("rejects a file and names the file tool", func(t *testing.T) {
		t.Parallel()
		args, err := json.Marshal(SearchDirectoryRequest{Path: filepath.Join("logs", "api.log"), Pattern: "PAYMENT_TIMEOUT"})
		require.NoError(t, err)
		_, err = handler.HandleTool(context.Background(), constants.MCPToolSearchDirectory, args)
		require.ErrorIs(t, err, constants.ErrNotADirectory)
		assert.Contains(t, err.Error(), constants.MCPToolSearchFile)
	})

	t.Run("missing directory fails", func(t *testing.T) {
		t.Parallel()
		args, err := json.Marshal(SearchDirectoryRequest{Path: "absent", Pattern: "PAYMENT_TIMEOUT"})
		require.NoError(t, err)
		_, err = handler.HandleTool(context.Background(), constants.MCPToolSearchDirectory, args)
		require.ErrorIs(t, err, constants.ErrPathNotFound)
	})

	t.Run("invalid pattern fails", func(t *testing.T) {
		t.Parallel()
		args, err := json.Marshal(SearchDirectoryRequest{Path: "logs", Pattern: "[invalid"})
		require.NoError(t, err)
		_, err = handler.HandleTool(context.Background(), constants.MCPToolSearchDirectory, args)
		require.ErrorIs(t, err, constants.ErrFsGrepInvalidPattern)
	})
}
