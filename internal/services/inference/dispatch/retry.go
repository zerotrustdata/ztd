// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package dispatch

import (
	"context"
	"time"
)

// transientRetryDelays is the wait before each retry of an inference attempt
// whose provider asked to retry later (constants.ErrInferenceBackendTransient,
// RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT): 5 s, 20 s, then 40 s. Together they
// span a provider's per-minute rate window.
var transientRetryDelays = [...]time.Duration{5 * time.Second, 20 * time.Second, 40 * time.Second}

// TransientRetry is the retry policy for an inference attempt the provider
// asked to retry later. Hosted clients never retry; the caller that owns the
// attempt's evidence retries it. Each retry is a new provider attempt with the
// next retry_count, so the Inference Operator classifies it
// INFERENCE_RETRY_CLASSIFICATION_INFRASTRUCTURE_PRE_RESULT and every attempt
// keeps its own signed result or signed FAILED receipt.
type TransientRetry struct {
	// Delays is the wait before each retry, in order; its length bounds the
	// retries.
	Delays []time.Duration
	// Wait blocks for one delay and returns ctx's error when ctx ends first.
	Wait func(ctx context.Context, delay time.Duration) error
}

// DefaultTransientRetry returns the policy every executor-dispatched
// inference retries under.
func DefaultTransientRetry() TransientRetry {
	return TransientRetry{Delays: transientRetryDelays[:], Wait: WaitDelay}
}

// MaxTransientRetries is the most retries DefaultTransientRetry allows for one
// call; a trace verifier rejects a retry_count above it.
func MaxTransientRetries() uint32 {
	return uint32(len(transientRetryDelays))
}

// Delay returns the wait before the attempt with retry_count retryCount, and
// false when the policy allows no such retry. retryCount is one-based here:
// the first attempt (retry_count 0) has no delay.
func (p TransientRetry) Delay(retryCount uint32) (time.Duration, bool) {
	if retryCount == 0 || int(retryCount) > len(p.Delays) {
		return 0, false
	}
	return p.Delays[retryCount-1], true
}

// WaitDelay blocks for delay or until ctx ends, returning ctx's error then.
func WaitDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
