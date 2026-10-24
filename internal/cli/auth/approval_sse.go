// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ApprovalSSETimeout is the maximum time Wait blocks for an approval.completed
// SSE event. The gateway's approval request TTL is 2 minutes; this allows a small
// grace period.
const ApprovalSSETimeout = 3 * time.Minute

// approvalBacklogLimit bounds the completed approvals an ApprovalSubscription keeps
// for transactions that Wait has not asked for yet. The oldest is dropped first.
const approvalBacklogLimit = 64

// approvalKey identifies one completed approval: the transaction and the user it is tagged with.
type approvalKey struct {
	txHash string
	userID string
}

// ApprovalSubscription is a live subscription to approval.completed events on the
// gateway SSE stream for one CLI session. SubscribeApprovals returns only after the
// gateway has registered the listener, so an approval completed after that point is
// held until Wait asks for it. Nothing is replayed and nothing polls.
type ApprovalSubscription struct {
	cancel context.CancelFunc
	done   chan struct{}

	// dropErr is why the live stream ended after it connected. It is written on the
	// SSE stream goroutine before done is closed and read only after done is closed.
	dropErr error

	mu        sync.Mutex
	completed map[approvalKey]models.ApprovalCompletedEvent
	order     []approvalKey
	arrived   chan struct{} // closed and replaced each time an approval is recorded

	// transactionsChanged holds one pending announcement of ztd.v1.platform.approvals.changed
	// (subject transactions). Announcements coalesce: the Gateway's event carries no hash,
	// so a caller that needs the list re-lists once it is woken.
	transactionsChanged chan struct{}
}

// SubscribeApprovals opens the gateway SSE stream live-only (since_id=0 carries no
// replay) for cliSessionID and returns once the gateway has registered the listener.
// Call it before the call that may suspend is submitted, or before the approval
// browser opens, then Wait for the decision and Close. httpClient must carry the CLI
// mTLS identity; baseURL is the gateway public HTTPS URL, so the TLS ServerName
// matches the cert SAN. A connection failure before the stream connects is returned
// and is not retried. A stream that drops after it connected ends the subscription,
// and Wait fails closed.
func SubscribeApprovals(ctx context.Context, httpClient *http.Client, baseURL, cliSessionID string) (*ApprovalSubscription, error) {
	sseURL := fmt.Sprintf("%s%s?since_id=0",
		baseURL,
		constants.APIPaths.SSEStream)

	sseClient := sse.NewClient(sseURL, httpClient)
	if cliSessionID != "" {
		sseClient.SetHeader(constants.HeaderCLISessionID, cliSessionID)
	}

	subCtx, cancel := context.WithCancel(ctx)
	sub := &ApprovalSubscription{
		cancel:              cancel,
		done:                make(chan struct{}),
		completed:           make(map[approvalKey]models.ApprovalCompletedEvent),
		arrived:             make(chan struct{}),
		transactionsChanged: make(chan struct{}, 1),
	}

	// ready carries exactly one result: nil once the listener is registered, or the
	// reason the first connection failed. The fields below are touched only by the
	// goroutine running sseClient.Run.
	ready := make(chan error, 1)
	var established bool
	var connectErr error
	sseClient.SetOnConnect(func() {
		if !established {
			established = true
			ready <- nil
		}
	})
	sseClient.SetOnDisconnect(func(err error) {
		if established {
			// A reconnect is live-only until an event id has been seen, so an approval
			// completed during the gap would be lost. The subscription ends instead and
			// Wait fails closed.
			sub.dropErr = err
			cancel()
			return
		}
		// A failed first connection is not retried: stop Run's backoff wait.
		connectErr = err
		cancel()
	})
	go func() {
		defer close(sub.done)
		sseClient.Run(subCtx, sub.record)
		if !established {
			if connectErr == nil {
				connectErr = ctx.Err()
			}
			if connectErr == nil {
				connectErr = context.Canceled
			}
			ready <- connectErr
		}
	}()

	if err := <-ready; err != nil {
		sub.Close()
		return nil, fmt.Errorf("approval subscription: connect: %w", err)
	}
	return sub, nil
}

// record decodes one SSE frame and keeps the approval.completed event or wakes a
// WaitTransactionsChanged caller for a transactions invalidation. It runs on the SSE
// stream goroutine. A frame that does not decode is not an approval and is ignored.
func (s *ApprovalSubscription) record(eventType, data string) {
	ev, ok := sse.DecodeEvent(eventType, data)
	if !ok {
		return
	}
	switch ev.Type {
	case constants.SSEEventTypeApprovalCompleted:
		s.recordCompleted(ev.Raw)
	case string(constants.EventPlatformApprovalsChanged):
		s.recordTransactionsChanged(ev.Data)
	}
}

// recordTransactionsChanged wakes one WaitTransactionsChanged caller when the event
// announces a change to the pending transactions. Enrollment changes are not this
// subscription's concern and are ignored.
func (s *ApprovalSubscription) recordTransactionsChanged(data json.RawMessage) {
	var payload models.ApprovalsChangedPayload
	if err := json.Unmarshal(data, &payload); err != nil || payload.Subject != models.ApprovalsChangedTransactions {
		return
	}
	select {
	case s.transactionsChanged <- struct{}{}:
	default:
	}
}

