// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func commandEventTime() time.Time { return time.Now().UTC() }

// TestRecordEvent_ExecutionIDRoundTripsRawOutput verifies that events is the execution
// record: the full raw stdout and stderr (well past the old truncation threshold) are
// stored encrypted and read back byte-for-byte by execution_id.
func TestRecordEvent_ExecutionIDRoundTripsRawOutput(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("exec-sess", constants.SessionTypeOperator, "Exec", "user-exec"))

	stdout := strings.Repeat("A", 300*1024) + "TAIL-STDOUT"
	stderr := strings.Repeat("E", 150*1024) + "TAIL-STDERR"
	event := commandEvent("exec-sess", "raw output", commandEventTime())
	event.CommandStdout = stdout
	event.CommandStderr = stderr
	event.ExecutionID = "exec-raw-1"

	_, err := ass.RecordEvent(event)
	require.NoError(t, err)

	got, err := ass.GetEventByExecutionID("exec-raw-1")
	require.NoError(t, err)
	assert.Equal(t, "exec-raw-1", got.ExecutionID)
	assert.Equal(t, "exec-sess", got.OperatorSessionID)
	assert.Equal(t, stdout, got.CommandStdout)
	assert.Equal(t, stderr, got.CommandStderr)
	assert.Equal(t, "echo raw output", got.CommandRaw)
}

// TestGetEventByExecutionID_MissingIsTyped verifies that an unknown execution fails with
// the typed sentinel instead of an empty record.
func TestGetEventByExecutionID_MissingIsTyped(t *testing.T) {
	ass := newIntegrationAuditStore(t)

	_, err := ass.GetEventByExecutionID("exec-does-not-exist")
	require.ErrorIs(t, err, constants.ErrAuditExecutionNotFound)
}

// TestGetFileMutationByID_ResolvesSessionForDiff verifies that a file mutation resolves to its
// owning session and timestamp, the keys the ledger diff needs, with decrypted columns.
func TestGetFileMutationByID_ResolvesSessionForDiff(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("mut-sess", constants.SessionTypeOperator, "Mut", "user-mut"))

	eventID, err := ass.RecordEvent(commandEvent("mut-sess", "edit", commandEventTime()))
	require.NoError(t, err)
	require.NoError(t, ass.RecordFileMutation(&FileMutationLog{
		EventID:          eventID,
		Filepath:         "/work/mut.txt",
		Operation:        FileMutationWrite,
		LedgerHashBefore: "before-hash",
		LedgerHashAfter:  "after-hash",
		DiffStat:         "1 file changed",
	}))

	listed, err := ass.ListFileMutationsBySession("mut-sess", 10)
	require.NoError(t, err)
	require.Len(t, listed, 1)

	got, err := ass.GetFileMutationByID(listed[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "/work/mut.txt", got.Filepath)
	assert.Equal(t, "mut-sess", got.OperatorSessionID)
	assert.Equal(t, "before-hash", got.LedgerHashBefore)
	assert.Equal(t, "after-hash", got.LedgerHashAfter)
	assert.False(t, got.Timestamp.IsZero())

	_, err = ass.GetFileMutationByID(999999)
	require.ErrorIs(t, err, constants.ErrAuditFileMutationNotFound)
}

// TestFetchExecutionLogs_ReadsEventsRecord verifies FETCH_LOGS is served from events by
// execution_id, and that a diff without a ledger fails closed instead of returning empty.
func TestFetchExecutionLogs_ReadsEventsRecord(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("fetch-sess", constants.SessionTypeOperator, "Fetch", "user-fetch"))
	event := commandEvent("fetch-sess", "fetch", commandEventTime())
	event.CommandStdout = "out-line"
	event.CommandStderr = "err-line"
	event.CommandExitCode = 3
	event.ExecutionID = "exec-fetch-1"
	_, err := ass.RecordEvent(event)
	require.NoError(t, err)

	hh, err := NewHistoryHandler(ass, &mockLedger{}, testutil.NewTestLogger(), nil, "")
	require.NoError(t, err)
	logs, err := hh.FetchExecutionLogs("exec-fetch-1")
	require.NoError(t, err)
	assert.Equal(t, "out-line", logs.GetStdout())
	assert.Equal(t, "err-line", logs.GetStderr())
	assert.Equal(t, int32(3), logs.GetReturnCode())
	assert.Equal(t, int32(len("out-line")), logs.GetStdoutSize())

	_, err = hh.FetchExecutionLogs("exec-fetch-missing")
	require.ErrorIs(t, err, constants.ErrAuditExecutionNotFound)

	_, err = hh.FetchFileDiff(1)
	require.ErrorIs(t, err, constants.ErrAuditFileMutationNotFound)
}
