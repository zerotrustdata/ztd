// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operator

import "sync"

// MaxFanOutConcurrency bounds the dispatches one fan-out keeps in flight.
const MaxFanOutConcurrency = 64

// FanOut runs dispatch for each of n targets with at most concurrency calls in
// flight and returns the results in target order. It is the one bounded pool
// every Operator fan-out uses: `ztd operator run` and the harness's fanned-out
// tool calls. A concurrency below one runs the targets one at a time.
func FanOut[R any](n, concurrency int, dispatch func(i int) R) []R {
	results := make([]R, n)
	if n == 0 {
		return results
	}
	concurrency = min(max(concurrency, 1), MaxFanOutConcurrency, n)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = dispatch(i)
			}
		}()
	}
	for i := range n {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}
