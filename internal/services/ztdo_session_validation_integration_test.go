// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package services

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestZtdoValidateOperatorSessionUsesMutualTLSGateway(t *testing.T) {
	const (
		operatorID = "session-validator-operator"
		sessionID  = "session-validator-session"
		spiffeURI  = "spiffe://ztd.local/operator/org-1/session-validator-operator/session-validator-session"
	)
	caPEM, serverCert, clientCert, roots := sessionValidationCertificates(t, spiffeURI)
	oldPublic, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	newPublic, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	oldTrust := sessionValidationTrust(t, 1, "member-old", oldPublic)
	newTrust := sessionValidationTrust(t, 2, "member-new", newPublic)

	var mu sync.Mutex
	currentResponse := models.OperatorSessionValidationResponse{Valid: true, OperatorID: operatorID, L2Trust: newTrust}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != constants.APIPaths.OperatorsValidate || r.Method != http.MethodPost {
			t.Errorf("unexpected validation endpoint %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 || len(r.TLS.PeerCertificates[0].URIs) != 1 || r.TLS.PeerCertificates[0].URIs[0].String() != spiffeURI {
			t.Errorf("request did not present the enrolled Operator mTLS identity")
			http.Error(w, "client certificate missing", http.StatusUnauthorized)
			return
		}
		var request models.OperatorSessionValidationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.OperatorSessionID != sessionID {
			t.Errorf("unexpected typed session validation request: %#v, err=%v", request, err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		response := currentResponse
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	t.Cleanup(server.Close)

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	cfg := testutil.NewTestConfig(t)
	cfg.OperatorID = operatorID
	cfg.OperatorSessionId = sessionID
	cfg.PubSubURL = "wss://" + serverURL.Host
	l2Trust, err := governance.NewL2TrustStore(oldTrust)
	require.NoError(t, err)
	tlsConfig := certs.NewTLSConfig(certs.NewTrustStore(caPEM), certs.NewClientIdentity(clientCert))
	service, err := NewZtdoService(cfg, testutil.NewTestLogger(), tlsConfig, l2Trust, newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)
	require.NoError(t, service.configureSessionValidationClient())
	t.Cleanup(func() { service.sessionValidationHTTPClient.CloseIdleConnections() })

	require.NoError(t, service.validateOperatorSession(t.Context(), sessionID))
	require.EqualValues(t, 2, l2Trust.BindingVersion())
	newKey, err := l2Trust.GetTrustedSigner(t.Context(), "member-new")
	require.NoError(t, err)
	require.Equal(t, newPublic, newKey)

	// A delayed old response cannot roll the current snapshot back.
	mu.Lock()
	currentResponse.L2Trust = oldTrust
	mu.Unlock()
	require.NoError(t, service.validateOperatorSession(t.Context(), sessionID))
	require.EqualValues(t, 2, l2Trust.BindingVersion())

	// An absent trust snapshot is a validation error and leaves held trust intact.
	mu.Lock()
	currentResponse.L2Trust = nil
	mu.Unlock()
	require.ErrorIs(t, service.validateOperatorSession(t.Context(), sessionID), constants.ErrValidationFailed)
	require.EqualValues(t, 2, l2Trust.BindingVersion())
}

func sessionValidationTrust(t *testing.T, version int64, member string, publicKey ed25519.PublicKey) *models.OperatorL2Trust {
	t.Helper()
	return &models.OperatorL2Trust{
		BindingVersion: version, ConsensusSetID: "bound-consensus", MemberKeyIDs: []string{member},
		Quorum: 1, RequireDistinct: true, Enabled: true,
		Signers: []models.OperatorSigner{{KeyID: member, PublicKeyHex: hex.EncodeToString(publicKey)}},
	}
}

func sessionValidationCertificates(t *testing.T, clientURI string) ([]byte, tls.Certificate, tls.Certificate, *x509.CertPool) {
	t.Helper()
	now := time.Now().UTC()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "validation-test-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	newLeaf := func(serial int64, usage []x509.ExtKeyUsage, uri string) (tls.Certificate, *ecdsa.PrivateKey) {
		key, keyErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, keyErr)
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "validation-test-leaf"},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usage,
		}
		if uri != "" {
			parsed, parseErr := url.Parse(uri)
			require.NoError(t, parseErr)
			template.URIs = []*url.URL{parsed}
		} else {
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		der, signErr := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		require.NoError(t, signErr)
		privateKey, marshalErr := x509.MarshalPKCS8PrivateKey(key)
		require.NoError(t, marshalErr)
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})
		certificate, pairErr := tls.X509KeyPair(certPEM, keyPEM)
		require.NoError(t, pairErr)
		return certificate, key
	}
	serverCert, _ := newLeaf(2, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, "")
	clientCert, _ := newLeaf(3, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, clientURI)
	return caPEM, serverCert, clientCert, roots
}
