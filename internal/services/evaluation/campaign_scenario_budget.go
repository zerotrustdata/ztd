// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"sync"
	"time"
)

// scenarioBudget is a scenario's wall-clock budget as a context. A wait the
// provider asked for before a retry is not the model's time, so credit moves
// the deadline later by the whole wait. The parent context (the assignment
// deadline, --assignment-timeout) still bounds everything: a credit that would
// pass the parent's deadline is refused, never granted in part. Err is exactly
// context.DeadlineExceeded once the budget runs out, and the parent's error
// when the parent ends first.
//
// A wait the Gateway takes (a Tribunal member retry) happens inside one call
// and is known only when the call returns. hold defers the budget's expiry for
// the call and release credits the wait afterwards, so the provider's time is
// never charged to the model.
//
// A call context taken from the budget (follow) moves later by every credit
// the budget grants, under the same refusal, so a credited wait never uses up
// the drain of a call still in flight.
type scenarioBudget struct {
	parent     context.Context
	done       chan struct{}
	timer      *time.Timer
	stopParent func() bool

	mu       sync.Mutex
	deadline time.Time
	err      error
	holds    int
	calls    map[*budgetCall]struct{}
}

// newScenarioBudget starts a budget of limit under parent. The returned cancel
// releases its timer and ends the budget with context.Canceled.
func newScenarioBudget(parent context.Context, limit time.Duration) (*scenarioBudget, context.CancelFunc) {
	b := &scenarioBudget{parent: parent, done: make(chan struct{}), deadline: time.Now().Add(limit), calls: make(map[*budgetCall]struct{})}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.timer = time.AfterFunc(limit, b.expire)
	b.stopParent = context.AfterFunc(parent, func() { b.end(parent.Err()) })
	return b, func() { b.end(context.Canceled) }
}

// Deadline returns the budget's current deadline, which credit moves later,
// or the parent's when that comes first.
func (b *scenarioBudget) Deadline() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if parentDeadline, ok := b.parent.Deadline(); ok && parentDeadline.Before(b.deadline) {
		return parentDeadline, true
	}
	return b.deadline, true
}

// Done is closed when the budget runs out or the parent ends.
func (b *scenarioBudget) Done() <-chan struct{} { return b.done }

// Err is nil until Done is closed.
func (b *scenarioBudget) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Value is the parent's.
func (b *scenarioBudget) Value(key any) any { return b.parent.Value(key) }

// credit moves the deadline later by wait. It refuses (false) once the budget
// has ended or its deadline has passed, and when the new deadline would pass
// the parent's.
func (b *scenarioBudget) credit(wait time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil || !time.Now().Before(b.deadline) {
		return false
	}
	next := b.deadline.Add(wait)
	if parentDeadline, ok := b.parent.Deadline(); ok && next.After(parentDeadline) {
		return false
	}
	b.moveLocked(wait)
	return true
}

// moveLocked moves the deadline later by wait and every followed call with
// it. b.mu is held.
func (b *scenarioBudget) moveLocked(wait time.Duration) {
	b.deadline = b.deadline.Add(wait)
	for call := range b.calls {
		call.extend(wait)
	}
}

// holding reports whether an unended budget is held.
func (b *scenarioBudget) holding() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err == nil && b.holds > 0
}

// hold defers the budget's expiry until the matching release, so a call whose
// waits are credited only when it returns is not cut off by the deadline. The
// parent still ends the budget immediately. Holds nest; the budget is
// evaluated when the last one is released. A nil budget holds nothing.
func (b *scenarioBudget) hold() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.holds++
}

