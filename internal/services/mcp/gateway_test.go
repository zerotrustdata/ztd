// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

type fakeEnvelopeProcessor struct {
	receipt    *operatorv1.ActionReceipt
	err        error
	gotPayload []byte
}

func (f *fakeEnvelopeProcessor) ProcessEnvelope(ctx context.Context, payload []byte) (*operatorv1.ActionReceipt, error) {
	f.gotPayload = append([]byte(nil), payload...)
	return f.receipt, f.err
}

type fakeStateRootProvider struct {
	root string
}

func (f *fakeStateRootProvider) GetCurrentStateRoot(context.Context) (string, error) {
	return f.root, nil
}

type fakeSuspendedStore struct {
	txs        map[string]*models.SuspendedTransaction
	deleteErr  error // returned by DeleteSuspendedTransaction when set
	releaseErr error // returned by ReleaseSuspendedTransaction when set
}

func (f *fakeSuspendedStore) StoreSuspendedTransaction(_ context.Context, tx *models.SuspendedTransaction) error {
	if tx.OperatorSessionID == "" {
		return fmt.Errorf("fake suspended store: operator_session_id: %w", constants.ErrMissingRequiredField)
	}
	if f.txs == nil {
		f.txs = make(map[string]*models.SuspendedTransaction)
	}
	f.txs[tx.TransactionHash] = tx
	return nil
}

func (f *fakeSuspendedStore) GetSuspendedTransaction(_ context.Context, txHash string) (*models.SuspendedTransaction, bool, error) {
	tx, ok := f.txs[txHash]
	return tx, ok, nil
}

func (f *fakeSuspendedStore) ListSuspendedTransactions(_ context.Context, userID string) ([]*models.SuspendedTransaction, error) {
	var result []*models.SuspendedTransaction
	for _, tx := range f.txs {
		if userID == "" || tx.UserID == userID {
			result = append(result, tx)
		}
	}
	return result, nil
}

func (f *fakeSuspendedStore) ApproveSuspendedTransaction(_ context.Context, txHash string, proof models.ApprovalProof) error {
	if tx, ok := f.txs[txHash]; ok {
		tx.ApprovedBy = proof.ApprovedBy
		tx.ApprovalSignature = proof.CliSignature
		tx.ExpectedCertFingerprint = proof.CertFingerprint
		tx.ApprovalPublicKey = proof.ApprovalPublicKey
		tx.PasskeyCredentialID = proof.CredentialID
		tx.PasskeyClientDataJSON = proof.ClientDataJSON
		tx.PasskeyAuthenticatorData = proof.AuthenticatorData
		tx.PasskeySignature = proof.Signature
	}
	return nil
}

func (f *fakeSuspendedStore) DeleteSuspendedTransaction(_ context.Context, txHash string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.txs, txHash)
	return nil
}

func (f *fakeSuspendedStore) ClaimSuspendedTransaction(_ context.Context, txHash string) error {
	tx, ok := f.txs[txHash]
	if !ok || tx.ExpiresAt.Before(time.Now()) {
		return constants.ErrSuspendedTransactionNotFound
	}
	if tx.ClaimedAt != nil {
		return constants.ErrSuspendedTransactionClaimed
	}
	now := time.Now().UTC()
	tx.ClaimedAt = &now
	return nil
}

func (f *fakeSuspendedStore) ReleaseSuspendedTransaction(_ context.Context, txHash string) error {
	if f.releaseErr != nil {
		return f.releaseErr
	}
	if tx, ok := f.txs[txHash]; ok {
		tx.ClaimedAt = nil
	}
	return nil
}

func (f *fakeSuspendedStore) DeleteExpiredSuspendedTransactions(_ context.Context, txs []*models.SuspendedTransaction) (int64, error) {
	var count int64
	for _, audited := range txs {
		if tx, ok := f.txs[audited.TransactionHash]; ok && tx.ExpiresAt.Before(time.Now()) {
			delete(f.txs, audited.TransactionHash)
			count++
		}
	}
	return count, nil
}

func (f *fakeSuspendedStore) GetExpiredSuspendedTransactions(_ context.Context) ([]*models.SuspendedTransaction, error) {
	var expired []*models.SuspendedTransaction
	for _, tx := range f.txs {
		if tx.ExpiresAt.Before(time.Now()) {
			expired = append(expired, tx)
		}
	}
	return expired, nil
}

// suspendableEnvelope returns a wire envelope that targets operatorSessionID,
// the only field StoreSuspendedTransaction reads from it.
func suspendableEnvelope(t *testing.T, operatorSessionID string) []byte {
	t.Helper()
	wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: "envelope-1", OperatorSessionId: operatorSessionID})
	require.NoError(t, err)
	return wire
}

func TestGatewayService_HandleToolsCall_ReturnsSignedReceiptProtojson(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId:   "tx-1",
		TransactionHash: "hash-1",
		SignerKeyId:     "warden-key-1",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "tool result",
		Signature:       "signature-1",
	}
	g := newTestGatewayService(t, withEnvProc(&fakeEnvelopeProcessor{receipt: receipt}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{},"execution_id":"execution-1","investigation_id":"investigation-1"}}`))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Result struct {
			Content []TextContent   `json:"content"`
			Receipt json.RawMessage `json:"receipt"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "tool result", resp.Result.Content[0].Text)
	var decoded operatorv1.ActionReceipt
	require.NoError(t, protojson.Unmarshal(resp.Result.Receipt, &decoded), "receipt must be strict protojson")
	assert.True(t, proto.Equal(receipt, &decoded), "the wire receipt must be the executing Operator's signed receipt")
}

func TestGatewayService_HandleToolsCall_GeneratesInvestigationIDWhenOmitted(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId:   "tx-2",
		TransactionHash: "hash-2",
		SignerKeyId:     "warden-key-2",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "tool result",
		Signature:       "signature-2",
	}
	proc := &fakeEnvelopeProcessor{receipt: receipt}
	g := newTestGatewayService(t, withEnvProc(proc))
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{},"execution_id":"execution-2"}}`))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, proc.gotPayload, "envelope payload must be captured")
	var env commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(proc.gotPayload, &env), "payload must be a GovernanceEnvelope")
	assert.NotEmpty(t, env.GetInvestigationId(), "gateway must generate an investigation ID when the client omits one")
}

func TestGatewayService_HandleToolsCall_BindsInvestigationIDIntoGovernanceEnvelope(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId:   "tx-3",
		TransactionHash: "hash-3",
		SignerKeyId:     "warden-key-3",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "tool result",
		Signature:       "signature-3",
	}
	proc := &fakeEnvelopeProcessor{receipt: receipt}
	g := newTestGatewayService(t, withEnvProc(proc))
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{},"execution_id":"execution-3","investigation_id":"investigation-3"}}`))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, proc.gotPayload, "envelope payload must be captured")
	var env commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(proc.gotPayload, &env), "payload must be a GovernanceEnvelope")
	assert.Equal(t, "investigation-3", env.GetInvestigationId(), "gateway must bind the authoritative investigation ID into the governance envelope")
}

func TestGatewayService_HandleToolsCall_ErrorMapping(t *testing.T) {
	cases := []struct {
		name         string
		GatewayErr   error
		expectedCode int
	}{
		{
			name:         "invalid envelope",
			GatewayErr:   governance.ErrInvalidEnvelope,
			expectedCode: constants.ErrCodeInvalidEnvelope,
		},
		{
			name:         "hash mismatch",
			GatewayErr:   governance.ErrTransactionHashMismatch,
			expectedCode: constants.ErrCodeHashMismatch,
		},
		{
			name:         "expired",
			GatewayErr:   governance.ErrTransactionExpired,
			expectedCode: constants.ErrCodeExpired,
		},
		{
			name:         "replay",
			GatewayErr:   governance.ErrTransactionReplay,
			expectedCode: constants.ErrCodeReplay,
		},
		{
			name:         "state root mismatch",
			GatewayErr:   governance.ErrStateRootMismatch,
			expectedCode: constants.ErrCodeStateMismatch,
		},
		{
			name:         "L1 validation failed",
			GatewayErr:   governance.ErrL1ValidationFailed,
			expectedCode: constants.ErrCodeL1ValidationFailed,
		},
		{
			name:         "L2 signature invalid",
			GatewayErr:   governance.ErrL2SignatureInvalid,
			expectedCode: constants.ErrCodeL2SignatureInvalid,
		},
		{
			name:         "L3 proof invalid",
			GatewayErr:   governance.ErrL3ProofInvalid,
			expectedCode: constants.ErrCodeL3ProofInvalid,
		},
		{
			name:         "payload decode failed",
			GatewayErr:   governance.ErrPayloadDecodeFailed,
			expectedCode: constants.ErrCodePayloadDecodeFailed,
		},
		{
			name:         "internal error fallback",
			GatewayErr:   fmt.Errorf("some internal error"),
			expectedCode: -32603,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc := &fakeEnvelopeProcessor{err: tc.GatewayErr}
			g := newTestGatewayService(t, withEnvProc(proc))

			// Valid MCP tools/call request
			reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{}}}`
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
			w := httptest.NewRecorder()

			g.HandleMCP(w, req)

			require.Equal(t, http.StatusOK, w.Code)

			var resp JSONRPCResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotNil(t, resp.Error)
			require.Equal(t, tc.expectedCode, resp.Error.Code)
			refusal := decodeRefusal(t, resp.Error.Data)
			require.Equal(t, governance.ClassifyIngressRefusal(tc.GatewayErr), refusal.GetCode())
			require.Equal(t, resp.Error.Message, refusal.GetError())
		})
	}
}

// TestGatewayService_RejectionReceiptIsErrorData proves MCP tools/call and
// A2A return the Operator's signed rejection receipt as typed
// GovernanceRejection error data, with its rejection_code.
func TestGatewayService_RejectionReceiptIsErrorData(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		reqBody string
		handle  func(g *GatewayService, w http.ResponseWriter, r *http.Request)
	}{
		{
			name:    "mcp tools/call",
			path:    "/mcp",
			reqBody: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_shell_command","arguments":{"command":"rm -rf /"}}}`,
			handle:  (*GatewayService).HandleMCP,
		},
		{
			name:    "mcp a2a/call",
			path:    "/mcp",
			reqBody: `{"jsonrpc":"2.0","id":1,"method":"a2a/call","params":{"skill_name":"wipe","payload":{}}}`,
			handle:  (*GatewayService).HandleMCP,
		},
		{
			name:    "a2a route",
			path:    constants.APIPaths.A2ACall,
			reqBody: `{"jsonrpc":"2.0","id":1,"method":"a2a/call","params":{"skill_name":"wipe","payload":{}}}`,
			handle:  (*GatewayService).HandleA2aCall,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			receipt := &operatorv1.ActionReceipt{
				TransactionId:   "tx-blocked-1",
				TransactionHash: "tx-blocked-1",
				SignerKeyId:     "warden-key-blocked",
				Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
				FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
				RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE,
				ResultSummary:   "rejected: L1 doctrine",
				Signature:       "sig-blocked-1",
			}
			verifyErr := fmt.Errorf("%w: destroy_rm_recursive_wide_target", governance.ErrL1ValidationFailed)
			proc := &fakeEnvelopeProcessor{receipt: receipt, err: governance.WithRejectionReceipt(receipt, verifyErr)}
			g := newTestGatewayService(t, withEnvProc(proc))
			w := httptest.NewRecorder()

			tc.handle(g, w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.reqBody)))

			require.Equal(t, http.StatusOK, w.Code)
			var resp JSONRPCResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ErrCodeL1ValidationFailed, resp.Error.Code)
			rejection := &operatorv1.GovernanceRejection{}
			require.NoError(t, protojson.Unmarshal(resp.Error.Data, rejection))
			assert.Equal(t, governance.ErrL1ValidationFailed.Error(), rejection.GetError())
			assert.Equal(t, "tx-blocked-1", rejection.GetReceipt().GetTransactionId())
			assert.Equal(t, "sig-blocked-1", rejection.GetReceipt().GetSignature())
			assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE, rejection.GetReceipt().GetRejectionCode())
		})
	}
}

