// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package judge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	transientFailure   = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT
	unavailableFailure = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE
	retryVerdict       = `{"met": true, "rationale": "declines and cites policy"}`
)

// governedFailure is the error the real dispatch path returns for a provider
// failure the Inference Operator signed: the FAILED receipt rides inside the
// error (client.inferenceDispatchFailure), and eval's governed dispatcher wraps
// that once more. The response is nil.
func governedFailure(code operatorv1.ReceiptFailureCode, transactionID string) error {
	sentinel := constants.ErrInferenceBackendUnavailable
	if code == transientFailure {
		sentinel = constants.ErrInferenceBackendTransient
	}
	receipt := &operatorv1.EventReceipt{TransactionId: transactionID, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, FailureCode: code}
	cause := fmt.Errorf("%w: inference dispatch returned status 502: %w", constants.ErrHTTPStatusError, sentinel)
	wrapped := inferencedispatch.WithObservationReceipts([]*operatorv1.EventReceipt{{TransactionId: "tx-observation"}}, governance.WithFailedReceipt(receipt, cause))
	return fmt.Errorf("evaluation: governed inference dispatch: %w", wrapped)
}

// retriedResponse answers req as the Inference Operator does: the result echoes
// the request's retry ordinal.
func retriedResponse(req *operatorv1.InferenceDispatchRequest, text string) *operatorv1.InferenceDispatchResponse {
	resp := judgmentResponse(req, text)
	resp.Result.RetryCount = req.GetRetryCount()
	return resp
}

// retryHarness is a judge whose dispatches fail as scripted and whose retry
// waits are recorded, never slept.
type retryHarness struct {
	t          *testing.T
	judge      *Judge
	dispatcher *fakeDispatcher
	waits      []time.Duration
	observer   *recordingObserver
	ids        int
}

// newRetryHarness scripts failures by dispatch position: a nil entry answers
// with a verdict, a non-nil entry fails the dispatch with that error.
func newRetryHarness(t *testing.T, failures ...error) *retryHarness {
	t.Helper()
	h := &retryHarness{t: t, observer: &recordingObserver{}}
	h.dispatcher = &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		n := len(h.dispatcher.requests) - 1
		if n < len(failures) && failures[n] != nil {
			return nil, failures[n]
		}
		return retriedResponse(req, retryVerdict), nil
	}}
	h.judge = newTestJudge(t, h.dispatcher, func(cfg *Config) {
		cfg.NewID = func(prefix string) string {
			h.ids++
			return fmt.Sprintf("%s-%d", prefix, h.ids)
		}
		cfg.Observer = h.observer
		cfg.Retry = inferencedispatch.TransientRetry{
			Delays: inferencedispatch.DefaultTransientRetry().Delays,
			Wait: func(_ context.Context, delay time.Duration) error {
				h.waits = append(h.waits, delay)
				return nil
			},
		}
	})
	return h
}

func (h *retryHarness) criterion(ctx context.Context) Score {
	h.t.Helper()
	return h.judge.ScoreCriterion(ctx, refusalCriterion(), onlyPosition, judgeObjective, criterionReply)
}

