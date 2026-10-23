// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
	_ "modernc.org/sqlite"
)

// mockNativeTool is a mock implementation of NativeTool for unit testing.
type mockNativeTool struct {
	name        string
	description string
	schema      *InputSchema
	executeFn   func(ctx context.Context, args json.RawMessage) (CallToolResult, error)
}

func (m *mockNativeTool) Name() string              { return m.name }
func (m *mockNativeTool) Description() string       { return m.description }
func (m *mockNativeTool) InputSchema() *InputSchema { return m.schema }
func (m *mockNativeTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, args)
	}
	return CallToolResult{}, nil
}

func TestNewNativeToolHandler(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		handler, err := NewNativeToolHandler(nil)
		require.NoError(t, err)
		require.NotNil(t, handler)
		require.NotNil(t, handler.registry)
		require.NotEmpty(t, handler.ListTools())
	})

	t.Run("with logger", func(t *testing.T) {
		t.Parallel()
		logger := slog.New(slog.DiscardHandler)
		handler, err := NewNativeToolHandler(logger)
		require.NoError(t, err)
		require.NotNil(t, handler)
		require.Equal(t, logger, handler.logger)
	})
}

func TestNativeToolHandler_HandleTool_Unit(t *testing.T) {
	t.Parallel()

	t.Run("unknown tool returns error", func(t *testing.T) {
		t.Parallel()
		registry := NewToolRegistry()
		handler := &NativeToolHandler{registry: registry}

		_, err := handler.HandleTool(context.Background(), "unknown_tool", nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unknown native tool: unknown_tool")
	})

	t.Run("success execution", func(t *testing.T) {
		t.Parallel()
		registry := NewToolRegistry()
		mockTool := &mockNativeTool{
			name:   "test_tool",
			schema: &InputSchema{Type: "object"},
			executeFn: func(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
				return CallToolResult{
					Content: []TextContent{{Type: "text", Text: "success"}},
				}, nil
			},
		}
		require.NoError(t, registry.Register(mockTool))

		handler := &NativeToolHandler{registry: registry}
		result, err := handler.HandleTool(context.Background(), "test_tool", nil)
		require.NoError(t, err)
		require.False(t, result.IsError)
		require.Len(t, result.Content, 1)
		require.Equal(t, "success", result.Content[0].Text)
	})

	t.Run("execution error", func(t *testing.T) {
		t.Parallel()
		registry := NewToolRegistry()
		mockTool := &mockNativeTool{
			name:   "fail_tool",
			schema: &InputSchema{Type: "object"},
			executeFn: func(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
				return CallToolResult{}, fmt.Errorf("execution failed")
			},
		}
		require.NoError(t, registry.Register(mockTool))

		handler := &NativeToolHandler{registry: registry}
		_, err := handler.HandleTool(context.Background(), "fail_tool", nil)
		require.Error(t, err)
		require.Equal(t, "execution failed", err.Error())
	})

	t.Run("logging verification", func(t *testing.T) {
		t.Parallel()
		// We use io.Discard but ensure it doesn't panic
		logger := slog.New(slog.DiscardHandler)
		registry := NewToolRegistry()
		mockTool := &mockNativeTool{
			name:   "log_tool",
			schema: &InputSchema{Type: "object"},
		}
		require.NoError(t, registry.Register(mockTool))

		handler := &NativeToolHandler{
			registry: registry,
			logger:   logger,
		}

		_, err := handler.HandleTool(context.Background(), "log_tool", nil)
		require.NoError(t, err)

		_, err = handler.HandleTool(context.Background(), "unknown", nil)
		require.Error(t, err)
	})
}

func TestNativeToolHandler_ListTools_Unit(t *testing.T) {
	t.Parallel()

	registry := NewToolRegistry()
	mockTool := &mockNativeTool{
		name:   "tool1",
		schema: &InputSchema{Type: "object"},
	}
	require.NoError(t, registry.Register(mockTool))

	handler := &NativeToolHandler{registry: registry}
	tools := handler.ListTools()
	require.Len(t, tools, 1)
	require.Equal(t, "tool1", tools[0].Name())
}

func TestNativeToolHandler_HandleTool_Integration(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("unknown tool", func(t *testing.T) {
		t.Parallel()
		_, err := handler.HandleTool(context.Background(), "unknown_tool", json.RawMessage(`{}`))
		require.Error(t, err, "expected error for unknown tool")

		// Verify error message includes the list of available tools
		errStr := err.Error()
		require.Contains(t, errStr, "available tools:", "error message should include list of available tools")
		// Verify at least one known tool name is in the error
		require.Contains(t, errStr, "sys_info", "error message should include known tool names")
	})

	t.Run("custom registry", func(t *testing.T) {
		t.Parallel()
		customRegistry := NewToolRegistry()
		customHandler := &NativeToolHandler{registry: customRegistry}
		require.NotNil(t, customHandler, "expected non-nil handler with custom registry")
		require.Equal(t, customRegistry, customHandler.registry, "expected handler to use provided registry")
	})

	t.Run("all 33 tools registered", func(t *testing.T) {
		t.Parallel()
		tools := handler.ListTools()
		require.Len(t, tools, 33, "expected 33 registered tools")

		expectedTools := []string{
			"db_discover_topology",
			"db_query_validate",
			"db_isolated_read",
			"db_index_triage",
			constants.MCPToolSearchFile,
			constants.MCPToolSearchDirectory,
			"sys_oom_detect",
			"config_diff_mask",
			"proc_metric_top",
			"fs_disk_profile",
			"proc_signal_safe",
			"net_socket_audit",
			"net_endpoint_ping",
			"net_http_probe",
			"sys_info",
			"net_dns_resolve",
			"tls_cert_inspect",
			"sys_env_vars",
			"fs_file_checksum",
			"sys_service_status",
			"sys_container_status",
			"fs_disk_usage",
			"sys_time_clock",
			"proc_tree",
			"git_ops",
			"cloud_metadata",
			"k8s_inspect",
			"read_file",
			"run_shell_command",
			"net_ssh_known_hosts",
			"operator_deploy",
			"audit_receipt_list",
			"audit_receipt_get",
		}

		toolNames := make(map[string]bool)
		for _, tool := range tools {
			toolNames[tool.Name()] = true
		}

		for _, expected := range expectedTools {
			require.True(t, toolNames[expected], "expected tool '%s' to be registered", expected)
		}
	})
}

func TestHandleDBDiscoverTopology(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid database", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		dbPath := filepath.Join(tmpDir, "test.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create test database: %v", err)
		}
		defer db.Close()

		_, err = db.Exec("CREATE TABLE test_table (id INTEGER PRIMARY KEY, name TEXT)")
		if err != nil {
			t.Fatalf("failed to create test table: %v", err)
		}

		req := DBDiscoverTopologyRequest{DatabasePath: dbPath}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "db_discover_topology", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		if len(result.Content) == 0 {
			t.Error("expected content in result")
		}

		var schema DBDiscoverTopologyResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &schema); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if _, ok := schema.Schema["test_table"]; !ok {
			t.Error("expected test_table in schema")
		}
	})

	t.Run("missing database path", func(t *testing.T) {
		t.Parallel()
		req := DBDiscoverTopologyRequest{}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "db_discover_topology", reqJSON)
		if err == nil {
			t.Error("expected error for missing database path")
		}
	})

	t.Run("non-existent database", func(t *testing.T) {
		t.Parallel()
		req := DBDiscoverTopologyRequest{DatabasePath: "/nonexistent/path.db"}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "db_discover_topology", reqJSON)
		if err == nil {
			t.Error("expected error for non-existent database")
		}
	})
}

