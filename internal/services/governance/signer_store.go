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
	"fmt"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// SignerStore defines the interface for loading trusted L2 signers.
type SignerStore interface {
	GetTrustedSigner(ctx context.Context, keyID string) (ed25519.PublicKey, error)
}

// FailClosedSignerStore implements SignerStore using a static map.
// Used as a production fallback (empty map = fail-closed) and in tests.
type FailClosedSignerStore struct {
	Signers map[string]ed25519.PublicKey
}

func (s *FailClosedSignerStore) GetTrustedSigner(_ context.Context, keyID string) (ed25519.PublicKey, error) {
	if s.Signers == nil {
		return nil, nil
	}
	pubKey, ok := s.Signers[keyID]
	if !ok {
		return nil, nil
	}
	return pubKey, nil
}

// L2TrustStore is an outbound Operator's L2 trust: the Gateway's consensus
// policy and its members' public keys, delivered in the enrollment bundle,
// replaced by each certificate renewal reply, and held in memory for the life
// of the process. It is the Operator's SignerStore and L2ConsensusPolicyStore.
// The zero value trusts nothing, so an envelope whose posture requires L2
// fails closed.
type L2TrustStore struct {
	mu    sync.RWMutex
	trust l2Trust
}

// l2Trust is one decoded, immutable L2 trust; Replace swaps it whole.
type l2Trust struct {
	policy  *L2ConsensusPolicy
	setID   string
	signers map[string]ed25519.PublicKey
}

// NewL2TrustStore decodes the bundle's L2 trust. A nil trust yields the empty
// store. A malformed trust is rejected rather than partially loaded: the
// Gateway built it, so a bad key or policy means the bundle is not trustworthy.
func NewL2TrustStore(trust *models.OperatorL2Trust) (*L2TrustStore, error) {
	decoded, err := decodeL2Trust(trust)
	if err != nil {
		return nil, err
	}
	return &L2TrustStore{trust: decoded}, nil
}

// Replace swaps in the Gateway's current L2 trust. A nil trust empties the
// store. A malformed trust is rejected and the held trust is kept.
func (s *L2TrustStore) Replace(trust *models.OperatorL2Trust) error {
	decoded, err := decodeL2Trust(trust)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.trust = decoded
	s.mu.Unlock()
	return nil
}

func decodeL2Trust(trust *models.OperatorL2Trust) (l2Trust, error) {
	if trust == nil {
		return l2Trust{}, nil
	}
	if trust.ConsensusSetID == "" || trust.Quorum < 1 || trust.Quorum > len(trust.MemberKeyIDs) {
		return l2Trust{}, fmt.Errorf("l2 trust: consensus set %q quorum %d of %d members: %w",
			trust.ConsensusSetID, trust.Quorum, len(trust.MemberKeyIDs), constants.ErrValidationFailed)
	}
	members := make(map[string]bool, len(trust.MemberKeyIDs))
	for _, id := range trust.MemberKeyIDs {
		if id == "" || members[id] {
			return l2Trust{}, fmt.Errorf("l2 trust: member %q: %w", id, constants.ErrValidationFailed)
		}
		members[id] = true
	}
	signers := make(map[string]ed25519.PublicKey, len(trust.Signers))
	for _, signer := range trust.Signers {
		if !members[signer.KeyID] {
			return l2Trust{}, fmt.Errorf("l2 trust: signer %q is not a member: %w", signer.KeyID, constants.ErrValidationFailed)
		}
		pub, err := hex.DecodeString(signer.PublicKeyHex)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return l2Trust{}, fmt.Errorf("l2 trust: signer %q public key: %w", signer.KeyID, constants.ErrValidationFailed)
		}
		signers[signer.KeyID] = ed25519.PublicKey(pub)
	}
	provider, err := constants.ParseConsensusProvider(trust.Provider)
	if err != nil {
		return l2Trust{}, fmt.Errorf("l2 trust: %w: %w", constants.ErrValidationFailed, err)
	}
	return l2Trust{
		policy: &L2ConsensusPolicy{
			MemberKeyIDs:    append([]string(nil), trust.MemberKeyIDs...),
			Quorum:          trust.Quorum,
			RequireDistinct: trust.RequireDistinct,
			Enabled:         trust.Enabled,
			Provider:        provider,
		},
		setID:   trust.ConsensusSetID,
		signers: signers,
	}, nil
}

// GetTrustedSigner returns the member's public key, or nil when the held
// trust carries none for keyID.
func (s *L2TrustStore) GetTrustedSigner(_ context.Context, keyID string) (ed25519.PublicKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.trust.signers[keyID], nil
}

// GetConsensusPolicy returns the held policy when id names it, and nil for
// any other consensus set.
func (s *L2TrustStore) GetConsensusPolicy(_ context.Context, id string) (*L2ConsensusPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.trust.policy == nil || id != s.trust.setID {
		return nil, nil
	}
	policy := *s.trust.policy
	return &policy, nil
}
