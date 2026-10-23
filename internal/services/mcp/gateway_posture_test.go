// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// countingL2Deliberator counts L2 deliberation requests and returns the envelope unchanged,
// so a test can observe whether the Gateway asked for L2 votes without altering the envelope.
type countingL2Deliberator struct {
	calls int
}

func (d *countingL2Deliberator) Deliberate(_ context.Context, envelopeBytes []byte) ([]byte, error) {
	d.calls++
	return envelopeBytes, nil
}

// postureDependencies returns the minimum valid Dependencies for NewGatewayService with the given posture name.
func postureDependencies(t *testing.T, posture constants.Posture) Dependencies {
	t.Helper()
	return Dependencies{
		Logger:           slog.Default(),
		Responder:        response.NewWriter(slog.Default()),
		AuditStore:       noopAuditEventRecorder{},
		EmbeddedSession:  fakeEmbeddedSession{sessionID: testEmbeddedSessionID},
		ScrubbingService: newTestScrubbingService(t),
		Posture:          posture,
	}
}

func TestNewGatewayService_RejectsEmptyOrUnknownPosture(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		posture constants.Posture
	}{
		{name: "empty posture", posture: ""},
		{name: "unrecognized posture", posture: "bogus"},
		{name: "posture names are case sensitive", posture: "Notary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, err := NewGatewayService(postureDependencies(t, tc.posture))
			require.ErrorIs(t, err, constants.ErrInvalidPosture)
			require.Nil(t, g)
		})
	}
}

func TestNewGatewayService_StoresTypedPostureNamedByConfiguration(t *testing.T) {
	t.Parallel()
	for _, name := range []constants.Posture{constants.PostureDoctrine, constants.PostureConsensus, constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(name), func(t *testing.T) {
			t.Parallel()
			g, err := NewGatewayService(postureDependencies(t, name))
			require.NoError(t, err)
			require.Equal(t, name, g.posture)
		})
	}
}

// TestProcessGatewayTransaction_DeliberatesOnlyWhereL2Enforced proves the Gateway asks for L2 votes exactly
// where governance.L2Enforced gates the envelope: consensus and notary for state-changing MCP calls,
// never for a read-only tool, and never for a bootstrap action, which predates the consensus tribunal.
func TestProcessGatewayTransaction_DeliberatesOnlyWhereL2Enforced(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		posture   constants.Posture
		eventType constants.EventType
		target    string
		wantCalls int
	}{
		{name: "doctrine does not deliberate MCP calls", posture: constants.PostureDoctrine, eventType: constants.Event.Operator.Mcp.CallRequested, target: constants.MCPToolRunShellCommand, wantCalls: 0},
		{name: "ratify does not deliberate MCP calls", posture: constants.PostureRatify, eventType: constants.Event.Operator.Mcp.CallRequested, target: constants.MCPToolRunShellCommand, wantCalls: 0},
		{name: "consensus deliberates state-changing MCP calls", posture: constants.PostureConsensus, eventType: constants.Event.Operator.Mcp.CallRequested, target: constants.MCPToolRunShellCommand, wantCalls: 1},
		{name: "notary deliberates state-changing MCP calls", posture: constants.PostureNotary, eventType: constants.Event.Operator.Mcp.CallRequested, target: constants.MCPToolRunShellCommand, wantCalls: 1},
		{name: "consensus deliberates unknown MCP tools", posture: constants.PostureConsensus, eventType: constants.Event.Operator.Mcp.CallRequested, target: "tool", wantCalls: 1},
		{name: "consensus does not deliberate read-only MCP tools", posture: constants.PostureConsensus, eventType: constants.Event.Operator.Mcp.CallRequested, target: constants.MCPToolReadFile, wantCalls: 0},
		{name: "notary does not deliberate platform enrollment bootstrap", posture: constants.PostureNotary, eventType: constants.EventPlatformEnrollmentCreateRequested, target: "tool", wantCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deliberator := &countingL2Deliberator{}
			g := newTestGatewayService(t)
			g.posture = tc.posture
			g.l2ConsensusDeliberator = deliberator
			ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, "operator-session-1")

			_, wire, _, err := g.processGatewayTransaction(ctx, processGatewayOptions{eventType: tc.eventType, targetResource: tc.target, payloadBytes: []byte("payload")})
			require.NoError(t, err)

			env := &commonv1.GovernanceEnvelope{}
			require.NoError(t, protojson.Unmarshal(wire, env))
			assert.Equal(t, string(tc.posture), env.Posture, "the envelope is stamped with the typed posture's name")
			assert.Equal(t, tc.wantCalls, deliberator.calls)
		})
	}
}

// TestGatewayService_SuspensionMessageNamesTypedPosture proves the approval pause reads the typed posture,
// so the instruction returned to the calling agent names the posture the Gateway is actually running.
func TestGatewayService_SuspensionMessageNamesTypedPosture(t *testing.T) {
	t.Parallel()
	g := newTestGatewayService(t,
		withEnvProc(&fakeEnvelopeProcessor{err: governance.ErrL3ProofMissing}),
		withSuspendedStore(&fakeSuspendedStore{}))
	g.posture = constants.PostureNotary

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{}}}`))
	req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyOperatorSessionID, "operator-session-1"))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Contains(t, w.Body.String(), "runs under the notary posture")
}
