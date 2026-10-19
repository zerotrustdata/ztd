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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// GetHealth fetches the gateway health endpoint over HTTP (no mTLS) and
// returns the typed HealthResponse.
func (c *E2EClient) GetHealth(ctx context.Context, httpURL string) (models.HealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+constants.APIPaths.Health, nil)
	if err != nil {
		return models.HealthResponse{}, fmt.Errorf("build health request: %w", err)
	}
	body, _, err := doRequest(c.publicClient, req, http.StatusOK)
	if err != nil {
		return models.HealthResponse{}, fmt.Errorf("health check: %w", err)
	}
	return decodeJSON[models.HealthResponse](body, "health response")
}

// GetCABundle fetches the CA bundle from the gateway's well-known PKI endpoint
// over HTTP (no mTLS). Returns the raw PEM bytes.
func (c *E2EClient) GetCABundle(ctx context.Context, httpURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+constants.APIPaths.WellKnownPKICABundle, nil)
	if err != nil {
		return nil, fmt.Errorf("build CA bundle request: %w", err)
	}
	body, _, err := doRequest(c.publicClient, req, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("fetch CA bundle: %w", err)
	}
	return body, nil
}

// GetPendingEnrollments fetches the authenticated pending platform enrollment
// list and returns the typed response.
func (c *E2EClient) GetPendingEnrollments(ctx context.Context) (models.PlatformEnrollmentPendingResponse, error) {
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, constants.APIPaths.AuthPlatformEnrollmentPending, nil)
	if err != nil {
		return models.PlatformEnrollmentPendingResponse{}, err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return models.PlatformEnrollmentPendingResponse{}, fmt.Errorf("fetch pending enrollments: %w", err)
	}
	return decodeJSON[models.PlatformEnrollmentPendingResponse](body, "pending enrollments")
}

// GetPendingRaw fetches the authenticated pending platform enrollment list and
// returns the raw JSON body as a string. Used by tests that verify the wire
// payload does not contain secret fields.
func (c *E2EClient) GetPendingRaw(ctx context.Context) (string, error) {
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, constants.APIPaths.AuthPlatformEnrollmentPending, nil)
	if err != nil {
		return "", err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("fetch pending raw: %w", err)
	}
	return string(body), nil
}

// PostEnrollmentDecision posts an approve or deny decision for the given
// request ID via the authenticated decision endpoint.
func (c *E2EClient) PostEnrollmentDecision(ctx context.Context, requestID string, decision models.PlatformEnrollmentDecision) error {
	body, err := json.Marshal(models.PlatformEnrollmentDecisionRequest{
		RequestID: requestID,
		Decision:  decision,
	})
	if err != nil {
		return fmt.Errorf("marshal decision body: %w", err)
	}
	req, err := c.newAuthenticatedRequest(ctx, http.MethodPost, constants.APIPaths.AuthPlatformEnrollmentDecision, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	_, _, err = doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return fmt.Errorf("post enrollment decision: %w", err)
	}
	return nil
}

// ApproveEnrollment posts an approve decision for the given request ID.
func (c *E2EClient) ApproveEnrollment(ctx context.Context, requestID string) error {
	return c.PostEnrollmentDecision(ctx, requestID, models.PlatformEnrollmentDecisionApprove)
}

// DenyEnrollment posts a deny decision for the given request ID.
func (c *E2EClient) DenyEnrollment(ctx context.Context, requestID string) error {
	return c.PostEnrollmentDecision(ctx, requestID, models.PlatformEnrollmentDecisionDeny)
}

// ListOperators fetches the operator list via the owner-authenticated operators
// endpoint and returns the typed slot response. The gateway requires the
// owner's user_id as a query parameter to scope the operator list to the
// authenticated user.
func (c *E2EClient) ListOperators(ctx context.Context) (models.OperatorSlotResponse, error) {
	path := constants.APIPaths.Operators + "?user_id=" + c.userID
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return models.OperatorSlotResponse{}, err
	}
	body, _, err := doRequestLimit(c.mtlsClient, req, http.StatusOK, maxListResponseBytes)
	if err != nil {
		return models.OperatorSlotResponse{}, fmt.Errorf("list operators: %w", err)
	}
	return decodeJSON[models.OperatorSlotResponse](body, "operator list")
}