func TestHandleDBQueryValidate(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("indexed SELECT query accepted", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		dbPath := filepath.Join(tmpDir, "test.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create test database: %v", err)
		}
		defer db.Close()

		_, err = db.Exec("CREATE TABLE test_table (id INTEGER PRIMARY KEY, name TEXT)")
		if err != nil {
			t.Fatalf("failed to create test table: %v", err)
		}

		req := DBQueryValidateRequest{
			DatabasePath: dbPath,
			Query:        "SELECT name FROM test_table WHERE id = 1",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "db_query_validate", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var validation DBQueryValidateResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &validation); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if validation.Rejected {
			t.Errorf("indexed query should not be rejected: %s", validation.Reason)
		}
	})

	t.Run("full table scan rejected", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		dbPath := filepath.Join(tmpDir, "test.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create test database: %v", err)
		}
		defer db.Close()

		_, err = db.Exec("CREATE TABLE test_table (id INTEGER PRIMARY KEY, name TEXT)")
		if err != nil {
			t.Fatalf("failed to create test table: %v", err)
		}

		req := DBQueryValidateRequest{
			DatabasePath: dbPath,
			Query:        "SELECT * FROM test_table",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "db_query_validate", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var validation DBQueryValidateResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &validation); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if !validation.Rejected {
			t.Error("full table scan should be rejected")
		}
	})

	t.Run("non-SELECT query rejected", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		dbPath := filepath.Join(tmpDir, "test.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create test database: %v", err)
		}
		defer db.Close()

		req := DBQueryValidateRequest{
			DatabasePath: dbPath,
			Query:        "DROP TABLE test_table",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "db_query_validate", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var validation DBQueryValidateResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &validation); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if !validation.Rejected {
			t.Error("expected non-SELECT query to be rejected")
		}
	})
}

