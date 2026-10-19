//go:build !linux && !windows && !darwin

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ProcessExitWatch exists so the shared code compiles here. This platform has
// no exit notification, so WatchProcessExit never returns a watch and the
// methods are never reached; they fail closed anyway.
type ProcessExitWatch struct{}

// WatchProcessExit fails closed: no exit notification is available on this
// platform, and polling would be a defect.
func WatchProcessExit(pid int) (*ProcessExitWatch, error) {
	return nil, fmt.Errorf("platform: watch PID %d exit: %w", pid, constants.ErrUnsupportedPlatform)
}

// Wait fails closed with constants.ErrUnsupportedPlatform.
func (*ProcessExitWatch) Wait(context.Context) error {
	return constants.ErrUnsupportedPlatform
}

// Close has nothing to release.
func (*ProcessExitWatch) Close() error {
	return nil
}
