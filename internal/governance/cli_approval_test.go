// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	"google.golang.org/protobuf/proto"
)

func TestCLIApprovalSignAndVerify(t *testing.T) {
	t.Parallel()
	cert := newCLIApprovalTestCertificate(t, "user-1", "session-1", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	txHash := strings.Repeat("a", 64)
	proof, err := SignCLIApproval(txHash, cert)
	require.NoError(t, err)
	require.NotEmpty(t, proof.GetCliCertificateDer())
	sessionID, err := VerifyCLIApproval("user-1", txHash, proof)
	require.NoError(t, err)
	require.Equal(t, "session-1", sessionID)
}

func TestCLIApprovalRejectsTamperingWrongUserAndExpiredCertificate(t *testing.T) {
	t.Parallel()
	txHash := strings.Repeat("b", 64)
	valid := newCLIApprovalTestCertificate(t, "user-1", "session-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	proof, err := SignCLIApproval(txHash, valid)
	require.NoError(t, err)

	_, err = VerifyCLIApproval("user-2", txHash, proof)
	require.Error(t, err)
	_, err = VerifyCLIApproval("user-1", strings.Repeat("c", 64), proof)
	require.Error(t, err)

	tampered := proto.Clone(proof).(*commonv1.L3Proof)
	tampered.CliSignature = tampered.CliSignature[:len(tampered.CliSignature)-2] + "00"
	_, err = VerifyCLIApproval("user-1", txHash, tampered)
	require.Error(t, err)

	expired := newCLIApprovalTestCertificate(t, "user-1", "session-1", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	_, err = SignCLIApproval(txHash, expired)
	require.Error(t, err)

	parsed, err := x509.ParseCertificate(valid.Certificate[0])
	require.NoError(t, err)
	expiredTemplate := *parsed
	expiredTemplate.SerialNumber = big.NewInt(2)
	expiredTemplate.NotBefore = time.Now().Add(-2 * time.Hour)
	expiredTemplate.NotAfter = time.Now().Add(-time.Hour)
	key := valid.PrivateKey.(*ecdsa.PrivateKey)
	expiredDER, err := x509.CreateCertificate(rand.Reader, &expiredTemplate, &expiredTemplate, &key.PublicKey, key)
	require.NoError(t, err)
	expiredProof := proto.Clone(proof).(*commonv1.L3Proof)
	expiredProof.CliCertificateDer = expiredDER
	fingerprint := sha256.Sum256(expiredDER)
	expiredProof.MtlsCertFingerprint = hex.EncodeToString(fingerprint[:])
	_, err = VerifyCLIApproval("user-1", txHash, expiredProof)
	require.Error(t, err)
}

func TestCLIApprovalRejectsMismatchedSigningKey(t *testing.T) {
	t.Parallel()
	cert := newCLIApprovalTestCertificate(t, "user-1", "session-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert.PrivateKey = otherKey
	_, err = SignCLIApproval(strings.Repeat("d", 64), cert)
	require.Error(t, err)
}

func newCLIApprovalTestCertificate(t *testing.T, userID, sessionID string, notBefore, notAfter time.Time) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	uri, err := url.Parse("spiffe://ztd.local/cli/" + userID + "/" + sessionID)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test cli"},
		NotBefore:    notBefore, NotAfter: notAfter,
		URIs:     []*url.URL{uri},
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
