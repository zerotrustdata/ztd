// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// deferredCancellationContext lets the test delay child cancellation callbacks.
// Context cancellation does not guarantee that sibling children stop together.
type deferredCancellationContext struct {
	context.Context
	callbacks chan func()
}

// Hide the underlying cancel context so children register through AfterFunc.
func (c *deferredCancellationContext) Value(any) any { return nil }

func (c *deferredCancellationContext) AfterFunc(f func()) func() bool {
	var stopped atomic.Bool
	c.callbacks <- func() {
		if !stopped.Swap(true) {
			f()
		}
	}
	return func() bool { return !stopped.Swap(true) }
}

func TestApprovalSubscription_WaitClassifiesCallerCancellationBeforeChildPropagation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancellation"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(testutil.Context(t), time.Second)
				defer cancel()
				caller := &deferredCancellationContext{Context: ctx, callbacks: make(chan func(), 1)}
				sub := &ApprovalSubscription{done: make(chan struct{}), arrived: make(chan struct{})}
				result := make(chan error, 1)
				go func() {
					receipt, err := sub.Wait(caller, "tx-cancelled", "")
					assert.Nil(t, receipt)
					result <- err
				}()
				synctest.Wait()

				if deadline {
					time.Sleep(time.Second)
				} else {
					cancel()
				}
				<-ctx.Done()
				// The stream has stopped, but Wait's child cancellation callback
				// has not run. The caller's context still identifies the cause.
				close(sub.done)
				err := <-result
				select {
				case callback := <-caller.callbacks:
					callback()
				default:
				}
				require.ErrorIs(t, err, constants.ErrApprovalSSETimeout)
				assert.NotErrorIs(t, err, constants.ErrSSEStreamClosed)
			})
		})
	}
}
