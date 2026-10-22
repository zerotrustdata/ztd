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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/uuid"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// seedReadFile returns a read path under the fixture work directory, with a file written
// at it so the embedded Operator has something to read.
func seedReadFile(t *testing.T, fx *directFixture) string {
	t.Helper()
	readAbs := fx.attemptFor(t, cases.Case{ID: "sentinel-read", Action: cases.ActionRead}).ReadAbs
	require.NoError(t, os.WriteFile(readAbs, []byte("canary"), 0o600))
	return readAbs
}

// TestSentinelReadReturnsEventReceiptOfAnExecutedRead checks that a read on the embedded
// executor returns the signed event receipt of a completed execution.
func TestSentinelReadReturnsEventReceiptOfAnExecutedRead(t *testing.T) {
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-sentinel-read",
		Posture:  constants.PostureDoctrine,
	})
	s, err := NewSentinel(gw.client, gw.persona, map[cases.Executor]operator.Target{cases.ExecutorEmbedded: gw.target})
	require.NoError(t, err)
	fx := &directFixture{f: gw.f}

	eventReceipt, err := s.Read(context.Background(), cases.ExecutorEmbedded, seedReadFile(t, fx))
	require.NoError(t, err)
	assert.NotEmpty(t, eventReceipt.GetTransactionId())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, eventReceipt.GetStatus())
}

// TestSentinelEachReadIsAFreshExecution checks that two reads of the same path each run as
// their own execution and return their own signed event receipt.
func TestSentinelEachReadIsAFreshExecution(t *testing.T) {
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-sentinel-fresh",
		Posture:  constants.PostureDoctrine,
	})
	s, err := NewSentinel(gw.client, gw.persona, map[cases.Executor]operator.Target{cases.ExecutorEmbedded: gw.target})
	require.NoError(t, err)
	fx := &directFixture{f: gw.f}
	readAbs := seedReadFile(t, fx)

	first, err := s.Read(context.Background(), cases.ExecutorEmbedded, readAbs)
	require.NoError(t, err)
	second, err := s.Read(context.Background(), cases.ExecutorEmbedded, readAbs)
	require.NoError(t, err)
	assert.NotEqual(t, first.GetTransactionId(), second.GetTransactionId())
}

// TestSentinelReadErrorsOnRefusal checks that a read the Gateway refuses returns an error
// and no event receipt. The session is unknown to the Gateway, so the dispatch ingress refuses it.
func TestSentinelReadErrorsOnRefusal(t *testing.T) {
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-sentinel-refusal",
		Posture:  constants.PostureDoctrine,
	})
	sessionID, err := uuid.NewString()
	require.NoError(t, err)
	unknown := operator.Target{OperatorID: gw.target.OperatorID, OperatorSessionID: sessionID}
	s, err := NewSentinel(gw.client, gw.persona, map[cases.Executor]operator.Target{cases.ExecutorEmbedded: unknown})
	require.NoError(t, err)
	fx := &directFixture{f: gw.f}

	eventReceipt, err := s.Read(context.Background(), cases.ExecutorEmbedded, seedReadFile(t, fx))
	require.Error(t, err)
	assert.Nil(t, eventReceipt)
	assert.True(t, errors.Is(err, constants.ErrEventReceiptMissing), "got %v", err)
}

// TestNewSentinelRequiresATargetAndRefusesAnUntargetedExecutor checks that construction
// fails with no target or no client, and that a sentinel built for one executor refuses to
// read from the other. The client points at an unreachable address: an untargeted read that
// submitted anything would fail at the transport instead of as unsupported.
func TestNewSentinelRequiresATargetAndRefusesAnUntargetedExecutor(t *testing.T) {
	c := unreachableClient(t)
	persona := testIdentity().Persona
	embedded := operator.Target{OperatorID: "embedded-operator", OperatorSessionID: "embedded-session"}

	_, err := NewSentinel(c, persona, map[cases.Executor]operator.Target{})
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "no targets: %v", err)

	s, err := NewSentinel(c, persona, map[cases.Executor]operator.Target{cases.ExecutorEmbedded: embedded})
	require.NoError(t, err)
	eventReceipt, err := s.Read(context.Background(), cases.ExecutorOutbound, "/work/canary.txt")
	assert.Nil(t, eventReceipt)
	assert.True(t, errors.Is(err, constants.ErrConformanceIngressUnsupported), "untargeted executor: %v", err)

	_, err = NewSentinel(nil, persona, map[cases.Executor]operator.Target{cases.ExecutorEmbedded: embedded})
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "nil client: %v", err)
}
