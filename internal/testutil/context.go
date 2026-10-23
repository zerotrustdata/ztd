// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"context"
	"testing"
	"time"
)

// DefaultTestTimeout bounds a test context that performs I/O. A stuck call
// fails the test instead of holding the package until the runner's deadline.
const DefaultTestTimeout = 30 * time.Second

// Context returns a context cancelled when the test finishes and expired after
// DefaultTestTimeout. Tests that do I/O use it so cancellation is owned by the
// test (INV-TEST-FIX-03).
func Context(tb testing.TB) context.Context {
	tb.Helper()
	return ContextWithTimeout(tb, DefaultTestTimeout)
}

// ContextWithTimeout returns a context cancelled when the test finishes and
// expired after d. Use it when a test's I/O legitimately needs a budget other
// than DefaultTestTimeout.
func ContextWithTimeout(tb testing.TB, d time.Duration) context.Context {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	tb.Cleanup(cancel)
	return ctx
}
