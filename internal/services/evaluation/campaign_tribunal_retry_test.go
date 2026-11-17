// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// memberTransient is a Tribunal member call the provider asked to retry later:
// no result, a signed FAILED receipt carrying the transient failure code.
func memberTransient(id string, retry uint32, retryOf string) *tribunalv1.TribunalMemberCall {
	return &tribunalv1.TribunalMemberCall{
		Stage: tribunalv1.TribunalStage_TRIBUNAL_STAGE_GENERATE, Persona: string(tribunal.MemberAxiom), Round: 1,
		ProviderAttemptId: id, RetryCount: retry, RetryOf: retryOf, TransactionId: "tx-" + id,
		Receipt: &operatorv1.EventReceipt{TransactionId: "tx-" + id, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, FailureCode: transientCode},
		Failure: &operatorv1.InferenceDispatchFailure{Code: operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_TRANSIENT, Error: "provider asked to retry later"},
	}
}

// memberCompleted is a completed Tribunal member call whose signed result echoes
// its retry ordinal.
func memberCompleted(t *testing.T, id string, retry uint32, retryOf string) *tribunalv1.TribunalMemberCall {
	t.Helper()
	result := &operatorv1.InferenceResult{
		ProviderAttemptId: id, RequestedModel: "model:tag", RetryCount: retry,
		Parts: []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: "answer"}}},
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	return &tribunalv1.TribunalMemberCall{
		Stage: tribunalv1.TribunalStage_TRIBUNAL_STAGE_GENERATE, Persona: string(tribunal.MemberAxiom), Round: 1,
		ProviderAttemptId: id, RetryCount: retry, RetryOf: retryOf, TransactionId: "tx-" + id,
		Result:  result,
		Receipt: &operatorv1.EventReceipt{TransactionId: "tx-" + id, ResultSummary: digest},
	}
}

func TestRecordMemberCalls_RetriedChainRecordsSupersededCallWithoutProviderFailure(t *testing.T) {
	run := newTribunalAuthoringRun(t, true, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
	members := []*tribunalv1.TribunalMemberCall{
		memberTransient("m-1", 0, ""),
		memberTransient("m-2", 1, "m-1"),
		memberCompleted(t, "m-3", 2, "m-2"),
	}

	failure := run.recordMemberCalls(AgentToolCall{CallID: "tool-1", LoopTurn: 1}, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, members)

	assert.Empty(t, failure)
	assert.Empty(t, run.trace.ProviderFailure, "a superseded attempt is not the trace's provider failure")
	assert.Empty(t, run.trace.ProviderFailureClass)
	require.Len(t, run.trace.ModelCalls, 3, "every receipted attempt stays on the trace")
	for i, call := range run.trace.ModelCalls {
		assert.Equal(t, members[i].GetProviderAttemptId(), call.ProviderAttemptID)
		assert.Equal(t, uint32(i), call.RetryCount)
		assert.Equal(t, members[i].GetRetryOf(), call.RetryOf)
		assert.Equal(t, agentModelCallPurposeTribunalPrefix+string(tribunal.StageGenerate), call.Purpose)
		assert.Equal(t, "tool-1", call.ToolCallID)
		assert.Equal(t, string(tribunal.MemberAxiom), call.Persona)
	}
	for _, call := range run.trace.ModelCalls[:2] {
		assert.Equal(t, string(ExecutionFailureProvider), call.Error)
		assert.False(t, call.Completed())
		require.NotNil(t, call.Receipt)
	}
	assert.True(t, run.trace.ModelCalls[2].Completed())
	assert.Equal(t, "answer", run.trace.ModelCalls[2].Text)
	require.NoError(t, verifyRetryChains(run.trace))
	assert.Equal(t, map[string]bool{"m-1": true, "m-2": true}, run.trace.supersededAttempts())
	assert.Len(t, run.trace.scoredModelCalls(), 1)
}

func TestRecordMemberCalls_ExhaustedChainIsTheLastAttemptsProviderFailure(t *testing.T) {
	run := newTribunalAuthoringRun(t, true, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
	members := []*tribunalv1.TribunalMemberCall{
		memberTransient("m-1", 0, ""),
		memberTransient("m-2", 1, "m-1"),
		memberTransient("m-3", 2, "m-2"),
		memberTransient("m-4", 3, "m-3"),
	}

	failure := run.recordMemberCalls(AgentToolCall{CallID: "tool-1", LoopTurn: 1}, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, members)

	assert.Equal(t, string(ExecutionFailureProvider), failure)
	assert.Equal(t, string(ExecutionFailureProvider), run.trace.ProviderFailure)
	assert.Equal(t, string(inference.AttemptFailureBackendTransient), run.trace.ProviderFailureClass)
	require.Len(t, run.trace.ModelCalls, 4)
	for _, call := range run.trace.ModelCalls {
		assert.Equal(t, string(ExecutionFailureProvider), call.Error)
	}
	assert.Equal(t, uint32(3), run.trace.ModelCalls[3].RetryCount)
	require.NoError(t, verifyRetryChains(run.trace))
	assert.Equal(t, map[string]bool{"m-1": true, "m-2": true, "m-3": true}, run.trace.supersededAttempts(), "the last attempt is the failure, not superseded")
}

func TestRecordMemberCalls_UnretriedTransientFailureEndsTheDeliberation(t *testing.T) {
	run := newTribunalAuthoringRun(t, true, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})

	failure := run.recordMemberCalls(AgentToolCall{CallID: "tool-1", LoopTurn: 1}, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
		[]*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, "")})

	assert.Equal(t, string(ExecutionFailureProvider), failure, "the Gateway did not retry it (wait past its deadline): it stands")
	assert.Equal(t, string(inference.AttemptFailureBackendTransient), run.trace.ProviderFailureClass)
	require.Len(t, run.trace.ModelCalls, 1)
	assert.Empty(t, run.trace.supersededAttempts())
}

