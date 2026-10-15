// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/httpclient"
)

// maxListResponseBytes bounds the operator registry listing, which grows with
// fleet size (each Operator document carries its latest heartbeat) and with
// retained stopped or stale registrations. A 500-Operator fleet alone exceeds
// maxResponseBytes.
const maxListResponseBytes = 64 << 20

// dispatchRequestJSON is the typed JSON body for POST
// /api/v1/operators/commands. Mirrors gateway.OperatorCommandRequest. Defined
// locally to keep the E2E package decoupled from internal gateway types.
type dispatchRequestJSON struct {
	TargetOperatorSessionID string `json:"target_operator_session_id"`
	EventType               string `json:"event_type"`
	Payload                 []byte `json:"payload"`
	TargetResource          string `json:"target_resource,omitempty"`
}

// DocumentResponse is the wire shape returned by GET /api/v1/data/{collection}/{id}.
type DocumentResponse map[string]json.RawMessage

// GetString extracts a string field from a DocumentResponse. The bool is false
// when the field is absent or not a JSON string.
func (d DocumentResponse) GetString(field string) (string, bool) {
	raw, ok := d[field]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// GetBool extracts a bool field from a DocumentResponse. The bool is false when
// the field is absent or not a JSON bool.
func (d DocumentResponse) GetBool(field string) (bool, bool) {
	raw, ok := d[field]
	if !ok {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

// SubmitGovernanceEnvelope posts a GovernanceEnvelope proto to /api/v1/governance/envelopes.
func (c *E2EClient) SubmitGovernanceEnvelope(ctx context.Context, env *governance.GovernanceEnvelope) (string, int, []byte, error) {
	wire, err := protojson.Marshal(env)
	if err != nil {
		return env.Id, 0, nil, fmt.Errorf("marshal governance envelope: %w", err)
	}

	req, err := c.newAuthenticatedRequest(ctx, http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(wire))
	if err != nil {
		return env.Id, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.mtlsClient.Do(req)
	if err != nil {
		return env.Id, 0, nil, fmt.Errorf("submit governance envelope: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return env.Id, resp.StatusCode, nil, fmt.Errorf("read governance envelope response: %w", err)
	}
	return env.Id, resp.StatusCode, body, nil
}

// GetDocument retrieves a document from /api/v1/data/{collection}/{id}.
func (c *E2EClient) GetDocument(ctx context.Context, collection, documentID string) (DocumentResponse, int, error) {
	path := constants.APIPaths.DataPrefix + collection + "/" + documentID
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.mtlsClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("get document %s/%s: %w", collection, documentID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read get document response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, http.StatusNotFound, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("get document status %d: %s", resp.StatusCode, truncateBody(body))
	}
	var doc DocumentResponse
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode document response: %w", err)
	}
	return doc, resp.StatusCode, nil
}

// GetStateRoot fetches the current state_merkle_root from /state on the gateway.
func (c *E2EClient) GetStateRoot(ctx context.Context) (string, error) {
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, constants.APIPaths.State, nil)
	if err != nil {
		return "", err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("fetch state root: %w", err)
	}
	var h struct {
		StateMerkleRoot string `json:"state_merkle_root"`
		StateRoot       string `json:"state_root"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return "", fmt.Errorf("decode state root response: %w", err)
	}
	if h.StateMerkleRoot != "" {
		return h.StateMerkleRoot, nil
	}
	return h.StateRoot, nil
}

// newE2EClient constructs an E2EClient from the resolved e2eConfig. It loads
// the owner CLI certificate and key from disk, reads the CA bundle from the
// runtime tree, and builds strict mTLS HTTP clients using ServerName derived
// from the validated HTTPS URL. No InsecureSkipVerify — normal Go TLS hostname
// and chain verification is used.
func newE2EClient(ctx context.Context, cfg *e2eConfig) (*E2EClient, error) {
	cliCert, err := auth.LoadCLICertificate(cfg.fileSvc, cfg.cfg)
	if err != nil {
		return nil, fmt.Errorf("e2e: load CLI key pair: %w", err)
	}

	caBundle, err := cfg.readCABundle(ctx)
	if err != nil {
		return nil, fmt.Errorf("e2e: read CA bundle: %w", err)
	}

	caPool, err := parseCAPool(caBundle)
	if err != nil {
		return nil, fmt.Errorf("e2e: parse CA bundle: %w", err)
	}

	serverName, err := extractServerName(cfg.gatewayHTTPSURL)
	if err != nil {
		return nil, fmt.Errorf("e2e: extract server name: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates:     []tls.Certificate{cliCert},
		RootCAs:          caPool,
		ServerName:       serverName,
		MinVersion:       tls.VersionTLS13,
		CurvePreferences: certs.FIPSCurvePreferences(),
	}

	mtlsClient := &http.Client{
		Transport: httpclient.NewIPv4Transport(tlsConfig),
		Timeout:   defaultClientTimeout,
	}

	publicTLSConfig := &tls.Config{
		RootCAs:          caPool,
		ServerName:       serverName,
		MinVersion:       tls.VersionTLS13,
		CurvePreferences: certs.FIPSCurvePreferences(),
	}

	publicClient := &http.Client{
		Transport: httpclient.NewIPv4Transport(publicTLSConfig),
		Timeout:   defaultClientTimeout,
	}

	return &E2EClient{
		publicClient:      publicClient,
		mtlsClient:        mtlsClient,
		cliSessionID:      cfg.cliSessionID,
		userID:            cfg.userID,
		operatorID:        cfg.operatorID,
		operatorSessionID: cfg.operatorSessionID,
		gatewayHTTPS:      cfg.gatewayHTTPSURL,
	}, nil
}
