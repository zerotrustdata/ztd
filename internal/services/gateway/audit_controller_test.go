// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func setupTestAuditController(t *testing.T) *AuditController {
	t.Helper()
	infra := setupTestInfrastructure(t, false)

	controller, err := newAuditController(AuditControllerDeps{
		Cfg:        infra.Cfg,
		Logger:     infra.Logger,
		AuditStore: infra.AuditStore,
		Responder:  infra.Responder,
		Scrubber:   infra.Scrubber,
	})
	require.NoError(t, err)
	return controller
}

func TestNewAuditController_AllDepsProvidedNoNilFields(t *testing.T) {
	infra := setupTestInfrastructure(t, false)

	controller, err := newAuditController(AuditControllerDeps{
		Cfg:        infra.Cfg,
		Logger:     infra.Logger,
		AuditStore: infra.AuditStore,
		Responder:  infra.Responder,
		Scrubber:   infra.Scrubber,
	})
	require.NoError(t, err)

	assert.NotNil(t, controller.cfg)
	assert.NotNil(t, controller.logger)
	assert.NotNil(t, controller.auditStore)
	assert.NotNil(t, controller.responder)

	assert.Equal(t, infra.Cfg, controller.cfg)
	assert.Equal(t, infra.Logger, controller.logger)
	assert.Equal(t, infra.AuditStore, controller.auditStore)
	assert.Equal(t, infra.Responder, controller.responder)
}

