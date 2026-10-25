// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// deliberateEnvelope is a hashed-looking envelope for the deliberation tests.
func deliberateEnvelope(txHash string) *commonv1.GovernanceEnvelope {
	return &commonv1.GovernanceEnvelope{Id: txHash, TransactionHash: txHash, ActionType: ActionMcpCall}
}

// deliberateProof is an L2 proof with one vote, as the bound app would attach.
func deliberateProof() *commonv1.L2Metadata {
	return &commonv1.L2Metadata{
		ConsensusSetId: "set-1",
		Votes: []*commonv1.L2Vote{{
			SignerKeyId:        "set-1-member-0",
			ConsensusSignature: "ab12",
			Decision:           true,
		}},
	}
}

// deliberateAppID is the bound primary consensus application of these tests.
const deliberateAppID = "conformance-consensus-app"

// deliberateCA issues workload certificates the way the Gateway Operator CA
// does: a SPIFFE URI SAN and no DNS name.
type deliberateCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newDeliberateCA(t *testing.T) *deliberateCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "conformance test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &deliberateCA{cert: cert, key: key, pool: pool}
}

// appCertificate issues a server certificate naming appID's SPIFFE identity.
func (ca *deliberateCA) appCertificate(t *testing.T, appID string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	spiffeID, err := protocol.NewWorkloadIdentity().AppSPIFFEURL(appID)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: appID},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{spiffeID},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// deliberateApp starts the application's HTTPS endpoint presenting cert.
func deliberateApp(t *testing.T, cert tls.Certificate, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	app := httptest.NewUnstartedServer(handler)
	app.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
	app.StartTLS()
	t.Cleanup(app.Close)
	return app
}

// deliberateGateway serves binding discovery for binding and returns a client
// whose Operator identity trusts roots.
func deliberateGateway(t *testing.T, binding models.ConsensusPolicy, roots *x509.CertPool) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, constants.APIPaths.AuthPlatformEnrollmentConsensus, r.URL.Path)
		assert.Empty(t, r.Header.Get(constants.HeaderCLISessionID), "binding discovery runs on the Operator identity, which never carries the CLI session")
		_ = json.NewEncoder(w).Encode(binding)
	}))
	t.Cleanup(srv.Close)
	c, err := New(config.Config{MTLSBaseURL: srv.URL, PublicBaseURL: srv.URL})
	require.NoError(t, err)
	c.http = srv.Client()
	c.roots = roots
	return c
}

// deliberateClient serves Gateway binding discovery and the bound app's
// protobuf endpoint under the app's SPIFFE identity.
func deliberateClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	ca := newDeliberateCA(t)
	app := deliberateApp(t, ca.appCertificate(t, deliberateAppID), handler)
	return deliberateGateway(t, models.ConsensusPolicy{Enabled: true, AppID: deliberateAppID, EndpointURL: app.URL + "/app/decide"}, ca.pool)
}

func TestDeliberate_Returns200ProofForTheSentTransaction(t *testing.T) {
	env := deliberateEnvelope("tx-1")
	env.Payload = []byte{0xff, 0x00, 0xfe}
	var mu sync.Mutex
	var gotPath, gotMethod, gotContentType, gotCLISession string
	var gotBody []byte
	c := deliberateClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotMethod, gotBody = r.URL.Path, r.Method, body
		gotContentType = r.Header.Get("Content-Type")
		gotCLISession = r.Header.Get(constants.HeaderCLISessionID)
		mu.Unlock()
		signed := deliberateEnvelope("tx-1")
		signed.Governance = &commonv1.GovernanceMetadata{L2: deliberateProof()}
		wire, err := proto.Marshal(signed)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wire)
	})
	var exchanges []Exchange
	c.Record(&exchanges)

	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance", CLISessionID: "gateway-cli-session"}, env)
	require.NoError(t, err)
	assert.True(t, proto.Equal(deliberateProof(), proof), "proof: %v", proof)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/app/decide", gotPath)
	assert.Equal(t, "application/x-protobuf", gotContentType)
	assert.Empty(t, gotCLISession, "Gateway session headers must stay on the Gateway")
	var sent commonv1.GovernanceEnvelope
	require.NoError(t, proto.Unmarshal(gotBody, &sent))
	assert.Equal(t, "tx-1", sent.GetTransactionHash())
	assert.True(t, proto.Equal(env, &sent), "the full envelope must reach the application")
	require.Len(t, exchanges, 2, "capture binding discovery and application deliberation")
	assert.Equal(t, gotBody, []byte(exchanges[1].ReqRaw), "capture protobuf bytes without altering them")
}

func TestDeliberate_NonOKAnswersWrapDeliberationFailed(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"bad request on hash mismatch", http.StatusBadRequest},
		{"app endpoint not found", http.StatusNotFound},
		{"internal error", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("refused"))
			})
			proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
			require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
			assert.Nil(t, proof)
		})
	}
}

func TestDeliberate_ProofForAnotherTransactionFails(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
		signed := deliberateEnvelope("tx-other")
		signed.Governance = &commonv1.GovernanceMetadata{L2: deliberateProof()}
		wire, _ := proto.Marshal(signed)
		_, _ = w.Write(wire)
	})
	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	assert.Nil(t, proof)
}

func TestDeliberate_AnswerWithoutVotesFails(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
		wire, _ := proto.Marshal(deliberateEnvelope("tx-1"))
		_, _ = w.Write(wire)
	})
	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	assert.Nil(t, proof)
}