// findLiveActiveRemoteOperator returns the active remote operator with the most
// recent last_heartbeat_at among operators reporting heartbeat telemetry.
// Multi-operator stacks may retain stale active registrations from prior
// sessions; heartbeat assertions must target a live operator rather than the
// first list entry. When several operators share a fresh last_heartbeat_at
// window, prefer the one whose
// CurrentHostname differs from enrollment Name — that signals canonical
// heartbeat telemetry rather than stale enrollment metadata.
func findLiveActiveRemoteOperator(operators []*operatorv1.OperatorDocument) *operatorv1.OperatorDocument {
	var candidates []*operatorv1.OperatorDocument
	for i := range operators {
		op := operators[i]
		if op.Status != string(constants.OperatorStatusActive) || op.OperatorType != string(constants.OperatorTypeRemote) {
			continue
		}
		if op.CurrentHostname == "" && op.LatestHeartbeatSnapshot == nil {
			continue
		}
		candidates = append(candidates, op)
	}
	if len(candidates) == 0 {
		var fallback *operatorv1.OperatorDocument
		for i := range operators {
			op := operators[i]
			if op.Status != string(constants.OperatorStatusActive) || op.OperatorType != string(constants.OperatorTypeRemote) {
				continue
			}
			if fallback == nil || lastSeen(op).After(lastSeen(fallback)) {
				fallback = op
			}
		}
		return fallback
	}

	best := candidates[0]
	for _, op := range candidates[1:] {
		if lastSeen(op).After(lastSeen(best)) {
			best = op
		}
	}

	const freshWindow = 90 * time.Second
	for _, op := range candidates {
		if lastSeen(best).Sub(lastSeen(op)) > freshWindow {
			continue
		}
		if op.CurrentHostname != "" && op.Name != "" && op.CurrentHostname != op.Name {
			return op
		}
	}
	return best
}

// lastSeen is the Gateway-clock last_heartbeat_at of op, or its claimed_at
// before the first heartbeat lands (the zero time when neither is set).
func lastSeen(op *operatorv1.OperatorDocument) time.Time {
	if ts := op.GetLastHeartbeatAt(); ts != nil {
		return ts.AsTime()
	}
	if ts := op.GetClaimedAt(); ts != nil {
		return ts.AsTime()
	}
	return time.Time{}
}

func findOperatorByID(operators []*operatorv1.OperatorDocument, id string) *operatorv1.OperatorDocument {
	for i := range operators {
		if operators[i].Id == id {
			return operators[i]
		}
	}
	return nil
}

// GetOperatorBySession fetches a single operator by session ID via the
// owner-authenticated session lookup endpoint and returns the typed response.
func (c *E2EClient) GetOperatorBySession(ctx context.Context, sessionID string) (models.OperatorResponse, error) {
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, constants.APIPaths.OperatorsSession+sessionID, nil)
	if err != nil {
		return models.OperatorResponse{}, err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return models.OperatorResponse{}, fmt.Errorf("get operator by session: %w", err)
	}
	return decodeJSON[models.OperatorResponse](body, "operator response")
}

