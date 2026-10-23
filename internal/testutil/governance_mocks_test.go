// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockReplayStore(t *testing.T) {
	t.Parallel()
	store := &MockReplayStore{}

	// ReserveNonce never detects replays
	isReplay, err := store.ReserveNonce("test-nonce", time.Now().Add(5*time.Minute))
	require.NoError(t, err)
	require.False(t, isReplay)

	// FinalizeNonce is a no-op
	err = store.FinalizeNonce("test-nonce")
	require.NoError(t, err)

	// ReleaseNonce is a no-op
	err = store.ReleaseNonce("test-nonce")
	require.NoError(t, err)
}

func TestStatefulMockReplayStore(t *testing.T) {
	t.Parallel()
	store := NewStatefulMockReplayStore()
	require.NotNil(t, store)
	require.NotNil(t, store.Nonces)

	nonce := "test-nonce-123"
	expiresAt := time.Now().Add(5 * time.Minute)

	// First use - should not be a replay
	isReplay, err := store.ReserveNonce(nonce, expiresAt)
	require.NoError(t, err)
	require.False(t, isReplay, "First use should not be a replay")

	// Second use - should be a replay
	isReplay, err = store.ReserveNonce(nonce, expiresAt)
	require.NoError(t, err)
	require.True(t, isReplay, "Second use should be detected as replay")

	// Different nonce - should not be a replay
	nonce2 := "test-nonce-456"
	isReplay, err = store.ReserveNonce(nonce2, expiresAt)
	require.NoError(t, err)
	require.False(t, isReplay, "Different nonce should not be a replay")

	// Test ReserveNonce
	nonce3 := "test-nonce-reserve"
	isReplay, err = store.ReserveNonce(nonce3, expiresAt)
	require.NoError(t, err)
	require.False(t, isReplay, "First reserve should not be a replay")

	isReplay, err = store.ReserveNonce(nonce3, expiresAt)
	require.NoError(t, err)
	require.True(t, isReplay, "Second reserve should be detected as replay")

	// Test ReleaseNonce
	err = store.ReleaseNonce(nonce3)
	require.NoError(t, err)

	// After release, nonce should be available again
	isReplay, err = store.ReserveNonce(nonce3, expiresAt)
	require.NoError(t, err)
	require.False(t, isReplay, "After release, nonce should not be a replay")

	// Test FinalizeNonce (no-op)
	err = store.FinalizeNonce(nonce3)
	require.NoError(t, err)
}

func TestStatefulMockReplayStore_Concurrent(t *testing.T) {
	t.Parallel()
	store := NewStatefulMockReplayStore()
	nonce := "concurrent-nonce"
	expiresAt := time.Now().Add(5 * time.Minute)

	// Test concurrent access
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := store.ReserveNonce(nonce, expiresAt)
			assert.NoError(t, err)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Nonce should be marked as used
	isReplay, err := store.ReserveNonce(nonce, expiresAt)
	require.NoError(t, err)
	require.True(t, isReplay, "Nonce should be marked as used after concurrent access")
}

func TestMockStateRootProvider(t *testing.T) {
	t.Parallel()
	root := "test-state-root-123"
	provider := NewMockStateRootProvider(root)

	require.Equal(t, root, provider.Root, "Root should be set correctly")

	retrievedRoot, err := provider.GetCurrentStateRoot(t.Context())
	require.NoError(t, err)
	require.Equal(t, root, retrievedRoot, "Retrieved root should match set root")
}

func TestMockStateRootProvider_EmptyRoot(t *testing.T) {
	t.Parallel()
	provider := NewMockStateRootProvider("")
	require.Empty(t, provider.Root)

	retrievedRoot, err := provider.GetCurrentStateRoot(t.Context())
	require.NoError(t, err)
	require.Empty(t, retrievedRoot)
}

func TestMockL3Notary(t *testing.T) {
	t.Parallel()
	notary := &MockL3Notary{}

	// MockL3Notary always approves
	approved, err := notary.VerifyL3Proof(context.Background(), "user-123", "tx-hash-456", "session-789", nil)
	require.NoError(t, err)
	require.True(t, approved, "MockL3Notary should always approve")
}

func TestConfigurableMockL3Notary(t *testing.T) {
	t.Parallel()
	// Test pass behavior

	passNotary := NewConfigurableMockL3Notary(true)
	approved, err := passNotary.VerifyL3Proof(context.Background(), "user-123", "tx-hash-456", "session-789", nil)
	require.NoError(t, err)
	require.True(t, approved, "Configurable notary with ShouldPass=true should approve")

	// Test fail behavior
	failNotary := NewConfigurableMockL3Notary(false)
	approved, err = failNotary.VerifyL3Proof(context.Background(), "user-123", "tx-hash-456", "session-789", nil)
	require.NoError(t, err)
	require.False(t, approved, "Configurable notary with ShouldPass=false should reject")
}

func TestSlowMockL3Notary(t *testing.T) {
	t.Parallel()
	delay := 50 * time.Millisecond
	notary := NewSlowMockL3Notary(delay)

	start := time.Now()
	approved, err := notary.VerifyL3Proof(context.Background(), "user-123", "tx-hash-456", "session-789", nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.True(t, approved, "SlowMockL3Notary should approve after delay")
	require.GreaterOrEqual(t, elapsed, delay, "SlowMockL3Notary should have waited at least the specified delay")
}

func TestMockTransactionAudit(t *testing.T) {
	t.Parallel()
	audit := &MockTransactionAudit{}

	// MockTransactionAudit is a no-op
	err := audit.DocSet(context.Background(), "test-collection", "test-id", json.RawMessage(`{"test": "data"}`))
	require.NoError(t, err, "MockTransactionAudit should not return errors")
}
