// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// TestApprovedRestart_ReenrollsAfterRestart verifies the restart contract for an
// approved Operator. Operator identity is memory-only: a restarted Operator does
// not reuse its certificate, session, or requester token. It enrolls again, the
// Gateway holds a new pending operator request, and approving that request issues
// a new session. The pre-restart session is superseded and is no longer active.
//
// The user starts the full stack, approves the enrollments, waits for the operator
// to become active, then restarts the Operator:
//
//	stop the operator process, then: ./ztd operator start
//
// and runs:
//
//	./ztd test e2e --run TestApprovedRestart
//
// Running it on an approved stack without a restart fails at the pending-request
// step with an actionable message; it does not pass by reusing the old identity.
func TestApprovedRestart_ReenrollsAfterRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Subscribe before any wait so no event after this point is missed. The
	// restart happened before this test ran, so the request may already exist:
	// each wait reads state once first, then waits for the next event.
	events := newE2EEventWaiters()
	stream := subscribeE2EEvents(ctx, t, events)
	defer stream.stop()

	// Remote sessions on record before the restarted Operator's request is approved.
	// Their documents are superseded by the new issuance, never reused.
	priorSessions := remoteOperatorSessions(ctx, t)

	// The restarted Operator files a new enrollment request. Read the pending list,
	// then wait for an approvals.changed (enrollments) event before reading again.
	var request *models.PlatformEnrollmentPendingRequest
	for request == nil {
		pending, err := e2eClient.GetPendingEnrollments(ctx)
		require.NoError(t, err, "pending enrollment list must succeed")
		request = newestPendingOperatorRequest(pending.Requests)
		if request == nil {
			events.awaitPending(ctx, t,
				"a new pending operator enrollment request; restart the operator first (stop it, then ./ztd operator start)")
		}
	}
	require.NotEmpty(t, request.RequestID, "pending operator request must have a non-empty request ID")
	t.Logf("restarted operator filed request: %s", request.RequestID)

	require.NoError(t, e2eClient.ApproveEnrollment(ctx, request.RequestID),
		"approving the restarted operator's request %s must succeed", request.RequestID)

	// Approval issues a new identity. Read the registry once, then wait for an
	// operator.status.updated event before reading again. Only sessions absent
	// before the restart count.
	var active *operatorv1.OperatorDocument
	for active == nil {
		operators, err := e2eClient.ListOperators(ctx)
		require.NoError(t, err, "operator list must succeed after approval")
		var fresh []*operatorv1.OperatorDocument
		for _, op := range operators.Operators {
			if !priorSessions[op.OperatorSessionId] {
				fresh = append(fresh, op)
			}
		}
		active = findLiveActiveRemoteOperator(fresh)
		if active == nil {
			events.awaitStatus(ctx, t, "a new active session after approving the restarted operator")
		}
	}
	require.NotEmpty(t, active.Id, "active operator must have a non-empty ID")
	require.NotEmpty(t, active.OperatorSessionId, "active operator must have a non-empty session ID")
	t.Logf("re-enrolled operator active: id=%s session=%s", active.Id, active.OperatorSessionId)

	// Every pre-restart remote session must have been superseded, not left active.
	operators, err := e2eClient.ListOperators(ctx)
	require.NoError(t, err, "operator list must succeed after re-enrollment")
	for _, op := range operators.Operators {
		if priorSessions[op.OperatorSessionId] {
			assert.NotEqual(t, string(constants.OperatorStatusActive), op.Status,
				"pre-restart session %s of operator %s must not stay active after re-enrollment", op.OperatorSessionId, op.Id)
		}
	}

	// The session lookup must agree with the list for the new session.
	single, err := e2eClient.GetOperatorBySession(ctx, active.OperatorSessionId)
	require.NoError(t, err, "session lookup must succeed for the re-enrolled operator")
	require.True(t, single.Success, "session lookup response must report success")
	require.NotNil(t, single.Operator, "session lookup must return an operator document")
	assert.Equal(t, active.Id, single.Operator.Id, "session lookup must return the same operator ID")
	assert.Equal(t, string(constants.OperatorStatusActive), single.Operator.Status,
		"session lookup must report active status")

	// Command roundtrip proves the new session has command readiness, not just registry state.
	resp := e2eClient.dispatchFsRead(t, ctx)
	assert.NotEmpty(t, resp.GetTransactionId(), "dispatch response must carry a transaction ID")
	assert.Equal(t, string(constants.Event.Operator.FsRead.Completed), resp.GetEventType(),
		"dispatch response event type must be the fs.read completed event")
	t.Logf("command roundtrip succeeded on re-enrolled session: txn=%s", resp.GetTransactionId())
}