func TestRecordMemberCalls_RetryMustEchoItsOrdinalInTheSignedResult(t *testing.T) {
	run := newTribunalAuthoringRun(t, true, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
	retried := memberCompleted(t, "m-2", 0, "m-1")
	retried.RetryCount = 1 // the member claims a retry its signed result does not echo

	failure := run.recordMemberCalls(AgentToolCall{CallID: "tool-1", LoopTurn: 1}, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
		[]*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), retried})

	assert.NotEmpty(t, failure, "a result that does not echo the retry ordinal fails closed")
	assert.NotEmpty(t, run.trace.ProviderFailure)
}

// A deliberation whose member calls break the retry rules is a malformed
// Gateway answer, not a model outcome: it is never graded.
func TestRunTribunal_MalformedMemberRetryChainFailsTheDispatch(t *testing.T) {
	unavailable := memberTransient("m-1", 0, "")
	unavailable.Receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE
	otherPersona := memberCompleted(t, "m-2", 0, "")
	otherPersona.Persona = string(tribunal.MemberConcord)
	tests := []struct {
		name  string
		calls []*tribunalv1.TribunalMemberCall
	}{
		{name: "retry ordinal without retried attempt", calls: []*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), memberCompleted(t, "m-2", 1, "")}},
		{name: "retried attempt without ordinal", calls: []*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), memberCompleted(t, "m-2", 0, "m-1")}},
		{name: "first call retries", calls: []*tribunalv1.TribunalMemberCall{memberCompleted(t, "m-2", 1, "m-1")}},
		{name: "retries a call that is not the one before it", calls: []*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), otherPersona, memberCompleted(t, "m-3", 1, "m-1")}},
		{name: "retries an unknown attempt", calls: []*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), memberCompleted(t, "m-2", 1, "elsewhere")}},
		{name: "ordinal gap", calls: []*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), memberCompleted(t, "m-2", 2, "m-1")}},
		{name: "completed predecessor", calls: []*tribunalv1.TribunalMemberCall{memberCompleted(t, "m-1", 0, ""), memberCompleted(t, "m-2", 1, "m-1")}},
		{name: "non-transient predecessor", calls: []*tribunalv1.TribunalMemberCall{unavailable, memberCompleted(t, "m-2", 1, "m-1")}},
		{name: "past the retry bound", calls: []*tribunalv1.TribunalMemberCall{
			memberTransient("m-1", 0, ""), memberTransient("m-2", 1, "m-1"), memberTransient("m-3", 2, "m-2"),
			memberTransient("m-4", 3, "m-3"), memberCompleted(t, "m-5", 4, "m-4"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := &tribunalAuthoringInference{result: &tribunalv1.TribunalIntentResult{
				Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS, MemberCalls: tt.calls,
			}}
			run := newTribunalAuthoringRun(t, true, intent, &tribunalAuthoringTools{})

			_, err := run.runTribunal(context.Background(), AgentToolCall{CallID: "tool-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: `{"command":"pwd"}`}, models.RunShellCommandRequest{Command: "pwd"})

			require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
			assert.Empty(t, run.trace.ProviderFailure, "a malformed answer is not a model outcome")
			assert.Empty(t, run.trace.ModelCalls)
		})
	}
}

func TestRunTribunal_RecordsRetriedChainAndKeepsTheTemplateRetryFree(t *testing.T) {
	intent := &tribunalAuthoringInference{result: &tribunalv1.TribunalIntentResult{
		Outcome:     tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS,
		MemberCalls: []*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), memberCompleted(t, "m-2", 1, "m-1")},
	}}
	run := newTribunalAuthoringRun(t, true, intent, &tribunalAuthoringTools{})

	call, err := run.runTribunal(context.Background(), AgentToolCall{CallID: "tool-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: `{"command":"pwd"}`}, models.RunShellCommandRequest{Command: "pwd"})

	require.NoError(t, err)
	assert.False(t, call.Success)
	assert.Contains(t, call.Error, "no consensus", "the deliberation went on to its own outcome: the retried attempt was not a failure")
	assert.Empty(t, run.trace.ProviderFailure)
	require.Len(t, run.trace.ModelCalls, 2)
	assert.Equal(t, "m-1", run.trace.ModelCalls[1].RetryOf)
	require.NoError(t, verifyRetryChains(run.trace))
	for _, template := range []*operatorv1.InferenceDispatchRequest{intent.intent.GetMemberInference(), intent.intent.GetMarshalInference(), intent.intent.GetAuditorInference()} {
		assert.Zero(t, template.GetRetryCount(), "the Gateway owns each call's retry ordinal")
	}
	require.NoError(t, tribunal.ValidateIntentRequest(intent.intent))
}

// Every producer appends an attempt and its retry back to back, so the verifier
// requires a retry to be the very next model call after its predecessor. A model
// call does not carry its Tribunal round: adjacency is what keeps a forged chain
// from linking the same persona's calls across rounds, stages or tool calls.
func TestVerifyRetryChains_RejectsForgedTribunalChains(t *testing.T) {
	member := func(attempt, stage, persona, tool string, retry uint32, retryOf string, transient bool) AgentModelCall {
		call := AgentModelCall{
			ProviderAttemptID: attempt, TransactionID: "tx-" + attempt, LoopTurn: 1, ToolCallID: tool,
			Purpose: agentModelCallPurposeTribunalPrefix + stage, Persona: persona, RetryCount: retry, RetryOf: retryOf,
		}
		if transient {
			call.Error = string(ExecutionFailureProvider)
			call.Receipt = &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, FailureCode: transientCode}}
		}
		return call
	}
	axiom, nemesis, auditor := string(tribunal.MemberAxiom), string(tribunal.MemberNemesis), tribunal.PersonaAuditor
	generate, marshal, audit := string(tribunal.StageGenerate), string(tribunal.StageMarshal), string(tribunal.StageAudit)
	tests := []struct {
		name  string
		calls []AgentModelCall
		valid bool
	}{
		{name: "valid member chain between other calls", valid: true, calls: []AgentModelCall{
			member("a1", generate, axiom, "t1", 0, "", false),
			member("n1", generate, nemesis, "t1", 0, "", true), member("n2", generate, nemesis, "t1", 1, "n1", false),
			member("m1", marshal, tribunal.PersonaMarshal, "t1", 0, "", false),
		}},
		{name: "valid Auditor chains in sequence", valid: true, calls: []AgentModelCall{
			member("u1", audit, auditor, "t1", 0, "", true), member("u2", audit, auditor, "t1", 1, "u1", false),
			member("u3", audit, auditor, "t1", 0, "", true), member("u4", audit, auditor, "t1", 1, "u3", false),
		}},
		{name: "round 1 Nemesis retried by a round 2 call over the calls between them", calls: []AgentModelCall{
			member("n1", generate, nemesis, "t1", 0, "", true),
			member("a2", generate, axiom, "t1", 0, "", false),
			member("n2", generate, nemesis, "t1", 1, "n1", false),
		}},
		{name: "retry names a predecessor an intervening call separates", calls: []AgentModelCall{
			member("u1", audit, auditor, "t1", 0, "", true),
			member("u2", audit, auditor, "t1", 0, "", false),
			member("u3", audit, auditor, "t1", 1, "u1", false),
		}},
		{name: "chain across stages", calls: []AgentModelCall{
			member("a1", generate, axiom, "t1", 0, "", true), member("m1", marshal, tribunal.PersonaMarshal, "t1", 1, "a1", false),
		}},
		{name: "chain across personas", calls: []AgentModelCall{
			member("a1", generate, axiom, "t1", 0, "", true), member("n1", generate, nemesis, "t1", 1, "a1", false),
		}},
		{name: "chain across tool calls", calls: []AgentModelCall{
			member("a1", generate, axiom, "t1", 0, "", true), member("a2", generate, axiom, "t2", 1, "a1", false),
		}},
		{name: "chain from a Tribunal member to the agent's turn", calls: []AgentModelCall{
			member("a1", generate, axiom, "t1", 0, "", true),
			{ProviderAttemptID: "turn-2", TransactionID: "tx-turn-2", Purpose: agentModelCallPurposeTurn, LoopTurn: 1, RetryCount: 1, RetryOf: "a1"},
		}},
		{name: "first model call retries", calls: []AgentModelCall{member("a2", generate, axiom, "t1", 1, "a1", false)}},
		{name: "a retried attempt retried again over the first retry", calls: []AgentModelCall{
			member("a1", generate, axiom, "t1", 0, "", true), member("a2", generate, axiom, "t1", 1, "a1", true), member("a3", generate, axiom, "t1", 1, "a1", false),
		}},
		{name: "retry of a completed member call", calls: []AgentModelCall{
			member("a1", generate, axiom, "t1", 0, "", false), member("a2", generate, axiom, "t1", 1, "a1", false),
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

// retryBudget is a scenario budget of limit under parent, stopped with the test.
func retryBudget(t *testing.T, parent context.Context, limit time.Duration) *scenarioBudget {
	t.Helper()
	budget, stop := newScenarioBudget(parent, limit)
	t.Cleanup(stop)
	return budget
}

func setBudgetDeadline(budget *scenarioBudget, deadline time.Time) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	budget.deadline = deadline
}

func TestScenarioBudget_HoldDefersExpiryUntilRelease(t *testing.T) {
	t.Run("a credit that covers the passed deadline keeps the budget alive", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		budget.hold()
		setBudgetDeadline(budget, time.Now().Add(-10*time.Second))
		budget.expire()
		require.NoError(t, budget.Err(), "an expiry during a hold is deferred")

		budget.release(time.Minute)

		require.NoError(t, budget.Err())
		select {
		case <-budget.Done():
			t.Fatal("the credited budget must not end")
		default:
		}
		deadline, _ := budget.Deadline()
		assert.WithinDuration(t, time.Now().Add(50*time.Second), deadline, 5*time.Second)
	})
	t.Run("a credit that does not cover it ends the budget at release", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		budget.hold()
		setBudgetDeadline(budget, time.Now().Add(-time.Minute))
		budget.expire()
		require.NoError(t, budget.Err())

		budget.release(5 * time.Second)

		select {
		case <-budget.Done():
		default:
			t.Fatal("an uncovered deadline ends the budget at release")
		}
		assert.Equal(t, context.DeadlineExceeded, budget.Err(), "the loop matches the exact deadline error")
	})
	t.Run("no credit ends an expired budget at release", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		budget.hold()
		setBudgetDeadline(budget, time.Now().Add(-time.Second))

		budget.release(0)

		assert.Equal(t, context.DeadlineExceeded, budget.Err())
	})
	t.Run("an unexpired budget re-arms for the time left", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), 20*time.Millisecond)
		budget.hold()
		budget.release(30 * time.Millisecond)

		select {
		case <-budget.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("a released budget must end at its credited deadline")
		}
		assert.Equal(t, context.DeadlineExceeded, budget.Err())
	})
	t.Run("a timer that fires during the hold does not end the budget", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		budget.hold()
		setBudgetDeadline(budget, time.Now().Add(time.Millisecond))
		budget.timer.Reset(time.Millisecond)

		assert.Never(t, func() bool { return budget.Err() != nil }, 60*time.Millisecond, 5*time.Millisecond)

		budget.release(0)
		assert.Equal(t, context.DeadlineExceeded, budget.Err())
	})
}

