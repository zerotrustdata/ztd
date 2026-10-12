// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"encoding/hex"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

// StoreKeyMaterial encrypts raw key bytes under the master key and writes them
// to the secrets directory as name. The hex encoding stays in zeroed byte
// buffers so key bytes never become an immutable string.
func (k *Keystore) StoreKeyMaterial(name string, key []byte) error {
	if len(key) == 0 {
		return fmt.Errorf("keystore: store key material %s: %w", name, constants.ErrMissingRequiredField)
	}
	encoded := make([]byte, hex.EncodedLen(len(key)))
	defer vault.SecureZero(encoded)
	hex.Encode(encoded, key)
	return k.writeSecret(name, encoded)
}

// LoadKeyMaterial decrypts key bytes written by StoreKeyMaterial and checks
// they are exactly size bytes long. The caller must SecureZero the result.
func (k *Keystore) LoadKeyMaterial(name string, size int) ([]byte, error) {
	encoded, err := k.readSecret(name)
	if err != nil {
		return nil, err
	}
	defer vault.SecureZero(encoded)
	key := make([]byte, hex.DecodedLen(len(encoded)))
	n, err := hex.Decode(key, encoded)
	if err != nil {
		vault.SecureZero(key)
		return nil, fmt.Errorf("keystore: load key material %s: %w: %w", name, constants.ErrKeyStoreDecodeFailed, err)
	}
	if n != size {
		vault.SecureZero(key)
		return nil, fmt.Errorf("keystore: load key material %s: %w: got %d, expected %d", name, constants.ErrKeyStoreInvalidKeyLength, n, size)
	}
	return key[:n], nil
}
