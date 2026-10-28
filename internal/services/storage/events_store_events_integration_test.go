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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// countEventsForSession counts raw events rows for one session, bypassing the store read path.
func countEventsForSession(t *testing.T, ass *LocalFirstEventsStore, sessionID string) int {
	t.Helper()
	var n int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM events WHERE operator_session_id = ?`, sessionID).Scan(&n))
	return n
}

func commandEvent(sessionID, content string, ts time.Time) *Event {
	return &Event{
		OperatorSessionID: sessionID,
		Timestamp:         ts,
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       content,
		CommandRaw:        "echo " + content,
		CommandExitCode:   0,
		CommandStdout:     content,
	}
}

// TestRecordEvents_UnknownSessionRollsBackWholeBatch verifies that a batch containing
// one event for a session that does not exist fails with the typed session error and
// leaves no partial rows behind, including for the valid event that preceded it.
func TestRecordEvents_UnknownSessionRollsBackWholeBatch(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("batch-known", constants.SessionTypeOperator, "Batch", "user-batch"))

	before := countEventsForSession(t, ass, "batch-known")
	totalBefore := rawEventCount(t, ass)

	err := ass.RecordEvents([]*Event{
		commandEvent("batch-known", "first-valid", time.Now().UTC()),
		commandEvent("batch-missing", "second-unknown", time.Now().UTC()),
		commandEvent("batch-known", "third-valid", time.Now().UTC()),
	})
	require.ErrorIs(t, err, constants.ErrEventsStoreSessionUnknown)

	assert.Equal(t, before, countEventsForSession(t, ass, "batch-known"), "valid events before the failing one must roll back")
	assert.Equal(t, totalBefore, rawEventCount(t, ass), "no rows may persist from a failed batch")
	assert.Equal(t, 0, countEventsForSession(t, ass, "batch-missing"))
}

// TestRecordEvents_EmptySessionReturnsTypedErrorAndRollsBack verifies that a batch
// event without an operator session fails with ErrEventsStoreSessionMissing and writes nothing.
func TestRecordEvents_EmptySessionReturnsTypedErrorAndRollsBack(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("batch-empty-known", constants.SessionTypeOperator, "Batch", "user-batch"))

	totalBefore := rawEventCount(t, ass)
	err := ass.RecordEvents([]*Event{
		commandEvent("batch-empty-known", "valid", time.Now().UTC()),
		commandEvent("", "no-session", time.Now().UTC()),
	})
	require.ErrorIs(t, err, constants.ErrEventsStoreSessionMissing)
	assert.Equal(t, totalBefore, rawEventCount(t, ass))
}

// TestRecordEvent_EmptySessionReturnsSessionMissing verifies the single-event path
// rejects an event with no operator session using the typed sentinel and no event id.
func TestRecordEvent_EmptySessionReturnsSessionMissing(t *testing.T) {
	ass := newIntegrationEventsStore(t)

	totalBefore := rawEventCount(t, ass)
	eventID, err := ass.RecordEvent(commandEvent("", "orphan", time.Now().UTC()))
	require.ErrorIs(t, err, constants.ErrEventsStoreSessionMissing)
	assert.Zero(t, eventID)
	assert.Equal(t, totalBefore, rawEventCount(t, ass))
}

// TestRecordEvent_UnknownSessionIsRejected pins the single-event contract: RecordEvent
// never creates a session. An event naming a session its owner never started fails with
// ErrEventsStoreSessionUnknown, persists nothing, and leaves no session row behind, exactly
// like the batch path (see TestRecordEvents_UnknownSessionRollsBackWholeBatch).
func TestRecordEvent_UnknownSessionIsRejected(t *testing.T) {
	ass := newIntegrationEventsStore(t)

	totalBefore := rawEventCount(t, ass)
	eventID, err := ass.RecordEvent(commandEvent("single-unknown-session", "direct", time.Now().UTC()))
	require.ErrorIs(t, err, constants.ErrEventsStoreSessionUnknown)
	assert.Zero(t, eventID)
	assert.Equal(t, totalBefore, rawEventCount(t, ass))

	session, err := ass.GetOperatorSession("single-unknown-session")
	require.NoError(t, err)
	assert.Nil(t, session, "a rejected event must not create its session")

	_, _, _, err = ass.RecordEventChained(commandEvent("single-unknown-session", "chained", time.Now().UTC()))
	require.ErrorIs(t, err, constants.ErrEventsStoreSessionUnknown)
}

// TestListEvents_SessionFilterIsolatesSessions verifies that a session filter returns only
// that session's events and that an empty session ID returns events across all sessions.
func TestListEvents_SessionFilterIsolatesSessions(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("iso-a", constants.SessionTypeOperator, "A", "user-a"))
	require.NoError(t, ass.CreateSession("iso-b", constants.SessionTypeOperator, "B", "user-b"))

	now := time.Now().UTC()
	_, err := ass.RecordEvent(commandEvent("iso-a", "a-one", now))
	require.NoError(t, err)
	_, err = ass.RecordEvent(commandEvent("iso-a", "a-two", now.Add(time.Second)))
	require.NoError(t, err)
	_, err = ass.RecordEvent(commandEvent("iso-b", "b-one", now.Add(2*time.Second)))
	require.NoError(t, err)

	onlyA, err := ass.ListEvents("iso-a", 100, 0)
	require.NoError(t, err)
	require.Len(t, onlyA, 2)
	for _, event := range onlyA {
		assert.Equal(t, "iso-a", event.OperatorSessionID)
	}

	onlyAScoped, err := ass.GetEvents(models.EventsScope{OperatorSessionID: "iso-a"}, 100, 0)
	require.NoError(t, err)
	assert.Len(t, onlyAScoped, 2)

	all, err := ass.ListEvents("", 100, 0)
	require.NoError(t, err)
	sessionsSeen := map[string]bool{}
	for _, event := range all {
		sessionsSeen[event.OperatorSessionID] = true
	}
	assert.True(t, sessionsSeen["iso-a"])
	assert.True(t, sessionsSeen["iso-b"])
}

// TestListEvents_PaginationWalksEveryEventOnce verifies limit/offset paging over ListEvents
// returns every event exactly once, in ascending timestamp order, and that a short last page
// is returned without error.
func TestListEvents_PaginationWalksEveryEventOnce(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("page-session", constants.SessionTypeOperator, "Page", "user-page"))

	base := time.Now().UTC().Add(-time.Hour)
	const total = 5
	written := map[int64]bool{}
	for i := range total {
		id, err := ass.RecordEvent(commandEvent("page-session", "page", base.Add(time.Duration(i)*time.Second)))
		require.NoError(t, err)
		written[id] = true
	}

	var seen []*Event
	for offset := 0; ; offset += 2 {
		page, err := ass.ListEvents("page-session", 2, offset)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		assert.LessOrEqual(t, len(page), 2)
		seen = append(seen, page...)
	}

	require.Len(t, seen, total)
	for i, event := range seen {
		assert.True(t, written[event.ID], "event %d was not written by this test", event.ID)
		if i > 0 {
			assert.False(t, event.Timestamp.Before(seen[i-1].Timestamp), "pages must be ordered by timestamp")
		}
	}
	ids := map[int64]bool{}
	for _, event := range seen {
		assert.False(t, ids[event.ID], "event %d returned on more than one page", event.ID)
		ids[event.ID] = true
	}

	beyond, err := ass.ListEvents("page-session", 2, total)
	require.NoError(t, err)
	assert.Empty(t, beyond)
}

// TestListEvents_LockedVaultFailsRead verifies that once the vault is locked, event reads fail
// closed with ErrLocalFirstEventsStoreVaultLocked rather than returning ciphertext or plaintext.
func TestListEvents_LockedVaultFailsRead(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("locked-read", constants.SessionTypeOperator, "Locked", "user-locked"))
	_, err := ass.RecordEvent(commandEvent("locked-read", "secret-output", time.Now().UTC()))
	require.NoError(t, err)

	vault := ass.GetEncryptionVault()
	require.NotNil(t, vault)
	vault.Lock()

	events, err := ass.ListEvents("locked-read", 10, 0)
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked)
	assert.Nil(t, events)

	scoped, err := ass.GetEvents(models.EventsScope{OperatorSessionID: "locked-read"}, 10, 0)
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked)
	assert.Nil(t, scoped)
}

// TestPruneChainedEvents_RemovesOnlyExpiredEventsAndChainVerifies verifies that pruning
// deletes events older than the cutoff, keeps recent events, writes a checkpoint, and leaves
// the remaining chain verifiable.
func TestPruneChainedEvents_RemovesOnlyExpiredEventsAndChainVerifies(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("prune-partial", constants.SessionTypeOperator, "Prune", "user-prune"))

	old := time.Now().UTC().AddDate(0, 0, -120)
	for i := range 3 {
		_, err := ass.RecordEvent(commandEvent("prune-partial", "expired", old.Add(time.Duration(i)*time.Second)))
		require.NoError(t, err)
	}

	recentIDs := []int64{}
	for i := range 2 {
		id, err := ass.RecordEvent(commandEvent("prune-partial", "recent", time.Now().UTC().Add(time.Duration(i)*time.Second)))
		require.NoError(t, err)
		recentIDs = append(recentIDs, id)
	}

	cutoffTime := time.Now().UTC().AddDate(0, 0, -30)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoffTime.Format(time.RFC3339Nano)))

	var expired int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(),
		`SELECT COUNT(*) FROM events WHERE timestamp < ? AND type != ?`,
		timesvc.FormatTimestamp(cutoffTime), string(constants.EventPlatformAuditChainCheckpointed)).Scan(&expired))
	assert.Zero(t, expired, "events older than the cutoff must be pruned")

	for _, id := range recentIDs {
		var n int
		require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM events WHERE id = ?`, id).Scan(&n))
		assert.Equal(t, 1, n, "recent event %d must survive pruning", id)
	}

	var checkpoints int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM audit_chain_checkpoints`).Scan(&checkpoints))
	assert.Positive(t, checkpoints)

	require.NoError(t, ass.VerifyChain(context.Background(), 0))

}

// rawEventCount counts every events row, bypassing the store read path.
func rawEventCount(t *testing.T, ass *LocalFirstEventsStore) int {
	t.Helper()
	var n int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM events`).Scan(&n))
	return n
}

