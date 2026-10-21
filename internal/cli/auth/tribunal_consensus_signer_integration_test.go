// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// sealTribunalTestKey writes appName's sealed signing key the way app
// enrollment does: the seed is sealed to the app's signing-key path under the
// runtime tree (app_enrollment.go, sealIdentityFile on RelFromAbs).
func sealTribunalTestKey(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config, appName string) ed25519.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rel, err := fileSvc.RelFromAbs(cfg.AppSigningKeyFile(appName))
	require.NoError(t, err)
	require.NoError(t, sealIdentityFile(t.Context(), fileSvc, cfg, filepath.ToSlash(rel), priv.Seed()))
	return pub
}

func TestTribunalConsensusSigner_SignsWithSealedMemberKey(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	pub := sealTribunalTestKey(t, fileSvc, cfg, "tribunal-axiom")
	signer := NewTribunalConsensusSigner(t.Context(), fileSvc, cfg)

	require.True(t, signer.HasKey("tribunal-axiom"))
	payload := []byte("deliberation-payload")
	sig, err := signer.Sign("tribunal-axiom", payload)
	require.NoError(t, err)
	require.True(t, ed25519.Verify(pub, payload, sig))
}

func TestTribunalConsensusSigner_MissingMemberKeyIsUnavailable(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	sealTribunalTestKey(t, fileSvc, cfg, "tribunal-axiom")
	signer := NewTribunalConsensusSigner(t.Context(), fileSvc, cfg)

	require.False(t, signer.HasKey("tribunal-concord"))
	_, err := signer.Sign("tribunal-concord", []byte("payload"))
	require.ErrorIs(t, err, constants.ErrConsensusMemberKeyUnavailable)
}

func TestTribunalConsensusSigner_NonMemberIDIsUnavailable(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	// The non-member has a sealed key on disk, so only the membership gate can refuse it.
	sealTribunalTestKey(t, fileSvc, cfg, "other-app")
	signer := NewTribunalConsensusSigner(t.Context(), fileSvc, cfg)

	require.False(t, signer.HasKey("other-app"))
	_, err := signer.Sign("other-app", []byte("payload"))
	require.ErrorIs(t, err, constants.ErrConsensusMemberKeyUnavailable)
}

func TestTribunalConsensusSigner_KeyEnrolledAfterFailureIsAvailableWithoutRestart(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	sealTribunalTestKey(t, fileSvc, cfg, "tribunal-axiom")
	signer := NewTribunalConsensusSigner(t.Context(), fileSvc, cfg)

	_, err := signer.Sign("tribunal-nemesis", []byte("payload"))
	require.ErrorIs(t, err, constants.ErrConsensusMemberKeyUnavailable)

	pub := sealTribunalTestKey(t, fileSvc, cfg, "tribunal-nemesis")
	require.True(t, signer.HasKey("tribunal-nemesis"))
	payload := []byte("late-enrollment-payload")
	sig, err := signer.Sign("tribunal-nemesis", payload)
	require.NoError(t, err)
	require.True(t, ed25519.Verify(pub, payload, sig))
}
