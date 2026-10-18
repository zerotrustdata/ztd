// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package execution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/logging"
)

// FileEditService handles file editing operations with modern best practices
type FileEditService struct {
	config  *config.Config
	logger  *slog.Logger
	fileSvc fs.RuntimeFileService
}

// NewFileEditService creates a new file editing service
func NewFileEditService(cfg *config.Config, logger *slog.Logger, fileSvc fs.RuntimeFileService) (*FileEditService, error) {
	if cfg == nil || logger == nil || fileSvc == nil {
		return nil, fmt.Errorf("file edit service: %w", constants.ErrMissingRequiredField)
	}
	fes := &FileEditService{
		config:  cfg,
		logger:  logger,
		fileSvc: fileSvc,
	}

	fes.logger.Info("File operations ready")
	return fes, nil
}

// ExecuteFileEdit performs a file editing operation
func (fes *FileEditService) ExecuteFileEdit(ctx context.Context, request *models.FileEditRequest) (*models.FileEditResult, error) {
	fes.logger.Info("Executing file edit operation",
		"execution_id", request.ExecutionID,
		"operation", request.Operation)

	startTime := time.Now().UTC()
	result := &models.FileEditResult{
		ExecutionID:     request.ExecutionID,
		CaseID:          request.CaseID,
		TaskID:          request.TaskID,
		InvestigationID: request.InvestigationID,
		Operation:       request.Operation,
		FilePath:        request.FilePath,
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
		StartTime:       startTime,
	}

	// Apply the runtime path policy and open the destination beneath a
	// directory handle; every operation below goes through that handle.
	target, err := fes.fileSvc.OpenFileEditTarget(ctx, request.FilePath, fes.config.WorkDir)
	if err != nil {
		result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		result.ErrorMessage = fmt.Sprintf("Invalid file path: %v", err)
		result.ErrorType = "validation_error"
		fes.finalizeResult(result)
		return result, fmt.Errorf("file edit: validate path: %w", err)
	}
	defer target.Close()
	request.FilePath = target.Path()

	// Execute operation based on type
	switch request.Operation {
	case constants.FileOperationRead:
		err = fes.executeRead(target, request, result)
	case constants.FileOperationWrite:
		err = fes.executeWrite(target, request, result)
	case constants.FileOperationReplace:
		err = fes.executeReplace(target, request, result)
	case constants.FileOperationInsert:
		err = fes.executeInsert(target, request, result)
	case constants.FileOperationDelete:
		err = fes.executeDelete(target, request, result)
	default:
		err = constants.ErrFileEditUnsupportedOperation
	}

	if err != nil {
		if result.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING {
			result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
			result.ErrorMessage = err.Error()
			result.ErrorType = "execution_error"
			if errors.Is(err, constants.ErrFileEditContentRequired) || errors.Is(err, constants.ErrFileEditOldContentRequired) || errors.Is(err, constants.ErrFileEditInsertContentRequired) || errors.Is(err, constants.ErrFileEditLineRangeRequired) {
				result.ErrorType = "validation_error"
			}
		}
	} else {
		result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	}

	// Finalize result
	fes.finalizeResult(result)

	logArgs := []any{
		"execution_id", request.ExecutionID,
		"operation", request.Operation,
		"status", result.Status,
		"duration_seconds", result.DurationSeconds,
	}

	// The error message can embed the requested path, so only its type is logged.
	if result.ErrorType != "" {
		logArgs = append(logArgs, "error_type", result.ErrorType)
	}

	if result.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
		logArgs = append(logArgs, logging.ErrorKind(err))
		fes.logger.Warn("File edit operation failed", logArgs...)
	} else {
		fes.logger.Info("File edit operation completed", logArgs...)
	}

	return result, nil
}

// OpenFileEditTarget applies the executing runtime's path policy and opens the
// destination for callers that read it outside ExecuteFileEdit, such as ledger
// snapshots. The caller must Close the target.
func (fes *FileEditService) OpenFileEditTarget(ctx context.Context, path string) (*fs.FileEditTarget, error) {
	return fes.fileSvc.OpenFileEditTarget(ctx, path, fes.config.WorkDir)
}