func TestHandleDBIsolatedRead(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid SELECT query", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		dbPath := filepath.Join(tmpDir, "test.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create test database: %v", err)
		}
		defer db.Close()

		_, err = db.Exec("CREATE TABLE test_table (id INTEGER PRIMARY KEY, name TEXT)")
		if err != nil {
			t.Fatalf("failed to create test table: %v", err)
		}

		_, err = db.Exec("INSERT INTO test_table (id, name) VALUES (1, 'test')")
		if err != nil {
			t.Fatalf("failed to insert test data: %v", err)
		}

		req := DBIsolatedReadRequest{
			DatabasePath: dbPath,
			Query:        "SELECT * FROM test_table",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "db_isolated_read", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var readResult DBIsolatedReadResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &readResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if len(readResult.Rows) == 0 {
			t.Error("expected rows in result")
		}
	})

	t.Run("non-SELECT query rejected", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		dbPath := filepath.Join(tmpDir, "test.db")

		req := DBIsolatedReadRequest{
			DatabasePath: dbPath,
			Query:        "DROP TABLE test_table",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "db_isolated_read", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var readResult DBIsolatedReadResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &readResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if readResult.Error == "" {
			t.Error("expected error field in result for non-SELECT query")
		}
	})

	t.Run("missing query rejected", func(t *testing.T) {
		t.Parallel()
		req := DBIsolatedReadRequest{DatabasePath: filepath.Join(testutil.TempDir(t), "test.db")}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "db_isolated_read", reqJSON)
		require.Error(t, err)
	})

	// Property: no query, including a stacked statement behind a SELECT
	// prefix, changes the database. The read-only DSN is the last line.
	t.Run("never mutates the database", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(testutil.TempDir(t), "test.db")
		db, err := sql.Open("sqlite", dbPath)
		require.NoError(t, err)
		defer db.Close()
		_, err = db.Exec("CREATE TABLE test_table (id INTEGER PRIMARY KEY, value TEXT)")
		require.NoError(t, err)
		_, err = db.Exec("INSERT INTO test_table (id, value) VALUES (1, 'original')")
		require.NoError(t, err)

		mutations := []string{
			"DROP TABLE test_table",
			"DELETE FROM test_table",
			"INSERT INTO test_table (id, value) VALUES (2, 'new')",
			"UPDATE test_table SET value = 'modified' WHERE id = 1",
			"CREATE TABLE new_table (id INTEGER)",
			"SELECT 1; DELETE FROM test_table",
			"SELECT * FROM test_table; UPDATE test_table SET value = 'modified'",
		}
		for _, query := range mutations {
			reqJSON, err := json.Marshal(DBIsolatedReadRequest{DatabasePath: dbPath, Query: query})
			require.NoError(t, err)
			_, err = handler.HandleTool(context.Background(), "db_isolated_read", reqJSON)
			require.NoError(t, err, query)

			var count int
			var value string
			require.NoError(t, db.QueryRow("SELECT COUNT(*), MAX(value) FROM test_table").Scan(&count, &value), query)
			require.Equal(t, 1, count, query)
			require.Equal(t, "original", value, query)
			var tables int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables), query)
			require.Equal(t, 1, tables, query)
		}
	})

	t.Run("concurrent reads", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(testutil.TempDir(t), "test.db")
		db, err := sql.Open("sqlite", dbPath)
		require.NoError(t, err)
		defer db.Close()
		_, err = db.Exec("CREATE TABLE test_table (id INTEGER PRIMARY KEY, value TEXT)")
		require.NoError(t, err)
		for i := 0; i < 100; i++ {
			_, err = db.Exec("INSERT INTO test_table (id, value) VALUES (?, ?)", i, fmt.Sprintf("value_%d", i))
			require.NoError(t, err)
		}

		const readers = 10
		errs := make(chan error, readers)
		for i := 0; i < readers; i++ {
			go func(id int) {
				reqJSON, err := json.Marshal(DBIsolatedReadRequest{
					DatabasePath: dbPath,
					Query:        fmt.Sprintf("SELECT * FROM test_table WHERE id = %d", id*10),
				})
				if err != nil {
					errs <- err
					return
				}
				result, err := handler.HandleTool(context.Background(), "db_isolated_read", reqJSON)
				if err != nil {
					errs <- fmt.Errorf("reader %d: %w", id, err)
					return
				}
				var readResult DBIsolatedReadResult
				if err := json.Unmarshal([]byte(result.Content[0].Text), &readResult); err != nil {
					errs <- fmt.Errorf("reader %d: %w", id, err)
					return
				}
				if len(readResult.Rows) != 1 || readResult.Error != "" {
					errs <- fmt.Errorf("reader %d: rows=%d error=%q", id, len(readResult.Rows), readResult.Error)
					return
				}
				errs <- nil
			}(i)
		}
		for i := 0; i < readers; i++ {
			require.NoError(t, <-errs)
		}
	})
}

func TestHandleDBIndexTriage(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid database", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		dbPath := filepath.Join(tmpDir, "test.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("failed to create test database: %v", err)
		}
		defer db.Close()

		_, err = db.Exec("CREATE TABLE test_table (id INTEGER PRIMARY KEY, name TEXT)")
		if err != nil {
			t.Fatalf("failed to create test table: %v", err)
		}

		req := DBIndexTriageRequest{DatabasePath: dbPath}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "db_index_triage", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var triage DBIndexTriageResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &triage); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if triage.Indexes == nil {
			triage.Indexes = []IndexInfo{}
		}
	})
}

func TestHandleSysOOMDetect(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("explicit log reports the killed process", func(t *testing.T) {
		t.Parallel()
		logPath := filepath.Join(testutil.TempDir(t), "dmesg")
		require.NoError(t, os.WriteFile(logPath, []byte("[ 1235.123456] oom-killer: killed process 5678 (worker) pid=5678 process worker 512 MB\n"), 0o644))
		reqJSON, err := json.Marshal(SysOOMDetectRequest{LogPath: logPath})
		require.NoError(t, err)

		result, err := handler.HandleTool(testutil.Context(t), "sys_oom_detect", reqJSON)
		require.NoError(t, err)
		require.NotEmpty(t, result.Content)

		var oomResult SysOOMDetectResult
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &oomResult))
		require.Len(t, oomResult.Events, 1)
		require.Equal(t, 5678, oomResult.Events[0].PID)
		require.Equal(t, "worker", oomResult.Events[0].Process)
		require.Equal(t, 512, oomResult.Events[0].MemoryMB)
	})

	t.Run("missing default log fails closed", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS != "linux" {
			t.Skip("the default log path is the Linux dmesg file")
		}
		if _, err := os.Stat(constants.PathVarLogDmesg); err == nil {
			t.Skip("host dmesg is present; the explicit log_path case covers the handler")
		}
		reqJSON, err := json.Marshal(SysOOMDetectRequest{})
		require.NoError(t, err)

		_, err = handler.HandleTool(testutil.Context(t), "sys_oom_detect", reqJSON)
		require.ErrorIs(t, err, constants.ErrMCPOpenLogFile)
	})
}

