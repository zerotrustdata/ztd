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
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Session is the CLI's authenticated Gateway session. *api.Client satisfies
// it, so the TUI uses the same mTLS identity, CLI session header, and SSE
// stream URL as every other CLI command.
type Session interface {
	NewSSEClient() *sse.Client
	DoRequestContext(ctx context.Context, method, path string, body interface{}) ([]byte, error)
}

// messageSender abstracts the Send method of tea.Program so tests can
// capture messages without a real bubbletea program.
type messageSender interface {
	Send(msg tea.Msg)
}

// Adapter bridges the CLI session's SSE stream, pending-approval list,
// Operator list, platform enrollments, and Gateway health to bubbletea
// messages.
type Adapter struct {
	sessions *sessionManager
	sender   messageSender
}

// sessionExpiredDetail is the connection detail shown when the Gateway
// rejects the CLI session; it names the same recovery as the rest of the CLI.
const sessionExpiredDetail = "CLI session expired — run ztd auth refresh"

func newAdapterWithManager(sessions *sessionManager, sender messageSender) *Adapter {
	return &Adapter{sessions: sessions, sender: sender}
}

// Run streams the CLI session's SSE events into the program until ctx is
// cancelled or the Gateway rejects the CLI session. The stream is live-only
// and approvals.changed is ephemeral, so the pending-approval list (and the
// Operator list and Gateway health) are re-fetched on every connect, and the
// pending list again on every approvals.changed event — the same
// reconciliation the console performs. Reconnects use the shared sse.Client
// backoff.
func (a *Adapter) Run(ctx context.Context) {
	if a.sessions == nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for {
		gw, generation, changed := a.sessions.snapshot()
		if gw == nil {
			return
		}
		if !a.runSession(ctx, gw, generation, changed) {
			return
		}
	}
}

// runSession streams one CLI session. A replacement session cancels this
// stream and returns true so Run can open the new stream with fresh headers.
func (a *Adapter) runSession(ctx context.Context, gw *gateway, generation uint64, changed <-chan struct{}) bool {
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	go func() {
		select {
		case <-streamCtx.Done():
		case <-changed:
			cancelStream()
		}
	}()

	connected := make(chan struct{}, 1)
	approvalsChanged := make(chan struct{}, 1)
	enrollmentsChanged := make(chan struct{}, 1)
	operatorsChanged := make(chan struct{}, 1)
	signal := func(ch chan struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}

	stream := gw.session.NewSSEClient()
	stream.SetReconnectOnClose(true)
	stream.SetOnConnect(func() {
		a.sender.Send(ConnStatusMsg{Status: ConnConnected})
		signal(connected)
	})
	stream.SetOnDisconnect(func(err error) {
		if errors.Is(err, constants.ErrCLISessionRefreshRequired) {
			a.sender.Send(ConnStatusMsg{Status: ConnFailed, Detail: sessionExpiredDetail})
			return
		}
		a.sender.Send(ConnStatusMsg{Status: ConnReconnecting, Detail: err.Error()})
	})

	refresherDone := make(chan struct{})
	go func() {
		defer close(refresherDone)
		a.refresh(streamCtx, connected, approvalsChanged, enrollmentsChanged, operatorsChanged)
	}()
	defer func() { <-refresherDone }()

	a.sender.Send(ConnStatusMsg{Status: ConnConnecting})
	stream.Run(streamCtx, func(eventType, data string) {
		ev, ok := decodeSSEEvent(eventType, data)
		if !ok {
			a.sender.Send(LedgerMsg{Level: LevelInfo, Message: data, Time: timeNow()})
			return
		}
		switch ev.Type {
		case string(constants.EventPlatformApprovalsChanged):
			var p models.ApprovalsChangedPayload
			if err := json.Unmarshal(ev.Data, &p); err != nil {
				a.sender.Send(decodeFailure(ev.Type, err))
				return
			}
			if p.Subject == models.ApprovalsChangedEnrollments {
				signal(enrollmentsChanged)
			} else {
				signal(approvalsChanged)
			}
		case string(constants.EventOperatorStatusUpdatedActive),
			string(constants.EventOperatorStatusUpdatedOffline),
			string(constants.EventOperatorStatusUpdatedStale),
			string(constants.EventOperatorStatusUpdatedStopped),
			string(constants.EventOperatorStatusUpdatedTerminated):
			signal(operatorsChanged)
		}
		for _, msg := range translateEvent(ev) {
			a.sender.Send(msg)
		}
	})
	if streamCtx.Err() != nil && ctx.Err() == nil {
		_, currentGeneration, _ := a.sessions.snapshot()
		return currentGeneration != generation
	}
	return false
}