// executeRead reads a file or file section
func (fes *FileEditService) executeRead(target *fs.FileEditTarget, request *models.FileEditRequest, result *models.FileEditResult) error {
	fes.logger.Info("Reading file", "execution_id", request.ExecutionID)

	// Check if file exists
	fileInfo, err := target.Stat()
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", constants.ErrPathNotFound, request.FilePath)
		}
		return fmt.Errorf("file edit: stat file: %w", err)
	}

	// Collect file stats if requested
	if request.ReadOptions != nil && request.ReadOptions.IncludeStats {
		stats, err := fes.collectFileStats(target, fileInfo)
		if err != nil {
			fes.logger.Warn("Failed to collect file stats", logging.ErrorKind(err))
		} else {
			result.FileStats = stats
		}
	}

	// Read file content
	file, err := target.Open()
	if err != nil {
		return fmt.Errorf("file edit: open file: %w", err)
	}
	defer file.Close()

	// Handle line-based reading
	if request.ReadOptions != nil && (request.ReadOptions.StartLine != 0 || request.ReadOptions.EndLine != 0 || request.ReadOptions.MaxLines != 0) {
		content, err := fes.readFileLines(file, request.ReadOptions)
		if err != nil {
			return fmt.Errorf("file edit: read file lines: %w", err)
		}
		result.Content = content
	} else {
		// Read entire file with limit
		fileInfo, err := file.Stat()
		if err != nil {
			return fmt.Errorf("file edit: stat file: %w", err)
		}

		if fileInfo.Size() > constants.FileEditMaxSize {
			return fmt.Errorf("%w: %d bytes (max %d)", constants.ErrFileEditFileTooLarge, fileInfo.Size(), constants.FileEditMaxSize)
		}

		var buf bytes.Buffer
		_, err = io.Copy(&buf, io.LimitReader(file, constants.FileEditMaxSize))
		if err != nil {
			return fmt.Errorf("file edit: read file: %w", err)
		}
		content := buf.String()
		result.Content = content
	}

	fes.logger.Info("File read successfully",
		"execution_id", request.ExecutionID,
		"content_size", len(result.Content))

	return nil
}

// readFileLines reads specific lines from a file
func (fes *FileEditService) readFileLines(file *os.File, opts *models.FileReadOptions) (string, error) {
	scanner := bufio.NewScanner(file)
	var lines []string
	lineNum := 1

	startLine := 1
	if opts.StartLine != 0 {
		startLine = opts.StartLine
	}

	endLine := -1
	if opts.EndLine != 0 {
		endLine = opts.EndLine
	}

	maxLines := -1
	if opts.MaxLines != 0 {
		maxLines = opts.MaxLines
	}

	for scanner.Scan() {
		if lineNum >= startLine {
			if endLine > 0 && lineNum > endLine {
				break
			}
			if maxLines > 0 && len(lines) >= maxLines {
				break
			}
			lines = append(lines, scanner.Text())
		}
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return strings.Join(lines, "\n"), nil
}

// executeWrite writes content to a file (overwrites existing content)
func (fes *FileEditService) executeWrite(target *fs.FileEditTarget, request *models.FileEditRequest, result *models.FileEditResult) error {
	if request.Content == "" {
		return constants.ErrFileEditContentRequired
	}

	fes.logger.Info("Writing to file", "execution_id", request.ExecutionID)

	// Check if file exists
	fileInfo, err := target.Stat()
	fileExists := err == nil

	if !fileExists && !request.CreateIfMissing {
		return constants.ErrPathNotFound
	}

	// Create backup if requested and file exists
	if fileExists && request.CreateBackup {
		if fileInfo.Size() > constants.FileEditMaxSize {
			return fmt.Errorf("%w: %d bytes (max %d)", constants.ErrFileEditFileTooLarge, fileInfo.Size(), constants.FileEditMaxSize)
		}
		backupPath, err := fes.createBackup(target)
		if err != nil {
			return fmt.Errorf("file edit: create backup: %w", err)
		}
		result.BackupPath = backupPath
		fes.logger.Info("Backup created", "execution_id", request.ExecutionID)
	}

	// Write content to file, creating missing parent directories
	bytesWritten := int64(len(request.Content))
	if err := target.WriteFile([]byte(request.Content), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("file edit: write file: %w", err)
	}

	result.BytesWritten = bytesWritten

	// Count lines
	lines := strings.Count(request.Content, "\n") + 1
	result.LinesChanged = lines

	fes.logger.Info("File written successfully",
		"execution_id", request.ExecutionID,
		"bytes_written", bytesWritten,
		"lines", lines)

	return nil
}

// executeReplace replaces old content with new content in a file
func (fes *FileEditService) executeReplace(target *fs.FileEditTarget, request *models.FileEditRequest, result *models.FileEditResult) error {
	if request.OldContent == "" {
		return constants.ErrFileEditOldContentRequired
	}

	fes.logger.Info("Replacing content in file", "execution_id", request.ExecutionID)

	// Read current file content (always fresh read)
	fileInfo, err := target.Stat()
	if err != nil {
		return fmt.Errorf("file edit: stat file: %w", err)
	}
	if fileInfo.Size() > constants.FileEditMaxSize {
		return fmt.Errorf("%w: %d bytes (max %d)", constants.ErrFileEditFileTooLarge, fileInfo.Size(), constants.FileEditMaxSize)
	}

	content, err := target.ReadFile()
	if err != nil {
		return fmt.Errorf("file edit: read file: %w", err)
	}

	// Create backup if requested
	if request.CreateBackup {
		backupPath, err := fes.createBackup(target)
		if err != nil {
			return fmt.Errorf("file edit: create backup: %w", err)
		}
		result.BackupPath = backupPath
	}

	// Perform replacement
	oldStr := request.OldContent
	newStr := request.NewContent
	originalContent := string(content)

	// Check if old content exists in file (exact match required)
	if !strings.Contains(originalContent, oldStr) {
		preview := oldStr
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}
		// Provide actionable error message to help AI recover
		return fmt.Errorf("%w: old_content not found (exact match required). "+
			"You must READ the file first and copy the exact text including whitespace. "+
			"Do NOT guess or retry with variations. Use operation='read' on this file, "+
			"then copy the exact content from the read result. Searched for: %q", constants.ErrFileEditOldContentNotFound, preview)
	}

	// Replace content (exact match found) - replace all occurrences
	originalContent = strings.ReplaceAll(originalContent, oldStr, newStr)

	// Write back to file
	bytesWritten := int64(len(originalContent))
	if err := target.WriteFile([]byte(originalContent), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("file edit: write file: %w", err)
	}

	result.BytesWritten = bytesWritten

	// Count changed lines (approximation)
	linesChanged := strings.Count(oldStr, "\n") + strings.Count(newStr, "\n")
	result.LinesChanged = linesChanged

	fes.logger.Info("Content replaced successfully",
		"execution_id", request.ExecutionID,
		"bytes_written", bytesWritten,
		"lines_changed", linesChanged)

	return nil
}

