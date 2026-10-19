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
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	agentconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// l3Gateway is one live Gateway with the CLI identity that the L3 tests submit as.
type l3Gateway struct {
	f       *fixtures.GatewayFixture
	ident   *fixtures.ClientIdentity
	client  *client.Client
	persona client.Persona
	target  operator.Target
}

// bootL3Gateway boots a Gateway under posture and returns a harness client that
// submits as the bootstrapped CLI identity to the embedded Operator.
func bootL3Gateway(t *testing.T, testName string, posture constants.Posture) *l3Gateway {
	t.Helper()
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{TestName: testName, Posture: posture})
	f.WaitForReady(t)
	ident := fixtures.BootstrapCLIIdentity(t, f, "conformance-l3-host")

	cliCert, err := tls.X509KeyPair([]byte(ident.CLICertificate), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ident.CLIPrivateKey}))
	require.NoError(t, err)
	caBundle := append(append([]byte{}, testutil.ReadRootCA(t, f.PKIDir)...), testutil.ReadOperatorCA(t, f.PKIDir)...)
	caPath := filepath.Join(testutil.TempDir(t), "ca.pem")
	require.NoError(t, os.WriteFile(caPath, caBundle, 0o600))

	url := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())
	c, err := client.New(agentconfig.Config{
		MTLSBaseURL:   url,
		PublicBaseURL: url,
		Auth:          agentconfig.Auth{Certificate: &cliCert, CABundle: caPath},
	})
	require.NoError(t, err)

	return &l3Gateway{
		f:       f,
		ident:   ident,
		client:  c,
		persona: client.Persona{ID: "conformance-l3", UserID: ident.UserID, CLISessionID: ident.CLISessionID},
		target:  operator.Target{OperatorID: ident.OperatorID, OperatorSessionID: ident.OperatorSessionID},
	}
}

// attempts builds attempts for this Gateway's work directory.
func (g *l3Gateway) attempts() *directFixture {
	return &directFixture{f: g.f, workDir: g.f.Config.WorkDir}
}

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
// approval before returning. The attended driver has already opened its approval
// subscription before this inner drive starts, so the approval is synchronous and the
// Gateway's approval.completed event is delivered live to that subscription. The test
// goroutine stays the owner of every require call.
type approvingDriver struct {
	inner   cases.Driver
	approve func(txHash string)
}

func (d approvingDriver) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	driven, err := d.inner.Drive(ctx, a)
	if err != nil {
		return cases.Driven{}, err
	}
	if len(driven.Observations) == 1 && driven.Observations[0].GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED {
		d.approve(driven.Observations[0].GetSuspendedTxHash())
	}
	return driven, nil
}

// TestAttendedRatifyApprovalResumesMCPCall drives row 23 on MCP under ratify. The
// call suspends, the test approves it with its passkey, and the attended driver
// returns the resumed signed receipt with the marker written.
func TestAttendedRatifyApprovalResumesMCPCall(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	row24 := findCase(t, all, 23, cases.IngressMCP, ratify)

	g := bootL3Gateway(t, "conformance-attended-mcp", constants.PostureRatify)
	mcpDriver, err := NewMCP(context.Background(), g.client, g.persona)
	require.NoError(t, err)

	// The approver is the human: the passkey session of the same identity over the CLI mTLS client.
	approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)
	session := fixtures.ConsoleSignIn(t, g.f, approver, g.ident)
	var approvedTxHash string
	approving := approvingDriver{inner: mcpDriver, approve: func(txHash string) {
		approvedTxHash = txHash
		status, body := fixtures.ApproveWithPasskey(t, g.f, approver, session, g.ident, txHash)
		require.Equal(t, http.StatusOK, status, "passkey approval must resume the call: %s", body)
	}}

	fx := g.attempts()
	a := fx.attemptFor(t, row24)

	driven, err := NewAttended(approving, g.client, g.persona, g.ident.UserID).Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	require.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, obs.GetKind(), "answer: %s", answerText(obs))

	receipt := obs.GetReceipt()
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
	assert.Equal(t, approvedTxHash, receipt.GetTransactionHash(), "the resumed receipt must be for the suspended transaction")
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, receipt.GetL3Status(), "the approved call must carry a valid L3 proof")
	assert.Equal(t, row24.Expect[0].L2Status, receipt.GetL2Status())

	written, err := os.ReadFile(a.TargetAbs)
	require.NoError(t, err)
	assert.Equal(t, a.Marker, string(written), "the approved mutation must write exactly the marker")
}

// cliPoster posts the CLI driver's requests the way the Gateway API client does: a
// non-2xx answer is an api.StatusError carrying its body, so RunOnce decodes the
// typed refusal or signed rejection.
type cliPoster struct {
	client  *http.Client
	baseURL string
}

func (p cliPoster) Post(path string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Post(p.baseURL+path, constants.HeaderValueApplicationJSON, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &api.StatusError{StatusCode: resp.StatusCode, Body: string(answer)}
	}
	return answer, nil
}

