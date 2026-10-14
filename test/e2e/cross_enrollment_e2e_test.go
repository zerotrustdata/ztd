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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// crossEnrollmentHostname is the stable substring used to identify the
// secondary gateway's pending operator request and active operator
// document. The secondary gateway process runs in operator mode
// (`operator start -e ztd.local`) under a hostname containing
// "gateway-secondary", so its operator enrollment request carries that
// substring. Filtering on it avoids matching the primary operator's
// enrollment request.
const crossEnrollmentHostname = "gateway-secondary"

// forbiddenPendingSecrets is the list of secret-bearing strings that must
// never appear in the raw pending-enrollment JSON. It mirrors the list in
// TestPlatformEnrollment_PendingDiscovery and is reused here so the
// cross-enrollment pending request is held to the same no-leak standard.
var forbiddenPendingSecrets = []string{
	"token_hash",
	"csr_pem",
	"operator_csr_pem",
	"cli_csr_pem",
	"BEGIN CERTIFICATE",
	"END CERTIFICATE",
	"BEGIN CERTIFICATE REQUEST",
	"END CERTIFICATE REQUEST",
	"BEGIN PRIVATE KEY",
	"END PRIVATE KEY",
	"BEGIN EC PRIVATE KEY",
	"END EC PRIVATE KEY",
	"BEGIN RSA PRIVATE KEY",
	"END RSA PRIVATE KEY",
	"requester_token",
}

// TestCrossEnrollment_GatewayAsOperator_PendingDiscovery verifies that when
// the secondary gateway process starts in operator mode against the
// primary gateway, a pending operator enrollment request appears in the
// primary gateway's authenticated pending list. The request is
// indistinguishable in shape from a standalone operator request: it carries
// ComponentKind == PlatformComponentOperator, a non-empty request ID, and
// the pending state. The raw pending JSON must not leak secret material.
//
// Preconditions (documented for the operator running the test):
//   - Primary gateway and owner bootstrapped, with a secondary gateway
//     process started in operator mode against the primary
//   - Owner bootstrapped on the primary gateway:
//     ./ztd auth enroll user --headless
//   - No enrollments approved yet.
//
// Run via:
//
//	./ztd test e2e --run TestCrossEnrollment_GatewayAsOperator_PendingDiscovery
func TestCrossEnrollment_GatewayAsOperator_PendingDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Subscribe before the first read so no event after it is missed; each wait
	// reads state once, then waits for the next event.
	events := newE2EEventWaiters()
	stream := subscribeE2EEvents(ctx, t, events)
	defer stream.stop()

	// Discover the secondary gateway's pending operator request. The helper
	// reads the pending list, then waits for approvals.changed events until a
	// matching request appears. A request that never appears means the
	// secondary gateway was not started in operator mode or did not start; the
	// wait fails after the bounded event wait.
	req := e2eClient.DiscoverPendingOperatorByHostname(t, ctx, events, crossEnrollmentHostname)
	require.NotEmpty(t, req.RequestID, "secondary gateway pending request must have a non-empty request ID")
	assert.Equal(t, models.PlatformComponentOperator, req.ComponentKind,
		"secondary gateway request must be an operator enrollment request")
	assert.Equal(t, models.PlatformEnrollmentStatePending, req.State,
		"secondary gateway request must be in the pending state")
	t.Logf("discovered secondary gateway pending operator request: id=%s instance=%s hostname=%s",
		req.RequestID, req.InstanceID, req.Hostname)

	// Assert the raw pending JSON excludes secret material. The typed
	// PlatformEnrollmentPendingRequest model omits secrets by
	// construction, but the raw wire payload is checked directly as
	// defense-in-depth: a future field addition that leaks secret
	// material would not be caught by the typed decode alone.
	raw, err := e2eClient.GetPendingRaw(ctx)
	require.NoError(t, err, "raw pending list fetch must succeed for secret-leak assertion")
	for _, forbidden := range forbiddenPendingSecrets {
		assert.NotContains(t, raw, forbidden,
			"pending list raw JSON must not expose %q", forbidden)
	}
	t.Logf("cross-enrollment pending request shape matches PlatformEnrollmentPendingRequest with no secret fields in raw JSON")
}

