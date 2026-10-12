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
	"testing"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// TestPubSub_HeartbeatAdvances proves heartbeat delivery over the pub/sub
// path. A heartbeat is observed-tier telemetry: the Gateway records it as the
// Operator's last_heartbeat_at and announces it with an ephemeral
// operator.heartbeat.received event on the owner's open streams; the bound
// Operator document (UpdatedAt) does not change. The test subscribes to the
// owner's CLI-session stream, takes one typed observation, and waits for a
// heartbeat event for that Operator carrying a strictly later
// last_heartbeat_at, which only a live heartbeat delivery can produce. A
// re-list must then return the same last_heartbeat_at the event carried.
func TestPubSub_HeartbeatAdvances(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*e2eEventWaitTimeout)
	defer cancel()

	events := newE2EEventWaiters()
	stream := subscribeE2EEvents(ctx, t, events)
	defer stream.stop()

	first := activeOperator(t, ctx)
	require.NotNil(t, first.LastHeartbeatAt,
		"first observation: active operator last_heartbeat_at must be set by at least one heartbeat")
	firstSeen := first.LastHeartbeatAt.AsTime()
	t.Logf("first heartbeat observation: operator_id=%s last_heartbeat_at=%s hostname=%s",
		first.Id, firstSeen.UTC().Format(time.RFC3339Nano), first.CurrentHostname)

	received := awaitHeartbeatAfter(ctx, t, events, first.Id, firstSeen)

	operators, err := e2eClient.ListOperators(ctx)
	require.NoError(t, err, "operator list must succeed after the heartbeat event")
	second := findOperatorByID(operators.Operators, first.Id)
	require.NotNil(t, second, "operator %s must remain registered", first.Id)
	require.NotNil(t, second.LastHeartbeatAt, "re-list must carry last_heartbeat_at")
	assert.False(t, second.LastHeartbeatAt.AsTime().Before(received.LastHeartbeatAt),
		"re-list last_heartbeat_at must be at least the value the heartbeat event announced")
	assert.Equal(t, string(constants.OperatorStatusActive), second.Status,
		"operator must remain active across heartbeat observations")
	assert.NotEmpty(t, second.CurrentHostname,
		"active remote operator CurrentHostname must be populated from heartbeat telemetry")
	assert.NotNil(t, second.LatestHeartbeatSnapshot,
		"active remote operator LatestHeartbeat must be populated from heartbeat telemetry")
	t.Logf("heartbeat event: last_heartbeat_at=%s (advanced by %s), hostname=%s",
		received.LastHeartbeatAt.UTC().Format(time.RFC3339Nano),
		received.LastHeartbeatAt.Sub(firstSeen).Round(time.Second),
		second.CurrentHostname)
}

// awaitHeartbeatAfter blocks until an operator.heartbeat.received event for
// operatorID carries a last_heartbeat_at strictly after since, or fails the test
// after the bounded wait. Events for other Operators and earlier heartbeats are
// skipped.
func awaitHeartbeatAfter(ctx context.Context, t *testing.T, w *e2eEventWaiters, operatorID string, since time.Time) models.OperatorHeartbeatReceivedPayload {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, e2eEventWaitTimeout)
	defer cancel()
	for {
		select {
		case p := <-w.heartbeat:
			if p.OperatorID == operatorID && p.LastHeartbeatAt.After(since) {
				return p
			}
		case <-waitCtx.Done():
			t.Fatalf("timed out after %s waiting for a heartbeat event for operator %s after %s — pub/sub heartbeat path may be dead: %v",
				e2eEventWaitTimeout, operatorID, since.UTC().Format(time.RFC3339Nano), waitCtx.Err())
		}
	}
}

// activeOperator fetches the operator list and returns a pointer to the first
// active remote operator. It fails the test if no active remote operator is found. The
// caller owns the context; this helper does not call require.Eventually or
// introduce its own polling — it is a single typed observation.
func activeOperator(t *testing.T, ctx context.Context) *operatorv1.OperatorDocument {
	t.Helper()
	operators, err := e2eClient.ListOperators(ctx)
	require.NoError(t, err, "operator list must succeed for heartbeat observation")
	require.True(t, operators.Success, "operator list response must report success")
	require.NotEmpty(t, operators.Operators, "at least one operator must be registered")
	operator := findLiveActiveRemoteOperator(operators.Operators)
	require.NotNil(t, operator, "no active remote operator found in registry — heartbeat test requires an approved stack with a live operator")
	return operator
}

func findActiveRemoteOperator(operators []*operatorv1.OperatorDocument) *operatorv1.OperatorDocument {
	return findLiveActiveRemoteOperator(operators)
}
