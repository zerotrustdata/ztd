// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration || e2e

package fixtures

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ApproveTransactionsWithCLI signs and submits suspended transaction approvals
// using the owner's enrolled CLI identity. It learns about transactions through
// the owner's SSE stream and follows the production Gateway approval routes.
func (c *OwnerConsole) ApproveTransactionsWithCLI(t *testing.T, identity *ClientIdentity) {
	t.Helper()
	require.NotNil(t, c)
	require.NotNil(t, identity)
	require.Equal(t, c.UserID, identity.UserID, "CLI approver must be the owner")
	require.Equal(t, c.CLISessionID, identity.CLISessionID, "CLI approver must use the owner's SSE session")

	c.mu.Lock()
	alreadyInstalled := c.l3Responder
	if !alreadyInstalled {
		c.l3Responder = true
	}
	c.mu.Unlock()
	require.False(t, alreadyInstalled, "CLI transaction responder is already installed")

	wakeups := make(chan struct{}, 1)
	client := CreateCLIMTLSClient(t, c.gateway, identity)
	baseURL := network.LocalhostHTTPSURL(c.gateway.Service.GetHTTPSPort())
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	unregister := c.gateway.Service.GetGatewayWebSocketHandler().RegisterHandler(
		pubsub.SSECLIChannel(c.CLISessionID),
		func(_ string, data []byte) {
			if transactionApprovalNotification(data, c.UserID, c.CLISessionID) {
				select {
				case wakeups <- struct{}{}:
				default:
				}
			}
		},
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wakeups:
				approvalCtx, approvalCancel := context.WithTimeout(ctx, 5*time.Minute)
				err := respondSuspendedTransactions(approvalCtx, client, baseURL, identity, c)
				approvalCancel()
				if err != nil && ctx.Err() == nil {
					c.faults.append(fmt.Errorf("CLI transaction approver: %w", err))
				}
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		wg.Wait()
		unregister()
		client.CloseIdleConnections()
	})
}

// DenyToolTransactions makes the installed CLI responder deny every
// transaction for toolName it answers from now on, through the same production
// route; the signed rejection is what the blocked request gets. Every other
// transaction, such as the inference calls an eval agent turn needs, is still
// approved.
func (c *OwnerConsole) DenyToolTransactions(toolName string) {
	c.deniedTool.Store(&toolName)
}

func (c *OwnerConsole) approvesTool(toolName string) bool {
	denied := c.deniedTool.Load()
	return denied == nil || *denied != toolName
}

// ApprovalEventReceipts returns the validated event receipts produced by the
// owner's CLI approvals, in the order the Gateway returned them.
func (c *OwnerConsole) ApprovalEventReceipts() []*operatorv1.EventReceipt {
	recorded := c.approvalEventReceipts.snapshot()
	eventReceipts := make([]*operatorv1.EventReceipt, 0, len(recorded))
	for _, eventReceipt := range recorded {
		eventReceipts = append(eventReceipts, proto.Clone(eventReceipt).(*operatorv1.EventReceipt))
	}
	return eventReceipts
}

// AwaitApprovalEventReceipts blocks until n CLI approval event receipts have been
// validated and recorded.
func (c *OwnerConsole) AwaitApprovalEventReceipts(ctx context.Context, n int) ([]*operatorv1.EventReceipt, error) {
	recorded, err := c.approvalEventReceipts.awaitAll(ctx, func(eventReceipts []*operatorv1.EventReceipt) bool {
		return len(eventReceipts) >= n
	})
	if err != nil {
		return nil, fmt.Errorf("owner console: await %d CLI approval event receipts (have %d): %w", n, len(recorded), err)
	}
	eventReceipts := make([]*operatorv1.EventReceipt, 0, len(recorded))
	for _, eventReceipt := range recorded {
		eventReceipts = append(eventReceipts, proto.Clone(eventReceipt).(*operatorv1.EventReceipt))
	}
	return eventReceipts, nil
}

func transactionApprovalNotification(data []byte, userID, cliSessionID string) bool {
	var published models.SSEPublishedEvent
	if err := json.Unmarshal(data, &published); err != nil {
		return false
	}
	var push models.SSEPushPayload
	if err := json.Unmarshal(published.Payload, &push); err != nil || push.UserID != userID || push.CliSessionID != cliSessionID {
		return false
	}
	var event struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(push.Event, &event); err != nil || event.Type != string(constants.EventPlatformApprovalsChanged) {
		return false
	}
	var payload models.ApprovalsChangedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return false
	}
	return payload.Subject == models.ApprovalsChangedTransactions
}

