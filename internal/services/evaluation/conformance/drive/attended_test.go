// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// errInnerDrive is the injected transport failure of the inner driver.
var errInnerDrive = errors.New("inner drive: transport down")

// callLog records the order in which the attended driver and its fakes are called.
type callLog struct {
	calls []string
}

// add appends one call name. A nil log records nothing.
func (l *callLog) add(name string) {
	if l != nil {
		l.calls = append(l.calls, name)
	}
}

// fakeDriver returns a fixed answer and records the attempts it was given.
type fakeDriver struct {
	driven   cases.Driven
	err      error
	attempts []cases.Attempt
	log      *callLog
}

func (f *fakeDriver) Drive(_ context.Context, a cases.Attempt) (cases.Driven, error) {
	f.log.add("drive")
	f.attempts = append(f.attempts, a)
	return f.driven, f.err
}

// waitRecord is one call the fake approval stream received.
type waitRecord struct {
	txHash string
	userID string
}

// fakeApprovals is the approval subscriber and the stream it opens. It records each
// subscribe, wait and close, and answers each wait with receipt or waitErr. onWait,
// when set, runs inside Wait the way the Gateway calls the A2A stub after an approval.
type fakeApprovals struct {
	log          *callLog
	subscribeErr error
	receipt      *operatorv1.ActionReceipt
	waitErr      error
	onWait       func()

	subscribes []client.Persona
	waits      []waitRecord
	closes     int
}

func (f *fakeApprovals) Subscribe(_ context.Context, p client.Persona) (approvalStream, error) {
	f.log.add("subscribe")
	f.subscribes = append(f.subscribes, p)
	if f.subscribeErr != nil {
		return nil, f.subscribeErr
	}
	return f, nil
}

func (f *fakeApprovals) Wait(_ context.Context, txHash, userID string) (*operatorv1.ActionReceipt, error) {
	f.log.add("wait")
	f.waits = append(f.waits, waitRecord{txHash: txHash, userID: userID})
	if f.onWait != nil {
		f.onWait()
	}
	return f.receipt, f.waitErr
}

func (f *fakeApprovals) Close() {
	f.log.add("close")
	f.closes++
}

// newTestAttended builds the wrapper through NewAttended, then replaces its approval
// subscriber with the fake.
func newTestAttended(t *testing.T, inner cases.Driver, fa *fakeApprovals) *Attended {
	t.Helper()
	c, err := client.New(config.Config{MTLSBaseURL: "https://127.0.0.1:1", PublicBaseURL: "https://127.0.0.1:1"})
	require.NoError(t, err)
	d := NewAttended(inner, c, testIdentity().Persona, "user-approver")
	d.approvals = fa
	return d
}

// attendedCase is an attended row-24 style attempt with the given fault.
func attendedCase(fault cases.Fault) cases.Attempt {
	a := testAttempt(fault, cases.ActionMutation)
	a.Case.Attended = true
	a.Case.Ingress = cases.IngressMCP
	return a
}

// suspendedDriven is the inner answer for a call the Gateway suspended.
func suspendedDriven(txHash string) cases.Driven {
	return cases.Driven{Observations: []*evalv1.ConformanceObservation{{
		Kind:            evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED,
		SuspendedTxHash: txHash,
	}}}
}

