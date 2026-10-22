// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	egressSessionID = "egress-session"
	egressMarker    = "123-45-6789"
	egressEmail     = "alice.secret@example.com"
)

// seedEgressAudit records an event and an event receipt whose content-bearing fields carry planted
// secrets, the way the raw local store holds them.
func seedEgressAudit(t *testing.T, store *storage.SQLAuditStore) {
	t.Helper()
	require.NoError(t, store.CreateSession(egressSessionID, constants.SessionTypeOperator, "Egress", "user@test.com"))
	_, err := store.RecordEvent(&storage.Event{
		TransactionID:     "tx-egress",
		OperatorSessionID: egressSessionID,
		Timestamp:         time.Now().UTC(),
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "content " + egressMarker,
		CommandRaw:        "cat /home/alice/notes " + egressMarker + " " + egressEmail,
		CommandStdout:     "stdout " + egressMarker,
		CommandStderr:     "stderr " + egressEmail,
	})
	require.NoError(t, err)
	vote, err := json.Marshal(models.ConsensusVoteRecord{
		TransactionID: "tx-egress",
		MemberAppID:   "tribunal-member-1",
		Outcome:       models.ConsensusVoteOutcomeDeny,
		Reason:        "reads notes of " + egressEmail,
		Failure:       "saw " + egressMarker,
	})
	require.NoError(t, err)
	_, err = store.RecordEvent(&storage.Event{
		TransactionID:     "tx-egress",
		OperatorSessionID: egressSessionID,
		Timestamp:         time.Now().UTC(),
		Type:              constants.EventConsensusVoteRecorded,
		ContentText:       string(vote),
	})
	require.NoError(t, err)

	eventReceipt := &operatorv1.EventReceipt{
		TransactionId:   "tx-egress",
		TransactionHash: "hash-egress",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		SignerKeyId:     "warden-key",
		Signature:       "signature",
	}
	require.NoError(t, store.RecordEventReceipt(&models.EventReceiptRecord{
		TransactionID:     eventReceipt.TransactionId,
		TransactionHash:   eventReceipt.TransactionHash,
		InvestigationID:   "investigation-egress",
		OperatorID:        "operator-1",
		OperatorSessionID: egressSessionID,
		ActionType:        constants.ActionTypeExecuteBash,
		TargetResource:    "/home/alice/" + egressEmail,
		Status:            eventReceipt.Status,
		ResultSummary:     "wrote " + egressMarker,
		ExecutedAt:        time.Now(),
		SignerKeyID:       eventReceipt.SignerKeyId,
		Signature:         eventReceipt.Signature,
		Timestamp:         time.Now(),
		EventReceipt:      eventReceipt,
	}))
}

func newEgressController(t *testing.T, scrubber *scrubbing.ScrubbingService) *AuditController {
	t.Helper()
	infra := setupTestInfrastructure(t, false)
	seedEgressAudit(t, infra.AuditStore)
	controller, err := newAuditController(AuditControllerDeps{
		Cfg: infra.Cfg, Logger: infra.Logger, AuditStore: infra.AuditStore, Responder: infra.Responder, Scrubber: scrubber,
	})
	require.NoError(t, err)
	return controller
}

func requireNoPlantedContent(t *testing.T, body string) {
	t.Helper()
	assert.NotContains(t, body, egressMarker)
	assert.NotContains(t, body, egressEmail)
}

func TestNewAuditController_RequiresScrubber(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	controller, err := newAuditController(AuditControllerDeps{Cfg: infra.Cfg, Logger: infra.Logger, AuditStore: infra.AuditStore, Responder: infra.Responder})
	require.ErrorIs(t, err, constants.ErrScrubbingServiceRequired)
	assert.Nil(t, controller)
}

