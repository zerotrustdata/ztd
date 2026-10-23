// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const (
	renewerOperatorID = "op-001"
	renewerSessionID  = "sess-1"
	renewerSPIFFEID   = "spiffe://ztd.local/operator/org-1/" + renewerOperatorID + "/" + renewerSessionID
)

// renewalTestCA issues Operator leaves the way the Gateway's Operator CA does.
type renewalTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newRenewalTestCA(t *testing.T) *renewalTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "renewal test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &renewalTestCA{cert: cert, key: key}
}

// issue signs a client leaf for pub naming spiffeID.
func (ca *renewalTestCA) issue(t *testing.T, pub crypto.PublicKey, spiffeID string, notBefore, notAfter time.Time) []byte {
	t.Helper()
	uri, err := url.Parse(spiffeID)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	require.NoError(t, err)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "ztdo"},
		URIs:         []*url.URL{uri},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, ca.cert, pub, ca.key)
	require.NoError(t, err)
	return der
}

// identity is a held Operator identity: the leaf followed by its issuer.
func (ca *renewalTestCA) identity(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der := ca.issue(t, &key.PublicKey, renewerSPIFFEID, notBefore, notAfter)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key, Leaf: leaf}
}

// renewed answers req the way the Gateway does: a leaf for the CSR's key and
// the chain leaf, intermediate, root.
func (ca *renewalTestCA) renewed(t *testing.T, req *models.OperatorRenewalRequest, spiffeID string, l2 *models.OperatorL2Trust) *models.OperatorRenewal {
	t.Helper()
	block, _ := pem.Decode([]byte(req.CSR))
	require.NotNil(t, block)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	require.NoError(t, err)
	der := ca.issue(t, csr.PublicKey, spiffeID, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	rootPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw}))
	return &models.OperatorRenewal{Certificate: leafPEM, CertificateChain: leafPEM + rootPEM, L2Trust: l2}
}

// renewalGateway is a pub/sub client whose Gateway answers each renewal
// request with reply.
type renewalGateway struct {
	t     *testing.T
	reply func(req models.OperatorRenewalFrame) []models.OperatorRenewalFrame

	mu       sync.Mutex
	subs     map[string]chan []byte
	requests []models.OperatorRenewalFrame
}

func (g *renewalGateway) Subscribe(_ context.Context, channel string) (<-chan []byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch := make(chan []byte, 8)
	g.subs[channel] = ch
	return ch, nil
}

func (g *renewalGateway) Publish(_ context.Context, channel string, data []byte) error {
	var frame models.OperatorRenewalFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, frame)
	for _, reply := range g.reply(frame) {
		out, err := json.Marshal(reply)
		require.NoError(g.t, err)
		g.subs[channel] <- out
	}
	return nil
}

func (g *renewalGateway) Close() {}

func (g *renewalGateway) Requests() []models.OperatorRenewalFrame {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]models.OperatorRenewalFrame(nil), g.requests...)
}

type renewerRig struct {
	ca       *renewalTestCA
	identity *certs.ClientIdentity
	l2Trust  *governance.L2TrustStore
	gateway  *renewalGateway
	renewer  *OperatorCertRenewer
	held     tls.Certificate
}