// refresh re-fetches Gateway state until ctx is cancelled: everything on each
// connect, pending approvals or platform enrollments on each approvals.changed
// for that subject, and Operators on each operator.status.updated event.
func (a *Adapter) refresh(ctx context.Context, connected, approvalsChanged, enrollmentsChanged, operatorsChanged <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-connected:
			if gw := a.currentGateway(); gw != nil {
				a.sender.Send(gw.fetchHealth(ctx))
				a.sender.Send(gw.fetchPendingApprovals(ctx))
				a.sender.Send(gw.fetchOperators(ctx))
				a.sender.Send(gw.fetchEnrollments(ctx))
			}
		case <-approvalsChanged:
			if gw := a.currentGateway(); gw != nil {
				a.sender.Send(gw.fetchPendingApprovals(ctx))
			}
		case <-enrollmentsChanged:
			if gw := a.currentGateway(); gw != nil {
				a.sender.Send(gw.fetchEnrollments(ctx))
			}
		case <-operatorsChanged:
			if gw := a.currentGateway(); gw != nil {
				a.sender.Send(gw.fetchOperators(ctx))
			}
		}
	}
}

func (a *Adapter) currentGateway() *gateway {
	if a.sessions == nil {
		return nil
	}
	gw, _, _ := a.sessions.snapshot()
	return gw
}

// decodeSSEEvent unwraps a Gateway SSE frame; an event without a type is
// labelled "unknown" for the ledger.
func decodeSSEEvent(eventType, data string) (sse.Event, bool) {
	ev, ok := sse.DecodeEvent(eventType, data)
	if ok && ev.Type == "" {
		ev.Type = "unknown"
	}
	return ev, ok
}

// decodeFailure reports a malformed SSE payload as a warning line. The event is
// dropped rather than rendered from the zero value of a failed decode.
func decodeFailure(eventType string, err error) LedgerMsg {
	return LedgerMsg{Level: LevelWarn, Message: fmt.Sprintf("Malformed %s event: %v", eventType, err), Time: timeNow()}
}

