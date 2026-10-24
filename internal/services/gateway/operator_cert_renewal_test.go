// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	pubsubv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/pubsub/v1"
)

const (
	renewalOrgID     = "org-1"
	renewalOpID      = "op-001"
	renewalSessionID = "sess-1"
)

// stubLeafAuthority records the SignCSR call and answers with fixed PEM.
type stubLeafAuthority struct {
	verifyErr error
	signErr   error
	signed    []string
}

func (a *stubLeafAuthority) VerifyCertificate(context.Context, *x509.Certificate) error {
	return a.verifyErr
}

func (a *stubLeafAuthority) SignCSR(_ string, leafType, organizationID, operatorID, userID, sessionID, gatewayID string) (string, string, error) {
	if a.signErr != nil {
		return "", "", a.signErr
	}
	a.signed = append(a.signed, leafType, organizationID, operatorID, userID, sessionID, gatewayID)
	return "leaf-pem", "chain-pem", nil
}

type stubL2TrustSource struct {
	trust *models.OperatorL2Trust
	err   error
}

func (s *stubL2TrustSource) OperatorL2Trust(context.Context, ...string) (*models.OperatorL2Trust, error) {
	return s.trust, s.err
}

// renewalPeer is the certificate an Operator session authenticates with.
func renewalPeer(t *testing.T, spiffeID string) *x509.Certificate {
	t.Helper()
	uri, err := url.Parse(spiffeID)
	require.NoError(t, err)
	return &x509.Certificate{SerialNumber: big.NewInt(7), URIs: []*url.URL{uri}}
}

func operatorPeer(t *testing.T) *x509.Certificate {
	t.Helper()
	return renewalPeer(t, "spiffe://ztd.local/operator/"+renewalOrgID+"/"+renewalOpID+"/"+renewalSessionID)
}

// renewalCSR is a P-256 CSR; dnsNames adds subject alternative names.
func renewalCSR(t *testing.T, dnsNames ...string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "ztdo"},
		DNSNames: dnsNames,
	}, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

type renewalRig struct {
	pki      *stubLeafAuthority
	sessions *stubOperatorSessionValidator
	trust    *stubL2TrustSource
	svc      *OperatorCertRenewalService
}

func newRenewalRig() *renewalRig {
	r := &renewalRig{
		pki:      &stubLeafAuthority{},
		sessions: &stubOperatorSessionValidator{op: &operatorv1.OperatorDocument{Id: renewalOpID, OrganizationId: renewalOrgID}},
		trust:    &stubL2TrustSource{trust: &models.OperatorL2Trust{ConsensusSetID: "set-1"}},
	}
	r.svc = NewOperatorCertRenewalService(r.pki, r.sessions, r.trust, testutil.NewTestLogger())
	return r
}

func TestOperatorCertRenewal_SignsForThePeerWorkload(t *testing.T) {
	r := newRenewalRig()
	renewed, err := r.svc.Renew(t.Context(), operatorPeer(t), pubsub.RenewalChannel(renewalOpID, renewalSessionID),
		&models.OperatorRenewalRequest{CSR: renewalCSR(t)})
	require.NoError(t, err)

	assert.Equal(t, &models.OperatorRenewal{Certificate: "leaf-pem", CertificateChain: "chain-pem", L2Trust: r.trust.trust}, renewed)
	assert.Equal(t, []string{constants.LeafTypeOperator, renewalOrgID, renewalOpID, "", renewalSessionID, ""}, r.pki.signed,
		"the renewed leaf names the workload the peer certificate names")
}