func TestGatewayService_HandleToolsCall_NoFailedStageDataWhenReceiptIsNil(t *testing.T) {
	proc := &fakeEnvelopeProcessor{err: governance.ErrL1ValidationFailed}
	g := newTestGatewayService(t, withEnvProc(proc))

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_shell_command","arguments":{},"execution_id":"execution-2","investigation_id":"investigation-2"}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Error *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data,omitempty"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ErrCodeL1ValidationFailed, resp.Error.Code)
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE,
		decodeRefusal(t, resp.Error.Data).GetCode(), "a governed error without a receipt must carry an IngressRefusal")
}

func TestGatewayService_HandleToolsCall_Suspension(t *testing.T) {
	proc := &fakeEnvelopeProcessor{err: governance.ErrL3ProofMissing}
	store := &fakeSuspendedStore{}

	g := newTestGatewayService(t,
		withEnvProc(proc),
		withSuspendedStore(store),
	)
	g.posture = &governance.NotaryPosture{}

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{"foo":"bar"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyOperatorSessionID, "operator-session-1"))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	require.Len(t, store.txs, 1)
	var txHash string
	for k, tx := range store.txs {
		txHash = k
		require.Equal(t, "operator-session-1", tx.OperatorSessionID)
		require.Equal(t, "test-tool", tx.ToolName)
		require.JSONEq(t, `{"foo":"bar"}`, string(tx.ToolArguments))
	}
	approvalURL := fmt.Sprintf("https://localhost:%d/approve/%s", constants.Ports.OperatorHttps, txHash)
	expected := CallToolResult{
		Content: []TextContent{{Type: "text", Text: approvalPausedMessage(constants.PostureNotary, approvalURL)}},
		Suspension: &GovernedSuspension{
			ID:          txHash,
			Status:      string(constants.GatewayResponseStatusSuspended),
			TxHash:      txHash,
			ApprovalURL: approvalURL,
			Message:     approvalPausedMessage(constants.PostureNotary, approvalURL),
		},
	}
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":`+string(expectedJSON)+`}`, w.Body.String())
}

func TestCallToolSuccessReturnsCanonicalSignedReceipt(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId:   "tx-call-1",
		TransactionHash: "hash-call-1",
		SignerKeyId:     "warden-key-call-1",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "tool result",
		Signature:       "signature-call-1",
	}
	g := newTestGatewayService(t, withEnvProc(&fakeEnvelopeProcessor{receipt: receipt}))
	params := json.RawMessage(`{"name":"test-tool","arguments":{},"execution_id":"execution-1","investigation_id":"investigation-1"}`)

	res, err := g.callTool(context.Background(), httptest.NewRequest(http.MethodPost, "/mcp", nil), params)
	require.NoError(t, err)
	result, ok := res.(CallToolResult)
	require.True(t, ok)
	assert.False(t, result.IsError)
	assert.Nil(t, result.Suspension)

	var decoded operatorv1.ActionReceipt
	require.NoError(t, protojson.Unmarshal(result.Receipt, &decoded), "receipt must be strict protojson")
	assert.True(t, proto.Equal(receipt, &decoded))
	canonical, err := response.MarshalProtoJSONCanonical(receipt)
	require.NoError(t, err)
	assert.Equal(t, canonical, []byte(result.Receipt), "the embedded receipt must be the canonical bytes")
}

func TestA2ACallSuccessReturnsCanonicalSignedReceipt(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId:   "tx-a2a-1",
		TransactionHash: "hash-a2a-1",
		SignerKeyId:     "warden-key-a2a-1",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "a2a result",
		Signature:       "signature-a2a-1",
	}
	g := newTestGatewayService(t, withEnvProc(&fakeEnvelopeProcessor{receipt: receipt}))
	params := json.RawMessage(`{"skill_name":"test-skill","payload":{"arg":"val"}}`)

	res, err := g.a2aCall(context.Background(), httptest.NewRequest(http.MethodPost, "/api/v1/a2a/call", nil), params)
	require.NoError(t, err)
	success, ok := res.(A2ASuccessResponse)
	require.True(t, ok)

	var decoded operatorv1.ActionReceipt
	require.NoError(t, protojson.Unmarshal(success.Result, &decoded), "result must be strict protojson")
	assert.True(t, proto.Equal(receipt, &decoded))
	canonical, err := response.MarshalProtoJSONCanonical(receipt)
	require.NoError(t, err)
	assert.Equal(t, canonical, []byte(success.Result), "the result must be the canonical receipt bytes")
}

func TestEncodeActionReceipt_FailsClosedOnNilReceipt(t *testing.T) {
	encoded, err := encodeActionReceipt(nil)
	require.ErrorIs(t, err, constants.ErrInternal)
	assert.Nil(t, encoded, "a governed success must never be returned without its receipt")
}

func TestGovernedSuspension_MCPAndA2AShareTypedShape(t *testing.T) {
	proc := &fakeEnvelopeProcessor{err: governance.ErrL3ProofMissing}
	store := &fakeSuspendedStore{}
	g := newTestGatewayService(t, withEnvProc(proc), withSuspendedStore(store))
	g.posture = &governance.NotaryPosture{}
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, "user-1")
	ctx = context.WithValue(ctx, constants.ContextKeyOperatorSessionID, "operator-session-1")
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)

	mcpRes, err := g.callTool(ctx, r, json.RawMessage(`{"name":"test-tool","arguments":{}}`))
	require.NoError(t, err)
	mcpSuspension := mcpRes.(CallToolResult).Suspension
	require.NotNil(t, mcpSuspension, "a missing L3 proof must suspend the MCP call")

	a2aRes, err := g.a2aCall(ctx, r, json.RawMessage(`{"skill_name":"test-skill","payload":{}}`))
	require.NoError(t, err)
	a2aSuspension, ok := a2aRes.(*GovernedSuspension)
	require.True(t, ok, "a2a suspension must be the typed GovernedSuspension")

	require.NotEmpty(t, mcpSuspension.TxHash)
	assert.Equal(t, mcpSuspension.TxHash, mcpSuspension.ID)
	assert.Equal(t, string(constants.GatewayResponseStatusSuspended), mcpSuspension.Status)
	assert.Contains(t, mcpSuspension.ApprovalURL, mcpSuspension.TxHash)
	assert.Equal(t, mcpSuspension.Message, mcpRes.(CallToolResult).Content[0].Text)
	assert.Equal(t, mcpSuspension.Status, a2aSuspension.Status, "MCP and A2A must share the suspension shape")
	assert.Equal(t, approvalPausedMessage(constants.PostureNotary, a2aSuspension.ApprovalURL), a2aSuspension.Message)
	assert.Equal(t, approvalPausedMessage(constants.PostureNotary, mcpSuspension.ApprovalURL), mcpSuspension.Message)
}

// suspendedCallFixture stores one suspended MCP tools/call and one suspended
// A2A a2a/call under a ratify posture, returning their transaction hashes.
func suspendedCallFixture(t *testing.T, proc *fakeEnvelopeProcessor, store *fakeSuspendedStore, call func(ctx context.Context, r *http.Request) (string, error)) string {
	t.Helper()
	proc.err = governance.ErrL3ProofMissing
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, "user-1")
	ctx = context.WithValue(ctx, constants.ContextKeyOperatorSessionID, "operator-session-1")
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
	hash, err := call(ctx, r)
	require.NoError(t, err)
	require.Contains(t, store.txs, hash, "the call must be stored as a suspension")
	return hash
}

