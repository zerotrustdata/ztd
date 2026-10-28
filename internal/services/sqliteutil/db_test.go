// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sqliteutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestDefaultDBConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultDBConfig("/some/path/db.sqlite")
	assert.Equal(t, "/some/path/db.sqlite", cfg.Path)
	assert.Equal(t, 64, cfg.CacheSizeMB)
	assert.Equal(t, DefaultBusyTimeoutMs, cfg.BusyTimeoutMs)
	assert.Equal(t, 10, cfg.MaxRetries)
	assert.Equal(t, 50, cfg.RetryBaseDelayMs)
}

func TestOpenDB_CreatesFile(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "test.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	var result int
	require.NoError(t, db.QueryRow("SELECT 1").Scan(&result))
	assert.Equal(t, 1, result)
}

func TestOpenDB_RequiresExistingParentDirectory(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "nested", "deep", "test.db"))

	_, err := OpenDB(fileSvc, cfg, logger)
	require.Error(t, err)
}

func TestOpenDB_WALModeEnabled(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "wal.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	var journalMode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&journalMode))
	assert.Equal(t, "wal", journalMode)
}

func TestOpenDB_ForeignKeysEnabled(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "fk.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	var fkEnabled int
	require.NoError(t, db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled))
	assert.Equal(t, 1, fkEnabled)
}

func TestOpenDB_SingleConnectionPool(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "pool.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	stats := db.Stats()
	assert.Equal(t, 20, stats.MaxOpenConnections)
}

func TestRunIncrementalVacuum(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "vacuum.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	err = db.RunIncrementalVacuum(100)
	require.NoError(t, err)
}

func TestRunIncrementalVacuum_ZeroPages(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "vacuum0.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	err = db.RunIncrementalVacuum(0)
	require.NoError(t, err)
}

func TestRunIncrementalVacuum_WrapsError(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "wraperrdb.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	db.Close()

	err = db.RunIncrementalVacuum(100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incremental vacuum")
}

func TestGetPath(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	path := filepath.Join(dir, "getpath.db")
	cfg := DefaultDBConfig(path)

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	assert.Equal(t, path, db.GetPath())
}

func TestGetDBSizeBytes(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "size.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	size, err := db.GetSizeBytes()
	require.NoError(t, err)
	assert.Positive(t, size)
}

func TestGetDBSizeBytes_GrowsWithData(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "grow.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	sizeBefore, err := db.GetSizeBytes()
	require.NoError(t, err)

	_, err = db.Exec(`CREATE TABLE t (data TEXT)`)
	require.NoError(t, err)
	for i := 0; i < 500; i++ {
		_, insertErr := db.Exec(`INSERT INTO t VALUES (?)`, "x-data-payload-to-grow-the-db")
		require.NoError(t, insertErr)
	}

	sizeAfter, err := db.GetSizeBytes()
	require.NoError(t, err)

	assert.GreaterOrEqual(t, sizeAfter, sizeBefore)
}

func TestGetDBSizeBytes_ReturnsErrorOnClosedDB(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "closederr.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	db.Close()

	_, err = db.GetSizeBytes()
	require.Error(t, err)
}

func TestHealthCheck_Success(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "health.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	err = db.HealthCheck(ctx)
	require.NoError(t, err)
}

func TestHealthCheck_ClosedDB(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "healthclosed.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	db.Close()

	ctx := context.Background()
	err = db.HealthCheck(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health check")
}

func TestHealthCheck_CancelledContext(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "healthcancel.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = db.HealthCheck(ctx)
	require.Error(t, err)
}

func TestExecWithRetry_Success(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "exec.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	result, err := db.ExecWithRetry(t.Context(), "CREATE TABLE test (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestExecWithRetry_InvalidSQL(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "execinvalid.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.ExecWithRetry(t.Context(), "INVALID SQL STATEMENT")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "retry", "non-busy errors should not retry")
}

func TestExecWithRetry_WithParameters(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "execparams.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.ExecWithRetry(t.Context(), "CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	result, err := db.ExecWithRetry(t.Context(), "INSERT INTO test (value) VALUES (?)", "test-value")
	require.NoError(t, err)

	rowsAffected, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), rowsAffected)
}

func TestQueryWithRetry_Success(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "query.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (value) VALUES ('a'), ('b'), ('c')")
	require.NoError(t, err)

	rows, err := db.QueryWithRetry(t.Context(), "SELECT value FROM test ORDER BY value")
	require.NoError(t, err)
	t.Cleanup(func() { rows.Close() })

	var values []string
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		values = append(values, v)
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, []string{"a", "b", "c"}, values)
}

