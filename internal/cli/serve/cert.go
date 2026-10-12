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
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// GenerateCSR generates a new ECDSA P-256 keypair and CSR for the given common name.
func GenerateCSR(commonName string) (string, *ecdsa.PrivateKey, error) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", constants.ErrCSRGenerationFailed, err)
	}

	template := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"ztd"},
		},
	}

	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, privKey)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", constants.ErrCSRGenerationFailed, err)
	}

	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrBytes,
	})

	return string(csrPEM), privKey, nil
}

// LogCertBundle parses every PEM certificate in pemData and logs its details.
func LogCertBundle(logger *slog.Logger, label string, pemData []byte) {
	rest := pemData
	idx := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			logger.Info("[TLS-DEBUG] non-cert PEM block in bundle", "label", label, "type", block.Type)
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			logger.Warn("[TLS-DEBUG] failed to parse cert in bundle", "label", label, "idx", idx, "error", err)
			idx++
			continue
		}
		fp := sha256.Sum256(block.Bytes)
		logger.Info("[TLS-DEBUG] bundle cert",
			"label", label,
			"idx", idx,
			"subject", cert.Subject.String(),
			"issuer", cert.Issuer.String(),
			"serial", cert.SerialNumber.String(),
			"not_before", cert.NotBefore.Format(time.RFC3339),
			"not_after", cert.NotAfter.Format(time.RFC3339),
			"is_ca", cert.IsCA,
			"key_algo", cert.PublicKeyAlgorithm.String(),
			"sig_algo", cert.SignatureAlgorithm.String(),
			"sha256", hex.EncodeToString(fp[:]),
		)
		idx++
	}
	logger.Info("[TLS-DEBUG] bundle parsed", "label", label, "cert_count", idx)
}
