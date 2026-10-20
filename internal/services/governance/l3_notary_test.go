// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// mockL3Notary is a test double for the L3Notary interface, used to verify
// the dispatch logic in NewGatewayL3Notary.
type mockL3Notary struct {
	called       bool
	calledUserID string
	calledTxHash string
	calledProof  *commonv1.L3Proof
	result       bool
	err          error
}

func (m *mockL3Notary) VerifyL3Proof(_ context.Context, userID, transactionHash, _ string, proof *commonv1.L3Proof) (bool, error) {
	m.called = true
	m.calledUserID = userID
	m.calledTxHash = transactionHash
	m.calledProof = proof
	return m.result, m.err
}

// VerifyPasskeyProof delegates to VerifyL3Proof so mockL3Notary also satisfies
// PasskeyVerifier for tests that wire it as the passkey delegate of
// NewGatewayL3Notary.
func (m *mockL3Notary) VerifyPasskeyProof(ctx context.Context, userID, transactionHash, cliSessionID string, proof *commonv1.L3Proof) (bool, error) {
	return m.VerifyL3Proof(ctx, userID, transactionHash, cliSessionID, proof)
}

func TestGatewayL3Notary_DispatchesToPasskeyVerifierForWebAuthnProofs(t *testing.T) {
	t.Parallel()

	passkeyMock := &mockL3Notary{result: true}
	notary := NewGatewayL3Notary(nil, passkeyMock, slog.Default())

	webauthnProof := &commonv1.L3Proof{
		CredentialId:      "cred-id",
		ClientDataJson:    "client-data",
		AuthenticatorData: "auth-data",
		Signature:         "sig",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "session-1", webauthnProof)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, passkeyMock.called)
	assert.Equal(t, "user-1", passkeyMock.calledUserID)
	assert.Equal(t, "tx-hash-1", passkeyMock.calledTxHash)
	assert.Equal(t, webauthnProof, passkeyMock.calledProof)
}

func TestGatewayL3Notary_RejectsInvalidCLISignatureWithoutPasskeyFallback(t *testing.T) {
	t.Parallel()

	txHash := "test-tx-hash-mtls-only"
	userID := "user-mtls-only"

	signature := "invalid-legacy-signature"

	passkeyMock := &mockL3Notary{result: false, err: errors.New("should not be called")}
	notary := NewGatewayL3Notary(nil, passkeyMock, slog.Default())

	mtlsProof := &commonv1.L3Proof{
		CliSignature:        signature,
		MtlsCertFingerprint: "abc123",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), userID, txHash, "cli-session", mtlsProof)
	require.ErrorIs(t, err, constants.ErrCLIL3TransactionHashRequired)
	assert.False(t, allowed)
	assert.False(t, passkeyMock.called)
}

func TestGatewayL3Notary_DualLayerPasskeyAndMTLS(t *testing.T) {
	t.Parallel()

	passkeyMock := &mockL3Notary{result: true}
	cliVerifier := &mockCLISessionVerifier{result: nil}
	notary := NewGatewayL3Notary(cliVerifier, passkeyMock, slog.Default())

	dualProof := &commonv1.L3Proof{
		CredentialId:        "cred-id",
		ClientDataJson:      "client-data",
		AuthenticatorData:   "auth-data",
		Signature:           "passkey-sig",
		MtlsCertFingerprint: "abc123",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "cli-session-1", dualProof)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, passkeyMock.called)
	assert.True(t, cliVerifier.called)
	assert.Equal(t, "user-1", cliVerifier.calledUserID)
	assert.Equal(t, "cli-session-1", cliVerifier.calledSessionID)
	assert.Equal(t, "abc123", cliVerifier.calledFingerprint)
}

func TestGatewayL3Notary_DualLayerCLISessionDeniedReturnsFalse(t *testing.T) {
	t.Parallel()

	passkeyMock := &mockL3Notary{result: true}
	cliVerifier := &mockCLISessionVerifier{result: constants.ErrCLISessionDenied}
	notary := NewGatewayL3Notary(cliVerifier, passkeyMock, slog.Default())

	dualProof := &commonv1.L3Proof{
		CredentialId:        "cred-id",
		ClientDataJson:      "client-data",
		AuthenticatorData:   "auth-data",
		Signature:           "passkey-sig",
		MtlsCertFingerprint: "abc123",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "cli-session-1", dualProof)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.True(t, passkeyMock.called)
	assert.True(t, cliVerifier.called)
}

func TestGatewayL3Notary_PasskeyVerifierErrorPropagates(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("passkey verification failed")
	passkeyMock := &mockL3Notary{result: false, err: expectedErr}
	notary := NewGatewayL3Notary(nil, passkeyMock, slog.Default())

	webauthnProof := &commonv1.L3Proof{
		CredentialId:      "cred-id",
		ClientDataJson:    "client-data",
		AuthenticatorData: "auth-data",
		Signature:         "sig",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "session-1", webauthnProof)
	assert.False(t, allowed)
	assert.ErrorIs(t, err, expectedErr)
	assert.True(t, passkeyMock.called)
}

