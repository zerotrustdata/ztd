// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// LogService owns the platform log file lifecycle: path resolution, file
// open (via RuntimeFileService), and logger configuration. Daemon-mode
// callers (RunGateway, StartOperator) go through LogService for all
// .ztd/logs/ztd.log I/O. CLI client commands use NewStdoutLogger directly
// and do not construct a LogService.
type LogService struct {
	fileSvc fs.RuntimeFileService
}

// NewLogService constructs a LogService backed by the given file service.
func NewLogService(fileSvc fs.RuntimeFileService) *LogService {
	return &LogService{fileSvc: fileSvc}
}

// ConfigureFileLogger opens ztd.log via the file service and returns a
// slog.Logger writing to it and stderr. The caller must close the returned
// handle on shutdown. Ensures the log directory exists.
func (s *LogService) ConfigureFileLogger(ctx context.Context, level string) (*slog.Logger, *os.File, error) {
	return s.configureFileLogger(ctx, level, os.Stderr)
}

func (s *LogService) configureFileLogger(ctx context.Context, level string, stderr *os.File) (*slog.Logger, *os.File, error) {
	handle, err := s.OpenLogForAppend(ctx)
	if err != nil {
		return nil, nil, err
	}
	logger, err := NewLogger(level, logWriter(handle, stderr))
	if err != nil {
		_ = handle.Close()
		return nil, nil, fmt.Errorf("logging: configure logger: %w", err)
	}
	return logger, handle, nil
}

// logWriter returns the log file alone when stderr already is that file. A
// background Gateway is started with its stderr redirected to ztd.log, so
// writing to both would record every line twice.
func logWriter(handle, stderr *os.File) io.Writer {
	if stderr == nil {
		return handle
	}
	handleInfo, err := handle.Stat()
	if err != nil {
		return io.MultiWriter(handle, stderr)
	}
	stderrInfo, err := stderr.Stat()
	if err == nil && os.SameFile(handleInfo, stderrInfo) {
		return handle
	}
	return io.MultiWriter(handle, stderr)
}

// OpenLogForAppend opens ztd.log for append via the file service, without
// configuring a logger. Used by the process manager to capture stdout/stderr
// of the re-exec'd child process.
func (s *LogService) OpenLogForAppend(ctx context.Context) (*os.File, error) {
	if err := s.fileSvc.MkdirAll(ctx, constants.LogDirname, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("logging: create log dir: %w", err)
	}
	relPath := filepath.Join(constants.LogDirname, constants.ZtdLogFilename)
	handle, err := s.fileSvc.OpenForAppend(ctx, relPath, constants.PermFilePrivate)
	if err != nil {
		return nil, fmt.Errorf("logging: open log file: %w", err)
	}
	return handle, nil
}

// OpenLogForRead opens ztd.log for streaming read (tail/follow). Returns
// constants.ErrNotFound if no log file exists yet.
func (s *LogService) OpenLogForRead(ctx context.Context) (*os.File, error) {
	relPath := filepath.Join(constants.LogDirname, constants.ZtdLogFilename)
	handle, err := s.fileSvc.OpenForRead(ctx, relPath)
	if err != nil {
		return nil, fmt.Errorf("logging: open log for read: %w", err)
	}
	return handle, nil
}

// LogFileExists checks whether ztd.log exists. Returns false, nil if absent.
func (s *LogService) LogFileExists(ctx context.Context) (bool, error) {
	return s.fileSvc.FileExists(ctx, filepath.Join(constants.LogDirname, constants.ZtdLogFilename))
}

// LogFilePath returns the absolute path to ztd.log. Used for error messages
// and diagnostics, not for file I/O (use OpenLogForAppend / OpenLogForRead).
func (s *LogService) LogFilePath() string {
	return s.fileSvc.Resolve(filepath.Join(constants.LogDirname, constants.ZtdLogFilename))
}
