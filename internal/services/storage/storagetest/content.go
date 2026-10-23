// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storagetest

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
)

// EncryptCommitmentAttestation encrypts a commitment_ledger.attestation_json fixture the way
// the commitment ledger stores it: bound to the commitment hash of its row. Tests that write or
// rewrite raw rows use it so the row still decrypts.
func EncryptCommitmentAttestation(tb testing.TB, v *vault.Vault, hash, payload string) []byte {
	tb.Helper()
	data, err := v.EncryptWithAAD([]byte(payload), storage.AuditContentAAD(storage.CommitmentColumnAttestationJSON, hash))
	require.NoError(tb, err)
	return data
}

// EncryptCommitmentTargetResource encrypts a commitment_ledger.target_resource fixture bound to
// the commitment hash of its row.
func EncryptCommitmentTargetResource(tb testing.TB, v *vault.Vault, hash, payload string) []byte {
	tb.Helper()
	data, err := v.EncryptWithAAD([]byte(payload), storage.AuditContentAAD(storage.CommitmentColumnTargetResource, hash))
	require.NoError(tb, err)
	return data
}

// EncryptEventContent encrypts an events.content_text fixture the way the audit store stores it:
// bound to the chain sequence of its row.
func EncryptEventContent(tb testing.TB, v *vault.Vault, seq int64, payload []byte) []byte {
	tb.Helper()
	data, err := v.EncryptWithAAD(payload, storage.AuditContentAAD("events.content_text", storage.EventContentRowID(seq)))
	require.NoError(tb, err)
	return data
}
