// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"crypto/ed25519"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

// LoadAppSigningKey unseals the app's independent L2 identity. The caller owns
// the returned key and must clear it after use. No transport key is reused.
func LoadAppSigningKey(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, appName string) (ed25519.PrivateKey, error) {
	if err := validateAppName(appName); err != nil {
		return nil, err
	}
	if fileSvc == nil || cfg == nil {
		return nil, constants.ErrInternal
	}
	seed, err := UnsealIdentityKey(ctx, fileSvc, cfg, cfg.AppSigningKeyFile(appName))
	if err != nil {
		return nil, fmt.Errorf("app signing identity %q: %w", appName, err)
	}
	defer vault.SecureZero(seed)
	if len(seed) != ed25519.SeedSize {
		return nil, constants.ErrAppSigningKeyInvalid
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