func TestHandleConfigDiffMask(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid config diff", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := "key1: value1\nkey2: secret_value"
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to create test config file: %v", err)
		}

		baseline := "key1: value1\nkey2: baseline_value"

		req := ConfigDiffMaskRequest{
			ConfigPath: configPath,
			Baseline:   baseline,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "config_diff_mask", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var diffResult ConfigDiffMaskResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &diffResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if diffResult.Differences == nil {
			t.Error("expected differences in result")
		}
	})

	t.Run("secret masking", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := "password: mypassword"
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to create test config file: %v", err)
		}

		baseline := "password: baseline_password"

		req := ConfigDiffMaskRequest{
			ConfigPath: configPath,
			Baseline:   baseline,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "config_diff_mask", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var diffResult ConfigDiffMaskResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &diffResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		for _, diff := range diffResult.Differences {
			if diff.Current == "mypassword" {
				t.Error("expected password to be masked")
			}
			if diff.Baseline == "baseline_password" {
				t.Error("expected baseline password to be masked")
			}
		}
	})
}

func TestHandleProcMetricTop(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("skipping on non-Linux - /proc not available")
	}

	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid request", func(t *testing.T) {
		t.Parallel()
		req := ProcMetricTopRequest{Limit: 5}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "proc_metric_top", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var topResult ProcMetricTopResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &topResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if topResult.Processes == nil {
			t.Error("expected processes in result")
		}
	})
}

func TestHandleFSDiskProfile(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid path", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)

		req := FSDiskProfileRequest{
			Path:     tmpDir,
			MaxDepth: 1,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "fs_disk_profile", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var profileResult FSDiskProfileResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &profileResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if profileResult.Entries == nil {
			t.Error("expected entries in result")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		t.Parallel()
		req := FSDiskProfileRequest{}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "fs_disk_profile", reqJSON)
		if err == nil {
			t.Error("expected error for missing path")
		}
	})
}

func TestHandleProcSignalSafe(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("protected PID rejected", func(t *testing.T) {
		t.Parallel()
		req := ProcSignalSafeRequest{
			PID:    1,
			Signal: "SIGTERM",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "proc_signal_safe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var signalResult ProcSignalSafeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &signalResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if signalResult.Sent {
			t.Error("expected protected PID to be rejected")
		}
	})

	t.Run("protected PID 2 rejected", func(t *testing.T) {
		t.Parallel()
		req := ProcSignalSafeRequest{
			PID:    2,
			Signal: "SIGKILL",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "proc_signal_safe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var signalResult ProcSignalSafeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &signalResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if signalResult.Sent {
			t.Error("expected protected PID 2 to be rejected")
		}
	})

	t.Run("unsupported signal", func(t *testing.T) {
		t.Parallel()
		req := ProcSignalSafeRequest{
			PID:    9999,
			Signal: "INVALID",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "proc_signal_safe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var signalResult ProcSignalSafeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &signalResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if signalResult.Sent {
			t.Error("expected unsupported signal to be rejected")
		}
	})

	t.Run("missing PID", func(t *testing.T) {
		t.Parallel()
		req := ProcSignalSafeRequest{
			Signal: "SIGTERM",
		}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "proc_signal_safe", reqJSON)
		if err == nil {
			t.Error("expected error for missing PID")
		}
	})

	t.Run("missing signal", func(t *testing.T) {
		t.Parallel()
		req := ProcSignalSafeRequest{
			PID: 9999,
		}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "proc_signal_safe", reqJSON)
		if err == nil {
			t.Error("expected error for missing signal")
		}
	})

	t.Run("valid signals accepted", func(t *testing.T) {
		t.Parallel()
		signals := []string{"SIGTERM", "SIGKILL", "SIGINT"}
		for _, sig := range signals {
			req := ProcSignalSafeRequest{
				PID:    99999, // Non-existent PID
				Signal: sig,
			}
			reqJSON, _ := json.Marshal(req)

			result, err := handler.HandleTool(context.Background(), "proc_signal_safe", reqJSON)
			if err != nil {
				t.Fatalf("HandleTool failed for %s: %v", sig, err)
			}

			var signalResult ProcSignalSafeResult
			if err := json.Unmarshal([]byte(result.Content[0].Text), &signalResult); err != nil {
				t.Fatalf("failed to unmarshal result for %s: %v", sig, err)
			}

			// Signal should be attempted (process may not exist, but signal is valid)
			if signalResult.Error == "" {
				t.Errorf("expected error for non-existent PID with %s", sig)
			}
		}
	})
}

func TestHandleNetSocketAudit(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("skipping on non-Linux - /proc not available")
	}

	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid request", func(t *testing.T) {
		t.Parallel()
		req := NetSocketAuditRequest{Protocol: "tcp"}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_socket_audit", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var auditResult NetSocketAuditResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &auditResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if auditResult.Sockets == nil {
			t.Error("expected sockets in result")
		}
	})

	t.Run("invalid protocol - path traversal attempt", func(t *testing.T) {
		t.Parallel()
		req := NetSocketAuditRequest{Protocol: "../../etc/passwd"}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "net_socket_audit", reqJSON)
		if err == nil {
			t.Error("expected error for invalid protocol, got nil")
		}
	})

	t.Run("invalid protocol - arbitrary string", func(t *testing.T) {
		t.Parallel()
		req := NetSocketAuditRequest{Protocol: "sctp"}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "net_socket_audit", reqJSON)
		if err == nil {
			t.Error("expected error for invalid protocol, got nil")
		}
	})
}

