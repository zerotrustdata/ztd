// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package certs

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// FetchTrustBundle fetches the hub trust bundle from the given URL
// (e.g. https://host/.well-known/ztd/pki/ca-bundle) with client, validates it
// is a non-empty PEM block, and returns the raw PEM bytes. It does NOT mutate
// any global state.
//
// This is the CLI's bootstrap step that establishes trust. The trust bundle
// endpoint is unauthenticated by design - it is equivalent to a certificate
// pinning fetch. All subsequent connections are verified against this trust
// bundle. Operators never call it: they receive trust on the bootstrap
// websocket.
//
// The optional caFingerprint parameter enables OOB pinning verification. If provided,
// the fetched CA bundle's SHA-256 fingerprint must match the expected value.
//
// The client's transport (and thus its DialContext) governs how the CA URL is
// dialed, so the CLI routes the fetch through its IPv4-only dialer.
func FetchTrustBundle(ctx context.Context, caURL string, caFingerprint string, client *http.Client) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: fetch trust bundle: nil client", constants.ErrHTTPRequestCreateFailed)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, caURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to build CA fetch request", constants.ErrHTTPRequestCreateFailed)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch CA certificate from %s", constants.ErrHTTPRequestExecuteFailed, caURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: CA fetch returned HTTP %d from %s", constants.ErrHTTPStatusError, resp.StatusCode, caURL)
	}

	pem, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read CA certificate body", constants.ErrHTTPResponseReadFailed)
	}

	if len(pem) == 0 {
		return nil, fmt.Errorf("%w: CA certificate from %s is empty", constants.ErrEmptyTrustBundle, caURL)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%w: CA certificate from %s is not a valid PEM-encoded certificate", constants.ErrCAParseFailed, caURL)
	}

	// Verify CA fingerprint if pin is provided
	if caFingerprint != "" {
		if err := verifyCAFingerprint(pem, caFingerprint); err != nil {
			return nil, fmt.Errorf("CA fingerprint verification failed: %w", err)
		}
	}

	return pem, nil
}

// verifyCAFingerprint verifies that a PEM-encoded CA bundle matches the expected fingerprint.
// This is a copy of the auth package function to avoid circular dependencies.
func verifyCAFingerprint(caPEM []byte, expectedFingerprint string) error {
	if expectedFingerprint == "" {
		return nil
	}

	// Parse the PEM to extract the DER-encoded certificate
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return constants.ErrPEMDecodeFailed
	}

	if block.Type != "CERTIFICATE" {
		return fmt.Errorf("%w: PEM block is not a certificate (type: %s)", constants.ErrInvalidPEMType, block.Type)
	}

	// Compute SHA-256 hash of the DER-encoded certificate
	hash := sha256.Sum256(block.Bytes)
	actualFP := hex.EncodeToString(hash[:])

	if actualFP != expectedFingerprint {
		return fmt.Errorf("%w: CA fingerprint mismatch: expected %s, got %s", constants.ErrValidationFailed, expectedFingerprint, actualFP)
	}

	return nil
}