func newRenewerRig(t *testing.T, reply func(*renewerRig, models.OperatorRenewalFrame) []models.OperatorRenewalFrame) *renewerRig {
	t.Helper()
	ca := newRenewalTestCA(t)
	held := ca.identity(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	l2Trust, err := governance.NewL2TrustStore(nil)
	require.NoError(t, err)
	r := &renewerRig{ca: ca, identity: certs.NewClientIdentity(held), l2Trust: l2Trust, held: held}
	r.gateway = &renewalGateway{t: t, subs: map[string]chan []byte{}, reply: func(req models.OperatorRenewalFrame) []models.OperatorRenewalFrame {
		return reply(r, req)
	}}
	r.renewer = NewOperatorCertRenewer(func() (pubsub.PubSubClient, error) { return r.gateway, nil },
		r.identity, l2Trust, renewerOperatorID, renewerSessionID, testutil.NewTestLogger())
	r.renewer.attemptTimeout = 2 * time.Second
	return r
}

func (r *renewerRig) heldSerial(t *testing.T) string {
	t.Helper()
	cert, ok := r.identity.GetCertificate()
	require.True(t, ok)
	return cert.Leaf.SerialNumber.String()
}

func renewerL2Trust() *models.OperatorL2Trust {
	return &models.OperatorL2Trust{ConsensusSetID: "set-2", MemberKeyIDs: []string{"m1"}, Quorum: 1, Enabled: true}
}

// renewedFrame answers req with a leaf from the rig's CA naming spiffeID.
func (r *renewerRig) renewedFrame(t *testing.T, req models.OperatorRenewalFrame, spiffeID string, l2 *models.OperatorL2Trust) models.OperatorRenewalFrame {
	t.Helper()
	return models.OperatorRenewalFrame{Type: models.OperatorRenewalFrameRenewed, RequestID: req.RequestID,
		Renewed: r.ca.renewed(t, req.Request, spiffeID, l2)}
}

func TestOperatorCertRenewer_InstallsRenewedIdentityAndL2Trust(t *testing.T) {
	t.Parallel()
	r := newRenewerRig(t, func(r *renewerRig, req models.OperatorRenewalFrame) []models.OperatorRenewalFrame {
		// A frame for another request precedes the answer and is ignored.
		return []models.OperatorRenewalFrame{
			{Type: models.OperatorRenewalFrameError, RequestID: "other", Error: &models.OperatorRenewalError{Message: "not yours"}},
			r.renewedFrame(t, req, renewerSPIFFEID, renewerL2Trust()),
		}
	})
	before := r.heldSerial(t)

	require.NoError(t, r.renewer.renewOnce(t.Context()))

	requests := r.gateway.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, models.OperatorRenewalFrameRequest, requests[0].Type)
	assert.NotEmpty(t, requests[0].RequestID)

	cert, ok := r.identity.GetCertificate()
	require.True(t, ok)
	assert.NotEqual(t, before, cert.Leaf.SerialNumber.String(), "the renewed leaf replaces the held one")
	require.Len(t, cert.Leaf.URIs, 1)
	assert.Equal(t, renewerSPIFFEID, cert.Leaf.URIs[0].String())
	policy, err := r.l2Trust.GetConsensusPolicy(t.Context(), "set-2")
	require.NoError(t, err)
	assert.NotNil(t, policy, "the L2 trust in the reply replaces the held trust")
}

func TestOperatorCertRenewer_RejectsInvalidReplies(t *testing.T) {
	t.Parallel()
	foreign := newRenewalTestCA(t)
	cases := map[string]func(*testing.T, *renewerRig, models.OperatorRenewalFrame) models.OperatorRenewalFrame{
		"another workload": func(t *testing.T, r *renewerRig, req models.OperatorRenewalFrame) models.OperatorRenewalFrame {
			t.Helper()
			return r.renewedFrame(t, req, "spiffe://ztd.local/operator/org-1/op-other/sess-1", renewerL2Trust())
		},
		"another issuer": func(t *testing.T, _ *renewerRig, req models.OperatorRenewalFrame) models.OperatorRenewalFrame {
			t.Helper()
			return models.OperatorRenewalFrame{Type: models.OperatorRenewalFrameRenewed, RequestID: req.RequestID,
				Renewed: foreign.renewed(t, req.Request, renewerSPIFFEID, renewerL2Trust())}
		},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRenewerRig(t, func(r *renewerRig, req models.OperatorRenewalFrame) []models.OperatorRenewalFrame {
				return []models.OperatorRenewalFrame{reply(t, r, req)}
			})
			before := r.heldSerial(t)

			require.ErrorIs(t, r.renewer.renewOnce(t.Context()), constants.ErrOperatorRenewalReplyInvalid)
			assert.Equal(t, before, r.heldSerial(t), "an invalid reply leaves the held identity")
			policy, err := r.l2Trust.GetConsensusPolicy(t.Context(), "set-2")
			require.NoError(t, err)
			assert.Nil(t, policy, "an invalid reply leaves the held L2 trust")
		})
	}
}

