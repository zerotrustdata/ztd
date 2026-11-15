// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/pkg/certutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// expireCLICert re-signs cert's leaf with the test PKI's Operator intermediate
// so that it expired at notAfter, keeping its URI SAN and key.
func expireCLICert(t *testing.T, f *renewalFixture, cert tls.Certificate, notAfter time.Time) tls.Certificate {
	t.Helper()
	var caKey *ecdsa.PrivateKey
	require.NoError(t, f.c.pki.loadCAPrivateKey(string(constants.CATypeOperator), &caKey))
	template := *cert.Leaf
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	template.SerialNumber = serial
	template.NotBefore = notAfter.Add(-7 * 24 * time.Hour)
	template.NotAfter = notAfter
	der, err := x509.CreateCertificate(rand.Reader, &template, f.c.pki.operatorCert, cert.Leaf.PublicKey, caKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: cert.PrivateKey, Leaf: leaf}
}

// makeSessionCurrent records cert as the fixture session's current certificate.
// The session expired with it, as a CLISession's expiry tracks its certificate.
func makeSessionCurrent(t *testing.T, f *renewalFixture, cert tls.Certificate) {
	t.Helper()
	session := f.session(t)
	session.CertFingerprint = fingerprintOf(cert)
	session.CertSerial = cert.Leaf.SerialNumber.String()
	session.ExpiresAt = cert.Leaf.NotAfter
	session.AbsoluteExpiresAt = cert.Leaf.NotAfter
	session.IdleExpiresAt = cert.Leaf.NotAfter
	b, err := json.Marshal(session)
	require.NoError(t, err)
	require.NoError(t, f.infra.DocStore.DocSet(testutil.Context(t), marshaler.CollectionName(constants.CollectionCLISessions), f.sessionID, b))
}

// newExpiredFixture is a renewal fixture whose session's current certificate
// expired at notAfter.
func newExpiredFixture(t *testing.T, notAfter time.Time) (*renewalFixture, tls.Certificate) {
	t.Helper()
	f := newRenewalFixture(t)
	expired := expireCLICert(t, f, f.original, notAfter)
	makeSessionCurrent(t, f, expired)
	return f, expired
}

// reinstate posts a reinstatement of presented, signed by signer, for a new CSR.
func (f *renewalFixture) reinstate(t *testing.T, presented tls.Certificate, signer *ecdsa.PrivateKey) (*httptest.ResponseRecorder, models.CLIReinstateRequest, *ecdsa.PrivateKey) {
	t.Helper()
	csrPEM, key, _ := generateTestCSR(t, "ztd-cli-reinstate")
	sig, err := ecdsa.SignASN1(rand.Reader, signer, certutil.CLIReinstateProofDigest(f.sessionID, csrPEM))
	require.NoError(t, err)
	req := models.CLIReinstateRequest{
		CLICertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: presented.Certificate[0]})),
		CLICSRPEM:  csrPEM,
		Signature:  base64.StdEncoding.EncodeToString(sig),
	}
	return f.postReinstate(t, req), req, key
}

func (f *renewalFixture) postReinstate(t *testing.T, req models.CLIReinstateRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	f.c.handleReinstate(rr, httptest.NewRequest(http.MethodPost, constants.APIPaths.AuthCLIReinstate, bytes.NewReader(body)))
	return rr
}

func TestCLIReinstate_ReissuesAnExpiredCertificateForTheSameSession(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-48*time.Hour))

	rr, req, key := f.reinstate(t, expired, expired.PrivateKey.(*ecdsa.PrivateKey))
	resp := parseRotationResponse(t, rr)
	assert.Equal(t, f.sessionID, resp.CLISessionID, "the CLI session is kept")
	assert.Equal(t, f.userID, resp.UserID)

	reinstated := tlsCertFromPEM(t, resp.CLICert, key)
	assert.True(t, time.Now().Before(reinstated.Leaf.NotAfter), "the reissued certificate is valid")
	session := f.session(t)
	assert.Equal(t, fingerprintOf(reinstated), session.CertFingerprint, "the session moves onto the reissued certificate")
	assert.True(t, session.ExpiresAt.After(time.Now()), "the expired session's expiry is extended")
	assert.True(t, session.AbsoluteExpiresAt.After(time.Now()))
	assert.True(t, session.IdleExpiresAt.After(time.Now()))

	// A replayed request no longer presents the session's current certificate.
	replay := f.postReinstate(t, req)
	assert.Equal(t, http.StatusPreconditionFailed, replay.Code, replay.Body.String())
}

func TestCLIReinstate_RefusesAProofByAnotherKey(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-time.Hour))
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	before := f.session(t).CertFingerprint

	rr, _, _ := f.reinstate(t, expired, other)
	assert.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLIReinstateProofInvalid.Error())
	assert.Equal(t, before, f.session(t).CertFingerprint, "nothing is reissued")
}