// TestAttendedNonAttendedCasePassesThroughUntouched checks that a case without the
// attended flag reaches the inner driver unchanged, returns its answer and error
// unchanged, and never subscribes to or waits for a human.
func TestAttendedNonAttendedCasePassesThroughUntouched(t *testing.T) {
	a := testAttempt(cases.FaultNone, cases.ActionMutation)
	a.Case.Attended = false
	answer := cases.Driven{
		Observations: []*evalv1.ConformanceObservation{{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT}},
		A2ACalls:     []*evalv1.ConformanceA2ACall{{SkillName: "ztd-conformance"}},
	}

	t.Run("answer", func(t *testing.T) {
		inner := &fakeDriver{driven: answer}
		fa := &fakeApprovals{}
		driven, err := newTestAttended(t, inner, fa).Drive(context.Background(), a)
		require.NoError(t, err)
		assert.Equal(t, answer.Observations[0].GetKind(), driven.Observations[0].GetKind())
		assert.Len(t, driven.A2ACalls, 1)
		assert.Equal(t, []cases.Attempt{a}, inner.attempts)
		assert.Empty(t, fa.subscribes)
		assert.Empty(t, fa.waits)
	})

	t.Run("error", func(t *testing.T) {
		inner := &fakeDriver{err: errInnerDrive}
		fa := &fakeApprovals{}
		_, err := newTestAttended(t, inner, fa).Drive(context.Background(), a)
		assert.ErrorIs(t, err, errInnerDrive)
		assert.Empty(t, fa.subscribes)
		assert.Empty(t, fa.waits)
	})
}

// TestAttendedReturnsNonSuspendedAnswerAsIs checks that an attended case whose
// inner answer is not exactly one SUSPENDED observation is returned as the inner
// driver gave it, with no wait. The verifier grades that evidence.
func TestAttendedReturnsNonSuspendedAnswerAsIs(t *testing.T) {
	tests := []struct {
		name  string
		inner cases.Driven
	}{
		{
			name: "rejection",
			inner: cases.Driven{Observations: []*evalv1.ConformanceObservation{{
				Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
			}}},
		},
		{
			name: "refusal",
			inner: cases.Driven{Observations: []*evalv1.ConformanceObservation{{
				Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
			}}},
		},
		{
			name: "receipt",
			inner: cases.Driven{Observations: []*evalv1.ConformanceObservation{{
				Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
				Receipt: &operatorv1.ActionReceipt{TransactionHash: "tx-1"},
			}}},
		},
		{
			name: "two suspensions",
			inner: cases.Driven{Observations: []*evalv1.ConformanceObservation{
				{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED, SuspendedTxHash: "tx-1"},
				{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED, SuspendedTxHash: "tx-2"},
			}},
		},
		{
			name:  "no observation",
			inner: cases.Driven{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeDriver{driven: tt.inner}
			fa := &fakeApprovals{}
			driven, err := newTestAttended(t, inner, fa).Drive(context.Background(), attendedCase(cases.FaultAttendedApprove))
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(tt.inner.Observations))
			for i := range tt.inner.Observations {
				assert.True(t, proto.Equal(tt.inner.Observations[i], driven.Observations[i]), "observation %d must be returned as the inner driver gave it", i)
			}
			assert.Empty(t, fa.waits, "a non-suspended answer must never wait for a human")
			assert.Equal(t, 1, fa.closes, "the subscription must close on this path")
		})
	}
}

// TestAttendedInnerErrorIsWrappedAndNoWait checks that a transport failure of the
// inner drive is returned with its cause, never waits for a human, and still closes
// the subscription.
func TestAttendedInnerErrorIsWrappedAndNoWait(t *testing.T) {
	inner := &fakeDriver{err: errInnerDrive}
	fa := &fakeApprovals{}
	_, err := newTestAttended(t, inner, fa).Drive(context.Background(), attendedCase(cases.FaultAttendedApprove))
	assert.ErrorIs(t, err, errInnerDrive)
	assert.Empty(t, fa.waits)
	assert.Equal(t, 1, fa.closes)
}

// TestAttendedSubscribeErrorSubmitsNothing checks that a failed approval subscription is
// returned with its cause and the attempt is never submitted, so no call can suspend
// without a stream to report the decision.
func TestAttendedSubscribeErrorSubmitsNothing(t *testing.T) {
	errSubscribe := errors.New("approval subscription: connect: refused")
	inner := &fakeDriver{driven: suspendedDriven("tx-hash-1")}
	fa := &fakeApprovals{subscribeErr: errSubscribe}

	driven, err := newTestAttended(t, inner, fa).Drive(context.Background(), attendedCase(cases.FaultAttendedApprove))
	assert.ErrorIs(t, err, errSubscribe)
	assert.Empty(t, driven.Observations)
	assert.Empty(t, inner.attempts, "the attempt must not be submitted without an approval stream")
	assert.Empty(t, fa.waits)
}

