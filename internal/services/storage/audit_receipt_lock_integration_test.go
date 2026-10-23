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
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// startReceiptSession creates the session row the receipt tests record against; the
// session owner, not the audit store, creates it at session start.
func startReceiptSession(t *testing.T, store *SQLAuditStore, sessionID string) {
	t.Helper()
	require.NoError(t, store.CreateSession(sessionID, constants.SessionTypeOperator, "Operator Session", "operator-integration-1"))
}

func TestReceiptPreparationDoesNotWaitForLedger(t *testing.T) {
	for _, invalid := range []string{"canonical JSON", "locked vault", "session whitespace"} {
		t.Run(invalid, func(t *testing.T) {
			store := newIntegrationAuditStore(t)
			record := sampleEventReceiptRecord(t, "session")
			switch invalid {
			case "canonical JSON":
				record.ActionReceipt.ResultSummary = string([]byte{0xff})
			case "locked vault":
				store.encryptionVault.Lock()
			case "session whitespace":
				record.OperatorSessionID = " session "
			}
			release, acquired := store.db.TryAcquireWriter()
			require.True(t, acquired)
			done := make(chan error, 1)
			go func() {
				done <- store.RecordEventReceiptWithCommitment(record, func(string) (*operatorv1.CommitmentAttestation, error) {
					return nil, constants.ErrNotFound
				})
			}()
			var err error
			select {
			case err = <-done:
				release()
			case <-time.After(time.Second):
				release()
				err = <-done // Join even when the regression is present.
				t.Error("receipt preparation waited for the occupied writer gate")
			}
			require.Error(t, err)
			switch invalid {
			case "locked vault":
				require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
			case "session whitespace":
				require.ErrorIs(t, err, constants.ErrAuditSessionMissing)
			}
			var count int
			require.NoError(t, store.db.QueryRow("SELECT count(*) FROM events WHERE type = ?", string(constants.EventOperatorReceiptRecorded)).Scan(&count))
			require.Zero(t, count)
		})
	}
}

type receiptLogObserver struct {
	slog.Handler
	observe func(slog.Record)
}

func (h receiptLogObserver) Handle(_ context.Context, record slog.Record) error {
	h.observe(record)
	return nil
}

func (h receiptLogObserver) Enabled(context.Context, slog.Level) bool { return true }

func TestReceiptSuccessLogsFollowCommitAndUnlock(t *testing.T) {
	store := newIntegrationAuditStore(t)
	startReceiptSession(t, store, "session")
	var messages []string
	logger := slog.New(receiptLogObserver{
		Handler: slog.DiscardHandler,
		observe: func(record slog.Record) {
			messages = append(messages, record.Message)
			release, unlocked := store.db.TryAcquireWriter()
			if unlocked {
				release()
			}
			assert.True(t, unlocked, "log handler must not serialize receipt writers: %s", record.Message)
			var count int
			err := store.db.QueryRow("SELECT count(*) FROM commitment_ledger").Scan(&count)
			require.NoError(t, err)
			assert.Equal(t, 1, count, "success logging must follow commit")
		},
	})
	store.logger = logger
	store.commitmentLedger.logger = logger
	record := sampleEventReceiptRecord(t, "session")
	require.NoError(t, store.RecordEventReceiptWithCommitment(record, func(prior string) (*operatorv1.CommitmentAttestation, error) {
		return &operatorv1.CommitmentAttestation{
			TransactionId: record.TransactionID, TransactionHash: record.TransactionHash,
			PriorCommitmentHash: prior, Hash: "commitment-hash",
		}, nil
	}))
	require.Contains(t, messages, "Event receipt recorded")
	require.Contains(t, messages, "Commitment appended to ledger")
	require.NoError(t, store.VerifyChain(t.Context(), 0))
}

func TestReceiptCommitFailureRollsBackWithoutSuccessLogs(t *testing.T) {
	store := newIntegrationAuditStore(t)
	startReceiptSession(t, store, "session")
	// Fail COMMIT after the commitment INSERT has succeeded, rather than
	// failing the builder before the old in-transaction success log ran.
	_, err := store.db.Exec(`
		CREATE TABLE commit_failure (
			session_id TEXT REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER fail_commit AFTER INSERT ON commitment_ledger BEGIN
			INSERT INTO commit_failure (session_id) VALUES ('missing-session');
		END;
	`)
	require.NoError(t, err)
	var messages []string
	logger := slog.New(receiptLogObserver{
		Handler: slog.DiscardHandler,
		observe: func(record slog.Record) { messages = append(messages, record.Message) },
	})
	store.logger, store.commitmentLedger.logger = logger, logger
	record := sampleEventReceiptRecord(t, "session")
	err = store.RecordEventReceiptWithCommitment(record, func(prior string) (*operatorv1.CommitmentAttestation, error) {
		return &operatorv1.CommitmentAttestation{
			TransactionId: record.TransactionID, TransactionHash: record.TransactionHash,
			PriorCommitmentHash: prior, Hash: "commitment-hash",
		}, nil
	})
	require.ErrorIs(t, err, constants.ErrAuditStoreRecordReceiptFailed)
	require.Empty(t, messages, "a rolled-back write must not emit success logs")
	for _, table := range []string{"events", "commitment_ledger"} {
		var count int
		require.NoError(t, store.db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count, table)
	}
	require.NoError(t, store.VerifyChain(t.Context(), 0))
}

