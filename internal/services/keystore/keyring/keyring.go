// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package keyring declares the master-key store contract with no dependencies,
// so configuration packages can carry a Keyring without importing the keystore
// (and through it the runtime filesystem and vault).
package keyring

// Keyring abstracts the protected store that holds the master encryption key.
// Implementations wrap the macOS Keychain (darwin), the freedesktop Secret
// Service over D-Bus or an ssh-agent-sealed blob (linux), DPAPI (windows),
// plus an in-memory implementation for tests.
// There is deliberately no implementation that writes the master key in
// plaintext next to the secrets it protects.
type Keyring interface {
	// RetrieveMasterKey retrieves the master encryption key from the keyring.
	RetrieveMasterKey() ([]byte, error)
	// StoreMasterKey stores the master encryption key in the keyring.
	StoreMasterKey(key []byte) error
	// DeleteMasterKey removes the master encryption key from the keyring.
	DeleteMasterKey() error
	// Name returns the human-readable name of the keyring implementation.
	Name() string
}