func invalidL3ProofRejection(t *testing.T) (*operatorv1.ActionReceipt, error) {
	t.Helper()
	receipt := &operatorv1.ActionReceipt{
		TransactionId:   "tx-l3-invalid",
		TransactionHash: "hash-l3-invalid",
		SignerKeyId:     "warden-key-l3",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_INVALID,
		ResultSummary:   "rejected: L3 proof invalid",
		Signature:       "sig-l3-invalid",
	}
	return receipt, governance.WithRejectionReceipt(receipt, governance.ErrL3ProofInvalid)
}

func TestResumeWithL3Proof_InvalidProofReturnsSignedRejectionForMCPCall(t *testing.T) {
	proc := &fakeEnvelopeProcessor{}
	store := &fakeSuspendedStore{}
	g := newTestGatewayService(t, withEnvProc(proc), withSuspendedStore(store))
	g.posture = &governance.RatifyPosture{}
	txHash := suspendedCallFixture(t, proc, store, func(ctx context.Context, r *http.Request) (string, error) {
		res, err := g.callTool(ctx, r, json.RawMessage(`{"name":"test-tool","arguments":{}}`))
		if err != nil {
			return "", err
		}
		return res.(CallToolResult).Suspension.TxHash, nil
	})
	assertInvalidProofRejectionKeepsSuspension(t, g, proc, store, txHash)
}

func TestResumeWithL3Proof_InvalidProofReturnsSignedRejectionForA2ACall(t *testing.T) {
	proc := &fakeEnvelopeProcessor{}
	store := &fakeSuspendedStore{}
	g := newTestGatewayService(t, withEnvProc(proc), withSuspendedStore(store))
	g.posture = &governance.RatifyPosture{}
	txHash := suspendedCallFixture(t, proc, store, func(ctx context.Context, r *http.Request) (string, error) {
		res, err := g.a2aCall(ctx, r, json.RawMessage(`{"skill_name":"test-skill","payload":{}}`))
		if err != nil {
			return "", err
		}
		return res.(*GovernedSuspension).TxHash, nil
	})
	assertInvalidProofRejectionKeepsSuspension(t, g, proc, store, txHash)
}

func assertInvalidProofRejectionKeepsSuspension(t *testing.T, g *GatewayService, proc *fakeEnvelopeProcessor, store *fakeSuspendedStore, txHash string) {
	t.Helper()
	rejection, procErr := invalidL3ProofRejection(t)
	proc.receipt = rejection
	proc.err = procErr

	receipt, err := g.ResumeWithL3Proof(context.Background(), txHash, "user-1", &commonv1.L3Proof{CredentialId: "cred-bad"})
	require.ErrorIs(t, err, governance.ErrL3ProofInvalid)
	require.NotNil(t, receipt, "an invalid proof must return the signed rejection receipt")
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_INVALID, receipt.GetRejectionCode())
	assert.NotEmpty(t, receipt.GetSignature(), "the rejection receipt must be signed")
	assert.Contains(t, store.txs, txHash, "a rejected proof keeps the suspension so it can be retried")
}

func TestGatewayService_ResumeWithL3Proof(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId: "tx-1",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary: "tool result",
	}
	proc := &fakeEnvelopeProcessor{receipt: receipt}
	store := &fakeSuspendedStore{}

	txHash := "hash-1"
	envelope := `{"id":"tx-1","transaction_hash":"hash-1","action_type":"MCP_CALL","payload":"e30=","operator_session_id":"operator-session-1"}`

	g := newTestGatewayService(t,
		withEnvProc(proc),
		withSuspendedStore(store),
	)

	require.NoError(t, g.StoreSuspendedTransaction(context.Background(), txHash, []byte(envelope), "test-tool", json.RawMessage(`{}`), "user-1", "op-1", ""))

	proof := &commonv1.L3Proof{
		CredentialId: "cred-1",
	}

	gotReceipt, err := g.ResumeWithL3Proof(context.Background(), txHash, "user-1", proof)
	require.NoError(t, err)
	require.Equal(t, receipt, gotReceipt)

	// Verify it was deleted from store
	require.Empty(t, store.txs)

	// Verify L3 proof was attached in the call to ProcessEnvelope
	require.Contains(t, string(proc.gotPayload), `"l3"`)
	require.Contains(t, string(proc.gotPayload), `"credentialId"`)
}

func TestGatewayService_ResumeWithL3Proof_ExpiredTransaction(t *testing.T) {
	proc := &fakeEnvelopeProcessor{}
	store := &fakeSuspendedStore{}

	g := newTestGatewayService(t,
		withEnvProc(proc),
		withSuspendedStore(store),
	)

	proof := &commonv1.L3Proof{
		CredentialId: "cred-1",
	}

	// Attempt to resume a non-existent (expired) transaction
	_, err := g.ResumeWithL3Proof(context.Background(), "expired-hash", "user-1", proof)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrTransactionExpired)
}

func l3DeniedRejection() (*operatorv1.ActionReceipt, error) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId:   "tx-l3-denied",
		TransactionHash: "hash-l3-denied",
		SignerKeyId:     "warden-key-l3",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED,
		ResultSummary:   "rejected: L3 denied",
		Signature:       "sig-l3-denied",
	}
	return receipt, governance.WithRejectionReceipt(receipt, constants.ErrTxL3Denied)
}

func TestDenySuspendedTransaction_ReturnsSignedRejectionForMCPAndA2ACalls(t *testing.T) {
	calls := map[string]func(g *GatewayService) func(ctx context.Context, r *http.Request) (string, error){
		"mcp": func(g *GatewayService) func(ctx context.Context, r *http.Request) (string, error) {
			return func(ctx context.Context, r *http.Request) (string, error) {
				res, err := g.callTool(ctx, r, json.RawMessage(`{"name":"test-tool","arguments":{}}`))
				if err != nil {
					return "", err
				}
				return res.(CallToolResult).Suspension.TxHash, nil
			}
		},
		"a2a": func(g *GatewayService) func(ctx context.Context, r *http.Request) (string, error) {
			return func(ctx context.Context, r *http.Request) (string, error) {
				res, err := g.a2aCall(ctx, r, json.RawMessage(`{"skill_name":"test-skill","payload":{}}`))
				if err != nil {
					return "", err
				}
				return res.(*GovernedSuspension).TxHash, nil
			}
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			proc := &fakeEnvelopeProcessor{}
			store := &fakeSuspendedStore{}
			g := newTestGatewayService(t, withEnvProc(proc), withSuspendedStore(store))
			g.posture = &governance.RatifyPosture{}
			txHash := suspendedCallFixture(t, proc, store, call(g))

			proc.receipt, proc.err = l3DeniedRejection()
			receipt, err := g.DenySuspendedTransaction(context.Background(), txHash, "user-1")
			require.NoError(t, err)
			require.NotNil(t, receipt)
			assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, receipt.GetRejectionCode())
			assert.NotEmpty(t, receipt.GetSignature(), "the deny must be an Operator-signed receipt")
			assert.Contains(t, string(proc.gotPayload), `"denied":true`, "the deny must reach L4 as the L3 denied marker")
			assert.NotContains(t, store.txs, txHash, "a signed deny ends the suspension")
		})
	}
}

