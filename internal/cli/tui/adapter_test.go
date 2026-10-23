// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// translateSSEEvents maps a raw SSE frame to tea.Msg values for test assertions.
func translateSSEEvents(eventType, data string) []tea.Msg {
	ev, ok := decodeSSEEvent(eventType, data)
	if !ok {
		return []tea.Msg{LedgerMsg{Level: LevelInfo, Message: data, Time: timeNow()}}
	}
	return translateEvent(ev)
}

func TestTranslateSSEEvent(t *testing.T) { //nolint:paralleltest // mutates package-level state

	fixedTime := time.Date(2026, 6, 15, 10, 30, 0, 0, time.UTC)
	originalTimeNow := timeNow
	t.Cleanup(func() { timeNow = originalTimeNow })
	timeNow = func() time.Time { return fixedTime }

	t.Run("unknown event type falls back to LedgerMsg", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		data := `{"type":"system.heartbeat","data":{"status":"ok"}}`
		msgs := translateSSEEvents("system.heartbeat", data)
		require.Len(t, msgs, 1)
		lm, ok := msgs[0].(LedgerMsg)
		require.True(t, ok)
		assert.Contains(t, lm.Message, "system.heartbeat")
		assert.Equal(t, fixedTime, lm.Time)
	})

	t.Run("event families with no Gateway producer are not special-cased", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		for _, eventType := range []string{"pipeline.advance", "ledger.entry", "consensus.vote"} {
			msgs := translateSSEEvents("", `{"type":"`+eventType+`","data":{}}`)
			require.Len(t, msgs, 1, eventType)
			_, ok := msgs[0].(LedgerMsg)
			assert.True(t, ok, "%s should fall back to a ledger line", eventType)
		}
	})

	t.Run("non-JSON data falls back to LedgerMsg with raw text", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		msgs := translateSSEEvents("unknown", "plain text message")
		require.Len(t, msgs, 1)
		lm, ok := msgs[0].(LedgerMsg)
		require.True(t, ok)
		assert.Equal(t, "plain text message", lm.Message)
		assert.Equal(t, LevelInfo, lm.Level)
	})

	t.Run("uses event type from SSE header when payload type is empty", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		data := `{"type":"","data":{"approval_id":"app-2","file_path":"/etc/hosts"}}`
		msgs := translateSSEEvents(string(constants.EventOperatorFileEditApprovalRequested), data)
		require.Len(t, msgs, 2)
		pm, ok := msgs[0].(PipelineMsg)
		require.True(t, ok)
		assert.Equal(t, StageL3, pm.Stage)
		assert.Contains(t, pm.Detail, "/etc/hosts")
	})

	t.Run("R14: extracts type from SSEPushPayload envelope when eventType is empty", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		innerEvent := `{"type":"` + string(constants.EventOperatorIntentApprovalRequested) + `","data":{"approval_id":"app-3","intent_name":"network.read"}}`
		envelopeJSON, err := json.Marshal(models.SSEPushPayload{CliSessionID: "cli-123", Event: json.RawMessage(innerEvent)})
		require.NoError(t, err)

		msgs := translateSSEEvents("", string(envelopeJSON))
		require.Len(t, msgs, 2)
		pm, ok := msgs[0].(PipelineMsg)
		require.True(t, ok, "expected PipelineMsg from SSEPushPayload envelope, got %T", msgs[0])
		assert.Equal(t, StatusWaiting, pm.Status)
		assert.Equal(t, "app-3", pm.TxID)
		assert.Contains(t, pm.Detail, "network.read")
	})
}

func TestAdapterRunNilSession(t *testing.T) {
	t.Parallel()

	a := newAdapterWithManager(nil, &mockSender{})
	done := make(chan struct{})
	go func() {
		a.Run(t.Context())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("adapter.Run with nil session should return immediately")
	}
}

// testSession is a Session over a plain-HTTP test server: the SSE stream is
// served at url, and Gateway requests return the configured JSON (or err).
type testSession struct {
	url              string
	pendingJSON      string
	operatorsJSON    string
	healthJSON       string
	auditEventsJSON  string
	auditSummaryJSON string
	auditVerifyJSON  string
	err              error
	mu               sync.Mutex
	listCalls        int
	paths            []string

	enrollPendingJSON string
	enrolledJSON      string
	decisionJSON      string
	revokeJSON        string
	stopJSON          string
	recoveryJSON      string
	bindJSON          string
	unbindJSON        string
	postErr           error
	enrollListCalls   int
	operatorCalls     int
	posted            []interface{}
}

