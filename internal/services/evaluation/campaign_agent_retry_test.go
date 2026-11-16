// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// scriptedInference fails each dispatch whose position has a failure code with
// a signed FAILED receipt carrying that code, and answers every other dispatch
// with a result echoing the request's retry ordinal.
type scriptedInference struct {
	AgentInferenceDispatcher
	failures []operatorv1.ReceiptFailureCode
	requests []*operatorv1.InferenceDispatchRequest
}

func (d *scriptedInference) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	d.requests = append(d.requests, req)
	n := len(d.requests) - 1
	if n < len(d.failures) && d.failures[n] != operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_UNSPECIFIED {
		code := d.failures[n]
		sentinel := constants.ErrInferenceBackendUnavailable
		if code == operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT {
			sentinel = constants.ErrInferenceBackendTransient
		}
		receipt := &operatorv1.EventReceipt{TransactionId: "tx-" + req.GetProviderAttemptId(), Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, FailureCode: code}
		return nil, &governance.ReceiptError{Receipt: receipt, Err: sentinel}
	}
	generation := int64(1_000_000_000)
	result := &operatorv1.InferenceResult{
		ProviderAttemptId:    req.GetProviderAttemptId(),
		RequestedModel:       req.GetModel(),
		ResultDigest:         "digest-" + req.GetProviderAttemptId(),
		CampaignId:           req.GetCampaignId(),
		ModelRegistryDigest:  req.GetModelRegistryDigest(),
		RetryCount:           req.GetRetryCount(),
		UsageReported:        true,
		CompletionTokens:     20,
		GenerationDurationNs: &generation,
		Parts:                []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: "answer"}}},
	}
	return &operatorv1.InferenceDispatchResponse{
		TransactionId: "tx-" + req.GetProviderAttemptId(),
		Result:        result,
		Receipt:       &operatorv1.EventReceipt{TransactionId: "tx-" + req.GetProviderAttemptId(), Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ResultSummary: result.GetResultDigest()},
	}, nil
}

// retryRun is an agent run over scripted inference whose retry waits are
// recorded instead of slept.
func retryRun(t *testing.T, failures ...operatorv1.ReceiptFailureCode) (*agentRun, *scriptedInference, *[]time.Duration) {
	t.Helper()
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	dispatcher := &scriptedInference{failures: failures}
	waits := &[]time.Duration{}
	ids := 0
	executor := &CampaignAgentExecutor{
		inference: dispatcher,
		catalog:   catalog,
		newID: func(prefix string) string {
			ids++
			return fmt.Sprintf("%s-%d", prefix, ids)
		},
		retry: inferencedispatch.TransientRetry{
			Delays: inferencedispatch.DefaultTransientRetry().Delays,
			Wait: func(_ context.Context, delay time.Duration) error {
				*waits = append(*waits, delay)
				return nil
			},
		},
	}
	run, err := executor.newRun(heterogeneousLiteRequest(t))
	require.NoError(t, err)
	return run, dispatcher, waits
}

func inferTurn(t *testing.T, ctx context.Context, run *agentRun) (*operatorv1.InferenceResult, *AgentModelCall) {
	t.Helper()
	messages, err := run.conversation()
	require.NoError(t, err)
	return run.infer(ctx, AgentModelCall{Purpose: agentModelCallPurposeTurn, LoopTurn: 1}, messages, nil)
}

const transientCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT

