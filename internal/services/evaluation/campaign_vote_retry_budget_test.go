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
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// retryVote is one member's L2 vote on subject, retried retry times.
func retryVote(subject, member string, retry uint32) *operatorv1.ConsensusVoteInference {
	return &operatorv1.ConsensusVoteInference{SubjectTransactionHash: subject, MemberAppId: member, RetryCount: retry}
}

func TestVoteRetryWait_CreditsEachSubjectsLongestMemberChain(t *testing.T) {
	tests := []struct {
		name  string
		votes []*operatorv1.ConsensusVoteInference
		want  time.Duration
	}{
		{name: "no votes", want: 0},
		{name: "votes without retries", votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "axiom", 0), retryVote("s1", "concord", 0)}, want: 0},
		{name: "one retry waits the first delay", votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "axiom", 1)}, want: 5 * time.Second},
		{name: "one member retried twice waits both delays", votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "axiom", 2)}, want: 25 * time.Second},
		{
			name:  "members of one subject vote concurrently: the longest chain",
			votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "axiom", 2), retryVote("s1", "concord", 1), retryVote("s1", "pragma", 0)},
			want:  25 * time.Second,
		},
		{
			name:  "the longest chain does not depend on vote order",
			votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "concord", 1), retryVote("s1", "axiom", 2)},
			want:  25 * time.Second,
		},
		{
			name:  "subjects deliberate one after another: their waits add",
			votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "axiom", 2), retryVote("s2", "axiom", 1)},
			want:  30 * time.Second,
		},
		{
			name:  "each subject takes its own longest chain",
			votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "axiom", 1), retryVote("s1", "concord", 3), retryVote("s2", "axiom", 2), retryVote("s2", "concord", 1)},
			want:  65*time.Second + 25*time.Second,
		},
		{name: "the whole policy", votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "axiom", 3)}, want: 65 * time.Second},
		{name: "a count past the policy bound credits only the allowed delays", votes: []*operatorv1.ConsensusVoteInference{retryVote("s1", "axiom", 9)}, want: 65 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, voteRetryWait(tt.votes))
		})
	}
}

func TestRunTribunal_CreditsVoteRetryWaitOnTopOfMemberWaits(t *testing.T) {
	record := AgentToolCall{CallID: "tool-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: `{"command":"pwd"}`}
	proposal := models.RunShellCommandRequest{Command: "pwd"}
	result := func() *tribunalv1.TribunalIntentResult {
		return &tribunalv1.TribunalIntentResult{
			Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS,
			// One member retry: 5 s.
			MemberCalls: []*tribunalv1.TribunalMemberCall{memberTransient("m-1", 0, ""), memberCompleted(t, "m-2", 1, "m-1")},
			// Two members of one subject retried twice and once, then a second
			// subject retried once: 25 s + 5 s.
			ConsensusInferences: []*operatorv1.ConsensusVoteInference{
				retryVote("s1", "axiom", 2), retryVote("s1", "concord", 1), retryVote("s2", "axiom", 1),
			},
		}
	}

	t.Run("the member and vote waits are credited together", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		intent := &budgetIntent{tribunalAuthoringInference: tribunalAuthoringInference{result: result()}}
		run := newTribunalAuthoringRun(t, true, &intent.tribunalAuthoringInference, &tribunalAuthoringTools{})
		run.executor.inference = intent
		run.budget = budget
		base := time.Now().Add(-time.Second)
		intent.during = func() {
			setBudgetDeadline(budget, base)
			budget.expire()
		}

		_, err := run.runTribunal(context.Background(), record, proposal, run.trace.Roster[0])

		require.NoError(t, err)
		require.NoError(t, budget.Err(), "35 s of the Gateway's waits cover a deadline that passed 1 s ago")
		deadline, _ := budget.Deadline()
		assert.True(t, deadline.Equal(base.Add(35*time.Second)), "5 s of member waits + 25 s + 5 s of vote waits, got %s", deadline.Sub(base))
		assert.Zero(t, budgetHolds(budget))
	})
	t.Run("vote retries alone are credited", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		votesOnly := result()
		votesOnly.MemberCalls = nil
		intent := &budgetIntent{tribunalAuthoringInference: tribunalAuthoringInference{result: votesOnly}}
		run := newTribunalAuthoringRun(t, true, &intent.tribunalAuthoringInference, &tribunalAuthoringTools{})
		run.executor.inference = intent
		run.budget = budget
		base := time.Now().Add(-time.Second)
		intent.during = func() { setBudgetDeadline(budget, base) }

		_, err := run.runTribunal(context.Background(), record, proposal, run.trace.Roster[0])

		require.NoError(t, err)
		deadline, _ := budget.Deadline()
		assert.True(t, deadline.Equal(base.Add(30*time.Second)), "got %s", deadline.Sub(base))
	})
	t.Run("a vote wait that does not cover the deadline ends the budget", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		intent := &budgetIntent{tribunalAuthoringInference: tribunalAuthoringInference{result: result()}}
		run := newTribunalAuthoringRun(t, true, &intent.tribunalAuthoringInference, &tribunalAuthoringTools{})
		run.executor.inference = intent
		run.budget = budget
		intent.during = func() { setBudgetDeadline(budget, time.Now().Add(-time.Minute)) }

		_, err := run.runTribunal(context.Background(), record, proposal, run.trace.Roster[0])

		require.NoError(t, err)
		assert.Equal(t, context.DeadlineExceeded, budget.Err())
	})
}

// votedTools answers every governed dispatch with outcome, or fails it with err,
// running during inside the dispatch as the Gateway's vote retry waits run.
type votedTools struct {
	tribunalAuthoringTools
	during  func()
	err     error
	outcome *client.GovernedOutcome
	calls   int
}

