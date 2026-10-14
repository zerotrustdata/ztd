// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
)

// sealTestKey seals keyPEM at the absolute runtime path keyPath the way
// enrollment stores identity keys. It gives cfg an in-memory keyring when none
// is set, so later reads through cfg unseal with the same master key.
func sealTestKey(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config, keyPath string, keyPEM []byte) {
	t.Helper()
	if cfg.Keyring == nil {
		cfg.Keyring = keystoretest.NewMemoryKeyring()
	}
	require.NoError(t, SealIdentityKey(context.Background(), fileSvc, cfg, keyPath, keyPEM))
}