func TestDeliberate_UndecodableAnswerFails(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"unknown_field": 1}`))
	})
	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	assert.Nil(t, proof)
}

func TestDeliberate_InvalidBindingDoesNotContactApplication(t *testing.T) {
	for _, tt := range []struct {
		name    string
		binding models.ConsensusPolicy
	}{
		{"disabled", models.ConsensusPolicy{EndpointURL: "https://app.invalid/decide"}},
		{"missing endpoint", models.ConsensusPolicy{Enabled: true}},
		{"plaintext endpoint", models.ConsensusPolicy{Enabled: true, EndpointURL: "http://app.invalid/decide"}},
		{"missing host", models.ConsensusPolicy{Enabled: true, EndpointURL: "https:///decide"}},
		{"malformed URL", models.ConsensusPolicy{Enabled: true, EndpointURL: "://invalid"}},
		{"URL credentials", models.ConsensusPolicy{Enabled: true, EndpointURL: "https://user:password@app.invalid/decide"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, constants.APIPaths.AuthPlatformEnrollmentConsensus, r.URL.Path)
				_ = json.NewEncoder(w).Encode(tt.binding)
			}))
			t.Cleanup(srv.Close)
			c, err := New(config.Config{MTLSBaseURL: srv.URL})
			require.NoError(t, err)
			c.http = srv.Client()
			proof, err := c.Deliberate(context.Background(), Persona{}, deliberateEnvelope("tx-1"))
			require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
			if tt.binding.Enabled {
				require.ErrorIs(t, err, constants.ErrConsensusEndpointInvalid)
			} else {
				require.ErrorIs(t, err, constants.ErrTxL2ConsensusNotConfigured)
			}
			assert.Nil(t, proof)
		})
	}
}

func TestDeliberate_ApplicationRedirectIsRejected(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://app.invalid/plaintext", http.StatusTemporaryRedirect)
	})
	proof, err := c.Deliberate(context.Background(), Persona{}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	require.ErrorIs(t, err, constants.ErrConsensusEndpointInvalid)
	assert.Nil(t, proof)
}

// TestDeliberate_ApplicationIdentityIsPinned proves the envelope reaches only
// the bound primary application, authenticated by its SPIFFE identity under
// the Operator identity's trust anchors.
func TestDeliberate_ApplicationIdentityIsPinned(t *testing.T) {
	ca := newDeliberateCA(t)
	for _, tt := range []struct {
		name    string
		cert    func(t *testing.T) tls.Certificate
		appID   string
		roots   *x509.CertPool
		wantErr error
	}{
		{"another application's identity", func(t *testing.T) tls.Certificate { t.Helper(); return ca.appCertificate(t, "other-consensus-app") }, deliberateAppID, ca.pool, constants.ErrConsensusAppNotEnrolled},
		{"untrusted issuer", func(t *testing.T) tls.Certificate {
			t.Helper()
			return newDeliberateCA(t).appCertificate(t, deliberateAppID)
		}, deliberateAppID, ca.pool, nil},
		{"no trust anchors", func(t *testing.T) tls.Certificate { t.Helper(); return ca.appCertificate(t, deliberateAppID) }, deliberateAppID, nil, constants.ErrEmptyTrustBundle},
		{"binding names no application", func(t *testing.T) tls.Certificate { t.Helper(); return ca.appCertificate(t, deliberateAppID) }, "", ca.pool, constants.ErrConsensusAppNotEnrolled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var reached atomic.Bool
			app := deliberateApp(t, tt.cert(t), func(w http.ResponseWriter, _ *http.Request) {
				reached.Store(true)
				w.WriteHeader(http.StatusOK)
			})
			c := deliberateGateway(t, models.ConsensusPolicy{Enabled: true, AppID: tt.appID, EndpointURL: app.URL + "/app/decide"}, tt.roots)
			proof, err := c.Deliberate(context.Background(), Persona{}, deliberateEnvelope("tx-1"))
			require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				var unknownAuthority x509.UnknownAuthorityError
				require.ErrorAs(t, err, &unknownAuthority)
			}
			assert.Nil(t, proof)
			assert.False(t, reached.Load(), "the envelope must not reach an unauthenticated application")
		})
	}
}

func TestDeliberateInternalTribunalUsesAuthenticatedGateway(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(models.ConsensusPolicy{Enabled: true, Provider: string(constants.ConsensusProviderTribunal), Quorum: 1})
			return
		}
		assert.Equal(t, "/api/v1/governance/deliberations", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var sent commonv1.GovernanceEnvelope
		raw, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) || !assert.NoError(t, protojson.Unmarshal(raw, &sent)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, "tx-internal", sent.GetTransactionHash())
		_, _ = w.Write([]byte(`{"envelope":{"id":"tx-internal","transactionHash":"tx-internal","governance":{"l2":{"consensusSetId":"set-1","votes":[{"signerKeyId":"set-1-member-0","consensusSignature":"ab12","decision":true}]}}}}`))
	}))
	defer srv.Close()
	c, err := New(config.Config{MTLSBaseURL: srv.URL, PublicBaseURL: srv.URL})
	require.NoError(t, err)
	c.http = srv.Client()
	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-internal"))
	require.NoError(t, err)
	assert.True(t, proto.Equal(deliberateProof(), proof))
}