func TestQueryWithRetry_InvalidSQL(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "queryinvalid.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	rows, err := db.QueryWithRetry(t.Context(), "INVALID QUERY")
	if rows != nil {
		defer rows.Close()
		require.NoError(t, rows.Err())
	}
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "retry", "non-busy errors should not retry")
}

func TestQueryRowWithRetry_Success(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "queryrow.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (value) VALUES ('single-row')")
	require.NoError(t, err)

	row := db.QueryRowWithRetry(t.Context(), "SELECT value FROM test WHERE id = 1")
	var value string
	err = row.Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, "single-row", value)
}

func TestQueryRowWithRetry_NoRows(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "queryrowempty.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	row := db.QueryRowWithRetry(t.Context(), "SELECT id FROM test WHERE id = 999")
	var id int
	err = row.Scan(&id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no rows")
}

func TestQueryRowWithRetry_InvalidSQL(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "queryrowinvalid.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	row := db.QueryRowWithRetry(t.Context(), "INVALID QUERY")
	err = row.Err()
	require.Error(t, err)
}

func TestExecInTxWithRetry_Commit(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "txcommit.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	err = db.ExecInTxWithRetry(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO test (value) VALUES (?)", "committed")
		return err
	})
	require.NoError(t, err)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test WHERE value = 'committed'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestExecInTxWithRetry_RollbackOnError(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "txrollback.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	err = db.ExecInTxWithRetry(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO test (value) VALUES (?)", "rolled-back")
		if err != nil {
			return err
		}
		return assert.AnError
	})
	require.Error(t, err)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test WHERE value = 'rolled-back'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "transaction should have been rolled back")
}

func TestExecInTxWithRetry_MultipleStatements(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "txmulti.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	err = db.ExecInTxWithRetry(t.Context(), func(tx *sql.Tx) error {
		for i := 0; i < 5; i++ {
			_, err := tx.Exec("INSERT INTO test (value) VALUES (?)", i)
			if err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 5, count)
}

func TestExecInTxWithRetry_InvalidSQL(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "txinvalid.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	err = db.ExecInTxWithRetry(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INVALID SQL")
		return err
	})
	require.Error(t, err)
}

func TestMaterializeRows_Success(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "materialize.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (value) VALUES ('a'), ('b'), ('c')")
	require.NoError(t, err)

	results, err := MaterializeRows(t.Context(), db, "SELECT value FROM test ORDER BY value", nil, func(rows *sql.Rows) (string, error) {
		var v string
		err := rows.Scan(&v)
		return v, err
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, results)
}

func TestMaterializeRows_EmptyResult(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "materializeempty.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	results, err := MaterializeRows(t.Context(), db, "SELECT id FROM test", nil, func(rows *sql.Rows) (int, error) {
		var id int
		err := rows.Scan(&id)
		return id, err
	})
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestMaterializeRows_WithParameters(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "materializeparams.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (value) VALUES ('a'), ('b'), ('c')")
	require.NoError(t, err)

	results, err := MaterializeRows(t.Context(), db, "SELECT value FROM test WHERE value > ? ORDER BY value", []interface{}{"a"}, func(rows *sql.Rows) (string, error) {
		var v string
		err := rows.Scan(&v)
		return v, err
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "c"}, results)
}

