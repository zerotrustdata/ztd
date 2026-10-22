// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestOpenReadOnlyOperationalEvidence_DoesNotCreateAbsentDatabase(t *testing.T) {
	dbPath := filepath.Join(testutil.TempDir(t), constants.TestReadOnlyDatabaseFilename)
	reader, err := OpenReadOnlyOperationalEvidence(dbPath, nil, testutil.NewTestLogger())
	require.Error(t, err)
	assert.Nil(t, reader)
	_, statErr := os.Stat(dbPath)
	assert.True(t, errors.Is(statErr, os.ErrNotExist))
}

func TestReadOnlyOperationalEvidence_SnapshotReadsBoundedReceiptAndCommitmentBodies(t *testing.T) {
	dbPath := filepath.Join(testutil.TempDir(t), constants.TestReadOnlyDatabaseFilename)
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)
	const receiptPlaintext = `{"transactionId":"tx-1"}`
	receiptBody, err := encryptAuditContent(testVault, auditColumnEventContentText, auditEventRowID(1), receiptPlaintext)
	require.NoError(t, err)
	contentDigest, err := computeEventContentDigest(testVault, &Event{ContentText: receiptPlaintext})
	require.NoError(t, err)
	attestationBody, err := encryptAuditContent(testVault, CommitmentColumnAttestationJSON, "commitment-1", `{"transactionId":"tx-1"}`)
	require.NoError(t, err)
	writer, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	_, err = writer.Exec(auditStoreSchema)
	require.NoError(t, err)
	_, err = writer.Exec(`INSERT INTO sessions (id) VALUES (?)`, "session-1")
	require.NoError(t, err)
	_, err = writer.Exec(`INSERT INTO events (operator_session_id, type, transaction_id, seq, prev_hash, hash, content_digest, timestamp, event_receipt_status, event_receipt_executed_at_ms, content_text) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "session-1", string(constants.EventOperatorReceiptRecorded), "tx-1", int64(1), auditChainGenesisPrevHash, "hash-1", contentDigest, "2023-11-14T22:13:21.000000Z", int64(3), int64(1_700_000_001_000), receiptBody)
	require.NoError(t, err)
	_, err = writer.Exec(`INSERT INTO commitment_ledger (transaction_id, transaction_hash, prior_commitment_hash, committed_at_unix_ms, hash, attestation_json) VALUES (?, ?, ?, ?, ?, ?)`, "tx-1", "hash-1", "", int64(1_700_000_002_000), "commitment-1", attestationBody)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	reader, err := OpenReadOnlyOperationalEvidence(dbPath, testVault, testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	_, err = reader.db.Exec(`CREATE TABLE should_not_exist (id INTEGER)`)
	require.Error(t, err)

	snapshot, err := reader.Snapshot(context.Background(), OperationalEvidenceQuery{
		WindowStart: time.UnixMilli(1_700_000_000_000).UTC(),
		WindowEnd:   time.UnixMilli(1_700_000_003_000).UTC(),
		MaxRows:     10,
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Receipts, 1)
	require.Len(t, snapshot.Commitments, 1)
	assert.Equal(t, "tx-1", snapshot.Receipts[0].TransactionID)
	assert.Equal(t, []byte(`{"transactionId":"tx-1"}`), snapshot.Receipts[0].Body)
	assert.Equal(t, "tx-1", snapshot.Commitments[0].TransactionID)
	assert.Equal(t, []byte(`{"transactionId":"tx-1"}`), snapshot.Commitments[0].Body)
}

func TestReadOnlyOperationalEvidence_SnapshotFailsInsteadOfTruncatingPopulation(t *testing.T) {
	dbPath := filepath.Join(testutil.TempDir(t), constants.TestReadOnlyDatabaseFilename)
	writer, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	_, err = writer.Exec(auditStoreSchema)
	require.NoError(t, err)
	_, err = writer.Exec(`INSERT INTO sessions (id) VALUES (?)`, "session-1")
	require.NoError(t, err)
	for seq, transactionID := range []string{"tx-1", "tx-2"} {
		_, err = writer.Exec(`INSERT INTO events (operator_session_id, type, transaction_id, seq, prev_hash, hash, event_receipt_status, event_receipt_executed_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "session-1", string(constants.EventOperatorReceiptRecorded), transactionID, int64(seq+1), auditChainGenesisPrevHash, "hash", int64(3), int64(1_700_000_001_000))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	reader, err := OpenReadOnlyOperationalEvidence(dbPath, nil, testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	_, err = reader.Snapshot(context.Background(), OperationalEvidenceQuery{
		WindowStart: time.UnixMilli(1_700_000_000_000).UTC(),
		WindowEnd:   time.UnixMilli(1_700_000_003_000).UTC(),
		MaxRows:     1,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactTooLarge)
}

func TestReadOnlyOperationalEvidence_LatestCommitmentAtReturnsNewestCommitTime(t *testing.T) {
	dbPath := filepath.Join(testutil.TempDir(t), constants.TestReadOnlyDatabaseFilename)
	writer, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	_, err = writer.Exec(auditStoreSchema)
	require.NoError(t, err)
	reader := func() *ReadOnlyOperationalEvidence {
		r, openErr := OpenReadOnlyOperationalEvidence(dbPath, nil, testutil.NewTestLogger())
		require.NoError(t, openErr)
		t.Cleanup(func() { require.NoError(t, r.Close()) })
		return r
	}

	_, err = reader().LatestCommitmentAt(context.Background())
	require.ErrorIs(t, err, constants.ErrNotFound, "an empty ledger must not yield an assessment window")

	for index, committedAtMs := range []int64{1_700_000_002_000, 1_700_000_009_500, 1_700_000_005_000} {
		_, err = writer.Exec(`INSERT INTO commitment_ledger (transaction_id, transaction_hash, prior_commitment_hash, committed_at_unix_ms, hash, attestation_json) VALUES (?, ?, ?, ?, ?, ?)`, "tx", "hash", "", committedAtMs, "commitment-"+string(rune('a'+index)), []byte(`{}`))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	latest, err := reader().LatestCommitmentAt(context.Background())
	require.NoError(t, err)
	assert.Equal(t, time.UnixMilli(1_700_000_009_500).UTC(), latest)
}
