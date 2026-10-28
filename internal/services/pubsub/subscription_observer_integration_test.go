//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

package pubsub

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestCommandSubscriptionObserverReportsConnectAndDisconnect(t *testing.T) {
	f := newPubsubFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Svc.ctx = ctx
	f.Svc.client = &droppingClient{}
	f.Svc.reconnectBaseDelay = time.Microsecond
	changes := make(chan bool, 2)
	f.Svc.onCommandSubscription = func(ctx context.Context, sessionID string, connected bool) error {
		if sessionID != f.Cfg.OperatorSessionId {
			return fmt.Errorf("unexpected session ID %q", sessionID)
		}
		changes <- connected
		if !connected {
			cancel()
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Svc.listenForCommands("command-channel")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("subscription listener did not stop")
	}
	require.Len(t, changes, 2)
	require.True(t, <-changes)
	require.False(t, <-changes)
}

func TestCommandSubscriptionObserverFailureRequestsShutdown(t *testing.T) {
	f := newPubsubFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Svc.ctx = ctx
	f.Svc.client = &droppingClient{}
	f.Svc.onCommandSubscription = func(context.Context, string, bool) error {
		return fmt.Errorf("state publication failed")
	}
	f.Svc.listenForCommands("command-channel")
	select {
	case reason := <-f.Svc.ShutdownChan:
		require.Equal(t, constants.ErrOperatorDeployFailed.Error(), reason)
	default:
		t.Fatal("publication failure left the Operator without a subscription or shutdown request")
	}
}

func TestCommandSubscriptionWaitsForSessionValidation(t *testing.T) {
	f := newPubsubFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Svc.ctx = ctx
	f.Svc.client = &droppingClient{}
	f.Svc.reconnectBaseDelay = time.Microsecond
	var validationCalls atomic.Int32
	f.Svc.beforeCommandSubscription = func(context.Context, string) error {
		if validationCalls.Add(1) == 1 {
			return fmt.Errorf("validation temporarily unavailable")
		}
		return nil
	}
	f.Svc.onCommandSubscription = func(_ context.Context, _ string, connected bool) error {
		if !connected {
			cancel()
		}
		return nil
	}
	f.Svc.listenForCommands("command-channel")
	require.GreaterOrEqual(t, validationCalls.Load(), int32(2))
	require.Equal(t, 1, f.Svc.client.(*droppingClient).subscribeCalls(), "no command subscription may be opened before validation succeeds")
}
