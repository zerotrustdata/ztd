// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	approvals "github.com/zerotrustdata/ztd/internal/governance"
)

func TestGatewayL3NotaryAcceptsEnrolledCLIApproval(t *testing.T) {
	t.Parallel()
	txHash := strings.Repeat("a", 64)
	cert := newGatewayCLIApprovalCertificate(t, "user-1", "approver-session", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	proof, err := approvals.SignCLIApproval(txHash, cert)
	require.NoError(t, err)

	passkey := &mockL3Notary{result: false}
	cliVerifier := &mockCLISessionVerifier{}
	notary := NewGatewayL3Notary(cliVerifier, passkey, nil)
	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", txHash, "submitter-session", proof)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.False(t, passkey.called, "CLI signatures must not fall back to WebAuthn")
	assert.True(t, cliVerifier.called)
	assert.Equal(t, "approver-session", cliVerifier.calledSessionID)
	assert.Equal(t, proof.MtlsCertFingerprint, cliVerifier.calledFingerprint)
}

func TestGatewayL3NotaryDeniesInvalidOrUnenrolledCLIApprovalWithoutFallback(t *testing.T) {
	t.Parallel()
	txHash := strings.Repeat("b", 64)
	cert := newGatewayCLIApprovalCertificate(t, "user-1", "approver-session", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	proof, err := approvals.SignCLIApproval(txHash, cert)
	require.NoError(t, err)
	proof.CliSignature = hex.EncodeToString([]byte("tampered"))

	passkey := &mockL3Notary{result: true}
	cliVerifier := &mockCLISessionVerifier{}
	notary := NewGatewayL3Notary(cliVerifier, passkey, nil)
	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", txHash, "submitter-session", proof)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.False(t, passkey.called)
	assert.False(t, cliVerifier.called, "cryptographically invalid proofs must not reach enrollment checks")

	validProof, err := approvals.SignCLIApproval(txHash, cert)
	require.NoError(t, err)
	notary = NewGatewayL3Notary(nil, passkey, nil)
	allowed, err = notary.VerifyL3Proof(context.Background(), "user-1", txHash, "submitter-session", validProof)
	require.NoError(t, err)
	assert.False(t, allowed, "CLI approval must fail closed when no enrollment verifier is configured")
	assert.False(t, passkey.called)
}

func TestGatewayL3NotaryDeniesRevokedApproverSession(t *testing.T) {
	t.Parallel()
	txHash := strings.Repeat("c", 64)
	cert := newGatewayCLIApprovalCertificate(t, "user-1", "approver-session", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	proof, err := approvals.SignCLIApproval(txHash, cert)
	require.NoError(t, err)

	passkey := &mockL3Notary{result: true}
	cliVerifier := &mockCLISessionVerifier{result: constants.ErrCLISessionDenied}
	notary := NewGatewayL3Notary(cliVerifier, passkey, nil)
	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", txHash, "different-submit-session", proof)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.False(t, passkey.called)
	assert.Equal(t, "approver-session", cliVerifier.calledSessionID)
}

func TestGatewayL3NotaryRejectsMixedCLIAndPasskeyProof(t *testing.T) {
	t.Parallel()
	txHash := strings.Repeat("d", 64)
	cert := newGatewayCLIApprovalCertificate(t, "user-1", "approver-session", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	proof, err := approvals.SignCLIApproval(txHash, cert)
	require.NoError(t, err)
	proof.CredentialId = "passkey-credential"

	passkey := &mockL3Notary{result: true}
	notary := NewGatewayL3Notary(&mockCLISessionVerifier{}, passkey, nil)
	allowed, err := notary.VerifyL3Proof(context.Background(), "user-1", txHash, "submitter-session", proof)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.False(t, passkey.called)
}

func newGatewayCLIApprovalCertificate(t *testing.T, userID, sessionID string, notBefore, notAfter time.Time) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	uri, err := url.Parse("spiffe://ztd.local/cli/" + userID + "/" + sessionID)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CLI"}, NotBefore: notBefore, NotAfter: notAfter, URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