// recordCompleted keeps one approval.completed event for Wait to find.
func (s *ApprovalSubscription) recordCompleted(raw json.RawMessage) {
	var event models.ApprovalCompletedEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := approvalKey{txHash: event.TxHash, userID: event.UserID}
	if _, seen := s.completed[key]; !seen {
		s.order = append(s.order, key)
		if len(s.order) > approvalBacklogLimit {
			delete(s.completed, s.order[0])
			s.order = s.order[1:]
		}
	}
	s.completed[key] = event
	close(s.arrived)
	s.arrived = make(chan struct{})
}

// Wait blocks until the approval.completed event for txHash arrives, then returns the
// event's resumed, signed receipt after strict protojson decoding and a transaction
// hash check. An event recorded before Wait is called is returned from the backlog.
//
// userID selects the approval tagged with that user. When userID is empty the newest
// approval for txHash counts. The CLI passes no user: the SSE stream it subscribes on
// is bound to the CLI's mTLS identity, so the stream carries only that user's events.
//
// Wait refuses an empty txHash with constants.ErrMissingRequiredField. It returns
// constants.ErrActionReceiptMissing when the receipt is missing or belongs to another
// transaction, and constants.ErrSSEStreamClosed (or the stream error) when the stream
// ends before the approval arrives. It returns constants.ErrApprovalSSETimeout when ctx
// or ApprovalSSETimeout ends first.
func (s *ApprovalSubscription) Wait(ctx context.Context, txHash, userID string) (*operatorv1.ActionReceipt, error) {
	if txHash == "" {
		return nil, fmt.Errorf("L3 approval: transaction hash: %w", constants.ErrMissingRequiredField)
	}

	waitCtx, cancel := context.WithTimeout(ctx, ApprovalSSETimeout)
	defer cancel()

	for {
		s.mu.Lock()
		event, ok := s.lookupLocked(txHash, userID)
		arrived := s.arrived
		s.mu.Unlock()
		if ok {
			return approvalReceipt(event, txHash)
		}

		select {
		case <-arrived:
		case <-s.done:
			s.mu.Lock()
			event, ok = s.lookupLocked(txHash, userID)
			s.mu.Unlock()
			if ok {
				return approvalReceipt(event, txHash)
			}
			// The stream and wait contexts are siblings. Caller cancellation
			// can stop the stream before it reaches waitCtx, so consult the
			// caller directly as well as the wait's own timeout.
			if ctx.Err() != nil || waitCtx.Err() != nil {
				return nil, constants.ErrApprovalSSETimeout
			}
			if s.dropErr != nil {
				return nil, fmt.Errorf("L3 approval: stream dropped: %w", s.dropErr)
			}
			return nil, fmt.Errorf("L3 approval: %w", constants.ErrSSEStreamClosed)
		case <-waitCtx.Done():
			return nil, constants.ErrApprovalSSETimeout
		}
	}
}

// WaitTransactionsChanged blocks until the Gateway announces that the subscribing user's
// pending transactions changed, then consumes that announcement. The announcement names
// no transaction, so the caller re-lists the pending transactions once it returns. It
// returns constants.ErrSSEStreamClosed (or the stream error) when the stream ends first,
// and ctx's error when ctx ends first.
func (s *ApprovalSubscription) WaitTransactionsChanged(ctx context.Context) error {
	select {
	case <-s.transactionsChanged:
		return nil
	case <-s.done:
		select {
		case <-s.transactionsChanged:
			return nil
		default:
		}
		if s.dropErr != nil {
			return fmt.Errorf("L3 approval: stream dropped: %w", s.dropErr)
		}
		return fmt.Errorf("L3 approval: %w", constants.ErrSSEStreamClosed)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close ends the subscription and waits for its SSE stream to stop. The owner calls
// it once, normally with defer.
func (s *ApprovalSubscription) Close() {
	s.cancel()
	<-s.done
}

// lookupLocked returns the recorded approval for txHash and userID. When userID is
// empty it returns the newest approval for txHash. The caller holds s.mu.
func (s *ApprovalSubscription) lookupLocked(txHash, userID string) (models.ApprovalCompletedEvent, bool) {
	if userID != "" {
		event, ok := s.completed[approvalKey{txHash: txHash, userID: userID}]
		return event, ok
	}
	for i := len(s.order) - 1; i >= 0; i-- {
		if s.order[i].txHash == txHash {
			return s.completed[s.order[i]], true
		}
	}
	return models.ApprovalCompletedEvent{}, false
}

// approvalReceipt decodes the resumed, signed receipt carried by an approval.completed
// event and checks that it belongs to txHash.
func approvalReceipt(event models.ApprovalCompletedEvent, txHash string) (*operatorv1.ActionReceipt, error) {
	if !event.HasReceipt() {
		return nil, fmt.Errorf("L3 approval: %w", constants.ErrActionReceiptMissing)
	}
	receipt := &operatorv1.ActionReceipt{}
	if err := protojson.Unmarshal(event.Receipt, receipt); err != nil {
		return nil, fmt.Errorf("L3 approval: decode resumed receipt: %w", err)
	}
	if receipt.GetTransactionHash() != txHash {
		return nil, fmt.Errorf("L3 approval: receipt transaction %s does not match %s: %w", receipt.GetTransactionHash(), txHash, constants.ErrActionReceiptMissing)
	}
	return receipt, nil
}
