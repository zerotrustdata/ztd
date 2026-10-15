// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTLSCertInspectTool_Metadata(t *testing.T) {
	tool := &TLSCertInspectTool{}
	assert.Equal(t, "tls_cert_inspect", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.NotNil(t, tool.InputSchema())
}

func TestInspectCertificate(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	notBefore := now.Add(-24 * time.Hour)
	notAfter := now.Add(24 * time.Hour)

	cert := &x509.Certificate{
		Subject: pkix.Name{
			CommonName:         "test-subject",
			Organization:       []string{"test-org"},
			OrganizationalUnit: []string{"test-ou"},
		},
		Issuer: pkix.Name{
			CommonName: "test-issuer",
		},
		SerialNumber:       big.NewInt(12345),
		NotBefore:          notBefore,
		NotAfter:           notAfter,
		SignatureAlgorithm: x509.SHA256WithRSA,
		PublicKeyAlgorithm: x509.RSA,
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:           []string{"example.com"},
		EmailAddresses:     []string{"test@example.com"},
		IPAddresses:        []net.IP{net.ParseIP("1.2.3.4")},
	}

	result := inspectCertificate(cert)

	assert.Equal(t, "test-subject", result.Subject)
	assert.Equal(t, "test-issuer", result.Issuer)
	assert.Equal(t, "12345", result.SerialNumber)
	assert.Equal(t, notBefore.Format(time.RFC3339), result.NotBefore)
	assert.Equal(t, notAfter.Format(time.RFC3339), result.NotAfter)
	assert.False(t, result.IsExpired)
	assert.True(t, result.DaysUntilExpiry >= 0)
	assert.Equal(t, "SHA256-RSA", result.SignatureAlgorithm)
	assert.Equal(t, "RSA", result.PublicKeyAlgorithm)
	assert.Equal(t, x509.KeyUsageDigitalSignature, result.KeyUsage)
	assert.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, result.ExtKeyUsage)
	assert.Equal(t, []string{"example.com"}, result.DNSNames)
	assert.Equal(t, []string{"test@example.com"}, result.EmailAddresses)
	assert.Equal(t, []string{"1.2.3.4"}, result.IPAddresses)
	assert.Equal(t, "test-org", result.Organization)
	assert.Equal(t, "test-ou", result.OrganizationalUnit)
}

func TestInspectCertificate_Expired(t *testing.T) {
	now := time.Now()
	cert := &x509.Certificate{
		NotAfter:     now.Add(-24 * time.Hour),
		SerialNumber: big.NewInt(1),
	}

	result := inspectCertificate(cert)
	assert.True(t, result.IsExpired)
	assert.True(t, result.DaysUntilExpiry < 0)
	assert.False(t, result.IsNearExpiry)
}

func TestInspectCertificate_NearExpiry(t *testing.T) {
	now := time.Now()
	// 15 days from now is within the 30-day "near expiry" window
	cert := &x509.Certificate{
		NotAfter:     now.Add(15 * 24 * time.Hour),
		SerialNumber: big.NewInt(1),
	}

	result := inspectCertificate(cert)
	assert.False(t, result.IsExpired)
	assert.True(t, result.IsNearExpiry)
}