// DispatchCommand sends a command dispatch request to the gateway via the
// owner-authenticated commands endpoint. The OperatorCommandRequest body carries
// the target operator session ID, action type, and protobuf payload. The 200
// body is a GovernedDispatchResult carrying the signed final receipt. The timeout parameter overrides the default client timeout for
// long-running dispatch operations.
func (c *E2EClient) DispatchCommand(ctx context.Context, body gateway.OperatorCommandRequest, timeout time.Duration) (*operatorv1.GovernedDispatchResult, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal dispatch body: %w", err)
	}
	req, err := c.newAuthenticatedRequest(ctx, http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := c.mtlsClient
	if timeout > 0 && timeout != defaultClientTimeout {
		client = &http.Client{
			Transport: client.Transport,
			Timeout:   timeout,
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute dispatch request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read dispatch response: %w", err)
	}
	if len(respBody) > maxResponseBytes {
		return nil, fmt.Errorf("dispatch response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dispatch status %d: %s", resp.StatusCode, truncateBody(respBody))
	}
	result := &operatorv1.GovernedDispatchResult{}
	if err := protojson.Unmarshal(respBody, result); err != nil {
		return nil, fmt.Errorf("decode dispatch response: %w", err)
	}
	return result, nil
}

// GetConsoleIndex fetches the Gateway-embedded console SPA index over HTTPS
// (no client certificate; the console route is public) and returns the raw
// body bytes.
func (c *E2EClient) GetConsoleIndex(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.gatewayHTTPS+constants.APIPaths.ConsolePrefix, nil)
	if err != nil {
		return nil, fmt.Errorf("build console index request: %w", err)
	}
	body, _, err := doRequest(c.publicClient, req, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("console index: %w", err)
	}
	return body, nil
}

// newBareMTLSRequest builds an HTTP GET request targeting the given URL
// without setting the CLI session header. It is used by tests that verify the
// session header is enforced: the mTLS handshake completes (the client cert
// is valid) but the authenticated route rejects the request for a missing
// header. The request is executed by the caller via the E2EClient's mtlsClient
// so the same strict TLS configuration applies.
func newBareMTLSRequest(ctx context.Context, reqURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build bare mTLS request: %w", err)
	}
	return req, nil
}

// GetAuditReceipts fetches the authenticated audit receipts list and returns
// the typed response up to the server default limit. Use GetActionReceipt for
// the canonical protobuf response selected by transaction ID.
func (c *E2EClient) GetAuditReceipts(ctx context.Context) (models.AuditReceiptsResponse, error) {
	path := constants.APIPaths.AuditReceipts
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return models.AuditReceiptsResponse{}, err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return models.AuditReceiptsResponse{}, fmt.Errorf("fetch audit receipts: %w", err)
	}
	return decodeJSON[models.AuditReceiptsResponse](body, "audit receipts")
}

// GetActionReceipt reads the bare canonical protobuf response selected by tx_id.
// The list endpoint returns an AuditReceiptsResponse wrapper instead.
func (c *E2EClient) GetActionReceipt(ctx context.Context, txID string) (*operatorv1.ActionReceipt, error) {
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, constants.APIPaths.AuditReceipts+"?tx_id="+txID, nil)
	if err != nil {
		return nil, err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("fetch canonical receipt: %w", err)
	}
	receipt := &operatorv1.ActionReceipt{}
	if err := protojson.Unmarshal(body, receipt); err != nil {
		return nil, fmt.Errorf("decode canonical receipt: %w", err)
	}
	return receipt, nil
}

// GetAuditSummary fetches the authenticated audit summary and returns the
// typed response with event and receipt counts broken down by type.
func (c *E2EClient) GetAuditSummary(ctx context.Context) (models.AuditSummaryResponse, error) {
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, constants.APIPaths.AuditSummary, nil)
	if err != nil {
		return models.AuditSummaryResponse{}, err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return models.AuditSummaryResponse{}, fmt.Errorf("fetch audit summary: %w", err)
	}
	return decodeJSON[models.AuditSummaryResponse](body, "audit summary")
}

// VerifyAuditChain fetches GET /api/v1/audit/verify and returns the typed response.
func (c *E2EClient) VerifyAuditChain(ctx context.Context, fromSeq int64) (models.AuditVerifyResponse, error) {
	path := constants.APIPaths.AuditVerify
	if fromSeq > 0 {
		path += fmt.Sprintf("?from_seq=%d", fromSeq)
	}
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return models.AuditVerifyResponse{}, err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return models.AuditVerifyResponse{}, fmt.Errorf("verify audit chain: %w", err)
	}
	return decodeJSON[models.AuditVerifyResponse](body, "audit verify")
}