// executeInsert inserts content at a specific line
func (fes *FileEditService) executeInsert(target *fs.FileEditTarget, request *models.FileEditRequest, result *models.FileEditResult) error {
	if request.InsertContent == "" || request.InsertPosition == 0 {
		return constants.ErrFileEditInsertContentRequired
	}

	fes.logger.Info("Inserting content into file",
		"execution_id", request.ExecutionID,
		"position", request.InsertPosition)

	// Read current file content
	fileInfo, err := target.Stat()
	if err != nil {
		return fmt.Errorf("file edit: stat file: %w", err)
	}
	if fileInfo.Size() > constants.FileEditMaxSize {
		return fmt.Errorf("%w: %d bytes (max %d)", constants.ErrFileEditFileTooLarge, fileInfo.Size(), constants.FileEditMaxSize)
	}

	content, err := target.ReadFile()
	if err != nil {
		return fmt.Errorf("file edit: read file: %w", err)
	}

	// Create backup if requested
	if request.CreateBackup {
		backupPath, err := fes.createBackup(target)
		if err != nil {
			return fmt.Errorf("file edit: create backup: %w", err)
		}
		result.BackupPath = backupPath
	}

	// Split content into lines
	lines := strings.Split(string(content), "\n")
	insertPos := request.InsertPosition - 1 // Convert to 0-indexed

	if insertPos < 0 || insertPos > len(lines) {
		return fmt.Errorf("%w: %d (file has %d lines)", constants.ErrFileEditInsertPositionOutOfRange, request.InsertPosition, len(lines))
	}

	// Insert new content
	insertLines := strings.Split(request.InsertContent, "\n")
	inserted := make([]string, 0, len(insertLines)+len(lines[insertPos:]))
	inserted = append(inserted, insertLines...)
	inserted = append(inserted, lines[insertPos:]...)
	newLines := make([]string, 0, insertPos+len(inserted))
	newLines = append(newLines, lines[:insertPos]...)
	newLines = append(newLines, inserted...)
	newContent := strings.Join(newLines, "\n")

	// Write back to file
	bytesWritten := int64(len(newContent))
	if err := target.WriteFile([]byte(newContent), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("file edit: write file: %w", err)
	}

	result.BytesWritten = bytesWritten
	linesChanged := len(insertLines)
	result.LinesChanged = linesChanged

	fes.logger.Info("Content inserted successfully",
		"execution_id", request.ExecutionID,
		"bytes_written", bytesWritten,
		"lines_inserted", linesChanged)

	return nil
}