// respondSuspendedTransactions answers every pending transaction with the
// owner's decision for its tool: an approval, or a denial for a denied tool.
// Per-transaction failures are joined into the returned error without stopping
// the remaining pending transactions.
func respondSuspendedTransactions(ctx context.Context, client *http.Client, baseURL string, identity *ClientIdentity, owner *OwnerConsole) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+constants.APIPaths.ApprovalsCLIList, nil)
	if err != nil {
		return fmt.Errorf("create pending approvals request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("list pending approvals: %w", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read pending approvals response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close pending approvals response: %w", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("list pending approvals: HTTP %d: %s", response.StatusCode, body)
	}
	var pending models.SuspendedTransactionsResponse
	if err := json.Unmarshal(body, &pending); err != nil {
		return fmt.Errorf("decode pending approvals: %w", err)
	}

	cliCert, err := tls.X509KeyPair([]byte(identity.CLICertificate), pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: identity.CLIPrivateKey,
	}))
	if err != nil {
		return fmt.Errorf("load enrolled CLI approval identity: %w", err)
	}
	var faults []error
	for _, transaction := range pending.Transactions {
		if ctx.Err() != nil {
			return errors.Join(append(faults, ctx.Err())...)
		}
		// A failed transaction is recorded and the rest of the batch is still answered.
		if err := respondSuspendedTransaction(ctx, client, baseURL, &cliCert, owner, transaction); err != nil {
			faults = append(faults, err)
		}
	}
	return errors.Join(faults...)
}

// respondSuspendedTransaction answers one pending transaction with the owner's
// decision for its tool and records the validated event receipt on success.
func respondSuspendedTransaction(ctx context.Context, client *http.Client, baseURL string, cliCert *tls.Certificate, owner *OwnerConsole, transaction models.SuspendedTxResponse) error {
	approved := owner.approvesTool(transaction.ToolName)
	proof, err := governance.SignCLIApproval(transaction.TransactionHash, cliCert)
	if err != nil {
		return fmt.Errorf("sign transaction %s: %w", transaction.TransactionHash, err)
	}
	respond := models.ApprovalRespondRequest{
		ApprovalID:   transaction.TransactionHash,
		Approved:     approved,
		CLISignature: proof.GetCliSignature(),
	}
	if !approved {
		respond.Reason = "eval stack: denied by the authenticated owner"
	}
	approvalBody, err := json.Marshal(respond)
	if err != nil {
		return fmt.Errorf("encode transaction %s approval: %w", transaction.TransactionHash, err)
	}
	approvalRequest, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+constants.APIPaths.ApprovalsRespond, bytes.NewReader(approvalBody))
	if err != nil {
		return fmt.Errorf("create transaction %s approval request: %w", transaction.TransactionHash, err)
	}
	approvalRequest.Header.Set("Content-Type", constants.HeaderValueApplicationJSON)
	approvalResponse, err := client.Do(approvalRequest)
	if err != nil {
		return fmt.Errorf("approve transaction %s: %w", transaction.TransactionHash, err)
	}
	approvalWire, readErr := io.ReadAll(approvalResponse.Body)
	closeErr := approvalResponse.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read transaction %s event_receipt: %w", transaction.TransactionHash, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close transaction %s event_receipt: %w", transaction.TransactionHash, closeErr)
	}
	if approvalResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("approve transaction %s: HTTP %d: %s", transaction.TransactionHash, approvalResponse.StatusCode, approvalWire)
	}
	var eventReceipt operatorv1.EventReceipt
	if err := protojson.Unmarshal(approvalWire, &eventReceipt); err != nil {
		return fmt.Errorf("decode transaction %s event_receipt: %w", transaction.TransactionHash, err)
	}
	if approved {
		if eventReceipt.GetTransactionHash() != transaction.TransactionHash ||
			eventReceipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED ||
			eventReceipt.GetL3Status() != operatorv1.L3Status_L3_STATUS_REQUIRED_VALID {
			return fmt.Errorf("transaction %s approval returned an invalid completed L3 event receipt", transaction.TransactionHash)
		}
	} else if eventReceipt.GetTransactionHash() != transaction.TransactionHash ||
		eventReceipt.GetStatus() == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fmt.Errorf("transaction %s denial returned an event receipt that is not a signed rejection", transaction.TransactionHash)
	}
	owner.approvalEventReceipts.append(&eventReceipt)
	return nil
}