// IngestAuditRecord posts an LFAA audit record to POST /api/v1/audit/records
// and returns the operator chain acknowledgement.
func (c *E2EClient) IngestAuditRecord(ctx context.Context, req models.AuditRecordIngestRequest) (models.AuditRecordIngestResponse, error) {
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return models.AuditRecordIngestResponse{}, fmt.Errorf("marshal audit ingest body: %w", err)
	}
	httpReq, err := c.newAuthenticatedRequest(ctx, http.MethodPost, constants.APIPaths.AuditRecords, bytes.NewReader(bodyBytes))
	if err != nil {
		return models.AuditRecordIngestResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	body, _, err := doRequest(c.mtlsClient, httpReq, http.StatusOK)
	if err != nil {
		return models.AuditRecordIngestResponse{}, fmt.Errorf("ingest audit record: %w", err)
	}
	return decodeJSON[models.AuditRecordIngestResponse](body, "audit ingest")
}

// DispatchCommandExpectStatus posts a dispatch request and returns the HTTP status
// and raw body without requiring success.
func (c *E2EClient) DispatchCommandExpectStatus(ctx context.Context, body gateway.OperatorCommandRequest, expectedStatus int) (int, []byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal dispatch body: %w", err)
	}
	req, err := c.newAuthenticatedRequest(ctx, http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	respBody, status, err := doRequest(c.mtlsClient, req, expectedStatus)
	return status, respBody, err
}

// GetAuditEvents fetches the authenticated audit events list and returns the
// typed response.
func (c *E2EClient) GetAuditEvents(ctx context.Context) (models.AuditEventsResponse, error) {
	req, err := c.newAuthenticatedRequest(ctx, http.MethodGet, constants.APIPaths.AuditEvents, nil)
	if err != nil {
		return models.AuditEventsResponse{}, err
	}
	body, _, err := doRequest(c.mtlsClient, req, http.StatusOK)
	if err != nil {
		return models.AuditEventsResponse{}, fmt.Errorf("fetch audit events: %w", err)
	}
	return decodeJSON[models.AuditEventsResponse](body, "audit events")
}

// dispatchFsRead discovers the live active remote operator, dispatches an
// FS_READ command for /etc/hostname once, and returns the dispatch response. It
// is the shared action used by tests that need a governed command to have
// executed before asserting on its consequences (audit receipts, compliance
// state). An operator the registry lists as active has established its command
// subscription, so the dispatch is not retried: a failed dispatch fails the
// test. A caller that has just enrolled the operator must first wait for its
// operator.status.updated event (e2eEventWaiters.awaitStatus). The caller owns
// the context; it must be called from a test goroutine.
func (c *E2EClient) dispatchFsRead(t *testing.T, ctx context.Context) *operatorv1.GovernedDispatchResult {
	t.Helper()

	operators, err := c.ListOperators(ctx)
	require.NoError(t, err, "operator list must succeed to discover the dispatch target")
	require.True(t, operators.Success, "operator list response must report success")
	require.NotEmpty(t, operators.Operators, "at least one operator must be registered")

	target := findLiveActiveRemoteOperator(operators.Operators)
	require.NotNil(t, target, "a live active remote operator must exist as the dispatch target")
	require.NotEmpty(t, target.OperatorSessionId, "target operator must have a session ID")
	t.Logf("dispatch target: id=%s session=%s hostname=%s", target.Id, target.OperatorSessionId, target.CurrentHostname)
	return c.dispatchFsReadToSession(t, ctx, target.OperatorSessionId)
}

