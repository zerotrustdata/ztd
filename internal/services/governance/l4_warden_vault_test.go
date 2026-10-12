// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// lockedRuntimeVault reports a locked vault, the state in which execution cannot be recorded.
type lockedRuntimeVault struct{}

func (lockedRuntimeVault) IsUnlocked() bool { return false }

// TestL4Warden_RefusesDispatchWhenRuntimeVaultLocked verifies that a locked or absent runtime
// vault refuses dispatch before any envelope check, so nothing executes unrecorded.
func TestL4Warden_RefusesDispatchWhenRuntimeVaultLocked(t *testing.T) {
	env := &govtypes.GovernanceEnvelope{Nonce: "vault-nonce"}

	locked := NewL4Warden(testutil.NewTestLogger(), nil, nil, nil, nil, nil, nil, nil, nil, nil, lockedRuntimeVault{})
	_, err := locked.VerifyEnvelope(context.Background(), env)
	require.ErrorIs(t, err, constants.ErrWardenVaultLocked)

	absent := NewL4Warden(testutil.NewTestLogger(), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = absent.VerifyEnvelope(context.Background(), env)
	require.ErrorIs(t, err, constants.ErrWardenVaultUnavailable)
}