func TestMaterializeRows_ScanError(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "materializescan.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (value) VALUES ('a')")
	require.NoError(t, err)

	_, err = MaterializeRows(t.Context(), db, "SELECT value FROM test", nil, func(rows *sql.Rows) (int, error) {
		var v string
		err := rows.Scan(&v)
		if err != nil {
			return 0, err
		}
		return 0, assert.AnError
	})
	require.Error(t, err)
}

func TestMaterializeRows_QueryError(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "materializequery.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = MaterializeRows(t.Context(), db, "INVALID QUERY", nil, func(rows *sql.Rows) (int, error) {
		return 0, nil
	})
	require.Error(t, err)
}

// Tier 1 Unit Tests (no external dependencies - mocks/stubs only)

func TestDefaultDBConfig_Tier1_SetsAllDefaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultDBConfig("/test/path.db")

	assert.Equal(t, "/test/path.db", cfg.Path)
	assert.Equal(t, 64, cfg.CacheSizeMB)
	assert.Equal(t, DefaultBusyTimeoutMs, cfg.BusyTimeoutMs)
	assert.Equal(t, 10, cfg.MaxRetries)
	assert.Equal(t, 50, cfg.RetryBaseDelayMs)
}

func TestDefaultDBConfig_Tier1_PathIsSet(t *testing.T) {
	t.Parallel()
	customPath := "/custom/path/to/database.sqlite"
	cfg := DefaultDBConfig(customPath)

	assert.Equal(t, customPath, cfg.Path)
}

func TestDBConfig_Tier1_AllFieldsAccessible(t *testing.T) {
	t.Parallel()
	cfg := DBConfig{
		Path:             "/test.db",
		CacheSizeMB:      128,
		BusyTimeoutMs:    5000,
		MaxRetries:       5,
		RetryBaseDelayMs: 100,
	}

	assert.Equal(t, "/test.db", cfg.Path)
	assert.Equal(t, 128, cfg.CacheSizeMB)
	assert.Equal(t, 5000, cfg.BusyTimeoutMs)
	assert.Equal(t, 5, cfg.MaxRetries)
	assert.Equal(t, 100, cfg.RetryBaseDelayMs)
}

func TestDB_Tier1_GetPathReturnsConfiguredPath(t *testing.T) {
	t.Parallel()
	db := &DB{
		path: "/configured/path.db",
	}

	assert.Equal(t, "/configured/path.db", db.GetPath())
}

func TestDB_Tier1_GetPathEmptyString(t *testing.T) {
	t.Parallel()
	db := &DB{
		path: "",
	}

	assert.Empty(t, db.GetPath())
}

func TestIsUniqueConstraintError_Tier1_NilError(t *testing.T) {
	t.Parallel()
	assert.False(t, IsUniqueConstraintError(nil))
}

func TestIsUniqueConstraintError_Tier1_ErrAlreadyExists(t *testing.T) {
	t.Parallel()
	err := constants.ErrAlreadyExists
	assert.True(t, IsUniqueConstraintError(err))
}

func TestIsUniqueConstraintError_Tier1_RenderedMessageNotUnique(t *testing.T) {
	t.Parallel()
	customErr := fmt.Errorf("UNIQUE constraint failed: test_column")
	assert.False(t, IsUniqueConstraintError(customErr))

	lowercaseErr := fmt.Errorf("unique constraint failed: column")
	assert.False(t, IsUniqueConstraintError(lowercaseErr))
}

func TestIsUniqueConstraintError_Tier1_GenericError(t *testing.T) {
	t.Parallel()
	err := assert.AnError
	assert.False(t, IsUniqueConstraintError(err))
}