// dispatchFsReadToSession dispatches one FS_READ command for /etc/hostname to
// the operator holding operatorSessionID and requires it to succeed. A 503
// ("delivered to no operator subscribers") is a failure: an operator announced
// active is subscribed to its command channel.
func (c *E2EClient) dispatchFsReadToSession(t *testing.T, ctx context.Context, operatorSessionID string) *operatorv1.GovernedDispatchResult {
	t.Helper()
	require.NotEmpty(t, operatorSessionID, "target operator session ID must be non-empty")

	payload, err := proto.Marshal(&operatorv1.FsReadRequested{Path: constants.PathEtcHostname})
	require.NoError(t, err, "marshal FsReadRequested payload")

	dispatchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := c.DispatchCommand(dispatchCtx, gateway.OperatorCommandRequest{
		TargetOperatorSessionID: operatorSessionID,
		EventType:               string(constants.EventOperatorFilesystemReadRequested),
		Payload:                 payload,
		TargetResource:          constants.PathEtcHostname,
	}, 60*time.Second)
	require.NoError(t, err, "dispatch to operator session %s must succeed", operatorSessionID)
	require.NotNil(t, resp.GetReceipt(), "dispatch to operator session %s must return the signed final receipt: %v", operatorSessionID, resp)
	return resp
}

// DiscoverPendingOperatorByHostname returns the first pending operator request
// whose Hostname or InstanceID contains the given substring. It reads the
// pending list once, then waits for an approvals.changed (enrollments) event
// before each further read. It is used by cross-enrollment tests to find the
// secondary gateway's pending operator request without duplicating the filter
// loop across tests. The caller owns the context and the event subscription
// (subscribeE2EEvents); it must be called from a test goroutine.
func (c *E2EClient) DiscoverPendingOperatorByHostname(t *testing.T, ctx context.Context, events *e2eEventWaiters, hostnameSubstring string) models.PlatformEnrollmentPendingRequest {
	t.Helper()
	for {
		pending, err := c.GetPendingEnrollments(ctx)
		require.NoError(t, err, "pending enrollment list must succeed")
		for _, r := range pending.Requests {
			if r.ComponentKind != models.PlatformComponentOperator {
				continue
			}
			if strings.Contains(r.Hostname, hostnameSubstring) || strings.Contains(r.InstanceID, hostnameSubstring) {
				return r
			}
		}
		events.awaitPending(ctx, t, fmt.Sprintf("a pending operator request matching %q", hostnameSubstring))
	}
}

// DiscoverActiveOperatorByName returns the first active operator whose Name
// contains the given substring. It reads the registry once, then waits for an
// operator.status.updated event before each further read, so active means the
// operator is subscribed to its command channel. It is used by cross-enrollment
// tests to find the secondary gateway's operator after approval. The caller
// owns the context and the event subscription (subscribeE2EEvents); it must be
// called from a test goroutine.
func (c *E2EClient) DiscoverActiveOperatorByName(t *testing.T, ctx context.Context, events *e2eEventWaiters, nameSubstring string) *operatorv1.OperatorDocument {
	t.Helper()
	for {
		operators, err := c.ListOperators(ctx)
		require.NoError(t, err, "operator list must succeed")
		for i := range operators.Operators {
			if operators.Operators[i].Status != string(constants.OperatorStatusActive) {
				continue
			}
			if strings.Contains(operators.Operators[i].Name, nameSubstring) {
				return operators.Operators[i]
			}
		}
		events.awaitStatus(ctx, t, fmt.Sprintf("an active operator matching %q", nameSubstring))
	}
}

// awaitRequestLeavesPending returns once requestID is absent from the pending
// enrollment list. It reads the list once, then waits for an approvals.changed
// (enrollments) event before each further read.
func awaitRequestLeavesPending(ctx context.Context, t *testing.T, events *e2eEventWaiters, requestID string) {
	t.Helper()
	for {
		pending, err := e2eClient.GetPendingEnrollments(ctx)
		require.NoError(t, err, "pending enrollment list must succeed")
		listed := false
		for _, r := range pending.Requests {
			if r.RequestID == requestID {
				listed = true
				break
			}
		}
		if !listed {
			return
		}
		events.awaitPending(ctx, t, fmt.Sprintf("request %s leaving the pending list", requestID))
	}
}