func TestAgentRunInfer_RetriesTransientFailureAsNewAttempts(t *testing.T) {
	run, dispatcher, waits := retryRun(t, transientCode, transientCode)
	budget, cancel := newScenarioBudget(t.Context(), time.Hour)
	defer cancel()
	run.budget = budget
	before, _ := budget.Deadline()

	result, call := inferTurn(t, budget, run)

	require.NotNil(t, result)
	require.NotNil(t, call)
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second}, *waits)
	after, _ := budget.Deadline()
	assert.Equal(t, 25*time.Second, after.Sub(before), "each wait is credited to the scenario budget")
	require.Len(t, dispatcher.requests, 3)
	require.Len(t, run.trace.ModelCalls, 3, "every receipted attempt stays on the trace")
	for i, attempt := range run.trace.ModelCalls {
		assert.Equal(t, uint32(i), attempt.RetryCount)
		assert.Equal(t, uint32(i), dispatcher.requests[i].GetRetryCount(), "the dispatch carries the retry ordinal")
		assert.Equal(t, attempt.ProviderAttemptID, dispatcher.requests[i].GetProviderAttemptId())
		if i > 0 {
			assert.Equal(t, run.trace.ModelCalls[i-1].ProviderAttemptID, attempt.RetryOf)
			assert.NotEqual(t, run.trace.ModelCalls[i-1].ProviderAttemptID, attempt.ProviderAttemptID, "a retry is a new provider attempt")
		} else {
			assert.Empty(t, attempt.RetryOf)
		}
	}
	assert.Equal(t, string(ExecutionFailureProvider), run.trace.ModelCalls[0].Error)
	assert.True(t, run.trace.ModelCalls[2].Completed())
	assert.Equal(t, run.trace.ModelCalls[2].ProviderAttemptID, call.ProviderAttemptID)
	assert.Empty(t, run.trace.ProviderFailure, "a superseded attempt is not the trace's provider failure")
	assert.Empty(t, run.trace.ProviderFailureClass)
	require.NoError(t, verifyRetryChains(run.trace))
	assert.Equal(t, map[string]bool{run.trace.ModelCalls[0].ProviderAttemptID: true, run.trace.ModelCalls[1].ProviderAttemptID: true}, run.trace.supersededAttempts())
}

func TestAgentRunInfer_ExhaustedRetriesEndAsTransientProviderFailure(t *testing.T) {
	run, dispatcher, waits := retryRun(t, transientCode, transientCode, transientCode, transientCode)

	result, call := inferTurn(t, t.Context(), run)

	assert.Nil(t, result)
	assert.Nil(t, call)
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second, 40 * time.Second}, *waits)
	assert.Len(t, dispatcher.requests, 4, "the policy bounds the retries")
	require.Len(t, run.trace.ModelCalls, 4)
	assert.Equal(t, inferencedispatch.MaxTransientRetries(), run.trace.ModelCalls[3].RetryCount)
	assert.Equal(t, string(ExecutionFailureProvider), run.trace.ProviderFailure)
	assert.Equal(t, string(inference.AttemptFailureBackendTransient), run.trace.ProviderFailureClass)
	assert.NotEqual(t, AgentTerminalClassificationTimeout, run.trace.TerminalClassification)
	require.NoError(t, verifyRetryChains(run.trace))
	assert.Len(t, run.trace.supersededAttempts(), 3, "the last attempt is the failure, not superseded")
}

func TestAgentRunInfer_NonTransientFailureIsNotRetried(t *testing.T) {
	run, dispatcher, waits := retryRun(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE)

	result, _ := inferTurn(t, t.Context(), run)

	assert.Nil(t, result)
	assert.Empty(t, *waits)
	assert.Len(t, dispatcher.requests, 1)
	require.Len(t, run.trace.ModelCalls, 1)
	assert.Equal(t, string(inference.AttemptFailureBackendUnavailable), run.trace.ProviderFailureClass)
}

func TestAgentRunInfer_WaitThatCannotFitIsNeverStarted(t *testing.T) {
	t.Run("past the assignment deadline", func(t *testing.T) {
		run, dispatcher, waits := retryRun(t, transientCode)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		result, _ := inferTurn(t, ctx, run)

		assert.Nil(t, result)
		assert.Empty(t, *waits, "a 5 s wait cannot end before a 1 s deadline")
		assert.Len(t, dispatcher.requests, 1)
		assert.Equal(t, string(inference.AttemptFailureBackendTransient), run.trace.ProviderFailureClass)
		assert.NotEqual(t, AgentTerminalClassificationTimeout, run.trace.TerminalClassification, "the provider's wait is never a model timeout")
	})
	t.Run("budget credit past the assignment deadline", func(t *testing.T) {
		run, dispatcher, waits := retryRun(t, transientCode)
		parent, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		budget, stop := newScenarioBudget(parent, time.Minute-2*time.Second)
		defer stop()
		run.budget = budget

		result, _ := inferTurn(t, budget, run)

		assert.Nil(t, result)
		assert.Empty(t, *waits, "the budget cannot credit the whole wait before the assignment deadline")
		assert.Len(t, dispatcher.requests, 1)
		assert.Equal(t, string(inference.AttemptFailureBackendTransient), run.trace.ProviderFailureClass)
	})
}

