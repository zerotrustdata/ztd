// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !unix

package testcmd

import (
	"context"
	"fmt"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// smokeProcessOwner is unsupported off unix: the runner refuses to start rather
// than leave owned children without process-group cleanup.
type smokeProcessOwner struct{}

type smokeOwnedProcess struct {
	name       string
	stdoutPath string
}

func (p *smokeOwnedProcess) exitedCh() <-chan struct{} { return nil }

func (o *smokeProcessOwner) start(name, _, _ string, _, _ []string) (*smokeOwnedProcess, error) {
	return nil, fmt.Errorf("release smoke process groups are unsupported on this platform: %w", constants.ErrAgentNotSupported)
}

func (o *smokeProcessOwner) wait(_ context.Context, _ *smokeOwnedProcess) error {
	return fmt.Errorf("release smoke process groups are unsupported on this platform: %w", constants.ErrAgentNotSupported)
}

func (o *smokeProcessOwner) stop(_ *smokeOwnedProcess, _ time.Duration) error {
	return nil
}

func (o *smokeProcessOwner) stopAll(_ time.Duration) error {
	return nil
}