func TestScoreCriterion_RetriesTransientFailureAsNewAttempts(t *testing.T) {
	h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"), governedFailure(transientFailure, "tx-1"))

	score := h.criterion(t.Context())

	require.Equal(t, StatusScored, score.Status, score.Detail)
	assert.True(t, score.Met)
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second}, h.waits)
	require.Len(t, h.dispatcher.requests, 3)
	attempts := make(map[string]bool)
	for i, req := range h.dispatcher.requests {
		assert.Equal(t, uint32(i), req.GetRetryCount(), "the dispatch carries the retry ordinal")
		assert.Equal(t, req.GetProviderAttemptId(), req.GetEvaluationAttemptId())
		assert.False(t, attempts[req.GetProviderAttemptId()], "a retry is a new provider attempt")
		attempts[req.GetProviderAttemptId()] = true
	}
	assert.Equal(t, h.dispatcher.requests[2].GetProviderAttemptId(), score.ProviderAttemptID)
	assert.Equal(t, uint32(2), score.RetryCount)
	require.Len(t, score.Superseded, 2)
	for i, superseded := range score.Superseded {
		assert.Equal(t, h.dispatcher.requests[i].GetProviderAttemptId(), superseded.ProviderAttemptID)
		assert.Equal(t, fmt.Sprintf("tx-%d", i), superseded.TransactionID)
		assert.Equal(t, uint32(i), superseded.RetryCount)
		assert.True(t, governance.IsTransientFailureReceipt(superseded.Receipt))
	}
	assert.Equal(t, h.dispatcher.requests[0].GetProviderAttemptId(), score.FirstAttemptID())
	assert.Equal(t, h.dispatcher.requests[0].GetMessages(), h.dispatcher.requests[2].GetMessages(), "a retry asks the same question")

	_, _, err := VerifyCriterionResult(judgeConfig(t, nil), score.ProviderAttemptID, score.RetryCount, score.Result, score.Receipt)
	require.NoError(t, err)
	_, _, err = VerifyCriterionResult(judgeConfig(t, nil), score.ProviderAttemptID, 0, score.Result, score.Receipt)
	require.ErrorIs(t, err, constants.ErrIdentityBindingFailed, "the signed result must echo the attempt's retry ordinal")
}

func TestScoreCriterion_ObserverSeesOneJudgmentWhateverTheAttempts(t *testing.T) {
	h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"))
	tick := time.Unix(1_700_000_000, 0)
	h.judge.cfg.Clock = func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}

	score := h.criterion(t.Context())

	require.Equal(t, StatusScored, score.Status)
	assert.Equal(t, []string{"started:criterion", "completed:criterion:1s"}, h.observer.calls,
		"one started and one completed per judgment; the elapsed time is the final attempt's, not the superseded attempt or the wait")
	started, completed := h.observer.stats[0], h.observer.stats[len(h.observer.stats)-1]
	assert.Equal(t, started.FirstAttemptID(), completed.FirstAttemptID(), "the judgment keeps one identity across its attempts")
	assert.Empty(t, started.Superseded)
	require.Len(t, completed.Superseded, 1)
	assert.NotEqual(t, started.ProviderAttemptID, completed.ProviderAttemptID)
}

func TestScoreCriterion_ExhaustedRetriesEndAsDispatchFailureWithSupersededAttempts(t *testing.T) {
	h := newRetryHarness(t,
		governedFailure(transientFailure, "tx-0"), governedFailure(transientFailure, "tx-1"),
		governedFailure(transientFailure, "tx-2"), governedFailure(transientFailure, "tx-3"))

	score := h.criterion(t.Context())

	assert.Equal(t, StatusFailed, score.Status)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, score.Failure)
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second, 40 * time.Second}, h.waits)
	assert.Len(t, h.dispatcher.requests, 4, "the policy bounds the retries")
	assert.Equal(t, inferencedispatch.MaxTransientRetries(), score.RetryCount)
	require.Len(t, score.Superseded, int(inferencedispatch.MaxTransientRetries()))
	assert.Empty(t, score.TransactionID, "a failed dispatch records no receipt")
	assert.Nil(t, score.Receipt)
	assert.Nil(t, score.Result)
	assert.Equal(t, h.dispatcher.requests[3].GetProviderAttemptId(), score.ProviderAttemptID)
	require.Len(t, h.observer.stats, 2, "one started and one completed for the whole judgment")
	assert.Equal(t, score.FirstAttemptID(), h.observer.stats[0].ProviderAttemptID)
	assert.Contains(t, score.Detail, constants.ErrInferenceBackendTransient.Error())
}

func TestScoreCriterion_NonTransientFailureIsNotRetried(t *testing.T) {
	for name, failure := range map[string]error{
		"signed non-transient failure":            governedFailure(unavailableFailure, "tx-0"),
		"transient error, no receipt":             fmt.Errorf("evaluation: governed inference dispatch: %w", constants.ErrInferenceBackendTransient),
		"transient receipt without a transaction": governedFailure(transientFailure, ""),
	} {
		t.Run(name, func(t *testing.T) {
			h := newRetryHarness(t, failure)

			score := h.criterion(t.Context())

			assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, score.Failure)
			assert.Empty(t, h.waits)
			assert.Len(t, h.dispatcher.requests, 1)
			assert.Empty(t, score.Superseded)
			assert.Zero(t, score.RetryCount)
		})
	}
}

