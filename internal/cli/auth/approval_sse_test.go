// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// resumedReceiptJSON returns the canonical signed receipt of txHash's resumed
// transaction, as the Gateway embeds it in approval.completed.
func resumedReceiptJSON(t *testing.T, txHash string) json.RawMessage {
	t.Helper()
	receiptJSON, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
		TransactionId:   "transaction-" + txHash,
		TransactionHash: txHash,
		SignerKeyId:     "warden-key-sse",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "resumed result",
		Signature:       "signature-sse",
	})
	require.NoError(t, err)
	return receiptJSON
}

// expectedReceipt decodes the receipt that resumedReceiptJSON encodes for txHash.
func expectedReceipt(t *testing.T, txHash string) *operatorv1.ActionReceipt {
	t.Helper()
	want := &operatorv1.ActionReceipt{}
	require.NoError(t, protojson.Unmarshal(resumedReceiptJSON(t, txHash), want))
	return want
}

// approvalEnvelope returns the SSE data payload that carries an approval.completed
// event for userID and txHash, with receipt as its embedded receipt.
func approvalEnvelope(t *testing.T, userID, txHash string, receipt json.RawMessage) string {
	t.Helper()
	eventPayload, err := json.Marshal(models.ApprovalCompletedEvent{
		Type:    constants.SSEEventTypeApprovalCompleted,
		UserID:  userID,
		TxHash:  txHash,
		Receipt: receipt,
	})
	require.NoError(t, err)
	envelopeJSON, err := json.Marshal(models.SSEPushPayload{UserID: userID, CliSessionID: "cli-session", Event: eventPayload})
	require.NoError(t, err)
	return string(envelopeJSON)
}

// approvalFrame returns one approval.completed SSE frame, with its event: line.
func approvalFrame(t *testing.T, userID, txHash string, receipt json.RawMessage) string {
	t.Helper()
	return fmt.Sprintf("event: %s\ndata: %s\n\n", constants.SSEEventTypeApprovalCompleted, approvalEnvelope(t, userID, txHash, receipt))
}

// startSSE writes the 200 and flushes it, as the Gateway does before it writes any event.
func startSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

// writeSSE writes the frames in order and flushes them onto the stream.
func writeSSE(w http.ResponseWriter, frames ...string) {
	for _, frame := range frames {
		fmt.Fprint(w, frame)
	}
	w.(http.Flusher).Flush()
}

// newSSEServer starts an SSE fake for the test and closes it on cleanup.
func newSSEServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestSubscribeApprovals_SendsCLISessionHeaderAndLiveOnlyQuery(t *testing.T) {
	type subscriptionRequest struct {
		cliSessionID string
		query        string
	}
	seen := make(chan subscriptionRequest, 1)
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- subscriptionRequest{cliSessionID: r.Header.Get(constants.HeaderCLISessionID), query: r.URL.RawQuery}
		startSSE(w)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-live-session")
	require.NoError(t, err)
	defer sub.Close()

	got := <-seen
	assert.Equal(t, "cli-live-session", got.cliSessionID, "the CLI session must travel as a header")
	assert.Equal(t, "since_id=0", got.query, "the subscription must be live-only")
}

func TestSubscribeApprovals_WaitReturnsApprovalPublishedAfterSubscribeReturns(t *testing.T) {
	const userID = "user-published-after-subscribe"
	const txHash = "tx-published-after-subscribe"
	frame := approvalFrame(t, userID, txHash, resumedReceiptJSON(t, txHash))

	release := make(chan struct{})
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		<-release
		writeSSE(w, frame)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-published-after")
	require.NoError(t, err)
	defer sub.Close()

	// Take the arrival signal before the approval is released, so the test can wait for
	// the record to land without polling.
	sub.mu.Lock()
	recorded := sub.arrived
	sub.mu.Unlock()
	close(release)
	select {
	case <-recorded:
	case <-time.After(5 * time.Second):
		t.Fatal("the approval published after SubscribeApprovals returned was not recorded")
	}

	sub.mu.Lock()
	_, held := sub.completed[approvalKey{txHash: txHash, userID: userID}]
	sub.mu.Unlock()
	require.True(t, held, "the approval must be held for Wait before Wait is called")

	receipt, err := sub.Wait(context.Background(), txHash, userID)
	require.NoError(t, err)
	assert.True(t, proto.Equal(expectedReceipt(t, txHash), receipt), "the approval must return the event's signed receipt")
}

// TestSubscribeApprovals_WaitIgnoresOtherTransactionAndUser places the decoys before the
// matching approval. The other-user frame carries a receipt that fails strict protojson
// decoding, so matching it would fail the wait.
func TestSubscribeApprovals_WaitIgnoresOtherTransactionAndUser(t *testing.T) {
	const userID = "user-ignored-frames"
	const txHash = "tx-ignored-frames"
	const otherTxHash = "tx-ignored-frames-other"
	frames := []string{
		approvalFrame(t, userID, otherTxHash, resumedReceiptJSON(t, otherTxHash)),
		approvalFrame(t, "some-other-user", txHash, json.RawMessage(`{"transaction_hash":"`+txHash+`","not_a_receipt_field":1}`)),
		approvalFrame(t, userID, txHash, resumedReceiptJSON(t, txHash)),
	}
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frames...)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-ignored")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), txHash, userID)
	require.NoError(t, err)
	assert.True(t, proto.Equal(expectedReceipt(t, txHash), receipt))
}