func TestDenySuspendedTransaction_ExecutionIsNeverADeny(t *testing.T) {
	proc := &fakeEnvelopeProcessor{}
	store := &fakeSuspendedStore{}
	g := newTestGatewayService(t, withEnvProc(proc), withSuspendedStore(store))
	g.posture = &governance.RatifyPosture{}
	txHash := suspendedCallFixture(t, proc, store, func(ctx context.Context, r *http.Request) (string, error) {
		res, err := g.callTool(ctx, r, json.RawMessage(`{"name":"test-tool","arguments":{}}`))
		if err != nil {
			return "", err
		}
		return res.(CallToolResult).Suspension.TxHash, nil
	})

	proc.receipt = &operatorv1.ActionReceipt{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
	proc.err = nil
	_, err := g.DenySuspendedTransaction(context.Background(), txHash, "user-1")
	require.ErrorIs(t, err, constants.ErrL3DenyNotRejected)
}

func TestDenySuspendedTransaction_UnsignedFailureReleasesTheClaim(t *testing.T) {
	proc := &fakeEnvelopeProcessor{}
	store := &fakeSuspendedStore{}
	g := newTestGatewayService(t, withEnvProc(proc), withSuspendedStore(store))
	g.posture = &governance.RatifyPosture{}
	txHash := suspendedCallFixture(t, proc, store, func(ctx context.Context, r *http.Request) (string, error) {
		res, err := g.callTool(ctx, r, json.RawMessage(`{"name":"test-tool","arguments":{}}`))
		if err != nil {
			return "", err
		}
		return res.(CallToolResult).Suspension.TxHash, nil
	})

	proc.receipt = nil
	proc.err = constants.ErrGatewayNotReady
	receipt, err := g.DenySuspendedTransaction(context.Background(), txHash, "user-1")
	require.ErrorIs(t, err, constants.ErrGatewayNotReady)
	assert.Nil(t, receipt)
	assert.Contains(t, store.txs, txHash, "an unsigned failure keeps the suspension so the deny can be retried")
}

func TestDenySuspendedTransaction_ExpiredTransaction(t *testing.T) {
	g := newTestGatewayService(t, withEnvProc(&fakeEnvelopeProcessor{}), withSuspendedStore(&fakeSuspendedStore{}))
	_, err := g.DenySuspendedTransaction(context.Background(), "expired-hash", "user-1")
	require.ErrorIs(t, err, constants.ErrTransactionExpired)
}

func TestGatewayService_RunMaintenance_AuditsExpiredTransactions(t *testing.T) {
	tempDir := testutil.TempDir(t)
	vaultDir := filepath.Join(tempDir, "vault")

	fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

	// Create test vault
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := storagetest.CreateTestVault(t, vaultDir, privKey)

	// Create audit store
	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), slog.Default())
	require.NoError(t, err)
	defer db.Close()

	auditConfig := &storage.AuditStoreConfig{
		RetentionDays:        7,
		PruneIntervalMinutes: 60,
		EncryptionVault:      testVault,
	}
	auditStore, err := storage.NewSQLAuditStore(auditConfig, db, slog.Default(), fileSvc)
	require.NoError(t, err)
	defer auditStore.Close()

	// Create session for the operator
	operatorSessionID := "session-maintenance-test"
	err = auditStore.CreateSession(operatorSessionID, "operator", "Maintenance Test", "user@test.com")
	require.NoError(t, err)

	// Create a fake store with an expired transaction
	store := &fakeSuspendedStore{}
	expiredTx := &models.SuspendedTransaction{
		TransactionHash:   "expired-hash-123",
		Envelope:          []byte(`{"id":"123"}`),
		CreatedAt:         time.Now().UTC().Add(-10 * time.Minute),
		ExpiresAt:         time.Now().UTC().Add(-5 * time.Minute),
		ToolName:          "test-tool",
		ToolArguments:     json.RawMessage(`{}`),
		UserID:            "user-1",
		OperatorID:        "app-1",
		OperatorSessionID: operatorSessionID,
	}
	require.NoError(t, store.StoreSuspendedTransaction(context.Background(), expiredTx))

	g := newTestGatewayService(t,
		withSuspendedStore(store),
		withAuditStore(auditStore),
	)

	// Run a single maintenance sweep directly
	err = g.runMaintenanceSweep(context.Background())
	require.NoError(t, err)

	// Verify expiry event was recorded in audit store
	events, err := auditStore.GetEvents(models.AuditScope{OperatorSessionID: operatorSessionID}, 10, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// Verify the event details
	require.Equal(t, constants.Event.Operator.Notary.TransactionExpired, events[0].Type)
	require.Contains(t, events[0].ContentText, "expired-hash-123")
	require.Contains(t, events[0].ContentText, "test-tool")
	require.Contains(t, events[0].ContentText, "expired")

	// Verify the expired transaction was deleted
	_, found, err := store.GetSuspendedTransaction(context.Background(), "expired-hash-123")
	require.NoError(t, err)
	require.False(t, found)
}

func TestGatewayService_RunMaintenanceSweep_KeepsExpiredTransactionsWhenAuditFails(t *testing.T) {
	store := &fakeSuspendedStore{}
	require.NoError(t, store.StoreSuspendedTransaction(context.Background(), &models.SuspendedTransaction{
		TransactionHash:   "expired-unaudited",
		Envelope:          []byte(`{"id":"1"}`),
		CreatedAt:         time.Now().UTC().Add(-10 * time.Minute),
		ExpiresAt:         time.Now().UTC().Add(-5 * time.Minute),
		ToolName:          "test-tool",
		OperatorSessionID: "operator-session-1",
	}))
	g := newTestGatewayService(t, withSuspendedStore(store), withAuditStore(failingAuditEventRecorder{err: constants.ErrAuditSessionUnknown}))

	err := g.runMaintenanceSweep(context.Background())

	require.ErrorIs(t, err, constants.ErrAuditSessionUnknown)
	require.Contains(t, store.txs, "expired-unaudited", "an expiry that is not on the audit chain must not be pruned")
}

// sessionFailingAuditEventRecorder records every event except those targeting
// failSession, which fail with err.
type sessionFailingAuditEventRecorder struct {
	failSession string
	err         error
	events      []*storage.Event
}

func (r *sessionFailingAuditEventRecorder) RecordEvent(event *storage.Event) (int64, error) {
	if event.OperatorSessionID == r.failSession {
		return 0, r.err
	}
	r.events = append(r.events, event)
	return int64(len(r.events)), nil
}

func (r *sessionFailingAuditEventRecorder) RecordEventOnce(event *storage.Event) (int64, bool, error) {
	id, err := r.RecordEvent(event)
	return id, err == nil, err
}

func TestGatewayService_RunMaintenanceSweep_DeletesOnlyAuditedExpiredTransactions(t *testing.T) {
	expiredAt := time.Now().UTC().Add(-5 * time.Minute)
	store := &fakeSuspendedStore{}
	for hash, session := range map[string]string{
		"expired-audited":   "operator-session-ok",
		"expired-unaudited": "operator-session-broken",
	} {
		require.NoError(t, store.StoreSuspendedTransaction(context.Background(), &models.SuspendedTransaction{
			TransactionHash:   hash,
			Envelope:          []byte(`{"id":"1"}`),
			CreatedAt:         expiredAt.Add(-5 * time.Minute),
			ExpiresAt:         expiredAt,
			ToolName:          "test-tool",
			OperatorSessionID: session,
		}))
	}
	require.NoError(t, store.StoreSuspendedTransaction(context.Background(), &models.SuspendedTransaction{
		TransactionHash:   "still-pending",
		Envelope:          []byte(`{"id":"2"}`),
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(time.Hour),
		ToolName:          "test-tool",
		OperatorSessionID: "operator-session-ok",
	}))
	recorder := &sessionFailingAuditEventRecorder{failSession: "operator-session-broken", err: constants.ErrAuditSessionUnknown}
	g := newTestGatewayService(t, withSuspendedStore(store), withAuditStore(recorder))

	err := g.runMaintenanceSweep(context.Background())

	require.ErrorIs(t, err, constants.ErrAuditSessionUnknown)
	require.NotContains(t, store.txs, "expired-audited", "an expiry on the audit chain is pruned")
	require.Contains(t, store.txs, "expired-unaudited", "an expiry that is not on the audit chain is kept for the next sweep")
	require.Contains(t, store.txs, "still-pending")
	require.Len(t, recorder.events, 1)
	require.Equal(t, "operator-session-ok", recorder.events[0].OperatorSessionID)
	require.Equal(t, constants.Event.Operator.Notary.TransactionExpired, recorder.events[0].Type)
	require.Contains(t, recorder.events[0].ContentText, "expired-audited")
}

func TestGatewayService_HandleResourcesRead(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId: "tx-1",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary: "resource content",
	}
	proc := &fakeEnvelopeProcessor{receipt: receipt}

	g := newTestGatewayService(t, withEnvProc(proc))

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"file:///test.txt"}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	expectedJSON := `{"jsonrpc":"2.0","id":1,"result":{"contents":[{"type":"text","text":"resource content"}]}}`
	require.JSONEq(t, expectedJSON, w.Body.String())

	var env commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(proc.gotPayload, &env))
	assert.Equal(t, string(constants.EventOperatorMcpResourceReadRequested), env.EventType)
	assert.Equal(t, string(constants.ActionTypeMcpResourceRead), env.ActionType)
}

func TestGatewayService_HandlePromptsGet(t *testing.T) {
	receipt := &operatorv1.ActionReceipt{
		TransactionId: "tx-1",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary: "prompt template",
	}
	proc := &fakeEnvelopeProcessor{receipt: receipt}

	g := newTestGatewayService(t, withEnvProc(proc))

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"test-prompt"}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	expectedJSON := `{"jsonrpc":"2.0","id":1,"result":{"description":"prompt template","messages":[{"role":"user","content":{"type":"text","text":"prompt template"}}]}}`
	require.JSONEq(t, expectedJSON, w.Body.String())

	var env commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(proc.gotPayload, &env))
	assert.Equal(t, string(constants.EventOperatorMcpPromptGetRequested), env.EventType)
	assert.Equal(t, string(constants.ActionTypeMcpPromptGet), env.ActionType)
}

func TestGatewayService_CircuitBreaker(t *testing.T) {

	t.Run("initially closed", func(t *testing.T) {
		g := newTestGatewayService(t, withCircuitBreaker(5, 1*time.Minute))
		require.False(t, g.isCircuitOpen())
	})

	t.Run("opens after max failures", func(t *testing.T) {
		g := newTestGatewayService(t, withCircuitBreaker(3, 1*time.Minute))

		for i := 0; i < 3; i++ {
			g.recordFailure()
		}
		require.True(t, g.isCircuitOpen())
	})

	t.Run("closes after success", func(t *testing.T) {
		g := newTestGatewayService(t, withCircuitBreaker(3, 1*time.Minute))

		// Open the circuit
		for i := 0; i < 3; i++ {
			g.recordFailure()
		}
		require.True(t, g.isCircuitOpen())

		// Close it with success
		g.recordSuccess()
		require.False(t, g.isCircuitOpen())
		require.Equal(t, 0, g.failureCount)
	})

	t.Run("half-open after cooldown", func(t *testing.T) {
		g := newTestGatewayService(t, withCircuitBreaker(3, 100*time.Millisecond))

		// Open the circuit
		for i := 0; i < 3; i++ {
			g.recordFailure()
		}
		require.True(t, g.isCircuitOpen())

		// Wait for cooldown
		time.Sleep(150 * time.Millisecond)
		require.False(t, g.isCircuitOpen(), "circuit should be half-open after cooldown")
	})
}

