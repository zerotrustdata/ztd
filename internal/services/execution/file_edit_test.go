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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newTestFileEditService(t *testing.T, cfg *config.Config, logger *slog.Logger) *FileEditService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(cfg.WorkDir, logger)
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	svc, err := NewFileEditService(cfg, logger, fileSvc)
	require.NoError(t, err)
	return svc
}

func TestFileEditService_ExecuteFileEdit_Write(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	svc := newTestFileEditService(t, cfg, logger)

	t.Run("write to new file", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		content := "test content"

		req := &models.FileEditRequest{
			ExecutionID:     "test-req-1",
			CaseID:          "test-case-1",
			Operation:       constants.FileOperationWrite,
			FilePath:        tmpFile,
			Content:         content,
			CreateIfMissing: true,
			RequestedBy:     "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.Equal(t, int64(12), result.BytesWritten)

		// Verify file was created
		data, err := os.ReadFile(tmpFile)
		require.NoError(t, err)
		assert.Equal(t, content, string(data))
	})

	t.Run("write with backup", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)

		// Create initial file
		os.WriteFile(tmpFile, []byte("original"), constants.PermFilePublic)

		newContent := "updated content"
		req := &models.FileEditRequest{
			ExecutionID:  "test-req-2",
			CaseID:       "test-case-2",
			Operation:    constants.FileOperationWrite,
			FilePath:     tmpFile,
			Content:      newContent,
			CreateBackup: true,
			RequestedBy:  "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.NotEmpty(t, result.BackupPath)
		assert.FileExists(t, result.BackupPath)

		// Verify backup contains original content
		backupData, err := os.ReadFile(result.BackupPath)
		require.NoError(t, err)
		assert.Equal(t, "original", string(backupData))

		// Verify file has new content
		data, err := os.ReadFile(tmpFile)
		require.NoError(t, err)
		assert.Equal(t, newContent, string(data))
	})

	t.Run("write without create_if_missing fails", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestNonexistentTxtFilename)
		content := "test"

		req := &models.FileEditRequest{
			ExecutionID:     "test-req-3",
			CaseID:          "test-case-3",
			Operation:       constants.FileOperationWrite,
			FilePath:        tmpFile,
			Content:         content,
			CreateIfMissing: false,
			RequestedBy:     "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
		assert.NotNil(t, result.ErrorMessage)
	})
}

func TestFileEditService_ExecuteFileEdit_Read(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	svc := newTestFileEditService(t, cfg, logger)

	t.Run("read entire file", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		content := "line1\nline2\nline3"
		os.WriteFile(tmpFile, []byte(content), constants.PermFilePublic)

		req := &models.FileEditRequest{
			ExecutionID: "test-req-1",
			CaseID:      "test-case-1",
			Operation:   constants.FileOperationRead,
			FilePath:    tmpFile,
			RequestedBy: "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.NotEmpty(t, result.Content)
		assert.Equal(t, content, result.Content)
	})

	t.Run("read specific lines", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		os.WriteFile(tmpFile, []byte("line1\nline2\nline3\nline4\nline5"), constants.PermFilePublic)

		startLine := 2
		endLine := 4
		req := &models.FileEditRequest{
			ExecutionID: "test-req-2",
			CaseID:      "test-case-2",
			Operation:   constants.FileOperationRead,
			FilePath:    tmpFile,
			ReadOptions: &models.FileReadOptions{
				StartLine: startLine,
				EndLine:   endLine,
			},
			RequestedBy: "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.Contains(t, result.Content, "line2")
		assert.Contains(t, result.Content, "line3")
		assert.Contains(t, result.Content, "line4")
	})

	t.Run("read non-existent file", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		req := &models.FileEditRequest{
			ExecutionID: "test-req-3",
			CaseID:      "test-case-3",
			Operation:   constants.FileOperationRead,
			FilePath:    filepath.Join(testutil.OperatorDir(t), constants.TestNonexistentTxtFilename),
			RequestedBy: "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
		assert.NotNil(t, result.ErrorMessage)
	})

	t.Run("read with file stats", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		os.WriteFile(tmpFile, []byte("test content"), constants.PermFilePublic)

		includeStats := true
		req := &models.FileEditRequest{
			ExecutionID: "test-req-4",
			CaseID:      "test-case-4",
			Operation:   constants.FileOperationRead,
			FilePath:    tmpFile,
			ReadOptions: &models.FileReadOptions{
				IncludeStats: includeStats,
			},
			RequestedBy: "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.NotNil(t, result.FileStats)
		assert.Positive(t, result.FileStats.Size)
	})
}

func TestFileEditService_ExecuteFileEdit_Replace(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	svc := newTestFileEditService(t, cfg, logger)

	t.Run("replace content", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		os.WriteFile(tmpFile, []byte("hello world"), constants.PermFilePublic)

		oldContent := "world"
		newContent := "universe"
		req := &models.FileEditRequest{
			ExecutionID: "test-req-1",
			CaseID:      "test-case-1",
			Operation:   constants.FileOperationReplace,
			FilePath:    tmpFile,
			OldContent:  oldContent,
			NewContent:  newContent,
			RequestedBy: "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)

		// Verify replacement
		data, err := os.ReadFile(tmpFile)
		require.NoError(t, err)
		assert.Equal(t, "hello universe", string(data))
	})

	t.Run("replace missing content fails", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		os.WriteFile(tmpFile, []byte("hello world"), constants.PermFilePublic)

		oldContent := "missing"
		newContent := "replacement"
		req := &models.FileEditRequest{
			ExecutionID: "test-req-2",
			CaseID:      "test-case-2",
			Operation:   constants.FileOperationReplace,
			FilePath:    tmpFile,
			OldContent:  oldContent,
			NewContent:  newContent,
			RequestedBy: "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
		assert.Contains(t, result.ErrorMessage, "not found")
	})
}

