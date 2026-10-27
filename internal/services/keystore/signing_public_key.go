// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// SigningPublicKey reads a pinned runtime signing identity without creating or
// rotating it. The seed comes from the local keystore, never from evidence.
func (k *Keystore) SigningPublicKey(secretName string) (ed25519.PublicKey, error) {
	if k == nil {
		return nil, constants.ErrTrustedSignerKeyNotFound
	}
	encoded, err := k.DecryptSecret(secretName)
	if err != nil {
		return nil, fmt.Errorf("read signing identity %s: %w", secretName, err)
	}
	seed, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("signing identity %s: %w: %w", secretName, constants.ErrValidationFailed, err)
	}
	if len(seed) != ed25519.SeedSize {
		clear(seed)
		return nil, fmt.Errorf("signing identity %s: %w", secretName, constants.ErrValidationFailed)
	}
	defer clear(seed)
	privateKey := ed25519.NewKeyFromSeed(seed)
	defer clear(privateKey)
	return privateKey.Public().(ed25519.PublicKey), nil
}