func TestHandleNetEndpointPing(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("invalid host", func(t *testing.T) {
		t.Parallel()
		req := NetEndpointPingRequest{
			Host: "invalid-host-that-does-not-exist-12345.com",
			Port: 9999,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_endpoint_ping", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var pingResult NetEndpointPingResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &pingResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if pingResult.Reachable {
			t.Error("expected unreachable host")
		}
	})

	t.Run("missing host", func(t *testing.T) {
		t.Parallel()
		req := NetEndpointPingRequest{Port: 80}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "net_endpoint_ping", reqJSON)
		if err == nil {
			t.Error("expected error for missing host")
		}
	})

	t.Run("missing port", func(t *testing.T) {
		t.Parallel()
		req := NetEndpointPingRequest{
			Host: "localhost",
		}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "net_endpoint_ping", reqJSON)
		if err == nil {
			t.Error("expected error for missing port")
		}
	})

	t.Run("invalid port", func(t *testing.T) {
		t.Parallel()
		req := NetEndpointPingRequest{
			Host: "localhost",
			Port: -1,
		}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "net_endpoint_ping", reqJSON)
		if err == nil {
			t.Error("expected error for invalid port")
		}
	})

	t.Run("localhost ping", func(t *testing.T) {
		t.Parallel()
		req := NetEndpointPingRequest{
			Host: "127.0.0.1",
			Port: 80, // May or may not be listening
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_endpoint_ping", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var pingResult NetEndpointPingResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &pingResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		// Latency should be set regardless of reachability
		if pingResult.LatencyMs < 0 {
			t.Error("expected non-negative latency")
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		req := NetEndpointPingRequest{
			Host: "google.com",
			Port: 443,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(ctx, "net_endpoint_ping", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var pingResult NetEndpointPingResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &pingResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		// Should fail due to context cancellation
		if pingResult.Reachable {
			t.Error("expected unreachable due to context cancellation")
		}
	})
}

func TestHandleNetHTTPProbe(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("invalid URL", func(t *testing.T) {
		t.Parallel()
		req := NetHTTPProbeRequest{
			URL: "http://invalid-host-that-does-not-exist-12345.com",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_http_probe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var probeResult NetHTTPProbeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &probeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if probeResult.Error == "" {
			t.Error("expected error for invalid URL")
		}
	})

	t.Run("missing URL", func(t *testing.T) {
		t.Parallel()
		req := NetHTTPProbeRequest{}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "net_http_probe", reqJSON)
		if err == nil {
			t.Error("expected error for missing URL")
		}
	})

	t.Run("default method is HEAD", func(t *testing.T) {
		t.Parallel()
		req := NetHTTPProbeRequest{
			URL: "http://example.com",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_http_probe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var probeResult NetHTTPProbeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &probeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		// Latency should be set regardless of success
		if probeResult.LatencyMs < 0 {
			t.Error("expected non-negative latency")
		}
	})

	t.Run("custom method GET", func(t *testing.T) {
		t.Parallel()
		req := NetHTTPProbeRequest{
			URL:    "http://example.com",
			Method: "GET",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_http_probe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var probeResult NetHTTPProbeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &probeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		// Latency should be set
		if probeResult.LatencyMs < 0 {
			t.Error("expected non-negative latency")
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		req := NetHTTPProbeRequest{
			URL: "http://example.com",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(ctx, "net_http_probe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var probeResult NetHTTPProbeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &probeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		// Should fail due to context cancellation
		if probeResult.Error == "" {
			t.Error("expected error due to context cancellation")
		}
	})

	t.Run("invalid URL format", func(t *testing.T) {
		t.Parallel()
		req := NetHTTPProbeRequest{
			URL: "not-a-valid-url",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_http_probe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var probeResult NetHTTPProbeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &probeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if probeResult.Error == "" {
			t.Error("expected error for invalid URL format")
		}
	})

	t.Run("blocks localhost", func(t *testing.T) {
		t.Parallel()
		req := NetHTTPProbeRequest{
			URL: "http://localhost:8080",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_http_probe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var probeResult NetHTTPProbeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &probeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if probeResult.Error == "" {
			t.Error("expected error for localhost URL")
		}
	})

	t.Run("blocks loopback IP", func(t *testing.T) {
		t.Parallel()
		req := NetHTTPProbeRequest{
			URL: "http://127.0.0.1:8080",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_http_probe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var probeResult NetHTTPProbeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &probeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if probeResult.Error == "" {
			t.Error("expected error for loopback IP URL")
		}
	})

	t.Run("blocks private IP", func(t *testing.T) {
		t.Parallel()
		req := NetHTTPProbeRequest{
			URL: "http://192.168.1.1",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_http_probe", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var probeResult NetHTTPProbeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &probeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if probeResult.Error == "" {
			t.Error("expected error for private IP URL")
		}
	})
}

func TestNativeTools(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)
	nativeTools := handler.ListTools()

	if len(nativeTools) != 33 {
		t.Errorf("expected 33 native tools, got %d", len(nativeTools))
	}

	expectedTools := []string{
		"db_discover_topology",
		"db_query_validate",
		"db_isolated_read",
		"db_index_triage",
		constants.MCPToolSearchFile,
		constants.MCPToolSearchDirectory,
		"sys_oom_detect",
		"config_diff_mask",
		"proc_metric_top",
		"fs_disk_profile",
		"proc_signal_safe",
		"net_socket_audit",
		"net_endpoint_ping",
		"net_http_probe",
		"sys_info",
		"net_dns_resolve",
		"tls_cert_inspect",
		"sys_env_vars",
		"fs_file_checksum",
		"sys_service_status",
		"sys_container_status",
		"fs_disk_usage",
		"sys_time_clock",
		"proc_tree",
		"git_ops",
		"cloud_metadata",
		"k8s_inspect",
		"read_file",
		"run_shell_command",
		"net_ssh_known_hosts",
		"operator_deploy",
		"audit_receipt_list",
		"audit_receipt_get",
	}

	toolNames := make(map[string]bool)
	for _, tool := range nativeTools {
		toolNames[tool.Name()] = true
	}

	for _, expected := range expectedTools {
		if !toolNames[expected] {
			t.Errorf("missing expected tool: %s", expected)
		}
	}
}

func TestNativeToolHandler_ScrubLine(t *testing.T) {
	t.Parallel()

	t.Run("redacts password", func(t *testing.T) {
		t.Parallel()
		line := "user login password=secret123"
		scrubbed := scrubLine(line)
		require.Equal(t, "user login password=REDACTED", scrubbed)
		require.NotContains(t, scrubbed, "secret123")
	})

	t.Run("redacts api_key", func(t *testing.T) {
		t.Parallel()
		line := "api_key=sk-12345 processed"
		scrubbed := scrubLine(line)
		require.Equal(t, "api_key=REDACTED processed", scrubbed)
		require.NotContains(t, scrubbed, "sk-12345")
	})

	t.Run("redacts secret", func(t *testing.T) {
		t.Parallel()
		line := "secret=mysecret value"
		scrubbed := scrubLine(line)
		require.Equal(t, "secret=REDACTED value", scrubbed)
		require.NotContains(t, scrubbed, "mysecret")
	})

	t.Run("redacts token", func(t *testing.T) {
		t.Parallel()
		line := "token=abc123def456 session"
		scrubbed := scrubLine(line)
		require.Equal(t, "token=REDACTED session", scrubbed)
		require.NotContains(t, scrubbed, "abc123def456")
	})

	t.Run("redacts bearer", func(t *testing.T) {
		t.Parallel()
		line := "Authorization: bearer xyz789"
		scrubbed := scrubLine(line)
		require.Equal(t, "Authorization: bearer REDACTED", scrubbed)
		require.NotContains(t, scrubbed, "xyz789")
	})

	t.Run("case insensitive", func(t *testing.T) {
		t.Parallel()
		line := "PASSWORD=secret123 API_KEY=sk-12345"
		scrubbed := scrubLine(line)
		require.Equal(t, "password=REDACTED api_key=REDACTED", scrubbed)
	})

	t.Run("safe line unchanged", func(t *testing.T) {
		t.Parallel()
		line := "INFO: request processed successfully"
		scrubbed := scrubLine(line)
		require.Equal(t, line, scrubbed)
	})
}

func TestNativeToolHandler_MaskSecret(t *testing.T) {
	t.Parallel()

	t.Run("masks password line", func(t *testing.T) {
		t.Parallel()
		line := "db_password=secret123"
		masked := maskSecret(line)
		require.Equal(t, "REDACTED", masked)
	})

	t.Run("masks secret line", func(t *testing.T) {
		t.Parallel()
		line := "shared_secret=mysecret"
		masked := maskSecret(line)
		require.Equal(t, "REDACTED", masked)
	})

	t.Run("masks token line", func(t *testing.T) {
		t.Parallel()
		line := "access_token=abc123"
		masked := maskSecret(line)
		require.Equal(t, "REDACTED", masked)
	})

	t.Run("masks key line", func(t *testing.T) {
		t.Parallel()
		line := "encryption_key=xyz789"
		masked := maskSecret(line)
		require.Equal(t, "REDACTED", masked)
	})

	t.Run("case insensitive", func(t *testing.T) {
		t.Parallel()
		line := "PASSWORD=secret123"
		masked := maskSecret(line)
		require.Equal(t, "REDACTED", masked)
	})

	t.Run("safe line unchanged", func(t *testing.T) {
		t.Parallel()
		line := "timeout=30"
		masked := maskSecret(line)
		require.Equal(t, line, masked)
	})
}

func TestIsValidIdentifier(t *testing.T) {
	t.Parallel()

	validIdentifiers := []string{
		"valid_table",
		"_table",
		"a",
		"table_name_123",
		"Table1",
		"TABLE",
		"_123",
	}

	for _, id := range validIdentifiers {
		t.Run("valid_"+id, func(t *testing.T) {
			t.Parallel()
			require.True(t, isValidIdentifier(id), "Expected '%s' to be valid identifier", id)
		})
	}

	invalidIdentifiers := []string{
		"",
		"123table",
		"table-name",
		"table name",
		"table.name",
		"table;drop",
		"table'or'1'='1",
		"table`",
		"table@",
		"table!",
	}

	for _, id := range invalidIdentifiers {
		t.Run("invalid_"+id, func(t *testing.T) {
			t.Parallel()
			require.False(t, isValidIdentifier(id), "Expected '%s' to be invalid identifier", id)
		})
	}
}

func TestParseSocketAddr(t *testing.T) {
	t.Parallel()

	t.Run("valid IPv4 address", func(t *testing.T) {
		t.Parallel()
		// 127.0.0.1:8080 in hex (little-endian)
		// IP: 0100007F (127.0.0.1), Port: 1F90 (8080)
		ip, port, err := parseSocketAddr("0100007F1F90")
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1", ip)
		require.Equal(t, 8080, port)
	})

	t.Run("localhost", func(t *testing.T) {
		t.Parallel()
		// 0.0.0.0:443 in hex
		// IP: 00000000 (0.0.0.0), Port: 01BB (443)
		ip, port, err := parseSocketAddr("0000000001BB")
		require.NoError(t, err)
		require.Equal(t, "0.0.0.0", ip)
		require.Equal(t, 443, port)
	})

	t.Run("invalid short address", func(t *testing.T) {
		t.Parallel()
		ip, port, err := parseSocketAddr("1234")
		require.NoError(t, err)
		require.Equal(t, "0.0.0.0", ip)
		require.Equal(t, 0, port)
	})

	t.Run("invalid IP length", func(t *testing.T) {
		t.Parallel()
		// IP part not 8 bytes
		ip, port, err := parseSocketAddr("12345601BB")
		require.NoError(t, err)
		require.Equal(t, "unknown", ip)
		require.Equal(t, 443, port)
	})
}

func TestHandleGitOps(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("git status on test repo", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)

		cmd := exec.Command("git", "init")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Skip("git not available")
		}

		cmd = exec.Command("git", "config", "user.email", "test@example.com")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Skip("git config failed")
		}

		cmd = exec.Command("git", "config", "user.name", "Test User")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Skip("git config failed")
		}

		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		cmd = exec.Command("git", "add", "test.txt")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git add failed: %v", err)
		}

		cmd = exec.Command("git", "commit", "-m", "test commit")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git commit failed: %v", err)
		}

		req := GitOpsRequest{
			Operation: "status",
			RepoPath:  tmpDir,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "git_ops", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		if len(result.Content) == 0 {
			t.Error("expected content in result")
		}

		var status gitStatusResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &status); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if status.Branch == "" {
			t.Error("expected branch in result")
		}
	})

	t.Run("git log with limit", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)

		cmd := exec.Command("git", "init")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Skip("git not available")
		}

		cmd = exec.Command("git", "config", "user.email", "test@example.com")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Skip("git config failed")
		}

		cmd = exec.Command("git", "config", "user.name", "Test User")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Skip("git config failed")
		}

		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		cmd = exec.Command("git", "add", "test.txt")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git add failed: %v", err)
		}

		cmd = exec.Command("git", "commit", "-m", "test commit")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git commit failed: %v", err)
		}

		req := GitOpsRequest{
			Operation: "log",
			RepoPath:  tmpDir,
			Limit:     5,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "git_ops", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var logResult gitLogResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &logResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if len(logResult.Commits) == 0 {
			t.Error("expected commits in result")
		}
	})

	t.Run("non-git repository", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		req := GitOpsRequest{
			Operation: "status",
			RepoPath:  tmpDir,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "git_ops", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var status gitErrorResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &status); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if status.Error == "" {
			t.Error("expected error field for non-git repository")
		}
	})
}

