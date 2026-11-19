// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func receiveFollowedLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line := <-lines:
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for followed line")
		return ""
	}
}

// TestFollowLog_ResumesFromStartAfterRotation covers `gw logs -f` across a
// ztd.log rotation, which copies the file into a backup and truncates it in
// place: records written after the truncate must still be followed.
func TestFollowLog_ResumesFromStartAfterRotation(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), constants.ZtdLogFilename)
	require.NoError(t, os.WriteFile(path, []byte("existing record that pushes the offset past the rotated size\n"), constants.PermFilePrivate))
	writer, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, constants.PermFilePrivate)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	reader, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	offset, err := reader.Seek(0, io.SeekEnd)
	require.NoError(t, err)

	lines := make(chan string)
	done := make(chan struct{})
	var wg sync.WaitGroup
	var followErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		followErr = followLog(reader, offset, done, lines)
	}()
	t.Cleanup(func() {
		close(done)
		wg.Wait()
		require.NoError(t, followErr)
	})

	_, err = writer.WriteString("before rotation\n")
	require.NoError(t, err)
	require.Equal(t, "before rotation\n", receiveFollowedLine(t, lines))

	require.NoError(t, os.Truncate(path, 0))
	_, err = writer.WriteString("after\n")
	require.NoError(t, err)
	require.Equal(t, "after\n", receiveFollowedLine(t, lines))
}
