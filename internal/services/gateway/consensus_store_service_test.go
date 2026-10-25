// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

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
	"github.com/zerotrustdata/ztd/internal/services/governance"
)

func TestConsensusStoreService_AddConsensus(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	consensusSvc := infra.ConsensusStore

	// Generate test signers
	pubKey1, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	pubKey2, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	pubKey3, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	signer1 := models.TrustedSigner{
		ID:        "member-1",
		PublicKey: hex.EncodeToString(pubKey1),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}
	signer2 := models.TrustedSigner{
		ID:        "member-2",
		PublicKey: hex.EncodeToString(pubKey2),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}
	disabledSigner := models.TrustedSigner{
		ID:        "disabled-member",
		PublicKey: hex.EncodeToString(pubKey3),
		AddedAt:   time.Now().UTC(),
		Enabled:   false,
	}

	// Register signers
	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer1)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "member-1") })

	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer2)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "member-2") })

	err = infra.SignerStore.AddTrustedSigner(t.Context(), disabledSigner)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "disabled-member") })

	tests := []struct {
		name        string
		policy      models.ConsensusPolicy
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid consensus",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus",
				MemberAppIDs:    []string{"member-1", "member-2"},
				Quorum:          2,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: false,
		},
		{
			name: "empty ID",
			policy: models.ConsensusPolicy{
				ID:              "",
				MemberAppIDs:    []string{"member-1"},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "consensus ID",
		},
		{
			name: "empty member IDs",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus-2",
				MemberAppIDs:    []string{},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "member_app_ids",
		},
		{
			name: "quorum less than 1",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus-3",
				MemberAppIDs:    []string{"member-1"},
				Quorum:          0,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "quorum must be >= 1",
		},
		{
			name: "quorum exceeds member count",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus-4",
				MemberAppIDs:    []string{"member-1"},
				Quorum:          2,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "quorum cannot exceed member count",
		},
		{
			name: "duplicate member IDs",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus-5",
				MemberAppIDs:    []string{"member-1", "member-1"},
				Quorum:          2,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "duplicate member_app_id",
		},
		{
			name: "unknown member ID",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus-6",
				MemberAppIDs:    []string{"unknown-member"},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "not an enabled trusted signer",
		},
		{
			name: "empty string in member IDs",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus-7",
				MemberAppIDs:    []string{"member-1", ""},
				Quorum:          2,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "cannot contain empty strings",
		},
		{
			name: "invalid consensus ID with special characters",
			policy: models.ConsensusPolicy{
				ID:              "test@consensus",
				MemberAppIDs:    []string{"member-1"},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "CONSENSUS_INVALID_ID",
		},
		{
			name: "invalid consensus ID with spaces",
			policy: models.ConsensusPolicy{
				ID:              "test consensus",
				MemberAppIDs:    []string{"member-1"},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "CONSENSUS_INVALID_ID",
		},
		{
			name: "invalid consensus ID with path separator",
			policy: models.ConsensusPolicy{
				ID:              "test/consensus",
				MemberAppIDs:    []string{"member-1"},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "CONSENSUS_INVALID_ID",
		},
		{
			name: "disabled new consensus rejected",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus-disabled",
				MemberAppIDs:    []string{"member-1"},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         false,
			},
			expectError: true,
			errorMsg:    "CONSENSUS_MUST_BE_ENABLED",
		},
		{
			name: "disabled signer as member rejected",
			policy: models.ConsensusPolicy{
				ID:              "test-consensus-disabled-signer",
				MemberAppIDs:    []string{"disabled-member"},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         true,
			},
			expectError: true,
			errorMsg:    "not an enabled trusted signer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := consensusSvc.AddConsensus(t.Context(), tt.policy)
			if tt.expectError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
				// Cleanup
				t.Cleanup(func() { consensusSvc.DeleteConsensus(context.Background(), tt.policy.ID) })
			}
		})
	}
}

func TestConsensusStoreService_GetConsensus(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	consensusSvc := infra.ConsensusStore

	// Create a test signer
	pubKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer := models.TrustedSigner{
		ID:        "test-member",
		PublicKey: hex.EncodeToString(pubKey),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}
	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "test-member") })

	// Create a test consensus
	policy := models.ConsensusPolicy{
		ID:              "get-test-consensus",
		MemberAppIDs:    []string{"test-member"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
	}
	err = consensusSvc.AddConsensus(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() { consensusSvc.DeleteConsensus(context.Background(), "get-test-consensus") })

	// Get the consensus
	retrieved, err := consensusSvc.GetConsensus(t.Context(), "get-test-consensus")
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, "get-test-consensus", retrieved.ID)
	assert.Equal(t, []string{"test-member"}, retrieved.MemberAppIDs)
	assert.Equal(t, 1, retrieved.Quorum)
	assert.True(t, retrieved.RequireDistinct)
	assert.True(t, retrieved.Enabled)

	// Get non-existent consensus
	retrieved, err = consensusSvc.GetConsensus(t.Context(), "non-existent")
	require.NoError(t, err)
	assert.Nil(t, retrieved)
}

