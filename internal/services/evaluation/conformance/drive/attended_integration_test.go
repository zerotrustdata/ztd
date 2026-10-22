// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// findCase returns the one catalogue case that matches row, ingress and posture.
func findCase(t *testing.T, all []cases.Case, row int, ingress cases.Ingress, posture evalv1.EvaluationGovernancePosture) cases.Case {
	t.Helper()
	for _, c := range all {
		if c.Row == row && c.Ingress == ingress && c.Posture == posture {
			return c
		}
	}
	t.Fatalf("catalogue has no row %d %s case under %s", row, ingress, posture)
	return cases.Case{}
}

// approvingDriver drives the inner call and, when the Gateway suspends it, runs the
// human decision the case asks for before returning. The attended driver has already
// opened its approval subscription before this inner drive starts, so the decision is
// synchronous and the Gateway's approval.completed event is delivered live to that
// subscription. The test goroutine stays the owner of every require call.
type approvingDriver struct {
	inner  cases.Driver
	decide func(txHash string)
}

func (d approvingDriver) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	driven, err := d.inner.Drive(ctx, a)
	if err != nil {
		return cases.Driven{}, err
	}
	if len(driven.Observations) == 1 && driven.Observations[0].GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED {
		d.decide(driven.Observations[0].GetSuspendedTxHash())
	}
	return driven, nil
}

// collectRequests is a request sink that keeps every request it receives.
func collectRequests() (*[]*evalv1.ConformanceApprovalRequest, RequestSink) {
	var got []*evalv1.ConformanceApprovalRequest
	return &got, func(r *evalv1.ConformanceApprovalRequest) { got = append(got, r) }
}

// assertRequests requires exactly one request: a's case asking for decision on txHash.
func assertRequests(t *testing.T, got []*evalv1.ConformanceApprovalRequest, a cases.Attempt, decision evalv1.ConformanceApprovalDecision, txHash string) {
	t.Helper()
	require.Len(t, got, 1, "one suspending case is one approval request")
	assert.Equal(t, a.Case.ID, got[0].GetCaseId())
	assert.Equal(t, a.AttemptID, got[0].GetAttemptId())
	assert.Equal(t, string(a.Case.Ingress), got[0].GetIngress())
	assert.Equal(t, decision, got[0].GetDecision())
	assert.Equal(t, txHash, got[0].GetTxHash())
}

// TestAttendedRatifyApprovalResumesMCPCall drives row 23 on MCP under ratify. The call
// suspends, the test approves it with its passkey, and the attended driver returns the
// resumed signed event receipt with the marker written. Its approval request names the
// suspended transaction.
func TestAttendedRatifyApprovalResumesMCPCall(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	row23 := findCase(t, all, 23, cases.IngressMCP, ratify)

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-attended-mcp", Posture: constants.PostureRatify})
	mcpDriver, err := NewMCP(context.Background(), g.client, g.persona)
	require.NoError(t, err)

	// The approver is the human: the passkey session of the same identity over the CLI mTLS client.
	approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)
	session := fixtures.ConsoleSignIn(t, g.f, approver, g.ident)
	var approvedTxHash string
	approving := approvingDriver{inner: mcpDriver, decide: func(txHash string) {
		approvedTxHash = txHash
		status, body := fixtures.ApproveWithPasskey(t, g.f, approver, session, g.ident, txHash)
		require.Equal(t, http.StatusOK, status, "passkey approval must resume the call: %s", body)
	}}

	a := gatewayAttempt(t, g.f, row23)
	requests, sink := collectRequests()
	driven, err := NewAttended(approving, g.client, g.persona, g.ident.UserID, sink).Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	require.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_EVENT_RECEIPT, obs.GetKind(), "answer kind: %s", obs.GetKind())
	assertRequests(t, *requests, a, evalv1.ConformanceApprovalDecision_CONFORMANCE_APPROVAL_DECISION_APPROVE, approvedTxHash)

	eventReceipt := obs.GetEventReceipt()
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, eventReceipt.GetStatus())
	assert.Equal(t, approvedTxHash, eventReceipt.GetTransactionHash(), "the resumed event receipt must be for the suspended transaction")
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, eventReceipt.GetL3Status(), "the approved call must carry a valid L3 proof")
	assert.Equal(t, row23.Expect[0].L2Status, eventReceipt.GetL2Status())

	written, err := os.ReadFile(a.TargetAbs)
	require.NoError(t, err)
	assert.Equal(t, a.Marker, string(written), "the approved mutation must write exactly the marker")
}