func TestScenarioBudget_HoldKeepsTheParentInCharge(t *testing.T) {
	t.Run("parent cancellation ends the budget immediately", func(t *testing.T) {
		parent, cancel := context.WithCancel(t.Context())
		budget := retryBudget(t, parent, time.Hour)
		budget.hold()

		cancel()

		select {
		case <-budget.Done():
		case <-time.After(time.Second):
			t.Fatal("the parent's end must end a held budget")
		}
		require.ErrorIs(t, budget.Err(), context.Canceled)
		budget.release(time.Hour)
		require.ErrorIs(t, budget.Err(), context.Canceled, "release after the parent ended only drops the hold")
	})
	t.Run("a credit past the parent's deadline is refused, not granted in part", func(t *testing.T) {
		parent, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		budget := retryBudget(t, parent, 30*time.Second)
		budget.hold()
		before, _ := budget.Deadline()

		budget.release(2 * time.Minute)

		require.NoError(t, budget.Err())
		after, _ := budget.Deadline()
		assert.Equal(t, before, after, "the whole credit is refused")
	})
	t.Run("a refused credit leaves a passed deadline to end the budget", func(t *testing.T) {
		parent, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		budget := retryBudget(t, parent, 30*time.Second)
		budget.hold()
		setBudgetDeadline(budget, time.Now().Add(-time.Second))

		budget.release(2 * time.Minute)

		assert.Equal(t, context.DeadlineExceeded, budget.Err())
	})
}