// TestCrossEnrollment_GatewayAsOperator_ApproveAndActivate verifies that
// approving the secondary gateway's enrollment request causes it to
// complete enrollment, receive certificates, and register as an active
// operator on the primary gateway. A governed FS_READ command roundtrip
// dispatched to the secondary gateway's operator succeeds, proving the
// full L4/L5 verification and execution chain works for a gateway-as-operator.
//
// Preconditions (documented for the operator running the test):
//   - Secondary gateway in operator mode running against an owner-bootstrapped primary.
//   - The secondary gateway's enrollment request is pending and not yet
//     approved.
//
// Run via:
//
//	./ztd test e2e --run TestCrossEnrollment_GatewayAsOperator_ApproveAndActivate
func TestCrossEnrollment_GatewayAsOperator_ApproveAndActivate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Subscribe before the first read so no event after it is missed; each wait
	// reads state once, then waits for the next event.
	events := newE2EEventWaiters()
	stream := subscribeE2EEvents(ctx, t, events)
	defer stream.stop()

	// Discover and approve the secondary gateway's pending operator
	// request. Approval triggers credential issuance; the secondary
	// gateway's in-process operator completes enrollment and registers.
	req := e2eClient.DiscoverPendingOperatorByHostname(t, ctx, events, crossEnrollmentHostname)
	require.NotEmpty(t, req.RequestID, "secondary gateway pending request must have a non-empty request ID")
	t.Logf("discovered secondary gateway pending operator request: id=%s", req.RequestID)

	require.NoError(t, e2eClient.ApproveEnrollment(ctx, req.RequestID),
		"approving secondary gateway enrollment request %s must succeed", req.RequestID)
	t.Logf("approved secondary gateway enrollment request %s", req.RequestID)

	// The secondary gateway's operator must become active in the registry. The
	// helper reads ListOperators, then waits for operator.status.updated events,
	// and returns the first active operator whose Name contains the secondary
	// gateway hostname substring. Active means its command subscription is
	// established.
	active := e2eClient.DiscoverActiveOperatorByName(t, ctx, events, crossEnrollmentHostname)
	require.NotEmpty(t, active.OperatorSessionId,
		"active secondary gateway operator must have a session ID")
	assert.Equal(t, string(constants.OperatorStatusActive), active.Status,
		"secondary gateway operator must be in the active state")
	t.Logf("secondary gateway became active operator: id=%s session=%s name=%s",
		active.Id, active.OperatorSessionId, active.Name)

	// Dispatch an FS_READ command to the secondary gateway's operator
	// specifically (by session ID), not to whichever operator happens to
	// be first in the list. This proves the gateway-as-operator executes
	// governed commands through the full L4/L5 chain.
	resp := dispatchFsReadToOperator(t, ctx, active.OperatorSessionId)
	assert.NotEmpty(t, resp.GetTransactionId(), "dispatch response must carry a transaction ID")
	assert.Equal(t, string(constants.Event.Operator.FsRead.Completed), resp.GetEventType(),
		"dispatch response event type must be the fs.read completed event")
	assert.NotEmpty(t, resp.GetResultPayload(), "dispatch response must carry the operator result payload")

	// Decode the result payload as FsReadResult and assert the operator
	// read /etc/hostname successfully. The content is the file's bytes
	// inside the secondary gateway process — proving the command
	// executed on the gateway-as-operator, not in the primary gateway
	// process or the primary operator process.
	var fsReadResult operatorv1.FsReadResult
	require.NoError(t, proto.Unmarshal(resp.GetResultPayload(), &fsReadResult),
		"unmarshal FsReadResult from response payload")
	assert.Equal(t, constants.PathEtcHostname, fsReadResult.Path,
		"result path must match the requested path")
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, fsReadResult.Status,
		"fs.read must complete successfully on the gateway-as-operator")
	assert.NotEmpty(t, fsReadResult.Content, "fs.read result content must not be empty")
	assert.Greater(t, fsReadResult.SizeBytes, int64(0), "fs.read result size must be positive")
	t.Logf("command roundtrip succeeded against gateway-as-operator: txn=%s content_size=%d",
		resp.GetTransactionId(), fsReadResult.SizeBytes)
}

