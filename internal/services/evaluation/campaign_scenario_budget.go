// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
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
type scenarioBudget struct {
	parent     context.Context
	done       chan struct{}
	timer      *time.Timer
	stopParent func() bool

	mu       sync.Mutex
	deadline time.Time
	err      error
	holds    int
}

// newScenarioBudget starts a budget of limit under parent. The returned cancel
// releases its timer and ends the budget with context.Canceled.
func newScenarioBudget(parent context.Context, limit time.Duration) (*scenarioBudget, context.CancelFunc) {
	b := &scenarioBudget{parent: parent, done: make(chan struct{}), deadline: time.Now().Add(limit)}
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
	b.deadline = next
	return true
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
			b.deadline = next
		}
	}
	if b.holds > 0 {
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
	b.mu.Unlock()
	b.stopParent()
}