func TestReceiptConcurrentStagesPreserveBothChains(t *testing.T) {
	store := newIntegrationAuditStore(t)
	startReceiptSession(t, store, "shared-session")
	const writers = 32
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		record := sampleEventReceiptRecord(t, "shared-session")
		record.TransactionID = fmt.Sprintf("receipt-%d", i)
		record.ActionReceipt.TransactionId = record.TransactionID
		record.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
		record.ActionReceipt.Status = record.Status
		wg.Go(func() {
			<-start
			err := store.RecordEventReceiptWithCommitment(record, func(prior string) (*operatorv1.CommitmentAttestation, error) {
				hash := "commitment-" + record.TransactionID
				return &operatorv1.CommitmentAttestation{
					TransactionId: record.TransactionID, TransactionHash: record.TransactionHash,
					PriorCommitmentHash: prior, Hash: hash,
				}, nil
			})
			if err == nil {
				record.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
				record.ActionReceipt.Status = record.Status
				err = store.RecordEventReceipt(record)
			}
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	rows, err := store.commitmentLedger.ListCommitments()
	require.NoError(t, err)
	require.Len(t, rows, writers)
	prior := ""
	for _, row := range rows {
		require.Equal(t, prior, row.PriorCommitmentHash)
		prior = row.Hash
		record, err := store.GetEventReceipt(row.TransactionID)
		require.NoError(t, err)
		require.NotNil(t, record)
		require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, record.Status)
	}
	var count int
	require.NoError(t, store.db.QueryRow("SELECT count(*) FROM events").Scan(&count))
	require.Equal(t, writers*2, count)
	require.NoError(t, store.VerifyChain(t.Context(), 0))
}

func TestReceiptMissingSessionFailsClosedAtomically(t *testing.T) {
	for _, withCommitment := range []bool{false, true} {
		t.Run(fmt.Sprintf("commitment=%t", withCommitment), func(t *testing.T) {
			store := newIntegrationAuditStore(t)
			headSeq, headHash, err := store.GetAuditChainHead(t.Context())
			require.NoError(t, err)

			record := sampleEventReceiptRecord(t, "missing-session")
			if withCommitment {
				err = store.RecordEventReceiptWithCommitment(record, func(string) (*operatorv1.CommitmentAttestation, error) {
					t.Error("commitment builder ran despite missing session")
					return nil, constants.ErrNotFound
				})
			} else {
				err = store.RecordEventReceipt(record)
			}
			require.ErrorIs(t, err, constants.ErrAuditStoreRecordReceiptFailed)
			require.ErrorIs(t, err, constants.ErrAuditSessionUnknown)
			persisted, err := store.GetEventReceipt(record.TransactionID)
			require.NoError(t, err)
			require.Nil(t, persisted)
			afterSeq, afterHash, err := store.GetAuditChainHead(t.Context())
			require.NoError(t, err)
			require.Equal(t, headSeq, afterSeq)
			require.Equal(t, headHash, afterHash)
			var count int
			require.NoError(t, store.db.QueryRow("SELECT count(*) FROM sessions").Scan(&count))
			require.Zero(t, count, "a rejected receipt must not create its session")
			require.NoError(t, store.db.QueryRow("SELECT count(*) FROM commitment_ledger").Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestReceiptCommitmentInvalidBuilderResultRollsBackAtomically(t *testing.T) {
	for _, invalid := range []string{"head mismatch", "nil attestation", "invalid UTF-8"} {
		t.Run(invalid, func(t *testing.T) {
			store := newIntegrationAuditStore(t)
			startReceiptSession(t, store, "session")
			record := sampleEventReceiptRecord(t, "session")
			err := store.RecordEventReceiptWithCommitment(record, func(prior string) (*operatorv1.CommitmentAttestation, error) {
				attestation := &operatorv1.CommitmentAttestation{
					TransactionId: record.TransactionID, TransactionHash: record.TransactionHash,
					PriorCommitmentHash: prior, Hash: "commitment-hash",
				}
				switch invalid {
				case "head mismatch":
					attestation.PriorCommitmentHash = "different-head"
				case "nil attestation":
					return nil, nil
				case "invalid UTF-8":
					attestation.TargetResource = string([]byte{0xff})
				}
				return attestation, nil
			})
			require.ErrorIs(t, err, constants.ErrAuditStoreRecordReceiptFailed)
			switch invalid {
			case "head mismatch":
				require.ErrorIs(t, err, constants.ErrValidationFailed)
			case "nil attestation":
				require.ErrorIs(t, err, constants.ErrMissingRequiredField)
			}
			for _, table := range []string{"events", "commitment_ledger"} {
				var count int
				require.NoError(t, store.db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
				require.Zero(t, count, table)
			}
		})
	}
}