// TestAttendedRatifyDenialResumesMCPAsSignedRejection drives row 24 on MCP under ratify.
// The call suspends, the test denies it the way `ztd auth approve --deny` does, and the
// attended driver returns the Operator-signed L3_DENIED rejection with nothing written.
func TestAttendedRatifyDenialResumesMCPAsSignedRejection(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	row24 := findCase(t, all, 24, cases.IngressMCP, ratify)

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-attended-mcp-deny", Posture: constants.PostureRatify})
	mcpDriver, err := NewMCP(context.Background(), g.client, g.persona)
	require.NoError(t, err)

	approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)
	var deniedTxHash string
	denying := approvingDriver{inner: mcpDriver, decide: func(txHash string) {
		deniedTxHash = txHash
		g.denyWithCLI(t, approver, txHash)
	}}

	a := gatewayAttempt(t, g.f, row24)
	driven, err := NewAttended(denying, g.client, g.persona, g.ident.UserID, nil).Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	require.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, obs.GetKind(), "answer kind: %s", obs.GetKind())
	assert.Nil(t, obs.GetEventReceipt(), "a deny is never reported as an executed event receipt")

	rejection := obs.GetRejection().GetEventReceipt()
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, rejection.GetRejectionCode())
	assert.Equal(t, deniedTxHash, rejection.GetTransactionHash(), "the signed deny must be for the suspended transaction")
	assert.NotEmpty(t, rejection.GetSignature(), "the deny must carry the Operator's signature")

	_, statErr := os.Stat(a.TargetAbs)
	assert.True(t, os.IsNotExist(statErr), "a denied mutation must leave no file")
}

// pendingTransactions returns the hashes of the transactions the Gateway holds for a human
// decision, as the approver's CLI client lists them.
func (g *liveGateway) pendingTransactions(approver *http.Client) ([]string, error) {
	resp, err := approver.Get(network.LocalhostHTTPSURL(g.f.Service.GetHTTPSPort()) + constants.APIPaths.ApprovalsCLIList)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pending approvals: status %d: %s", resp.StatusCode, body)
	}
	var pending models.SuspendedTransactionsResponse
	if err := json.Unmarshal(body, &pending); err != nil {
		return nil, fmt.Errorf("pending approvals: decode: %w", err)
	}
	hashes := make([]string, 0, len(pending.Transactions))
	for _, tx := range pending.Transactions {
		hashes = append(hashes, tx.TransactionHash)
	}
	return hashes, nil
}

// watchPendingApprovals opens the approver's approval stream. Open it before the blocked
// call is submitted, so the Gateway's announcement for the suspension cannot be missed.
func (g *liveGateway) watchPendingApprovals(t *testing.T) *auth.ApprovalSubscription {
	t.Helper()
	watch, err := g.client.SubscribeApprovals(t.Context(), g.persona)
	require.NoError(t, err)
	t.Cleanup(watch.Close)
	return watch
}

// awaitPendingApproval waits for the one transaction the blocked call suspended and
// returns its hash. The Gateway announces the suspension on the approver's stream as
// ztd.v1.platform.approvals.changed (subject transactions). That announcement names no
// transaction, so the helper wakes on it and re-lists the pending transactions once,
// the way the console and the TUI do. Nothing polls.
func (g *liveGateway) awaitPendingApproval(t *testing.T, approver *http.Client, watch *auth.ApprovalSubscription) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.NoError(t, watch.WaitTransactionsChanged(ctx), "the blocked call must announce its suspended transaction")
	hashes, err := g.pendingTransactions(approver)
	require.NoError(t, err)
	require.Len(t, hashes, 1, "the blocked call must suspend one transaction for the human")
	return hashes[0]
}

