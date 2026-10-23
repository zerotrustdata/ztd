// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TestGatewayAuditAttribution keeps caller attribution separate from the
// embedded Operator that executes Gateway-local calls.
func newAuditAttributionGateway(t *testing.T) (*GatewayService, *auditAttributionCaptureProcessor) {
	t.Helper()
	processor := &fakeEnvelopeProcessor{
		receipt: &operatorv1.ActionReceipt{
			TransactionId: "tx-audit-1",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		},
	}
	capture := &auditAttributionCaptureProcessor{delegate: processor}
	return newTestGatewayService(t, withEnvProc(capture)), capture
}

func TestGatewayAuditAttribution(t *testing.T) {
	t.Parallel()

	t.Run("attribution from operator session context", func(t *testing.T) {
		t.Parallel()
		g, capture := newAuditAttributionGateway(t)
		opID := "op-123"
		opSessionID := "opsess-456"

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sys_info","arguments":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))

		// Simulate what the auth middleware does
		ctx := req.Context()
		ctx = context.WithValue(ctx, constants.ContextKeyOperatorID, opID)
		ctx = context.WithValue(ctx, constants.ContextKeyOperatorSessionID, opSessionID)
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		g.HandleMCP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, capture.lastEnvelope)
		assert.Equal(t, string(constants.DocIDEmbeddedOperator), capture.lastEnvelope.OperatorId)
		assert.Equal(t, opSessionID, capture.lastEnvelope.OperatorSessionId)
	})

	t.Run("attribution from app identity context (priority)", func(t *testing.T) {
		t.Parallel()
		g, capture := newAuditAttributionGateway(t)
		appID := "spiffe://ztd.local/app/my-app"
		opID := "op-123"
		opSessionID := "opsess-456"

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sys_info","arguments":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))

		// Simulate app auth
		ctx := req.Context()
		ctx = context.WithValue(ctx, constants.ContextKeyAppID, appID)
		ctx = context.WithValue(ctx, constants.ContextKeyOperatorID, opID)
		ctx = context.WithValue(ctx, constants.ContextKeyOperatorSessionID, opSessionID)
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		g.HandleMCP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, capture.lastEnvelope)
		assert.Equal(t, string(constants.DocIDEmbeddedOperator), capture.lastEnvelope.OperatorId)
		assert.Equal(t, appID, capture.lastEnvelope.ActingAppId)
		// The app's own context session is ignored; the envelope carries the
		// claimed embedded Operator's session, which executes it.
		assert.Equal(t, testEmbeddedSessionID, capture.lastEnvelope.OperatorSessionId)
	})

	t.Run("attribution empty when no context present", func(t *testing.T) {
		t.Parallel()
		g, capture := newAuditAttributionGateway(t)
		reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sys_info","arguments":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))

		w := httptest.NewRecorder()
		g.HandleMCP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, capture.lastEnvelope)
		assert.Equal(t, string(constants.DocIDEmbeddedOperator), capture.lastEnvelope.OperatorId)
		assert.Empty(t, capture.lastEnvelope.OperatorSessionId)
	})
}

// auditAttributionCaptureProcessor stores the last envelope for audit attribution testing
type auditAttributionCaptureProcessor struct {
	delegate     governance.EnvelopeProcessor
	lastEnvelope *commonv1.GovernanceEnvelope
}

func (e *auditAttributionCaptureProcessor) ProcessEnvelope(ctx context.Context, payload []byte) (*operatorv1.ActionReceipt, error) {
	var envelope commonv1.GovernanceEnvelope
	if err := protojson.Unmarshal(payload, &envelope); err != nil {
		// Log the error for debugging
		fmt.Printf("DEBUG: Failed to unmarshal envelope: %v\nPayload: %s\n", err, string(payload))
	} else {
		e.lastEnvelope = &envelope
	}
	return e.delegate.ProcessEnvelope(ctx, payload)
}
