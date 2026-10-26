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
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newTestEventsStoreWithAuditor(t *testing.T) *LocalFirstEventsStore {
	t.Helper()

	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, vaultPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, vaultPriv)

	auditorPub, auditorPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	config := DefaultLocalFirstEventsStoreConfig()
	config.EncryptionVault = testVault
	config.CheckpointSigner = NewEd25519CheckpointSigner(auditorPriv, hex.EncodeToString(auditorPub))
	config.PinnedAuditorPublicKey = auditorPub

	ass, err := NewLocalFirstEventsStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ass.Close()) })
	return ass
}

func TestAuditChainCheckpoint_SignedPruneAndVerify(t *testing.T) {
	ass := newTestEventsStoreWithAuditor(t)
	require.NoError(t, ass.CreateSession("checkpoint-session", constants.SessionTypeOperator, "Checkpoint", "user-cp"))

	oldTimestamp := time.Now().UTC().AddDate(0, 0, -120)
	for i := range 3 {
		_, err := ass.RecordEvent(&Event{
			OperatorSessionID: "checkpoint-session",
			Timestamp:         oldTimestamp.Add(time.Duration(i) * time.Second),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "prunable command event",
			CommandExitCode:   constants.ExitCodeNone,
		})
		require.NoError(t, err)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff))

	var (
		count         int
		signature     string
		auditorKeyID  string
		formatVersion string
	)
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `
		SELECT COUNT(*), signature, auditor_key_id, format_version FROM audit_chain_checkpoints
	`).Scan(&count, &signature, &auditorKeyID, &formatVersion))
	assert.Equal(t, 1, count)
	assert.NotEmpty(t, signature, "checkpoint must be signed")
	assert.NotEmpty(t, auditorKeyID, "checkpoint must carry auditor key ID")
	assert.Equal(t, EventsChainCheckpointVersion, formatVersion)

	require.NoError(t, ass.VerifyChain(context.Background(), 0))
}

func TestAuditChainCheckpoint_UnsignedFailsVerification(t *testing.T) {
	ass := newTestEventsStoreWithAuditor(t)
	require.NoError(t, ass.CreateSession("unsigned-session", constants.SessionTypeOperator, "Unsigned", "user-unsigned"))

	oldTimestamp := time.Now().UTC().AddDate(0, 0, -120)
	_, err := ass.RecordEvent(&Event{
		OperatorSessionID: "unsigned-session",
		Timestamp:         oldTimestamp,
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "prunable command event",
	})
	require.NoError(t, err)

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff))

	// Strip the signature
	_, err = ass.db.ExecWithRetry(t.Context(), `UPDATE audit_chain_checkpoints SET signature = ''`)
	require.NoError(t, err)

	err = ass.VerifyChain(context.Background(), 0)
	require.ErrorIs(t, err, constants.ErrAuditCheckpointUnsigned)
}

func TestAuditChainCheckpoint_UntrustedSignerFailsVerification(t *testing.T) {
	ass := newTestEventsStoreWithAuditor(t)
	require.NoError(t, ass.CreateSession("untrusted-session", constants.SessionTypeOperator, "Untrusted", "user-untrusted"))

	oldTimestamp := time.Now().UTC().AddDate(0, 0, -120)
	_, err := ass.RecordEvent(&Event{
		OperatorSessionID: "untrusted-session",
		Timestamp:         oldTimestamp,
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "prunable command event",
	})
	require.NoError(t, err)

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff))

	// Sign with a foreign key
	foreignPub, foreignPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	foreignSigner := NewEd25519CheckpointSigner(foreignPriv, hex.EncodeToString(foreignPub))

	var (
		seq         int64
		hash        string
		prunedCount int64
		cpHash      string
		createdMs   int64
		version     string
	)
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `
		SELECT pruned_through_seq, pruned_through_hash, pruned_count, checkpoint_hash, created_at_unix_ms, format_version
		FROM audit_chain_checkpoints LIMIT 1
	`).Scan(&seq, &hash, &prunedCount, &cpHash, &createdMs, &version))

	canonical, err := CanonicalizeCheckpoint(seq, hash, prunedCount, cpHash, createdMs, foreignSigner.KeyID(), version)
	require.NoError(t, err)
	sig, err := foreignSigner.Sign(canonical)
	require.NoError(t, err)

	_, err = ass.db.ExecWithRetry(t.Context(), `
		UPDATE audit_chain_checkpoints SET auditor_key_id = ?, signature = ?
	`, foreignSigner.KeyID(), hex.EncodeToString(sig))
	require.NoError(t, err)

	err = ass.VerifyChain(context.Background(), 0)
	require.ErrorIs(t, err, constants.ErrAuditCheckpointUntrustedSigner)
}