func TestHandleCloudMetadata(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("detect provider", func(t *testing.T) {
		t.Parallel()
		req := CloudMetadataRequest{
			Operation: "detect",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "cloud_metadata", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var metadata CloudMetadataDetectResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &metadata); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if metadata.Provider == "" {
			t.Error("expected provider in result")
		}
	})

	t.Run("all metadata", func(t *testing.T) {
		t.Parallel()
		req := CloudMetadataRequest{
			Operation: "all",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "cloud_metadata", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var allMetadata CloudMetadataAllResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &allMetadata); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if allMetadata.Provider == "" {
			t.Error("expected provider in result")
		}
	})
}

func TestHandleSysInfo(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid request", func(t *testing.T) {
		t.Parallel()
		req := SysInfoRequest{}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "sys_info", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var info SysInfoResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &info); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if info.Hostname == "" {
			t.Error("expected hostname in result")
		}
	})
}

func TestHandleNetDNSResolve(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid hostname", func(t *testing.T) {
		t.Parallel()
		req := NetDNSResolveRequest{
			Hostname: "localhost",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "net_dns_resolve", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var resolve NetDNSResolveResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &resolve); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if resolve.Count == 0 {
			t.Error("expected records in result")
		}
	})

	t.Run("missing hostname", func(t *testing.T) {
		t.Parallel()
		req := NetDNSResolveRequest{}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "net_dns_resolve", reqJSON)
		if err == nil {
			t.Error("expected error for missing hostname")
		}
	})
}