// A throttled-then-retried call keeps a completed assignment's performance
// metrics: the superseded attempt is a RETRIED record that no metric selects.
func TestModelInferenceRecords_RetriedAttemptsAreNotMetrics(t *testing.T) {
	run, _, _ := retryRun(t, transientCode)
	_, call := inferTurn(t, t.Context(), run)
	require.NotNil(t, call)

	records, span, err := modelInferenceRecordsFromAgentTrace(run.req, run.trace, func(prefix string) string { return prefix })
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_RETRIED, records[0].GetClassification())
	assert.Equal(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN, records[1].GetClassification())
	assert.Equal(t, evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_UNAVAILABLE, records[0].GetUsageAvailability())
	require.NotNil(t, span)
	assert.Equal(t, uint64(1_000_000_000), *span)
	assert.Len(t, evidenceModelInferences(&evalv1.EvaluationAssignmentResult{ModelInferences: records}), 2, "evidence walks every attempt")

	assignments := []*evalv1.EvaluationAssignment{homogeneousAssignment("assign-1", "qwen3-4b", evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY)}
	assignments[0].LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	result := terminalResultWithEvidence("assign-1", span, records...)
	state, err := CollectRunAggregateState(assignments, map[string]*evalv1.EvaluationAssignmentResult{"assign-1": result})
	require.NoError(t, err)
	require.NotNil(t, state.Headline.LatencyP50MS.Value)
	require.NotNil(t, state.Headline.OutputTokensP50.Value)
	require.NotNil(t, state.Headline.OutputThroughputP50.Value, "the retried attempt is not an incomplete contributor")
	assert.InDelta(t, 20.0, *state.Headline.OutputThroughputP50.Value, 0)
	assert.Empty(t, publicUnavailableMetricReasons(result))
	assert.Empty(t, collectUnavailableMetricReasons(result))
}

func TestAgentTrace_SupersededAttemptsLeaveConversationAndSteps(t *testing.T) {
	run, _, _ := retryRun(t, transientCode)
	_, call := inferTurn(t, t.Context(), run)
	require.NotNil(t, call)
	call.Text = "answer"
	run.trace.FinalOutput = call.Text
	run.trace.Input = &AgentTraceInput{Messages: []AgentTraceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "Investigate."}}}

	assert.InDelta(t, 1.0, computeStepsToDiagnosis(run.trace), 1e-9, "a retried turn is one step")
	assert.True(t, singleTriageResultCall(run.trace), "a retried single answer is still a single call")
	messages, err := run.trace.Conversation()
	require.NoError(t, err)
	assert.Equal(t, "answer", messages[len(messages)-1].Text, "the conversation continues past the superseded attempt")
}

func TestVerifyRetryChains_RejectsForgedRetries(t *testing.T) {
	transient := func(attempt string, retry uint32, retryOf string) AgentModelCall {
		return AgentModelCall{
			ProviderAttemptID: attempt, TransactionID: "tx-" + attempt, Purpose: agentModelCallPurposeTurn, LoopTurn: 1,
			RetryCount: retry, RetryOf: retryOf, Error: string(ExecutionFailureProvider),
			Receipt: &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, FailureCode: transientCode}},
		}
	}
	completed := func(attempt string, retry uint32, retryOf string) AgentModelCall {
		return AgentModelCall{ProviderAttemptID: attempt, TransactionID: "tx-" + attempt, Purpose: agentModelCallPurposeTurn, LoopTurn: 1, RetryCount: retry, RetryOf: retryOf}
	}
	unavailable := transient("a", 0, "")
	unavailable.Receipt = &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, FailureCode: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE}}
	otherTurn := transient("a", 0, "")
	otherTurn.LoopTurn = 2
	otherModel := completed("c", 1, "a")
	otherModel.ModelDigest = "other-digest"
	otherTools := completed("c", 1, "a")
	otherTools.ToolsDeclared = []string{"read_file"}
	otherInput := completed("c", 1, "a")
	otherInput.Input = []AgentTraceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "changed"}}
	tests := []struct {
		name  string
		calls []AgentModelCall
		valid bool
	}{
		{name: "valid chain", calls: []AgentModelCall{transient("a", 0, ""), transient("b", 1, "a"), completed("c", 2, "b")}, valid: true},
		{name: "retry ordinal without retried attempt", calls: []AgentModelCall{completed("c", 1, "")}},
		{name: "retried attempt without ordinal", calls: []AgentModelCall{transient("a", 0, ""), completed("c", 0, "a")}},
		{name: "retries no earlier attempt", calls: []AgentModelCall{completed("c", 1, "a"), transient("a", 0, "")}},
		{name: "ordinal gap", calls: []AgentModelCall{transient("a", 0, ""), completed("c", 2, "a")}},
		{name: "non-transient predecessor", calls: []AgentModelCall{unavailable, completed("c", 1, "a")}},
		{name: "completed predecessor", calls: []AgentModelCall{completed("a", 0, ""), completed("c", 1, "a")}},
		{name: "different call", calls: []AgentModelCall{otherTurn, completed("c", 1, "a")}},
		{name: "different model", calls: []AgentModelCall{transient("a", 0, ""), otherModel}},
		{name: "different tools", calls: []AgentModelCall{transient("a", 0, ""), otherTools}},
		{name: "different input", calls: []AgentModelCall{transient("a", 0, ""), otherInput}},
		{name: "retried twice", calls: []AgentModelCall{transient("a", 0, ""), transient("b", 1, "a"), completed("c", 1, "a")}},
		{name: "past the retry bound", calls: []AgentModelCall{
			transient("a", 0, ""), transient("b", 1, "a"), transient("c", 2, "b"), transient("d", 3, "c"), completed("e", 4, "d"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyRetryChains(&AgentTrace{ModelCalls: tt.calls})
			if tt.valid {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, constants.ErrEvaluationEvidenceTraceMismatch)
		})
	}
}