func (s *testSession) NewSSEClient() *sse.Client { return sse.NewClient(s.url, nil) }

func (s *testSession) DoRequestContext(_ context.Context, method, path string, body interface{}) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths = append(s.paths, path)
	if s.err != nil {
		return nil, s.err
	}
	if method == http.MethodPost {
		s.posted = append(s.posted, body)
		if s.postErr != nil {
			return nil, s.postErr
		}
		switch path {
		case constants.APIPaths.AuthPlatformEnrollmentDecision:
			return []byte(orDefault(s.decisionJSON, `{"request_id":"req-1","state":"approved"}`)), nil
		case constants.APIPaths.AuthPlatformEnrollmentRevoke:
			return []byte(orDefault(s.revokeJSON, `{"request_id":"req-1","component_kind":"operator","state":"revoked"}`)), nil
		case constants.APIPaths.OperatorsStop:
			return []byte(orDefault(s.stopJSON, `{"success":true,"operator_id":"op-remote","operator_session_id":"sess-remote"}`)), nil
		case constants.APIPaths.AuthCLIRecoveryApproveCLI:
			return []byte(orDefault(s.recoveryJSON, `{"success":true,"state":"approved"}`)), nil
		case constants.APIPaths.AuthCLIBind:
			return []byte(orDefault(s.bindJSON, `{"success":true,"cli_session_id":"cli-new","user_id":"user-1","operator_id":"op-remote","operator_session_id":"sess-remote","bound":[{"operator_id":"op-remote","operator_session_id":"sess-remote"}]}`)), nil
		case constants.APIPaths.AuthCLIUnbind:
			return []byte(orDefault(s.unbindJSON, `{"success":true,"cli_session_id":"cli-unbound","user_id":"user-1"}`)), nil
		}
		return nil, constants.ErrNotFound
	}
	if method != http.MethodGet {
		return nil, constants.ErrNotFound
	}
	switch {
	case path == constants.APIPaths.AuthPlatformEnrollmentPending:
		s.enrollListCalls++
		return []byte(orDefault(s.enrollPendingJSON, `{"requests":[]}`)), nil
	case path == constants.APIPaths.AuthPlatformEnrollmentEnrolled:
		return []byte(orDefault(s.enrolledJSON, `{"enrollments":[]}`)), nil
	case path == constants.APIPaths.ApprovalsCLIList:
		s.listCalls++
		return []byte(orDefault(s.pendingJSON, `{"transactions":[]}`)), nil
	case strings.HasPrefix(path, constants.APIPaths.Operators):
		s.operatorCalls++
		return []byte(orDefault(s.operatorsJSON, `{"success":true,"operators":[]}`)), nil
	case path == constants.APIPaths.Health:
		return []byte(orDefault(s.healthJSON, `{"status":"ok","posture":"notary"}`)), nil
	case strings.HasPrefix(path, constants.APIPaths.AuditEvents):
		return []byte(orDefault(s.auditEventsJSON, `{"success":true,"events":[],"count":0}`)), nil
	case path == constants.APIPaths.AuditSummary:
		return []byte(orDefault(s.auditSummaryJSON, `{"success":true,"events_summary":{},"events_total":0,"receipts_summary":{},"receipts_total":0,"total_records":0}`)), nil
	case strings.HasPrefix(path, constants.APIPaths.AuditVerify):
		return []byte(orDefault(s.auditVerifyJSON, `{"success":true,"ok":true,"verified_from_seq":0,"head_seq":0,"head_hash":""}`)), nil
	}
	return nil, constants.ErrNotFound
}

func (s *testSession) enrollmentListCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enrollListCalls
}

func (s *testSession) operatorListCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.operatorCalls
}

func (s *testSession) pendingListCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls
}

func (s *testSession) requestedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// mockSender captures tea.Msg values sent by the adapter for test assertions.
type mockSender struct {
	mu       sync.Mutex
	messages []tea.Msg
}

func (m *mockSender) Send(msg tea.Msg) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
}

func (m *mockSender) snapshot() []tea.Msg {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tea.Msg, len(m.messages))
	copy(out, m.messages)
	return out
}