// Everything the audit endpoints return leaves the runtime, so no planted secret may appear in
// any response, and the response still carries the identifiers and the (scrubbed) command.
func TestAuditController_EventsAndReportNeverReturnRawContent(t *testing.T) {
	controller := newEgressController(t, newTestScrubber(t))

	t.Run("events", func(t *testing.T) {
		rr := httptest.NewRecorder()
		controller.handleAuditEvents(rr, httptest.NewRequest(http.MethodGet, "/api/v1/audit/events?operator_session_id="+egressSessionID, nil))
		require.Equal(t, http.StatusOK, rr.Code)
		requireNoPlantedContent(t, rr.Body.String())
		assert.Contains(t, rr.Body.String(), "tx-egress")
		assert.Contains(t, rr.Body.String(), "cat /home/alice/notes", "non-sensitive command text is kept")
	})

	t.Run("report", func(t *testing.T) {
		rr := httptest.NewRecorder()
		controller.handleAuditReport(rr, httptest.NewRequest(http.MethodGet, "/api/v1/audit/report?operator_session_id="+egressSessionID, nil))
		require.Equal(t, http.StatusOK, rr.Code)
		requireNoPlantedContent(t, rr.Body.String())
		assert.Contains(t, rr.Body.String(), "tx-egress")
	})

	t.Run("consensus votes", func(t *testing.T) {
		rr := httptest.NewRecorder()
		controller.handleAuditConsensusVotes(rr, httptest.NewRequest(http.MethodGet, "/api/v1/audit/consensus-votes?tx_id=tx-egress", nil))
		require.Equal(t, http.StatusOK, rr.Code)
		requireNoPlantedContent(t, rr.Body.String())
		var resp models.ConsensusVotesResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		require.Len(t, resp.Votes, 1, "only the vote event, not the command event")
		assert.Equal(t, "tribunal-member-1", resp.Votes[0].MemberAppID)
		assert.Equal(t, models.ConsensusVoteOutcomeDeny, resp.Votes[0].Outcome)
		assert.Contains(t, resp.Votes[0].Reason, "reads notes of", "non-sensitive reason text is kept")

		rr = httptest.NewRecorder()
		controller.handleAuditConsensusVotes(rr, httptest.NewRequest(http.MethodGet, "/api/v1/audit/consensus-votes", nil))
		assert.Equal(t, http.StatusBadRequest, rr.Code, "tx_id is required")
	})

	t.Run("event receipt list and export", func(t *testing.T) {
		for path, call := range map[string]func(http.ResponseWriter, *http.Request){
			"/api/v1/audit/event-receipts?operator_session_id=" + egressSessionID:        controller.handleAuditEventReceipts,
			"/api/v1/audit/event-receipts/export?operator_session_id=" + egressSessionID: controller.handleAuditEventReceiptsExport,
		} {
			rr := httptest.NewRecorder()
			call(rr, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusOK, rr.Code, path)
			requireNoPlantedContent(t, rr.Body.String())
			assert.Contains(t, rr.Body.String(), "tx-egress", path)
		}
	})
}

// A disabled scrubber withholds the response: fail closed, never the raw rows.
func TestAuditController_DisabledScrubberWithholds(t *testing.T) {
	cfg := scrubbing.DefaultConfig()
	cfg.Enabled = false
	disabled, err := scrubbing.NewScrubbingService(context.Background(), cfg, testutil.NewTestLogger(), nil)
	require.NoError(t, err)
	controller := newEgressController(t, disabled)

	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"events":               controller.handleAuditEvents,
		"report":               controller.handleAuditReport,
		"event_receipts":       controller.handleAuditEventReceipts,
		"event receipt export": controller.handleAuditEventReceiptsExport,
		"consensus votes":      controller.handleAuditConsensusVotes,
	} {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			call(rr, httptest.NewRequest(http.MethodGet, "/x?operator_session_id="+egressSessionID, nil))
			assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
			assert.Contains(t, rr.Body.String(), constants.ErrEgressScrubberUnavailable.Error())
			requireNoPlantedContent(t, rr.Body.String())
			assert.NotContains(t, rr.Body.String(), "tx-egress", "no row of any kind is returned")
		})
	}
}