func TestValidateAgentTraceRules_HistoricalTraceCannotCarryRetries(t *testing.T) {
	trace := &AgentTrace{
		SchemaVersion: agentTraceSchemaVersionV12, EvidenceDigest: "digest",
		Input:      &AgentTraceInput{Messages: []AgentTraceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "hi"}}},
		ModelCalls: []AgentModelCall{{ProviderAttemptID: "b", Purpose: agentModelCallPurposeTurn, RetryCount: 1, RetryOf: "a"}},
	}
	require.ErrorIs(t, validateAgentTraceRules(trace), constants.ErrEvidenceArtifactMalformed)
}

func TestScenarioBudget_CreditMovesDeadlineWithinParent(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	budget, stop := newScenarioBudget(parent, 10*time.Minute)
	defer stop()
	before, ok := budget.Deadline()
	require.True(t, ok)

	require.True(t, budget.credit(time.Minute))
	after, _ := budget.Deadline()
	assert.Equal(t, time.Minute, after.Sub(before))
	assert.False(t, budget.credit(time.Hour), "a credit past the parent's deadline is refused, not granted in part")
	unchanged, _ := budget.Deadline()
	assert.Equal(t, after, unchanged)
	require.NoError(t, budget.Err())

	spent, stopSpent := newScenarioBudget(parent, time.Hour)
	defer stopSpent()
	spent.mu.Lock()
	spent.deadline = time.Now().Add(-time.Second)
	spent.mu.Unlock()
	assert.False(t, spent.credit(time.Second), "a deadline that already passed is not revived before its timer fires")
}

func TestScenarioBudget_EndsWithDeadlineExceededOrParentError(t *testing.T) {
	expired, stop := newScenarioBudget(t.Context(), 0)
	defer stop()
	select {
	case <-expired.Done():
	case <-time.After(time.Second):
		t.Fatal("an exhausted budget must end")
	}
	require.ErrorIs(t, expired.Err(), context.DeadlineExceeded)
	assert.Equal(t, context.DeadlineExceeded, expired.Err(), "the loop matches the exact deadline error")
	assert.False(t, expired.credit(time.Minute), "an ended budget takes no credit")

	parent, cancel := context.WithCancel(t.Context())
	budget, stopBudget := newScenarioBudget(parent, time.Hour)
	defer stopBudget()
	cancel()
	select {
	case <-budget.Done():
	case <-time.After(time.Second):
		t.Fatal("the parent's end must end the budget")
	}
	require.ErrorIs(t, budget.Err(), context.Canceled)

	short, cancelShort := context.WithTimeout(t.Context(), time.Minute)
	defer cancelShort()
	capped, stopCapped := newScenarioBudget(short, time.Hour)
	defer stopCapped()
	parentDeadline, _ := short.Deadline()
	deadline, _ := capped.Deadline()
	assert.Equal(t, parentDeadline, deadline, "the budget never reports a deadline past its parent's")
}