func TestConsensusStoreService_ListConsensus(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	consensusSvc := infra.ConsensusStore

	// Create test signers
	pubKey1, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	pubKey2, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	signer1 := models.TrustedSigner{
		ID:        "list-member-1",
		PublicKey: hex.EncodeToString(pubKey1),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}
	signer2 := models.TrustedSigner{
		ID:        "list-member-2",
		PublicKey: hex.EncodeToString(pubKey2),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}

	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer1)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "list-member-1") })

	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer2)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "list-member-2") })

	// Create test consensus
	policy1 := models.ConsensusPolicy{
		ID:              "list-consensus-1",
		MemberAppIDs:    []string{"list-member-1"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
	}
	err = consensusSvc.AddConsensus(t.Context(), policy1)
	require.NoError(t, err)
	t.Cleanup(func() { consensusSvc.DeleteConsensus(context.Background(), "list-consensus-1") })

	policy2 := models.ConsensusPolicy{
		ID:              "list-consensus-2",
		MemberAppIDs:    []string{"list-member-2"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
	}
	err = consensusSvc.AddConsensus(t.Context(), policy2)
	require.NoError(t, err)
	t.Cleanup(func() { consensusSvc.DeleteConsensus(context.Background(), "list-consensus-2") })

	// List consensus
	consensus, err := consensusSvc.ListConsensus(t.Context())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(consensus), 2)

	// Verify our consensus are in the list
	ids := make(map[string]bool)
	for _, t := range consensus {
		ids[t.ID] = true
	}
	assert.True(t, ids["list-consensus-1"])
	assert.True(t, ids["list-consensus-2"])
}

func TestConsensusStoreService_DeleteConsensus(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	consensusSvc := infra.ConsensusStore

	// Create a test signer
	pubKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer := models.TrustedSigner{
		ID:        "delete-member",
		PublicKey: hex.EncodeToString(pubKey),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}
	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "delete-member") })

	// Create a test consensus
	policy := models.ConsensusPolicy{
		ID:              "delete-test-consensus",
		MemberAppIDs:    []string{"delete-member"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
	}
	err = consensusSvc.AddConsensus(t.Context(), policy)
	require.NoError(t, err)

	// Delete the consensus
	deleted, err := consensusSvc.DeleteConsensus(t.Context(), "delete-test-consensus")
	require.NoError(t, err)
	assert.True(t, deleted)

	// Verify it's gone
	retrieved, err := consensusSvc.GetConsensus(t.Context(), "delete-test-consensus")
	require.NoError(t, err)
	assert.Nil(t, retrieved)

	// Delete non-existent consensus
	deleted, err = consensusSvc.DeleteConsensus(t.Context(), "non-existent")
	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestConsensusStoreService_UpdateDisableConsensus(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	consensusSvc := infra.ConsensusStore

	pubKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer := models.TrustedSigner{
		ID:        "update-member",
		PublicKey: hex.EncodeToString(pubKey),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}
	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "update-member") })

	// Create enabled consensus
	policy := models.ConsensusPolicy{
		ID:              "update-consensus",
		MemberAppIDs:    []string{"update-member"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
	}
	err = consensusSvc.AddConsensus(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() { consensusSvc.DeleteConsensus(context.Background(), "update-consensus") })

	// Update: disable the existing consensus (should succeed)
	policy.Enabled = false
	err = consensusSvc.AddConsensus(t.Context(), policy)
	require.NoError(t, err)

	// Verify it's stored as disabled
	retrieved, err := consensusSvc.GetConsensus(t.Context(), "update-consensus")
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.False(t, retrieved.Enabled)
}