// awaitPendingApproval waits for the one transaction the blocked call suspended and
// returns its hash. The approver learns of it the way a human does, from the Gateway's
// pending approval list.
func (g *l3Gateway) awaitPendingApproval(t *testing.T, approver *http.Client) string {
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
func (g *l3Gateway) denyWithCLI(t *testing.T, approver *http.Client, txHash string) {
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
// governed dispatch and the CLI under ratify against a live Gateway. The request
// blocks while the transaction is pending, the human decides over the Gateway's
// approval routes, and the attended driver returns the signed answer the blocked
// request got: the executed receipt on approve, the L3_DENIED rejection on deny.
func TestAttendedRatifyBlockingIngressesFollowTheHumanDecision(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY

	for _, ingress := range []cases.Ingress{cases.IngressDispatchEmbedded, cases.IngressCLI} {
		for _, row := range []int{23, 24} {
			c := findCase(t, all, row, ingress, ratify)
			t.Run(c.ID, func(t *testing.T) {
				g := bootL3Gateway(t, "conformance-attended-blocking", constants.PostureRatify)
				approver := fixtures.CreateCLIMTLSClient(t, g.f, g.ident)

				var inner cases.Driver
				var err error
				if ingress == cases.IngressCLI {
					inner, err = NewCLI(cliPoster{client: approver, baseURL: network.LocalhostHTTPSURL(g.f.Service.GetHTTPSPort())}, g.target, g.ident.CLISessionID)
				} else {
					inner, err = NewDispatch(g.client, g.persona, map[cases.Executor]operator.Target{cases.ExecutorEmbedded: g.target})
				}
				require.NoError(t, err)
				a := g.attempts().attemptFor(t, c)

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
				assertObservation(t, c.Expect[0], obs)

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

// TestL3DirectRowsUnderRatify drives row 20 on DIRECT under ratify: a missing L3
// proof and an invalid L3 proof are each rejected by L4 before anything executes.
func TestL3DirectRowsUnderRatify(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY

	g := bootL3Gateway(t, "conformance-l3-ratify", constants.PostureRatify)
	direct, err := NewDirect(g.client, g.persona, g.target, nil, BaseMutators(), L3Mutators())
	require.NoError(t, err)
	fx := g.attempts()

	found := 0
	for _, c := range all {
		if c.Row != 20 || c.Ingress != cases.IngressDirect || c.Posture != ratify {
			continue
		}
		found++
		t.Run(c.ID, func(t *testing.T) {
			driven, err := direct.Drive(context.Background(), fx.attemptFor(t, c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(c.Expect))
			for i, want := range c.Expect {
				assertObservation(t, want, driven.Observations[i])
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

	g := bootL3Gateway(t, "conformance-l3-doctrine", constants.PostureDoctrine)
	direct, err := NewDirect(g.client, g.persona, g.target, nil, BaseMutators(), L3Mutators())
	require.NoError(t, err)
	fx := g.attempts()
	a := fx.attemptFor(t, c)

	driven, err := direct.Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, len(c.Expect))
	assertObservation(t, c.Expect[0], driven.Observations[0])
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, driven.Observations[0].GetReceipt().GetL3Status())

	written, err := os.ReadFile(a.TargetAbs)
	require.NoError(t, err)
	assert.Equal(t, a.Marker, string(written))
}

// TestAttendedA2AStubCallsDuringWaitAreAttributed checks that the A2A downstream
// calls made after the approval belong to the attempt. The fake approval wait
// posts to the live stub the way the Gateway does, after the inner drive returned.
func TestAttendedA2AStubCallsDuringWaitAreAttributed(t *testing.T) {
	g := &fakeA2AGateway{callBody: a2aResultBody(t, mcp.GovernedSuspension{ID: "s-1", Status: "PENDING", TxHash: "tx-hash-1"})}
	a2a, stub := newFakeA2A(t, g)
	// A suspended call never reaches the downstream, so the fake Gateway does not forward it.
	g.downstream = nil
	a := a2aAttempt(cases.Case{ID: "row-24-a2a", Fault: cases.FaultAttendedApprove, Action: cases.ActionMutation, Attended: true, Ingress: cases.IngressA2A})

	fa := &fakeApprovals{
		receipt: &operatorv1.ActionReceipt{TransactionHash: "tx-hash-1"},
		onWait: func() {
			assert.Equal(t, http.StatusOK, postDownstream(t, stub, `{"skill_name":"ztd-conformance","payload":{"marker":"marker-1"}}`))
		},
	}
	d := NewAttended(a2a, nil, testIdentity().Persona, "user-approver")
	d.approvals = fa

	driven, err := d.Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, driven.Observations[0].GetKind())
	require.Len(t, driven.A2ACalls, 1, "the call made during the wait must be attributed to the attempt")
	assert.Equal(t, "ztd-conformance", driven.A2ACalls[0].GetSkillName())
	assert.JSONEq(t, `{"marker":"marker-1"}`, driven.A2ACalls[0].GetPayloadJson())
	assert.Len(t, g.recorded(), 1, "the inner drive sent exactly one a2a/call")
}
