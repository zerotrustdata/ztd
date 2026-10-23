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

// sseApproveServer returns an httptest.Server that serves a single
// approval.completed SSE event for the test user and given transaction.
func sseApproveServer(t *testing.T, txHash string) *httptest.Server {
	t.Helper()
	return sseDecisionServer(t, txHash, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED)
}

// sseDecisionServer serves one approval.completed event whose receipt carries
// rejection, so a deny is the signed L3_DENIED receipt.
func sseDecisionServer(t *testing.T, txHash string, rejection operatorv1.GovernanceRejectionCode) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		receipt, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
			TransactionId:   "transaction-approve-test",
			TransactionHash: txHash,
			SignerKeyId:     "warden-key-approve-test",
			Signature:       "signature-approve-test",
			RejectionCode:   rejection,
		})
		assert.NoError(t, err)
		eventPayload, err := json.Marshal(models.ApprovalCompletedEvent{
			Type:    constants.SSEEventTypeApprovalCompleted,
			UserID:  "user-test",
			TxHash:  txHash,
			Receipt: receipt,
		})
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

func TestApproveCmd_SSE_HappyPath(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	srv := sseApproveServer(t, "txhash123")
	t.Cleanup(srv.Close)
	cmdtest.WithEndpointOverride(t, srv.URL)

	mockClient := &cmdtest.MockAPIClient{
		GetResp: []byte(`{"status":"approved","result_summary":"success"}`),
	}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{"txhash123"})
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "approved successfully")
	assert.Contains(t, buf.String(), "txhash123")
}

func TestApproveCmd_SSE_DenyIsNeverReportedApproved(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	srv := sseDecisionServer(t, "txhash123", operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED)
	t.Cleanup(srv.Close)
	cmdtest.WithEndpointOverride(t, srv.URL)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`{"status":"approved"}`)}
	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{"txhash123"})
	require.ErrorIs(t, err, constants.ErrTxL3Denied)
	assert.NotContains(t, buf.String(), "approved successfully")
}

func TestApproveCmd_SSE_Timeout(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	srv := sseNoEventServer(t)
	cmdtest.WithEndpointOverride(t, srv.URL)

	mockClient := &cmdtest.MockAPIClient{
		GetResp: []byte(`{"status":"approved"}`),
	}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd.SetContext(ctx)

	err := cmd.RunE(cmd, []string{"txhash123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestApproveCmd_SSE_Success_GetError(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	srv := sseApproveServer(t, "txhash123")
	t.Cleanup(srv.Close)
	cmdtest.WithEndpointOverride(t, srv.URL)

	mockClient := &cmdtest.MockAPIClient{
		GetErr: fmt.Errorf("network failure"),
	}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{"txhash123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verify status")
}

func TestApproveCmd_SSE_Success_InvalidJSONStatus(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	srv := sseApproveServer(t, "txhash123")
	t.Cleanup(srv.Close)
	cmdtest.WithEndpointOverride(t, srv.URL)

	mockClient := &cmdtest.MockAPIClient{
		GetResp: []byte(`not json {{{`),
	}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{"txhash123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse status response")
}

func TestApproveCmd_SSE_Success_EmptyStatus(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	srv := sseApproveServer(t, "txhash456")
	t.Cleanup(srv.Close)
	cmdtest.WithEndpointOverride(t, srv.URL)

	mockClient := &cmdtest.MockAPIClient{
		GetResp: []byte(`{}`),
	}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{"txhash456"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status")
	assert.Contains(t, buf.String(), "txhash456")
}

func TestApproveCmd_SSE_Success_StatusNotApproved(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	srv := sseApproveServer(t, "txhash789")
	t.Cleanup(srv.Close)
	cmdtest.WithEndpointOverride(t, srv.URL)

	mockClient := &cmdtest.MockAPIClient{
		GetResp: []byte(`{"status":"pending"}`),
	}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{"txhash789"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status")
}
