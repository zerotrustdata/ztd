// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

const (
	retryTransient   = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT
	retryUnavailable = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE
)

// scriptedMemberDispatcher fails each dispatch whose position has a failure code
// with a signed FAILED receipt carrying it, and delegates every other dispatch
// to the fake that answers deliberation calls. It records every request,
// failed ones included, and echoes nothing: the Gateway's own result check is
// the dispatch service's.
type scriptedMemberDispatcher struct {
	*fakeMemberDispatcher
	failures []operatorv1.ReceiptFailureCode
	// unsigned fails with the scripted code but without a receipt.
	unsigned bool
	attempts []*operatorv1.InferenceDispatchRequest
}

func (d *scriptedMemberDispatcher) DispatchMember(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, *operatorv1.InferenceDispatchFailure) {
	d.attempts = append(d.attempts, req)
	n := len(d.attempts) - 1
	if n < len(d.failures) && d.failures[n] != operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_UNSPECIFIED {
		failure := &operatorv1.InferenceDispatchFailure{Error: fmt.Sprintf("scripted failure %d", n+1)}
		if code := d.failures[n]; code == retryTransient {
			failure.Code = operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_TRANSIENT
		}
		if !d.unsigned {
			failure.Receipt = &operatorv1.EventReceipt{
				TransactionId: fmt.Sprintf("txn-failed-%d", n+1),
				Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
				FailureCode:   d.failures[n],
			}
		}
		return nil, failure
	}
	return d.fakeMemberDispatcher.DispatchMember(ctx, req)
}

// retryService is a Tribunal over scripted dispatches whose retry waits are
// recorded instead of slept.
func retryService(t *testing.T, dispatcher *scriptedMemberDispatcher) (*Service, *[]time.Duration) {
	t.Helper()
	service, err := NewService(dispatcher, governance.NewL1Doctrine())
	require.NoError(t, err)
	waits := &[]time.Duration{}
	service.retry = inferencedispatch.TransientRetry{
		Delays: inferencedispatch.DefaultTransientRetry().Delays,
		Wait: func(_ context.Context, delay time.Duration) error {
			*waits = append(*waits, delay)
			return nil
		},
	}
	return service, waits
}

func approvingDispatcher(failures ...operatorv1.ReceiptFailureCode) *scriptedMemberDispatcher {
	return &scriptedMemberDispatcher{
		fakeMemberDispatcher: &fakeMemberDispatcher{
			generate: proposals(map[Member]string{MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "netstat -tlpn"}),
			marshal:  lowRisk,
			audit:    []string{approve},
		},
		failures: failures,
	}
}

func TestNewService_DefaultsToTheTransientRetryPolicy(t *testing.T) {
	service, err := NewService(approvingDispatcher(), governance.NewL1Doctrine())
	require.NoError(t, err)
	assert.Equal(t, inferencedispatch.DefaultTransientRetry().Delays, service.retry.Delays)
	require.NotNil(t, service.retry.Wait)
}

func TestDeliberate_RetriesTransientMemberFailureAsNewAttempts(t *testing.T) {
	dispatcher := approvingDispatcher(retryTransient, retryTransient)
	service, waits := retryService(t, dispatcher)

	result, err := service.Deliberate(context.Background(), intentRequest(memberTemplate()))

	require.NoError(t, err)
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, result.GetOutcome())
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second}, *waits, "each retry waits the policy's delay for its ordinal")

	// Two transient failures and one success for the first Generate call, then
	// the four other members, Marshal, and the Auditor.
	calls := result.GetMemberCalls()
	require.Len(t, calls, len(AllMembers)+2+2)
	require.Len(t, dispatcher.attempts, len(calls))
	for i, call := range calls[:3] {
		attemptID := fmt.Sprintf("%s-%d", testPrefix, i+1)
		assert.Equal(t, attemptID, call.GetProviderAttemptId(), "a retry is a new provider attempt")
		assert.Equal(t, attemptID, dispatcher.attempts[i].GetProviderAttemptId())
		assert.Equal(t, uint32(i), call.GetRetryCount())
		assert.Equal(t, uint32(i), dispatcher.attempts[i].GetRetryCount(), "the dispatch carries the retry ordinal")
		assert.Equal(t, tribunalv1.TribunalStage_TRIBUNAL_STAGE_GENERATE, call.GetStage())
		assert.Equal(t, string(MemberAxiom), call.GetPersona())
		assert.Equal(t, uint32(1), call.GetRound())
		if i == 0 {
			assert.Empty(t, call.GetRetryOf())
		} else {
			assert.Equal(t, calls[i-1].GetProviderAttemptId(), call.GetRetryOf())
		}
	}
	for i, call := range calls[:2] {
		require.NotNil(t, call.GetFailure(), "the failed attempt stays recorded")
		assert.True(t, governance.IsTransientFailureReceipt(call.GetReceipt()))
		assert.Equal(t, fmt.Sprintf("txn-failed-%d", i+1), call.GetTransactionId())
		assert.Nil(t, call.GetResult())
	}
	assert.Nil(t, calls[2].GetFailure())
	assert.NotNil(t, calls[2].GetResult(), "the last retry completed")
	for _, call := range calls[3:] {
		assert.Zero(t, call.GetRetryCount(), "a later call starts its own chain")
		assert.Empty(t, call.GetRetryOf())
	}
	for _, attempt := range dispatcher.attempts[3:] {
		assert.Zero(t, attempt.GetRetryCount())
	}
}

