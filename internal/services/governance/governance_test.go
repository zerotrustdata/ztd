// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// mockExecutionHandler is a test-only implementation of ExecutionHandler.
type mockExecutionHandler struct {
	executed                       bool
	err                            error
	ExecuteVerifiedTransactionFunc func(ctx context.Context, eventType constants.EventType, cmdMsg CommandMessage) (string, error)
}

func (m *mockExecutionHandler) ExecuteVerifiedTransaction(ctx context.Context, eventType constants.EventType, cmdMsg CommandMessage) (string, error) {
	m.executed = true
	if m.ExecuteVerifiedTransactionFunc != nil {
		return m.ExecuteVerifiedTransactionFunc(ctx, eventType, cmdMsg)
	}
	return "", m.err
}

// mockEventReceiptPublisher is a test-only implementation of EventReceiptPublisher.
// It records the envelope and event receipt passed to PublishEventReceipt so
// tests can assert that Execute calls the publisher after successful
// execution and does not call it when signAndLogFinalEventReceipt fails.
type mockEventReceiptPublisher struct {
	mu           sync.Mutex
	calls        int
	envelope     *govtypes.GovernanceEnvelope
	eventReceipt *operatorv1.EventReceipt
	publishError error
}

func (m *mockEventReceiptPublisher) PublishEventReceipt(_ context.Context, env *govtypes.GovernanceEnvelope, eventReceipt *operatorv1.EventReceipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.envelope = env
	m.eventReceipt = eventReceipt
	return m.publishError
}

func (m *mockEventReceiptPublisher) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}
