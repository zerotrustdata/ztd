// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// rotatingLogFile is the daemon ztd.log writer. When a write would take the
// file past the rotation size, it copies ztd.log into ztd.log.1, shifting
// older backups up and removing the oldest, then truncates ztd.log in place.
//
// Copy-then-truncate keeps every other handle on ztd.log valid. A background
// Gateway's stdout and stderr are an inherited ztd.log handle, and
// `ztd gw logs -f` holds its own; renaming would strand both in a backup, and
// Windows refuses to rename a file another handle holds open. Every writer
// opens ztd.log with O_APPEND, so writes after the truncate start at offset 0.
type rotatingLogFile struct {
	mu     sync.Mutex
	svc    *LogService
	handle *os.File
	size   int64
}

func newRotatingLogFile(svc *LogService, handle *os.File) (*rotatingLogFile, error) {
	info, err := handle.Stat()
	if err != nil {
		return nil, fmt.Errorf("logging: stat log file: %w", err)
	}
	return &rotatingLogFile{svc: svc, handle: handle, size: info.Size()}, nil
}

// Write appends p, rotating first when p would take the file past the
// rotation size. A failed rotation does not drop the record: p is still
// written and the rotation error is returned.
func (w *rotatingLogFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var rotateErr error
	if w.size > 0 && w.size+int64(len(p)) > w.svc.maxBytes {
		rotateErr = w.svc.rotateLog(context.Background())
		// Count from zero either way. After a rotation the file is empty;
		// after a failed one the next attempt waits for another rotation's
		// worth of records instead of repeating the copy on every record.
		w.size = 0
	}
	n, err := w.handle.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, errors.Join(rotateErr, fmt.Errorf("logging: write log file: %w", err))
	}
	return n, rotateErr
}

// Close closes the underlying ztd.log handle.
func (w *rotatingLogFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.handle.Close()
}

// RotateLogBeforeStart rotates ztd.log when it is within the start headroom
// of the rotation size, and leaves a missing or smaller file alone. The
// process manager calls it before it opens ztd.log for a background Gateway
// and records the start-attempt offset, so a rotation during the child's
// startup does not move that output away from the offset the parent reads
// startup diagnostics and the approval-pending marker at.
func (s *LogService) RotateLogBeforeStart(ctx context.Context) error {
	info, err := s.fileSvc.Stat(ctx, logRelPath())
	if errors.Is(err, constants.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: stat log file: %w", constants.ErrLogRotate, err)
	}
	if info.Size()+s.startHeadroom < s.maxBytes {
		return nil
	}
	return s.rotateLog(ctx)
}

// rotateLog shifts the backups, copies ztd.log into ztd.log.1, and truncates
// ztd.log. Callers serialize it: the rotating writer holds its lock, and the
// process manager calls RotateLogBeforeStart before the Gateway child exists.
func (s *LogService) rotateLog(ctx context.Context) error {
	if err := s.fileSvc.Remove(ctx, backupRelPath(s.maxBackups)); err != nil {
		return fmt.Errorf("%w: remove oldest backup: %w", constants.ErrLogRotate, err)
	}
	for n := s.maxBackups - 1; n >= 1; n-- {
		from := backupRelPath(n)
		exists, err := s.fileSvc.FileExists(ctx, from)
		if err != nil {
			return fmt.Errorf("%w: check backup: %w", constants.ErrLogRotate, err)
		}
		if !exists {
			continue
		}
		if err := s.fileSvc.Rename(ctx, from, backupRelPath(n+1)); err != nil {
			return fmt.Errorf("%w: shift backup: %w", constants.ErrLogRotate, err)
		}
	}
	if err := s.copyLogToBackup(ctx); err != nil {
		return err
	}
	if err := s.fileSvc.Truncate(ctx, logRelPath(), 0); err != nil {
		return fmt.Errorf("%w: truncate log file: %w", constants.ErrLogRotate, err)
	}
	return nil
}

func (s *LogService) copyLogToBackup(ctx context.Context) error {
	src, err := s.fileSvc.OpenForRead(ctx, logRelPath())
	if err != nil {
		return fmt.Errorf("%w: open log file: %w", constants.ErrLogRotate, err)
	}
	dst, err := s.fileSvc.OpenForAppend(ctx, backupRelPath(1), constants.PermFilePrivate)
	if err != nil {
		return fmt.Errorf("%w: open backup: %w", constants.ErrLogRotate, errors.Join(err, src.Close()))
	}
	_, copyErr := io.Copy(dst, src)
	if err := errors.Join(copyErr, dst.Close(), src.Close()); err != nil {
		return fmt.Errorf("%w: copy log file: %w", constants.ErrLogRotate, err)
	}
	return nil
}

func logRelPath() string {
	return filepath.Join(constants.LogDirname, constants.ZtdLogFilename)
}

func backupRelPath(n int) string {
	return filepath.Join(constants.LogDirname, fmt.Sprintf(constants.ZtdLogBackupFilenameFormat, n))
}
