// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// TestConsensusDeliberation_SpeaksProtobufToApplicationAndReturnsCallerWire proves
// the deliberator takes the Gateway's protojson envelope wire, sends binary
// protobuf to a strict application, and returns the deliberated envelope as
// protojson so dispatch can publish it unchanged.
func TestConsensusDeliberation_SpeaksProtobufToApplicationAndReturnsCallerWire(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	publicKey, signingKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	const appID = "strict-consensus-app"
	require.NoError(t, infra.SignerStore.AddTrustedSigner(t.Context(), models.TrustedSigner{ID: appID, PublicKey: hex.EncodeToString(publicKey), AddedAt: time.Now().UTC(), Enabled: true}))
	var sawContentType atomic.Value
	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawContentType.Store(r.Header.Get("Content-Type"))
		wire, err := io.ReadAll(r.Body)
		var envelope commonv1.GovernanceEnvelope
		if err != nil || proto.Unmarshal(wire, &envelope) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		envelope.Governance = &commonv1.GovernanceMetadata{L2: &commonv1.L2Metadata{
			ConsensusSetId: string(constants.DocIDConsensusBinding),
			Votes: []*commonv1.L2Vote{{
				SignerKeyId: appID, Decision: true,
				ConsensusSignature: hex.EncodeToString(ed25519.Sign(signingKey, governance.L2VotePayload(envelope.GetId(), true))),
			}},
		}}
		signed, err := proto.Marshal(&envelope)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(signed)
	}))
	t.Cleanup(app.Close)
	require.NoError(t, infra.ConsensusStore.AddConsensus(t.Context(), models.ConsensusPolicy{
		ID: string(constants.DocIDConsensusBinding), MemberAppIDs: []string{appID},
		Quorum: 1, RequireDistinct: true, EndpointURL: app.URL, Enabled: true,
	}))

	envelope := &commonv1.GovernanceEnvelope{Id: "transcoded-envelope", TransactionHash: "transcoded-envelope", Payload: []byte("payload")}
	wire, err := protojson.Marshal(envelope)
	require.NoError(t, err)
	deliberated, err := infra.ConsensusStore.NewConsensusAppDeliberator(app.Client()).Deliberate(t.Context(), wire)
	require.NoError(t, err)
	require.Equal(t, "application/x-protobuf", sawContentType.Load())
	var signed commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(deliberated, &signed), "the caller receives the protojson wire it sent")
	require.Equal(t, envelope.GetPayload(), signed.GetPayload())
	require.Len(t, signed.GetGovernance().GetL2().GetVotes(), 1)

	_, err = infra.ConsensusStore.NewConsensusAppDeliberator(app.Client()).Deliberate(t.Context(), []byte("not an envelope"))
	require.ErrorIs(t, err, constants.ErrTxInvalidEnvelope)
}

// TestConsensusDeliberation_RejectsAlteredEnvelope proves the application may
// only add L2 votes: dispatch publishes the response, so a changed payload
// or id must not reach the Operator.
func TestConsensusDeliberation_RejectsAlteredEnvelope(t *testing.T) {
	const appID = "altering-consensus-app"
	alterations := map[string]func(*commonv1.GovernanceEnvelope){
		"payload": func(env *commonv1.GovernanceEnvelope) { env.Payload = []byte("substituted") },
		"id": func(env *commonv1.GovernanceEnvelope) {
			env.Id = "substituted-envelope"
			env.TransactionHash = "substituted-envelope"
		},
	}
	for name, alter := range alterations {
		t.Run(name, func(t *testing.T) {
			infra := setupTestInfrastructure(t, false)
			publicKey, signingKey, err := ed25519.GenerateKey(nil)
			require.NoError(t, err)
			require.NoError(t, infra.SignerStore.AddTrustedSigner(t.Context(), models.TrustedSigner{ID: appID, PublicKey: hex.EncodeToString(publicKey), AddedAt: time.Now().UTC(), Enabled: true}))
			app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wire, err := io.ReadAll(r.Body)
				var envelope commonv1.GovernanceEnvelope
				if err != nil || proto.Unmarshal(wire, &envelope) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				alter(&envelope)
				envelope.Governance = &commonv1.GovernanceMetadata{L2: &commonv1.L2Metadata{
					ConsensusSetId: string(constants.DocIDConsensusBinding),
					Votes: []*commonv1.L2Vote{{
						SignerKeyId: appID, Decision: true,
						ConsensusSignature: hex.EncodeToString(ed25519.Sign(signingKey, governance.L2VotePayload(envelope.GetId(), true))),
					}},
				}}
				signed, err := proto.Marshal(&envelope)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
				_, _ = w.Write(signed)
			}))
			t.Cleanup(app.Close)
			require.NoError(t, infra.ConsensusStore.AddConsensus(t.Context(), models.ConsensusPolicy{
				ID: string(constants.DocIDConsensusBinding), MemberAppIDs: []string{appID},
				Quorum: 1, RequireDistinct: true, EndpointURL: app.URL, Enabled: true,
			}))

			wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: "sent-envelope", TransactionHash: "sent-envelope", Payload: []byte("payload")})
			require.NoError(t, err)
			_, err = infra.ConsensusStore.NewConsensusAppDeliberator(app.Client()).Deliberate(t.Context(), wire)
			require.ErrorIs(t, err, constants.ErrTxL2EnvelopeAltered)
		})
	}
}

