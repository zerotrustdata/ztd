// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sqliteutil

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestNewPruner_DefaultsNegativeIntervalToOneHour(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, -1*time.Second, func(_ context.Context, _ *DB, _ *slog.Logger) error { return nil })
	assert.Equal(t, time.Hour, p.interval)
}

func TestNewPruner_DefaultsZeroIntervalToOneHour(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, 0, func(_ context.Context, _ *DB, _ *slog.Logger) error { return nil })
	assert.Equal(t, time.Hour, p.interval)
}

func TestNewPruner_RespectsPositiveInterval(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, 5*time.Minute, func(_ context.Context, _ *DB, _ *slog.Logger) error { return nil })
	assert.Equal(t, 5*time.Minute, p.interval)
}

func TestPruner_InvokesFnOnTick(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "pruner.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	var callCount atomic.Int64
	fn := func(_ context.Context, _ *DB, _ *slog.Logger) error {
		callCount.Add(1)
		return nil
	}

	p := NewPruner(db, logger, 20*time.Millisecond, fn)
	p.Start()
	t.Cleanup(p.Stop)

	assert.Eventually(t, func() bool {
		return callCount.Load() >= 2
	}, 2*time.Second, 10*time.Millisecond)
}

func TestPruner_Stop_IsIdempotent(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	var calls atomic.Int32
	p := NewPruner(nil, logger, time.Millisecond, func(_ context.Context, _ *DB, _ *slog.Logger) error {
		calls.Add(1)
		return nil
	})
	p.Start()
	p.Stop()
	assert.False(t, p.started)
	stopped := calls.Load()

	p.Stop()
	assert.False(t, p.started)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, stopped, calls.Load(), "a second Stop must not resume pruning")
}

func TestPruner_Stop_BeforeStart_LeavesPrunerStartable(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	var calls atomic.Int32
	p := NewPruner(nil, logger, 10*time.Millisecond, func(_ context.Context, _ *DB, _ *slog.Logger) error {
		calls.Add(1)
		return nil
	})

	p.Stop()
	assert.False(t, p.started)

	p.Start()
	assert.Eventually(t, func() bool { return calls.Load() >= 1 }, time.Second, 5*time.Millisecond)
	p.Stop()
	assert.False(t, p.started)
}

func TestPruner_Stop_HaltsInvocations(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "halt.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	var callCount atomic.Int64
	fn := func(_ context.Context, _ *DB, _ *slog.Logger) error {
		callCount.Add(1)
		return nil
	}

	p := NewPruner(db, logger, 20*time.Millisecond, fn)
	p.Start()

	assert.Eventually(t, func() bool {
		return callCount.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	p.Stop()
	countAfterStop := callCount.Load()

	require.Eventually(t, func() bool {
		return callCount.Load() == countAfterStop
	}, 200*time.Millisecond, 10*time.Millisecond, "no more invocations after Stop")
}

func TestPruner_FnReceivesCorrectLogger(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "logarg.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	var receivedLogger *slog.Logger
	done := make(chan struct{})
	fn := func(_ context.Context, _ *DB, l *slog.Logger) error {
		receivedLogger = l
		select {
		case <-done:
		default:
			close(done)
		}
		return nil
	}

	p := NewPruner(db, logger, 20*time.Millisecond, fn)
	p.Start()
	t.Cleanup(p.Stop)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pruner fn was never called")
	}

	assert.Same(t, logger, receivedLogger)
}

func TestPruner_StartAfterStop_DoesNotInvokeFn(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "postStop.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	var callCount atomic.Int64
	fn := func(_ context.Context, _ *DB, _ *slog.Logger) error {
		callCount.Add(1)
		return nil
	}

	p := NewPruner(db, logger, 20*time.Millisecond, fn)
	p.Start()

	assert.Eventually(t, func() bool {
		return callCount.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	p.Stop()

	p.Start()
	t.Cleanup(p.Stop)

	countAfterRestart := callCount.Load()
	require.Eventually(t, func() bool {
		return callCount.Load() == countAfterRestart
	}, 200*time.Millisecond, 10*time.Millisecond, "fn must not be invoked after Start on a stopped pruner")
}

func TestPruner_FnReceivesCorrectDB(t *testing.T) {
	t.Parallel()
	fileSvc := newTestRuntimeFiles(t)
	dir := fileSvc.Resolve("")
	logger := testutil.NewTestLogger()
	cfg := DefaultDBConfig(filepath.Join(dir, "dbarg.db"))

	db, err := OpenDB(fileSvc, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	var receivedDB *DB
	done := make(chan struct{})
	fn := func(_ context.Context, d *DB, _ *slog.Logger) error {
		receivedDB = d
		select {
		case <-done:
		default:
			close(done)
		}
		return nil
	}

	p := NewPruner(db, logger, 20*time.Millisecond, fn)
	p.Start()
	t.Cleanup(p.Stop)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pruner fn was never called")
	}

	assert.Same(t, db, receivedDB)
}

// Tier 1 Unit Tests (no external dependencies - mocks/stubs only)

func TestNewPruner_Tier1_SetsFieldsCorrectly(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	db := &DB{}
	interval := 5 * time.Minute
	callCount := 0
	fn := func(_ context.Context, _ *DB, _ *slog.Logger) error {
		callCount++
		return nil
	}

	p := NewPruner(db, logger, interval, fn)

	assert.Same(t, db, p.db)
	assert.Same(t, logger, p.logger)
	assert.Equal(t, interval, p.interval)
	assert.NotNil(t, p.fn)
	assert.NotNil(t, p.ctx)
	assert.NotNil(t, p.cancel)
	assert.False(t, p.started)
}

func TestNewPruner_Tier1_NilDBAllowed(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	fn := func(_ context.Context, _ *DB, _ *slog.Logger) error { return nil }

	p := NewPruner(nil, logger, time.Hour, fn)

	assert.Nil(t, p.db)
	assert.NotNil(t, p)
}

func TestNewPruner_Tier1_NilLoggerAllowed(t *testing.T) {
	t.Parallel()
	db := &DB{}
	fn := func(_ context.Context, _ *DB, _ *slog.Logger) error { return nil }

	p := NewPruner(db, nil, time.Hour, fn)

	assert.Nil(t, p.logger)
	assert.NotNil(t, p)
}

func TestNewPruner_Tier1_NilFnAllowed(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	db := &DB{}

	p := NewPruner(db, logger, time.Hour, nil)

	assert.Nil(t, p.fn)
	assert.NotNil(t, p)
}

func TestPruner_Start_Tier1_Idempotent(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, time.Hour, nil)

	p.Start()
	t.Cleanup(p.Stop)
	assert.True(t, p.started)

	p.Start()
	assert.True(t, p.started, "second Start should be no-op")
}

func TestPruner_Stop_Tier1_Idempotent(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, time.Hour, nil)

	p.Stop()
	assert.False(t, p.started)

	p.Stop()
	assert.False(t, p.started, "second Stop should be no-op")
}

func TestPruner_Stop_Tier1_WithoutStartDoesNotPanic(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, time.Hour, nil)

	assert.NotPanics(t, func() {
		p.Stop()
	})
	assert.False(t, p.started)
}