func TestSubscribeApprovals_WaitMatchesTransactionOnlyWhenUserIsEmpty(t *testing.T) {
	const txHash = "tx-any-user"
	frame := approvalFrame(t, "user-of-the-approver", txHash, resumedReceiptJSON(t, txHash))
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frame)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-any-user")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), txHash, "")
	require.NoError(t, err)
	assert.True(t, proto.Equal(expectedReceipt(t, txHash), receipt))
}

// TestSubscribeApprovals_WaitRefusesEmptyTransactionHash holds an approval in the backlog
// and still refuses to wait on an empty transaction hash: there is no accept-any mode.
func TestSubscribeApprovals_WaitRefusesEmptyTransactionHash(t *testing.T) {
	const userID = "user-empty-hash"
	const txHash = "tx-empty-hash"
	frame := approvalFrame(t, userID, txHash, resumedReceiptJSON(t, txHash))
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frame)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-empty-hash")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), "", "")
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	assert.Nil(t, receipt)
}

func TestSubscribeApprovals_WaitFailsClosedWhenReceiptIsMissing(t *testing.T) {
	const userID = "user-no-receipt"
	const txHash = "tx-no-receipt"
	frame := approvalFrame(t, userID, txHash, nil)
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frame)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-no-receipt")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), txHash, userID)
	require.ErrorIs(t, err, constants.ErrActionReceiptMissing)
	assert.Nil(t, receipt)
}

func TestSubscribeApprovals_WaitFailsClosedWhenReceiptIsNotProtojson(t *testing.T) {
	const userID = "user-malformed-receipt"
	const txHash = "tx-malformed-receipt"
	frame := approvalFrame(t, userID, txHash, json.RawMessage(`{"transaction_hash":"`+txHash+`","not_a_receipt_field":1}`))
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frame)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-malformed")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), txHash, userID)
	require.Error(t, err, "a receipt with unknown fields must fail strict protojson decoding")
	assert.Nil(t, receipt)
}

func TestSubscribeApprovals_WaitFailsClosedWhenReceiptBelongsToAnotherTransaction(t *testing.T) {
	const userID = "user-mismatched-receipt"
	const txHash = "tx-matched-event"
	frame := approvalFrame(t, userID, txHash, resumedReceiptJSON(t, "different-transaction-hash"))
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frame)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-mismatched")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), txHash, userID)
	require.ErrorIs(t, err, constants.ErrActionReceiptMissing)
	assert.Nil(t, receipt)
}

// TestSubscribeApprovals_WaitFailsClosedWhenStreamEndsBeforeApproval checks that a live
// stream that ends before the approval fails Wait at once. The subscription must not
// reconnect live-only, because an approval completed in the gap would be lost.
func TestSubscribeApprovals_WaitFailsClosedWhenStreamEndsBeforeApproval(t *testing.T) {
	const userID = "user-stream-ends"
	const txHash = "tx-stream-ends"
	const otherTxHash = "tx-stream-ends-other"
	frame := approvalFrame(t, userID, otherTxHash, resumedReceiptJSON(t, otherTxHash))
	var connections atomic.Int32
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		startSSE(w)
		writeSSE(w, frame)
		// Returning closes the stream cleanly without the approval.
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-stream-ends")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), txHash, userID)
	require.ErrorIs(t, err, constants.ErrSSEStreamClosed)
	assert.NotErrorIs(t, err, constants.ErrApprovalSSETimeout)
	assert.Nil(t, receipt)
	assert.Equal(t, int32(1), connections.Load(), "a stream that ended after connecting must not be re-opened")
}

// approvalsChangedFrame returns one ztd.v1.platform.approvals.changed SSE frame for subject.
func approvalsChangedFrame(t *testing.T, subject models.ApprovalsChangedSubject) string {
	t.Helper()
	data, err := json.Marshal(models.ApprovalsChangedPayload{Subject: subject, Timestamp: time.Now().UTC()})
	require.NoError(t, err)
	event, err := json.Marshal(struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}{Type: string(constants.EventPlatformApprovalsChanged), Data: data})
	require.NoError(t, err)
	envelopeJSON, err := json.Marshal(models.SSEPushPayload{UserID: "user-changed", CliSessionID: "cli-changed", Event: event})
	require.NoError(t, err)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", constants.EventPlatformApprovalsChanged, envelopeJSON)
}

// TestSubscribeApprovals_WaitTransactionsChangedWakesOnTransactionsAnnouncement checks that
// a transactions invalidation wakes the caller, which then re-lists the pending transactions.
func TestSubscribeApprovals_WaitTransactionsChangedWakesOnTransactionsAnnouncement(t *testing.T) {
	frame := approvalsChangedFrame(t, models.ApprovalsChangedTransactions)
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frame)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-changed")
	require.NoError(t, err)
	defer sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, sub.WaitTransactionsChanged(ctx))
}