// TestCrossEnrollment_GatewayAsOperator_Denial verifies that denying the
// secondary gateway's enrollment request moves it to terminal state,
// removes it from the pending list, leaves the gateway healthy, and
// produces no active operator from the secondary gateway. This mirrors
// TestPlatformEnrollment_Denial for the cross-enrollment path.
//
// Preconditions (documented for the operator running the test):
//   - Fresh stack with a secondary gateway in operator mode and owner
//     bootstrapped, with the secondary gateway pending and no approvals.
//
// Run via:
//
//	./ztd test e2e --run TestCrossEnrollment_GatewayAsOperator_Denial
func TestCrossEnrollment_GatewayAsOperator_Denial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Subscribe before the first read so no event after it is missed; each wait
	// reads state once, then waits for the next event.
	events := newE2EEventWaiters()
	stream := subscribeE2EEvents(ctx, t, events)
	defer stream.stop()

	// Discover and deny the secondary gateway's pending operator
	// request. The decision endpoint is owner-authenticated and records
	// the denial as a terminal state transition.
	req := e2eClient.DiscoverPendingOperatorByHostname(t, ctx, events, crossEnrollmentHostname)
	require.NotEmpty(t, req.RequestID, "secondary gateway pending request must have a non-empty request ID")
	t.Logf("discovered secondary gateway pending operator request: id=%s", req.RequestID)

	require.NoError(t, e2eClient.DenyEnrollment(ctx, req.RequestID),
		"denying secondary gateway enrollment request %s must succeed", req.RequestID)
	t.Logf("denied secondary gateway enrollment request %s", req.RequestID)

	// The denied request must leave the pending list. It is now in the
	// denied terminal state, not pending.
	awaitRequestLeavesPending(ctx, t, events, req.RequestID)
	t.Logf("denied secondary gateway request %s is no longer pending", req.RequestID)

	// The gateway must remain healthy after the denial. A denial is a
	// routine owner decision; it must not destabilize the gateway.
	health, err := e2eClient.GetHealth(ctx, e2eCfg.gatewayHTTPURL)
	require.NoError(t, err, "gateway health must succeed after denial")
	assert.Equal(t, constants.GatewayModeStatusOK, health.Status,
		"gateway must remain healthy after denying a cross-enrollment request")
	t.Logf("gateway remains healthy after denial: status=%s", health.Status)

	// No active operator matching the secondary gateway must appear in
	// the registry. A denied enrollment never issues operator
	// credentials, so the gateway-as-operator cannot register.
	operators, err := e2eClient.ListOperators(ctx)
	require.NoError(t, err, "operator list must succeed after denial")
	for _, op := range operators.Operators {
		if op.Status != string(constants.OperatorStatusActive) {
			continue
		}
		assert.NotContains(t, op.Name, crossEnrollmentHostname,
			"no active operator matching the secondary gateway must exist after denial (name=%s, session=%s)",
			op.Name, op.OperatorSessionId)
	}
	t.Logf("no active operator from the secondary gateway after denial (%d total operators)",
		len(operators.Operators))
}

