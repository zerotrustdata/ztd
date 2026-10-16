//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// Receipt facts interleave with other chained events (command audit records, for
// example). The exported segment must carry those events too, or it has sequence
// gaps and cannot verify offline.
func TestReadOnlyOperationalEvidence_AuditChainSpansInterleavedEvents(t *testing.T) {
	ass, dbPath, testVault := newOperationalSnapshotAuditStore(t)
	now := time.Now().UTC()
	record := func(offset time.Duration, eventType constants.EventType, transactionID, content string) int64 {
		return recordOperationalChainEvent(t, ass, now.Add(offset), eventType, transactionID, content)
	}
	record(-3*time.Second, constants.EventOperatorAuditCommandRecorded, "", "before")
	firstSeq := record(-2*time.Second, constants.EventOperatorReceiptRecorded, "tx-a", `{"transactionId":"tx-a"}`)
	record(-time.Second, constants.EventOperatorAuditCommandRecorded, "", "between")
	lastSeq := record(0, constants.EventOperatorReceiptRecorded, "tx-b", `{"transactionId":"tx-b"}`)
	record(time.Second, constants.EventOperatorAuditCommandRecorded, "", "after")

	reader, err := OpenReadOnlyOperationalEvidence(dbPath, testVault, testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	snapshot, err := reader.Snapshot(t.Context(), OperationalEvidenceQuery{WindowStart: now.Add(-time.Minute), WindowEnd: now.Add(time.Minute), MaxRows: 10})
	require.NoError(t, err)

	require.Len(t, snapshot.AuditChain, 3)
	segment := make([]AuditChainSegmentEntry, 0, len(snapshot.AuditChain))
	for index, entry := range snapshot.AuditChain {
		assert.Equal(t, firstSeq+int64(index), entry.Seq)
		if entry.EventType == string(constants.EventOperatorReceiptRecorded) {
			assert.NotEmpty(t, entry.ContentSHA256, "a receipt fact carries the hash its exported receipt is matched against")
		} else {
			assert.Empty(t, entry.ContentSHA256, "non-receipt content is never exported, so neither is its hash")
		}
		segment = append(segment, AuditChainSegmentEntry{
			Seq:               entry.Seq,
			PrevHash:          entry.PrevHash,
			Hash:              entry.Hash,
			EventType:         entry.EventType,
			OperatorSessionID: entry.OperatorSessionID,
			Timestamp:         timesvc.FormatTimestamp(entry.Timestamp),
			ContentDigest:     entry.ContentDigest,
			ContentSHA256:     entry.ContentSHA256,
			TransactionID:     entry.TransactionID,
		})
	}
	assert.Equal(t, lastSeq, snapshot.AuditChain[2].Seq)
	assert.Equal(t, string(constants.EventOperatorAuditCommandRecorded), snapshot.AuditChain[1].EventType)
	require.NoError(t, VerifyAuditChainSegment(segment, segment[0].PrevHash, segment[2].Hash))
}

// The release window ends at a millisecond commit time while chain timestamps carry
// microseconds. A fact later in that same millisecond is after the window end.
func TestReadOnlyOperationalEvidence_AuditChainWindowEndIsExact(t *testing.T) {
	ass, dbPath, testVault := newOperationalSnapshotAuditStore(t)
	windowEnd := time.Now().UTC().Truncate(time.Millisecond)
	insideSeq := recordOperationalChainEvent(t, ass, windowEnd.Add(-time.Millisecond), constants.EventOperatorReceiptRecorded, "tx-inside", `{"transactionId":"tx-inside"}`)
	recordOperationalChainEvent(t, ass, windowEnd.Add(389*time.Microsecond), constants.EventOperatorReceiptRecorded, "tx-after", `{"transactionId":"tx-after"}`)

	reader, err := OpenReadOnlyOperationalEvidence(dbPath, testVault, testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	snapshot, err := reader.Snapshot(t.Context(), OperationalEvidenceQuery{WindowStart: windowEnd.Add(-time.Minute), WindowEnd: windowEnd, MaxRows: 10})
	require.NoError(t, err)
	require.Len(t, snapshot.AuditChain, 1)
	assert.Equal(t, insideSeq, snapshot.AuditChain[0].Seq)
}

func newOperationalSnapshotAuditStore(t *testing.T) (*SQLAuditStore, string, *vault.Vault) {
	t.Helper()
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)
	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	config := DefaultAuditStoreConfig()
	config.EncryptionVault = testVault
	ass, err := NewSQLAuditStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ass.Close()) })
	require.NoError(t, ass.CreateSession("chain-session", constants.SessionTypeOperator, "", ""))
	return ass, dbPath, testVault
}

func recordOperationalChainEvent(t *testing.T, ass *SQLAuditStore, at time.Time, eventType constants.EventType, transactionID, content string) int64 {
	t.Helper()
	_, seq, _, err := ass.RecordEventChained(&Event{
		OperatorSessionID: "chain-session",
		TransactionID:     transactionID,
		Timestamp:         at,
		Type:              eventType,
		ContentText:       content,
		CommandExitCode:   constants.ExitCodeNone,
	})
	require.NoError(t, err)
	return seq
}
