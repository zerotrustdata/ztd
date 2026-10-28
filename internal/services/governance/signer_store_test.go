// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestFailClosedSignerStore_NilMap(t *testing.T) {
	t.Parallel()
	s := &FailClosedSignerStore{}
	pubKey, err := s.GetTrustedSigner(context.Background(), "key1")
	require.NoError(t, err)
	assert.Nil(t, pubKey)
}

func TestFailClosedSignerStore_NotFound(t *testing.T) {
	t.Parallel()
	s := &FailClosedSignerStore{Signers: map[string]ed25519.PublicKey{}}
	pubKey, err := s.GetTrustedSigner(context.Background(), "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, pubKey)
}

func TestFailClosedSignerStore_Found(t *testing.T) {
	t.Parallel()
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	s := &FailClosedSignerStore{Signers: map[string]ed25519.PublicKey{"key1": pub}}
	result, err := s.GetTrustedSigner(context.Background(), "key1")
	require.NoError(t, err)
	assert.Equal(t, pub, result)
}

func testL2Trust(t *testing.T) (*models.OperatorL2Trust, ed25519.PublicKey) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return &models.OperatorL2Trust{
		ConsensusSetID:  "set-1",
		MemberKeyIDs:    []string{"m1", "m2"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
		Signers:         []models.OperatorSigner{{KeyID: "m1", PublicKeyHex: hex.EncodeToString(pub)}},
	}, pub
}

func TestL2TrustStore_NilTrustTrustsNothing(t *testing.T) {
	t.Parallel()
	store, err := NewL2TrustStore(nil)
	require.NoError(t, err)

	pubKey, err := store.GetTrustedSigner(context.Background(), "m1")
	require.NoError(t, err)
	assert.Nil(t, pubKey)
	policy, err := store.GetConsensusPolicy(context.Background(), "set-1")
	require.NoError(t, err)
	assert.Nil(t, policy)
}

func TestL2TrustStore_ServesBundlePolicyAndSigners(t *testing.T) {
	t.Parallel()
	trust, pub := testL2Trust(t)
	store, err := NewL2TrustStore(trust)
	require.NoError(t, err)

	got, err := store.GetTrustedSigner(context.Background(), "m1")
	require.NoError(t, err)
	assert.Equal(t, pub, got)
	missing, err := store.GetTrustedSigner(context.Background(), "m2")
	require.NoError(t, err)
	assert.Nil(t, missing, "a member without a key in the bundle is not trusted")

	policy, err := store.GetConsensusPolicy(context.Background(), "set-1")
	require.NoError(t, err)
	require.NotNil(t, policy)
	assert.Equal(t, L2ConsensusPolicy{MemberKeyIDs: []string{"m1", "m2"}, Quorum: 1, RequireDistinct: true, Enabled: true}, *policy)
	other, err := store.GetConsensusPolicy(context.Background(), "set-2")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestL2TrustStore_RejectsMalformedTrust(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*models.OperatorL2Trust){
		"empty set id":      func(tr *models.OperatorL2Trust) { tr.ConsensusSetID = "" },
		"zero quorum":       func(tr *models.OperatorL2Trust) { tr.Quorum = 0 },
		"quorum over size":  func(tr *models.OperatorL2Trust) { tr.Quorum = 3 },
		"duplicate member":  func(tr *models.OperatorL2Trust) { tr.MemberKeyIDs = []string{"m1", "m1"} },
		"non-member signer": func(tr *models.OperatorL2Trust) { tr.Signers[0].KeyID = "outsider" },
		"invalid hex":       func(tr *models.OperatorL2Trust) { tr.Signers[0].PublicKeyHex = "not-hex!" },
		"wrong key size":    func(tr *models.OperatorL2Trust) { tr.Signers[0].PublicKeyHex = hex.EncodeToString([]byte("short")) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			trust, _ := testL2Trust(t)
			mutate(trust)
			_, err := NewL2TrustStore(trust)
			require.ErrorIs(t, err, constants.ErrValidationFailed)
		})
	}
}

func TestL2TrustStore_ReplaceSwapsHeldTrust(t *testing.T) {
	t.Parallel()
	trust, _ := testL2Trust(t)
	store, err := NewL2TrustStore(trust)
	require.NoError(t, err)

	next, nextPub := testL2Trust(t)
	next.ConsensusSetID = "set-2"
	require.NoError(t, store.Replace(next))

	old, err := store.GetConsensusPolicy(context.Background(), "set-1")
	require.NoError(t, err)
	assert.Nil(t, old, "the replaced consensus set is no longer served")
	policy, err := store.GetConsensusPolicy(context.Background(), "set-2")
	require.NoError(t, err)
	require.NotNil(t, policy)
	got, err := store.GetTrustedSigner(context.Background(), "m1")
	require.NoError(t, err)
	assert.Equal(t, nextPub, got)

	require.NoError(t, store.Replace(nil))
	emptied, err := store.GetTrustedSigner(context.Background(), "m1")
	require.NoError(t, err)
	assert.Nil(t, emptied, "a nil trust empties the store")
}

func TestL2TrustStore_ReplaceRejectsMalformedAndKeepsHeldTrust(t *testing.T) {
	t.Parallel()
	trust, pub := testL2Trust(t)
	store, err := NewL2TrustStore(trust)
	require.NoError(t, err)

	bad, _ := testL2Trust(t)
	bad.Quorum = 0
	require.ErrorIs(t, store.Replace(bad), constants.ErrValidationFailed)

	got, err := store.GetTrustedSigner(context.Background(), "m1")
	require.NoError(t, err)
	assert.Equal(t, pub, got)
}

func TestL2TrustStore_ReplaceIfNewerIgnoresOutOfOrderSnapshots(t *testing.T) {
	t.Parallel()
	current, _ := testL2Trust(t)
	current.BindingVersion = 2
	store, err := NewL2TrustStore(current)
	require.NoError(t, err)

	stale, _ := testL2Trust(t)
	stale.BindingVersion = 1
	updated, err := store.ReplaceIfNewer(stale)
	require.NoError(t, err)
	assert.False(t, updated)
	assert.EqualValues(t, 2, store.BindingVersion())

	next, _ := testL2Trust(t)
	next.BindingVersion = 3
	updated, err = store.ReplaceIfNewer(next)
	require.NoError(t, err)
	assert.True(t, updated)
	assert.EqualValues(t, 3, store.BindingVersion())
}

func TestL4Warden_Doctrine(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	doctrine := NewL1Doctrine()
	warden := NewL4Warden(
		logger,
		nil,
		&governancetest.SimpleStateRootProvider{Root: "root"},
		&FailClosedSignerStore{},
		&consensusStoreTestAdapter{Inner: &governancetest.SimpleConsensusStore{}},
		nil,
		doctrine,
		[]constants.ActionType{constants.ActionTypeFileEdit},
		nil,
		&testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "operator-session-1"},
		&governancetest.UnlockedVault{},
	)

	assert.Same(t, doctrine, warden.Doctrine())
}

func TestL5Actuator_WaitJoinsInFlightExecution(t *testing.T) {
	t.Parallel()
	actuator := &L5Actuator{}
	actuator.wg.Add(1)

	finished := make(chan struct{})
	go func() {
		actuator.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("Wait returned while an execution was still in flight")
	case <-time.After(30 * time.Millisecond):
	}

	actuator.wg.Done()

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after the in-flight execution finished")
	}
}