func TestAuditChainCheckpoint_InvalidSignatureFailsVerification(t *testing.T) {
	ass := newTestEventsStoreWithAuditor(t)
	require.NoError(t, ass.CreateSession("invalid-sig-session", constants.SessionTypeOperator, "InvalidSig", "user-sig"))

	oldTimestamp := time.Now().UTC().AddDate(0, 0, -120)
	_, err := ass.RecordEvent(&Event{
		OperatorSessionID: "invalid-sig-session",
		Timestamp:         oldTimestamp,
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "prunable command event",
	})
	require.NoError(t, err)

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff))

	// Corrupt signature
	_, err = ass.db.ExecWithRetry(t.Context(), `UPDATE audit_chain_checkpoints SET signature = 'deadbeefdeadbeef'`)
	require.NoError(t, err)

	err = ass.VerifyChain(context.Background(), 0)
	require.ErrorIs(t, err, constants.ErrAuditCheckpointSignatureInvalid)
}

func TestAuditChainCheckpoint_TamperedAnchorRowFailsVerification(t *testing.T) {
	ass := newTestEventsStoreWithAuditor(t)
	require.NoError(t, ass.CreateSession("tamper-anchor-session", constants.SessionTypeOperator, "TamperAnchor", "user-anchor"))

	oldTimestamp := time.Now().UTC().AddDate(0, 0, -120)
	_, err := ass.RecordEvent(&Event{
		OperatorSessionID: "tamper-anchor-session",
		Timestamp:         oldTimestamp,
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "prunable command event",
	})
	require.NoError(t, err)

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff))

	// Tamper pruned_through_hash in audit_chain_checkpoints
	_, err = ass.db.ExecWithRetry(t.Context(), `UPDATE audit_chain_checkpoints SET pruned_through_hash = 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'`)
	require.NoError(t, err)

	err = ass.VerifyChain(context.Background(), 0)
	require.ErrorIs(t, err, constants.ErrAuditCheckpointSignatureInvalid)
}

func TestAuditChainCheckpoint_EventMissingFailsVerification(t *testing.T) {
	ass := newTestEventsStoreWithAuditor(t)
	require.NoError(t, ass.CreateSession("event-missing-session", constants.SessionTypeOperator, "MissingEvent", "user-miss"))

	oldTimestamp := time.Now().UTC().AddDate(0, 0, -120)
	_, err := ass.RecordEvent(&Event{
		OperatorSessionID: "event-missing-session",
		Timestamp:         oldTimestamp,
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "prunable command event",
	})
	require.NoError(t, err)

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff))

	// Delete the checkpointed event from events
	_, err = ass.db.ExecWithRetry(t.Context(), `DELETE FROM events WHERE type = ?`, string(constants.EventPlatformAuditChainCheckpointed))
	require.NoError(t, err)

	err = ass.VerifyChain(context.Background(), 0)
	require.ErrorIs(t, err, constants.ErrAuditCheckpointEventMissing)
}

