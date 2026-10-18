// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"sort"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// evidenceWindowWatch follows the Gateway's recorded events for one evidence
// kind. connected runs on each (re)connect of the event stream; recordings
// before it were not delivered, so the waiter reconciles there. It returns nil
// when ctx ends and an error when the stream cannot continue.
type evidenceWindowWatch func(ctx context.Context, connected func(), recorded func(providerAttemptID string)) error

// evidenceWindowCapture persists the Gateway window for one attempt. It reports
// false, without error, when the Gateway has not recorded the window yet.
type evidenceWindowCapture func(ctx context.Context, providerAttemptID string) (bool, error)

// awaitEvidenceWindows captures every pending attempt as the Gateway records
// it. Each (re)connect reconciles all pending attempts and each recorded event
// captures its attempt; nothing is re-read on a timer. It ends with nil once
// pending is empty, the capture error, or missing(cause) when ctx or the
// stream ends first. The caller owns the deadline.
func awaitEvidenceWindows(
	ctx context.Context,
	pending map[string]struct{},
	watch evidenceWindowWatch,
	capture evidenceWindowCapture,
	missing func(cause error) error,
) error {
	if len(pending) == 0 {
		return nil
	}
	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	connected := make(chan struct{}, 1)
	recorded := make(chan string)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- watch(watchCtx, func() {
			select {
			case connected <- struct{}{}:
			default:
			}
		}, func(id string) {
			select {
			case recorded <- id:
			case <-watchCtx.Done():
			}
		})
	}()
	captureOne := func(id string) error {
		ok, err := capture(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return missing(ctx.Err())
			}
			return err
		}
		if ok {
			delete(pending, id)
		}
		return nil
	}
	for len(pending) > 0 {
		select {
		case <-ctx.Done():
			return missing(ctx.Err())
		case err := <-watchDone:
			if err == nil {
				err = constants.ErrSSEStreamClosed
			}
			return missing(err)
		case <-connected:
			for id := range pending {
				if err := captureOne(id); err != nil {
					return err
				}
			}
		case id := <-recorded:
			if _, ok := pending[id]; ok {
				if err := captureOne(id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// missingEvidenceWindowsError marks windows that never arrived as unavailable
// evidence, keeping why the wait ended and a sample of the missing attempts.
func missingEvidenceWindowsError(kind, hint string, cause error, pending map[string]struct{}) error {
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	count := len(ids)
	if len(ids) > 5 {
		ids = ids[:5]
	}
	return fmt.Errorf("evaluation: wait for %s: %w: %w: %d missing windows (sample attempts: %v); %s",
		kind, constants.ErrEvaluationObservationUnavailable, cause, count, ids, hint)
}