func TestFileEditService_ExecuteFileEdit_Insert(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	svc := newTestFileEditService(t, cfg, logger)

	t.Run("insert at position", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		os.WriteFile(tmpFile, []byte("line1\nline3"), constants.PermFilePublic)

		insertContent := "line2"
		insertPos := 2
		req := &models.FileEditRequest{
			ExecutionID:    "test-req-1",
			CaseID:         "test-case-1",
			Operation:      constants.FileOperationInsert,
			FilePath:       tmpFile,
			InsertContent:  insertContent,
			InsertPosition: insertPos,
			RequestedBy:    "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)

		// Verify insertion
		data, err := os.ReadFile(tmpFile)
		require.NoError(t, err)
		assert.Contains(t, string(data), "line1\nline2\nline3")
	})

	t.Run("insert at invalid position fails", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		os.WriteFile(tmpFile, []byte("line1"), constants.PermFilePublic)

		insertContent := "test"
		insertPos := 100
		req := &models.FileEditRequest{
			ExecutionID:    "test-req-2",
			CaseID:         "test-case-2",
			Operation:      constants.FileOperationInsert,
			FilePath:       tmpFile,
			InsertContent:  insertContent,
			InsertPosition: insertPos,
			RequestedBy:    "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
	})
}

func TestFileEditService_ExecuteFileEdit_Delete(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	svc := newTestFileEditService(t, cfg, logger)

	t.Run("delete lines", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		os.WriteFile(tmpFile, []byte("line1\nline2\nline3\nline4"), constants.PermFilePublic)

		startLine := 2
		endLine := 3
		req := &models.FileEditRequest{
			ExecutionID: "test-req-1",
			CaseID:      "test-case-1",
			Operation:   constants.FileOperationDelete,
			FilePath:    tmpFile,
			StartLine:   startLine,
			EndLine:     endLine,
			RequestedBy: "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)

		// Verify deletion
		data, err := os.ReadFile(tmpFile)
		require.NoError(t, err)
		assert.Contains(t, string(data), "line1")
		assert.NotContains(t, string(data), "line2")
		assert.NotContains(t, string(data), "line3")
		assert.Contains(t, string(data), "line4")
	})

	t.Run("delete invalid range fails", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tmpDir := testutil.OperatorDir(t)
		tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
		os.WriteFile(tmpFile, []byte("line1"), constants.PermFilePublic)

		startLine := 5
		endLine := 10
		req := &models.FileEditRequest{
			ExecutionID: "test-req-2",
			CaseID:      "test-case-2",
			Operation:   constants.FileOperationDelete,
			FilePath:    tmpFile,
			StartLine:   startLine,
			EndLine:     endLine,
			RequestedBy: "test-user",
		}

		result, err := svc.ExecuteFileEdit(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
	})
}

func TestFileEditService_RejectsPatchWithoutFilesystemChanges(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := testutil.NewTestConfig(t)
	svc := newTestFileEditService(t, cfg, testutil.NewTestLogger())
	for _, exists := range []bool{true, false} { //nolint:paralleltest // mutates package-level state
		t.Run(fmt.Sprintf("file_exists_%t", exists), func(t *testing.T) {

			tmpDir := testutil.OperatorDir(t)
			tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
			const content = "original content"
			if exists {
				require.NoError(t, os.WriteFile(tmpFile, []byte(content), constants.PermFilePublic))
			}
			before, err := os.ReadDir(tmpDir)
			require.NoError(t, err)
			result, err := svc.ExecuteFileEdit(t.Context(), &models.FileEditRequest{
				ExecutionID:     "unsupported-patch",
				Operation:       "patch",
				FilePath:        tmpFile,
				CreateBackup:    true,
				CreateIfMissing: true,
			})
			require.NoError(t, err)
			require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
			require.Equal(t, constants.ErrFileEditUnsupportedOperation.Error(), result.ErrorMessage)
			require.Empty(t, result.BackupPath)
			after, err := os.ReadDir(tmpDir)
			require.NoError(t, err)
			require.Len(t, after, len(before))
			if exists {
				data, err := os.ReadFile(tmpFile)
				require.NoError(t, err)
				require.Equal(t, content, string(data))
			} else {
				require.NoFileExists(t, tmpFile)
			}
		})
	}
}

func TestFileEditService_CreateBackup(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	svc := newTestFileEditService(t, cfg, logger)

	tmpDir := testutil.OperatorDir(t)
	tmpFile := filepath.Join(tmpDir, constants.TestFileTxtFilename)
	content := "original content"
	os.WriteFile(tmpFile, []byte(content), constants.PermFilePublic)

	target, err := svc.fileSvc.OpenFileEditTarget(t.Context(), tmpFile, cfg.WorkDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, target.Close()) })
	backupPath, err := svc.createBackup(target)

	require.NoError(t, err)
	assert.NotEmpty(t, backupPath)
	assert.FileExists(t, backupPath)

	// Verify backup has same content
	backupData, err := os.ReadFile(backupPath)
	require.NoError(t, err)
	assert.Equal(t, content, string(backupData))
}
