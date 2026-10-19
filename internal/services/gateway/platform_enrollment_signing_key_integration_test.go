// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestPlatformEnrollment_OrdinaryApplicationEnrollsSigningKeyAndBindsConsensus(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)
	csr, transportKey := generateAppCSRAndKey(t)
	publicKey, signingKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	created, err := createPlatformEnrollmentRequest(t, env.enrollSvc, t.Context(), models.PlatformEnrollmentCreateRequest{
		ComponentKind: models.PlatformComponentApplication, AppName: "ordinary-consensus",
		InstanceID: "ordinary-consensus-1", Hostname: "consensus.local",
		App: &models.PlatformAppCSRPayload{CSRPEM: csr, SigningPublicKey: hex.EncodeToString(publicKey)},
	}, "https://localhost")
	require.NoError(t, err)
	require.NotEmpty(t, created.Fingerprints.SigningKey)
	wrongFingerprints := created.Fingerprints
	wrongFingerprints.SigningKey = "another-signing-key"
	_, err = env.enrollSvc.DecideBatch(t.Context(), env.ownerID, models.PlatformEnrollmentBatchDecisionRequest{
		Requests: []models.PlatformEnrollmentDecisionTarget{{RequestID: created.RequestID, Fingerprints: wrongFingerprints}},
		Decision: models.PlatformEnrollmentDecisionApprove,
	})
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentInvalidDecision)
	_, err = env.enrollSvc.Decide(t.Context(), env.ownerID, models.PlatformEnrollmentDecisionRequest{
		RequestID: created.RequestID, Decision: models.PlatformEnrollmentDecisionApprove,
	})
	require.NoError(t, err)
	stored := loadStoredRequest(t, env, created.RequestID)
	transcript, err := platformEnrollmentCompletionTranscript(stored)
	require.NoError(t, err)
	digest := sha256.Sum256(transcript)
	proofs := models.PlatformEnrollmentProofs{App: signCompletionTranscript(t, stored, transportKey)}
	token := platformEnrollmentTestToken(t, created.RequestID)
	_, err = env.enrollSvc.Complete(t.Context(), token, proofs)
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentProofRequired)
	_, otherKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	proofs.SigningKey = base64.RawURLEncoding.EncodeToString(ed25519.Sign(otherKey, digest[:]))
	_, err = env.enrollSvc.Complete(t.Context(), token, proofs)
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentProofInvalid)
	proofs.SigningKey = base64.RawURLEncoding.EncodeToString(ed25519.Sign(signingKey, digest[:]))
	completed, err := env.enrollSvc.Complete(t.Context(), token, proofs)
	require.NoError(t, err)
	block, _ := pem.Decode([]byte(completed.App.AppCert))
	require.NotNil(t, block)
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.IsType(t, &ecdsa.PublicKey{}, certificate.PublicKey)
	require.Empty(t, certificate.DNSNames, "app certificate TLS identity is the Gateway-issued SPIFFE URI, not CSR-provided DNS")
	require.Contains(t, certificate.URIs[0].String(), "/app/ordinary-consensus")
	registered, err := env.svc.GetSignerStore().GetTrustedSigner(t.Context(), "ordinary-consensus")
	require.NoError(t, err)
	require.Equal(t, publicKey, registered)
	endpointHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-protobuf" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		wire, err := io.ReadAll(r.Body)
		var envelope commonv1.GovernanceEnvelope
		if err != nil || proto.Unmarshal(wire, &envelope) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		signature := ed25519.Sign(signingKey, governance.L2VotePayload(envelope.GetTransactionHash(), true))
		envelope.Governance = &commonv1.GovernanceMetadata{L2: &commonv1.L2Metadata{
			ConsensusSetId: string(constants.DocIDConsensusBinding),
			Votes:          []*commonv1.L2Vote{{SignerKeyId: "ordinary-consensus", Decision: true, ConsensusSignature: hex.EncodeToString(signature)}},
		}}
		wire, err = proto.Marshal(&envelope)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(wire)
	})
	transportKeyDER, err := x509.MarshalECPrivateKey(transportKey)
	require.NoError(t, err)
	serverCert, err := tls.X509KeyPair(
		[]byte(completed.App.AppCert+"\n"+completed.App.CertChain),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: transportKeyDER}),
	)
	require.NoError(t, err)
	endpoint := httptest.NewUnstartedServer(endpointHandler)
	endpoint.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}}
	endpoint.StartTLS()
	t.Cleanup(endpoint.Close)
	_, port, err := net.SplitHostPort(endpoint.Listener.Addr().String())
	require.NoError(t, err)
	endpointURL := "https://localhost:" + port + "/deliberate"
	bound, err := env.enrollSvc.BindConsensusApplication(t.Context(), env.ownerID, models.PlatformEnrollmentBindConsensusRequest{
		AppID: "ordinary-consensus", EndpointURL: endpointURL,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"ordinary-consensus"}, bound.MemberAppIDs)
	require.Equal(t, endpointURL, bound.EndpointURL)
	envelope := &commonv1.GovernanceEnvelope{TransactionHash: "ordinary-enrollment-l2", Payload: []byte("full-envelope-payload")}
	wire, err := protojson.Marshal(envelope)
	require.NoError(t, err)
	signedWire, err := env.svc.GetConsensusStore().NewConsensusAppDeliberatorWithClientFactory(env.svc.GetPKI().NewConsensusAppHTTPClient).Deliberate(t.Context(), wire)
	require.NoError(t, err)
	var signed commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(signedWire, &signed))
	require.Equal(t, envelope.TransactionHash, signed.GetTransactionHash())
	require.Equal(t, envelope.Payload, signed.GetPayload())
	proof := signed.GetGovernance().GetL2()
	require.Equal(t, string(constants.DocIDConsensusBinding), proof.GetConsensusSetId())
	require.Len(t, proof.GetVotes(), 1)
	signature, err := hex.DecodeString(proof.GetVotes()[0].GetConsensusSignature())
	require.NoError(t, err)
	require.True(t, ed25519.Verify(registered, governance.L2VotePayload(envelope.TransactionHash, true), signature))

	wrongIdentityClient, err := env.svc.GetPKI().NewConsensusAppHTTPClient("different-consensus-app")
	require.NoError(t, err)
	wrongIdentityRequest, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpointURL, nil)
	require.NoError(t, err)
	_, err = wrongIdentityClient.Do(wrongIdentityRequest)
	require.Error(t, err, "TLS peer with another enrolled app identity must be rejected")
	wrongIdentityClient.CloseIdleConnections()
	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(untrusted.Close)
	untrustedClient, err := env.svc.GetPKI().NewConsensusAppHTTPClient("ordinary-consensus")
	require.NoError(t, err)
	untrustedRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, untrusted.URL, nil)
	require.NoError(t, err)
	_, err = untrustedClient.Do(untrustedRequest)
	require.Error(t, err, "certificate chain outside Gateway PKI must be rejected")
	untrustedClient.CloseIdleConnections()
	trust, err := env.svc.GetConsensusStore().OperatorL2Trust(t.Context())
	require.NoError(t, err)
	require.Len(t, trust.Signers, 1)
	require.Equal(t, hex.EncodeToString(publicKey), trust.Signers[0].PublicKeyHex)
	_, err = env.enrollSvc.Revoke(t.Context(), env.ownerID, models.PlatformEnrollmentRevokeRequest{RequestID: created.RequestID})
	require.NoError(t, err)
	registered, err = env.svc.GetSignerStore().GetTrustedSigner(t.Context(), "ordinary-consensus")
	require.NoError(t, err)
	require.Nil(t, registered)
	bound, err = env.svc.GetConsensusStore().GetBoundConsensusApp(t.Context())
	require.NoError(t, err)
	require.Nil(t, bound)
	_, err = env.enrollSvc.Complete(t.Context(), token, proofs)
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentRevoked)
}
