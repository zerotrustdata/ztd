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
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/httpclient"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// maxListResponseBytes bounds the operator registry listing, which grows with
// fleet size (each Operator document carries its latest heartbeat) and with
// retained stopped or stale registrations. A 500-Operator fleet alone exceeds
// maxResponseBytes.
const maxListResponseBytes = 64 << 20

// E2EClient owns bounded public and mTLS HTTP clients for communicating with
// a running production platform. It has no deployment-specific fields, no test framework
// dependency, no bootstrap mutation, and no filesystem writes. Each request
// accepts a context, uses constants from constants.APIPaths, applies the CLI
// session header when authenticated, limits response reads, checks status
// codes, decodes a typed response, and returns contextual errors.
type E2EClient struct {
	publicClient      *http.Client // no client cert, for health/CA bundle endpoints
	mtlsClient        *http.Client // owner CLI cert, for authenticated endpoints
	cliSessionID      string
	userID            string
	operatorID        string
	operatorSessionID string
	gatewayHTTPS      string
}

// newAuthenticatedRequest builds an HTTP request with the CLI session header
// set, targeting the gateway HTTPS URL. The body parameter may be nil for GET
// requests.
func (c *E2EClient) newAuthenticatedRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	reqURL := c.gatewayHTTPS + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set(constants.HeaderCLISessionID, c.cliSessionID)
	return req, nil
}

// SubmitGovernanceEnvelope posts a GovernanceEnvelope to
// /api/v1/governance/envelopes and returns the signed EventReceipt the Gateway
// answers with. Any status other than 200 (an IngressRefusal or a
// GovernanceRejection) is an error carrying the response body.
func (c *E2EClient) SubmitGovernanceEnvelope(ctx context.Context, env *governance.GovernanceEnvelope) (*operatorv1.EventReceipt, error) {
	wire, err := protojson.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal governance envelope: %w", err)
	}
	req, err := c.newAuthenticatedRequest(ctx, http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("submit governance envelope %s: %w", env.GetId(), err)
	}
	receipt := &operatorv1.EventReceipt{}
	if err := protojson.Unmarshal(body, receipt); err != nil {
		return nil, fmt.Errorf("decode governance envelope receipt: %w", err)
	}
	return receipt, nil
}

// GetDocument reads a document from /api/v1/data/{collection}/{id} and returns
// its wire JSON for the caller to decode into the shape it wrote. A document
// the Gateway does not hold returns constants.ErrNotFound.
func (c *E2EClient) GetDocument(ctx context.Context, collection, documentID string) ([]byte, error) {
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, constants.APIPaths.DataPrefix+collection+"/"+documentID, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("get document %s/%s: %w", collection, documentID, constants.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get document %s/%s: %w", collection, documentID, err)
	}
	return body, nil
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
	state, err := decodeJSON[models.StateResponse](body, "state root response")
	if err != nil {
		return "", err
	}
	if state.StateMerkleRoot == "" {
		return "", fmt.Errorf("fetch state root: state_merkle_root: %w", constants.ErrMissingRequiredField)
	}
	return state.StateMerkleRoot, nil
}

// newE2EClient constructs an E2EClient from the resolved e2eConfig. The mTLS
// client is the CLI's own auth.BuildMTLSClient (owner certificate plus the
// runtime trust bundle); the public client trusts the same bundle without a
// client certificate. Both verify the chain and the gateway hostname from the
// request URL — there is no InsecureSkipVerify.
func newE2EClient(cfg *e2eConfig) (*E2EClient, error) {
	mtlsClient, err := auth.BuildMTLSClient(cfg.fileSvc, cfg.cfg, defaultClientTimeout)
	if err != nil {
		return nil, fmt.Errorf("e2e: build mTLS client: %w", err)
	}

	caBundle, err := auth.ReadTrustBundle(cfg.fileSvc, cfg.cfg)
	if err != nil {
		return nil, fmt.Errorf("e2e: read CA bundle: %w", err)
	}
	caPool, err := parseCAPool(caBundle)
	if err != nil {
		return nil, fmt.Errorf("e2e: parse CA bundle: %w", err)
	}
	publicClient := &http.Client{
		Transport: httpclient.NewIPv4Transport(&tls.Config{
			RootCAs:          caPool,
			MinVersion:       tls.VersionTLS13,
			CurvePreferences: certs.FIPSCurvePreferences(),
		}),
		Timeout: defaultClientTimeout,
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
