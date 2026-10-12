// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governancetest

// UnlockedVault satisfies governance.RuntimeVault for tests that exercise the L4
// Warden without a real encryption vault. Production wires the runtime's vault.
type UnlockedVault struct{}

// IsUnlocked always reports an unlocked vault.
func (UnlockedVault) IsUnlocked() bool { return true }