// TestCrossEnrollment_GatewayAsOperator_RestartDuringPending verifies that
// restarting the secondary gateway process while its enrollment is
// pending resumes the same request ID rather than creating a duplicate.
// This mirrors TestPlatformEnrollment_RestartDuringPending for the
// cross-enrollment path.
//
// Preconditions (documented for the operator running the test):
//   - Fresh stack with a secondary gateway in operator mode and owner
//     bootstrapped, with the secondary gateway pending and no approvals.
//   - The user has restarted the secondary gateway process before running the
//     test:
//     stop the secondary gateway process, then start it again in operator mode
//
// Run via:
//
//	./ztd test e2e --run TestCrossEnrollment_GatewayAsOperator_RestartDuringPending
func TestCrossEnrollment_GatewayAsOperator_RestartDuringPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Subscribe before the first read so no event after it is missed; each wait
	// reads state once, then waits for the next event.
	events := newE2EEventWaiters()
	stream := subscribeE2EEvents(ctx, t, events)
	defer stream.stop()

	// Discover the secondary gateway's pending operator request after
	// the restart. The helper polls GetPendingEnrollments and returns the
	// first matching pending operator request.
	req := e2eClient.DiscoverPendingOperatorByHostname(t, ctx, events, crossEnrollmentHostname)
	require.NotEmpty(t, req.RequestID,
		"resumed secondary gateway request must have a non-empty request ID")
	t.Logf("discovered resumed secondary gateway operator request: %s", req.RequestID)

	// Assert exactly one pending operator request from the secondary
	// gateway exists (no duplicate from restart). A duplicate would
	// indicate the secondary gateway lost its persisted pending state and
	// re-submitted, breaking request-ID continuity.
	pending, err := e2eClient.GetPendingEnrollments(ctx)
	require.NoError(t, err, "pending enrollment list must succeed for duplicate check")
	matchCount := 0
	for _, r := range pending.Requests {
		if r.ComponentKind != models.PlatformComponentOperator {
			continue
		}
		if strings.Contains(r.Hostname, crossEnrollmentHostname) || strings.Contains(r.InstanceID, crossEnrollmentHostname) {
			matchCount++
		}
	}
	assert.Equal(t, 1, matchCount,
		"exactly one pending operator request from the secondary gateway must exist after restart (found %d)",
		matchCount)
	t.Logf("request-ID continuity confirmed: %d pending operator request(s) from the secondary gateway", matchCount)

	// Approve the resumed request and verify the operator becomes active.
	require.NoError(t, e2eClient.ApproveEnrollment(ctx, req.RequestID),
		"approving resumed secondary gateway enrollment request %s must succeed", req.RequestID)
	t.Logf("approved resumed secondary gateway enrollment request %s", req.RequestID)

	active := e2eClient.DiscoverActiveOperatorByName(t, ctx, events, crossEnrollmentHostname)
	require.NotEmpty(t, active.OperatorSessionId,
		"active secondary gateway operator must have a session ID after restart-during-pending approval")
	assert.Equal(t, string(constants.OperatorStatusActive), active.Status,
		"secondary gateway operator must be active after restart-during-pending approval")
	t.Logf("secondary gateway became active after restart-during-pending approval: session=%s",
		active.OperatorSessionId)

	// A command roundtrip must succeed against the now-active
	// gateway-as-operator. This proves the full L4/L5 verification and
	// execution chain works after restart-during-pending enrollment.
	resp := dispatchFsReadToOperator(t, ctx, active.OperatorSessionId)
	assert.NotEmpty(t, resp.GetTransactionId(), "dispatch response must carry a transaction ID")
	assert.Equal(t, string(constants.Event.Operator.FsRead.Completed), resp.GetEventType(),
		"dispatch response event type must be the fs.read completed event")
	t.Logf("command roundtrip succeeded after restart-during-pending: txn=%s",
		resp.GetTransactionId())
}

// dispatchFsReadToOperator dispatches one FS_READ command for /etc/hostname to a
// specific operator by session ID. Unlike the shared dispatchFsRead helper
// (which targets the live active remote operator), this targets a named operator
// so cross-enrollment tests prove the gateway-as-operator specifically executes
// the command. It is not retried: the operator was discovered active, so its
// command subscription is established.
func dispatchFsReadToOperator(t *testing.T, ctx context.Context, operatorSessionID string) *operatorv1.GovernedDispatchResult {
	t.Helper()
	return e2eClient.dispatchFsReadToSession(t, ctx, operatorSessionID)
}