// e2eEventWaiters holds the level-triggered wake-ups the enrollment restart tests
// wait on. A signal that arrives while nobody waits is kept (capacity one), so a
// waiter reads state, sees nothing, and then waits: no event is lost. Waiters
// re-read authoritative state after each wake; the wake itself carries no data.
type e2eEventWaiters struct {
	pending   chan struct{} // approvals.changed for the enrollments subject
	status    chan struct{} // operator.status.updated.* on this CLI session
	heartbeat chan models.OperatorHeartbeatReceivedPayload
}

// e2eEventWaitTimeout bounds one wait for an event. Each wait reads state once
// before blocking, so this is the only wall-clock limit on the wait itself.
const e2eEventWaitTimeout = 180 * time.Second

// e2eOperatorStatusUpdated is the set of typed operator.status.updated.* events.
var e2eOperatorStatusUpdated = map[string]bool{
	string(constants.EventOperatorStatusUpdatedActive):      true,
	string(constants.EventOperatorStatusUpdatedAvailable):   true,
	string(constants.EventOperatorStatusUpdatedBound):       true,
	string(constants.EventOperatorStatusUpdatedOffline):     true,
	string(constants.EventOperatorStatusUpdatedStale):       true,
	string(constants.EventOperatorStatusUpdatedStopped):     true,
	string(constants.EventOperatorStatusUpdatedTerminated):  true,
	string(constants.EventOperatorStatusUpdatedUnavailable): true,
}

func newE2EEventWaiters() *e2eEventWaiters {
	return &e2eEventWaiters{
		pending:   make(chan struct{}, 1),
		status:    make(chan struct{}, 1),
		heartbeat: make(chan models.OperatorHeartbeatReceivedPayload, 1024),
	}
}

func signalE2EWake(w chan struct{}) {
	select {
	case w <- struct{}{}:
	default:
	}
}

// handle classifies one live event from the owner's CLI-session stream.
func (w *e2eEventWaiters) handle(ev sse.Event) {
	switch {
	case ev.Type == string(constants.EventPlatformApprovalsChanged):
		var p models.ApprovalsChangedPayload
		if json.Unmarshal(ev.Data, &p) == nil && p.Subject == models.ApprovalsChangedEnrollments {
			signalE2EWake(w.pending)
		}
	case e2eOperatorStatusUpdated[ev.Type]:
		signalE2EWake(w.status)
	case ev.Type == string(constants.EventOperatorHeartbeatReceived):
		var p models.OperatorHeartbeatReceivedPayload
		if json.Unmarshal(ev.Data, &p) == nil {
			select {
			case w.heartbeat <- p:
			default:
			}
		}
	}
}

// awaitPending blocks until an approvals.changed (enrollments) event arrives, or
// fails the test after the bounded wait. The caller re-reads the pending list.
func (w *e2eEventWaiters) awaitPending(ctx context.Context, t *testing.T, what string) {
	t.Helper()
	awaitE2EWake(ctx, t, w.pending, what)
}

// awaitStatus blocks until an operator.status.updated event arrives, or fails the
// test after the bounded wait. The caller re-reads the registry.
func (w *e2eEventWaiters) awaitStatus(ctx context.Context, t *testing.T, what string) {
	t.Helper()
	awaitE2EWake(ctx, t, w.status, what)
}