func TestScenarioBudget_HoldsNest(t *testing.T) {
	budget := retryBudget(t, t.Context(), time.Hour)
	budget.hold()
	budget.hold()
	setBudgetDeadline(budget, time.Now().Add(-time.Second))

	budget.release(0)
	require.NoError(t, budget.Err(), "an outer hold still defers expiry")
	budget.release(0)
	assert.Equal(t, context.DeadlineExceeded, budget.Err(), "only the last release evaluates the budget")
}

func TestScenarioBudget_NilBudgetHasNothingToHold(t *testing.T) {
	var budget *scenarioBudget
	assert.NotPanics(t, func() {
		budget.hold()
		budget.release(time.Minute)
	})
}

// budgetHolds reports the budget's hold count under its lock.
func budgetHolds(budget *scenarioBudget) int {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.holds
}

// budgetIntent runs a hook inside DispatchIntent, as the Gateway's retry waits
// run inside it, then answers result.
type budgetIntent struct {
	tribunalAuthoringInference
	during func()
	err    error
}

func (f *budgetIntent) DispatchIntent(_ context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error) {
	f.intentCalls++
	f.intent = req
	if f.during != nil {
		f.during()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestRunTribunal_HoldsTheScenarioBudgetAndCreditsTheGatewaysRetryWaits(t *testing.T) {
	retried := func() *tribunalv1.TribunalIntentResult {
		return &tribunalv1.TribunalIntentResult{
			Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS,
			MemberCalls: []*tribunalv1.TribunalMemberCall{
				memberTransient("m-1", 0, ""), memberTransient("m-2", 1, "m-1"), memberCompleted(t, "m-3", 2, "m-2"),
			},
		}
	}
	record := AgentToolCall{CallID: "tool-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: `{"command":"pwd"}`}
	proposal := models.RunShellCommandRequest{Command: "pwd"}

	t.Run("the waits the Gateway took are credited, even past the passed deadline", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		intent := &budgetIntent{tribunalAuthoringInference: tribunalAuthoringInference{result: retried()}}
		run := newTribunalAuthoringRun(t, true, &intent.tribunalAuthoringInference, &tribunalAuthoringTools{})
		run.executor.inference = intent
		run.budget = budget
		held := 0
		intent.during = func() {
			held = budgetHolds(budget)
			// The Gateway's 5 s and 20 s waits outlast the budget's deadline.
			setBudgetDeadline(budget, time.Now().Add(-time.Second))
			budget.expire()
		}

		_, err := run.runTribunal(context.Background(), record, proposal)

		require.NoError(t, err)
		assert.Equal(t, 1, held, "the budget is held while the Gateway deliberates")
		assert.Zero(t, budgetHolds(budget), "and released afterwards")
		require.NoError(t, budget.Err(), "the 25 s credit covers a deadline that passed 1 s ago")
		deadline, _ := budget.Deadline()
		assert.WithinDuration(t, time.Now().Add(24*time.Second), deadline, 5*time.Second)
		assert.Empty(t, run.trace.ProviderFailure)
	})
	t.Run("a credit that does not cover the deadline ends the budget", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		intent := &budgetIntent{tribunalAuthoringInference: tribunalAuthoringInference{result: retried()}}
		run := newTribunalAuthoringRun(t, true, &intent.tribunalAuthoringInference, &tribunalAuthoringTools{})
		run.executor.inference = intent
		run.budget = budget
		intent.during = func() { setBudgetDeadline(budget, time.Now().Add(-time.Minute)) }

		_, err := run.runTribunal(context.Background(), record, proposal)

		require.NoError(t, err)
		assert.Equal(t, context.DeadlineExceeded, budget.Err(), "the model's own time ran out")
		assert.Zero(t, budgetHolds(budget))
	})
	t.Run("a deliberation without retries credits nothing", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		intent := &budgetIntent{tribunalAuthoringInference: tribunalAuthoringInference{result: &tribunalv1.TribunalIntentResult{
			Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS, MemberCalls: []*tribunalv1.TribunalMemberCall{memberCompleted(t, "m-1", 0, "")},
		}}}
		run := newTribunalAuthoringRun(t, true, &intent.tribunalAuthoringInference, &tribunalAuthoringTools{})
		run.executor.inference = intent
		run.budget = budget
		before, _ := budget.Deadline()

		_, err := run.runTribunal(context.Background(), record, proposal)

		require.NoError(t, err)
		after, _ := budget.Deadline()
		assert.Equal(t, before, after)
	})
	t.Run("a failed dispatch releases the hold", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		intent := &budgetIntent{err: constants.ErrEvaluationDispatchFailed}
		run := newTribunalAuthoringRun(t, true, &intent.tribunalAuthoringInference, &tribunalAuthoringTools{})
		run.executor.inference = intent
		run.budget = budget
		intent.during = func() { setBudgetDeadline(budget, time.Now().Add(-time.Second)) }

		_, err := run.runTribunal(context.Background(), record, proposal)

		require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
		assert.Zero(t, budgetHolds(budget), "every path releases")
		assert.Equal(t, context.DeadlineExceeded, budget.Err(), "an uncredited expiry lands when the hold ends")
	})
	t.Run("a malformed chain still releases the hold with its credit", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		intent := &budgetIntent{tribunalAuthoringInference: tribunalAuthoringInference{result: &tribunalv1.TribunalIntentResult{
			Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS, MemberCalls: []*tribunalv1.TribunalMemberCall{memberCompleted(t, "m-2", 1, "m-1")},
		}}}
		run := newTribunalAuthoringRun(t, true, &intent.tribunalAuthoringInference, &tribunalAuthoringTools{})
		run.executor.inference = intent
		run.budget = budget

		_, err := run.runTribunal(context.Background(), record, proposal)

		require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
		assert.Zero(t, budgetHolds(budget))
	})
}

