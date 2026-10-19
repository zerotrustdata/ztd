// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package e2e

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// maxResponseBytes limits response body reads to prevent unbounded memory
// consumption from a misbehaving or malicious endpoint. 1 MiB is generous for
// ordinary E2E responses (health, individual operator documents and results).
const maxResponseBytes = 1 << 20

// defaultClientTimeout is the per-request timeout for standard E2E client
// operations. Long-running operations (command dispatch) override this.
const defaultClientTimeout = 30 * time.Second

// parseCAPool parses a PEM-encoded CA bundle into an x509.CertPool. An empty
// bundle returns constants.ErrEmptyTrustBundle; a bundle with no valid PEM
// certificate returns constants.ErrCAParseFailed.
func parseCAPool(caBundle []byte) (*x509.CertPool, error) {
	if len(caBundle) == 0 {
		return nil, constants.ErrEmptyTrustBundle
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBundle) {
		return nil, constants.ErrCAParseFailed
	}
	return pool, nil
}

// truncateBody returns a shortened representation of body bytes for error
// messages, capped at 512 characters.
func truncateBody(body []byte) string {
	const max = 512
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "...(truncated)"
}

// doRequest executes an HTTP request, reads a bounded response body, checks
// the status code, and returns the raw body bytes. Returns an error if the
// status code is not the expected value or if the response exceeds
// maxResponseBytes.
func doRequest(client *http.Client, req *http.Request, expectedStatus int) ([]byte, int, error) {
	return doRequestLimit(client, req, expectedStatus, maxResponseBytes)
}

// doRequestLimit is doRequest with an explicit response size bound.
func doRequestLimit(client *http.Client, req *http.Request, expectedStatus int, limit int64) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, resp.StatusCode, fmt.Errorf("response exceeds %d bytes", limit)
	}
	if resp.StatusCode != expectedStatus {
		return body, resp.StatusCode, fmt.Errorf("status %d, expected %d: %s", resp.StatusCode, expectedStatus, truncateBody(body))
	}
	return body, resp.StatusCode, nil
}

// decodeJSON unmarshals body into a value of type T, wrapping any decode
// error with the provided label for contextual diagnostics. It is the single
// typed decode path used by all E2EClient response handlers, so decode-failure
// behavior is unit-testable without network or platform dependencies.
func decodeJSON[T any](body []byte, label string) (T, error) {
	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return result, fmt.Errorf("decode %s: %w", label, err)
	}
	return result, nil
}