func TestDeliberate_ExhaustedRetriesEndAsInferenceFailedOnTheLastFailure(t *testing.T) {
	dispatcher := approvingDispatcher(retryTransient, retryTransient, retryTransient, retryTransient, retryTransient)
	service, waits := retryService(t, dispatcher)

	result, err := service.Deliberate(context.Background(), intentRequest(memberTemplate()))

	require.NoError(t, err)
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, result.GetOutcome())
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second, 40 * time.Second}, *waits)
	calls := result.GetMemberCalls()
	require.Len(t, calls, 1+int(inferencedispatch.MaxTransientRetries()), "the policy bounds the retries")
	last := calls[len(calls)-1]
	assert.Equal(t, inferencedispatch.MaxTransientRetries(), last.GetRetryCount())
	assert.Equal(t, calls[len(calls)-2].GetProviderAttemptId(), last.GetRetryOf())
	assert.Equal(t, "scripted failure 4", last.GetFailure().GetError(), "the last attempt's failure stands")
	assert.Equal(t, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_TRANSIENT, last.GetFailure().GetCode())
	for _, call := range calls {
		assert.NotNil(t, call.GetFailure())
	}
}

func TestDeliberate_DoesNotRetryOtherFailures(t *testing.T) {
	tests := []struct {
		name     string
		dispatch *scriptedMemberDispatcher
	}{
		{name: "non-transient failure", dispatch: approvingDispatcher(retryUnavailable)},
		{name: "transient failure without a signed receipt", dispatch: func() *scriptedMemberDispatcher {
			d := approvingDispatcher(retryTransient)
			d.unsigned = true
			return d
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, waits := retryService(t, tt.dispatch)

			result, err := service.Deliberate(context.Background(), intentRequest(memberTemplate()))

			require.NoError(t, err)
			assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, result.GetOutcome())
			assert.Empty(t, *waits)
			require.Len(t, result.GetMemberCalls(), 1)
			assert.Len(t, tt.dispatch.attempts, 1)
			assert.Zero(t, result.GetMemberCalls()[0].GetRetryCount())
			assert.Empty(t, result.GetMemberCalls()[0].GetRetryOf())
		})
	}
}

func TestDeliberate_WaitThatCannotFitIsNeverStarted(t *testing.T) {
	t.Run("past the context deadline", func(t *testing.T) {
		dispatcher := approvingDispatcher(retryTransient)
		service, waits := retryService(t, dispatcher)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		result, err := service.Deliberate(ctx, intentRequest(memberTemplate()))

		require.NoError(t, err)
		assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, result.GetOutcome())
		assert.Empty(t, *waits, "a 5 s wait cannot end before a 1 s deadline")
		assert.Len(t, result.GetMemberCalls(), 1)
	})
	t.Run("context already ended", func(t *testing.T) {
		dispatcher := approvingDispatcher()
		service, waits := retryService(t, dispatcher)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// The call fails transiently, then the caller ends before the retry.
		service.members = &endingDispatcher{inner: dispatcher, cancel: cancel}

		result, err := service.Deliberate(ctx, intentRequest(memberTemplate()))

		require.NoError(t, err)
		assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, result.GetOutcome())
		assert.Empty(t, *waits)
		assert.Len(t, result.GetMemberCalls(), 1)
	})
	t.Run("wait interrupted", func(t *testing.T) {
		dispatcher := approvingDispatcher(retryTransient)
		service, _ := retryService(t, dispatcher)
		interrupted := 0
		service.retry.Wait = func(context.Context, time.Duration) error {
			interrupted++
			return context.Canceled
		}

		result, err := service.Deliberate(context.Background(), intentRequest(memberTemplate()))

		require.NoError(t, err)
		assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, result.GetOutcome())
		assert.Equal(t, 1, interrupted)
		assert.Len(t, result.GetMemberCalls(), 1, "an interrupted wait dispatches no retry")
	})
	t.Run("no retries in the policy", func(t *testing.T) {
		dispatcher := approvingDispatcher(retryTransient)
		service, waits := retryService(t, dispatcher)
		service.retry.Delays = nil

		result, err := service.Deliberate(context.Background(), intentRequest(memberTemplate()))

		require.NoError(t, err)
		assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, result.GetOutcome())
		assert.Empty(t, *waits)
	})
}