func TestGatewayService_IsNativeTool(t *testing.T) {
	g := newTestGatewayService(t)

	t.Run("native tool recognized", func(t *testing.T) {
		require.True(t, g.isNativeTool("db_discover_topology"))
		require.True(t, g.isNativeTool("db_query_validate"))
		require.True(t, g.isNativeTool("log_stream_filter"))
	})

	t.Run("non-native tool rejected", func(t *testing.T) {
		require.False(t, g.isNativeTool("external_tool"))
		require.False(t, g.isNativeTool(""))
	})
}

func TestGatewayService_ScanForForbiddenPatterns(t *testing.T) {
	g := newTestGatewayService(t)

	strVal := func(s string) FieldValue { return FieldValue{Str: &s} }

	t.Run("detects destructive rm rf root", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("sudo rm -rf /"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "L1 hard gate")
		require.Contains(t, err.Error(), "data_destruction")
		require.Contains(t, err.Error(), "destroy_rm_rf_root")
	})

	t.Run("detects password credential leak", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("password=secret123"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "L1 hard gate")
		require.Contains(t, err.Error(), "credential_access")
		require.Contains(t, err.Error(), "cred_leak_password_assignment")
	})

	t.Run("detects api_key credential leak", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("api_key=sk-12345"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "L1 hard gate")
		require.Contains(t, err.Error(), "credential_access")
		require.Contains(t, err.Error(), "cred_leak_api_key_assignment")
	})

	t.Run("detects destructive file operation", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("rm -rf /"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "data_destruction")
	})

	t.Run("allows safe values", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("safe value 123"))
		require.NoError(t, err)
	})

	t.Run("null value allowed", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(FieldValue{Null: true})
		require.NoError(t, err)
	})

	t.Run("pseudo-random does not match sudo", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("pseudo-random value"))
		require.NoError(t, err)
	})

	t.Run("token field name does not trigger false positive", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("csrf_token column in database"))
		require.NoError(t, err)
	})

	t.Run("URL in data does not trigger false positive", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("visit https://example.com"))
		require.NoError(t, err)
	})

	t.Run("private key block detected", func(t *testing.T) {
		err := g.scanForForbiddenPatterns(strVal("-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA..."))
		require.Error(t, err)
		require.Contains(t, err.Error(), "credential_access")
		require.Contains(t, err.Error(), "cred_leak_private_key_block")
	})
}

func TestGatewayService_MapGatewayError(t *testing.T) {
	g := newTestGatewayService(t)

	t.Run("maps governance errors", func(t *testing.T) {
		tests := []struct {
			err      error
			wantCode int
		}{
			{governance.ErrInvalidEnvelope, constants.ErrCodeInvalidEnvelope},
			{governance.ErrTransactionHashMismatch, constants.ErrCodeHashMismatch},
			{governance.ErrTransactionExpired, constants.ErrCodeExpired},
			{governance.ErrTransactionReplay, constants.ErrCodeReplay},
			{governance.ErrStateRootMismatch, constants.ErrCodeStateMismatch},
			{governance.ErrL1ValidationFailed, constants.ErrCodeL1ValidationFailed},
			{governance.ErrL2SignatureInvalid, constants.ErrCodeL2SignatureInvalid},
			{governance.ErrL3ProofInvalid, constants.ErrCodeL3ProofInvalid},
			{governance.ErrPayloadDecodeFailed, constants.ErrCodePayloadDecodeFailed},
		}

		for _, tt := range tests {
			require.Equal(t, tt.wantCode, g.mapGatewayError(tt.err))
		}
	})

	t.Run("unknown error maps to internal error", func(t *testing.T) {
		require.Equal(t, -32603, g.mapGatewayError(fmt.Errorf("unknown error"))) // Internal error code
	})
}

func TestGatewayService_StoreSuspendedTransaction(t *testing.T) {

	t.Run("stores transaction", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withSuspendedStore(store))

		require.NoError(t, g.StoreSuspendedTransaction(context.Background(), "hash-123", suspendableEnvelope(t, "operator-session-1"), "test-tool", json.RawMessage(`{"arg":"val"}`), "user-1", "op-1", "cert-fp-abc123"))

		tx, found, err := store.GetSuspendedTransaction(context.Background(), "hash-123")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "hash-123", tx.TransactionHash)
		require.Equal(t, "test-tool", tx.ToolName)
		require.Equal(t, "user-1", tx.UserID)
		require.Equal(t, "op-1", tx.OperatorID)
		require.Equal(t, "operator-session-1", tx.OperatorSessionID, "the envelope's Operator session, not the app or user ID")
		require.Equal(t, "cert-fp-abc123", tx.ExpectedCertFingerprint)
	})

	t.Run("populates submitter CLI session ID from context", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withSuspendedStore(store))

		ctx := context.WithValue(context.Background(), constants.ContextKeyCLISessionID, "cli-session-from-ctx")
		require.NoError(t, g.StoreSuspendedTransaction(ctx, "hash-cli-ctx", suspendableEnvelope(t, "operator-session-1"), "test-tool", json.RawMessage(`{"arg":"val"}`), "user-1", "op-1", "cert-fp-abc123"))

		tx, found, err := store.GetSuspendedTransaction(context.Background(), "hash-cli-ctx")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "cli-session-from-ctx", tx.SubmitterCLISessionID)
	})

	t.Run("nil store fails closed instead of dropping the suspension", func(t *testing.T) {
		g := newTestGatewayService(t, withSuspendedStore(nil))

		err := g.StoreSuspendedTransaction(context.Background(), "test-hash", suspendableEnvelope(t, "operator-session-1"), "test-tool", json.RawMessage(`{}`), "user-1", "op-1", "")

		require.ErrorIs(t, err, constants.ErrSuspendedStoreRequired)
	})

	t.Run("records approval requested event", func(t *testing.T) {
		tempDir := testutil.TempDir(t)
		vaultDir := filepath.Join(tempDir, "vault")

		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)
		require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

		// Create test vault
		_, privKey, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		testVault := storagetest.CreateTestVault(t, vaultDir, privKey)

		// Create audit store
		dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
		db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), slog.Default())
		require.NoError(t, err)
		defer db.Close()

		auditConfig := &storage.AuditStoreConfig{
			RetentionDays:        7,
			PruneIntervalMinutes: 60,
			EncryptionVault:      testVault,
		}
		auditStore, err := storage.NewSQLAuditStore(auditConfig, db, slog.Default(), fileSvc)
		require.NoError(t, err)
		defer auditStore.Close()

		// Create session for the operator
		operatorSessionID := "session-approval-requested-test"
		err = auditStore.CreateSession(operatorSessionID, "operator", "Approval Requested Test", "user@test.com")
		require.NoError(t, err)

		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t,
			withSuspendedStore(store),
			withAuditStore(auditStore),
		)

		txHash := "hash-approval-123"
		require.NoError(t, g.StoreSuspendedTransaction(context.Background(), txHash, suspendableEnvelope(t, operatorSessionID), "test-tool", json.RawMessage(`{"arg":"val"}`), "user-1", "app-1", "cert-fp-abc123"))

		// Verify approval requested event was recorded in audit store
		events, err := auditStore.GetEvents(models.AuditScope{OperatorSessionID: operatorSessionID}, 10, 0)
		require.NoError(t, err)
		require.Len(t, events, 1)

		// Verify the event details
		require.Equal(t, constants.Event.Operator.Notary.ApprovalRequested, events[0].Type)
		require.Contains(t, events[0].ContentText, txHash)
		require.Contains(t, events[0].ContentText, "test-tool")
		require.Contains(t, events[0].ContentText, "approval requested")
	})
}

func TestGatewayService_GetSuspendedTransaction(t *testing.T) {

	t.Run("successful retrieval", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withSuspendedStore(store))

		require.NoError(t, g.StoreSuspendedTransaction(context.Background(), "test-hash", suspendableEnvelope(t, "operator-session-1"), "test-tool", json.RawMessage(`{"arg":"val"}`), "user-1", "op-1", ""))

		retrieved, ok, err := g.GetSuspendedTransaction(context.Background(), "test-hash")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "test-hash", retrieved.TransactionHash)
		require.Equal(t, "test-tool", retrieved.ToolName)
	})

	t.Run("transaction not found", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withSuspendedStore(store))

		retrieved, ok, err := g.GetSuspendedTransaction(context.Background(), "nonexistent")
		require.NoError(t, err)
		require.False(t, ok)
		require.Nil(t, retrieved)
	})

	t.Run("nil store returns false", func(t *testing.T) {
		g := newTestGatewayService(t, withSuspendedStore(nil))

		retrieved, ok, err := g.GetSuspendedTransaction(context.Background(), "test-hash")
		require.NoError(t, err)
		require.False(t, ok)
		require.Nil(t, retrieved)
	})
}

func TestGatewayService_DeleteSuspendedTransaction(t *testing.T) {

	t.Run("successful deletion", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withSuspendedStore(store))

		require.NoError(t, g.StoreSuspendedTransaction(context.Background(), "test-hash", suspendableEnvelope(t, "operator-session-1"), "test-tool", json.RawMessage(`{}`), "user-1", "op-1", ""))

		g.DeleteSuspendedTransaction(context.Background(), "test-hash")

		_, ok, err := store.GetSuspendedTransaction(context.Background(), "test-hash")
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("delete nonexistent transaction", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withSuspendedStore(store))

		// Should not panic
		g.DeleteSuspendedTransaction(context.Background(), "nonexistent")
	})

	t.Run("nil store does not panic", func(t *testing.T) {
		g := newTestGatewayService(t, withSuspendedStore(nil))

		// Should not panic
		g.DeleteSuspendedTransaction(context.Background(), "test-hash")
	})
}