// release ends one hold and moves the deadline later by credit, the wait the
// held call spent on the provider's behalf. A credit that would pass the
// parent's deadline is refused, never granted in part. The credit applies even
// when the deadline passed during the hold: that is the time it covers. When
// the last hold is released the budget ends with context.DeadlineExceeded if
// its (possibly credited) deadline has passed, and otherwise re-arms for the
// time left. A budget that already ended, by its parent or its cancel, only
// drops the hold. A nil budget has nothing to release.
func (b *scenarioBudget) release(credit time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.holds > 0 {
		b.holds--
	}
	if b.err != nil {
		b.mu.Unlock()
		return
	}
	if credit > 0 {
		next := b.deadline.Add(credit)
		if parentDeadline, ok := b.parent.Deadline(); !ok || !next.After(parentDeadline) {
			b.moveLocked(credit)
		}
	}
	if b.holds > 0 {
		b.mu.Unlock()
		return
	}
	b.rearmCallsLocked()
	if left := time.Until(b.deadline); left > 0 {
		b.timer.Reset(left)
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.end(context.DeadlineExceeded)
}

// rearmCallsLocked re-arms every followed call's timer so a call whose
// deadline passed while the budget was held ends now, or at its moved deadline.
// b.mu is held.
func (b *scenarioBudget) rearmCallsLocked() {
	for call := range b.calls {
		call.rearm()
	}
}

// expire ends the budget when its deadline has passed; a credited deadline
// re-arms the timer for the time left. A held budget does not expire: its
// release evaluates the deadline instead.
func (b *scenarioBudget) expire() {
	b.mu.Lock()
	if b.err != nil || b.holds > 0 {
		b.mu.Unlock()
		return
	}
	if left := time.Until(b.deadline); left > 0 {
		b.timer.Reset(left)
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.end(context.DeadlineExceeded)
}

func (b *scenarioBudget) end(err error) {
	b.mu.Lock()
	if b.err != nil {
		b.mu.Unlock()
		return
	}
	b.err = err
	close(b.done)
	b.timer.Stop()
	b.rearmCallsLocked()
	b.mu.Unlock()
	b.stopParent()
}

// follow returns a call context under ctx (the budget or a context derived
// from it) whose deadline starts at ctx's deadline plus drain and moves later
// by every credit the budget grants from now on. It is cancelled when ctx is
// cancelled, but not when ctx reaches its deadline: an admitted call drains.
// Err is context.DeadlineExceeded at the (moved) deadline. The returned cancel
// stops following the budget and ends the call with context.Canceled.
func (b *scenarioBudget) follow(ctx context.Context, drain time.Duration) (context.Context, context.CancelFunc) {
	deadline, _ := ctx.Deadline()
	call := &budgetCall{budget: b, values: context.WithoutCancel(ctx), done: make(chan struct{}), deadline: deadline.Add(drain)}
	call.mu.Lock()
	call.timer = time.AfterFunc(time.Until(call.deadline), call.expire)
	call.mu.Unlock()
	b.mu.Lock()
	b.calls[call] = struct{}{}
	b.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			call.end(context.Canceled)
		}
	})
	return call, func() {
		stop()
		b.mu.Lock()
		delete(b.calls, call)
		b.mu.Unlock()
		call.end(context.Canceled)
	}
}

// budgetCall is a call context whose deadline follows its scenario budget's
// credits (scenarioBudget.follow).
type budgetCall struct {
	budget *scenarioBudget
	values context.Context
	done   chan struct{}
	timer  *time.Timer

	mu       sync.Mutex
	deadline time.Time
	err      error
}

// Deadline is the call's current deadline.
func (c *budgetCall) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deadline, true
}

// Done is closed when the call is cancelled or reaches its deadline.
func (c *budgetCall) Done() <-chan struct{} { return c.done }

// Err is nil until Done is closed.
func (c *budgetCall) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Value is the followed context's.
func (c *budgetCall) Value(key any) any { return c.values.Value(key) }

// extend moves the call's deadline later by wait; an ended call stays ended.
func (c *budgetCall) extend(wait time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.deadline = c.deadline.Add(wait)
	c.timer.Reset(time.Until(c.deadline))
}

// expire ends the call once its deadline has passed, and re-arms the timer
// for a deadline that moved later.
func (c *budgetCall) expire() {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	if left := time.Until(c.deadline); left > 0 {
		c.timer.Reset(left)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	// A held budget defers the call's expiry the same way it defers its own:
	// the release re-arms the call once the wait is credited.
	if c.budget.holding() {
		return
	}
	c.end(context.DeadlineExceeded)
}

// rearm points the call's timer at its current deadline, so an expiry the
// hold deferred is evaluated again. b.mu is held by the caller.
func (c *budgetCall) rearm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.timer.Reset(max(time.Until(c.deadline), 0))
}

func (c *budgetCall) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
	c.timer.Stop()
}
