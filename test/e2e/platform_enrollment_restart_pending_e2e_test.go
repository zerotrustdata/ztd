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

// TestPlatformEnrollment_RestartDuringPending verifies the restart contract for an
// Operator whose enrollment is pending. Operator identity, keys, and the requester
// token are memory-only, so a restarted Operator does not resume its previous
// request. It files a new request with a new request ID. The Gateway does not
// withdraw the orphaned request when the bootstrap socket drops; it stays pending
// until it expires, and the test does not approve it.
//
// The user starts the full stack (gateway and operators) with the owner bootstrapped
// and no approvals, waits for the operator's pending request to appear, restarts
// the operator process (stop it, then ./ztd operator start), then runs:
//
//	./ztd test e2e --run TestPlatformEnrollment_RestartDuringPending
//
// The test waits for the newest pending operator request, approves it, and
// verifies the operator becomes active and a command roundtrip succeeds.
func TestPlatformEnrollment_RestartDuringPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Subscribe before any wait. Each wait reads state once, then blocks on the
	// next event, so an event that fired before the subscription is covered by
	// the initial read.
	events := newE2EEventWaiters()
	stream := subscribeE2EEvents(ctx, t, events)
	defer stream.stop()

	// The restarted Operator's new request is the newest pending operator request.
	// Pre-restart requests are orphans and are not approved here.
	var operatorReq *models.PlatformEnrollmentPendingRequest
	for operatorReq == nil {
		pending, err := e2eClient.GetPendingEnrollments(ctx)
		require.NoError(t, err, "pending enrollment list must succeed")
		operatorReq = newestPendingOperatorRequest(pending.Requests)
		if operatorReq == nil {
			events.awaitPending(ctx, t,
				"the restarted operator's pending enrollment request; the stack must have no approvals and the operator must be restarted (stop it, then ./ztd operator start) before running TestPlatformEnrollment_RestartDuringPending")
		}
	}
	require.NotEmpty(t, operatorReq.RequestID, "restarted operator's request must have a non-empty request ID")
	t.Logf("discovered restarted operator request: %s", operatorReq.RequestID)

	require.NoError(t, e2eClient.ApproveEnrollment(ctx, operatorReq.RequestID),
		"approving restarted operator enrollment request %s must succeed", operatorReq.RequestID)
	t.Logf("approved restarted operator enrollment request %s", operatorReq.RequestID)

	// Approval issues a fresh identity. Read the registry once per event until the
	// restarted Operator is active under its new session.
	var active *operatorv1.OperatorDocument
	for active == nil {
		operators, err := e2eClient.ListOperators(ctx)
		require.NoError(t, err, "operator list must succeed after approval")
		active = findLiveActiveRemoteOperator(operators.Operators)
		if active == nil {
			events.awaitStatus(ctx, t, "an active operator after approving the restarted request")
		}
	}
	require.NotEmpty(t, active.OperatorSessionId, "active operator must have a session ID")
	t.Logf("operator became active after restart-during-pending approval: session=%s", active.OperatorSessionId)

	// A command roundtrip must succeed against the newly issued session.
	resp := e2eClient.dispatchFsRead(t, ctx)
	assert.NotEmpty(t, resp.GetTransactionId(), "dispatch response must carry a transaction ID")
	assert.Equal(t, string(constants.Event.Operator.FsRead.Completed), resp.GetEventType(),
		"dispatch response event type must be the fs.read completed event")
	t.Logf("command roundtrip succeeded after restart-during-pending: txn=%s",
		resp.GetTransactionId())
}
