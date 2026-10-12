// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

// ProvisionMasterKeyFile writes a fresh base64-encoded 32-byte master key to a
// private file named constants.SecretsFileMasterKey in dir and returns its path,
// in the format the external-file keyring reads. It never overwrites an existing
// file: an existing key in dir is an error, not a replacement. The file is owned
// by the caller, who removes dir when done.
func ProvisionMasterKeyFile(dir string) (string, error) {
	keyBytes := make([]byte, vault.KeySize)
	defer vault.SecureZero(keyBytes)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", fmt.Errorf("generate master key: %w", err)
	}
	path := filepath.Join(dir, constants.SecretsFileMasterKey)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(keyBytes))+1)
	defer vault.SecureZero(encoded)
	base64.StdEncoding.Encode(encoded, keyBytes)
	encoded[len(encoded)-1] = '\n'
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, constants.PermFilePrivate)
	if err != nil {
		return "", fmt.Errorf("create master key %s: %w", path, err)
	}
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write master key: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close master key: %w", err)
	}
	return path, nil
}
