//go:build unix || linux || darwin

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package execution

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestFsGrepService_ExecuteFsGrep(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	workDir := testutil.TempDir(t)

	// Setup test files
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "file1.txt"), []byte("hello world\nthis is a test\nztd is cool"), 0644))
	require.NoError(t, os.Mkdir(filepath.Join(workDir, "subdir"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "subdir", "file2.txt"), []byte("another file\nwith hello in it\nand more text"), 0644))
	require.NoError(t, os.Mkdir(filepath.Join(workDir, ".hidden"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".hidden", "secret.txt"), []byte("hidden hello"), 0644))

	service, err := NewFsGrepService(workDir, logger)
	require.NoError(t, err)

	t.Run("simple grep search", func(t *testing.T) {
		t.Parallel()
		req := &models.FsGrepRequest{
			ExecutionID: "test-1",
			CaseID:      "case-1",
			Path:        ".",
			Pattern:     "hello",
			MaxMatches:  100,
		}

		result, err := service.ExecuteFsGrep(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.Equal(t, 2, result.TotalMatches)

		foundFiles := make(map[string]bool)
		for _, m := range result.Matches {
			foundFiles[filepath.Base(m.Path)] = true
			assert.Contains(t, m.Content, "hello")
		}
		assert.True(t, foundFiles["file1.txt"])
		assert.True(t, foundFiles["file2.txt"])
		assert.False(t, foundFiles["secret.txt"], "should skip hidden directories")
	})

	t.Run("grep with regex pattern", func(t *testing.T) {
		t.Parallel()
		req := &models.FsGrepRequest{
			ExecutionID: "test-2",
			CaseID:      "case-2",
			Path:        ".",
			Pattern:     "ztd.*cool",
			MaxMatches:  100,
		}

		result, err := service.ExecuteFsGrep(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.Equal(t, 1, result.TotalMatches)
		assert.Contains(t, result.Matches[0].Content, "ztd is cool")
	})

	t.Run("grep with include filter", func(t *testing.T) {
		t.Parallel()
		req := &models.FsGrepRequest{
			ExecutionID: "test-3",
			CaseID:      "case-3",
			Path:        ".",
			Pattern:     "hello",
			Includes:    []string{"file1.txt"},
			MaxMatches:  100,
		}

		result, err := service.ExecuteFsGrep(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.Equal(t, 1, result.TotalMatches)
		assert.Equal(t, "file1.txt", filepath.Base(result.Matches[0].Path))
	})

	t.Run("respects max_matches limit", func(t *testing.T) {
		t.Parallel()
		req := &models.FsGrepRequest{
			ExecutionID: "test-4",
			CaseID:      "case-4",
			Path:        ".",
			Pattern:     "o", // Matches in multiple files/lines
			MaxMatches:  2,
		}

		result, err := service.ExecuteFsGrep(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.Equal(t, 2, result.TotalMatches)
		assert.True(t, result.Truncated)
	})

	t.Run("handles invalid regex", func(t *testing.T) {
		t.Parallel()
		req := &models.FsGrepRequest{
			ExecutionID: "test-5",
			CaseID:      "case-5",
			Path:        ".",
			Pattern:     "[invalid",
			MaxMatches:  100,
		}

		result, err := service.ExecuteFsGrep(context.Background(), req)
		require.Error(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
		assert.Equal(t, "fs_grep invalid pattern", result.ErrorType)
	})
}

func TestNewFsGrepService_RequiresDependencies(t *testing.T) {
	t.Parallel()
	_, err := NewFsGrepService("", slog.New(slog.DiscardHandler))
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	_, err = NewFsGrepService(testutil.TempDir(t), nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestFsGrepService_TargetKind(t *testing.T) {
	t.Parallel()
	workDir := testutil.TempDir(t)
	logsDir := filepath.Join(workDir, "logs")
	require.NoError(t, os.Mkdir(logsDir, 0755))
	logFile := filepath.Join(logsDir, "app.log")
	require.NoError(t, os.WriteFile(logFile, []byte("ok\nERROR PAYMENT_TIMEOUT order=1\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, "worker.log"), []byte("ERROR PAYMENT_TIMEOUT order=2\n"), 0644))

	service, err := NewFsGrepService(workDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ctx := context.Background()

	t.Run("search directory spans files", func(t *testing.T) {
		t.Parallel()
		result, err := service.SearchDirectory(ctx, &models.FsGrepRequest{Path: "logs", Pattern: "PAYMENT_TIMEOUT"})
		require.NoError(t, err)
		assert.Equal(t, 2, result.TotalMatches)
	})

	t.Run("search file reads one file", func(t *testing.T) {
		t.Parallel()
		result, err := service.SearchFile(ctx, &models.FsGrepRequest{Path: logFile, Pattern: "PAYMENT_TIMEOUT"})
		require.NoError(t, err)
		require.Equal(t, 1, result.TotalMatches)
		assert.Equal(t, 2, result.Matches[0].LineNumber)
	})

	t.Run("search file rejects a directory", func(t *testing.T) {
		t.Parallel()
		result, err := service.SearchFile(ctx, &models.FsGrepRequest{Path: "logs", Pattern: "PAYMENT_TIMEOUT"})
		require.ErrorIs(t, err, constants.ErrPathIsDirectory)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
	})

	t.Run("search directory rejects a file", func(t *testing.T) {
		t.Parallel()
		_, err := service.SearchDirectory(ctx, &models.FsGrepRequest{Path: logFile, Pattern: "PAYMENT_TIMEOUT"})
		require.ErrorIs(t, err, constants.ErrNotADirectory)
	})

	t.Run("missing path fails instead of matching nothing", func(t *testing.T) {
		t.Parallel()
		_, err := service.ExecuteFsGrep(ctx, &models.FsGrepRequest{Path: "absent", Pattern: "x"})
		require.ErrorIs(t, err, constants.ErrPathNotFound)
	})
}

func TestFsGrepService_HiddenRootAndSensitiveFiles(t *testing.T) {
	t.Parallel()
	workDir := testutil.TempDir(t)
	sshDir := filepath.Join(workDir, constants.SshDirname)
	require.NoError(t, os.Mkdir(sshDir, 0700))
	privateKey := filepath.Join(sshDir, constants.SshKeyRSA)
	require.NoError(t, os.WriteFile(privateKey, []byte("needle private\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, constants.SshConfigBasename), []byte("needle config\n"), 0600))

	service, err := NewFsGrepService(workDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ctx := context.Background()

	t.Run("hidden root is searched and sensitive entries are skipped", func(t *testing.T) {
		t.Parallel()
		result, err := service.SearchDirectory(ctx, &models.FsGrepRequest{Path: sshDir, Pattern: "needle"})
		require.NoError(t, err)
		require.Equal(t, 1, result.TotalMatches)
		assert.Equal(t, constants.SshConfigBasename, filepath.Base(result.Matches[0].Path))
	})

	t.Run("sensitive file root is refused", func(t *testing.T) {
		t.Parallel()
		_, err := service.SearchFile(ctx, &models.FsGrepRequest{Path: privateKey, Pattern: "needle"})
		require.ErrorIs(t, err, constants.ErrSensitivePath)
	})
}

// grepWithin runs one directory search and fails the test if it does not
// return within the bound, so a blocking read surfaces as a failure rather
// than a hung suite.
func grepWithin(t *testing.T, service *FsGrepService, req *models.FsGrepRequest) (*models.FsGrepResult, error) {
	t.Helper()
	type outcome struct {
		result *models.FsGrepResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.SearchDirectory(context.Background(), req)
		done <- outcome{result, err}
	}()
	select {
	case got := <-done:
		return got.result, got.err
	case <-time.After(5 * time.Second):
		t.Fatal("search_directory did not return; a non-regular file blocked the walk")
		return nil, nil
	}
}

func TestFsGrepService_SkipsNonRegularFiles(t *testing.T) {
	t.Parallel()
	workDir := testutil.TempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "app.log"), []byte("needle\n"), 0644))
	fifo := filepath.Join(workDir, "pipe")
	require.NoError(t, syscall.Mkfifo(fifo, constants.PermFilePrivate))
	require.NoError(t, os.Symlink(fifo, filepath.Join(workDir, "pipe-link")))

	service, err := NewFsGrepService(workDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	result, err := grepWithin(t, service, &models.FsGrepRequest{Path: workDir, Pattern: "needle"})
	require.NoError(t, err)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
	require.Equal(t, 1, result.TotalMatches)
	assert.Equal(t, "app.log", filepath.Base(result.Matches[0].Path))
	assert.False(t, result.Truncated)
}

func TestFsGrepService_WalkBoundsTruncate(t *testing.T) {
	t.Parallel()
	workDir := testutil.TempDir(t)
	for i := range 10 {
		require.NoError(t, os.WriteFile(filepath.Join(workDir, fmt.Sprintf("file-%02d.log", i)), []byte("needle\n"), 0644))
	}

	t.Run("entry bound", func(t *testing.T) {
		t.Parallel()
		service, err := NewFsGrepService(workDir, slog.New(slog.DiscardHandler))
		require.NoError(t, err)
		service.maxWalkEntries = 4 // the root plus three files

		result, err := grepWithin(t, service, &models.FsGrepRequest{Path: workDir, Pattern: "needle"})
		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.True(t, result.Truncated)
		assert.Equal(t, 3, result.TotalMatches)
	})

	t.Run("duration bound", func(t *testing.T) {
		t.Parallel()
		service, err := NewFsGrepService(workDir, slog.New(slog.DiscardHandler))
		require.NoError(t, err)
		service.maxWalkDuration = 0

		result, err := grepWithin(t, service, &models.FsGrepRequest{Path: workDir, Pattern: "needle"})
		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.True(t, result.Truncated)
		assert.Zero(t, result.TotalMatches)
	})

	t.Run("default bounds leave a small tree untruncated", func(t *testing.T) {
		t.Parallel()
		service, err := NewFsGrepService(workDir, slog.New(slog.DiscardHandler))
		require.NoError(t, err)

		result, err := grepWithin(t, service, &models.FsGrepRequest{Path: workDir, Pattern: "needle"})
		require.NoError(t, err)
		assert.False(t, result.Truncated)
		assert.Equal(t, 10, result.TotalMatches)
	})
}