func TestOperatorCertRenewal_Refusals(t *testing.T) {
	ownChannel := pubsub.RenewalChannel(renewalOpID, renewalSessionID)
	cases := []struct {
		name      string
		mutate    func(*renewalRig)
		peer      func(*testing.T) *x509.Certificate
		channel   string
		req       func(*testing.T) *models.OperatorRenewalRequest
		wantErrIs error
	}{
		{
			name: "peer is not an Operator",
			peer: func(t *testing.T) *x509.Certificate {
				t.Helper()
				return renewalPeer(t, "spiffe://ztd.local/app/test-app")
			},
			wantErrIs: constants.ErrOperatorRenewalPeerNotOperator,
		},
		{
			name:      "no peer certificate",
			peer:      func(*testing.T) *x509.Certificate { return nil },
			wantErrIs: constants.ErrOperatorRenewalPeerNotOperator,
		},
		{
			name:      "another session's channel",
			channel:   pubsub.RenewalChannel(renewalOpID, "sess-2"),
			wantErrIs: constants.ErrOperatorRenewalChannelMismatch,
		},
		{
			name:      "revoked certificate",
			mutate:    func(r *renewalRig) { r.pki.verifyErr = constants.ErrPKICertificateRevoked },
			wantErrIs: constants.ErrPKICertificateRevoked,
		},
		{
			name:   "session refused",
			mutate: func(r *renewalRig) { r.sessions.err = &AuthError{Message: "operator terminated"} },
		},
		{
			name: "session names another organization",
			mutate: func(r *renewalRig) {
				r.sessions.op = &operatorv1.OperatorDocument{Id: renewalOpID, OrganizationId: "org-other"}
			},
			wantErrIs: constants.ErrOperatorRenewalSessionMismatch,
		},
		{
			name:      "missing request",
			req:       func(*testing.T) *models.OperatorRenewalRequest { return nil },
			wantErrIs: constants.ErrOperatorRenewalInvalidFrame,
		},
		{
			name: "CSR names a subject alternative name",
			req: func(t *testing.T) *models.OperatorRenewalRequest {
				t.Helper()
				return &models.OperatorRenewalRequest{CSR: renewalCSR(t, "gateway.example")}
			},
			wantErrIs: constants.ErrOperatorRenewalCSRSubjectAltName,
		},
		{
			name: "CSR is not PEM",
			req: func(*testing.T) *models.OperatorRenewalRequest {
				return &models.OperatorRenewalRequest{CSR: "not-a-csr"}
			},
			wantErrIs: constants.ErrPKIInvalidCSR,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRenewalRig()
			if tc.mutate != nil {
				tc.mutate(r)
			}
			peer := operatorPeer(t)
			if tc.peer != nil {
				peer = tc.peer(t)
			}
			channel := ownChannel
			if tc.channel != "" {
				channel = tc.channel
			}
			req := &models.OperatorRenewalRequest{CSR: renewalCSR(t)}
			if tc.req != nil {
				req = tc.req(t)
			}

			_, err := r.svc.Renew(t.Context(), peer, channel, req)
			require.ErrorIs(t, err, constants.ErrOperatorRenewalRefused)
			if tc.wantErrIs != nil {
				require.ErrorIs(t, err, tc.wantErrIs)
			}
			assert.Empty(t, r.pki.signed, "a refused renewal signs nothing")
		})
	}
}

func TestOperatorCertRenewal_GatewayFailuresAreRetryable(t *testing.T) {
	failure := errors.New("store unavailable")
	cases := map[string]func(*renewalRig){
		"certificate check fails": func(r *renewalRig) { r.pki.verifyErr = failure },
		"session lookup fails":    func(r *renewalRig) { r.sessions.err = failure },
		"signing fails":           func(r *renewalRig) { r.pki.signErr = failure },
		"l2 trust lookup fails":   func(r *renewalRig) { r.trust.err = failure },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRenewalRig()
			mutate(r)
			_, err := r.svc.Renew(t.Context(), operatorPeer(t), pubsub.RenewalChannel(renewalOpID, renewalSessionID),
				&models.OperatorRenewalRequest{CSR: renewalCSR(t)})
			require.ErrorIs(t, err, failure)
			assert.NotErrorIs(t, err, constants.ErrOperatorRenewalRefused)
		})
	}
}

// publishRenewal publishes data on the handler's own renewal channel and
// returns every frame the broker fanned out on it.
func publishRenewal(t *testing.T, broker *GatewayWebSocketHandler, handler *pubSubSessionHandler, data []byte) [][]byte {
	t.Helper()
	channel := pubsub.RenewalChannel(renewalOpID, renewalSessionID)
	var published [][]byte
	unregister := broker.RegisterHandler(channel, func(_ string, frame []byte) {
		published = append(published, frame)
	})
	defer unregister()
	handler.handleAction(&pubsubv1.PubSubMessage{Action: constants.PubSubActionPublish, Channel: channel, Data: data})
	return published
}