func TestPruner_Start_Tier1_SetsStartedFlag(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, time.Hour, nil)

	assert.False(t, p.started)
	p.Start()
	t.Cleanup(p.Stop)
	assert.True(t, p.started)
}

func TestPruner_Stop_Tier1_ResetsStartedFlag(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, time.Hour, nil)

	p.Start()
	assert.True(t, p.started)

	p.Stop()
	assert.False(t, p.started)
}

func TestPruner_Tier1_ContextCancelledOnStop(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, time.Hour, nil)

	require.NoError(t, p.ctx.Err(), "context should not be cancelled initially")

	p.Start()
	p.Stop()

	assert.ErrorIs(t, p.ctx.Err(), context.Canceled, "context should be cancelled after Stop")
}

func TestPruner_Tier1_FnStoresCorrectly(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	called := false
	fn := func(_ context.Context, _ *DB, _ *slog.Logger) error {
		called = true
		return nil
	}

	p := NewPruner(nil, logger, time.Hour, fn)

	assert.False(t, called)
	p.fn(context.Background(), nil, logger)
	assert.True(t, called)
}

func TestPruner_Tier1_FnErrorHandling(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	expectedErr := errors.New("prune failed")
	fn := func(_ context.Context, _ *DB, _ *slog.Logger) error {
		return expectedErr
	}

	p := NewPruner(nil, logger, time.Hour, fn)

	err := p.fn(context.Background(), nil, logger)
	assert.ErrorIs(t, err, expectedErr)
}

func TestPruner_Tier1_ContextPassedToFn(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	var receivedCtx any
	fn := func(ctx context.Context, _ *DB, _ *slog.Logger) error {
		receivedCtx = ctx
		return nil
	}

	p := NewPruner(nil, logger, time.Hour, fn)

	testCtx := context.Background()
	p.fn(testCtx, nil, logger)
	assert.Equal(t, testCtx, receivedCtx)
}

func TestPruner_Tier1_DBPassedToFn(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	db := &DB{}
	var receivedDB *DB
	fn := func(_ context.Context, d *DB, _ *slog.Logger) error {
		receivedDB = d
		return nil
	}

	p := NewPruner(db, logger, time.Hour, fn)

	p.fn(context.Background(), db, logger)
	assert.Same(t, db, receivedDB)
}

func TestPruner_Tier1_LoggerPassedToFn(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	var receivedLogger *slog.Logger
	fn := func(_ context.Context, _ *DB, l *slog.Logger) error {
		receivedLogger = l
		return nil
	}

	p := NewPruner(nil, logger, time.Hour, fn)

	p.fn(context.Background(), nil, logger)
	assert.Same(t, logger, receivedLogger)
}

func TestPruner_Tier1_MutexProtectsStartedFlag(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	p := NewPruner(nil, logger, time.Hour, nil)

	// Concurrent Start calls should be safe
	done := make(chan struct{}, 2)
	go func() {
		p.Start()
		done <- struct{}{}
	}()
	go func() {
		p.Start()
		done <- struct{}{}
	}()

	<-done
	<-done
	t.Cleanup(p.Stop)
	assert.True(t, p.started)
}
