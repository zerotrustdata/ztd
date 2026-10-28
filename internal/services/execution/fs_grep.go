// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package execution

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/security"
	"github.com/zerotrustdata/ztd/internal/services/logging"
)

// FsGrepService handles recursive grep operations
type FsGrepService struct {
	workDir         string
	logger          *slog.Logger
	maxWalkEntries  int
	maxWalkDuration time.Duration
}

// fsGrepTarget is the kind of search root a grep accepts.
type fsGrepTarget int

const (
	fsGrepTargetAny fsGrepTarget = iota
	fsGrepTargetFile
	fsGrepTargetDirectory
)

// NewFsGrepService creates a new FsGrepService rooted at workDir, against
// which relative search paths resolve.
func NewFsGrepService(workDir string, logger *slog.Logger) (*FsGrepService, error) {
	if workDir == "" {
		return nil, fmt.Errorf("fs_grep service: work dir: %w", constants.ErrMissingRequiredField)
	}
	if logger == nil {
		return nil, fmt.Errorf("fs_grep service: logger: %w", constants.ErrMissingRequiredField)
	}
	return &FsGrepService{
		workDir:         workDir,
		logger:          logger,
		maxWalkEntries:  constants.FsGrepMaxWalkEntries,
		maxWalkDuration: constants.FsGrepMaxWalkDuration,
	}, nil
}

// ExecuteFsGrep performs a recursive grep operation over a file or directory.
func (s *FsGrepService) ExecuteFsGrep(ctx context.Context, req *models.FsGrepRequest) (*models.FsGrepResult, error) {
	return s.grep(ctx, req, fsGrepTargetAny)
}

// SearchFile greps one file. A directory path fails with
// constants.ErrPathIsDirectory.
func (s *FsGrepService) SearchFile(ctx context.Context, req *models.FsGrepRequest) (*models.FsGrepResult, error) {
	return s.grep(ctx, req, fsGrepTargetFile)
}

// SearchDirectory greps every file under one directory. A file path fails with
// constants.ErrNotADirectory.
func (s *FsGrepService) SearchDirectory(ctx context.Context, req *models.FsGrepRequest) (*models.FsGrepResult, error) {
	return s.grep(ctx, req, fsGrepTargetDirectory)
}

