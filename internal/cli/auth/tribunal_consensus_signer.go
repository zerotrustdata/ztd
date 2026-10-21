// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"
	"sync"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
)

var _ gateway.ConsensusMemberSigner = (*TribunalConsensusSigner)(nil)

// TribunalConsensusSigner signs Tribunal consensus votes with the sealed app
// signing keys of the Gateway-hosted panel members. A member's key unseals on
// first use through LoadAppSigningKey and stays cached. A failed load is not
// cached, so a key enrolled while the Gateway runs becomes signable without a
// restart.
type TribunalConsensusSigner struct {
	ctx     context.Context
	fileSvc fs.RuntimeFileService
	cfg     *config.Config

	mu   sync.Mutex
	keys map[string]ed25519.PrivateKey
}

// NewTribunalConsensusSigner returns a signer that unseals member keys from the
// runtime tree under fileSvc with cfg's keyring. ctx scopes the lazy loads.
func NewTribunalConsensusSigner(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config) *TribunalConsensusSigner {
	return &TribunalConsensusSigner{
		ctx:     ctx,
		fileSvc: fileSvc,
		cfg:     cfg,
		keys:    make(map[string]ed25519.PrivateKey),
	}
}

// Sign signs payload with memberAppID's sealed signing key.
func (s *TribunalConsensusSigner) Sign(memberAppID string, payload []byte) ([]byte, error) {
	key, err := s.key(memberAppID)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(key, payload), nil
}

// HasKey reports whether memberAppID's sealed key is present and unseals here.
func (s *TribunalConsensusSigner) HasKey(memberAppID string) bool {
	_, err := s.key(memberAppID)
	return err == nil
}

// key returns memberAppID's signing key, unsealing and caching it on first use.
// Only Tribunal panel member IDs are signable; any other ID is refused before
// the runtime tree is touched.
func (s *TribunalConsensusSigner) key(memberAppID string) (ed25519.PrivateKey, error) {
	if !slices.Contains(constants.TribunalConsensusMemberAppIDs(), memberAppID) {
		return nil, fmt.Errorf("tribunal consensus member %q is not a panel member: %w", memberAppID, constants.ErrConsensusMemberKeyUnavailable)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if key, ok := s.keys[memberAppID]; ok {
		return key, nil
	}
	key, err := LoadAppSigningKey(s.ctx, s.fileSvc, s.cfg, memberAppID)
	if err != nil {
		return nil, fmt.Errorf("tribunal consensus member %q: %w: %w", memberAppID, constants.ErrConsensusMemberKeyUnavailable, err)
	}
	s.keys[memberAppID] = key
	return key, nil
}