func TestConsensusStoreService_AddConsensus_AlreadyExists(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	consensusSvc := infra.ConsensusStore

	pubKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer := models.TrustedSigner{
		ID:        "exists-member",
		PublicKey: hex.EncodeToString(pubKey),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}
	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "exists-member") })

	policy := models.ConsensusPolicy{
		ID:              "exists-consensus",
		MemberAppIDs:    []string{"exists-member"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
	}
	err = consensusSvc.AddConsensus(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() { consensusSvc.DeleteConsensus(context.Background(), "exists-consensus") })

	// Attempt to create the same consensus again with Enabled=true
	err = consensusSvc.AddConsensus(t.Context(), policy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestConsensusStoreService_GetConsensusPolicy(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	consensusSvc := infra.ConsensusStore

	pubKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer := models.TrustedSigner{
		ID:        "consensus-member",
		PublicKey: hex.EncodeToString(pubKey),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}
	err = infra.SignerStore.AddTrustedSigner(t.Context(), signer)
	require.NoError(t, err)
	t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), "consensus-member") })

	policy := models.ConsensusPolicy{
		ID:              "consensus-test-consensus",
		MemberAppIDs:    []string{"consensus-member"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
	}
	err = consensusSvc.AddConsensus(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() { consensusSvc.DeleteConsensus(context.Background(), "consensus-test-consensus") })

	consensusPolicy, err := consensusSvc.GetConsensusPolicy(t.Context(), "consensus-test-consensus")
	require.NoError(t, err)
	require.NotNil(t, consensusPolicy)
	assert.Equal(t, []string{"consensus-member"}, consensusPolicy.MemberKeyIDs)
	assert.Equal(t, 1, consensusPolicy.Quorum)
	assert.True(t, consensusPolicy.RequireDistinct)
	assert.True(t, consensusPolicy.Enabled)

	consensusPolicy, err = consensusSvc.GetConsensusPolicy(t.Context(), "non-existent")
	require.NoError(t, err)
	assert.Nil(t, consensusPolicy)
}

func TestConsensusStoreService_OperatorL2Trust(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	consensusSvc := infra.ConsensusStore

	enabledPub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	revokedPub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	for id, pub := range map[string]ed25519.PublicKey{"trust-enabled": enabledPub, "trust-revoked": revokedPub} {
		require.NoError(t, infra.SignerStore.AddTrustedSigner(t.Context(), models.TrustedSigner{
			ID: id, PublicKey: hex.EncodeToString(pub), AddedAt: time.Now().UTC(), Enabled: true,
		}))
		t.Cleanup(func() { infra.SignerStore.DeleteTrustedSigner(context.Background(), id) })
	}
	require.NoError(t, consensusSvc.AddConsensus(t.Context(), models.ConsensusPolicy{
		ID: "operator-trust", MemberAppIDs: []string{"trust-enabled", "trust-revoked"}, Quorum: 1, RequireDistinct: true, Enabled: true,
	}))
	t.Cleanup(func() { consensusSvc.DeleteConsensus(context.Background(), "operator-trust") })
	require.NoError(t, infra.SignerStore.AddTrustedSigner(t.Context(), models.TrustedSigner{
		ID: "trust-revoked", PublicKey: hex.EncodeToString(revokedPub), AddedAt: time.Now().UTC(), Enabled: false,
	}))

	trust, err := consensusSvc.OperatorL2Trust(t.Context(), "operator-trust")
	require.NoError(t, err)
	assert.Equal(t, &models.OperatorL2Trust{
		ConsensusSetID:  "operator-trust",
		MemberKeyIDs:    []string{"trust-enabled", "trust-revoked"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
		Signers:         []models.OperatorSigner{{KeyID: "trust-enabled", PublicKeyHex: hex.EncodeToString(enabledPub)}},
	}, trust, "a disabled member's key is not delivered")

	store, err := governance.NewL2TrustStore(trust)
	require.NoError(t, err, "the delivered trust must load on the Operator")
	got, err := store.GetTrustedSigner(t.Context(), "trust-enabled")
	require.NoError(t, err)
	assert.Equal(t, enabledPub, got)

	for _, id := range []string{"", "non-existent"} {
		trust, err := consensusSvc.OperatorL2Trust(t.Context(), id)
		require.NoError(t, err)
		assert.Nil(t, trust, "id %q", id)
	}
}

// readinessSigner is a ConsensusMemberSigner that holds sealed keys for the listed members only.
type readinessSigner struct{ keys map[string]bool }

func (s readinessSigner) Sign(string, []byte) ([]byte, error) {
	return nil, constants.ErrConsensusMemberKeyUnavailable
}

func (s readinessSigner) HasKey(memberAppID string) bool { return s.keys[memberAppID] }

func addReadinessSigner(t *testing.T, infra *TestInfrastructure, id string, enabled bool) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, infra.SignerStore.AddTrustedSigner(t.Context(), models.TrustedSigner{
		ID:        id,
		PublicKey: hex.EncodeToString(pub),
		AddedAt:   time.Now().UTC(),
		Enabled:   enabled,
	}))
}

func bindReadinessConsensus(t *testing.T, infra *TestInfrastructure, policy models.ConsensusPolicy) {
	t.Helper()
	policy.ID = string(constants.DocIDConsensusBinding)
	if policy.Provider != string(constants.ConsensusProviderTribunal) {
		policy.EndpointURL = "https://consensus.example.test/deliberate"
	}
	policy.RequireDistinct = true
	policy.Enabled = true
	require.NoError(t, infra.ConsensusStore.AddConsensus(t.Context(), policy))
}

