//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// rawColumn reads one stored column straight from SQLite, bypassing the store
// decrypt path, so the test sees exactly what is on disk.
func rawColumn(t *testing.T, ass *LocalFirstEventsStore, query string, args ...any) []byte {
	t.Helper()
	var raw []byte
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), query, args...).Scan(&raw))
	return raw
}

// TestContentColumnsAreEncryptedAtRest writes a distinctive marker into every
// content column of ztd.db and asserts the raw column never holds the marker.
func TestContentColumnsAreEncryptedAtRest(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	const marker = "PLAINTEXT-MARKER-7f3a"

	require.NoError(t, ass.CreateSession("enc-session", constants.SessionTypeOperator, "title "+marker, "identity "+marker))
	rawTitle := rawColumn(t, ass, `SELECT title FROM sessions WHERE id = ?`, "enc-session")
	require.NotContains(t, string(rawTitle), marker, "sessions.title")
	rawIdentity := rawColumn(t, ass, `SELECT user_identity FROM sessions WHERE id = ?`, "enc-session")
	require.NotContains(t, string(rawIdentity), marker, "sessions.user_identity")

	eventID, err := ass.RecordEvent(&Event{
		OperatorSessionID: "enc-session",
		Timestamp:         time.Now().UTC(),
		Type:              constants.EventType("COMMAND_EXECUTION"),
		ContentText:       "content " + marker,
		CommandRaw:        "echo " + marker,
		CommandStdout:     "stdout " + marker,
		CommandStderr:     "stderr " + marker,
	})
	require.NoError(t, err)
	for _, column := range []string{"content_text", "command_raw", "command_stdout", "command_stderr"} {
		raw := rawColumn(t, ass, `SELECT `+column+` FROM events WHERE id = ?`, eventID)
		require.NotContains(t, string(raw), marker, "events."+column)
	}

	require.NoError(t, ass.RecordFileMutation(&FileMutationLog{
		EventID:   eventID,
		Filepath:  "/secret/" + marker + ".txt",
		Operation: FileMutationWrite,
		DiffStat:  "1 file changed " + marker,
	}))
	rawPath := rawColumn(t, ass, `SELECT filepath FROM file_mutation_log WHERE event_id = ?`, eventID)
	require.NotContains(t, string(rawPath), marker, "file_mutation_log.filepath")
	rawDiff := rawColumn(t, ass, `SELECT diff_stat FROM file_mutation_log WHERE event_id = ?`, eventID)
	require.NotContains(t, string(rawDiff), marker, "file_mutation_log.diff_stat")

	record := sampleEventReceiptRecord(t, "enc-session")
	record.TargetResource = "/target/" + marker
	record.ResultSummary = "summary " + marker
	record.EventReceipt = &operatorv1.EventReceipt{TransactionId: record.TransactionID, TransactionHash: record.TransactionHash}
	require.NoError(t, ass.RecordEventReceipt(record))
	rawTarget := rawColumn(t, ass, `SELECT event_receipt_target_resource FROM events WHERE transaction_id = ? AND event_receipt_status IS NOT NULL`, record.TransactionID)
	require.NotContains(t, string(rawTarget), marker, "events.event_receipt_target_resource")
	rawSummary := rawColumn(t, ass, `SELECT event_receipt_result_summary FROM events WHERE transaction_id = ? AND event_receipt_status IS NOT NULL`, record.TransactionID)
	require.NotContains(t, string(rawSummary), marker, "events.event_receipt_result_summary")
	// content_text carries the canonical event receipt, including the transaction ID; its ciphertext must not.
	rawReceipt := rawColumn(t, ass, `SELECT content_text FROM events WHERE transaction_id = ? AND event_receipt_status IS NOT NULL`, record.TransactionID)
	require.NotContains(t, string(rawReceipt), record.TransactionID, "events.content_text")

	err = ass.CommitmentLedger().AppendCommitment(func(prior string) (*operatorv1.CommitmentAttestation, error) {
		return &operatorv1.CommitmentAttestation{
			TransactionId:       "tx-commit-enc",
			TransactionHash:     "hash-commit-enc",
			PriorCommitmentHash: prior,
			TargetResource:      "/commit/" + marker,
			Hash:                "commit-hash-enc",
		}, nil
	})
	require.NoError(t, err)
	rawCommitTarget := rawColumn(t, ass, `SELECT target_resource FROM commitment_ledger WHERE transaction_id = ?`, "tx-commit-enc")
	require.NotContains(t, string(rawCommitTarget), marker, "commitment_ledger.target_resource")
	rawAttestation := rawColumn(t, ass, `SELECT attestation_json FROM commitment_ledger WHERE transaction_id = ?`, "tx-commit-enc")
	require.NotContains(t, string(rawAttestation), marker, "commitment_ledger.attestation_json")
	require.NotContains(t, string(rawAttestation), "tx-commit-enc", "commitment_ledger.attestation_json")

	// The same rows read back decrypted through the store.
	events, err := ass.GetEvents(scopeForSession("enc-session"), 10, 0)
	require.NoError(t, err)
	var commandEvent *Event
	for _, event := range events {
		if event.ID == eventID {
			commandEvent = event
		}
	}
	require.NotNil(t, commandEvent)
	require.Equal(t, "echo "+marker, commandEvent.CommandRaw)
	require.Equal(t, "stdout "+marker, commandEvent.CommandStdout)
	got, err := ass.GetEventReceipt(record.TransactionID)
	require.NoError(t, err)
	require.Equal(t, "/target/"+marker, got.TargetResource)
	require.Equal(t, "summary "+marker, got.ResultSummary)
}

// TestLockedVaultFailsContentWrites asserts that every content write fails with
// the typed locked-vault sentinel and never falls back to plaintext.
func TestLockedVaultFailsContentWrites(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("locked-session", constants.SessionTypeOperator, "", ""))
	eventID, err := ass.RecordEvent(&Event{
		OperatorSessionID: "locked-session",
		Timestamp:         time.Now().UTC(),
		Type:              constants.EventType("COMMAND_EXECUTION"),
	})
	require.NoError(t, err)

	ass.encryptionVault.Lock()

	err = ass.CreateSession("locked-session-2", constants.SessionTypeOperator, "title", "identity")
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked, "sessions: %v", err)

	err = ass.RecordFileMutation(&FileMutationLog{
		EventID:   eventID,
		Filepath:  "/locked/file.txt",
		Operation: FileMutationWrite,
		DiffStat:  "1 file changed",
	})
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked, "file_mutation_log: %v", err)

	record := sampleEventReceiptRecord(t, "locked-session")
	record.TargetResource = "/locked/target"
	record.ResultSummary = "locked summary"
	err = ass.RecordEventReceipt(record)
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked, "receipts: %v", err)

	_, err = ass.RecordEvent(&Event{
		OperatorSessionID: "locked-session",
		Timestamp:         time.Now().UTC(),
		Type:              constants.EventType("COMMAND_EXECUTION"),
		CommandRaw:        "echo locked",
	})
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked, "events.command_raw: %v", err)

	err = ass.CommitmentLedger().AppendCommitment(func(prior string) (*operatorv1.CommitmentAttestation, error) {
		return &operatorv1.CommitmentAttestation{TransactionId: "tx-locked", PriorCommitmentHash: prior, TargetResource: "/locked"}, nil
	})
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked, "commitment_ledger: %v", err)
}

func scopeForSession(sessionID string) models.EventsScope {
	return models.EventsScope{OperatorSessionID: sessionID}
}
