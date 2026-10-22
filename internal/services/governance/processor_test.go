// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// mockProcessor implements EnvelopeProcessor for testing interface compliance.
type mockProcessor struct {
	eventReceipt *operatorv1.EventReceipt
	err          error
}

func (m *mockProcessor) ProcessEnvelope(ctx context.Context, payload []byte) (*operatorv1.EventReceipt, error) {
	return m.eventReceipt, m.err
}

func TestEnvelopeProcessorInterface(t *testing.T) {
	t.Parallel()
	// Verify that mockProcessor implements EnvelopeProcessor
	var _ EnvelopeProcessor = (*mockProcessor)(nil)

	proc := &mockProcessor{
		eventReceipt: &operatorv1.EventReceipt{TransactionId: "test-tx"},
		err:          nil,
	}

	eventReceipt, err := proc.ProcessEnvelope(context.Background(), []byte("{}"))
	require.NoError(t, err)
	require.Equal(t, "test-tx", eventReceipt.TransactionId)
}