func TestScoreCriterion_ResultFailuresAreNotRetried(t *testing.T) {
	h := newRetryHarness(t)
	h.dispatcher.respond = func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		return retriedResponse(req, "not a verdict"), nil
	}

	score := h.criterion(t.Context())

	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID, score.Failure)
	assert.Empty(t, h.waits)
	assert.Len(t, h.dispatcher.requests, 1)
}

func TestScoreCriterion_WaitThatCannotFitIsNeverStarted(t *testing.T) {
	t.Run("past the context deadline", func(t *testing.T) {
		h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"))
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		score := h.criterion(ctx)

		assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, score.Failure)
		assert.Empty(t, h.waits, "a 5 s wait cannot end before a 1 s deadline")
		assert.Len(t, h.dispatcher.requests, 1)
		assert.Empty(t, score.Superseded)
	})
	t.Run("context already ended", func(t *testing.T) {
		h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"))
		ctx, cancel := context.WithCancel(t.Context())
		h.dispatcher.respond = func(*operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			cancel()
			return nil, governedFailure(transientFailure, "tx-0")
		}

		score := h.criterion(ctx)

		assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, score.Failure)
		assert.Empty(t, h.waits)
		assert.Len(t, h.dispatcher.requests, 1)
	})
	t.Run("wait cut short", func(t *testing.T) {
		h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"))
		h.judge.cfg.Retry.Wait = func(context.Context, time.Duration) error { return context.Canceled }

		score := h.criterion(t.Context())

		assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, score.Failure)
		assert.Len(t, h.dispatcher.requests, 1)
		assert.Empty(t, score.Superseded, "an attempt is superseded only when its retry is dispatched")
	})
}

func TestScoreCriterion_WithoutAPolicyEveryJudgmentIsDispatchedOnce(t *testing.T) {
	h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"))
	h.judge.cfg.Retry = inferencedispatch.TransientRetry{}

	score := h.criterion(t.Context())

	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, score.Failure)
	assert.Len(t, h.dispatcher.requests, 1)
}

func TestScoreCriterion_RetryNeverReusesAnAttemptID(t *testing.T) {
	h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"))
	h.judge.cfg.NewID = func(prefix string) string { return prefix }

	score := h.criterion(t.Context())

	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, score.Failure)
	assert.Len(t, h.dispatcher.requests, 1, "a retry under the failed attempt's ID would forge two attempts out of one")
	assert.Empty(t, h.waits)
}

func TestScoreCriterion_RetriedResultMustEchoItsRetryOrdinal(t *testing.T) {
	h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"))
	h.dispatcher.respond = func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		if req.GetRetryCount() == 0 {
			return nil, governedFailure(transientFailure, "tx-0")
		}
		resp := judgmentResponse(req, retryVerdict)
		resp.Result.RetryCount = 0
		return resp, nil
	}

	score := h.criterion(t.Context())

	assert.Equal(t, StatusFailed, score.Status)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_RESPONSE_MISMATCH, score.Failure)
	require.Len(t, score.Superseded, 1)
}

func TestScoreTrace_RetriesQualityJudgments(t *testing.T) {
	h := newRetryHarness(t, governedFailure(transientFailure, "tx-0"))
	h.dispatcher.respond = func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		if req.GetRetryCount() == 0 {
			return nil, governedFailure(transientFailure, "tx-0")
		}
		return retriedResponse(req, `{"score": 4, "rationale": "answers the objective"}`), nil
	}

	scores := h.judge.ScoreTrace(t.Context(), trace(judgeFinalReply), judgeObjective, nil)

	require.Len(t, scores, 1)
	assert.Equal(t, StatusScored, scores[0].Status)
	assert.Equal(t, 4, scores[0].Value)
	assert.Equal(t, uint32(1), scores[0].RetryCount)
	require.Len(t, scores[0].Superseded, 1)
	value, _, err := VerifyQualityResult(judgeConfig(t, nil), scores[0].ProviderAttemptID, scores[0].RetryCount, scores[0].Result, scores[0].Receipt)
	require.NoError(t, err)
	assert.Equal(t, 4, value)
}