func TestConsensusDeliberation_RejectsPlaintextAndRedirects(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	privateEnvelope, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Payload: []byte("private envelope")})
	require.NoError(t, err)
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, status)
			}))
			t.Cleanup(redirect.Close)
			client := redirect.Client()
			originalRedirect := client.CheckRedirect
			deliberator := infra.ConsensusStore.NewConsensusAppDeliberator(client)
			for _, endpoint := range []string{target.URL, redirect.URL} {
				data, err := json.Marshal(models.ConsensusPolicy{Enabled: true, EndpointURL: endpoint})
				require.NoError(t, err)
				require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding), data))
				_, err = deliberator.Deliberate(t.Context(), privateEnvelope)
				require.ErrorIs(t, err, constants.ErrConsensusEndpointInvalid)
			}
			require.Equal(t, int32(0), received.Load())
			require.Equal(t, originalRedirect == nil, client.CheckRedirect == nil)
		})
	}
}

// TestConsensusDeliberation_FactoryClientRejectsRedirects proves the production
// factory path enforces the redirect contract. The Gateway's PKI client sets no
// CheckRedirect of its own, so the deliberator must refuse the redirect even
// though the TLS peer is the enrolled, identity-verified application.
func TestConsensusDeliberation_FactoryClientRejectsRedirects(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	const appID = "redirecting-consensus-app"
	publicKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, infra.SignerStore.AddTrustedSigner(t.Context(), models.TrustedSigner{ID: appID, PublicKey: hex.EncodeToString(publicKey), AddedAt: time.Now().UTC(), Enabled: true}))

	csr, transportKey := generateAppCSRAndKey(t)
	certPEM, chainPEM, err := infra.PKI.SignPlatformAppCSR(csr, appID, "consensus-owner")
	require.NoError(t, err)
	transportKeyDER, err := x509.MarshalECPrivateKey(transportKey)
	require.NoError(t, err)
	serverCert, err := tls.X509KeyPair(
		[]byte(certPEM+"\n"+chainPEM),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: transportKeyDER}),
	)
	require.NoError(t, err)

	var reached atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/deliberate", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/redirected", func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	endpoint := httptest.NewUnstartedServer(mux)
	endpoint.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}}
	endpoint.StartTLS()
	t.Cleanup(endpoint.Close)

	require.NoError(t, infra.ConsensusStore.AddConsensus(t.Context(), models.ConsensusPolicy{
		ID: string(constants.DocIDConsensusBinding), MemberAppIDs: []string{appID},
		Quorum: 1, RequireDistinct: true, EndpointURL: endpoint.URL + "/deliberate", Enabled: true,
	}))
	wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: "factory-redirect", TransactionHash: "factory-redirect", Payload: []byte("payload")})
	require.NoError(t, err)

	deliberator := infra.ConsensusStore.NewConsensusAppDeliberatorWithClientFactory(infra.PKI.NewConsensusAppHTTPClient)
	_, err = deliberator.Deliberate(t.Context(), wire)
	require.ErrorIs(t, err, constants.ErrConsensusEndpointInvalid)
	require.Equal(t, int32(0), reached.Load(), "a redirect target must never receive the envelope")
}
