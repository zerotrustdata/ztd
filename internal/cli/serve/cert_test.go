// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger returns a silent logger suitable for unit tests.
func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// generateTestCertPEM returns PEM-encoded certificate bytes for testing.
func generateTestCertPEM(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})
}

// ---------------------------------------------------------------------------
// GenerateCSR
// ---------------------------------------------------------------------------

func TestGenerateCSR(t *testing.T) {
	t.Parallel()
	t.Run("Success", func(t *testing.T) {
		t.Parallel()
		csrPEM, privKey, err := GenerateCSR("test-operator")
		require.NoError(t, err)
		assert.NotEmpty(t, csrPEM)
		assert.NotNil(t, privKey)

		block, _ := pem.Decode([]byte(csrPEM))
		require.NotNil(t, block)
		assert.Equal(t, "CERTIFICATE REQUEST", block.Type)

		csr, err := x509.ParseCertificateRequest(block.Bytes)
		require.NoError(t, err)
		assert.Equal(t, "test-operator", csr.Subject.CommonName)
		assert.Equal(t, []string{"ztd"}, csr.Subject.Organization)

		ecPub, ok := privKey.Public().(*ecdsa.PublicKey)
		require.True(t, ok)
		assert.Equal(t, elliptic.P256(), ecPub.Curve)
		assert.NotEqual(t, x509.Ed25519, csr.PublicKeyAlgorithm,
			"CSR must not use Ed25519 (excluded from Go FIPS TLS mode)")
	})

	t.Run("EmptyCommonName", func(t *testing.T) {
		t.Parallel()
		csrPEM, privKey, err := GenerateCSR("")
		require.NoError(t, err)
		assert.NotEmpty(t, csrPEM)
		assert.NotNil(t, privKey)

		block, _ := pem.Decode([]byte(csrPEM))
		require.NotNil(t, block)
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		require.NoError(t, err)
		assert.Empty(t, csr.Subject.CommonName)
	})

	t.Run("Uniqueness", func(t *testing.T) {
		t.Parallel()
		csr1, key1, err := GenerateCSR("test")
		require.NoError(t, err)

		csr2, key2, err := GenerateCSR("test")
		require.NoError(t, err)

		assert.NotEqual(t, csr1, csr2, "two CSRs with same CN should differ due to random keys")
		assert.False(t, key1.Equal(key2), "private keys should be distinct")
	})
}

// ---------------------------------------------------------------------------
// LogCertBundle
// ---------------------------------------------------------------------------

func TestLogCertBundle(t *testing.T) {
	t.Parallel()
	certPEM := generateTestCertPEM(t, time.Now(), time.Now().Add(365*24*time.Hour))

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tests := []struct {
		name    string
		label   string
		data    []byte
		wantErr bool
	}{
		{
			name:  "SingleCert",
			label: "test-bundle",
			data:  certPEM,
		},
		{
			name:  "MultipleCerts",
			label: "multi-bundle",
			data:  append(certPEM, generateTestCertPEM(t, time.Now(), time.Now().Add(200*24*time.Hour))...),
		},
		{
			name:  "EmptyData",
			label: "empty",
			data:  []byte{},
		},
		{
			name:  "NonCertPEMBlock",
			label: "mixed",
			data:  keyPEM,
		},
		{
			name:  "NilData",
			label: "nil",
			data:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger := testLogger()
			assert.NotPanics(t, func() {
				LogCertBundle(logger, tt.label, tt.data)
			})
		})
	}
}