func TestCLIReinstate_RefusesAProofForAnotherCSR(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-time.Hour))
	signedCSR, _, _ := generateTestCSR(t, "ztd-cli-reinstate")
	otherCSR, _, _ := generateTestCSR(t, "ztd-cli-reinstate")
	sig, err := ecdsa.SignASN1(rand.Reader, expired.PrivateKey.(*ecdsa.PrivateKey), certutil.CLIReinstateProofDigest(f.sessionID, signedCSR))
	require.NoError(t, err)

	rr := f.postReinstate(t, models.CLIReinstateRequest{
		CLICertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: expired.Certificate[0]})),
		CLICSRPEM:  otherCSR,
		Signature:  base64.StdEncoding.EncodeToString(sig),
	})
	assert.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLIReinstateProofInvalid.Error())
}

func TestCLIReinstate_RefusesACertificateExpiredPastTheWindow(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-constants.CLIReinstateWindow-time.Minute))

	rr, _, _ := f.reinstate(t, expired, expired.PrivateKey.(*ecdsa.PrivateKey))
	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLIReinstateWindowElapsed.Error())
}

func TestCLIReinstate_RefusesALoggedOutSession(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-time.Hour))
	session := f.session(t)
	session.IsActive = false
	b, err := json.Marshal(session)
	require.NoError(t, err)
	require.NoError(t, f.infra.DocStore.DocSet(testutil.Context(t), marshaler.CollectionName(constants.CollectionCLISessions), f.sessionID, b))

	rr, _, _ := f.reinstate(t, expired, expired.PrivateKey.(*ecdsa.PrivateKey))
	assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLISessionAlreadyDeactivated.Error())
}

func TestCLIReinstate_RefusesARevokedCertificate(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-time.Hour))
	require.NoError(t, f.c.pki.RevokeCertificate(testutil.Context(t), expired.Leaf.SerialNumber.String(), "test"))

	rr, _, _ := f.reinstate(t, expired, expired.PrivateKey.(*ecdsa.PrivateKey))
	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrPKICertificateRevoked.Error())
}

func TestCLIReinstate_RefusesACertificateFromAnotherCA(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-time.Hour))
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: expired.Leaf.Issuer,
		NotBefore: time.Now().Add(-30 * 24 * time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	forgedTemplate := *expired.Leaf
	forgedDER, err := x509.CreateCertificate(rand.Reader, &forgedTemplate, caCert, expired.Leaf.PublicKey, caKey)
	require.NoError(t, err)
	forgedLeaf, err := x509.ParseCertificate(forgedDER)
	require.NoError(t, err)
	forged := tls.Certificate{Certificate: [][]byte{forgedDER}, PrivateKey: expired.PrivateKey, Leaf: forgedLeaf}
	makeSessionCurrent(t, f, forged)

	rr, _, _ := f.reinstate(t, forged, expired.PrivateKey.(*ecdsa.PrivateKey))
	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLIReinstateCertInvalid.Error())
}

func TestCLIReinstate_RefusesACertificateTheSessionNoLongerHolds(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-time.Hour))
	makeSessionCurrent(t, f, f.original)

	rr, _, _ := f.reinstate(t, expired, expired.PrivateKey.(*ecdsa.PrivateKey))
	assert.Equal(t, http.StatusPreconditionFailed, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLIRenewalStaleCertificate.Error())
}

// The Operator intermediate also signs Operator and application leaves, which
// are client certificates too. Only a URI SAN naming a CLI session reinstates.
func TestCLIReinstate_RefusesANonCLILeafFromTheSameIntermediate(t *testing.T) {
	f, _ := newExpiredFixture(t, time.Now().Add(-time.Hour))
	csrPEM, key, _ := generateTestCSR(t, "ztd-app-reinstate")
	appPEM, _, err := f.c.pki.SignCSR(csrPEM, constants.LeafTypeApp, "", "", f.userID, "", "")
	require.NoError(t, err)
	app := tlsCertFromPEM(t, appPEM, key)
	// A client certificate, so the refusal comes from the URI SAN check, not EKU.
	require.Contains(t, app.Leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	before := f.session(t).CertFingerprint

	rr, _, _ := f.reinstate(t, app, key)
	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLIReinstateCertInvalid.Error())
	assert.Equal(t, before, f.session(t).CertFingerprint, "nothing is reissued")
}

func TestCLIReinstate_RefusesADisabledUser(t *testing.T) {
	f, expired := newExpiredFixture(t, time.Now().Add(-time.Hour))
	require.NoError(t, f.infra.UserSvc.Disable(testutil.Context(t), f.userID, "test", "actor", "op"))
	before := f.session(t).CertFingerprint

	rr, _, _ := f.reinstate(t, expired, expired.PrivateKey.(*ecdsa.PrivateKey))
	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	assert.Equal(t, before, f.session(t).CertFingerprint, "nothing is reissued")
}
