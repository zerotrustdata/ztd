//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const plantedSSN = "123-45-6789"

func newNativeAuditGateway(t *testing.T, scrubber *scrubbing.ScrubbingService, recorder AuditEventRecorder) *GatewayService {
	t.Helper()
	rec := sampleReceiptRecord("tx-egress-1", "sess-1", "FILE_EDIT", time.Now().UTC(), operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED)
	rec.ResultSummary = "leaked " + plantedSSN
	stub := &stubEventReceiptQuery{getByID: map[string]*models.EventReceiptRecord{"tx-egress-1": rec}}

	g := newTestGatewayService(t, withScrubbingService(scrubber), withEventsStore(recorder))
	g.nativeToolHandler.SetEventReceiptQuery(stub)
	return g
}

// withFieldRegistry gives the test gateway the production field path registry that read_field requires.
func withFieldRegistry(t *testing.T) testGatewayOption {
	t.Helper()
	registry, err := NewFieldPathRegistry(slog.Default())
	require.NoError(t, err)
	return func(g *GatewayService) { g.fieldPathRegistry = registry }
}

func downstreamServer(t *testing.T, text string) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(JSONRPCResponse{JSONRPC: "2.0", ID: 1, Result: mustMarshal(t, CallToolResult{Content: []TextContent{{Type: "text", Text: text}}})})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Every branch of DispatchToDownstream scrubs unconditionally: a native tool result, a
// read_field result and a downstream MCP result never leave this layer raw.
func TestDispatchToDownstream_ScrubsEveryBranch(t *testing.T) {
	scrubber := newTestScrubbingService(t)
	args := mustMarshal(t, EventReceiptGetRequest{TransactionID: "tx-egress-1"})

	t.Run("native tool", func(t *testing.T) {
		g := newNativeAuditGateway(t, scrubber, &recordingAuditEventRecorder{})
		out, err := g.DispatchToDownstream(context.Background(), "event_receipt_get", args, "sess-1")
		require.NoError(t, err)
		assert.NotContains(t, out, plantedSSN)
		assert.Contains(t, out, "tx-egress-1")
	})

	t.Run("downstream MCP", func(t *testing.T) {
		srv := downstreamServer(t, "record "+plantedSSN)
		g := newTestGatewayService(t, withDownstreamURL(srv.URL), withScrubbingService(scrubber))
		out, err := g.DispatchToDownstream(context.Background(), "ext-tool", json.RawMessage(`{}`), "sess-1")
		require.NoError(t, err)
		assert.NotContains(t, out, plantedSSN)
	})

	t.Run("read_field", func(t *testing.T) {
		g := newTestGatewayService(t, withScrubbingService(scrubber), withFieldRegistry(t),
			withDBService(&integrationTestDBService{data: map[string]map[string]interface{}{
				"investigations": {"investigation-123": map[string]interface{}{"status": "contact " + plantedSSN}},
			}}),
			withSessionValidator(&integrationTestSessionValidator{validSessions: map[string]bool{"sess-1": true}}))
		out, err := g.DispatchToDownstream(context.Background(), "read_field", mustMarshal(t, map[string]string{
			"collection": "investigations", "document_id": "investigation-123", "field_path": "status", "operator_session_id": "sess-1",
		}), "sess-1")
		require.NoError(t, err)
		assert.NotContains(t, out, plantedSSN)

		withoutScrubber := newTestGatewayService(t, withScrubbingService(nil), withFieldRegistry(t),
			withDBService(&integrationTestDBService{data: map[string]map[string]interface{}{
				"investigations": {"investigation-123": map[string]interface{}{"status": "contact " + plantedSSN}},
			}}),
			withSessionValidator(&integrationTestSessionValidator{validSessions: map[string]bool{"sess-1": true}}))
		out, err = withoutScrubber.DispatchToDownstream(context.Background(), "read_field", mustMarshal(t, map[string]string{
			"collection": "investigations", "document_id": "investigation-123", "field_path": "status", "operator_session_id": "sess-1",
		}), "sess-1")
		require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
		assert.Empty(t, out)
	})
}

// With no active scrubber nothing is returned: the call fails closed with the typed sentinel.
func TestDispatchToDownstream_NoScrubberWithholdsOutput(t *testing.T) {
	disabled, err := scrubbing.NewScrubbingService(context.Background(), &scrubbing.Config{Enabled: false}, slog.Default(), nil)
	require.NoError(t, err)
	args := mustMarshal(t, EventReceiptGetRequest{TransactionID: "tx-egress-1"})

	for name, scrubber := range map[string]*scrubbing.ScrubbingService{"nil scrubber": nil, "disabled scrubber": disabled} {
		t.Run(name+" native tool", func(t *testing.T) {
			g := newNativeAuditGateway(t, scrubber, &recordingAuditEventRecorder{})
			out, err := g.DispatchToDownstream(context.Background(), "event_receipt_get", args, "sess-1")
			require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
			assert.Empty(t, out)
		})

		t.Run(name+" downstream MCP", func(t *testing.T) {
			srv := downstreamServer(t, "record "+plantedSSN)
			recorder := &recordingAuditEventRecorder{}
			g := newTestGatewayService(t, withDownstreamURL(srv.URL), withScrubbingService(scrubber), withEventsStore(recorder))
			out, err := g.DispatchToDownstream(context.Background(), "ext-tool", json.RawMessage(`{}`), "sess-1")
			require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
			assert.Empty(t, out)
			assert.Empty(t, recorder.events, "no audit event is written with an unscrubbed result")
		})
	}
}

// The downstream call has already run when its audit event is recorded, so a record failure
// marks the result FAILED instead of returning output with no audit record behind it.
func TestDispatchToDownstream_AuditRecordFailureFailsTheResult(t *testing.T) {
	srv := downstreamServer(t, "harmless output")
	cause := constants.ErrEventsStoreSessionUnknown
	g := newTestGatewayService(t, withDownstreamURL(srv.URL), withEventsStore(failingAuditEventRecorder{err: cause}))

	out, err := g.DispatchToDownstream(context.Background(), "ext-tool", json.RawMessage(`{}`), "sess-1")
	require.ErrorIs(t, err, constants.ErrGatewayDownstreamAuditFailed)
	require.ErrorIs(t, err, cause, "the record failure cause is preserved")
	assert.Empty(t, out)
}