// endingDispatcher fails the first dispatch transiently and cancels the caller
// as it does, so the retry finds the context already ended.
type endingDispatcher struct {
	inner  *scriptedMemberDispatcher
	cancel context.CancelFunc
	failed bool
}

func (d *endingDispatcher) DispatchMember(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, *operatorv1.InferenceDispatchFailure) {
	if d.failed {
		return d.inner.DispatchMember(ctx, req)
	}
	d.failed = true
	d.cancel()
	return nil, &operatorv1.InferenceDispatchFailure{
		Error: "transient",
		Code:  operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_TRANSIENT,
		Receipt: &operatorv1.EventReceipt{
			TransactionId: "txn-failed-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, FailureCode: retryTransient,
		},
	}
}

func TestReviewAdmitted_RetriesTransientAuditorFailureWithinTheEnvelope(t *testing.T) {
	dispatcher := approvingDispatcher(retryTransient)
	service, waits := retryService(t, dispatcher)
	result := admittedResult()
	env := admittedEnvelope(admittedL3Approval(), admittedVote())

	require.NoError(t, service.ReviewAdmitted(context.Background(), admittedRequest(), result, env, admittedVoteInference()))

	assert.Equal(t, []time.Duration{5 * time.Second}, *waits)
	calls := result.GetMemberCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, fmt.Sprintf("%s-1", testPrefix), calls[0].GetProviderAttemptId())
	assert.Equal(t, fmt.Sprintf("%s-2", testPrefix), calls[1].GetProviderAttemptId())
	assert.Equal(t, uint32(1), calls[1].GetRetryCount())
	assert.Equal(t, calls[0].GetProviderAttemptId(), calls[1].GetRetryOf())
	assert.NotNil(t, calls[0].GetFailure())
	assert.Nil(t, calls[1].GetFailure())
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, result.GetOutcome())
}

func TestReviewAdmitted_EnvelopeLifetimeBoundsTheRetryWait(t *testing.T) {
	dispatcher := approvingDispatcher(retryTransient)
	service, waits := retryService(t, dispatcher)
	result := admittedResult()
	env := admittedEnvelope(admittedL3Approval(), admittedVote())
	env.ExpiresAt = timestamppb.New(time.Now().Add(time.Second))

	err := service.ReviewAdmitted(context.Background(), admittedRequest(), result, env, admittedVoteInference())

	require.ErrorIs(t, err, constants.ErrTribunalInferenceFailed)
	assert.Empty(t, *waits, "a 5 s wait cannot end before the envelope expires in 1 s")
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, result.GetOutcome())
	require.Len(t, result.GetMemberCalls(), 1)
	assert.True(t, governance.IsTransientFailureReceipt(result.GetMemberCalls()[0].GetReceipt()))
}

func TestValidateIntentRequest_RejectsTemplateRetryCount(t *testing.T) {
	for stage, mutate := range map[string]func(*tribunalv1.TribunalIntentRequest){
		"member":  func(req *tribunalv1.TribunalIntentRequest) { req.MemberInference.RetryCount = 1 },
		"marshal": func(req *tribunalv1.TribunalIntentRequest) { req.MarshalInference.RetryCount = 2 },
		"auditor": func(req *tribunalv1.TribunalIntentRequest) { req.AuditorInference.RetryCount = 3 },
	} {
		t.Run(stage, func(t *testing.T) {
			req := intentRequest(memberTemplate())
			mutate(req)
			require.ErrorIs(t, ValidateIntentRequest(req), constants.ErrTribunalTemplateInvalid)
		})
	}
}