func TestAuditControllerHandleAuditEventReceipts(t *testing.T) {
	auditController := setupTestAuditController(t)
	require.NoError(t, auditController.auditStore.CreateSession("event_receipt-session", constants.SessionTypeOperator, "Event receipt Session", "operator-1"))

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audit/event-receipts", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("GET by tx_id - not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts?tx_id=nonexistent", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("GET by tx_id returns canonical action event receipt", func(t *testing.T) {
		eventReceipt := &operatorv1.EventReceipt{
			TransactionId:   "tx-evidence",
			TransactionHash: "hash-evidence",
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			SignerKeyId:     "warden-key",
			Signature:       "signature",
			DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{{
				StageId:         "tx-evidence:l4",
				Kind:            operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
				Outcome:         operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED,
				TransactionId:   "tx-evidence",
				TransactionHash: "hash-evidence",
			}},
		}
		err := auditController.auditStore.RecordEventReceipt(&models.EventReceiptRecord{
			TransactionID:     eventReceipt.TransactionId,
			TransactionHash:   eventReceipt.TransactionHash,
			InvestigationID:   "investigation-evidence",
			OperatorID:        "operator-1",
			OperatorSessionID: "event_receipt-session",
			ActionType:        constants.ActionTypeExecuteBash,
			Status:            eventReceipt.Status,
			ExecutedAt:        time.Now(),
			SignerKeyID:       eventReceipt.SignerKeyId,
			Signature:         eventReceipt.Signature,
			Timestamp:         time.Now(),
			EventReceipt:      eventReceipt,
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts?tx_id=tx-evidence", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, constants.HeaderValueApplicationJSON, rr.Header().Get(constants.HeaderContentType))
		assert.Equal(t, constants.HeaderValueNoSniff, rr.Header().Get(constants.HeaderXContentTypeOptions))
		assert.Equal(t, constants.HeaderValueDeny, rr.Header().Get(constants.HeaderXFrameOptions))
		assert.Equal(t, constants.HeaderValueCSPNone, rr.Header().Get(constants.HeaderContentSecurityPolicy))
		parsed := &operatorv1.EventReceipt{}
		require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), parsed))
		assert.Equal(t, eventReceipt.TransactionId, parsed.TransactionId)
		require.Len(t, parsed.DeterministicStageEvidence, 1)
		assert.Equal(t, eventReceipt.DeterministicStageEvidence[0], parsed.DeterministicStageEvidence[0])
	})

	t.Run("GET by investigation_id and action_type returns canonical action event receipt", func(t *testing.T) {
		documentEventReceipt := &operatorv1.EventReceipt{
			TransactionId:   "tx-document-update",
			TransactionHash: "hash-document-update",
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			SignerKeyId:     "warden-key",
			Signature:       "signature",
		}
		err := auditController.auditStore.RecordEventReceipt(&models.EventReceiptRecord{
			TransactionID:     documentEventReceipt.TransactionId,
			TransactionHash:   documentEventReceipt.TransactionHash,
			InvestigationID:   "investigation-evidence",
			OperatorID:        "operator-1",
			OperatorSessionID: "event_receipt-session",
			ActionType:        constants.ActionTypeDocumentUpdate,
			Status:            documentEventReceipt.Status,
			ExecutedAt:        time.Now(),
			SignerKeyID:       documentEventReceipt.SignerKeyId,
			Signature:         documentEventReceipt.Signature,
			Timestamp:         time.Now(),
			EventReceipt:      documentEventReceipt,
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts?investigation_id=investigation-evidence&action_type=EXECUTE_BASH", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		parsed := &operatorv1.EventReceipt{}
		require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), parsed))
		assert.Equal(t, "tx-evidence", parsed.TransactionId)
	})

	t.Run("GET by investigation_id rejects duplicate correlation", func(t *testing.T) {
		eventReceipt := &operatorv1.EventReceipt{
			TransactionId:   "tx-evidence-duplicate",
			TransactionHash: "hash-evidence-duplicate",
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			SignerKeyId:     "warden-key",
			Signature:       "signature",
		}
		err := auditController.auditStore.RecordEventReceipt(&models.EventReceiptRecord{
			TransactionID:     eventReceipt.TransactionId,
			TransactionHash:   eventReceipt.TransactionHash,
			InvestigationID:   "investigation-evidence",
			OperatorID:        "operator-1",
			OperatorSessionID: "event_receipt-session",
			ActionType:        constants.ActionTypeExecuteBash,
			Status:            eventReceipt.Status,
			ExecutedAt:        time.Now(),
			SignerKeyID:       eventReceipt.SignerKeyId,
			Signature:         eventReceipt.Signature,
			Timestamp:         time.Now(),
			EventReceipt:      eventReceipt,
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts?investigation_id=investigation-evidence&action_type=EXECUTE_BASH", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)

		assert.Equal(t, http.StatusConflict, rr.Code)
		assert.Contains(t, rr.Body.String(), constants.ErrAuditStoreEventReceiptCorrelationDuplicate.Error())
	})

	t.Run("GET with multiple correlation fields is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts?tx_id=tx-evidence&investigation_id=investigation-evidence&action_type=EXECUTE_BASH", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("GET by investigation_id without action_type is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts?investigation_id=investigation-evidence", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("GET list - success with defaults", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), `"success":true`)
	})

	t.Run("GET list - with operator_session_id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts?operator_session_id=op-123", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("GET list - with limit and offset", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts?limit=10&offset=5", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceipts(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}

func TestAuditControllerHandleAuditEventReceiptsExport(t *testing.T) {
	auditController := setupTestAuditController(t)

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audit/event-receipts/export", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceiptsExport(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("GET - success with defaults", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts/export", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceiptsExport(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		assert.True(t, json.Valid(rr.Body.Bytes()))

		var response models.AuditEventReceiptsResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
		assert.True(t, response.Success)
	})

	t.Run("GET - with since parameter RFC3339", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts/export?since=2026-01-01T00:00:00Z", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceiptsExport(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("GET - with since parameter timestamp", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts/export?since=1704067200000", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceiptsExport(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("GET - with limit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/event-receipts/export?limit=50", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEventReceiptsExport(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}

func TestAuditControllerHandleAuditEvents(t *testing.T) {
	auditController := setupTestAuditController(t)

	t.Run("Failure - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audit/events", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEvents(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Success - empty events", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEvents(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		var resp models.AuditEventsResponse
		err := json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Equal(t, 0, resp.Count)
	})

	t.Run("Success - with operator_session_id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events?operator_session_id=op-session-123", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEvents(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		var resp models.AuditEventsResponse
		err := json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.Success)
	})

	t.Run("Success - with limit and offset", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events?limit=10&offset=5", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditEvents(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		var resp models.AuditEventsResponse
		err := json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.Success)
	})
}

func TestAuditControllerHandleAuditSummary(t *testing.T) {
	auditController := setupTestAuditController(t)

	t.Run("Failure - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audit/summary", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditSummary(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Success - empty summary", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/summary", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditSummary(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		var resp models.AuditSummaryResponse
		err := json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Equal(t, 0, resp.EventsTotal)
		assert.Equal(t, 0, resp.EventReceiptsTotal)
		assert.Equal(t, 0, resp.TotalRecords)
	})

	t.Run("Success - with operator_session_id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/summary?operator_session_id=op-session-123", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditSummary(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		var resp models.AuditSummaryResponse
		err := json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.NotNil(t, resp.EventsSummary)
		assert.NotNil(t, resp.EventReceiptsSummary)
	})
}

func TestAuditControllerHandleAuditReport(t *testing.T) {
	auditController := setupTestAuditController(t)

	t.Run("Failure - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audit/report", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditReport(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Success - empty report", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/report", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditReport(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		var resp models.AuditReportResponse
		err := json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Equal(t, 0, resp.Report.EventsCount)
		assert.Equal(t, 0, resp.Report.EventReceiptsCount)
		assert.Equal(t, 0, resp.Report.TotalRecords)
		assert.NotEmpty(t, resp.Report.GeneratedAt)
	})

	t.Run("Success - with operator_session_id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/report?operator_session_id=op-session-123", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditReport(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		var resp models.AuditReportResponse
		err := json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Equal(t, "op-session-123", resp.Report.OperatorSessionID)
	})
}

func TestAuditControllerHandleAuditVerify(t *testing.T) {
	auditController := setupTestAuditController(t)

	t.Run("Failure - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audit/verify", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditVerify(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Success - empty chain verifies", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/verify", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditVerify(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		var resp models.AuditVerifyResponse
		err := json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.True(t, resp.OK)
		assert.Equal(t, int64(0), resp.HeadSeq)
	})

	t.Run("Success - event receipt stage appends verify", func(t *testing.T) {
		require.NoError(t, auditController.auditStore.CreateSession("verify-session", constants.SessionTypeOperator, "Verify Session", "operator-1"))
		err := auditController.auditStore.RecordEventReceipt(&models.EventReceiptRecord{
			TransactionID:     "tx-verify-1",
			TransactionHash:   "hash-verify",
			OperatorID:        "operator-1",
			OperatorSessionID: "verify-session",
			ActionType:        constants.ActionTypeExecuteBash,
			TargetResource:    "localhost",
			Status:            operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ResultSummary:     "done",
			ExecutedAt:        time.Now().UTC(),
			SignerKeyID:       "key-1",
			Signature:         "sig-1",
			Timestamp:         time.Now().UTC(),
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/verify", nil)
		rr := httptest.NewRecorder()
		auditController.handleAuditVerify(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		var resp models.AuditVerifyResponse
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.OK)
		assert.Equal(t, int64(1), resp.HeadSeq)
		assert.NotEmpty(t, resp.HeadHash)
	})
}