func TestHandleTLSCertInspect(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("missing cert path", func(t *testing.T) {
		t.Parallel()
		req := TLSCertInspectRequest{}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "tls_cert_inspect", reqJSON)
		if err == nil {
			t.Error("expected error for missing cert path")
		}
	})
}

func TestHandleSysEnvVars(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid request", func(t *testing.T) {
		t.Parallel()
		req := SysEnvVarsRequest{
			Pattern:       "PATH",
			RedactSecrets: true,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "sys_env_vars", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var env SysEnvVarsResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &env); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if env.Variables == nil {
			t.Error("expected variables in result")
		}
	})
}

func TestHandleFSFileChecksum(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid file", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		req := FSFileChecksumRequest{
			FilePath: testFile,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "fs_file_checksum", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var checksum FSFileChecksumResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &checksum); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if checksum.Checksum == "" {
			t.Error("expected checksum in result")
		}
	})

	t.Run("missing file path", func(t *testing.T) {
		t.Parallel()
		req := FSFileChecksumRequest{}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "fs_file_checksum", reqJSON)
		if err == nil {
			t.Error("expected error for missing file path")
		}
	})
}

func TestHandleSysServiceStatus(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("missing service name", func(t *testing.T) {
		t.Parallel()
		req := SysServiceStatusRequest{}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "sys_service_status", reqJSON)
		if err == nil {
			t.Error("expected error for missing service name")
		}
	})
}