func TestGatewayL3Notary_NilProofReturnsError(t *testing.T) {
	t.Parallel()

	passkeyMock := &mockL3Notary{}
	notary := NewGatewayL3Notary(nil, passkeyMock, slog.Default())

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "session-1", nil)
	require.Error(t, err)
	assert.False(t, allowed)
	assert.False(t, passkeyMock.called)
	assert.True(t, errors.Is(err, constants.ErrGatewayL3ProofRequired))
}

// mockCLISessionVerifier is a test double for the CLISessionVerifier interface.
type mockCLISessionVerifier struct {
	called            bool
	calledUserID      string
	calledSessionID   string
	calledFingerprint string
	result            error
}

func (m *mockCLISessionVerifier) VerifyCLISession(_ context.Context, userID, cliSessionID, certFingerprint string) error {
	m.called = true
	m.calledUserID = userID
	m.calledSessionID = cliSessionID
	m.calledFingerprint = certFingerprint
	return m.result
}

func TestCompositionMatrix_GatewayNotary_DoesNotAccessSuspendedStore(t *testing.T) {
	t.Parallel()

	passkeyMock := &mockL3Notary{result: true}
	cliVerifier := &mockCLISessionVerifier{result: nil}
	notary := NewGatewayL3Notary(cliVerifier, passkeyMock, slog.Default())

	proof := &commonv1.L3Proof{
		CredentialId:        "cred-id",
		ClientDataJson:      "client-data",
		AuthenticatorData:   "auth-data",
		Signature:           "sig",
		MtlsCertFingerprint: "abc123",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "cli-session-1", proof)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, passkeyMock.called, "gatewayNotary must invoke passkey verifier")
	assert.True(t, cliVerifier.called, "gatewayNotary must invoke CLI session verifier when mTLS fingerprint present")
}

func TestCompositionMatrix_GatewayNotary_NoMTLSFingerprintSkipsCLIVerifier(t *testing.T) {
	t.Parallel()

	passkeyMock := &mockL3Notary{result: true}
	cliVerifier := &mockCLISessionVerifier{result: nil}
	notary := NewGatewayL3Notary(cliVerifier, passkeyMock, slog.Default())

	proof := &commonv1.L3Proof{
		CredentialId:      "cred-id",
		ClientDataJson:    "client-data",
		AuthenticatorData: "auth-data",
		Signature:         "sig",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "session-1", proof)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, passkeyMock.called, "gatewayNotary must invoke passkey verifier")
	assert.False(t, cliVerifier.called, "gatewayNotary must NOT invoke CLI session verifier without mTLS fingerprint")
}

// mockPasskeyVerifier is a test double for the PasskeyVerifier interface,
// distinct from mockL3Notary, used to verify the passkeyL3Notary adapter
// delegates correctly.
type mockPasskeyVerifier struct {
	called       bool
	calledUserID string
	calledTxHash string
	result       bool
	err          error
}

func (m *mockPasskeyVerifier) VerifyPasskeyProof(_ context.Context, userID, transactionHash, _ string, _ *commonv1.L3Proof) (bool, error) {
	m.called = true
	m.calledUserID = userID
	m.calledTxHash = transactionHash
	return m.result, m.err
}

func TestPasskeyL3Notary_DelegatesToPasskeyVerifier(t *testing.T) {
	t.Parallel()

	verifier := &mockPasskeyVerifier{result: true}
	notary := NewGatewayL3Notary(nil, verifier, slog.Default())

	proof := &commonv1.L3Proof{
		CredentialId:      "cred-id",
		ClientDataJson:    "client-data",
		AuthenticatorData: "auth-data",
		Signature:         "sig",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "session-1", proof)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, verifier.called, "passkeyL3Notary must delegate to the PasskeyVerifier")
	assert.Equal(t, "user-1", verifier.calledUserID)
	assert.Equal(t, "tx-hash-1", verifier.calledTxHash)
}

func TestPasskeyL3Notary_PropagatesVerifierError(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("passkey verification failed")
	verifier := &mockPasskeyVerifier{result: false, err: expectedErr}
	notary := NewGatewayL3Notary(nil, verifier, slog.Default())

	proof := &commonv1.L3Proof{
		CredentialId:      "cred-id",
		ClientDataJson:    "client-data",
		AuthenticatorData: "auth-data",
		Signature:         "sig",
	}

	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", "tx-hash-1", "session-1", proof)
	assert.False(t, allowed)
	assert.ErrorIs(t, err, expectedErr)
	assert.True(t, verifier.called)
}