func TestMemberRetryWait_IsThePolicyDelayOfEachRetryOrdinal(t *testing.T) {
	assert.Zero(t, memberRetryWait(nil))
	assert.Zero(t, memberRetryWait([]*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, "")}))
	assert.Equal(t, 5*time.Second, memberRetryWait([]*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), memberTransient("m-2", 1, "m-1")}))
	assert.Equal(t, 65*time.Second, memberRetryWait([]*tribunalv1.TribunalMemberCall{
		memberTransient("m-1", 0, ""), memberTransient("m-2", 1, "m-1"), memberTransient("m-3", 2, "m-2"), memberTransient("m-4", 3, "m-3"),
	}))
	assert.Equal(t, 10*time.Second, memberRetryWait([]*tribunalv1.TribunalMemberCall{
		memberTransient("m-1", 0, ""), memberTransient("m-2", 1, "m-1"), memberTransient("m-3", 0, ""), memberTransient("m-4", 1, "m-3"),
	}), "each chain's waits are credited")
	assert.Zero(t, memberRetryWait([]*tribunalv1.TribunalMemberCall{memberTransient("m-9", 9, "m-8")}), "an ordinal past the policy credits nothing")
}

// A superseded Tribunal attempt sits in the middle of a tool call, followed by
// its retry, the deliberation's other calls, and later agent turns. The shared
// trace rules treat it as evidence and never as the run's failure: the record
// rebuild classifies it RETRIED, the lifecycle is not PROVIDER_FAILED, and the
// sealed-version rules accept it.
func TestTribunalRetriedChain_RunsThroughTheSharedTraceRules(t *testing.T) {
	run, _, _ := retryRun(t)
	lite := run.variants[evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE]
	require.NotNil(t, lite)
	completed := func(id string, retry uint32, retryOf string) *tribunalv1.TribunalMemberCall {
		member := memberCompleted(t, id, retry, retryOf)
		member.Result.RequestedModel = lite.GetServedModelTag()
		member.Result.CampaignId = run.trace.CampaignID
		member.Result.ModelRegistryDigest = run.trace.ModelRegistryDigest
		digest, err := models.ComputeInferenceResultDigest(member.Result)
		require.NoError(t, err)
		member.Result.ResultDigest = digest
		member.Receipt.ResultSummary = digest
		return member
	}

	_, first := inferTurn(t, t.Context(), run)
	require.NotNil(t, first)
	failure := run.recordMemberCalls(AgentToolCall{CallID: "tool-1", LoopTurn: 1}, run.designated, []*tribunalv1.TribunalMemberCall{
		memberTransient("m-1", 0, ""), completed("m-2", 1, "m-1"), completed("m-3", 0, ""),
	})
	require.Empty(t, failure)
	messages, err := run.conversation()
	require.NoError(t, err)
	_, last := run.infer(t.Context(), AgentModelCall{Purpose: agentModelCallPurposeTurn, LoopTurn: 2}, messages, nil)
	require.NotNil(t, last)
	require.Len(t, run.trace.ModelCalls, 5)
	run.trace.FinalOutput = "done"

	require.NoError(t, verifyRetryChains(run.trace))
	records, _, err := modelInferenceRecordsFromAgentTrace(run.req, run.trace, func(prefix string) string { return prefix })
	require.NoError(t, err)
	var classes []evalv1.EvaluationCallClassification
	for _, record := range records {
		classes = append(classes, record.GetClassification())
	}
	assert.Equal(t, []evalv1.EvaluationCallClassification{
		evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
		evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_RETRIED,
		evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
		evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
		evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
	}, classes)
	assert.Equal(t, agentCallSiteTribunalPrefix+string(tribunal.StageGenerate), records[1].GetCallSite())
	assert.Equal(t, string(tribunal.MemberAxiom), records[1].GetAgentPersona())
	require.NotNil(t, records[2].RetryCount)
	assert.Equal(t, uint32(1), records[2].GetRetryCount())
	assert.NotEqual(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, agentTraceLifecycle(run.req, run.trace))

	run.trace.SchemaVersion, run.trace.EvidenceDigest = agentTraceSchemaVersion, "digest"
	run.trace.Input = &AgentTraceInput{Messages: []AgentTraceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "Investigate."}}}
	require.NoError(t, validateAgentTraceRules(run.trace))
	assert.Len(t, run.trace.scoredModelCalls(), 4, "the superseded attempt is not a scored call")
}