func TestHandleSysContainerStatus(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("missing container name", func(t *testing.T) {
		t.Parallel()
		req := SysContainerStatusRequest{}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "sys_container_status", reqJSON)
		if err == nil {
			t.Error("expected error for missing container name")
		}
	})
}

func TestHandleFSDiskUsage(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid path", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)

		req := FSDiskUsageRequest{
			Path: tmpDir,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "fs_disk_usage", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var usage FSDiskUsageResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &usage); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if usage.Path == "" {
			t.Error("expected path in result")
		}
	})

	t.Run("missing path (defaults to all filesystems)", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS != "linux" {
			t.Skip("skipping on non-Linux - /proc/mounts not available")
		}
		req := FSDiskUsageRequest{}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "fs_disk_usage", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var usage FSDiskUsageResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &usage); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if len(usage.Filesystems) == 0 {
			t.Error("expected filesystems in result")
		}
	})
}

func TestHandleSysTimeClock(t *testing.T) {
	t.Parallel()
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid request", func(t *testing.T) {
		t.Parallel()
		req := SysTimeClockRequest{}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "sys_time_clock", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var timeResult SysTimeClockResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &timeResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if timeResult.SystemTime.UTC == "" {
			t.Error("expected system_time in result")
		}
	})
}

func TestHandleProcTree(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("skipping on non-Linux - /proc not available")
	}

	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("valid request", func(t *testing.T) {
		t.Parallel()
		req := ProcTreeRequest{
			PID: 1,
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "proc_tree", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var tree processTreeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &tree); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if tree.RootPID == 0 {
			t.Error("expected root_pid in result")
		}
	})

	t.Run("missing pid (defaults to 1)", func(t *testing.T) {
		t.Parallel()
		req := ProcTreeRequest{}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "proc_tree", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var tree processTreeResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &tree); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if tree.RootPID == 0 {
			t.Error("expected root_pid in result")
		}
	})
}

func TestHandleK8sInspect(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not found in PATH, skipping k8s_inspect tests")
	}

	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	t.Run("kubectl not available", func(t *testing.T) {
		t.Parallel()
		if _, err := exec.LookPath("kubectl"); err == nil {
			t.Skip("kubectl is available, skipping unavailable test")
		}

		req := K8sInspectRequest{Operation: "pods"}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "k8s_inspect", reqJSON)
		if err == nil {
			t.Error("expected error when kubectl not available")
		}
	})

	t.Run("invalid namespace format", func(t *testing.T) {
		t.Parallel()
		req := K8sInspectRequest{
			Operation: "pods",
			Namespace: "Invalid_Namespace",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "k8s_inspect", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var inspectResult K8sInspectResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &inspectResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if inspectResult.Error == "" {
			t.Error("expected error for invalid namespace format")
		}
	})

	t.Run("invalid resource name format", func(t *testing.T) {
		t.Parallel()
		req := K8sInspectRequest{
			Operation: "pod_logs",
			Name:      "Invalid_Pod_Name",
		}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "k8s_inspect", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var inspectResult K8sInspectResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &inspectResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if inspectResult.Error == "" {
			t.Error("expected error for invalid resource name format")
		}
	})

	t.Run("pod_logs missing name", func(t *testing.T) {
		t.Parallel()
		req := K8sInspectRequest{
			Operation: "pod_logs",
		}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "k8s_inspect", reqJSON)
		if err == nil {
			t.Error("expected error for missing pod name")
		}
	})

	t.Run("pod_describe missing name", func(t *testing.T) {
		t.Parallel()
		req := K8sInspectRequest{
			Operation: "pod_describe",
		}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "k8s_inspect", reqJSON)
		if err == nil {
			t.Error("expected error for missing pod name")
		}
	})

	t.Run("unsupported operation", func(t *testing.T) {
		t.Parallel()
		req := K8sInspectRequest{
			Operation: "invalid_operation",
		}
		reqJSON, _ := json.Marshal(req)

		_, err := handler.HandleTool(context.Background(), "k8s_inspect", reqJSON)
		if err == nil {
			t.Error("expected error for unsupported operation")
		}
	})

	t.Run("default operation is pods", func(t *testing.T) {
		t.Parallel()
		req := K8sInspectRequest{}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(context.Background(), "k8s_inspect", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var inspectResult K8sInspectResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &inspectResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if inspectResult.Operation != "pods" {
			t.Errorf("expected default operation to be pods, got %s", inspectResult.Operation)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		req := K8sInspectRequest{Operation: "pods"}
		reqJSON, _ := json.Marshal(req)

		result, err := handler.HandleTool(ctx, "k8s_inspect", reqJSON)
		if err != nil {
			t.Fatalf("HandleTool failed: %v", err)
		}

		var inspectResult K8sInspectResult
		if err := json.Unmarshal([]byte(result.Content[0].Text), &inspectResult); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if inspectResult.Error == "" {
			t.Error("expected error due to context cancellation")
		}
	})
}