// awaitStatusOrHeartbeat blocks until an operator.status.updated or an
// operator.heartbeat.received event arrives, or fails the test after the bounded
// wait. Events that queued up behind the one that woke it are drained, so a
// burst from a large cohort costs one re-read of the registry, not one each.
// The caller re-reads the registry.
func (w *e2eEventWaiters) awaitStatusOrHeartbeat(ctx context.Context, t *testing.T, what string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, e2eEventWaitTimeout)
	defer cancel()
	select {
	case <-w.status:
	case <-w.heartbeat:
	case <-waitCtx.Done():
		t.Fatalf("timed out after %s waiting for an event announcing %s: %v", e2eEventWaitTimeout, what, waitCtx.Err())
	}
	for {
		select {
		case <-w.heartbeat:
		default:
			return
		}
	}
}

func awaitE2EWake(ctx context.Context, t *testing.T, wake chan struct{}, what string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, e2eEventWaitTimeout)
	defer cancel()
	select {
	case <-wake:
	case <-waitCtx.Done():
		t.Fatalf("timed out after %s waiting for an event announcing %s: %v", e2eEventWaitTimeout, what, waitCtx.Err())
	}
}

// e2eEventStream is one connected owner CLI-session SSE subscription.
type e2eEventStream struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// stop ends the subscription and waits for its goroutine to exit.
func (s *e2eEventStream) stop() {
	s.cancel()
	<-s.done
}

// subscribeE2EEvents opens the owner's CLI-session SSE stream using the same
// sse.NewClient construction as the other e2e stream tests, and returns once it
// is connected. It fails the test if the stream does not connect within 15s.
func subscribeE2EEvents(ctx context.Context, t *testing.T, w *e2eEventWaiters) *e2eEventStream {
	t.Helper()
	sseHTTPClient := &http.Client{Timeout: 0, Transport: e2eClient.mtlsClient.Transport}
	sseURL := fmt.Sprintf("%s%s?since_id=0", e2eClient.gatewayHTTPS, constants.APIPaths.SSEStream)
	client := sse.NewClient(sseURL, sseHTTPClient)
	client.SetHeader(constants.HeaderCLISessionID, e2eClient.cliSessionID)

	connected := make(chan struct{})
	var once sync.Once
	client.SetOnConnect(func() { once.Do(func() { close(connected) }) })

	streamCtx, cancel := context.WithCancel(ctx)
	s := &e2eEventStream{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		client.Run(streamCtx, func(eventType, data string) {
			if ev, ok := sse.DecodeEvent(eventType, data); ok {
				w.handle(ev)
			}
		})
	}()

	select {
	case <-connected:
		return s
	case <-time.After(15 * time.Second):
		s.stop()
		t.Fatal("owner CLI-session SSE stream did not connect within 15s")
	case <-ctx.Done():
		s.stop()
		t.Fatalf("owner CLI-session SSE stream was not connected: %v", ctx.Err())
	}
	return nil
}

// remoteOperatorSessions returns the session IDs of remote Operator documents
// currently in the registry. The embedded Operator is excluded: it is live with
// the Gateway and is never superseded by enrollment.
func remoteOperatorSessions(ctx context.Context, t *testing.T) map[string]bool {
	t.Helper()
	operators, err := e2eClient.ListOperators(ctx)
	require.NoError(t, err, "operator list must succeed")
	sessions := make(map[string]bool)
	for _, op := range operators.Operators {
		if op.OperatorType == string(constants.OperatorTypeRemote) && op.OperatorSessionId != "" {
			sessions[op.OperatorSessionId] = true
		}
	}
	return sessions
}

// newestPendingOperatorRequest returns the most recently created pending operator
// request. A request from before a restart is orphaned: the Gateway does not
// withdraw it when its bootstrap socket drops, so it stays pending until it
// expires, but the restarted Operator never resumes it. The newest request is
// the restarted Operator's.
func newestPendingOperatorRequest(requests []models.PlatformEnrollmentPendingRequest) *models.PlatformEnrollmentPendingRequest {
	var newest *models.PlatformEnrollmentPendingRequest
	for i := range requests {
		r := &requests[i]
		if r.ComponentKind != models.PlatformComponentOperator {
			continue
		}
		if newest == nil || r.CreatedAt.After(newest.CreatedAt) {
			newest = r
		}
	}
	return newest
}
