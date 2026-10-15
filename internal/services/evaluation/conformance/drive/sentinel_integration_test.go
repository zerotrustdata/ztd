// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package drive

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TestSentinelReadReturnsReceiptAndSendsFsRead checks that a RECEIPT answer
// returns the signed receipt, and that the request is a FS_READ of the path.
func TestSentinelReadReturnsReceiptAndSendsFsRead(t *testing.T) {
	fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
	s, err := NewSentinel(c, testIdentity().Persona, testDispatchTargets())
	require.NoError(t, err)

	receipt, err := s.Read(context.Background(), cases.ExecutorOutbound, "/tmp/conformance/read.txt")
	require.NoError(t, err)
	assert.Equal(t, "tx-1", receipt.GetTransactionId())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())

	require.Equal(t, 1, fake.count())
	req := decodeDispatchRequest(t, fake.recorded(t, 0))
	assert.Equal(t, "outbound-session", req.TargetOperatorSessionID)
	assert.Equal(t, string(constants.ActionTypeFsRead), req.ActionType)
	assert.Equal(t, string(constants.EventOperatorFilesystemReadRequested), req.EventType)
	read := &operatorv1.FsReadRequested{}
	require.NoError(t, proto.Unmarshal(req.Payload, read))
	assert.Equal(t, "/tmp/conformance/read.txt", read.GetPath())
	assert.NotEmpty(t, read.GetExecutionId())
}

// TestSentinelReadUsesFreshExecutionIDPerCall checks that two reads carry two
// different execution ids.
func TestSentinelReadUsesFreshExecutionIDPerCall(t *testing.T) {
	fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
	s, err := NewSentinel(c, testIdentity().Persona, testDispatchTargets())
	require.NoError(t, err)

	for range 2 {
		_, err := s.Read(context.Background(), cases.ExecutorEmbedded, "/tmp/conformance/read.txt")
		require.NoError(t, err)
	}
	require.Equal(t, 2, fake.count())
	first := &operatorv1.FsReadRequested{}
	second := &operatorv1.FsReadRequested{}
	require.NoError(t, proto.Unmarshal(decodeDispatchRequest(t, fake.recorded(t, 0)).Payload, first))
	require.NoError(t, proto.Unmarshal(decodeDispatchRequest(t, fake.recorded(t, 1)).Payload, second))
	assert.NotEqual(t, first.GetExecutionId(), second.GetExecutionId())
}

// TestSentinelReadErrorsOnNonReceiptOutcomes checks that a rejection, a refusal
// and an untyped body each return an error and no receipt.
func TestSentinelReadErrorsOnNonReceiptOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   func(t *testing.T) []byte
		target error
	}{
		{
			name:   "signed rejection",
			status: http.StatusForbidden,
			body:   rejectionBody,
			target: constants.ErrActionReceiptMissing,
		},
		{
			name:   "typed refusal",
			status: http.StatusBadRequest,
			body:   refusalBody,
			target: constants.ErrActionReceiptMissing,
		},
		{
			name:   "untyped body",
			status: http.StatusInternalServerError,
			body:   func(*testing.T) []byte { return []byte("internal failure") },
			target: constants.ErrIngressResponseUntyped,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, c := newFakeDispatch(t, tt.status, tt.body(t))
			s, err := NewSentinel(c, testIdentity().Persona, testDispatchTargets())
			require.NoError(t, err)

			receipt, err := s.Read(context.Background(), cases.ExecutorEmbedded, "/tmp/conformance/read.txt")
			require.Error(t, err)
			assert.Nil(t, receipt)
			assert.True(t, errors.Is(err, tt.target), "got %v", err)
		})
	}
}

// TestNewSentinelRequiresATargetAndRefusesAnUntargetedExecutor checks that
// construction fails with no target or no client, and that a sentinel built for
// one executor refuses to read from the other.
func TestNewSentinelRequiresATargetAndRefusesAnUntargetedExecutor(t *testing.T) {
	_, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
	persona := testIdentity().Persona

	_, err := NewSentinel(c, persona, map[cases.Executor]operator.Target{})
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "no targets: %v", err)

	s, err := NewSentinel(c, persona, map[cases.Executor]operator.Target{
		cases.ExecutorEmbedded: testDispatchTargets()[cases.ExecutorEmbedded],
	})
	require.NoError(t, err)
	receipt, err := s.Read(context.Background(), cases.ExecutorOutbound, "/tmp/canary.txt")
	assert.Nil(t, receipt)
	assert.True(t, errors.Is(err, constants.ErrConformanceIngressUnsupported), "untargeted executor: %v", err)

	_, err = NewSentinel(nil, persona, testDispatchTargets())
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "nil client: %v", err)
}