// denyWithCLI denies txHash the way `ztd auth approve --deny` does.
func (g *liveGateway) denyWithCLI(t *testing.T, approver *http.Client, txHash string) {
	t.Helper()
	body, err := json.Marshal(models.ApprovalRespondRequest{ApprovalID: txHash, Approved: false, Reason: "conformance deny"})
	require.NoError(t, err)
	resp, err := approver.Post(network.LocalhostHTTPSURL(g.f.Service.GetHTTPSPort())+constants.APIPaths.ApprovalsRespond, constants.HeaderValueApplicationJSON, bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "deny must return the signed rejection: %s", answer)
}

// TestAttendedRatifyBlockingIngressesFollowTheHumanDecision drives rows 23 and 24 on
// governed dispatch and the CLI under ratify against a live Gateway. The request blocks
// while the transaction is pending, the human decides over the Gateway's approval routes,
// and the attended driver returns the signed answer the blocked request got: the executed
// event receipt on approve, the L3_DENIED rejection on deny.
func TestAttendedRatifyBlockingIngressesFollowTheHumanDecision(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY

	for _, ingress := range []cases.Ingress{cases.IngressDispatchEmbedded, cases.IngressCLI} {
		for _, row := range []int{23, 24} {
			c := findCase(t, all, row, ingress, ratify)
			t.Run(c.ID, func(t *testing.T) {
				g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-attended-blocking", Posture: constants.PostureRatify})
				approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)

				var inner cases.Driver
				var err error
				if ingress == cases.IngressCLI {
					// The production CLI client reads the identity from an installed CLI runtime.
					inner, err = NewCLI(cliPosterFor(t, g.f, g.ident), g.target, g.ident.CLISessionID)
				} else {
					inner, err = NewDispatch(g.client, g.persona, map[cases.Executor]operator.Target{cases.ExecutorEmbedded: g.target})
				}
				require.NoError(t, err)
				a := gatewayAttempt(t, g.f, c)

				type answer struct {
					driven cases.Driven
					err    error
				}
				watch := g.watchPendingApprovals(t)
				answered := make(chan answer, 1)
				go func() {
					driven, err := NewAttended(inner, g.client, g.persona, g.ident.UserID, nil).Drive(t.Context(), a)
					answered <- answer{driven, err}
				}()

				txHash := g.awaitPendingApproval(t, approver, watch)
				select {
				case early := <-answered:
					t.Fatalf("the call answered before the human decided: %+v, %v", early.driven, early.err)
				default:
				}
				_, statErr := os.Stat(a.TargetAbs)
				require.True(t, os.IsNotExist(statErr), "nothing may execute while the approval is pending")

				if row == 23 {
					session := fixtures.ConsoleSignIn(t, g.f, approver, g.ident)
					status, body := fixtures.ApproveWithPasskey(t, g.f, approver, session, g.ident, txHash)
					require.Equal(t, http.StatusOK, status, "passkey approval must resume the call: %s", body)
				} else {
					g.denyWithCLI(t, approver, txHash)
				}

				var got answer
				select {
				case got = <-answered:
				case <-time.After(30 * time.Second):
					t.Fatal("the call never answered after the human decided")
				}
				require.NoError(t, got.err)
				require.Len(t, got.driven.Observations, len(c.Expect))
				obs := got.driven.Observations[0]
				assertExpect(t, c.Expect[0], obs)

				if row == 23 {
					eventReceipt := obs.GetEventReceipt()
					assert.Equal(t, txHash, eventReceipt.GetTransactionHash(), "the event receipt must be for the suspended transaction")
					assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, eventReceipt.GetL3Status())
					written, err := os.ReadFile(a.TargetAbs)
					require.NoError(t, err)
					assert.Equal(t, a.Marker, string(written), "the approved mutation must write exactly the marker")
					return
				}
				assert.Equal(t, txHash, obs.GetRejection().GetEventReceipt().GetTransactionHash(), "the rejection must be for the suspended transaction")
				_, statErr = os.Stat(a.TargetAbs)
				assert.True(t, os.IsNotExist(statErr), "a denied mutation must leave no file")
			})
		}
	}
}