func renewalRequestFrame(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(models.OperatorRenewalFrame{
		Type:      models.OperatorRenewalFrameRequest,
		RequestID: "req-1",
		Request:   &models.OperatorRenewalRequest{CSR: renewalCSR(t)},
	})
	require.NoError(t, err)
	return data
}

func renewalSessionHandler(t *testing.T, broker *GatewayWebSocketHandler) *pubSubSessionHandler {
	t.Helper()
	handler := newOperatorSessionHandler(broker, renewalOpID)
	handler.sub.peerCert = operatorPeer(t)
	return handler
}

func decodeRenewalReply(t *testing.T, published [][]byte) models.OperatorRenewalFrame {
	t.Helper()
	require.Len(t, published, 1, "the request is answered once and never fanned out")
	var reply models.OperatorRenewalFrame
	require.NoError(t, json.Unmarshal(published[0], &reply))
	assert.Equal(t, "req-1", reply.RequestID)
	return reply
}

func TestRelayCertRenewal_RepliesWithRenewedIdentity(t *testing.T) {
	broker := newTestBroker(t)
	r := newRenewalRig()
	broker.SetCertRenewalService(r.svc)

	reply := decodeRenewalReply(t, publishRenewal(t, broker, renewalSessionHandler(t, broker), renewalRequestFrame(t)))
	assert.Equal(t, models.OperatorRenewalFrameRenewed, reply.Type)
	require.NotNil(t, reply.Renewed)
	assert.Equal(t, "leaf-pem", reply.Renewed.Certificate)
}

func TestRelayCertRenewal_RefusalIsFinalAndNamed(t *testing.T) {
	broker := newTestBroker(t)
	r := newRenewalRig()
	r.pki.verifyErr = constants.ErrPKICertificateRevoked
	broker.SetCertRenewalService(r.svc)

	reply := decodeRenewalReply(t, publishRenewal(t, broker, renewalSessionHandler(t, broker), renewalRequestFrame(t)))
	assert.Equal(t, models.OperatorRenewalFrameError, reply.Type)
	require.NotNil(t, reply.Error)
	assert.False(t, reply.Error.Retryable)
	assert.Contains(t, reply.Error.Message, constants.ErrPKICertificateRevoked.Error())
}

func TestRelayCertRenewal_GatewayFailureIsRetryableAndOpaque(t *testing.T) {
	broker := newTestBroker(t)
	r := newRenewalRig()
	r.pki.signErr = errors.New("ca key unreadable at /secret/path")
	broker.SetCertRenewalService(r.svc)

	reply := decodeRenewalReply(t, publishRenewal(t, broker, renewalSessionHandler(t, broker), renewalRequestFrame(t)))
	require.NotNil(t, reply.Error)
	assert.True(t, reply.Error.Retryable)
	assert.Equal(t, constants.ErrInternal.Error(), reply.Error.Message, "a Gateway-side failure stays in the Gateway log")
}

func TestRelayCertRenewal_UnconfiguredServiceIsRetryable(t *testing.T) {
	broker := newTestBroker(t)

	reply := decodeRenewalReply(t, publishRenewal(t, broker, renewalSessionHandler(t, broker), renewalRequestFrame(t)))
	require.NotNil(t, reply.Error)
	assert.True(t, reply.Error.Retryable)
	assert.Equal(t, constants.ErrOperatorRenewalUnavailable.Error(), reply.Error.Message)
}

func TestRelayCertRenewal_DropsFramesThatAreNotRequests(t *testing.T) {
	broker := newTestBroker(t)
	broker.SetCertRenewalService(newRenewalRig().svc)
	handler := renewalSessionHandler(t, broker)

	reply, err := json.Marshal(models.OperatorRenewalFrame{Type: models.OperatorRenewalFrameRenewed, RequestID: "req-1",
		Renewed: &models.OperatorRenewal{Certificate: "forged"}})
	require.NoError(t, err)
	noID, err := json.Marshal(models.OperatorRenewalFrame{Type: models.OperatorRenewalFrameRequest})
	require.NoError(t, err)

	for name, data := range map[string][]byte{"not json": []byte("{"), "reply frame": reply, "request without id": noID} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, publishRenewal(t, broker, handler, data), "an Operator cannot publish replies or malformed frames")
		})
	}
}