func TestTranslateSSEEvents_Approvals(t *testing.T) { //nolint:paralleltest // mutates package-level state

	fixedTime := time.Date(2026, 6, 15, 10, 30, 0, 0, time.UTC)
	originalTimeNow := timeNow
	t.Cleanup(func() { timeNow = originalTimeNow })
	timeNow = func() time.Time { return fixedTime }

	t.Run("command approval requested", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		msgs := translateSSEEvents(string(constants.EventOperatorCommandApprovalRequested), `{"type":"ztd.v1.operator.command.approval.requested","data":{"approval_id":"app-1","command":"rm -rf /tmp/test"}}`)
		require.Len(t, msgs, 2)
		pm, ok := msgs[0].(PipelineMsg)
		require.True(t, ok)
		assert.Equal(t, StageL3, pm.Stage)
		assert.Equal(t, StatusWaiting, pm.Status)
		assert.Equal(t, "app-1", pm.TxID)
		assert.Contains(t, pm.Detail, "rm -rf /tmp/test")
		lm, ok := msgs[1].(LedgerMsg)
		require.True(t, ok)
		assert.Contains(t, lm.Message, "APPROVAL REQUIRED")
	})

	t.Run("approval completed", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		msgs := translateSSEEvents(constants.SSEEventTypeApprovalCompleted, `{"type":"approval.completed","tx_hash":"tx-999"}`)
		assert.Equal(t, []tea.Msg{ApprovalCompletedMsg{TxHash: "tx-999"}}, msgs)
	})
}

// gatewayFrame builds an SSE data payload exactly as the Gateway's
// SSEEventPublisher does: SSEPushPayload{event: {type, data}}.
func gatewayFrame(t *testing.T, eventType string, data any) string {
	t.Helper()
	dataJSON, err := json.Marshal(data)
	require.NoError(t, err)
	inner, err := json.Marshal(struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}{Type: eventType, Data: dataJSON})
	require.NoError(t, err)
	frame, err := json.Marshal(models.SSEPushPayload{UserID: "user-1", CliSessionID: "cli-1", Event: inner})
	require.NoError(t, err)
	return string(frame)
}

