// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"crypto/x509"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestCLIRenewalLoop_SchedulesRetriesAndStops(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		wantCalls int
	}{
		{"renews at two thirds", nil, 1},
		{"retries a transient failure", constants.ErrHTTPRequestExecuteFailed, 2},
		{"requires manual recovery for a stale certificate", constants.ErrCLIRenewalStaleCertificate, 1},
		{"stops for a Gateway without renewal", constants.ErrCLIRenewalUnavailable, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			leaf := &x509.Certificate{NotBefore: now, NotAfter: now.Add(9 * time.Hour)}
			ctx, cancel := context.WithCancel(testutil.Context(t))
			defer cancel()
			calls := 0
			var waits []time.Duration
			loop := cliRenewalLoop{
				current: func() *x509.Certificate { return leaf },
				now:     func() time.Time { return now },
				wait: func(ctx context.Context, d time.Duration) bool {
					if ctx.Err() != nil {
						return false
					}
					waits = append(waits, d)
					now = now.Add(d)
					return true
				},
				renew: func(context.Context) (bool, error) {
					calls++
					if calls == 1 {
						require.Equal(t, leaf.NotBefore.Add(6*time.Hour), now)
						if test.err != nil {
							return false, test.err
						}
					}
					// Simulate a committed new certificate, then let the command
					// cancellation stop the loop at its next scheduled wait.
					leaf = &x509.Certificate{NotBefore: now, NotAfter: now.Add(9 * time.Hour)}
					cancel()
					return true, nil
				},
				logger: slog.New(slog.DiscardHandler),
			}
			loop.run(ctx)
			require.Equal(t, test.wantCalls, calls)
			require.Equal(t, 6*time.Hour, waits[0])
			if calls == 2 {
				require.Equal(t, time.Second, waits[1])
			}
		})
	}
}

func TestCLIRenewalLoop_SkippedAttemptsBackOffUntilExpiry(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	leaf := &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(4 * time.Second)}
	calls := 0
	var waits []time.Duration
	loop := cliRenewalLoop{
		current: func() *x509.Certificate { return leaf },
		now:     func() time.Time { return now },
		wait:    func(_ context.Context, d time.Duration) bool { waits = append(waits, d); now = now.Add(d); return true },
		renew:   func(context.Context) (bool, error) { calls++; return false, nil },
		logger:  slog.New(slog.DiscardHandler),
	}
	loop.run(testutil.Context(t))
	require.Equal(t, 3, calls)
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, time.Second}, waits)
}

func TestCLIRenewalLoop_DoesNotRenewExpiredMissingOrCanceledIdentities(t *testing.T) {
	now := time.Now()
	valid := &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	cancelled, cancel := context.WithCancel(testutil.Context(t))
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		leaf *x509.Certificate
	}{
		{"missing identity", testutil.Context(t), nil},
		{"expired identity is left for reinstatement at command start", testutil.Context(t), &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now}},
		{"cancelled command", cancelled, valid},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			loop := cliRenewalLoop{
				current: func() *x509.Certificate { reads++; return test.leaf },
				now:     func() time.Time { return now },
				renew:   func(context.Context) (bool, error) { t.Fatal("unexpected renewal"); return false, nil },
				wait:    func(context.Context, time.Duration) bool { t.Fatal("unexpected wait"); return false },
			}
			loop.run(test.ctx)
			if test.ctx.Err() != nil {
				assert.Zero(t, reads, "a cancelled command reads no identity")
			} else {
				assert.Equal(t, 1, reads)
			}
		})
	}
}

func TestWaitForCLIRenewal_ReturnsFalseOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.Context(t))
	cancel()
	assert.False(t, waitForCLIRenewal(ctx, time.Hour))
	assert.True(t, waitForCLIRenewal(testutil.Context(t), time.Millisecond))
}

func TestCLIRenewalLoop_CancellationInterruptsScheduledWait(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(testutil.Context(t))
	t.Cleanup(cancel)
	waiting, done := make(chan struct{}), make(chan struct{})
	loop := cliRenewalLoop{
		current: func() *x509.Certificate { return &x509.Certificate{NotBefore: now, NotAfter: now.Add(9 * time.Hour)} },
		now:     time.Now,
		wait:    func(ctx context.Context, d time.Duration) bool { close(waiting); return waitForCLIRenewal(ctx, d) },
		renew: func(context.Context) (bool, error) {
			t.Error("renewal fired before its scheduled time")
			return false, nil
		},
	}
	go func() { defer close(done); loop.run(ctx) }()
	<-waiting
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("renewal loop did not stop on command cancellation")
	}
}
