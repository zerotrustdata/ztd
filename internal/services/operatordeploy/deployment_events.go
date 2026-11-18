// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// DeploymentWatcher registers a launch to be announced by the Gateway.
type DeploymentWatcher interface {
	Watch(launchID string) *DeploymentWatch
}

// DeploymentEvents is the deploying caller's one SSE subscription to the Gateway.
// A worker launched by a deploy presents its launch ID when it creates its
// enrollment request and when it establishes its command subscription; the
// Gateway announces both to the owner's CLI sessions keyed by that ID, so
// staging and readiness are learned from events and nothing polls.
type DeploymentEvents struct {
	mu      sync.Mutex
	watches map[string]*DeploymentWatch
}

// DeploymentWatch holds what the Gateway has announced for one launch.
type DeploymentWatch struct {
	mu            sync.Mutex
	requestID     string
	sessionID     string
	status        constants.OperatorStatus
	requestSeen   chan struct{}
	readinessSeen chan struct{}
}

// NewDeploymentEvents constructs an empty deployment event router.
func NewDeploymentEvents() *DeploymentEvents {
	return &DeploymentEvents{watches: make(map[string]*DeploymentWatch)}
}

// Watch registers launchID. It must run before the worker starts, because the
// stream delivers only live events.
func (e *DeploymentEvents) Watch(launchID string) *DeploymentWatch {
	w := &DeploymentWatch{requestSeen: make(chan struct{}), readinessSeen: make(chan struct{})}
	e.mu.Lock()
	e.watches[launchID] = w
	e.mu.Unlock()
	return w
}

// Handle routes one SSE frame to the watch of the launch it names. Events for
// other launches, and every other event type, are ignored.
func (e *DeploymentEvents) Handle(eventType, data string) {
	event, ok := sse.DecodeEvent(eventType, data)
	if !ok {
		return
	}
	switch event.Type {
	case string(constants.EventPlatformApprovalsChanged):
		var payload models.ApprovalsChangedPayload
		if json.Unmarshal(event.Data, &payload) != nil || payload.DeploymentID == "" || payload.RequestID == "" {
			return
		}
		if w := e.lookup(payload.DeploymentID); w != nil {
			w.SetRequest(payload.RequestID)
		}
	case string(constants.EventOperatorStatusUpdatedActive),
		string(constants.EventOperatorStatusUpdatedStale),
		string(constants.EventOperatorStatusUpdatedStopped),
		string(constants.EventOperatorStatusUpdatedTerminated):
		var payload models.OperatorStatusUpdatedPayload
		if json.Unmarshal(event.Data, &payload) != nil || payload.DeploymentID == "" {
			return
		}
		if w := e.lookup(payload.DeploymentID); w != nil {
			w.SetReadiness(payload.OperatorSessionID, payload.Status)
		}
	}
}

func (e *DeploymentEvents) lookup(launchID string) *DeploymentWatch {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.watches[launchID]
}

// SetRequest records the pending enrollment request ID announced for this launch.
func (w *DeploymentWatch) SetRequest(requestID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.requestID == "" {
		w.requestID = requestID
		close(w.requestSeen)
	}
}

// SetReadiness records the Operator session ID and status announced for this launch.
func (w *DeploymentWatch) SetReadiness(sessionID string, status constants.OperatorStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == constants.OperatorStatusActive {
		return
	}
	first := w.status == ""
	w.sessionID, w.status = sessionID, status
	if first {
		close(w.readinessSeen)
	}
}

// AwaitStaged returns the request ID the worker's enrollment request was
// announced with. Operator identity is memory-only, so every launch requests
// enrollment; a worker announced ready without a request fails closed.
func (w *DeploymentWatch) AwaitStaged(ctx context.Context) (string, error) {
	select {
	case <-w.requestSeen:
	case <-w.readinessSeen:
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %w", constants.ErrOperatorDeployFailed, ctx.Err())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.requestID == "" {
		return "", fmt.Errorf("%w: operator announced readiness without an enrollment request", constants.ErrOperatorDeployFailed)
	}
	return w.requestID, nil
}

// AwaitReady returns the session ID of the worker's command subscription once
// the Gateway announces it. A subscription announced for an Operator that is
// not active is a failure, not readiness.
func (w *DeploymentWatch) AwaitReady(ctx context.Context) (string, error) {
	select {
	case <-w.readinessSeen:
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %w", constants.ErrOperatorDeployFailed, ctx.Err())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status != constants.OperatorStatusActive {
		return "", fmt.Errorf("%w: operator established its command subscription as %s", constants.ErrOperatorDeployFailed, w.status)
	}
	return w.sessionID, nil
}

// ConnectDeploymentEvents opens the owner's CLI-session SSE stream and returns
// once it is established, so no event for a worker started afterwards is
// missed. The returned stop ends the stream.
func ConnectDeploymentEvents(ctx context.Context, httpClient *http.Client, baseURL, cliSessionID string) (*DeploymentEvents, func(), error) {
	events := NewDeploymentEvents()
	client := sse.NewClient(baseURL+constants.APIPaths.SSEStream+"?since_id=0", httpClient)
	client.SetHeader(constants.HeaderCLISessionID, cliSessionID)
	connected := make(chan struct{})
	var once sync.Once
	client.SetOnConnect(func() { once.Do(func() { close(connected) }) })

	streamCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(streamCtx, events.Handle)
	}()
	stop := func() {
		cancel()
		<-done
	}

	select {
	case <-connected:
		return events, stop, nil
	case <-done:
		cancel()
		return nil, nil, fmt.Errorf("%w: deployment event stream closed before it connected", constants.ErrOperatorDeployFailed)
	case <-ctx.Done():
		stop()
		return nil, nil, fmt.Errorf("%w: %w", constants.ErrOperatorDeployFailed, ctx.Err())
	}
}