func (s *FsGrepService) grep(ctx context.Context, req *models.FsGrepRequest, target fsGrepTarget) (*models.FsGrepResult, error) {
	startTime := time.Now().UTC()
	s.logger.Info("Executing fs_grep operation",
		"execution_id", req.ExecutionID,
		"max_matches", req.MaxMatches)

	result := &models.FsGrepResult{
		ExecutionID:     req.ExecutionID,
		CaseID:          req.CaseID,
		TaskID:          req.TaskID,
		InvestigationID: req.InvestigationID,
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
		Path:            req.Path,
		Pattern:         req.Pattern,
		Matches:         []models.FsGrepMatch{},
	}
	result.StartTime = startTime

	// Resolve path
	path := req.Path
	if path == "" || path == constants.PathCurrentDir {
		path = s.workDir
	}

	// Validate and resolve path (security check)
	absPath, err := security.ValidatePath(path, s.workDir)
	if err != nil {
		return s.failResult(result, constants.ErrFsGrepValidation, fmt.Errorf("fs_grep: %w: %w: %w", constants.ErrFsGrepValidation, constants.ErrPathValidation, err))
	}

	result.Path = absPath

	if security.IsSensitiveReadPath(absPath) {
		return s.failResult(result, constants.ErrFsGrepValidation, fmt.Errorf("fs_grep: %w: %w: %s", constants.ErrFsGrepValidation, constants.ErrSensitivePath, absPath))
	}

	rootInfo, err := os.Stat(absPath)
	if errors.Is(err, fs.ErrNotExist) {
		return s.failResult(result, constants.ErrFsGrepValidation, fmt.Errorf("fs_grep: %w: %w: %s", constants.ErrFsGrepValidation, constants.ErrPathNotFound, absPath))
	}
	if err != nil {
		return s.failResult(result, constants.ErrFsGrepValidation, fmt.Errorf("fs_grep: %w: %w: %w", constants.ErrFsGrepValidation, constants.ErrStatFailed, err))
	}
	if target == fsGrepTargetFile && rootInfo.IsDir() {
		return s.failResult(result, constants.ErrFsGrepValidation, fmt.Errorf("fs_grep: %w: %w: %s", constants.ErrFsGrepValidation, constants.ErrPathIsDirectory, absPath))
	}
	if target == fsGrepTargetDirectory && !rootInfo.IsDir() {
		return s.failResult(result, constants.ErrFsGrepValidation, fmt.Errorf("fs_grep: %w: %w: %s", constants.ErrFsGrepValidation, constants.ErrNotADirectory, absPath))
	}

	// Compile regex
	re, err := regexp.Compile(req.Pattern)
	if err != nil {
		return s.failResult(result, constants.ErrFsGrepInvalidPattern, fmt.Errorf("fs_grep: %w: %w", constants.ErrFsGrepInvalidPattern, err))
	}

	// Prepare includes filters
	var includePatterns []*regexp.Regexp
	for _, inc := range req.Includes {
		// Convert glob-ish patterns to regex if needed, or just use simple string matching
		// For simplicity, we'll treat them as substrings or basic globs
		p := strings.ReplaceAll(inc, ".", "\\.")
		p = strings.ReplaceAll(p, "*", ".*")
		ir, err := regexp.Compile("^" + p + "$")
		if err == nil {
			includePatterns = append(includePatterns, ir)
		}
	}

	maxMatches := req.MaxMatches
	if maxMatches <= 0 {
		maxMatches = constants.FsGrepDefaultMaxMatches
	}
	if maxMatches > constants.FsGrepMaxMatches {
		maxMatches = constants.FsGrepMaxMatches
	}

	matches := []models.FsGrepMatch{}
	truncated := false
	// The walk is bounded by entries visited and by elapsed time; reaching
	// either bound ends the search with a truncated result, not a failure.
	walkDeadline := startTime.Add(s.maxWalkDuration)
	walkEntries := 0

	err = filepath.WalkDir(absPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			s.logger.Debug("Skipping inaccessible path during walk", slog.String("execution_id", req.ExecutionID), logging.ErrorKind(err))
			return nil // Skip files we can't access
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		walkEntries++
		if walkEntries > s.maxWalkEntries || time.Now().After(walkDeadline) {
			s.logger.Warn("fs_grep walk bound reached; returning truncated result",
				"execution_id", req.ExecutionID,
				"entries", walkEntries-1)
			truncated = true
			return io.EOF
		}

		// Security: Check for symlinks and enforce boundary constraints
		info, err := d.Info()
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			// Resolve symlink target
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				s.logger.Debug("Skipping symlink that cannot be resolved", slog.String("execution_id", req.ExecutionID), logging.ErrorKind(err))
				// Skip symlinks we can't resolve
				return nil
			}

			// Check if resolved target is within the base directory
			rel, err := filepath.Rel(absPath, target)
			if err != nil {
				s.logger.Debug("Skipping symlink with unresolvable relative path", slog.String("execution_id", req.ExecutionID), logging.ErrorKind(err))
				// Skip symlinks we can't resolve relative path
				return nil
			}

			// If the relative path starts with "..", it points outside the base directory
			if strings.HasPrefix(rel, "..") {
				s.logger.Warn("Skipping symlink that points outside search boundary",
					"execution_id", req.ExecutionID)
				return nil
			}
			if security.IsSensitiveReadPath(target) {
				return nil
			}
		}

		if d.IsDir() {
			// Skip hidden directories (like .git) below the root; a hidden
			// root was named explicitly and is searched.
			if path != absPath && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}

		if security.IsSensitiveReadPath(path) {
			s.logger.Debug("Skipping sensitive file during walk", slog.String("execution_id", req.ExecutionID))
			return nil
		}

		// Apply include filters if any
		if len(includePatterns) > 0 {
			matched := false
			rel, err := filepath.Rel(absPath, path)
			if err != nil {
				s.logger.Debug("Failed to get relative path for include filter", slog.String("execution_id", req.ExecutionID), logging.ErrorKind(err))
				return nil
			}
			for _, ip := range includePatterns {
				if ip.MatchString(rel) || ip.MatchString(d.Name()) {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}

		// Search in file
		fileMatches, err := s.searchInFile(path, re, maxMatches-len(matches), walkDeadline)
		if err != nil {
			s.logger.Debug("Skipping file that cannot be read", slog.String("execution_id", req.ExecutionID), logging.ErrorKind(err))
			return nil // Skip files we can't read
		}

		matches = append(matches, fileMatches...)

		if len(matches) >= maxMatches || time.Now().After(walkDeadline) {
			truncated = true
			return io.EOF // Stop walking
		}

		return nil
	})

	if err != nil && err != io.EOF {
		return s.failResult(result, constants.ErrFsGrepExecution, fmt.Errorf("fs_grep: %w: %w", constants.ErrFsGrepExecution, err))
	}

	result.Matches = matches
	result.TotalMatches = len(matches)
	result.Truncated = truncated
	result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED

	endTime := time.Now().UTC()
	result.EndTime = endTime
	result.DurationSeconds = endTime.Sub(startTime).Seconds()

	s.logger.Info("fs_grep operation completed",
		"execution_id", req.ExecutionID,
		"matches", len(matches),
		"truncated", truncated,
		"duration_ms", result.DurationSeconds*1000)

	return result, nil
}

func (s *FsGrepService) searchInFile(path string, re *regexp.Regexp, limit int, deadline time.Time) ([]models.FsGrepMatch, error) {
	// Only regular files are read: opening a FIFO, device or socket, directly
	// or through a symlink, can block indefinitely.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("fs_grep: stat %s: %w: %w", path, constants.ErrStatFailed, err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("fs_grep: failed to open file %s: %w", path, constants.ErrFileOpenFailed)
	}
	defer file.Close()

	var matches []models.FsGrepMatch
	scanner := bufio.NewScanner(file)
	// Limit line size to avoid OOM
	buf := make([]byte, constants.FsGrepScannerInitialBufSize)
	scanner.Buffer(buf, constants.FsGrepScannerMaxBufSize)

	lineNum := 0
	for scanner.Scan() {
		if time.Now().After(deadline) {
			break
		}
		lineNum++
		text := scanner.Text()
		if re.MatchString(text) {
			matches = append(matches, models.FsGrepMatch{
				Path:       path,
				LineNumber: lineNum,
				Content:    text,
			})
			if len(matches) >= limit {
				break
			}
		}
	}

	return matches, scanner.Err()
}

func (s *FsGrepService) failResult(result *models.FsGrepResult, errType error, err error) (*models.FsGrepResult, error) {
	result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	result.ErrorType = errType.Error()
	result.ErrorMessage = err.Error()
	endTime := time.Now().UTC()
	result.EndTime = endTime
	if !result.StartTime.IsZero() {
		result.DurationSeconds = endTime.Sub(result.StartTime).Seconds()
	}
	return result, err
}