func (f *votedTools) DispatchAgentTool(context.Context, Target, string, string, string, string, constants.ActionType, string, []byte) (GovernedToolDispatch, error) {
	f.calls++
	if f.during != nil {
		f.during()
	}
	if f.err != nil {
		return GovernedToolDispatch{}, f.err
	}
	return GovernedToolDispatch{Outcome: f.outcome, TransactionID: f.outcome.Observation.GetReceipt().GetTransactionId()}, nil
}

// votedOutcome is a governed receipt answer whose admission carried votes.
func votedOutcome(votes ...*operatorv1.ConsensusVoteInference) *client.GovernedOutcome {
	outcome := shellToolOutcome(`{"exit_code":0}`)
	outcome.ConsensusInferences = votes
	return outcome
}

// TestAgentRunFanOut_HoldsTheScenarioBudgetAndCreditsVoteRetryWaits drives one
// governed shell call through fanOut, which holds the budget for the group and
// credits the longest vote-retry wait.
func TestAgentRunFanOut_HoldsTheScenarioBudgetAndCreditsVoteRetryWaits(t *testing.T) {
	shellGroup := func(t *testing.T, run *agentRun) (AgentToolCall, error) {
		t.Helper()
		spec, err := catalogToolSpec(constants.MCPToolRunShellCommand)
		require.NoError(t, err)
		request := decodeAgentToolRequest(spec, true, `{"command":"echo"}`)
		require.Nil(t, request.Failure)
		lead := onTarget(AgentToolCall{CallID: "tool-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: request.ArgumentsJSON})
		group, err := run.fanOut(context.Background(), spec, request, []AgentToolCall{lead})
		if err != nil {
			return AgentToolCall{}, err
		}
		require.Len(t, group, 1)
		return group[0], nil
	}
	newRun := func(t *testing.T, tools *votedTools, budget *scenarioBudget) *agentRun {
		t.Helper()
		run := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
		run.executor.tools = tools
		run.budget = budget
		return run
	}

	t.Run("a retried vote keeps the budget alive past its original deadline", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		tools := &votedTools{outcome: votedOutcome(retryVote("s1", "axiom", 2), retryVote("s1", "concord", 1))}
		base := time.Now().Add(-time.Second)
		held := 0
		tools.during = func() {
			held = budgetHolds(budget)
			// The Gateway's vote retry waits outlast the budget's deadline.
			setBudgetDeadline(budget, base)
			budget.expire()
		}
		run := newRun(t, tools, budget)

		dispatched, err := shellGroup(t, run)

		require.NoError(t, err)
		assert.NotEqual(t, agentPolicyDecisionDeny, dispatched.PolicyDecision)
		assert.Equal(t, "txn-1", dispatched.TransactionID)
		assert.Equal(t, 1, held, "the budget is held while the fanned-out group dispatches")
		assert.Zero(t, budgetHolds(budget), "and released afterwards")
		require.NoError(t, budget.Err(), "the 25 s vote wait covers a deadline that passed 1 s ago")
		deadline, _ := budget.Deadline()
		assert.True(t, deadline.Equal(base.Add(25*time.Second)), "the longest member chain is credited, got %s", deadline.Sub(base))
	})
	t.Run("a vote wait that does not cover the deadline ends the budget at release", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		tools := &votedTools{outcome: votedOutcome(retryVote("s1", "axiom", 1))}
		tools.during = func() { setBudgetDeadline(budget, time.Now().Add(-time.Minute)) }
		run := newRun(t, tools, budget)

		_, err := shellGroup(t, run)

		require.NoError(t, err)
		assert.Equal(t, context.DeadlineExceeded, budget.Err())
		assert.Zero(t, budgetHolds(budget))
	})
	t.Run("a dispatch without votes credits nothing", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		tools := &votedTools{outcome: votedOutcome()}
		run := newRun(t, tools, budget)
		before, _ := budget.Deadline()

		_, err := shellGroup(t, run)

		require.NoError(t, err)
		after, _ := budget.Deadline()
		assert.Equal(t, before, after)
		assert.Zero(t, budgetHolds(budget))
	})
	t.Run("a dispatch error releases the hold with no credit", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		tools := &votedTools{err: constants.ErrEvaluationDispatchFailed}
		base := time.Now().Add(time.Minute)
		tools.during = func() { setBudgetDeadline(budget, base) }
		run := newRun(t, tools, budget)

		var err error
		require.NotPanics(t, func() {
			_, err = shellGroup(t, run)
		}, "an error answer has no outcome to read votes from")

		require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
		assert.Zero(t, budgetHolds(budget))
		require.NoError(t, budget.Err())
		deadline, _ := budget.Deadline()
		assert.True(t, deadline.Equal(base), "nothing was credited")
	})
	t.Run("a dispatch error after the deadline passed ends the budget", func(t *testing.T) {
		budget := retryBudget(t, t.Context(), time.Hour)
		tools := &votedTools{err: constants.ErrEvaluationDispatchFailed}
		tools.during = func() { setBudgetDeadline(budget, time.Now().Add(-time.Second)) }
		run := newRun(t, tools, budget)

		_, err := shellGroup(t, run)

		require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
		assert.Equal(t, context.DeadlineExceeded, budget.Err())
	})
	t.Run("without a budget it is a plain dispatch", func(t *testing.T) {
		tools := &votedTools{outcome: votedOutcome(retryVote("s1", "axiom", 2))}
		run := newRun(t, tools, nil)

		var (
			dispatched AgentToolCall
			err        error
		)
		require.NotPanics(t, func() {
			dispatched, err = shellGroup(t, run)
		})

		require.NoError(t, err)
		assert.NotEqual(t, agentPolicyDecisionDeny, dispatched.PolicyDecision)
		assert.Equal(t, 1, tools.calls)
	})
}
