// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !linux

package keystore

import (
	"context"

	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// EphemeralAgent is a no-op outside Linux: the ssh-agent seal is Linux-only,
// so harness children use the platform key store (Keychain, DPAPI).
type EphemeralAgent struct{}

// StartEphemeralAgent returns the no-op agent.
func StartEphemeralAgent(context.Context, fs.RuntimeFileService, string) (*EphemeralAgent, error) {
	return &EphemeralAgent{}, nil
}

// Env adds nothing; children inherit the host key store.
func (a *EphemeralAgent) Env() []string { return nil }

// Keyring returns nil, which selects the platform key store.
func (a *EphemeralAgent) Keyring(fs.RuntimeFileService) Keyring { return nil }

// Close is a no-op.
func (a *EphemeralAgent) Close() error { return nil }