func TestGatewayService_NewGatewayService(t *testing.T) {

	t.Run("creates service with dependencies", func(t *testing.T) {
		deps := Dependencies{
			Posture:          constants.PostureDoctrine,
			Logger:           slog.Default(),
			Responder:        response.NewWriter(slog.Default()),
			SuspendedStore:   &fakeSuspendedStore{},
			MaxPayloadBytes:  10 * 1024 * 1024,
			AuditStore:       noopAuditEventRecorder{},
			EmbeddedSession:  fakeEmbeddedSession{sessionID: testEmbeddedSessionID},
			ScrubbingService: newTestScrubbingService(t),
		}

		g, err := NewGatewayService(deps)
		require.NoError(t, err)

		require.NotNil(t, g)
		require.Equal(t, deps.Logger, g.logger)
		require.Equal(t, deps.Responder, g.responder)
		require.Equal(t, deps.SuspendedStore, g.suspendedStore)
		require.Equal(t, deps.MaxPayloadBytes, g.maxPayloadBytes)
		require.Equal(t, 5, g.maxFailures)
		require.Equal(t, 1*time.Minute, g.cooldownDuration)
		require.NotNil(t, g.nativeToolHandler)
	})

	t.Run("initializes field path registry", func(t *testing.T) {
		deps := Dependencies{
			Posture:          constants.PostureDoctrine,
			Logger:           slog.Default(),
			Responder:        response.NewWriter(slog.Default()),
			MaxPayloadBytes:  10 * 1024 * 1024,
			AuditStore:       noopAuditEventRecorder{},
			EmbeddedSession:  fakeEmbeddedSession{sessionID: testEmbeddedSessionID},
			ScrubbingService: newTestScrubbingService(t),
		}

		g, err := NewGatewayService(deps)
		require.NoError(t, err)

		require.NotNil(t, g.fieldPathRegistry)
	})

	t.Run("returns error when field path registry factory fails", func(t *testing.T) {
		factoryErr := fmt.Errorf("simulated registry initialization failure")
		deps := Dependencies{
			Posture:          constants.PostureDoctrine,
			Logger:           slog.Default(),
			Responder:        response.NewWriter(slog.Default()),
			MaxPayloadBytes:  10 * 1024 * 1024,
			AuditStore:       noopAuditEventRecorder{},
			EmbeddedSession:  fakeEmbeddedSession{sessionID: testEmbeddedSessionID},
			ScrubbingService: newTestScrubbingService(t),
			FieldPathRegistryFactory: func(*slog.Logger) (*FieldPathRegistry, error) {
				return nil, factoryErr
			},
		}

		g, err := NewGatewayService(deps)
		require.Error(t, err)
		require.Nil(t, g)
		require.Contains(t, err.Error(), "gateway: initialize field path registry")
		require.ErrorIs(t, err, factoryErr)
	})
}

func TestGatewayService_NilAuditStore_ReturnsError(t *testing.T) {
	deps := Dependencies{
		Posture:         constants.PostureDoctrine,
		Logger:          slog.Default(),
		Responder:       response.NewWriter(slog.Default()),
		SuspendedStore:  &fakeSuspendedStore{},
		MaxPayloadBytes: 10 * 1024 * 1024,
		// AuditStore intentionally omitted — a missing audit store is a wiring
		// bug, not a no-op condition. NewGatewayService must fail closed.
	}

	g, err := NewGatewayService(deps)
	require.Error(t, err)
	require.Nil(t, g)
	require.ErrorIs(t, err, constants.ErrAuditStoreRequired)
}

func TestGatewayService_AuditStore_InjectedRecorderReceivesEvents(t *testing.T) {
	recorder := &recordingAuditEventRecorder{}
	g := newTestGatewayService(t, withAuditStore(recorder))

	require.NoError(t, g.StoreSuspendedTransaction(
		context.Background(),
		"hash-audit-test",
		suspendableEnvelope(t, "op-session-audit"),
		"test-tool",
		json.RawMessage(`{"arg":"val"}`),
		"user-1",
		"app-1",
		"cert-fp",
	))

	require.Len(t, recorder.events, 1)
	require.Equal(t, "op-session-audit", recorder.events[0].OperatorSessionID)
	require.Equal(t, constants.Event.Operator.Notary.ApprovalRequested, recorder.events[0].Type)
}

type recordingAuditEventRecorder struct {
	events []*storage.Event
}

func (r *recordingAuditEventRecorder) RecordEvent(event *storage.Event) (int64, error) {
	r.events = append(r.events, event)
	return int64(len(r.events)), nil
}

// RecordEventOnce mirrors SQLAuditStore.RecordEventOnce: an event with the same
// transaction_id and type as one already recorded is not recorded again.
func (r *recordingAuditEventRecorder) RecordEventOnce(event *storage.Event) (int64, bool, error) {
	for i, existing := range r.events {
		if existing.TransactionID == event.TransactionID && existing.Type == event.Type {
			return int64(i + 1), false, nil
		}
	}
	id, err := r.RecordEvent(event)
	return id, err == nil, err
}

type failingAuditEventRecorder struct{ err error }

func (r failingAuditEventRecorder) RecordEvent(*storage.Event) (int64, error) { return 0, r.err }

func (r failingAuditEventRecorder) RecordEventOnce(*storage.Event) (int64, bool, error) {
	return 0, false, r.err
}

func TestGatewayService_StoreSuspendedTransaction_FailsClosed(t *testing.T) {
	t.Run("envelope without Operator session is not suspended", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		recorder := &recordingAuditEventRecorder{}
		g := newTestGatewayService(t, withSuspendedStore(store), withAuditStore(recorder))

		err := g.StoreSuspendedTransaction(context.Background(), "hash-app", suspendableEnvelope(t, ""), "test-tool", json.RawMessage(`{}`), "user-1", "app-1", "")

		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		require.Empty(t, store.txs)
		require.Empty(t, recorder.events, "no approval event for a suspension that was never stored")
	})

	t.Run("undecodable envelope is not suspended", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withSuspendedStore(store))

		err := g.StoreSuspendedTransaction(context.Background(), "hash-bad", []byte(`not json`), "test-tool", json.RawMessage(`{}`), "user-1", "app-1", "")

		require.Error(t, err)
		require.Empty(t, store.txs)
	})

	t.Run("unrecordable approval event removes the suspension", func(t *testing.T) {
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withSuspendedStore(store), withAuditStore(failingAuditEventRecorder{err: constants.ErrAuditSessionUnknown}))

		err := g.StoreSuspendedTransaction(context.Background(), "hash-unaudited", suspendableEnvelope(t, "operator-session-1"), "test-tool", json.RawMessage(`{}`), "user-1", "app-1", "")

		require.ErrorIs(t, err, constants.ErrAuditSessionUnknown)
		require.Empty(t, store.txs, "an approval must never be pending without its approval.requested record")
	})
}

// fakeEmbeddedSession resolves a fixed embedded Operator session, or fails
// with err when the embedded Operator is unclaimed.
type fakeEmbeddedSession struct {
	sessionID string
	err       error
}

func (f fakeEmbeddedSession) OperatorSessionID(context.Context) (string, error) {
	return f.sessionID, f.err
}

func withEmbeddedSession(r EmbeddedSessionResolver) testGatewayOption {
	return func(g *GatewayService) {
		g.embeddedSession = r
	}
}

const testEmbeddedSessionID = "embedded-operator-session"

func TestGatewayService_HandleToolsCall_AppCredentialSuspendsOnEmbeddedSession(t *testing.T) {
	store := &fakeSuspendedStore{}
	recorder := &recordingAuditEventRecorder{}
	g := newTestGatewayService(t,
		withEnvProc(&fakeEnvelopeProcessor{err: governance.ErrL3ProofMissing}),
		withSuspendedStore(store),
		withAuditStore(recorder))
	g.posture = &governance.NotaryPosture{}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{}}}`))
	req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyAppID, "calling-app"))
	w := httptest.NewRecorder()

	g.HandleMCP(w, req)

	require.Len(t, store.txs, 1)
	for hash, tx := range store.txs {
		require.Equal(t, testEmbeddedSessionID, tx.OperatorSessionID)
		require.Contains(t, w.Body.String(), "/approve/"+hash)

		env := &commonv1.GovernanceEnvelope{}
		require.NoError(t, protojson.Unmarshal(tx.Envelope, env))
		require.Equal(t, testEmbeddedSessionID, env.GetOperatorSessionId())
		require.Equal(t, "calling-app", env.GetActingAppId(), "app identity stays in acting_app_id")
		require.Equal(t, string(constants.DocIDEmbeddedOperator), env.GetOperatorId())
	}
	require.NotContains(t, w.Body.String(), `"error"`)
	require.Len(t, recorder.events, 1)
	require.Equal(t, constants.Event.Operator.Notary.ApprovalRequested, recorder.events[0].Type)
	require.Equal(t, testEmbeddedSessionID, recorder.events[0].OperatorSessionID)
}

func TestGatewayService_HandleToolsCall_AppCredentialFailsClosedWhenEmbeddedOperatorUnclaimed(t *testing.T) {
	store := &fakeSuspendedStore{}
	proc := &fakeEnvelopeProcessor{err: governance.ErrL3ProofMissing}
	g := newTestGatewayService(t,
		withEnvProc(proc),
		withSuspendedStore(store),
		withEmbeddedSession(fakeEmbeddedSession{err: constants.ErrEmbeddedOperatorUnclaimed}))
	g.posture = &governance.NotaryPosture{}

	t.Run("processGatewayTransaction", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyAppID, "calling-app")
		_, envelope, _, err := g.processGatewayTransaction(ctx, processGatewayOptions{eventType: constants.Event.Operator.Mcp.CallRequested, targetResource: "tool", payloadBytes: []byte("payload")})
		require.ErrorIs(t, err, constants.ErrEmbeddedOperatorUnclaimed)
		require.Nil(t, envelope)
	})

	t.Run("HandleMCP", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test-tool","arguments":{}}}`))
		req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyAppID, "calling-app"))
		w := httptest.NewRecorder()

		g.HandleMCP(w, req)

		require.Empty(t, store.txs)
		require.NotContains(t, w.Body.String(), "/approve/")
		require.Contains(t, w.Body.String(), `"error"`)
	})
}

