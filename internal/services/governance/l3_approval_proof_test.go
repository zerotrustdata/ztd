// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"testing"

	"log/slog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// TestL3Notary_CLIAndPasskey verifies both authorization layers.
func TestL3Notary_CLIAndPasskey(t *testing.T) {
	logger := slog.Default()

	ctx := context.Background()
	userID := "pipeline-user-001"
	cliSessionID := "pipeline-cli-session-001"
	txHash := "pipeline-tx-hash-abc123"
	certFingerprint := "pipeline-cert-fingerprint-xyz"

	// Step 4: L3 notary verifies the proof using the layered model
	// Passkey verifier (mock) validates the WebAuthn assertion — Layer 1
	// CLI session verifier (mock) validates mTLS transport — Layer 2
	passkeyVerifier := &mockL3Notary{result: true}
	cliVerifier := &mockCLISessionVerifier{result: nil}
	notary := NewGatewayL3Notary(cliVerifier, passkeyVerifier, logger)

	l3Proof := &commonv1.L3Proof{
		CredentialId:        "base64-credential-id",
		ClientDataJson:      "base64-client-data",
		AuthenticatorData:   "base64-authenticator-data",
		Signature:           "base64-passkey-signature",
		MtlsCertFingerprint: certFingerprint,
	}

	allowed, err := notary.VerifyL3Proof(ctx, userID, txHash, cliSessionID, l3Proof)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, passkeyVerifier.called)
	assert.True(t, cliVerifier.called)
	assert.Equal(t, userID, cliVerifier.calledUserID)
	assert.Equal(t, cliSessionID, cliVerifier.calledSessionID)
	assert.Equal(t, certFingerprint, cliVerifier.calledFingerprint)
}

// TestL3Notary_PasskeyDenialSkipsCLI verifies that the pipeline
// rejects the L3 proof when the passkey verifier denies the WebAuthn assertion.
func TestL3Notary_PasskeyDenialSkipsCLI(t *testing.T) {
	logger := slog.Default()

	ctx := context.Background()
	userID := "pipeline-user-002"
	cliSessionID := "pipeline-cli-session-002"
	txHash := "pipeline-tx-hash-denied"

	// Passkey verifier denies the assertion
	passkeyVerifier := &mockL3Notary{result: false}
	cliVerifier := &mockCLISessionVerifier{result: nil}
	notary := NewGatewayL3Notary(cliVerifier, passkeyVerifier, logger)

	l3Proof := &commonv1.L3Proof{
		CredentialId:        "base64-credential-id",
		ClientDataJson:      "base64-client-data",
		AuthenticatorData:   "base64-authenticator-data",
		Signature:           "base64-passkey-signature",
		MtlsCertFingerprint: "fingerprint-xyz",
	}

	allowed, err := notary.VerifyL3Proof(ctx, userID, txHash, cliSessionID, l3Proof)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.True(t, passkeyVerifier.called)
	assert.False(t, cliVerifier.called, "CLI verifier should not be called when passkey fails")
}

// TestL3Notary_BrowserOnlySkipsCLI verifies the browser-only flow
// where no mTLS certificate fingerprint is present (pure web session approval).
func TestL3Notary_BrowserOnlySkipsCLI(t *testing.T) {
	logger := slog.Default()

	ctx := context.Background()
	userID := "pipeline-user-003"
	txHash := "pipeline-tx-hash-browser-only"

	// Browser-only proof: no mTLS fingerprint, so CLI verifier should not be called
	passkeyVerifier := &mockL3Notary{result: true}
	cliVerifier := &mockCLISessionVerifier{result: nil}
	notary := NewGatewayL3Notary(cliVerifier, passkeyVerifier, logger)

	l3Proof := &commonv1.L3Proof{
		CredentialId:      "browser-credential-id",
		ClientDataJson:    "browser-client-data",
		AuthenticatorData: "browser-authenticator-data",
		Signature:         "browser-passkey-signature",
	}

	allowed, err := notary.VerifyL3Proof(ctx, userID, txHash, "", l3Proof)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, passkeyVerifier.called)
	assert.False(t, cliVerifier.called, "CLI verifier should not be called for browser-only proof")
}
