// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package reporting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestReceiptRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns returns expected headers", func(t *testing.T) {
		t.Parallel()

		cols := ReceiptRow{}.Columns()
		assert.Contains(t, cols, "transaction_id")
		assert.Contains(t, cols, "transaction_hash")
		assert.Contains(t, cols, "action_type")
		assert.Contains(t, cols, "status")
		assert.Contains(t, cols, "executed_at_utc")
		assert.Len(t, cols, 11)
	})

	t.Run("Record returns values in column order", func(t *testing.T) {
		t.Parallel()

		r := ReceiptRow{
			TransactionID:   "tx-001",
			TransactionHash: "hash123",
			OperatorID:      "op-1",
			SessionID:       "sess-1",
			ActionType:      "FS_READ",
			Status:          "OK",
			StateRootBefore: "root-a",
			StateRootAfter:  "root-b",
			SignerKeyID:     "key-1",
			Signature:       "sig-1",
			ExecutedAtUTC:   "2026-01-01T00:00:00Z",
		}
		rec := r.Record()
		require.Len(t, rec, 11)
		assert.Equal(t, "tx-001", rec[0])
		assert.Equal(t, "hash123", rec[1])
		assert.Equal(t, "FS_READ", rec[4])
		assert.Equal(t, "2026-01-01T00:00:00Z", rec[10])
	})
}

func TestSessionRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := SessionRow{
			ID:           "sess-1",
			SessionType:  "operator",
			CreatedAtUTC: "2026-01-01T00:00:00Z",
		}
		cols := SessionRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 3)
		require.Len(t, rec, 3)
		assert.Equal(t, "sess-1", rec[0])
	})
}

func TestEventRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns returns expected headers", func(t *testing.T) {
		t.Parallel()

		cols := EventRow{}.Columns()
		assert.Contains(t, cols, "id")
		assert.Contains(t, cols, "type")
		assert.Contains(t, cols, "exit_code")
		assert.Len(t, cols, 7)
	})

	t.Run("Record with non-None exit code", func(t *testing.T) {
		t.Parallel()

		r := EventRow{
			ID:           42,
			SessionID:    "sess-1",
			TimestampUTC: "2026-01-01T00:00:00Z",
			Type:         "COMMAND_EXECUTION",
			ExitCode:     0,
			DurationMs:   150,
		}
		rec := r.Record()
		require.Len(t, rec, 7)
		assert.Equal(t, "42", rec[0])
		assert.Equal(t, "0", rec[4])
		assert.Equal(t, "150", rec[5])
	})

	t.Run("Record with ExitCodeNone produces empty string", func(t *testing.T) {
		t.Parallel()

		r := EventRow{
			ID:       1,
			ExitCode: constants.ExitCodeNone,
		}
		rec := r.Record()
		assert.Empty(t, rec[4])
	})
}

func TestFileMutationRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := FileMutationRow{
			ID:               1,
			EventID:          10,
			Operation:        "WRITE",
			LedgerHashBefore: "hash-before",
			LedgerHashAfter:  "hash-after",
		}
		cols := FileMutationRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 5)
		require.Len(t, rec, 5)
		assert.Equal(t, "1", rec[0])
		assert.Equal(t, "10", rec[1])
		assert.Equal(t, "WRITE", rec[2])
	})
}

func TestCommitmentRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := CommitmentRow{
			Seq:                 1,
			CommittedAtUTC:      "2026-01-01T00:00:00Z",
			TransactionID:       "tx-1",
			TransactionHash:     "hash-1",
			PriorCommitmentHash: "prior-1",
			Hash:                "commit-hash-1",
			StateRootAtCommit:   "root-1",
			ActionType:          "FS_WRITE",
			AuditorKeyID:        "key-1",
			Signature:           "sig-1",
		}
		cols := CommitmentRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 10)
		require.Len(t, rec, 10)
		assert.Equal(t, "1", rec[0])
		assert.Equal(t, "tx-1", rec[2])
	})
}

func TestLedgerCommitRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := LedgerCommitRow{
			CommitHash:   "abc123",
			ParentHash:   "def456",
			TimestampUTC: "2026-01-01T00:00:00Z",
			FilesChanged: "2",
		}
		cols := LedgerCommitRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 4)
		require.Len(t, rec, 4)
		assert.Equal(t, []string{"commit_hash", "parent_hash", "timestamp_utc", "files_changed"}, cols)
		assert.Equal(t, "abc123", rec[0])
		assert.Equal(t, "2", rec[3])
	})
}

func TestLedgerMerkleRootRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := LedgerMerkleRootRow{
			MerkleRoot:    "root-hash",
			CapturedAtUTC: "2026-01-01T00:00:00Z",
		}
		cols := LedgerMerkleRootRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 2)
		require.Len(t, rec, 2)
		assert.Equal(t, "root-hash", rec[0])
	})
}

func TestReplayNonceRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := ReplayNonceRow{
			Nonce:         "nonce-123",
			Status:        "used",
			ReservedAtUTC: "2026-01-01T00:00:00Z",
			UsedAtUTC:     "2026-01-01T00:01:00Z",
			ExpiresAtUTC:  "2026-01-01T00:05:00Z",
		}
		cols := ReplayNonceRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 5)
		require.Len(t, rec, 5)
		assert.Equal(t, "nonce-123", rec[0])
		assert.Equal(t, "used", rec[1])
	})
}

func TestSuspendedTxRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := SuspendedTxRow{
			TxHash:       "tx-hash",
			UserID:       "user-1",
			ActionType:   "FS_WRITE",
			Status:       "pending",
			CreatedAtUTC: "2026-01-01T00:00:00Z",
			ExpiresAtUTC: "2026-01-01T01:00:00Z",
		}
		cols := SuspendedTxRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 6)
		require.Len(t, rec, 6)
		assert.Equal(t, "tx-hash", rec[0])
		assert.Equal(t, "pending", rec[3])
	})
}

func TestVerificationRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := VerificationRow{
			Check:   "commitment_chain",
			Scope:   "commitment_ledger",
			Subject: "all",
			Result:  "PASS",
			Detail:  "5 commitments verified",
		}
		cols := VerificationRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 5)
		require.Len(t, rec, 5)
		assert.Equal(t, "PASS", rec[3])
	})
}

func TestManifestRow(t *testing.T) {
	t.Parallel()

	t.Run("Columns and Record match", func(t *testing.T) {
		t.Parallel()

		r := ManifestRow{
			File:           "event_receipts.csv",
			RecordType:     "event_receipts",
			RowCount:       "10",
			SHA256:         "abc123",
			GeneratedAtUTC: "2026-01-01T00:00:00Z",
		}
		cols := ManifestRow{}.Columns()
		rec := r.Record()
		require.Len(t, cols, 5)
		require.Len(t, rec, 5)
		assert.Equal(t, "event_receipts.csv", rec[0])
	})
}