func TestGatewayService_NewGatewayService_RequiresScrubbingService(t *testing.T) {
	g, err := NewGatewayService(Dependencies{
		Posture:         constants.PostureDoctrine,
		Logger:          slog.Default(),
		Responder:       response.NewWriter(slog.Default()),
		AuditStore:      noopAuditEventRecorder{},
		EmbeddedSession: fakeEmbeddedSession{sessionID: testEmbeddedSessionID},
	})
	require.ErrorIs(t, err, constants.ErrScrubbingServiceRequired)
	require.Nil(t, g)
}

func TestGatewayService_NewGatewayService_RequiresEmbeddedSession(t *testing.T) {
	g, err := NewGatewayService(Dependencies{
		Posture:    constants.PostureDoctrine,
		Logger:     slog.Default(),
		Responder:  response.NewWriter(slog.Default()),
		AuditStore: noopAuditEventRecorder{},
	})
	require.ErrorIs(t, err, constants.ErrEmbeddedSessionResolverRequired)
	require.Nil(t, g)
}

func TestGatewayService_HandleReadField(t *testing.T) {

	t.Run("field path registry not initialized", func(t *testing.T) {
		g := newTestGatewayService(t, withFieldPathRegistry(nil))

		_, err := g.handleReadField(context.Background(), json.RawMessage(`{}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "field path registry not initialized")
	})

	t.Run("database service not configured", func(t *testing.T) {
		registry, _ := NewFieldPathRegistry(slog.Default())
		g := newTestGatewayService(t, withFieldPathRegistry(registry), withDBService(nil))

		_, err := g.handleReadField(context.Background(), json.RawMessage(`{}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "database service not configured")
	})

	t.Run("invalid JSON arguments", func(t *testing.T) {
		registry, _ := NewFieldPathRegistry(slog.Default())
		g := newTestGatewayService(t, withFieldPathRegistry(registry), withDBService(&fakeDBService{}))

		_, err := g.handleReadField(context.Background(), json.RawMessage(`invalid json`))
		require.Error(t, err)
		require.Error(t, err)
	})

	t.Run("missing required fields", func(t *testing.T) {
		registry, _ := NewFieldPathRegistry(slog.Default())
		g := newTestGatewayService(t, withFieldPathRegistry(registry), withDBService(&fakeDBService{}))

		testCases := []struct {
			name  string
			args  string
			error string
		}{
			{"missing collection", `{"document_id":"doc1","field_path":"field1","operator_session_id":"sess1"}`, "collection required"},
			{"missing document_id", `{"collection":"coll1","field_path":"field1","operator_session_id":"sess1"}`, "document_id required"},
			{"missing field_path", `{"collection":"coll1","document_id":"doc1","operator_session_id":"sess1"}`, "field_path required"},
			{"missing operator_session_id", `{"collection":"coll1","document_id":"doc1","field_path":"field1"}`, "operator_session_id required"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := g.handleReadField(context.Background(), json.RawMessage(tc.args))
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.error)
			})
		}
	})

	t.Run("field path validation failed", func(t *testing.T) {
		registry, _ := NewFieldPathRegistry(slog.Default())
		g := newTestGatewayService(t, withFieldPathRegistry(registry), withDBService(&fakeDBService{}))

		args := `{"collection":"investigations","document_id":"doc1","field_path":"credentials.api_key","operator_session_id":"sess1"}`
		_, err := g.handleReadField(context.Background(), json.RawMessage(args))
		require.Error(t, err)
		require.Error(t, err)
	})

	t.Run("session validation failed", func(t *testing.T) {
		registry, _ := NewFieldPathRegistry(slog.Default())
		validator := &fakeSessionValidator{valid: false}
		g := newTestGatewayService(t, withFieldPathRegistry(registry), withDBService(&fakeDBService{}), withSessionValidator(validator))

		args := `{"collection":"investigations","document_id":"doc1","field_path":"status","operator_session_id":"sess1"}`
		_, err := g.handleReadField(context.Background(), json.RawMessage(args))
		require.Error(t, err)
		require.Contains(t, err.Error(), "operator session is invalid or expired")
	})

	t.Run("successful field read", func(t *testing.T) {
		registry, _ := NewFieldPathRegistry(slog.Default())
		db := &fakeDBService{}
		validator := &fakeSessionValidator{valid: true}
		audit := &fakeAuditLogger{}
		g := newTestGatewayService(t, withFieldPathRegistry(registry), withDBService(db), withSessionValidator(validator), withAuditLogger(audit))

		args := `{"collection":"investigations","document_id":"doc1","field_path":"status","operator_session_id":"sess1"}`
		result, err := g.handleReadField(context.Background(), json.RawMessage(args))
		require.NoError(t, err)
		require.NotNil(t, result)

		toolResult, ok := result.(CallToolResult)
		require.True(t, ok)
		require.Len(t, toolResult.Content, 1)
		require.Equal(t, "text", toolResult.Content[0].Type)
		require.Contains(t, toolResult.Content[0].Text, "test-value")
	})

	t.Run("forbidden pattern in field value", func(t *testing.T) {
		registry, _ := NewFieldPathRegistry(slog.Default())
		s := "password=secret123"
		db := &fakeDBService{fieldValue: &FieldValue{Str: &s}}
		validator := &fakeSessionValidator{valid: true}
		g := newTestGatewayService(t, withFieldPathRegistry(registry), withDBService(db), withSessionValidator(validator))

		args := `{"collection":"investigations","document_id":"doc1","field_path":"status","operator_session_id":"sess1"}`
		_, err := g.handleReadField(context.Background(), json.RawMessage(args))
		require.Error(t, err)
		require.Error(t, err)
	})
}

func TestGatewayService_HandleA2ARequest(t *testing.T) {

	t.Run("method not allowed", func(t *testing.T) {
		g := newTestGatewayService(t)

		req := httptest.NewRequest(http.MethodGet, "/mcp/test", nil)
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return nil, nil
		})

		require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("circuit breaker open", func(t *testing.T) {
		g := newTestGatewayService(t, withCircuitBreaker(3, 1*time.Minute))

		// Open the circuit
		for i := 0; i < 5; i++ {
			g.recordFailure()
		}

		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"test/method"}`))
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return nil, nil
		})

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Contains(t, resp.Error.Message, "circuit open")
	})

	t.Run("payload too large", func(t *testing.T) {
		g := newTestGatewayService(t, withMaxPayloadBytes(1024))

		largeBody := strings.Repeat("a", 2048)
		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"test/method","params":"%s"}`, largeBody)))
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return nil, nil
		})

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Equal(t, constants.ErrPayloadExceedsLimit.Error(), resp.Error.Message)
		require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_PAYLOAD_TOO_LARGE,
			decodeRefusal(t, resp.Error.Data).GetCode())
	})

	t.Run("invalid JSON", func(t *testing.T) {
		g := newTestGatewayService(t)

		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`invalid json`))
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return nil, nil
		})

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Equal(t, constants.ErrInvalidJSONBody.Error(), resp.Error.Message)
		require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			decodeRefusal(t, resp.Error.Data).GetCode())
	})

	t.Run("invalid JSON-RPC version", func(t *testing.T) {
		g := newTestGatewayService(t)

		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{"jsonrpc":"1.0","id":1,"method":"test/method"}`))
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return nil, nil
		})

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Equal(t, constants.ErrJSONRPCVersionInvalid.Error(), resp.Error.Message)
		require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			decodeRefusal(t, resp.Error.Data).GetCode())
	})

	t.Run("missing method", func(t *testing.T) {
		g := newTestGatewayService(t)

		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{"jsonrpc":"2.0","id":1}`))
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return nil, nil
		})

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Contains(t, resp.Error.Message, "method required")
	})

	t.Run("method mismatch", func(t *testing.T) {
		g := newTestGatewayService(t)

		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"wrong/method"}`))
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return nil, nil
		})

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Contains(t, resp.Error.Message, "method not found")
	})

	t.Run("handler error", func(t *testing.T) {
		g := newTestGatewayService(t)

		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"test/method"}`))
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return nil, fmt.Errorf("handler error")
		})

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Equal(t, constants.ErrInternal.Error(), resp.Error.Message)
		require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED,
			decodeRefusal(t, resp.Error.Data).GetCode())
	})

	t.Run("successful request", func(t *testing.T) {
		g := newTestGatewayService(t)

		req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"test/method"}`))
		w := httptest.NewRecorder()

		g.handleA2ARequest(w, req, "test/method", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
			return map[string]string{"result": "success"}, nil
		})

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Nil(t, resp.Error)
		require.NotNil(t, resp.Result)
	})
}

func TestGatewayService_Dependencies(t *testing.T) {

	t.Run("envProc nil before initialization", func(t *testing.T) {
		g := &GatewayService{logger: slog.Default(), posture: &governance.DoctrinePosture{}}
		require.Nil(t, g.envProc, "envProc should be nil initially")
	})

	t.Run("runtime fields accessible when configured", func(t *testing.T) {
		_, privKey, _ := ed25519.GenerateKey(rand.Reader)
		mock := &mockL2ConsensusDeliberator{}
		envProc := &fakeEnvelopeProcessor{}
		stateRoot := &fakeStateRootProvider{root: "test"}
		g := &GatewayService{
			posture:                &governance.DoctrinePosture{},
			logger:                 slog.Default(),
			envProc:                envProc,
			stateRootProvider:      stateRoot,
			signingKey:             privKey,
			keyID:                  "key-1",
			downstreamURL:          "http://downstream",
			l2ConsensusDeliberator: mock,
		}

		require.Same(t, envProc, g.envProc)
		require.Same(t, stateRoot, g.stateRootProvider)
		require.Equal(t, privKey, g.signingKey)
		require.Equal(t, "key-1", g.keyID)
		require.Equal(t, "http://downstream", g.downstreamURL)
		assert.Same(t, mock, g.l2ConsensusDeliberator)
	})

	t.Run("dispatchMCP returns not-ready error before deps are wired", func(t *testing.T) {
		g := &GatewayService{
			posture:         &governance.DoctrinePosture{},
			logger:          slog.Default(),
			responder:       response.NewWriter(slog.Default()),
			maxPayloadBytes: 10 * 1024 * 1024,
		}
		reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test","arguments":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
		w := httptest.NewRecorder()
		g.HandleMCP(w, req)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Contains(t, resp.Error.Message, "not ready")
	})

	t.Run("dispatchMCP allows initialize without deps", func(t *testing.T) {
		g := &GatewayService{
			posture:         &governance.DoctrinePosture{},
			logger:          slog.Default(),
			responder:       response.NewWriter(slog.Default()),
			maxPayloadBytes: 10 * 1024 * 1024,
		}
		reqBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
		w := httptest.NewRecorder()
		g.HandleMCP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp JSONRPCResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp.Result)
	})

	t.Run("dispatchMCP allows ping without deps", func(t *testing.T) {
		g := &GatewayService{
			posture:         &governance.DoctrinePosture{},
			logger:          slog.Default(),
			responder:       response.NewWriter(slog.Default()),
			maxPayloadBytes: 10 * 1024 * 1024,
		}
		reqBody := `{"jsonrpc":"2.0","id":42,"method":"ping"}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
		w := httptest.NewRecorder()
		g.HandleMCP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp JSONRPCResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp.Result)
	})
}

