// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestComputeAuditEventHash_IsDeterministic(t *testing.T) {
	hash1 := computeEventHash(1, eventsChainGenesisPrevHash, "ztd.v1.operator.audit.command", "session-1", "2026-01-01T00:00:00.000000Z", "digest", "tx-1")
	hash2 := computeEventHash(1, eventsChainGenesisPrevHash, "ztd.v1.operator.audit.command", "session-1", "2026-01-01T00:00:00.000000Z", "digest", "tx-1")
	assert.Equal(t, hash1, hash2)
	assert.Len(t, hash1, 64)
}

func newDigestTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return CreateTestVault(t, newTestFileSvc(t, testutil.TempDir(t)), privKey)
}

func TestComputeEventContentDigest_ChangesWithPayload(t *testing.T) {
	v := newDigestTestVault(t)
	eventA := &Event{ContentText: "hello", CommandExitCode: constants.ExitCodeNone}
	eventB := &Event{ContentText: "world", CommandExitCode: constants.ExitCodeNone}

	digestA, err := computeEventContentDigest(v, eventA)
	require.NoError(t, err)
	digestB, err := computeEventContentDigest(v, eventB)
	require.NoError(t, err)
	assert.NotEqual(t, digestA, digestB)
}

func TestComputeEventContentDigest_IsKeyedNotPlainSHA256(t *testing.T) {
	v := newDigestTestVault(t)
	event := &Event{ContentText: "approve", CommandExitCode: constants.ExitCodeNone}

	digest, err := computeEventContentDigest(v, event)
	require.NoError(t, err)

	raw, err := json.Marshal(eventContentDigestInput{ContentText: "approve", CommandExitCode: constants.ExitCodeNone})
	require.NoError(t, err)
	plain := sha256.Sum256(raw)
	assert.NotEqual(t, hex.EncodeToString(plain[:]), digest, "digest must not be the unkeyed SHA-256 of the content")

	other := newDigestTestVault(t)
	otherDigest, err := computeEventContentDigest(other, event)
	require.NoError(t, err)
	assert.NotEqual(t, digest, otherDigest, "digest must depend on the vault key")

	again, err := computeEventContentDigest(v, event)
	require.NoError(t, err)
	assert.Equal(t, digest, again)
}

func TestComputeEventContentDigest_LockedVaultFailsClosed(t *testing.T) {
	v := newDigestTestVault(t)
	v.Lock()
	_, err := computeEventContentDigest(v, &Event{ContentText: "x"})
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked)
	_, err = computeEventContentDigest(nil, &Event{ContentText: "x"})
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked)
}
