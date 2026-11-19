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
)

// TestAgentCallContext_FollowsTheBudgetsCredits moves an in-flight call's
// deadline with every credit the budget grants, through credit and through
// release, under the budget's own refusal.
func TestAgentCallContext_FollowsTheBudgetsCredits(t *testing.T) {
	parent, cancelParent := context.WithTimeout(t.Context(), time.Hour)
	defer cancelParent()
	budget, cancelBudget := newScenarioBudget(parent, time.Minute)
	defer cancelBudget()

	callCtx, finish := agentCallContext(budget, budget)
	defer finish()
	start, ok := callCtx.Deadline()
	require.True(t, ok)
	budgetDeadline, _ := budget.Deadline()
	assert.True(t, start.After(budgetDeadline), "the call drains past the budget")

	require.True(t, budget.credit(25*time.Second))
	moved, _ := callCtx.Deadline()
	assert.Equal(t, 25*time.Second, moved.Sub(start), "a credited wait moves the call with the budget")

	budget.hold()
	budget.release(5 * time.Second)
	released, _ := callCtx.Deadline()
	assert.Equal(t, 5*time.Second, released.Sub(moved), "a wait credited on release moves the call too")

	require.False(t, budget.credit(2*time.Hour), "a credit past the assignment deadline is refused")
	refused, _ := callCtx.Deadline()
	assert.Equal(t, released, refused, "a refused credit leaves the call where it was")

	finish()
	require.ErrorIs(t, callCtx.Err(), context.Canceled)
	require.True(t, budget.credit(time.Second))
	ended, _ := callCtx.Deadline()
	assert.Equal(t, released, ended, "a finished call no longer follows the budget")
}

// TestBudgetCall_ExpiresAtTheMovedDeadline shows a credit granted while the
// call is in flight keeps it alive past the deadline it started with, and that
// it still ends with context.DeadlineExceeded at the moved one.
func TestBudgetCall_ExpiresAtTheMovedDeadline(t *testing.T) {
	budget, cancelBudget := newScenarioBudget(t.Context(), 20*time.Millisecond)
	defer cancelBudget()
	callCtx, finish := budget.follow(budget, 20*time.Millisecond)
	defer finish()
	require.True(t, budget.credit(150*time.Millisecond))

	time.Sleep(80 * time.Millisecond)
	require.NoError(t, callCtx.Err(), "the call's original deadline has passed, but the credit moved it")

	select {
	case <-callCtx.Done():
		require.ErrorIs(t, callCtx.Err(), context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("the call must end at its moved deadline")
	}
}

// TestBudgetCall_DrainsPastTheBudgetButHonorsCancellation keeps the drain
// rule agentCallContext has without a budget.
func TestBudgetCall_DrainsPastTheBudgetButHonorsCancellation(t *testing.T) {
	budget, cancelBudget := newScenarioBudget(t.Context(), 10*time.Millisecond)
	defer cancelBudget()
	callCtx, finish := agentCallContext(budget, budget)
	defer finish()
	<-budget.Done()
	require.ErrorIs(t, budget.Err(), context.DeadlineExceeded)
	require.NoError(t, callCtx.Err(), "an admitted call can still collect its signed outcome")

	parent, cancelParent := context.WithCancel(t.Context())
	budget, cancelBudget = newScenarioBudget(parent, time.Hour)
	defer cancelBudget()
	callCtx, finish = agentCallContext(budget, budget)
	defer finish()
	cancelParent()
	select {
	case <-callCtx.Done():
		require.ErrorIs(t, callCtx.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("explicit cancellation must interrupt an admitted call")
	}
}

// TestBudgetCall_CarriesTheFollowedContextsValues keeps request-scoped values.
func TestBudgetCall_CarriesTheFollowedContextsValues(t *testing.T) {
	type key struct{}
	parent, cancelParent := context.WithTimeout(context.WithValue(t.Context(), key{}, "v"), time.Hour)
	defer cancelParent()
	budget, cancelBudget := newScenarioBudget(parent, time.Minute)
	defer cancelBudget()
	callCtx, finish := agentCallContext(budget, budget)
	defer finish()
	assert.Equal(t, "v", callCtx.Value(key{}))
}

// TestBudgetCall_HeldBudgetDoesNotCutTheCallOff keeps an in-flight call alive
// while the budget is held: the held wait is credited only on release, so the
// call must not end at its drained deadline before that credit lands.
func TestBudgetCall_HeldBudgetDoesNotCutTheCallOff(t *testing.T) {
	budget, cancelBudget := newScenarioBudget(t.Context(), 20*time.Millisecond)
	defer cancelBudget()
	budget.hold()
	callCtx, finish := budget.follow(budget, 20*time.Millisecond)
	defer finish()

	time.Sleep(100 * time.Millisecond)
	select {
	case <-callCtx.Done():
		t.Fatalf("the call ended while the budget was held: %v", callCtx.Err())
	default:
	}

	budget.release(100 * time.Millisecond)
	require.NoError(t, callCtx.Err(), "the credit on release moves the call past the hold")
	select {
	case <-callCtx.Done():
		require.ErrorIs(t, callCtx.Err(), context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("the call must still end at its moved deadline")
	}
}

// TestBudgetCall_HeldCallEndsWhenItsBudgetEndsByParentDeadline shows a call
// whose deadline passed during a hold ends once the parent ends the budget, so
// it is not left waiting on a timer the hold suppressed.
func TestBudgetCall_HeldCallEndsWhenItsBudgetEndsByParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancelParent()
	budget, cancelBudget := newScenarioBudget(parent, time.Hour)
	defer cancelBudget()
	budget.hold()
	callCtx, finish := budget.follow(budget, 10*time.Millisecond)
	defer finish()

	time.Sleep(80 * time.Millisecond)
	budget.release(0)
	select {
	case <-callCtx.Done():
		require.ErrorIs(t, callCtx.Err(), context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("a call whose deadline passed during the hold must end once the budget ends")
	}
}