func TestGatewayService_RunMaintenance(t *testing.T) {
	g := newTestGatewayService(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run maintenance for a short time - should not panic
	g.RunMaintenance(ctx)
}

// Helper fakes for dependency tests

type fakeDBService struct {
	fieldValue *FieldValue
}

func (f *fakeDBService) GetField(_ context.Context, collection, id, fieldPath string) (FieldValue, error) {
	if f.fieldValue != nil {
		return *f.fieldValue, nil
	}
	s := "test-value"
	return FieldValue{Str: &s}, nil
}

type fakeSessionValidator struct {
	valid bool
}

func (f *fakeSessionValidator) ValidateSession(operatorSessionID string) (bool, error) {
	return f.valid, nil
}

type fakeAuditLogger struct{}

func (f *fakeAuditLogger) LogFieldRead(operatorSessionID, collection, documentID, fieldPath string, value FieldValue) error {
	return nil
}

// testGatewayOption is a functional option for configuring test GatewayService instances
type testGatewayOption func(*GatewayService)

// withEnvProc sets a custom envelope processor for the test GatewayService
func withEnvProc(proc governance.EnvelopeProcessor) testGatewayOption {
	return func(g *GatewayService) {
		g.envProc = proc
	}
}

// withSuspendedStore sets a custom suspended store for the test GatewayService
func withSuspendedStore(store storage.SuspendedTransactionStore) testGatewayOption {
	return func(g *GatewayService) {
		g.suspendedStore = store
	}
}

// withAuditStore sets a custom audit store for the test GatewayService
func withAuditStore(auditStore AuditEventRecorder) testGatewayOption {
	return func(g *GatewayService) {
		g.auditStore = auditStore
	}
}

// withDownstreamURL sets a custom downstream URL for the test GatewayService
func withDownstreamURL(url string) testGatewayOption {
	return func(g *GatewayService) {
		g.downstreamURL = url
	}
}

// withA2ADownstreamURL sets a custom A2A downstream URL for the test GatewayService
func withA2ADownstreamURL(url string) testGatewayOption {
	return func(g *GatewayService) {
		g.a2aDownstreamURL = url
	}
}

// withCircuitBreaker sets custom circuit breaker settings for the test GatewayService
func withCircuitBreaker(maxFailures int, cooldown time.Duration) testGatewayOption {
	return func(g *GatewayService) {
		g.maxFailures = maxFailures
		g.cooldownDuration = cooldown
	}
}

// withScrubbingService sets a custom scrubbing service for the test GatewayService
func withScrubbingService(svc *scrubbing.ScrubbingService) testGatewayOption {
	return func(g *GatewayService) {
		g.scrubbingService = svc
	}
}

// withMaxPayloadBytes sets a custom max payload size for the test GatewayService
func withMaxPayloadBytes(bytes int64) testGatewayOption {
	return func(g *GatewayService) {
		g.maxPayloadBytes = bytes
	}
}

// withFieldPathRegistry sets a custom field path registry for the test GatewayService
func withFieldPathRegistry(registry *FieldPathRegistry) testGatewayOption {
	return func(g *GatewayService) {
		g.fieldPathRegistry = registry
	}
}

// withDBService sets a custom database service for the test GatewayService
func withDBService(db FieldReader) testGatewayOption {
	return func(g *GatewayService) {
		g.dbService = db
	}
}

// withSessionValidator sets a custom session validator for the test GatewayService
func withSessionValidator(v SessionValidator) testGatewayOption {
	return func(g *GatewayService) {
		g.sessionValidator = v
	}
}

// withAuditLogger sets a custom audit logger for the test GatewayService
func withAuditLogger(audit AuditLogger) testGatewayOption {
	return func(g *GatewayService) {
		g.auditLogger = audit
	}
}

// newTestGatewayService creates a GatewayService with sensible defaults for testing.
// Options can be provided to override specific fields.
// newTestScrubbingService builds an enabled egress scrubber with the default configuration.
func newTestScrubbingService(t *testing.T) *scrubbing.ScrubbingService {
	t.Helper()
	svc, err := scrubbing.NewScrubbingService(context.Background(), scrubbing.DefaultConfig(), slog.Default(), storagetest.NewTestTokenStore())
	require.NoError(t, err)
	return svc
}

func newTestGatewayService(t *testing.T, opts ...testGatewayOption) *GatewayService {
	// Generate default signing key
	_, privKey, _ := ed25519.GenerateKey(rand.Reader)

	g := &GatewayService{
		posture:           &governance.DoctrinePosture{},
		logger:            slog.Default(),
		responder:         response.NewWriter(slog.Default()),
		suspendedStore:    &fakeSuspendedStore{},
		auditStore:        noopAuditEventRecorder{},
		scrubbingService:  newTestScrubbingService(t),
		threatScanner:     governance.NewL1Doctrine(),
		publicBaseURL:     network.LocalhostHTTPSURL(constants.Ports.OperatorHttps),
		maxFailures:       5,
		cooldownDuration:  1 * time.Minute,
		maxPayloadBytes:   10 * 1024 * 1024,
		envProc:           &fakeEnvelopeProcessor{},
		stateRootProvider: &fakeStateRootProvider{root: "test-root"},
		signingKey:        privKey,
		keyID:             "test-key",
		embeddedSession:   fakeEmbeddedSession{sessionID: testEmbeddedSessionID},
	}
	nativeToolHandler, err := NewNativeToolHandler(nil)
	if err != nil {
		t.Fatalf("failed to create native tool handler: %v", err)
	}
	g.nativeToolHandler = nativeToolHandler

	// Apply options
	for _, opt := range opts {
		opt(g)
	}

	return g
}

func TestGatewayService_HandleA2aCall(t *testing.T) {

	t.Run("successful call", func(t *testing.T) {
		receipt := &operatorv1.ActionReceipt{
			TransactionId: "tx-1",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ResultSummary: "a2a result",
		}
		proc := &fakeEnvelopeProcessor{receipt: receipt}
		g := newTestGatewayService(t, withEnvProc(proc))

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"a2a/call","params":{"skill_name":"test-skill","payload":{"arg":"val"}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp/a2a/call", strings.NewReader(reqBody))
		w := httptest.NewRecorder()

		g.HandleA2aCall(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.Nil(t, resp.Error)
	})

	t.Run("missing skill_name", func(t *testing.T) {
		g := newTestGatewayService(t)

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"a2a/call","params":{"payload":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp/a2a/call", strings.NewReader(reqBody))
		w := httptest.NewRecorder()

		g.HandleA2aCall(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		require.Contains(t, resp.Error.Message, "skill_name required")
	})

	t.Run("L3 proof missing - suspension", func(t *testing.T) {
		proc := &fakeEnvelopeProcessor{err: governance.ErrL3ProofMissing}
		store := &fakeSuspendedStore{}
		g := newTestGatewayService(t, withEnvProc(proc), withSuspendedStore(store))

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"a2a/call","params":{"skill_name":"test-skill","payload":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp/a2a/call", strings.NewReader(reqBody))
		req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyOperatorSessionID, "operator-session-1"))
		w := httptest.NewRecorder()

		g.HandleA2aCall(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		require.Len(t, store.txs, 1)
		for _, tx := range store.txs {
			require.Equal(t, "operator-session-1", tx.OperatorSessionID)
		}
	})
}
