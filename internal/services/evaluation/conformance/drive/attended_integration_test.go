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
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/network"
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

// TestAttendedRatifyApprovalResumesMCPCall drives row 23 on MCP under ratify. The call
// suspends, the test approves it with its passkey, and the attended driver returns the
// resumed signed receipt with the marker written.
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
	driven, err := NewAttended(approving, g.client, g.persona, g.ident.UserID).Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	require.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, obs.GetKind(), "answer kind: %s", obs.GetKind())

	receipt := obs.GetReceipt()
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
	assert.Equal(t, approvedTxHash, receipt.GetTransactionHash(), "the resumed receipt must be for the suspended transaction")
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, receipt.GetL3Status(), "the approved call must carry a valid L3 proof")
	assert.Equal(t, row23.Expect[0].L2Status, receipt.GetL2Status())

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
	driven, err := NewAttended(denying, g.client, g.persona, g.ident.UserID).Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	require.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, obs.GetKind(), "answer kind: %s", obs.GetKind())
	assert.Nil(t, obs.GetReceipt(), "a deny is never reported as an executed receipt")

	rejection := obs.GetRejection().GetReceipt()
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, rejection.GetRejectionCode())
	assert.Equal(t, deniedTxHash, rejection.GetTransactionHash(), "the signed deny must be for the suspended transaction")
	assert.NotEmpty(t, rejection.GetSignature(), "the deny must carry the Operator's signature")

	_, statErr := os.Stat(a.TargetAbs)
	assert.True(t, os.IsNotExist(statErr), "a denied mutation must leave no file")
}

// awaitPendingApproval waits for the one transaction the blocked call suspended and
// returns its hash. The approver learns of it the way a human does, from the Gateway's
// pending approval list.
func (g *liveGateway) awaitPendingApproval(t *testing.T, approver *http.Client) string {
	t.Helper()
	listURL := network.LocalhostHTTPSURL(g.f.Service.GetHTTPSPort()) + constants.APIPaths.ApprovalsCLIList
	var txHash string
	require.Eventually(t, func() bool {
		resp, err := approver.Get(listURL)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			return false
		}
		var pending models.SuspendedTransactionsResponse
		if err := json.Unmarshal(body, &pending); err != nil || len(pending.Transactions) != 1 {
			return false
		}
		txHash = pending.Transactions[0].TransactionHash
		return true
	}, 10*time.Second, 25*time.Millisecond, "the blocked call must suspend one transaction for the human")
	return txHash
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
// receipt on approve, the L3_DENIED rejection on deny.
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
				answered := make(chan answer, 1)
				go func() {
					driven, err := NewAttended(inner, g.client, g.persona, g.ident.UserID).Drive(t.Context(), a)
					answered <- answer{driven, err}
				}()

				txHash := g.awaitPendingApproval(t, approver)
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
					receipt := obs.GetReceipt()
					assert.Equal(t, txHash, receipt.GetTransactionHash(), "the receipt must be for the suspended transaction")
					assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, receipt.GetL3Status())
					written, err := os.ReadFile(a.TargetAbs)
					require.NoError(t, err)
					assert.Equal(t, a.Marker, string(written), "the approved mutation must write exactly the marker")
					return
				}
				assert.Equal(t, txHash, obs.GetRejection().GetReceipt().GetTransactionHash(), "the rejection must be for the suspended transaction")
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

	type answer struct {
		driven cases.Driven
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		driven, err := NewAttended(a2a, g.client, g.persona, g.ident.UserID).Drive(t.Context(), a)
		answered <- answer{driven, err}
	}()

	txHash := g.awaitPendingApproval(t, approver)
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
	require.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, obs.GetKind(), "answer kind: %s", obs.GetKind())
	assert.Equal(t, txHash, obs.GetReceipt().GetTransactionHash(), "the receipt must be for the suspended transaction")

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
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, driven.Observations[0].GetReceipt().GetL3Status())

	written, err := os.ReadFile(a.TargetAbs)
	require.NoError(t, err)
	assert.Equal(t, a.Marker, string(written))
}
