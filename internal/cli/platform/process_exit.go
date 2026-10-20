// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"
	"errors"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// WaitForProcessExit blocks until the process with pid has exited and returns
// nil. It returns ctx.Err() when ctx ends first, and an error when the exit
// cannot be observed. A process that is already gone returns nil at once.
func WaitForProcessExit(ctx context.Context, pid int) error {
	watch, err := WatchProcessExit(pid)
	if errors.Is(err, constants.ErrProcessNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = watch.Close() }()
	return watch.Wait(ctx)
}