// TestAttendedRatifyA2AApprovalAttributesDownstreamCallToAttempt drives row 23 on A2A under
// ratify against a live Gateway whose A2A downstream is the conformance stub. The call
// suspends, the human approves, and the downstream call the Gateway makes to resume it is
// attributed to the attempt.
func TestAttendedRatifyA2AApprovalAttributesDownstreamCallToAttempt(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	row23 := findCase(t, all, 23, cases.IngressA2A, ratify)

	stub, err := StartA2AStub("127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, stub.Close(ctx))
	})

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName:         "conformance-attended-a2a",
		Posture:          constants.PostureRatify,
		A2ADownstreamURL: stub.URL(),
	})
	approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)
	session := fixtures.ConsoleSignIn(t, g.f, approver, g.ident)
	a2a := NewA2A(g.client, g.persona, stub)
	a := gatewayAttempt(t, g.f, row23)
	watch := g.watchPendingApprovals(t)

	type answer struct {
		driven cases.Driven
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		driven, err := NewAttended(a2a, g.client, g.persona, g.ident.UserID, nil).Drive(t.Context(), a)
		answered <- answer{driven, err}
	}()

	txHash := g.awaitPendingApproval(t, approver, watch)
	status, body := fixtures.ApproveWithPasskey(t, g.f, approver, session, g.ident, txHash)
	require.Equal(t, http.StatusOK, status, "passkey approval must resume the call: %s", body)

	var got answer
	select {
	case got = <-answered:
	case <-time.After(30 * time.Second):
		t.Fatal("the call never answered after the human approved")
	}
	require.NoError(t, got.err)
	require.Len(t, got.driven.Observations, 1)
	obs := got.driven.Observations[0]
	require.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_EVENT_RECEIPT, obs.GetKind(), "answer kind: %s", obs.GetKind())
	assert.Equal(t, txHash, obs.GetEventReceipt().GetTransactionHash(), "the event receipt must be for the suspended transaction")

	require.Len(t, got.driven.A2ACalls, 1, "the downstream call made to resume the approval must be attributed to the attempt")
	call := got.driven.A2ACalls[0]
	assert.Equal(t, ConformanceA2ASkill, call.GetSkillName())
	var payload cases.MarkerSkillPayload
	require.NoError(t, json.Unmarshal([]byte(call.GetPayloadJson()), &payload))
	assert.Equal(t, a.Marker, payload.Marker)
}

// TestL3DirectRowsUnderRatify drives row 20 on DIRECT under ratify: a missing L3 proof and
// an invalid L3 proof are each rejected by L4 before anything executes.
func TestL3DirectRowsUnderRatify(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-l3-ratify", Posture: constants.PostureRatify})
	direct, err := NewDirect(g.client, g.persona, g.target, nil, BaseMutators(), L3Mutators())
	require.NoError(t, err)

	found := 0
	for _, c := range all {
		if c.Row != 20 || c.Ingress != cases.IngressDirect || c.Posture != ratify {
			continue
		}
		found++
		t.Run(c.ID, func(t *testing.T) {
			driven, err := direct.Drive(context.Background(), gatewayAttempt(t, g.f, c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(c.Expect))
			for i, want := range c.Expect {
				assertExpect(t, want, driven.Observations[i])
			}
		})
	}
	require.Equal(t, 2, found, "catalogue must hold the row-20 missing and invalid DIRECT ratify cases")
}

// TestL3GarbageProofExecutesUnderDoctrine drives row 21 on DIRECT under doctrine: an
// invalid L3 proof is audited but does not gate, so the mutation executes with L3
// NOT_REQUIRED and the marker written.
func TestL3GarbageProofExecutesUnderDoctrine(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE
	c := findCase(t, all, 21, cases.IngressDirect, doctrine)

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-l3-doctrine", Posture: constants.PostureDoctrine})
	direct, err := NewDirect(g.client, g.persona, g.target, nil, BaseMutators(), L3Mutators())
	require.NoError(t, err)
	a := gatewayAttempt(t, g.f, c)

	driven, err := direct.Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, len(c.Expect))
	assertExpect(t, c.Expect[0], driven.Observations[0])
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, driven.Observations[0].GetEventReceipt().GetL3Status())

	written, err := os.ReadFile(a.TargetAbs)
	require.NoError(t, err)
	assert.Equal(t, a.Marker, string(written))
}