func TestIsUniqueConstraintError_Tier1_SQLiteDriver(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "unique_err.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx := t.Context()
	_, err = db.ExecContext(ctx, "CREATE TABLE unique_test (email TEXT UNIQUE)")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO unique_test (email) VALUES ('a@example.com')")
	require.NoError(t, err)

	_, uniqueErr := db.ExecContext(ctx, "INSERT INTO unique_test (email) VALUES ('a@example.com')")
	require.Error(t, uniqueErr)
	assert.True(t, IsUniqueConstraintError(uniqueErr))
	assert.True(t, IsUniqueConstraintError(fmt.Errorf("wrap: %w", uniqueErr)))

	_, err = db.ExecContext(ctx, "CREATE TABLE pk_test (id TEXT PRIMARY KEY)")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO pk_test (id) VALUES ('nonce')")
	require.NoError(t, err)
	_, pkErr := db.ExecContext(ctx, "INSERT INTO pk_test (id) VALUES ('nonce')")
	require.Error(t, pkErr)
	assert.True(t, IsUniqueConstraintError(pkErr))

	_, err = db.ExecContext(ctx, "CREATE TABLE notnull_test (id INTEGER NOT NULL)")
	require.NoError(t, err)
	_, notNullErr := db.ExecContext(ctx, "INSERT INTO notnull_test (id) VALUES (NULL)")
	require.Error(t, notNullErr)
	assert.False(t, IsUniqueConstraintError(notNullErr))
}

func TestIsBusyError_Tier1_NilError(t *testing.T) {
	t.Parallel()
	assert.False(t, IsBusyError(nil))
}

func TestIsBusyError_Tier1_DatabaseIsLocked(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("database is locked")
	assert.False(t, IsBusyError(err))
}

func TestIsBusyError_Tier1_SQLITE_BUSY(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("SQLITE_BUSY")
	assert.False(t, IsBusyError(err))
}

func TestIsBusyError_Tier1_GenericError(t *testing.T) {
	t.Parallel()
	err := assert.AnError
	assert.False(t, IsBusyError(err))
}

func TestIsBusyError_Tier1_CaseSensitive(t *testing.T) {
	t.Parallel()
	lowercaseErr := fmt.Errorf("database is locked")
	assert.False(t, IsBusyError(lowercaseErr))

	lowercaseBusy := fmt.Errorf("sqlite_busy")
	assert.False(t, IsBusyError(lowercaseBusy))
}

func TestIsBusyError_Tier1_ContainsInMessage(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("some error: database is locked: more context")
	assert.False(t, IsBusyError(err))
}

func TestDB_Tier1_StructFieldsAccessible(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	db := &DB{
		DB:     nil,
		logger: logger,
		path:   "/test.db",
		config: DBConfig{Path: "/test.db"},
	}

	assert.Nil(t, db.DB)
	assert.Same(t, logger, db.logger)
	assert.Equal(t, "/test.db", db.path)
	assert.Equal(t, "/test.db", db.config.Path)
}

func TestDB_Tier1_NilFieldsAllowed(t *testing.T) {
	t.Parallel()
	db := &DB{}

	assert.Nil(t, db.DB)
	assert.Nil(t, db.logger)
	assert.Empty(t, db.path)
	assert.Empty(t, db.config.Path)
}

func TestDB_Tier1_EmbeddedSQLDBAccessible(t *testing.T) {
	t.Parallel()
	// Test that the embedded sql.DB field is accessible
	var sqlDB *sql.DB
	db := &DB{
		DB: sqlDB,
	}

	assert.Same(t, sqlDB, db.DB)
}

func TestOpenReadOnlyDB_ReadsExistingDatabaseWithoutWriting(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	path := filepath.Join(dir, "readonly.db")
	logger := testutil.NewTestLogger()

	writable, err := OpenDB(fileSvc, DefaultDBConfig(path), logger)
	require.NoError(t, err)
	_, err = writable.Exec("CREATE TABLE records (value TEXT)")
	require.NoError(t, err)
	_, err = writable.Exec("INSERT INTO records (value) VALUES ('kept')")
	require.NoError(t, err)
	require.NoError(t, writable.Close())

	readonly, err := OpenReadOnlyDB(DBConfig{Path: path}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { readonly.Close() })

	var value string
	require.NoError(t, readonly.QueryRow("SELECT value FROM records").Scan(&value))
	assert.Equal(t, "kept", value)
	_, err = readonly.Exec("INSERT INTO records (value) VALUES ('rejected')")
	require.Error(t, err)
}

