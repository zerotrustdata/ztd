// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

func postProducer(t *testing.T, h http.Handler, path string, cert *x509.Certificate, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if cert != nil {
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func liveAssignmentBody(webSessionID string, terminal models.TerminalOutcomeStatus) models.ObserveProducerAssignmentRequest {
	return models.ObserveProducerAssignmentRequest{
		SchemaVersion:  constants.ObserveEventPayloadSchemaVersion,
		CycleID:        "run-live-1",
		CampaignID:     "campaign-live",
		AssignmentID:   "assignment-live-1",
		VariantID:      "variant-live",
		Role:           models.ModelRolePrimary,
		TaskID:         "scenario-live",
		Repetition:     1,
		TerminalStatus: terminal,
		WebSessionID:   webSessionID,
	}
}

func liveSSEPayload(t *testing.T, row models.SSEEventRow, into any) {
	t.Helper()
	var push models.SSEPushPayload
	require.NoError(t, json.Unmarshal([]byte(row.Payload), &push))
	var env sseEventEnvelope
	require.NoError(t, json.Unmarshal(push.Event, &env))
	require.NoError(t, json.Unmarshal(env.Data, into))
	assert.NotContains(t, string(env.Data), "user_id", "ownership is routing only")
}

// TestObserveProducerEndpoint_LiveEvalEvents posts an assignment start, a
// fan-out and the completion through the mTLS router and reads each event back
// from the SSE store, in order, routed to the caller's session.
func TestObserveProducerEndpoint_LiveEvalEvents(t *testing.T) {
	env := setupObserveProducerEndpointEnv(t)
	userID := "user-endpoint-live"
	seedActiveUser(t, env.infra, userID)
	seedAppPolicy(t, env.infra)
	webSessionID := seedWebSession(t, env.infra, userID)
	cert := appUserMTLSCert(t, userID)

	rr := postProducer(t, env.handler, constants.APIPaths.ObserveProducerAssignmentStarted, cert, liveAssignmentBody(webSessionID, ""))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = postProducer(t, env.handler, constants.APIPaths.ObserveProducerToolCallFannedOut, cert, models.ObserveProducerToolCallFannedOutRequest{
		SchemaVersion: constants.ObserveEventPayloadSchemaVersion, CycleID: "run-live-1", CampaignID: "campaign-live", AssignmentID: "assignment-live-1",
		CallID: "tool-call-1", GroupID: "tool-call-1", ToolName: constants.MCPToolRunShellCommand, Targets: 3, Succeeded: 2, Concurrency: 3, DurationMS: 1200,
		WebSessionID: webSessionID,
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = postProducer(t, env.handler, constants.APIPaths.ObserveProducerAssignmentCompleted, cert, liveAssignmentBody(webSessionID, models.TerminalOutcomeStatusFailed))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rows, err := env.infra.SSEStore.SSEEventsListSince(SSERoute{UserID: userID, WebSessionID: webSessionID}, 0, 100)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, string(constants.EventAiEvalAssignmentStarted), rows[0].EventType)
	assert.Equal(t, string(constants.EventAiEvalToolCallFannedOut), rows[1].EventType)
	assert.Equal(t, string(constants.EventAiEvalAssignmentCompleted), rows[2].EventType)

	var fanOut models.EvalToolCallFannedOutPayload
	liveSSEPayload(t, rows[1], &fanOut)
	assert.Equal(t, "tool-call-1", fanOut.GroupID)
	assert.Equal(t, 3, fanOut.Targets)
	assert.Equal(t, 2, fanOut.Succeeded)
	assert.Equal(t, int64(1200), fanOut.DurationMS)
	assert.NotEmpty(t, fanOut.EventID)

	var completed models.EvalAssignmentCompletedPayload
	liveSSEPayload(t, rows[2], &completed)
	assert.Equal(t, models.TerminalOutcomeStatusFailed, completed.TerminalStatus)

	doc, err := env.infra.DocStore.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionObserveAssignmentProgress), liveProjectionID(userID, "assignment-live-1"))
	require.NoError(t, err)
	require.NotNil(t, doc)
	var projected models.AssignmentProgressProjection
	require.NoError(t, doc.DecodeData(&projected))
	assert.Equal(t, models.AssignmentProgressStatusFailed, projected.Status)
}

// TestObserveProducerEndpoint_LiveEvalEventsRejectInvalidRequests refuses a
// malformed live event with its typed code and emits nothing.
func TestObserveProducerEndpoint_LiveEvalEventsRejectInvalidRequests(t *testing.T) {
	env := setupObserveProducerEndpointEnv(t)
	userID := "user-endpoint-live-invalid"
	seedActiveUser(t, env.infra, userID)
	seedAppPolicy(t, env.infra)
	webSessionID := seedWebSession(t, env.infra, userID)
	cert := appUserMTLSCert(t, userID)

	noGroup := models.ObserveProducerToolCallFannedOutRequest{SchemaVersion: constants.ObserveEventPayloadSchemaVersion, CampaignID: "c", AssignmentID: "a", WebSessionID: webSessionID}
	tooMany := noGroup
	tooMany.GroupID, tooMany.Targets, tooMany.Succeeded = "g", 1, 2
	for name, tc := range map[string]struct {
		path string
		body any
		code models.ObserveProducerRejectionCode
	}{
		"a start with a terminal status":             {constants.APIPaths.ObserveProducerAssignmentStarted, liveAssignmentBody(webSessionID, models.TerminalOutcomeStatusCompleted), models.ObserveProducerRejectionTerminalStatusInvalid},
		"a completion without one":                   {constants.APIPaths.ObserveProducerAssignmentCompleted, liveAssignmentBody(webSessionID, ""), models.ObserveProducerRejectionTerminalStatusInvalid},
		"a fan-out without a group":                  {constants.APIPaths.ObserveProducerToolCallFannedOut, noGroup, models.ObserveProducerRejectionGroupIDRequired},
		"a fan-out with more successes than targets": {constants.APIPaths.ObserveProducerToolCallFannedOut, tooMany, models.ObserveProducerRejectionCompletedExceedsTotal},
	} {
		t.Run(name, func(t *testing.T) {
			rr := postProducer(t, env.handler, tc.path, cert, tc.body)
			require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
			var rejection models.ObserveProducerRejection
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rejection))
			assert.Equal(t, tc.code, rejection.Code)
		})
	}
	rows, err := env.infra.SSEStore.SSEEventsListSince(SSERoute{UserID: userID, WebSessionID: webSessionID}, 0, 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestObserveProducerEndpoint_LiveEvalEventsNeedAnAppWorkload keeps the new
// producer paths closed to a CLI certificate.
func TestObserveProducerEndpoint_LiveEvalEventsNeedAnAppWorkload(t *testing.T) {
	env := setupObserveProducerEndpointEnv(t)
	userID := "user-endpoint-live-cli"
	seedActiveUser(t, env.infra, userID)
	seedAppPolicy(t, env.infra)
	cert := cliMTLSCert(t, userID, "cli-session-live")
	for _, path := range []string{constants.APIPaths.ObserveProducerAssignmentStarted, constants.APIPaths.ObserveProducerAssignmentCompleted, constants.APIPaths.ObserveProducerToolCallFannedOut} {
		rr := postProducer(t, env.handler, path, cert, liveAssignmentBody("web-1", ""))
		assert.Equal(t, http.StatusUnauthorized, rr.Code, path)
	}
}