// attendedCase is an attended row-23 style attempt on MCP with the given fault.
func attendedCase(fault cases.Fault) cases.Attempt {
	a := testAttempt(fault, cases.ActionMutation)
	a.Case.Attended = true
	a.Case.Ingress = cases.IngressMCP
	return a
}

// unknownCLIPersona returns p with a CLI session the Gateway never issued, so the approval
// stream the persona asks for is refused.
func unknownCLIPersona(t *testing.T, p client.Persona) client.Persona {
	t.Helper()
	session, err := uuid.NewString()
	require.NoError(t, err)
	p.CLISessionID = session
	return p
}

// redirectStderr sends os.Stderr to a file for the rest of the test and returns its path.
// A file write returns only once the bytes are in the file, so the test can read what was
// printed before a call reached the Gateway.
func redirectStderr(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testutil.TempDir(t), "stderr.txt")
	file, err := os.Create(path)
	require.NoError(t, err)
	saved := os.Stderr
	os.Stderr = file
	t.Cleanup(func() {
		os.Stderr = saved
		assert.NoError(t, file.Close())
	})
	return path
}

// TestAttendedNonAttendedCasePassesThroughUntouched drives row 22 on MCP under ratify without
// the attended flag. The Gateway suspends the call and the attended driver returns that
// suspension as the inner driver gave it, reporting it as LEAVE_PENDING. The persona the driver would subscribe with names a
// CLI session the Gateway never issued, so an approval stream would fail the call: none is
// opened for an unattended case.
func TestAttendedNonAttendedCasePassesThroughUntouched(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	row22 := findCase(t, all, 22, cases.IngressMCP, ratify)
	require.False(t, row22.Attended, "row 22 is the unattended suspension")

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-attended-passthrough", Posture: constants.PostureRatify})
	mcpDriver, err := NewMCP(t.Context(), g.client, g.persona)
	require.NoError(t, err)
	approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)

	a := gatewayAttempt(t, g.f, row22)
	requests, sink := collectRequests()
	driven, err := NewAttended(mcpDriver, g.client, unknownCLIPersona(t, g.persona), g.ident.UserID, sink).Drive(t.Context(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	require.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED, obs.GetKind(), "answer kind: %s", obs.GetKind())
	assertRequests(t, *requests, a, evalv1.ConformanceApprovalDecision_CONFORMANCE_APPROVAL_DECISION_LEAVE_PENDING, obs.GetSuspendedTxHash())

	pending, err := g.pendingTransactions(approver)
	require.NoError(t, err)
	assert.Equal(t, []string{obs.GetSuspendedTxHash()}, pending, "the returned suspension must be the Gateway's pending transaction")
}