func TestTranslateSSEEvents_GatewayWireShape(t *testing.T) {
	t.Parallel()

	t.Run("publisher envelope payload is decoded from data", func(t *testing.T) {
		t.Parallel()

		frame := gatewayFrame(t, string(constants.EventOperatorCommandApprovalRequested), map[string]string{"approval_id": "app-7", "command": "systemctl restart nginx"})
		msgs := translateSSEEvents("", frame)
		require.Len(t, msgs, 2)
		pm, ok := msgs[0].(PipelineMsg)
		require.True(t, ok)
		assert.Equal(t, "app-7", pm.TxID)
		assert.Contains(t, pm.Detail, "systemctl restart nginx")
	})

	t.Run("approval.completed is read from the flat event", func(t *testing.T) {
		t.Parallel()

		event, err := json.Marshal(models.ApprovalCompletedEvent{Type: constants.SSEEventTypeApprovalCompleted, UserID: "user-1", TxHash: "tx-abc"})
		require.NoError(t, err)
		frame, err := json.Marshal(models.SSEPushPayload{UserID: "user-1", CliSessionID: "cli-1", Event: event})
		require.NoError(t, err)
		msgs := translateSSEEvents("", string(frame))
		assert.Equal(t, []tea.Msg{ApprovalCompletedMsg{TxHash: "tx-abc"}}, msgs)
	})

	t.Run("approval.completed with the L3_DENIED receipt is a deny", func(t *testing.T) {
		t.Parallel()

		receipt, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
			TransactionHash: "tx-denied",
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
			RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED,
		})
		require.NoError(t, err)
		event, err := json.Marshal(models.ApprovalCompletedEvent{Type: constants.SSEEventTypeApprovalCompleted, UserID: "user-1", TxHash: "tx-denied", Receipt: receipt})
		require.NoError(t, err)
		frame, err := json.Marshal(models.SSEPushPayload{UserID: "user-1", CliSessionID: "cli-1", Event: event})
		require.NoError(t, err)
		msgs := translateSSEEvents("", string(frame))
		require.Len(t, msgs, 1)
		completed, ok := msgs[0].(ApprovalCompletedMsg)
		require.True(t, ok)
		assert.Equal(t, "tx-denied", completed.TxHash)
		require.NotNil(t, completed.Receipt)
		assert.Equal(t, "tx-denied", completed.Receipt.GetTransactionHash())
		assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, completed.Receipt.GetRejectionCode())
	})

	t.Run("approval.completed with an undecodable receipt is a decode failure", func(t *testing.T) {
		event, err := json.Marshal(models.ApprovalCompletedEvent{Type: constants.SSEEventTypeApprovalCompleted, UserID: "user-1", TxHash: "tx-bad", Receipt: json.RawMessage(`{"status":"NOT_A_STATUS"}`)})
		require.NoError(t, err)
		frame, err := json.Marshal(models.SSEPushPayload{UserID: "user-1", CliSessionID: "cli-1", Event: event})
		require.NoError(t, err)
		msgs := translateSSEEvents("", string(frame))
		require.Len(t, msgs, 1)
		_, ok := msgs[0].(ApprovalCompletedMsg)
		assert.False(t, ok, "an undecodable receipt never reaches the model as a completion")
	})

	t.Run("transaction approvals.changed produces no message", func(t *testing.T) {
		t.Parallel()

		frame := gatewayFrame(t, string(constants.EventPlatformApprovalsChanged), models.ApprovalsChangedPayload{Subject: models.ApprovalsChangedTransactions})
		assert.Empty(t, translateSSEEvents("", frame))
	})

	t.Run("enrollment approvals.changed produces no message", func(t *testing.T) {
		t.Parallel(

		// The adapter re-lists enrollments; the model announces new requests.
		)

		frame := gatewayFrame(t, string(constants.EventPlatformApprovalsChanged), models.ApprovalsChangedPayload{Subject: models.ApprovalsChangedEnrollments})
		assert.Empty(t, translateSSEEvents("", frame))
	})

	t.Run("replay error sentinel surfaces as a warning", func(t *testing.T) {
		t.Parallel()

		sentinel, err := json.Marshal(models.SSEErrorEvent{Type: "error", Reason: "replay_failed"})
		require.NoError(t, err)
		msgs := translateSSEEvents("", string(sentinel))
		require.Len(t, msgs, 1)
		lm, ok := msgs[0].(LedgerMsg)
		require.True(t, ok)
		assert.Equal(t, LevelWarn, lm.Level)
		assert.Contains(t, lm.Message, "replay_failed")
	})
}

// TestTranslateSSEEvents_MalformedPayloadWarns verifies that a payload which
// fails to decode is reported as one warning line naming the event, and never
// renders as a message built from the zero value of the failed decode.
func TestTranslateSSEEvents_MalformedPayloadWarns(t *testing.T) {
	t.Parallel()

	malformedFrame := func(t *testing.T, eventType, data string) string {
		t.Helper()
		frame, err := json.Marshal(models.SSEPushPayload{
			UserID:       "user-1",
			CliSessionID: "cli-1",
			Event:        json.RawMessage(`{"type":"` + eventType + `","data":` + data + `}`),
		})
		require.NoError(t, err)
		return string(frame)
	}

	tests := []struct {
		name      string
		eventType string
		data      string
	}{
		{"approval request data is a string", string(constants.EventOperatorCommandApprovalRequested), `"not-an-object"`},
		{"heartbeat data is a string", string(constants.EventOperatorHeartbeatReceived), `"not-an-object"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msgs := translateSSEEvents("", malformedFrame(t, tt.eventType, tt.data))
			require.Len(t, msgs, 1, "a malformed payload must yield exactly one message")
			lm, ok := msgs[0].(LedgerMsg)
			require.True(t, ok, "got %T", msgs[0])
			assert.Equal(t, LevelWarn, lm.Level)
			assert.Contains(t, lm.Message, tt.eventType)
		})
	}

	t.Run("stream error sentinel with a mistyped field warns instead of an empty reason", func(t *testing.T) {
		t.Parallel()

		msgs := translateSSEEvents("", `{"type":"error","reason":5}`)
		require.Len(t, msgs, 1)
		lm, ok := msgs[0].(LedgerMsg)
		require.True(t, ok, "got %T", msgs[0])
		assert.Equal(t, LevelWarn, lm.Level)
		assert.Contains(t, lm.Message, "Malformed error event")
		assert.NotContains(t, lm.Message, "Event stream error")
	})
}