func TestAuditChainCheckpoint_PayloadMismatchFailsVerification(t *testing.T) {
	ass := newTestEventsStoreWithAuditor(t)
	require.NoError(t, ass.CreateSession("payload-mismatch-session", constants.SessionTypeOperator, "Mismatch", "user-mismatch"))

	oldTimestamp := time.Now().UTC().AddDate(0, 0, -120)
	_, err := ass.RecordEvent(&Event{
		OperatorSessionID: "payload-mismatch-session",
		Timestamp:         oldTimestamp,
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "prunable command event",
	})
	require.NoError(t, err)

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff))

	// Replace the checkpoint event's payload with mismatched sequence
	var (
		seq int64
		id  int64
	)
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `
		SELECT id, seq FROM events WHERE type = ?
	`, string(constants.EventPlatformAuditChainCheckpointed)).Scan(&id, &seq))

	badPayload := &operatorv1.AuditChainCheckpointPayload{
		PrunedThroughSeq:  9999, // Mismatched!
		PrunedThroughHash: "bad-hash",
		EventsPrunedCount: 1,
		CreatedAtUnixMs:   time.Now().UnixMilli(),
		FormatVersion:     EventsChainCheckpointVersion,
	}
	badJSON, err := protojson.Marshal(badPayload)
	require.NoError(t, err)

	encrypted, err := ass.encryptColumn("events.content_text", eventRowID(seq), string(badJSON))
	require.NoError(t, err)

	_, err = ass.db.ExecWithRetry(t.Context(), `UPDATE events SET content_text = ? WHERE id = ?`, encrypted, id)
	require.NoError(t, err)

	err = ass.VerifyChain(context.Background(), 0)
	require.ErrorIs(t, err, constants.ErrAuditCheckpointPayloadMismatch)
}

func TestAuditChainCheckpoint_MultiPruneNoOrphans(t *testing.T) {
	ass := newTestEventsStoreWithAuditor(t)
	require.NoError(t, ass.CreateSession("multi-prune-session", constants.SessionTypeOperator, "MultiPrune", "user-mp"))

	// 1. Add 3 old events and prune
	t1 := time.Now().UTC().AddDate(0, 0, -200)
	for i := range 3 {
		_, err := ass.RecordEvent(&Event{
			OperatorSessionID: "multi-prune-session",
			Timestamp:         t1.Add(time.Duration(i) * time.Second),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "batch 1",
		})
		require.NoError(t, err)
	}
	cutoff1 := time.Now().UTC().AddDate(0, 0, -150).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff1))

	var firstPruneThrough int64
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `
		SELECT pruned_through_seq FROM audit_chain_checkpoints ORDER BY pruned_through_seq DESC LIMIT 1
	`).Scan(&firstPruneThrough))
	assert.Equal(t, int64(3), firstPruneThrough)

	// 2. Add 3 more events older than cutoff 2
	t2 := time.Now().UTC().AddDate(0, 0, -100)
	for i := range 3 {
		_, err := ass.RecordEvent(&Event{
			OperatorSessionID: "multi-prune-session",
			Timestamp:         t2.Add(time.Duration(i) * time.Second),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "batch 2",
		})
		require.NoError(t, err)
	}

	// 3. Add 2 recent events
	for i := range 2 {
		_, err := ass.RecordEvent(&Event{
			OperatorSessionID: "multi-prune-session",
			Timestamp:         time.Now().UTC().Add(time.Duration(i) * time.Second),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "recent",
		})
		require.NoError(t, err)
	}

	cutoff2 := time.Now().UTC().AddDate(0, 0, -50).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, ass.checkpointSigner, cutoff2))

	var latestPrunedThrough int64
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `
		SELECT pruned_through_seq FROM audit_chain_checkpoints ORDER BY pruned_through_seq DESC LIMIT 1
	`).Scan(&latestPrunedThrough))

	// Assert NO events exist with seq <= latestPrunedThrough (no orphaned old checkpointed events!)
	var orphans int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `
		SELECT COUNT(*) FROM events WHERE seq <= ?
	`, latestPrunedThrough).Scan(&orphans))
	assert.Zero(t, orphans, "no orphaned events may exist at or below pruned_through_seq")

	// Verification of surviving chain must pass
	require.NoError(t, ass.VerifyChain(context.Background(), 0))
}

func TestAuditChainCheckpoint_LegacySchemaRejected(t *testing.T) {
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, vaultPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, vaultPriv)

	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	defer db.Close()

	// Create legacy audit_chain_checkpoints table missing signature columns
	_, err = db.Exec(`
		CREATE TABLE audit_chain_checkpoints (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pruned_through_seq INTEGER NOT NULL,
			pruned_through_hash TEXT NOT NULL,
			pruned_count INTEGER NOT NULL,
			checkpoint_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
	`)
	require.NoError(t, err)

	config := DefaultLocalFirstEventsStoreConfig()
	config.EncryptionVault = testVault

	// Initializing store over legacy schema must fail closed
	_, err = NewLocalFirstEventsStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.ErrorIs(t, err, constants.ErrAuditCheckpointSchemaIncompatible)
}
