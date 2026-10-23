// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package authcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// completedApprovalReceipt is the resumed execution receipt of an approved
// transaction: completed, with the required L3 proof valid.
func completedApprovalReceipt(txHash string) *operatorv1.ActionReceipt {
	return &operatorv1.ActionReceipt{
		TransactionId:   "transaction-approve-test",
		TransactionHash: txHash,
		SignerKeyId:     "warden-key-approve-test",
		Signature:       "signature-approve-test",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		L3Status:        operatorv1.L3Status_L3_STATUS_REQUIRED_VALID,
	}
}

// sseDecisionServer serves one approval.completed event for txHash carrying
// receipt. A nil receipt sends the event without one.
func sseDecisionServer(t *testing.T, txHash string, receipt *operatorv1.ActionReceipt) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		event := models.ApprovalCompletedEvent{
			Type:   constants.SSEEventTypeApprovalCompleted,
			UserID: "user-test",
			TxHash: txHash,
		}
		if receipt != nil {
			body, err := response.MarshalProtoJSONCanonical(receipt)
			assert.NoError(t, err)
			event.Receipt = body
		}
		eventPayload, err := json.Marshal(event)
		assert.NoError(t, err)
		envelope := models.SSEPushPayload{
			UserID: "user-test",
			Event:  eventPayload,
		}
		envelopeJSON, err := json.Marshal(envelope)
		assert.NoError(t, err)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", constants.SSEEventTypeApprovalCompleted, string(envelopeJSON))
	}))
}

// sseNoEventServer returns an httptest.Server that accepts SSE connections
// but never sends any events, causing the client to block until context cancel.
// It flushes the 200 before it waits, as the Gateway does, so the subscription
// connects and the wait is what times out.
// The server registers its own cleanup to ensure the handler unblocks before
// Close is called, which is necessary on Windows where the TCP stack does not
// promptly notify the server of client-side connection closure.
func sseNoEventServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	t.Cleanup(func() {
		cancel()
		srv.Close()
	})
	return srv
}

// newBrowserApprove builds 'ztd auth approve' against the SSE server at srvURL
// and returns it with its output buffer and the API client it uses.
func newBrowserApprove(t *testing.T, srvURL string) (*cobra.Command, *bytes.Buffer, *cmdtest.MockAPIClient) {
	t.Helper()
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)
	cmdtest.WithEndpointOverride(t, srvURL)

	mockClient := &cmdtest.MockAPIClient{}
	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	return cmd, &buf, mockClient
}

// runBrowserApprove runs 'ztd auth approve txHash' against the SSE server at
// srvURL and returns its output and error.
func runBrowserApprove(t *testing.T, srvURL, txHash string) (string, error) {
	t.Helper()
	cmd, out, _ := newBrowserApprove(t, srvURL)
	err := cmd.RunE(cmd, []string{txHash})
	return out.String(), err
}

func TestApproveCmd_SSE_HappyPath(t *testing.T) {
	srv := sseDecisionServer(t, "txhash123", completedApprovalReceipt("txhash123"))
	t.Cleanup(srv.Close)

	cmd, out, client := newBrowserApprove(t, srv.URL)
	require.NoError(t, cmd.RunE(cmd, []string{"txhash123"}))

	assert.Contains(t, out.String(), "approved successfully")
	assert.Contains(t, out.String(), "txhash123")
	assert.Contains(t, out.String(), "EXECUTION_STATUS_COMPLETED")
	assert.Empty(t, client.GetCalls, "the executed transaction has left the pending store; the receipt alone is checked")
}

func TestApproveCmd_SSE_DenyIsNeverReportedApproved(t *testing.T) {
	srv := sseDecisionServer(t, "txhash123", &operatorv1.ActionReceipt{
		TransactionHash: "txhash123",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED,
	})
	t.Cleanup(srv.Close)

	out, err := runBrowserApprove(t, srv.URL, "txhash123")
	require.ErrorIs(t, err, constants.ErrTxL3Denied)
	assert.NotContains(t, out, "approved successfully")
}

func TestApproveCmd_SSE_ReceiptRejected(t *testing.T) {
	failed := completedApprovalReceipt("txhash123")
	failed.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	failed.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED

	unproven := completedApprovalReceipt("txhash123")
	unproven.L3Status = operatorv1.L3Status_L3_STATUS_NOT_REQUIRED

	rejected := completedApprovalReceipt("txhash123")
	rejected.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET

	cases := []struct {
		name    string
		receipt *operatorv1.ActionReceipt
		want    error
	}{
		{name: "missing receipt", receipt: nil, want: constants.ErrActionReceiptMissing},
		{name: "receipt for another transaction", receipt: completedApprovalReceipt("txhash-other"), want: constants.ErrActionReceiptMissing},
		{name: "failed execution", receipt: failed, want: constants.ErrActionExecutionFailed},
		{name: "governance rejection", receipt: rejected, want: constants.ErrActionExecutionFailed},
		{name: "completed without required L3 proof", receipt: unproven, want: constants.ErrTxL3ProofInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := sseDecisionServer(t, "txhash123", tc.receipt)
			t.Cleanup(srv.Close)

			out, err := runBrowserApprove(t, srv.URL, "txhash123")
			require.ErrorIs(t, err, tc.want)
			assert.NotContains(t, out, "approved successfully")
		})
	}
}

func TestApproveCmd_SSE_Timeout(t *testing.T) {
	srv := sseNoEventServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cmd, _, _ := newBrowserApprove(t, srv.URL)
	cmd.SetContext(ctx)
	err := cmd.RunE(cmd, []string{"txhash123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}