func TestOperatorCertRenewer_MalformedL2TrustInstallsNothing(t *testing.T) {
	t.Parallel()
	r := newRenewerRig(t, func(r *renewerRig, req models.OperatorRenewalFrame) []models.OperatorRenewalFrame {
		bad := renewerL2Trust()
		bad.Quorum = 0
		return []models.OperatorRenewalFrame{r.renewedFrame(t, req, renewerSPIFFEID, bad)}
	})
	before := r.heldSerial(t)

	require.ErrorIs(t, r.renewer.renewOnce(t.Context()), constants.ErrValidationFailed)
	assert.Equal(t, before, r.heldSerial(t))
}

func TestOperatorCertRenewer_GatewayErrors(t *testing.T) {
	t.Parallel()
	errorReply := func(retryable bool) func(*renewerRig, models.OperatorRenewalFrame) []models.OperatorRenewalFrame {
		return func(_ *renewerRig, req models.OperatorRenewalFrame) []models.OperatorRenewalFrame {
			return []models.OperatorRenewalFrame{{Type: models.OperatorRenewalFrameError, RequestID: req.RequestID,
				Error: &models.OperatorRenewalError{Retryable: retryable, Message: "reason"}}}
		}
	}

	t.Run("refusal is final", func(t *testing.T) {
		t.Parallel()
		r := newRenewerRig(t, errorReply(false))
		require.ErrorIs(t, r.renewer.renewOnce(t.Context()), constants.ErrOperatorRenewalRefused)
	})
	t.Run("gateway failure is retryable", func(t *testing.T) {
		t.Parallel()
		r := newRenewerRig(t, errorReply(true))
		err := r.renewer.renewOnce(t.Context())
		require.Error(t, err)
		assert.NotErrorIs(t, err, constants.ErrOperatorRenewalRefused)
	})
	t.Run("no reply times out", func(t *testing.T) {
		t.Parallel()
		r := newRenewerRig(t, func(*renewerRig, models.OperatorRenewalFrame) []models.OperatorRenewalFrame { return nil })
		r.renewer.attemptTimeout = 50 * time.Millisecond
		require.ErrorIs(t, r.renewer.renewOnce(t.Context()), constants.ErrOperatorRenewalTimeout)
	})
	t.Run("refusal stops retries", func(t *testing.T) {
		t.Parallel()
		r := newRenewerRig(t, errorReply(false))
		require.ErrorIs(t, r.renewer.renewBefore(t.Context(), time.Now().Add(time.Hour)), constants.ErrOperatorRenewalRefused)
		assert.Len(t, r.gateway.Requests(), 1)
	})
	t.Run("retries end at expiry", func(t *testing.T) {
		t.Parallel()
		r := newRenewerRig(t, errorReply(true))
		require.ErrorIs(t, r.renewer.renewBefore(t.Context(), time.Now()), constants.ErrOperatorRenewalExpired)
	})
}

func TestOperatorCertRenewer_RunRenewsAtTwoThirdsOfLifetime(t *testing.T) {
	t.Parallel()
	r := newRenewerRig(t, func(r *renewerRig, req models.OperatorRenewalFrame) []models.OperatorRenewalFrame {
		return []models.OperatorRenewalFrame{r.renewedFrame(t, req, renewerSPIFFEID, renewerL2Trust())}
	})
	// Two thirds of this leaf's lifetime has passed: Run renews at once.
	r.identity.SetCertificate(r.ca.identity(t, time.Now().Add(-2*time.Hour), time.Now().Add(30*time.Minute)))
	before := r.heldSerial(t)
	updated := r.identity.Updated()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.renewer.Run(ctx)
	}()
	select {
	case <-updated:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Run did not renew a leaf past two thirds of its lifetime")
	}
	assert.NotEqual(t, before, r.heldSerial(t))

	// The renewed leaf is fresh, so Run waits until ctx ends.
	cancel()
	<-done
	assert.Len(t, r.gateway.Requests(), 1)
}
