// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestReceiptProjectionNeverRegressesTerminalStatus(t *testing.T) {
	store := newIntegrationEventsStore(t)
	require.NoError(t, store.CreateSession("projection-session", constants.SessionTypeOperator, "Projection", "user-1"))

	completed := sampleEventReceiptRecord(t, "projection-session")
	require.NoError(t, store.RecordEventReceipt(completed))

	replayed := sampleEventReceiptRecord(t, "projection-session")
	replayed.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	replayed.EventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	replayed.ResultSummary = ""
	require.NoError(t, store.RecordEventReceipt(replayed))

	got, err := store.GetEventReceipt(completed.TransactionID)
	require.NoError(t, err)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, got.Status)
	require.Equal(t, "completed successfully", got.ResultSummary)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, got.EventReceipt.GetStatus())

	// The replayed stage is still evidence: both stage writes append an audit fact.
	var facts int
	require.NoError(t, store.db.QueryRow("SELECT count(*) FROM events WHERE type = ?", string(constants.EventOperatorReceiptRecorded)).Scan(&facts))
	require.Equal(t, 2, facts)
}

func TestReceiptProjectionAdvancesFromExecutingToTerminal(t *testing.T) {
	store := newIntegrationEventsStore(t)
	require.NoError(t, store.CreateSession("projection-session", constants.SessionTypeOperator, "Projection", "user-1"))

	executing := sampleEventReceiptRecord(t, "projection-session")
	executing.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	executing.EventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	require.NoError(t, store.RecordEventReceipt(executing))

	failed := sampleEventReceiptRecord(t, "projection-session")
	failed.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	failed.EventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	require.NoError(t, store.RecordEventReceipt(failed))

	got, err := store.GetEventReceipt(failed.TransactionID)
	require.NoError(t, err)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, got.Status)
}

func TestReceiptReadsRestoreL2L3Validity(t *testing.T) {
	store := newIntegrationEventsStore(t)
	require.NoError(t, store.CreateSession("projection-session", constants.SessionTypeOperator, "Projection", "user-1"))

	record := sampleEventReceiptRecord(t, "projection-session")
	record.EventReceipt.L2Status = operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
	record.EventReceipt.L3Status = operatorv1.L3Status_L3_STATUS_NOT_REQUIRED
	require.NoError(t, store.RecordEventReceipt(record))

	got, err := store.GetEventReceipt(record.TransactionID)
	require.NoError(t, err)
	require.True(t, got.L2Valid)
	require.False(t, got.L3Valid)

	listed, err := store.ListEventReceipts(models.EventsScope{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.True(t, listed[0].L2Valid)
}

func TestReceiptReadsTolerateLegacyNullColumns(t *testing.T) {
	store := newIntegrationEventsStore(t)
	require.NoError(t, store.CreateSession("projection-session", constants.SessionTypeOperator, "Projection", "user-1"))

	record := sampleEventReceiptRecord(t, "projection-session")
	require.NoError(t, store.RecordEventReceipt(record))
	// Writers may omit the optional columns, which then read back as NULL.
	_, err := store.db.Exec(`UPDATE events SET event_receipt_requestor_user_id = NULL, event_receipt_acting_app_id = NULL, event_receipt_event_type = NULL,
		event_receipt_target_resource = NULL, event_receipt_result_summary = NULL, event_receipt_state_root_before = NULL, event_receipt_state_root_after = NULL
		WHERE event_receipt_status IS NOT NULL`)
	require.NoError(t, err)

	got, err := store.GetEventReceipt(record.TransactionID)
	require.NoError(t, err)
	require.Empty(t, got.RequestorUserID)
	require.Empty(t, got.TargetResource)

	listed, err := store.ListEventReceipts(models.EventsScope{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, listed, 1)
}

func TestReceiptProjectionIgnoresReplayedUnspecifiedAfterTerminal(t *testing.T) {
	store := newIntegrationEventsStore(t)
	require.NoError(t, store.CreateSession("projection-session", constants.SessionTypeOperator, "Projection", "user-1"))

	failed := sampleEventReceiptRecord(t, "projection-session")
	failed.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	failed.EventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	require.NoError(t, store.RecordEventReceipt(failed))

	stale := sampleEventReceiptRecord(t, "projection-session")
	stale.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED
	stale.EventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED
	require.NoError(t, store.RecordEventReceipt(stale))

	got, err := store.GetEventReceipt(failed.TransactionID)
	require.NoError(t, err)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, got.Status)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, got.EventReceipt.GetStatus())
}

func TestReceiptProjectionAllowsTerminalReplacement(t *testing.T) {
	store := newIntegrationEventsStore(t)
	require.NoError(t, store.CreateSession("projection-session", constants.SessionTypeOperator, "Projection", "user-1"))

	first := sampleEventReceiptRecord(t, "projection-session")
	require.NoError(t, store.RecordEventReceipt(first))

	second := sampleEventReceiptRecord(t, "projection-session")
	second.ResultSummary = "re-signed final stage"
	require.NoError(t, store.RecordEventReceipt(second))

	got, err := store.GetEventReceipt(first.TransactionID)
	require.NoError(t, err)
	require.Equal(t, "re-signed final stage", got.ResultSummary)
}

func TestReceiptProjectionKeepsTransactionsIndependent(t *testing.T) {
	store := newIntegrationEventsStore(t)
	require.NoError(t, store.CreateSession("projection-session", constants.SessionTypeOperator, "Projection", "user-1"))

	done := sampleEventReceiptRecord(t, "projection-session")
	require.NoError(t, store.RecordEventReceipt(done))

	other := sampleEventReceiptRecord(t, "projection-session")
	other.TransactionID = "tx-integration-2"
	other.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	other.EventReceipt.TransactionId = other.TransactionID
	other.EventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	require.NoError(t, store.RecordEventReceipt(other))

	got, err := store.GetEventReceipt(other.TransactionID)
	require.NoError(t, err)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING, got.Status)
}

func TestReceiptExportReadsRestoreL2L3ValidityAndTolerateNulls(t *testing.T) {
	store := newIntegrationEventsStore(t)
	require.NoError(t, store.CreateSession("projection-session", constants.SessionTypeOperator, "Projection", "user-1"))

	record := sampleEventReceiptRecord(t, "projection-session")
	record.EventReceipt.L2Status = operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
	record.EventReceipt.L3Status = operatorv1.L3Status_L3_STATUS_REQUIRED_VALID
	require.NoError(t, store.RecordEventReceipt(record))
	_, err := store.db.Exec(`UPDATE events SET event_receipt_requestor_user_id = NULL, event_receipt_acting_app_id = NULL, event_receipt_event_type = NULL,
		event_receipt_target_resource = NULL, event_receipt_result_summary = NULL, event_receipt_state_root_before = NULL, event_receipt_state_root_after = NULL
		WHERE event_receipt_status IS NOT NULL`)
	require.NoError(t, err)

	got, err := store.ListEventReceiptsSince(time.Now().Add(-time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, got[0].L2Valid)
	require.True(t, got[0].L3Valid)
	require.Empty(t, got[0].StateRootBefore)
}