func TestOpenReadOnlyDB_RejectsMissingPath(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")

	_, err := OpenReadOnlyDB(DBConfig{Path: filepath.Join(dir, "missing.db")}, testutil.NewTestLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ping read-only database")
}

func TestOpenReadOnlyDB_RequiresPath(t *testing.T) {
	t.Parallel()

	_, err := OpenReadOnlyDB(DBConfig{}, testutil.NewTestLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path is required")
}

func TestExecInImmediateTxWithRetry_RejectsCancelledContext(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	db, err := OpenDB(fileSvc, DefaultDBConfig(filepath.Join(dir, "immediate-cancelled.db")), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = db.ExecInImmediateTxWithRetry(ctx, func(*sql.Conn) error { return nil })
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestExecInImmediateTxWithRetry_CommitsCallback(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	db, err := OpenDB(fileSvc, DefaultDBConfig(filepath.Join(dir, "immediate.db")), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE records (value TEXT)")
	require.NoError(t, err)
	err = db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "INSERT INTO records (value) VALUES ('committed')")
		return err
	})
	require.NoError(t, err)

	var value string
	require.NoError(t, db.QueryRow("SELECT value FROM records").Scan(&value))
	assert.Equal(t, "committed", value)
}

func TestExecInImmediateTxWithRetry_RollsBackCallbackError(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	db, err := OpenDB(fileSvc, DefaultDBConfig(filepath.Join(dir, "immediate-rollback.db")), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE records (value TEXT)")
	require.NoError(t, err)
	expectedErr := fmt.Errorf("callback failed")
	err = db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		_, execErr := conn.ExecContext(context.Background(), "INSERT INTO records (value) VALUES ('rolled back')")
		require.NoError(t, execErr)
		return expectedErr
	})
	require.ErrorIs(t, err, expectedErr)

	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM records").Scan(&count))
	assert.Zero(t, count)
}

func TestOpenDB_CreatesPrivateDatabaseFileAndSidecars(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	dbPath := filepath.Join(dir, "private.db")

	db, err := OpenDB(fileSvc, DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO t DEFAULT VALUES")
	require.NoError(t, err)

	// The WAL and shared-memory files exist while the pool is open and must
	// inherit the database file's private mode.
	for _, path := range append([]string{dbPath}, sidecarPaths(dbPath)...) {
		info, err := os.Stat(path)
		require.NoError(t, err, path)
		assert.Equal(t, testutil.FileMode(constants.PermFilePrivate, false), info.Mode().Perm(), "wrong permissions for %s", path)
	}
}

func TestOpenDB_TightensExistingDatabaseFiles(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	dbPath := filepath.Join(dir, "existing.db")
	for _, path := range append([]string{dbPath}, sidecarPaths(dbPath)...) {
		require.NoError(t, os.WriteFile(path, nil, constants.PermFilePublic))
		require.NoError(t, os.Chmod(path, constants.PermFilePublic))
	}

	db, err := OpenDB(fileSvc, DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	info, err := os.Stat(dbPath)
	require.NoError(t, err)
	assert.Equal(t, testutil.FileMode(constants.PermFilePrivate, false), info.Mode().Perm())
}

func TestOpenDB_MemoryDatabaseCreatesNoFile(t *testing.T) {
	t.Parallel()

	db, err := OpenDB(nil, DefaultDBConfig(memoryDatabasePath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	// A file named ":memory:" in the working directory would mean the path was
	// treated as a file.
	assert.NoFileExists(t, memoryDatabasePath)
}