// TestSubscribeApprovals_WaitTransactionsChangedIgnoresEnrollmentAnnouncement checks that an
// enrollments invalidation does not wake a transactions waiter. The approval that follows it
// on the same stream is processed first, so the wait sees the enrollments frame already.
func TestSubscribeApprovals_WaitTransactionsChangedIgnoresEnrollmentAnnouncement(t *testing.T) {
	const userID = "user-enrollment-only"
	const txHash = "tx-enrollment-only"
	frames := approvalsChangedFrame(t, models.ApprovalsChangedEnrollments) + approvalFrame(t, userID, txHash, resumedReceiptJSON(t, txHash))
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frames)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-enrollment-only")
	require.NoError(t, err)
	defer sub.Close()

	_, err = sub.Wait(context.Background(), txHash, userID)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, sub.WaitTransactionsChanged(ctx), context.DeadlineExceeded)
}

// TestSubscribeApprovals_WaitTransactionsChangedFailsClosedWhenStreamEnds checks that a
// transactions waiter fails at once when the stream ends before any announcement.
func TestSubscribeApprovals_WaitTransactionsChangedFailsClosedWhenStreamEnds(t *testing.T) {
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		// Returning closes the stream cleanly without an announcement.
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-stream-closed")
	require.NoError(t, err)
	defer sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.ErrorIs(t, sub.WaitTransactionsChanged(ctx), constants.ErrSSEStreamClosed)
}

func TestSubscribeApprovals_ReturnsFirstConnectFailureWithoutRetry(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr error
	}{
		{name: "server error", status: http.StatusInternalServerError},
		{name: "cli session rejected", status: http.StatusUnauthorized, wantErr: constants.ErrCLISessionRefreshRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tt.status)
			})

			sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-refused")
			require.Error(t, err)
			assert.Nil(t, sub)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
			assert.Equal(t, int32(1), requests.Load(), "a failed first connection must not be retried")
		})
	}
}

func TestSubscribeApprovals_WaitTimesOutWithoutMatchingApproval(t *testing.T) {
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-timeout")
	require.NoError(t, err)
	defer sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	receipt, err := sub.Wait(ctx, "tx-never-approved", "user-never-approved")
	require.ErrorIs(t, err, constants.ErrApprovalSSETimeout)
	assert.Nil(t, receipt)
}

// TestSubscribeApprovals_BacklogDropsOldestApprovalsBeyondLimit sends one more approval
// than the backlog holds. The stream handles frames in order, so the final approval is
// recorded after every earlier one by the time Wait returns.
func TestSubscribeApprovals_BacklogDropsOldestApprovalsBeyondLimit(t *testing.T) {
	const userID = "user-backlog"
	const finalTxHash = "tx-backlog-final"
	frames := make([]string, 0, approvalBacklogLimit+2)
	for i := 0; i <= approvalBacklogLimit; i++ {
		txHash := fmt.Sprintf("tx-backlog-%03d", i)
		frames = append(frames, approvalFrame(t, userID, txHash, resumedReceiptJSON(t, txHash)))
	}
	frames = append(frames, approvalFrame(t, userID, finalTxHash, resumedReceiptJSON(t, finalTxHash)))
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frames...)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-backlog")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), finalTxHash, userID)
	require.NoError(t, err)
	assert.True(t, proto.Equal(expectedReceipt(t, finalTxHash), receipt))

	sub.mu.Lock()
	defer sub.mu.Unlock()
	assert.Len(t, sub.completed, approvalBacklogLimit, "the backlog must stay bounded")
	_, oldestKept := sub.completed[approvalKey{txHash: "tx-backlog-000", userID: userID}]
	assert.False(t, oldestKept, "the oldest approval must be dropped first")
}

// TestSubscribeApprovals_ReadsApprovalTypeFromPayloadWhenEventFieldIsOmitted covers a
// gateway frame with no event: line. The type comes from the inner payload.
func TestSubscribeApprovals_ReadsApprovalTypeFromPayloadWhenEventFieldIsOmitted(t *testing.T) {
	const userID = "user-no-event-line"
	const txHash = "tx-no-event-line"
	frame := fmt.Sprintf("data: %s\n\n", approvalEnvelope(t, userID, txHash, resumedReceiptJSON(t, txHash)))
	srv := newSSEServer(t, func(w http.ResponseWriter, r *http.Request) {
		startSSE(w)
		writeSSE(w, frame)
		<-r.Context().Done()
	})

	sub, err := SubscribeApprovals(context.Background(), srv.Client(), srv.URL, "cli-no-event-line")
	require.NoError(t, err)
	defer sub.Close()

	receipt, err := sub.Wait(context.Background(), txHash, userID)
	require.NoError(t, err)
	assert.True(t, proto.Equal(expectedReceipt(t, txHash), receipt))
}
