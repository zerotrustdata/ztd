// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package certutil

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// RenewalTime is when a workload leaf is due for renewal: two thirds of the
// way through its validity window. Operator and CLI leaves both renew from
// this point, which leaves the final third for retries before expiry.
func RenewalTime(leaf *x509.Certificate) time.Time {
	return leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
}

// CLIReinstateProofDigest is the SHA-256 digest an expired CLI certificate's
// key signs (ECDSA over the digest) to reinstate its CLI session
// (POST /api/v1/auth/cli/reinstate). The hashed message is domain separated
// and binds the session and the exact replacement CSR, so a captured proof
// cannot obtain a certificate for any other key or session. ECDSA signs a
// digest, not a message: signing the message itself would truncate it to the
// curve size and drop the CSR binding.
func CLIReinstateProofDigest(cliSessionID, csrPEM string) []byte {
	csrDigest := sha256.Sum256([]byte(csrPEM))
	digest := sha256.Sum256([]byte(constants.CLIReinstateProofDomain + "\n" + cliSessionID + "\n" + hex.EncodeToString(csrDigest[:])))
	return digest[:]
}

// ParseCertFromPEM decodes PEM-encoded certificate data and returns the parsed
// x509 certificate. It validates that the PEM block is of type "CERTIFICATE".
func ParseCertFromPEM(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, constants.ErrPEMDecodeFailed
	}

	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%w: type=%s", constants.ErrInvalidPEMType, block.Type)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrCertParseFailed, err)
	}

	return cert, nil
}

// EncodeCertAndKey marshals an EC private key to PEM and combines a leaf
// certificate with an optional chain using the canonical "\n" separator.
// Returns the certificate bytes and key bytes. Storage is left to the caller.
func EncodeCertAndKey(certPEM, chainPEM string, key *ecdsa.PrivateKey) ([]byte, []byte, error) {
	if key == nil {
		return nil, nil, constants.ErrKeyParseFailed
	}

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", constants.ErrKeyParseFailed, err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	})

	certContent := certPEM
	if chainPEM != "" {
		certContent += "\n" + chainPEM
	}

	return []byte(certContent), keyPEM, nil
}
