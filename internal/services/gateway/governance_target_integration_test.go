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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// foreignOperatorEnvelope builds the envelope of the live failure: addressed to
// a remote Operator under a session id the Gateway never started.
func foreignOperatorEnvelope(t *testing.T, svc *GatewayModeService, nonce string) *commonv1.GovernanceEnvelope {
	t.Helper()
	payload, err := proto.Marshal(&operatorv1.FsReadRequested{Path: constants.TestPathShortData, ExecutionId: nonce})
	require.NoError(t, err)
	env := &commonv1.GovernanceEnvelope{
		ProtocolVersion:   govtypes.GovernanceProtocolVersionV2,
		Timestamp:         timestamppb.Now(),
		ExpiresAt:         timestamppb.New(time.Now().Add(time.Minute)),
		OperatorId:        "foreign-outbound-operator",
		OperatorSessionId: "session-1",
		ActionType:        string(constants.ActionTypeFsRead),
		Payload:           payload,
		Nonce:             nonce,
	}
	eventType, err := constants.RequestEventForAction(constants.ActionTypeFsRead)
	require.NoError(t, err)
	env.EventType = string(eventType)
	env.StateMerkleRoot, err = svc.GetStateRootSvc().GetCurrentStateRoot(t.Context())
	require.NoError(t, err)
	env.Id, err = govtypes.GenerateMessageID(env)
	require.NoError(t, err)
	env.TransactionHash = env.Id
	return env
}

func submitForeignOperatorEnvelope(t *testing.T, svc *GatewayModeService, env *commonv1.GovernanceEnvelope) *httptest.ResponseRecorder {
	t.Helper()
	uri := "spiffe://ztd.local/operator/org-1/foreign-outbound-operator/session-1"
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(marshalEnvelope(t, env)))
	req.TLS = identityBindingRequest(t, uri).TLS
	w := httptest.NewRecorder()
	svc.GetHTTPHandler().governanceController.handleGovernanceEnvelope(w, req)
	return w
}

// TestGovernanceEnvelope_ForeignOperatorRejectionIsRecordedUnderEmbeddedSession
// proves the rejection of an envelope addressed to another Operator under an
// unknown session is persisted under the embedded Operator's own session, keeps
// the claimed ids in the signed receipt, and leaks no audit error text.
func TestGovernanceEnvelope_ForeignOperatorRejectionIsRecordedUnderEmbeddedSession(t *testing.T) {
	svc := newTestGatewayService(t, testGatewayOpts{posture: constants.PostureDoctrine})
	_, embeddedSessionID, err := svc.embeddedOperator.ClaimEmbeddedOperator(t.Context(), "owner")
	require.NoError(t, err)
	env := foreignOperatorEnvelope(t, svc, "foreign-session-nonce")

	w := submitForeignOperatorEnvelope(t, svc, env)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NotContains(t, w.Body.String(), "AUDIT_SESSION_UNKNOWN", "the response carries the typed code, not the audit store error")
	rejection := &operatorv1.GovernanceRejection{}
	require.NoError(t, protojson.Unmarshal(w.Body.Bytes(), rejection))
	require.Equal(t, constants.ErrTxTargetOperatorMismatch.Error(), rejection.GetError(), "the error text is the sentinel only, never the chain")
	require.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TARGET_OPERATOR_MISMATCH, rejection.GetReceipt().GetRejectionCode())

	record, err := svc.GetAuditStore().GetActionReceipt(env.Id)
	require.NoError(t, err)
	require.NotNil(t, record, "the rejection evidence must be persisted")
	require.Equal(t, record.ActionReceipt.GetSignature(), rejection.GetReceipt().GetSignature(), "the response carries the persisted signed receipt")
	require.Equal(t, embeddedSessionID, record.OperatorSessionID, "the audit row belongs to the embedded Operator's session")
	require.Equal(t, string(constants.DocIDEmbeddedOperator), record.OperatorID)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, record.Status)
	require.NotNil(t, record.ActionReceipt)
	require.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, record.ActionReceipt.FailureCode)
	var claimed bool
	for _, stage := range record.ActionReceipt.DeterministicStageEvidence {
		if stage.OperatorId == "foreign-outbound-operator" && stage.OperatorSessionId == "session-1" {
			claimed = true
		}
	}
	require.True(t, claimed, "the signed receipt keeps the claimed operator and session")
}

// TestGovernanceEnvelope_ForeignOperatorRejectionBeforeEmbeddedClaim proves a
// Gateway whose embedded Operator is not yet claimed still records the rejection,
// under the bootstrap audit session it started at startup.
func TestGovernanceEnvelope_ForeignOperatorRejectionBeforeEmbeddedClaim(t *testing.T) {
	svc := newTestGatewayService(t, testGatewayOpts{posture: constants.PostureDoctrine})
	env := foreignOperatorEnvelope(t, svc, "unclaimed-nonce")

	w := submitForeignOperatorEnvelope(t, svc, env)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "TX_TARGET_OPERATOR_MISMATCH")
	require.NotContains(t, w.Body.String(), "L5Actuator")
	require.NotContains(t, w.Body.String(), "rejection receipt")
	record, err := svc.GetAuditStore().GetActionReceipt(env.Id)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, constants.EmbeddedOperatorBootstrapSessionID, record.OperatorSessionID)
}

func TestGovernanceEnvelope_ForeignOperatorTarget_Returns403(t *testing.T) {
	svc := newTestGatewayService(t, testGatewayOpts{posture: constants.PostureDoctrine})
	payload, err := proto.Marshal(&operatorv1.FsReadRequested{Path: constants.TestPathShortData, ExecutionId: "target-rejection"})
	require.NoError(t, err)
	env := &commonv1.GovernanceEnvelope{
		ProtocolVersion:   govtypes.GovernanceProtocolVersionV2,
		Timestamp:         timestamppb.Now(),
		ExpiresAt:         timestamppb.New(time.Now().Add(time.Minute)),
		OperatorId:        "foreign-outbound-operator",
		OperatorSessionId: "session-1",
		ActionType:        string(constants.ActionTypeFsRead),
		Payload:           payload,
		Nonce:             "foreign-target-nonce",
	}
	eventType, err := constants.RequestEventForAction(constants.ActionTypeFsRead)
	require.NoError(t, err)
	env.EventType = string(eventType)
	env.StateMerkleRoot, err = svc.GetStateRootSvc().GetCurrentStateRoot(t.Context())
	require.NoError(t, err)
	env.Id, err = govtypes.GenerateMessageID(env)
	require.NoError(t, err)
	env.TransactionHash = env.Id
	uri := "spiffe://ztd.local/operator/org-1/foreign-outbound-operator/session-1"
	body := marshalEnvelope(t, env)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(body))
	req.TLS = identityBindingRequest(t, uri).TLS
	w := httptest.NewRecorder()
	svc.GetHTTPHandler().governanceController.handleGovernanceEnvelope(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "TX_TARGET_OPERATOR_MISMATCH")
	// A second submission must reach the same target rejection, not a replay error.
	w = httptest.NewRecorder()
	req.Body = io.NopCloser(bytes.NewReader(body))
	svc.GetHTTPHandler().governanceController.handleGovernanceEnvelope(w, req)
	require.Contains(t, w.Body.String(), "TX_TARGET_OPERATOR_MISMATCH")
}
