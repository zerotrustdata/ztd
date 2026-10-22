// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

func runAuditPrune(t *testing.T, ass *SQLAuditStore) {
	t.Helper()
	require.NoError(t, auditStorePrune(ass.config)(context.Background(), ass.db, testutil.NewTestLogger()))
}

func setSessionEndedAt(t *testing.T, ass *SQLAuditStore, id string, endedAt time.Time) {
	t.Helper()
	_, err := ass.db.ExecWithRetry(context.Background(),
		`UPDATE sessions SET ended_at = ? WHERE id = ?`, timesvc.FormatTimestamp(endedAt), id)
	require.NoError(t, err)
}

func requireSessionRow(t *testing.T, ass *SQLAuditStore, id string, want bool) {
	t.Helper()
	session, err := ass.GetOperatorSession(id)
	require.NoError(t, err)
	if want {
		require.NotNil(t, session, "session %s must still exist", id)
		return
	}
	require.Nil(t, session, "session %s must be pruned", id)
}

// TestPrune_LiveIdleSessionSurvivesAndStillRecords pins H1: a session its runtime
// still owns has no events or receipts while idle, and the pruner must not remove it.
func TestPrune_LiveIdleSessionSurvivesAndStillRecords(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.StartSession("live-idle", constants.SessionTypeOperator, "Operator Session", "operator-1"))
	// created long ago: only liveness, never age, may decide whether it is pruned
	_, err := ass.db.ExecWithRetry(context.Background(),
		`UPDATE sessions SET created_at = '2000-01-01T00:00:00.000' WHERE id = ?`, "live-idle")
	require.NoError(t, err)

	runAuditPrune(t, ass)
	requireSessionRow(t, ass, "live-idle", true)

	_, err = ass.RecordEvent(commandEvent("live-idle", "after prune", time.Now().UTC()))
	require.NoError(t, err)
	require.NoError(t, ass.RecordEventReceipt(sampleEventReceiptRecord(t, "live-idle")))
}

func TestPrune_EndedEmptyExpiredSessionIsPruned(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.StartSession("ended-expired", constants.SessionTypeOperator, "Operator Session", "operator-1"))
	require.NoError(t, ass.EndSession("ended-expired"))
	setSessionEndedAt(t, ass, "ended-expired", time.Now().AddDate(0, 0, -(ass.config.RetentionDays+1)))

	runAuditPrune(t, ass)
	requireSessionRow(t, ass, "ended-expired", false)
}

func TestPrune_EndedSessionWithinRetentionOrWithEvidenceSurvives(t *testing.T) {
	ass := newIntegrationAuditStore(t)

	require.NoError(t, ass.StartSession("ended-recent", constants.SessionTypeOperator, "Operator Session", "operator-1"))
	require.NoError(t, ass.EndSession("ended-recent"))

	require.NoError(t, ass.StartSession("ended-with-events", constants.SessionTypeOperator, "Operator Session", "operator-1"))
	_, err := ass.RecordEvent(commandEvent("ended-with-events", "evidence", time.Now().UTC()))
	require.NoError(t, err)
	require.NoError(t, ass.EndSession("ended-with-events"))
	setSessionEndedAt(t, ass, "ended-with-events", time.Now().AddDate(0, 0, -(ass.config.RetentionDays+1)))

	runAuditPrune(t, ass)
	requireSessionRow(t, ass, "ended-recent", true)
	requireSessionRow(t, ass, "ended-with-events", true)
}

func TestEndSession_UnknownSessionFailsClosed(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.ErrorIs(t, ass.EndSession("never-started"), constants.ErrAuditSessionUnknown)
	require.ErrorIs(t, ass.EndSession(""), constants.ErrAuditSessionMissing)
}

// TestStartSession_RestartMarksEndedSessionLive pins that a restarted runtime reusing
// its session ID makes the ended row live again, so it cannot be pruned while owned.
func TestStartSession_RestartMarksEndedSessionLive(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.StartSession("restart", constants.SessionTypeOperator, "Operator Session", "operator-1"))
	require.NoError(t, ass.EndSession("restart"))
	setSessionEndedAt(t, ass, "restart", time.Now().AddDate(0, 0, -(ass.config.RetentionDays+1)))

	require.NoError(t, ass.StartSession("restart", constants.SessionTypeOperator, "Operator Session", "operator-1"))

	runAuditPrune(t, ass)
	requireSessionRow(t, ass, "restart", true)
}

func TestStartSession_ConflictingAttributesFail(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.StartSession("conflict", constants.SessionTypeOperator, "Operator Session", "operator-1"))
	require.ErrorIs(t,
		ass.StartSession("conflict", constants.SessionTypeOperator, "Operator Session", "operator-2"),
		constants.ErrAuditSessionConflict)
}

// TestStartSession_ConcurrentIdenticalStartsAllSucceed pins L13: concurrent identical
// starts do not lose to a PRIMARY KEY error.
func TestStartSession_ConcurrentIdenticalStartsAllSucceed(t *testing.T) {
	ass := newIntegrationAuditStore(t)

	const starters = 8
	errs := make([]error, starters)
	var wg sync.WaitGroup
	for i := 0; i < starters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = ass.StartSession("concurrent", constants.SessionTypeOperator, "Operator Session", "operator-1")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "starter %d", i)
	}
	requireSessionRow(t, ass, "concurrent", true)
}
