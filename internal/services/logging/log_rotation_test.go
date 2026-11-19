// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newRotationTestService returns a LogService over a real temporary runtime
// root with a small rotation threshold.
func newRotationTestService(t *testing.T, maxBytes int64, maxBackups int) (*LogService, fs.RuntimeFileService) {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	svc := NewLogService(fileSvc)
	svc.maxBytes = maxBytes
	svc.maxBackups = maxBackups
	svc.startHeadroom = 8
	return svc, fileSvc
}

func readRuntimeLog(t *testing.T, fileSvc fs.RuntimeFileService, name string) string {
	t.Helper()
	data, err := fileSvc.ReadFile(t.Context(), filepath.Join(constants.LogDirname, name))
	require.NoError(t, err)
	return string(data)
}

func backupName(n int) string {
	return fmt.Sprintf(constants.ZtdLogBackupFilenameFormat, n)
}

func TestLogService_WritePastThresholdRotatesIntoBackup(t *testing.T) {
	svc, fileSvc := newRotationTestService(t, 64, 3)
	logger, closer, err := svc.configureFileLogger(t.Context(), "info", nil)
	require.NoError(t, err)

	logger.Info(strings.Repeat("a", 60))
	logger.Info("after-rotation")
	require.NoError(t, closer.Close())

	assert.Contains(t, readRuntimeLog(t, fileSvc, backupName(1)), strings.Repeat("a", 60))
	current := readRuntimeLog(t, fileSvc, constants.ZtdLogFilename)
	assert.Contains(t, current, "after-rotation")
	assert.NotContains(t, current, strings.Repeat("a", 60))
}

func TestLogService_RotationKeepsOnlyMaxBackups(t *testing.T) {
	svc, fileSvc := newRotationTestService(t, 16, 2)
	logger, closer, err := svc.configureFileLogger(t.Context(), "info", nil)
	require.NoError(t, err)

	for i := range 5 {
		logger.Info(fmt.Sprintf("record-%d", i))
	}
	require.NoError(t, closer.Close())

	assert.Contains(t, readRuntimeLog(t, fileSvc, constants.ZtdLogFilename), "record-4")
	assert.Contains(t, readRuntimeLog(t, fileSvc, backupName(1)), "record-3")
	assert.Contains(t, readRuntimeLog(t, fileSvc, backupName(2)), "record-2")
	exists, err := fileSvc.FileExists(t.Context(), filepath.Join(constants.LogDirname, backupName(3)))
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestLogService_RotationKeepsInheritedHandleWriting covers the background
// Gateway, whose stdout and stderr are an inherited ztd.log handle: after a
// rotation, raw writes through that handle must land in the current ztd.log,
// not in a backup.
func TestLogService_RotationKeepsInheritedHandleWriting(t *testing.T) {
	svc, fileSvc := newRotationTestService(t, 64, 3)
	inherited, err := svc.OpenLogForAppend(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inherited.Close() })

	logger, closer, err := svc.configureFileLogger(t.Context(), "info", inherited)
	require.NoError(t, err)
	logger.Info(strings.Repeat("b", 60))
	logger.Info("after-rotation")
	_, err = inherited.WriteString("raw-stderr-line\n")
	require.NoError(t, err)
	require.NoError(t, closer.Close())

	current := readRuntimeLog(t, fileSvc, constants.ZtdLogFilename)
	assert.Contains(t, current, "after-rotation")
	assert.Contains(t, current, "raw-stderr-line")
	assert.Equal(t, 1, strings.Count(current, "after-rotation"))
	assert.NotContains(t, readRuntimeLog(t, fileSvc, backupName(1)), "raw-stderr-line")
}

func TestLogService_RotateLogBeforeStart(t *testing.T) {
	tests := []struct {
		name         string
		existing     string
		wantRotated  bool
		wantNoLogYet bool
	}{
		{name: "missing log is left alone", wantNoLogYet: true},
		{name: "log under start headroom is left alone", existing: "short\n"},
		{name: "log within start headroom rotates", existing: strings.Repeat("c", 26), wantRotated: true},
		{name: "log at threshold rotates", existing: strings.Repeat("c", 32), wantRotated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fileSvc := newRotationTestService(t, 32, 3)
			ctx := t.Context()
			logRel := filepath.Join(constants.LogDirname, constants.ZtdLogFilename)
			if !tt.wantNoLogYet {
				require.NoError(t, fileSvc.WriteFile(ctx, logRel, []byte(tt.existing), constants.PermFilePrivate))
			}

			require.NoError(t, svc.RotateLogBeforeStart(ctx))

			backupExists, err := fileSvc.FileExists(ctx, filepath.Join(constants.LogDirname, backupName(1)))
			require.NoError(t, err)
			assert.Equal(t, tt.wantRotated, backupExists)
			if tt.wantNoLogYet {
				return
			}
			if tt.wantRotated {
				assert.Equal(t, tt.existing, readRuntimeLog(t, fileSvc, backupName(1)))
				assert.Empty(t, readRuntimeLog(t, fileSvc, constants.ZtdLogFilename))
				return
			}
			assert.Equal(t, tt.existing, readRuntimeLog(t, fileSvc, constants.ZtdLogFilename))
		})
	}
}

func TestLogService_RotatedBackupIsPrivate(t *testing.T) {
	svc, fileSvc := newRotationTestService(t, 8, 1)
	ctx := t.Context()
	require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join(constants.LogDirname, constants.ZtdLogFilename), []byte("0123456789"), constants.PermFilePrivate))

	require.NoError(t, svc.RotateLogBeforeStart(ctx))

	info, err := fileSvc.Stat(ctx, filepath.Join(constants.LogDirname, backupName(1)))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(constants.PermFilePrivate), info.Mode().Perm())
	}
}
