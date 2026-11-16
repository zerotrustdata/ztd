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
type scenarioBudget struct {
	parent     context.Context
	done       chan struct{}
	timer      *time.Timer
	stopParent func() bool

	mu       sync.Mutex
	deadline time.Time
	err      error
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

// expire ends the budget when its deadline has passed; a credited deadline
// re-arms the timer for the time left.
func (b *scenarioBudget) expire() {
	b.mu.Lock()
	if b.err != nil {
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