// TestAttendedStreamOrderAndCloseOnEveryPath checks that the approval stream is opened
// before the inner drive, awaited only after a suspension, and closed on every path.
func TestAttendedStreamOrderAndCloseOnEveryPath(t *testing.T) {
	tests := []struct {
		name     string
		inner    cases.Driven
		innerErr error
		waitErr  error
		want     []string
	}{
		{
			name:  "approve",
			inner: suspendedDriven("tx-hash-1"),
			want:  []string{"subscribe", "drive", "wait", "close"},
		},
		{
			name:  "answer that is not a suspension",
			inner: cases.Driven{},
			want:  []string{"subscribe", "drive", "close"},
		},
		{
			name:     "inner error",
			innerErr: errInnerDrive,
			want:     []string{"subscribe", "drive", "close"},
		},
		{
			name:    "wait error",
			inner:   suspendedDriven("tx-hash-1"),
			waitErr: errors.New("approval wait: sse closed"),
			want:    []string{"subscribe", "drive", "wait", "close"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &callLog{}
			inner := &fakeDriver{driven: tt.inner, err: tt.innerErr, log: log}
			fa := &fakeApprovals{log: log, receipt: &operatorv1.ActionReceipt{TransactionHash: "tx-hash-1"}, waitErr: tt.waitErr}

			_, _ = newTestAttended(t, inner, fa).Drive(context.Background(), attendedCase(cases.FaultAttendedApprove))
			assert.Equal(t, tt.want, log.calls)
			assert.Equal(t, 1, fa.closes, "the subscription must close exactly once")
		})
	}
}

// TestAttendedApproveReturnsOneReceipt checks row 24: a suspended attended case
// waits on the suspended transaction for the approver, and the resumed receipt
// replaces the suspension as the single observation.
func TestAttendedApproveReturnsOneReceipt(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionHash: "tx-hash-1",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}
	innerCalls := []*evalv1.ConformanceA2ACall{{SkillName: "ztd-conformance"}}
	inner := &fakeDriver{driven: cases.Driven{
		Observations: suspendedDriven("tx-hash-1").Observations,
		A2ACalls:     innerCalls,
	}}
	fa := &fakeApprovals{receipt: receipt}
	a := attendedCase(cases.FaultAttendedApprove)

	driven, err := newTestAttended(t, inner, fa).Drive(context.Background(), a)
	require.NoError(t, err)

	require.Len(t, driven.Observations, 1)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, driven.Observations[0].GetKind())
	assert.True(t, proto.Equal(receipt, driven.Observations[0].GetReceipt()), "the resumed receipt must be the observation")
	assert.Equal(t, innerCalls, driven.A2ACalls, "the inner driver's A2A calls are kept")

	require.Len(t, fa.subscribes, 1)
	assert.Equal(t, testIdentity().Persona, fa.subscribes[0], "the subscription must use the attended persona")
	require.Len(t, fa.waits, 1)
	assert.Equal(t, "tx-hash-1", fa.waits[0].txHash, "the wait must watch the suspended transaction")
	assert.Equal(t, "user-approver", fa.waits[0].userID)
}

// TestAttendedWaitErrorIsReturned checks that a failed approval wait is an error,
// with the cause kept, and no observation is reported for it.
func TestAttendedWaitErrorIsReturned(t *testing.T) {
	errWait := errors.New("approval wait: sse closed")
	inner := &fakeDriver{driven: suspendedDriven("tx-hash-1")}
	fa := &fakeApprovals{waitErr: errWait}

	driven, err := newTestAttended(t, inner, fa).Drive(context.Background(), attendedCase(cases.FaultAttendedApprove))
	assert.ErrorIs(t, err, errWait)
	assert.Empty(t, driven.Observations)
}
