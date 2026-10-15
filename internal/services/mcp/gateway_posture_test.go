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
func postureDependencies(t *testing.T, posture string) Dependencies {
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
	cases := []struct {
		name    string
		posture string
	}{
		{name: "empty posture", posture: ""},
		{name: "unrecognized posture", posture: "bogus"},
		{name: "posture names are case sensitive", posture: "Notary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := NewGatewayService(postureDependencies(t, tc.posture))
			require.ErrorIs(t, err, constants.ErrInvalidPosture)
			require.Nil(t, g)
		})
	}
}

func TestNewGatewayService_StoresTypedPostureNamedByConfiguration(t *testing.T) {
	for _, name := range []string{constants.PostureDoctrine, constants.PostureConsensus, constants.PostureRatify, constants.PostureNotary} {
		t.Run(name, func(t *testing.T) {
			g, err := NewGatewayService(postureDependencies(t, name))
			require.NoError(t, err)
			require.Equal(t, name, g.posture.Name())
		})
	}
}

// TestProcessGatewayTransaction_DeliberatesOnlyWhereL2Enforced proves the Gateway asks for L2 votes exactly
// where governance.L2Enforced gates the action: consensus and notary for MCP calls, and never for a
// bootstrap action, which predates the consensus tribunal.
func TestProcessGatewayTransaction_DeliberatesOnlyWhereL2Enforced(t *testing.T) {
	cases := []struct {
		name      string
		posture   governance.GovernancePosture
		eventType constants.EventType
		wantCalls int
	}{
		{name: "doctrine does not deliberate MCP calls", posture: &governance.DoctrinePosture{}, eventType: constants.Event.Operator.Mcp.CallRequested, wantCalls: 0},
		{name: "ratify does not deliberate MCP calls", posture: &governance.RatifyPosture{}, eventType: constants.Event.Operator.Mcp.CallRequested, wantCalls: 0},
		{name: "consensus deliberates MCP calls", posture: &governance.ConsensusPosture{}, eventType: constants.Event.Operator.Mcp.CallRequested, wantCalls: 1},
		{name: "notary deliberates MCP calls", posture: &governance.NotaryPosture{}, eventType: constants.Event.Operator.Mcp.CallRequested, wantCalls: 1},
		{name: "notary does not deliberate platform enrollment bootstrap", posture: &governance.NotaryPosture{}, eventType: constants.EventPlatformEnrollmentCreateRequested, wantCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deliberator := &countingL2Deliberator{}
			g := newTestGatewayService(t)
			g.posture = tc.posture
			g.l2ConsensusDeliberator = deliberator
			ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, "operator-session-1")

			_, wire, _, err := g.processGatewayTransaction(ctx, processGatewayOptions{eventType: tc.eventType, targetResource: "tool", payloadBytes: []byte("payload")})
			require.NoError(t, err)

			env := &commonv1.GovernanceEnvelope{}
			require.NoError(t, protojson.Unmarshal(wire, env))
			assert.Equal(t, tc.posture.Name(), env.Posture, "the envelope is stamped with the typed posture's name")
			assert.Equal(t, tc.wantCalls, deliberator.calls)
		})
	}
}

// TestGatewayService_SuspensionMessageNamesTypedPosture proves the approval pause reads the typed posture,
// so the instruction returned to the calling agent names the posture the Gateway is actually running.
func TestGatewayService_SuspensionMessageNamesTypedPosture(t *testing.T) {
	g := newTestGatewayService(t,
		withEnvProc(&fakeEnvelopeProcessor{err: governance.ErrL3ProofMissing}),
		withSuspendedStore(&fakeSuspendedStore{}))
	g.posture = &governance.NotaryPosture{}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{}}}`))
	req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyOperatorSessionID, "operator-session-1"))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Contains(t, w.Body.String(), "running in notary posture")
}