// TestAttendedSubscriptionRefusedSubmitsNothing drives row 23 on MCP under ratify with an
// approval stream the Gateway refuses: the persona names a CLI session it never issued. The
// attended driver returns the subscribe error without submitting the call, so the Gateway's
// approval queue holds nothing and no file is written.
func TestAttendedSubscriptionRefusedSubmitsNothing(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	row23 := findCase(t, all, 23, cases.IngressMCP, ratify)

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-attended-refused", Posture: constants.PostureRatify})
	mcpDriver, err := NewMCP(t.Context(), g.client, g.persona)
	require.NoError(t, err)
	approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)
	a := gatewayAttempt(t, g.f, row23)

	driven, err := NewAttended(mcpDriver, g.client, unknownCLIPersona(t, g.persona), g.ident.UserID, nil).Drive(t.Context(), a)
	require.Error(t, err)
	assert.ErrorContains(t, err, "subscribe for approval", "the error must name the failed subscription")
	assert.Empty(t, driven.Observations)

	pending, err := g.pendingTransactions(approver)
	require.NoError(t, err)
	assert.Empty(t, pending, "a call without an approval stream must never reach the approval queue")
	_, statErr := os.Stat(a.TargetAbs)
	assert.True(t, os.IsNotExist(statErr), "nothing may execute without an approval stream")
}

// TestAttendedNonSuspendedAnswerReturnsWithoutWaiting drives an attended MCP call under
// doctrine, where nothing is suspended for a human: row 1 executes and row 14 is rejected by
// L1. Each answer is returned as the Gateway gave it. A suspension would fail the test
// through the decide hook, so no wait for a human can happen.
func TestAttendedNonSuspendedAnswerReturnsWithoutWaiting(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE

	tests := []struct {
		name     string
		row      int
		executes bool
	}{
		{name: "event_receipt", row: 1, executes: true},
		{name: "rejection", row: 14, executes: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := findCase(t, all, tt.row, cases.IngressMCP, doctrine)
			c.Attended = true

			g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-attended-nonsuspended-" + tt.name, Posture: constants.PostureDoctrine})
			mcpDriver, err := NewMCP(t.Context(), g.client, g.persona)
			require.NoError(t, err)
			approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)
			notSuspended := approvingDriver{inner: mcpDriver, decide: func(txHash string) {
				t.Fatalf("a non-suspended answer must not wait for a human, but the call suspended %s", txHash)
			}}
			a := gatewayAttempt(t, g.f, c)

			driven, err := NewAttended(notSuspended, g.client, g.persona, g.ident.UserID, nil).Drive(t.Context(), a)
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(c.Expect))
			assertExpect(t, c.Expect[0], driven.Observations[0])

			pending, err := g.pendingTransactions(approver)
			require.NoError(t, err)
			assert.Empty(t, pending, "nothing may be held for a human under doctrine")
			_, statErr := os.Stat(a.TargetAbs)
			if tt.executes {
				written, err := os.ReadFile(a.TargetAbs)
				require.NoError(t, err)
				assert.Equal(t, a.Marker, string(written), "the executed mutation must write exactly the marker")
			} else {
				assert.True(t, os.IsNotExist(statErr), "an L1-rejected mutation must leave no file")
			}
		})
	}
}