// TestRecordEventOnce_IsIdempotentPerTransactionAndType verifies the idempotency key of
// facts that must be recorded once per transaction (transaction.expired): a repeat with
// the same transaction_id and type writes nothing, while a different type or a different
// transaction still records.
func TestRecordEventOnce_IsIdempotentPerTransactionAndType(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("once-session", constants.SessionTypeOperator, "Once", "user-once"))

	expired := func(txID string) *Event {
		return &Event{
			TransactionID:     txID,
			OperatorSessionID: "once-session",
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.Notary.TransactionExpired,
			ContentText:       "expired " + txID,
		}
	}

	before := rawEventCount(t, ass)
	id, recorded, err := ass.RecordEventOnce(expired("tx-1"))
	require.NoError(t, err)
	assert.True(t, recorded)
	assert.NotZero(t, id)

	_, recorded, err = ass.RecordEventOnce(expired("tx-1"))
	require.NoError(t, err)
	assert.False(t, recorded, "the same transaction must not get a second expiry event")
	assert.Equal(t, before+1, rawEventCount(t, ass))

	_, recorded, err = ass.RecordEventOnce(expired("tx-2"))
	require.NoError(t, err)
	assert.True(t, recorded, "another transaction records its own expiry")

	other := expired("tx-1")
	other.Type = constants.Event.Operator.Notary.ApprovalRequested
	_, recorded, err = ass.RecordEventOnce(other)
	require.NoError(t, err)
	assert.True(t, recorded, "a different event type for the same transaction is a different fact")

	require.NoError(t, ass.VerifyChain(t.Context(), 0))

	_, _, err = ass.RecordEventOnce(&Event{OperatorSessionID: "once-session", Type: constants.Event.Operator.Notary.TransactionExpired})
	require.ErrorIs(t, err, constants.ErrMissingRequiredField, "an idempotency key is required")
}
