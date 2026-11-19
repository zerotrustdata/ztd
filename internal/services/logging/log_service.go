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
	// maxBytes, maxBackups, and startHeadroom bound ztd.log rotation
	// (constants.ZtdLogMaxBytes, constants.ZtdLogMaxBackups,
	// constants.ZtdLogStartHeadroomBytes).
	maxBytes      int64
	maxBackups    int
	startHeadroom int64
}

// NewLogService constructs a LogService backed by the given file service.
func NewLogService(fileSvc fs.RuntimeFileService) *LogService {
	return &LogService{
		fileSvc:       fileSvc,
		maxBytes:      constants.ZtdLogMaxBytes,
		maxBackups:    constants.ZtdLogMaxBackups,
		startHeadroom: constants.ZtdLogStartHeadroomBytes,
	}
}

// ConfigureFileLogger opens ztd.log via the file service and returns a
// slog.Logger writing to it and stderr. ztd.log rotates by size as records
// are written (see rotatingLogFile). The caller must close the returned
// writer on shutdown. Ensures the log directory exists.
func (s *LogService) ConfigureFileLogger(ctx context.Context, level string) (*slog.Logger, io.Closer, error) {
	return s.configureFileLogger(ctx, level, os.Stderr)
}

func (s *LogService) configureFileLogger(ctx context.Context, level string, stderr *os.File) (*slog.Logger, io.Closer, error) {
	handle, err := s.OpenLogForAppend(ctx)
	if err != nil {
		return nil, nil, err
	}
	file, err := newRotatingLogFile(s, handle)
	if err != nil {
		_ = handle.Close()
		return nil, nil, err
	}
	logger, err := NewLogger(level, logWriter(file, stderr))
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("logging: configure logger: %w", err)
	}
	return logger, file, nil
}

// logWriter returns the log file alone when stderr already is that file. A
// background Gateway is started with its stderr redirected to ztd.log, so
// writing to both would record every line twice.
func logWriter(file *rotatingLogFile, stderr *os.File) io.Writer {
	if stderr == nil {
		return file
	}
	handleInfo, err := file.handle.Stat()
	if err != nil {
		return io.MultiWriter(file, stderr)
	}
	stderrInfo, err := stderr.Stat()
	if err == nil && os.SameFile(handleInfo, stderrInfo) {
		return file
	}
	return io.MultiWriter(file, stderr)
}

// OpenLogForAppend opens ztd.log for append via the file service, without
// configuring a logger. Used by the process manager to capture stdout/stderr
// of the re-exec'd child process.
func (s *LogService) OpenLogForAppend(ctx context.Context) (*os.File, error) {
	if err := s.fileSvc.MkdirAll(ctx, constants.LogDirname, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("logging: create log dir: %w", err)
	}
	handle, err := s.fileSvc.OpenForAppend(ctx, logRelPath(), constants.PermFilePrivate)
	if err != nil {
		return nil, fmt.Errorf("logging: open log file: %w", err)
	}
	return handle, nil
}

// OpenLogForRead opens ztd.log for streaming read (tail/follow). Returns
// constants.ErrNotFound if no log file exists yet.
func (s *LogService) OpenLogForRead(ctx context.Context) (*os.File, error) {
	handle, err := s.fileSvc.OpenForRead(ctx, logRelPath())
	if err != nil {
		return nil, fmt.Errorf("logging: open log for read: %w", err)
	}
	return handle, nil
}

// LogFileExists checks whether ztd.log exists. Returns false, nil if absent.
func (s *LogService) LogFileExists(ctx context.Context) (bool, error) {
	return s.fileSvc.FileExists(ctx, logRelPath())
}

// LogFilePath returns the absolute path to ztd.log. Used for error messages
// and diagnostics, not for file I/O (use OpenLogForAppend / OpenLogForRead).
func (s *LogService) LogFilePath() string {
	return s.fileSvc.Resolve(logRelPath())
}