// translateEvent maps a decoded Gateway event to tea.Msg values.
func translateEvent(ev sse.Event) []tea.Msg {
	innerType, payload := ev.Type, ev.Data

	switch {
	// Stream sentinels
	case innerType == "error":
		var p models.SSEErrorEvent
		if err := json.Unmarshal(ev.Raw, &p); err != nil {
			return []tea.Msg{decodeFailure(innerType, err)}
		}
		return []tea.Msg{LedgerMsg{Level: LevelWarn, Message: "Event stream error: " + p.Reason, Time: timeNow()}}
	case innerType == "truncated":
		var p models.SSETruncationEvent
		if err := json.Unmarshal(ev.Raw, &p); err != nil {
			return []tea.Msg{decodeFailure(innerType, err)}
		}
		return []tea.Msg{LedgerMsg{Level: LevelWarn, Message: fmt.Sprintf("Event replay truncated at %d events (cursor %d)", p.Limit, p.SinceID), Time: timeNow()}}

	// Operator approvals requested
	case innerType == string(constants.EventOperatorCommandApprovalRequested):
		var p struct {
			ApprovalID string `json:"approval_id"`
			TxHash     string `json:"tx_hash"`
			Command    string `json:"command"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return []tea.Msg{decodeFailure(innerType, err)}
		}
		txID := firstNonEmpty(p.TxHash, p.ApprovalID)
		cmd := firstNonEmpty(p.Command, txID)
		return []tea.Msg{
			PipelineMsg{Stage: StageL3, Status: StatusWaiting, TxID: txID, Detail: "Command approval requested: " + cmd},
			LedgerMsg{Level: LevelWarn, Message: "APPROVAL REQUIRED: Command execution: " + cmd, Time: timeNow()},
		}
	case innerType == string(constants.EventOperatorFileEditApprovalRequested):
		var p struct {
			ApprovalID string `json:"approval_id"`
			FilePath   string `json:"file_path"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return []tea.Msg{decodeFailure(innerType, err)}
		}
		targetPath := firstNonEmpty(p.FilePath, p.ApprovalID)
		return []tea.Msg{
			PipelineMsg{Stage: StageL3, Status: StatusWaiting, TxID: p.ApprovalID, Detail: "File edit approval requested: " + targetPath},
			LedgerMsg{Level: LevelWarn, Message: "APPROVAL REQUIRED: File edit: " + targetPath, Time: timeNow()},
		}
	case innerType == string(constants.EventOperatorIntentApprovalRequested):
		var p struct {
			ApprovalID string `json:"approval_id"`
			IntentName string `json:"intent_name"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return []tea.Msg{decodeFailure(innerType, err)}
		}
		intent := firstNonEmpty(p.IntentName, p.ApprovalID)
		return []tea.Msg{
			PipelineMsg{Stage: StageL3, Status: StatusWaiting, TxID: p.ApprovalID, Detail: "Intent approval requested: " + intent},
			LedgerMsg{Level: LevelWarn, Message: "APPROVAL REQUIRED: Intent authorization: " + intent, Time: timeNow()},
		}
	case innerType == string(constants.EventOperatorNotaryApprovalRequested):
		var p struct {
			ApprovalID string `json:"approval_id"`
			TxHash     string `json:"tx_hash"`
			ToolName   string `json:"tool_name"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return []tea.Msg{decodeFailure(innerType, err)}
		}
		txID := firstNonEmpty(p.TxHash, p.ApprovalID)
		tool := firstNonEmpty(p.ToolName, "transaction")
		return []tea.Msg{
			PipelineMsg{Stage: StageL3, Status: StatusWaiting, TxID: txID, Detail: "Notary approval requested: " + tool},
			LedgerMsg{Level: LevelWarn, Message: "APPROVAL REQUIRED: Notary approval for " + tool, Time: timeNow()},
		}
	// Operator heartbeats are applied in place by the model, so the stream
	// does not re-list Operators for them and writes no ledger line.
	case innerType == string(constants.EventOperatorHeartbeatReceived):
		var p models.OperatorHeartbeatReceivedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return []tea.Msg{decodeFailure(innerType, err)}
		}
		return []tea.Msg{OperatorHeartbeatMsg{Heartbeat: p}}
	// approval.completed is a flat event (models.ApprovalCompletedEvent),
	// the same shape auth.ApprovalSubscription.Wait consumes.
	case innerType == constants.SSEEventTypeApprovalCompleted:
		var p models.ApprovalCompletedEvent
		if err := json.Unmarshal(ev.Raw, &p); err != nil {
			return []tea.Msg{decodeFailure(innerType, err)}
		}
		msg := ApprovalCompletedMsg{TxHash: p.TxHash}
		if p.HasEventReceipt() {
			eventReceipt := &operatorv1.EventReceipt{}
			if err := protojson.Unmarshal(p.EventReceipt, eventReceipt); err != nil {
				return []tea.Msg{decodeFailure(innerType, err)}
			}
			msg.Denied = eventReceipt.GetRejectionCode() == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED
		}
		return []tea.Msg{msg}
	case innerType == string(constants.EventPlatformApprovalsChanged):
		// Both subjects are reconciled by the adapter's refresh: transactions
		// re-list pending approvals, and enrollments re-list enrollments.
		return nil

	default:
		return []tea.Msg{LedgerMsg{Level: LevelInfo, Message: innerType + ": " + string(payload), Time: timeNow()}}
	}
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