// executeDelete deletes lines from a file
func (fes *FileEditService) executeDelete(target *fs.FileEditTarget, request *models.FileEditRequest, result *models.FileEditResult) error {
	if request.StartLine == 0 || request.EndLine == 0 {
		return constants.ErrFileEditLineRangeRequired
	}

	fes.logger.Info("Deleting lines from file",
		"execution_id", request.ExecutionID,
		"start_line", request.StartLine,
		"end_line", request.EndLine)

	// Read current file content
	fileInfo, err := target.Stat()
	if err != nil {
		return fmt.Errorf("file edit: stat file: %w", err)
	}
	if fileInfo.Size() > constants.FileEditMaxSize {
		return fmt.Errorf("%w: %d bytes (max %d)", constants.ErrFileEditFileTooLarge, fileInfo.Size(), constants.FileEditMaxSize)
	}

	content, err := target.ReadFile()
	if err != nil {
		return fmt.Errorf("file edit: read file: %w", err)
	}

	// Create backup if requested
	if request.CreateBackup {
		backupPath, err := fes.createBackup(target)
		if err != nil {
			return fmt.Errorf("file edit: create backup: %w", err)
		}
		result.BackupPath = backupPath
	}

	// Split content into lines
	lines := strings.Split(string(content), "\n")
	startLine := request.StartLine - 1 // Convert to 0-indexed
	endLine := request.EndLine - 1     // Convert to 0-indexed

	if startLine < 0 || endLine >= len(lines) || startLine > endLine {
		return fmt.Errorf("%w: %d-%d (file has %d lines)", constants.ErrFileEditInvalidLineRange, request.StartLine, request.EndLine, len(lines))
	}

	// Delete lines
	newLines := make([]string, 0, startLine+len(lines[endLine+1:]))
	newLines = append(newLines, lines[:startLine]...)
	newLines = append(newLines, lines[endLine+1:]...)
	newContent := strings.Join(newLines, "\n")

	// Write back to file
	bytesWritten := int64(len(newContent))
	if err := target.WriteFile([]byte(newContent), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("file edit: write file: %w", err)
	}

	result.BytesWritten = bytesWritten
	linesDeleted := (endLine - startLine + 1)
	result.LinesChanged = linesDeleted

	fes.logger.Info("Lines deleted successfully",
		"execution_id", request.ExecutionID,
		"bytes_written", bytesWritten,
		"lines_deleted", linesDeleted)

	return nil
}

// createBackup creates a backup of a file using streaming to prevent OOM
func (fes *FileEditService) createBackup(target *fs.FileEditTarget) (backupPath string, err error) {
	// Generate backup filename with timestamp and hash
	timestamp := time.Now().UTC().Format("20060102-150405")

	// Calculate file hash for uniqueness (streaming)
	file, err := target.Open()
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", constants.ErrPathNotFound, target.Path())
		}
		return "", err
	}
	defer file.Close()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	hashStr := hex.EncodeToString(h.Sum(nil))[:8]

	// Reset file pointer to beginning
	if _, err := file.Seek(0, 0); err != nil {
		return "", err
	}

	// Create backup file
	backupFile, backupPath, err := target.CreateSibling(fmt.Sprintf(constants.BackupFileSuffixPattern, timestamp, hashStr), constants.PermFilePublic)
	if err != nil {
		return "", err
	}
	defer func() {
		if backupFile != nil {
			if closeErr := backupFile.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}
	}()

	// Copy content streaming
	if _, err = io.Copy(backupFile, file); err != nil {
		return "", err
	}

	if err := backupFile.Close(); err != nil {
		backupFile = nil
		return "", err
	}
	backupFile = nil

	return backupPath, nil
}

// collectFileStats collects file statistics
func (fes *FileEditService) collectFileStats(target *fs.FileEditTarget, fileInfo os.FileInfo) (*models.FileStats, error) {
	stats := &models.FileStats{
		Size:    fileInfo.Size(),
		Mode:    fmt.Sprintf("%o", fileInfo.Mode().Perm()),
		ModTime: fileInfo.ModTime(),
	}

	// Count lines
	file, err := target.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	stats.Lines = lineCount

	// Check if symlink
	if linkInfo, err := target.Lstat(); err == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
		stats.IsSymlink = true
		if dest, err := target.Readlink(); err == nil {
			stats.SymlinkTarget = dest
		}
	}

	// Platform-specific ownership information
	if err := fes.collectFileOwnership(fileInfo, stats); err != nil {
		fes.logger.Warn("Failed to collect file ownership information",
			logging.ErrorKind(err))
		// Continue without ownership info - non-critical for file operations
	}

	return stats, nil
}

// finalizeResult finalizes the file edit result
func (fes *FileEditService) finalizeResult(result *models.FileEditResult) {
	finalizeTiming(result.StartTime, &result.EndTime, &result.DurationSeconds)
}
