// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	approvals "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

func TestOutboundL3Notary_VerifiesPinnedCLIProofWithoutSuspendedStore(t *testing.T) {
	txHash := strings.Repeat("a", 64)
	cert := newGatewayCLIApprovalCertificate(t, "user-1", "approver-session", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	notary, err := NewOutboundL3Notary(roots)
	require.NoError(t, err)
	valid, err := approvals.SignCLIApproval(txHash, cert)
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		mutate     func(*commonv1.L3Proof)
		user, hash string
		allowed    bool
	}{
		{name: "valid separate approver session", allowed: true},
		{name: "different user", user: "other-user"},
		{name: "different transaction", hash: strings.Repeat("b", 64)},
		{name: "tampered signature", mutate: func(p *commonv1.L3Proof) { p.CliSignature = "00" }},
		{name: "tampered fingerprint", mutate: func(p *commonv1.L3Proof) { p.MtlsCertFingerprint = strings.Repeat("0", 64) }},
		{name: "mixed mechanisms", mutate: func(p *commonv1.L3Proof) { p.CredentialId = "passkey" }},
		{name: "mixed context", mutate: func(p *commonv1.L3Proof) { p.PasskeyContext = &commonv1.PasskeyVerificationContext{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof := proto.Clone(valid).(*commonv1.L3Proof)
			if tc.mutate != nil {
				tc.mutate(proof)
			}
			user, hash := tc.user, tc.hash
			if user == "" {
				user = "user-1"
			}
			if hash == "" {
				hash = txHash
			}
			allowed, err := notary.VerifyL3Proof(t.Context(), user, hash, "submitter-session", proof)
			require.Equal(t, tc.allowed, allowed)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	untrusted, err := NewOutboundL3Notary(x509.NewCertPool())
	require.NoError(t, err)
	allowed, err := untrusted.VerifyL3Proof(t.Context(), "user-1", txHash, "", valid)
	require.False(t, allowed)
	require.Error(t, err)
	_, err = NewOutboundL3Notary(nil)
	require.ErrorIs(t, err, constants.ErrEmptyTrustBundle)
}

func TestOutboundL3Notary_VerifiesPasskeyAssertionFromRegistrationContext(t *testing.T) {
	txHash := strings.Repeat("c", 64)
	key := testutil.NewSoftwarePasskey(t)
	credential := key.Credential(t)
	assertion := key.Assert(t, "localhost", []byte(txHash))
	valid := &commonv1.L3Proof{
		CredentialId: assertion.ID, ClientDataJson: assertion.ClientDataJSON,
		AuthenticatorData: assertion.AuthenticatorData, Signature: assertion.Signature,
		PasskeyContext: &commonv1.PasskeyVerificationContext{
			UserId: "user-1", CredentialId: assertion.ID, PublicKeyCose: credential.PublicKey,
			RpId: "localhost", AllowedOrigins: []string{testutil.SoftwarePasskeyOrigin},
		},
	}
	notary, err := NewOutboundL3Notary(x509.NewCertPool())
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		mutate  func(*commonv1.L3Proof)
		allowed bool
	}{
		{name: "valid", allowed: true},
		{name: "missing context", mutate: func(p *commonv1.L3Proof) { p.PasskeyContext = nil }},
		{name: "other user", mutate: func(p *commonv1.L3Proof) { p.PasskeyContext.UserId = "other-user" }},
		{name: "other credential", mutate: func(p *commonv1.L3Proof) { p.PasskeyContext.CredentialId = "other-credential" }},
		{name: "other relying party", mutate: func(p *commonv1.L3Proof) { p.PasskeyContext.RpId = "other.example" }},
		{name: "other origin", mutate: func(p *commonv1.L3Proof) { p.PasskeyContext.AllowedOrigins = []string{"https://other.example"} }},
		{name: "missing origins", mutate: func(p *commonv1.L3Proof) { p.PasskeyContext.AllowedOrigins = nil }},
		{name: "other key", mutate: func(p *commonv1.L3Proof) {
			p.PasskeyContext.PublicKeyCose = testutil.NewSoftwarePasskey(t).Credential(t).PublicKey
		}},
		{name: "tampered signature", mutate: func(p *commonv1.L3Proof) { p.Signature = "AA" }},
		{name: "mixed mechanisms", mutate: func(p *commonv1.L3Proof) { p.CliSignature = "00" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof := proto.Clone(valid).(*commonv1.L3Proof)
			if tc.mutate != nil {
				tc.mutate(proof)
			}
			allowed, err := notary.VerifyL3Proof(t.Context(), "user-1", txHash, "", proof)
			require.Equal(t, tc.allowed, allowed)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	allowed, err := notary.VerifyL3Proof(t.Context(), "user-1", strings.Repeat("d", 64), "", valid)
	require.False(t, allowed)
	require.Error(t, err)
}