// TestAttendedBlockingDispatchAnnouncesBeforeTheCallWithoutOpeningAStream drives row 23 on
// governed dispatch under ratify with an approval client whose stream endpoint is unreachable.
// The banner is printed before the blocking request reaches the Gateway, and the approval
// still resolves: a blocking ingress opens no approval stream, so the unreachable stream
// endpoint is never dialed.
func TestAttendedBlockingDispatchAnnouncesBeforeTheCallWithoutOpeningAStream(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	c := findCase(t, all, 23, cases.IngressDispatchEmbedded, ratify)

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-attended-banner", Posture: constants.PostureRatify})
	approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)
	noStream := liveClient(t, g.f, g.ident, network.LocalhostHTTPSURL(g.f.Service.GetHTTPSPort()), unreachableURL(t))
	inner, err := NewDispatch(g.client, g.persona, map[cases.Executor]operator.Target{cases.ExecutorEmbedded: g.target})
	require.NoError(t, err)
	a := gatewayAttempt(t, g.f, c)
	stderr := redirectStderr(t)
	watch := g.watchPendingApprovals(t)

	type answer struct {
		driven cases.Driven
		err    error
	}
	answered := make(chan answer, 1)
	requests, sink := collectRequests()
	go func() {
		driven, err := NewAttended(inner, noStream, g.persona, g.ident.UserID, sink).Drive(t.Context(), a)
		answered <- answer{driven, err}
	}()

	txHash := g.awaitPendingApproval(t, approver, watch)
	banner, err := os.ReadFile(stderr)
	require.NoError(t, err)
	assert.Contains(t, string(banner), "HUMAN APPROVAL REQUIRED", "the banner must be printed before the blocking request reaches the Gateway")

	session := fixtures.ConsoleSignIn(t, g.f, approver, g.ident)
	status, body := fixtures.ApproveWithPasskey(t, g.f, approver, session, g.ident, txHash)
	require.Equal(t, http.StatusOK, status, "passkey approval must resume the call: %s", body)

	var got answer
	select {
	case got = <-answered:
	case <-time.After(30 * time.Second):
		t.Fatal("the call never answered after the human approved")
	}
	require.NoError(t, got.err, "a blocking ingress must not dial the approval stream")
	require.Len(t, got.driven.Observations, len(c.Expect))
	assertExpect(t, c.Expect[0], got.driven.Observations[0])
	assertRequests(t, *requests, a, evalv1.ConformanceApprovalDecision_CONFORMANCE_APPROVAL_DECISION_APPROVE, "")
	written, err := os.ReadFile(a.TargetAbs)
	require.NoError(t, err)
	assert.Equal(t, a.Marker, string(written), "the approved mutation must write exactly the marker")
}

// TestAttendedInnerTransportErrorIsWrappedWithItsCause drives a blocking governed dispatch
// whose inner call cannot reach the Gateway. The attended driver returns the transport failure
// with its cause and no observation.
func TestAttendedInnerTransportErrorIsWrappedWithItsCause(t *testing.T) {
	a := attendedCase(cases.FaultAttendedApprove)
	a.Case.Ingress = cases.IngressDispatchEmbedded
	inner, err := NewDispatch(unreachableClient(t), testIdentity().Persona, testDispatchTargets())
	require.NoError(t, err)

	driven, err := NewAttended(inner, unreachableClient(t), testIdentity().Persona, "user-approver", nil).Drive(t.Context(), a)
	require.Error(t, err)
	var urlErr *url.Error
	assert.ErrorAs(t, err, &urlErr, "the transport failure must keep its cause")
	assert.ErrorContains(t, err, "attended "+string(cases.FaultAttendedApprove)+": drive:")
	assert.Empty(t, driven.Observations)
}

// TestAttendedCancelledWaitReturnsWrappedErrorWithNoObservation drives row 23 on MCP under
// ratify and cancels the call's context once the Gateway suspends it, before any human decides.
// The wait fails with its cause and no observation is returned. The cause is whichever ends the
// wait first: the cancelled context, the wait timeout, or the closed stream.
func TestAttendedCancelledWaitReturnsWrappedErrorWithNoObservation(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	row23 := findCase(t, all, 23, cases.IngressMCP, ratify)

	g := bootLiveGateway(t, fixtures.GatewayFixtureOptions{TestName: "conformance-attended-cancelled", Posture: constants.PostureRatify})
	mcpDriver, err := NewMCP(t.Context(), g.client, g.persona)
	require.NoError(t, err)
	a := gatewayAttempt(t, g.f, row23)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cancelling := approvingDriver{inner: mcpDriver, decide: func(string) { cancel() }}

	driven, err := NewAttended(cancelling, g.client, g.persona, g.ident.UserID, nil).Drive(ctx, a)
	require.Error(t, err)
	assert.ErrorContains(t, err, "wait for approval")
	assert.Empty(t, driven.Observations)
	assert.True(t,
		errors.Is(err, context.Canceled) || errors.Is(err, constants.ErrApprovalSSETimeout) || errors.Is(err, constants.ErrSSEStreamClosed),
		"the wait must keep its cause: %v", err)
}