func TestConsensusStoreService_ConsensusReadiness(t *testing.T) {
	t.Run("unbound reports not bound", func(t *testing.T) {
		infra := setupTestInfrastructure(t, false)
		got, err := infra.ConsensusStore.ConsensusReadiness(t.Context(), nil)
		require.NoError(t, err)
		assert.Equal(t, models.ConsensusReadiness{}, got)
	})

	t.Run("application provider counts enabled signers", func(t *testing.T) {
		infra := setupTestInfrastructure(t, false)
		members := []string{"app-1", "app-2", "app-3"}
		for _, id := range members {
			addReadinessSigner(t, infra, id, true)
		}
		bindReadinessConsensus(t, infra, models.ConsensusPolicy{
			MemberAppIDs: members,
			Quorum:       2,
			Provider:     string(constants.ConsensusProviderApplication),
		})

		// The application provider never reads local keys, even when the signer holds them.
		signer := readinessSigner{keys: map[string]bool{"app-1": true, "app-2": true, "app-3": true}}
		got, err := infra.ConsensusStore.ConsensusReadiness(t.Context(), signer)
		require.NoError(t, err)
		assert.True(t, got.Bound)
		assert.Equal(t, constants.ConsensusProviderApplication, got.Provider)
		assert.Equal(t, 2, got.Quorum)
		assert.Equal(t, 3, got.Usable)
		assert.True(t, got.Ready)
		require.Len(t, got.Members, 3)
		for i, member := range got.Members {
			assert.Equal(t, members[i], member.AppID)
			assert.True(t, member.SignerEnabled)
			assert.False(t, member.LocalKey)
		}
	})

	t.Run("disabled signer is not usable", func(t *testing.T) {
		infra := setupTestInfrastructure(t, false)
		members := []string{"app-1", "app-2", "app-3"}
		for _, id := range members {
			addReadinessSigner(t, infra, id, true)
		}
		bindReadinessConsensus(t, infra, models.ConsensusPolicy{
			MemberAppIDs: members,
			Quorum:       3,
			Provider:     string(constants.ConsensusProviderApplication),
		})
		// AddConsensus only binds enabled members, so disable one after binding.
		addReadinessSigner(t, infra, "app-3", false)

		got, err := infra.ConsensusStore.ConsensusReadiness(t.Context(), nil)
		require.NoError(t, err)
		assert.Equal(t, 2, got.Usable)
		assert.False(t, got.Ready)
		require.Len(t, got.Members, 3)
		assert.False(t, got.Members[2].SignerEnabled)
	})

	t.Run("tribunal provider counts only members with local keys", func(t *testing.T) {
		infra := setupTestInfrastructure(t, false)
		members := constants.TribunalConsensusMemberAppIDs()
		for _, id := range members {
			addReadinessSigner(t, infra, id, true)
		}
		bindReadinessConsensus(t, infra, models.ConsensusPolicy{
			MemberAppIDs: members,
			Quorum:       constants.TribunalConsensusDefaultQuorum,
			Provider:     string(constants.ConsensusProviderTribunal),
			ModelTag:     "tribunal-test-model",
			ModelDigest:  "sha256:test",
		})

		signer := readinessSigner{keys: map[string]bool{members[0]: true, members[1]: true}}
		got, err := infra.ConsensusStore.ConsensusReadiness(t.Context(), signer)
		require.NoError(t, err)
		assert.Equal(t, constants.ConsensusProviderTribunal, got.Provider)
		assert.Equal(t, "tribunal-test-model", got.ModelTag)
		assert.Equal(t, "sha256:test", got.ModelDigest)
		assert.Equal(t, 2, got.Usable)
		assert.False(t, got.Ready)
		require.Len(t, got.Members, 5)
		assert.True(t, got.Members[0].LocalKey)
		assert.True(t, got.Members[1].LocalKey)
		assert.False(t, got.Members[2].LocalKey)
		assert.True(t, got.Members[2].SignerEnabled)
	})
}

func TestIsValidConsensusID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"valid-id", true},
		{"valid_id", true},
		{"ValidID123", true},
		{"a", true},
		{"", false},
		{"test@consensus", false},
		{"test consensus", false},
		{"test/consensus", false},
		{"test\\consensus", false},
		{"test:consensus", false},
		{"test.consensus", false},
		{"test\tconsensus", false},
		{"日本語", true}, // unicode letters are allowed
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := isValidConsensusID(tt.id)
			assert.Equal(t, tt.want, got)
		})
	}
}
