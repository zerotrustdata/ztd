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
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jsonschema"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// stubAuditEventReceiptQuery is a Tier 1 stub implementation of AuditEventReceiptQuery
// for unit testing the audit event receipt native tools. It returns canned records
// and records the arguments it was called with so tests can assert dispatch.
type stubAuditEventReceiptQuery struct {
	listBySession []*models.EventReceiptRecord
	listSince     []*models.EventReceiptRecord
	getByID       map[string]*models.EventReceiptRecord
	listErr       error
	listSinceErr  error
	getErr        error

	lastListSession       string
	lastListInvestigation string
	lastListOperator      string
	lastExcludeAuditReads bool
	listCalls             int
	lastListLimit         int
	lastListOffset        int
	lastSinceTime         time.Time
	lastSinceLimit        int
	lastGetID             string
}

func (s *stubAuditEventReceiptQuery) ListEventReceipts(scope models.AuditScope, limit, offset int) ([]*models.EventReceiptRecord, error) {
	s.listCalls++
	s.lastListSession = scope.OperatorSessionID
	s.lastListInvestigation = scope.InvestigationID
	s.lastListOperator = scope.OperatorID
	s.lastExcludeAuditReads = scope.ExcludeAuditReads
	s.lastListLimit = limit
	s.lastListOffset = offset
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.listBySession, nil
}

func (s *stubAuditEventReceiptQuery) ListEventReceiptsSince(since time.Time, limit int) ([]*models.EventReceiptRecord, error) {
	s.lastSinceTime = since
	s.lastSinceLimit = limit
	if s.listSinceErr != nil {
		return nil, s.listSinceErr
	}
	return s.listSince, nil
}

func (s *stubAuditEventReceiptQuery) GetEventReceipt(transactionID string) (*models.EventReceiptRecord, error) {
	s.lastGetID = transactionID
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getByID[transactionID], nil
}

func sampleEventReceiptRecord(txID, _ string, actionType string, executedAt time.Time, _ operatorv1.ExecutionStatus) *models.EventReceiptRecord {
	return &models.EventReceiptRecord{
		TransactionID:     txID,
		TransactionHash:   "hash-" + txID,
		OperatorID:        "op-1",
		OperatorSessionID: "sess-1",
		RequestorUserID:   "user-1",
		ActingAppID:       "app-1",
		ActionType:        constants.ActionType(actionType),
		TargetResource:    "/tmp/file.txt",
		Status:            operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:     "ok",
		StateRootBefore:   "root-before",
		StateRootAfter:    "root-after",
		ExecutedAt:        executedAt,
		SignerKeyID:       "key-1",
		Signature:         "sig-" + txID,
		Timestamp:         executedAt,
	}
}

func TestAuditEventReceiptListTool_Name_Description_Schema(t *testing.T) {
	tool := &AuditEventReceiptListTool{}
	assert.Equal(t, "audit_event_receipt_list", tool.Name())
	assert.NotEmpty(t, tool.Description())
	schema := tool.InputSchema()
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
	assert.Contains(t, schema.Properties, "operator_session_id")
	assert.Contains(t, schema.Properties, "investigation_id")
	assert.NotContains(t, schema.Required, "operator_session_id", "the shared schema must allow Operator-local queries to span sessions")
	assert.Contains(t, schema.Properties["operator_session_id"].Description, "required there")
	assert.Contains(t, schema.Properties["investigation_id"].Description, "required for Operator-local queries")
	assert.Contains(t, schema.Properties["investigation_id"].Description, "Optional")
	assert.NotContains(t, schema.Required, "investigation_id")
}

func TestAuditEventReceiptListTool_InputSchemaAcceptsOperatorLocalSessionlessQuery(t *testing.T) {
	schemaWire, err := json.Marshal((&AuditEventReceiptListTool{}).InputSchema())
	require.NoError(t, err)
	compiled, err := jsonschema.NewCompiler().Compile(schemaWire)
	require.NoError(t, err)

	localQuery := []byte(`{"investigation_id":"investigation-1","exclude_audit_reads":true}`)
	assert.Empty(t, jsonschema.NewValidator().Validate(compiled, localQuery), "the declared schema must admit the Operator-local query shape")
}

func TestAuditEventReceiptListTool_Execute_Success(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	stub := &stubAuditEventReceiptQuery{
		listBySession: []*models.EventReceiptRecord{
			sampleEventReceiptRecord("tx-1", "sess-1", "FILE_EDIT", now, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED),
			sampleEventReceiptRecord("tx-2", "sess-1", "DOCUMENT_UPDATE", now.Add(-time.Minute), operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED),
		},
	}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1"})

	tool := &AuditEventReceiptListTool{}
	result, err := tool.Execute(ctx, args)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.False(t, result.IsError)

	var lr AuditEventReceiptListResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &lr))
	assert.Len(t, lr.EventReceipts, 2)
	assert.Equal(t, 2, lr.Count)
	assert.Equal(t, "sess-1", stub.lastListSession)
	assert.Empty(t, stub.lastListInvestigation)
	assert.Equal(t, 50, stub.lastListLimit)
	assert.Equal(t, 0, stub.lastListOffset)
}

func TestAuditEventReceiptListTool_Execute_InvestigationOnlyScopeFailsBeforeQuery(t *testing.T) {
	stub := &stubAuditEventReceiptQuery{}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, err := json.Marshal(AuditEventReceiptListRequest{
		InvestigationID: "investigation-1",
	})
	require.NoError(t, err)

	_, err = (&AuditEventReceiptListTool{}).Execute(ctx, args)
	require.ErrorIs(t, err, constants.ErrMCPAuditEventReceiptListOperatorSessionReq)
	assert.Zero(t, stub.listCalls, "investigation-only native queries are rejected before reading the Gateway store")
}

func TestAuditEventReceiptListTool_Execute_BothScopes(t *testing.T) {
	stub := &stubAuditEventReceiptQuery{}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, err := json.Marshal(AuditEventReceiptListRequest{
		OperatorSessionID: "sess-1",
		InvestigationID:   "investigation-1",
	})
	require.NoError(t, err)

	_, err = (&AuditEventReceiptListTool{}).Execute(ctx, args)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", stub.lastListSession)
	assert.Equal(t, "investigation-1", stub.lastListInvestigation)
}

func TestOperatorNativeToolHandler_AuditEventReceiptListUsesLocalAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	eventReceipt := sampleEventReceiptRecord("tx-command", "session-old", "EXECUTE_BASH", now, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED)
	eventReceipt.EventReceipt = &operatorv1.EventReceipt{TransactionId: "tx-command", Signature: "sig-tx-command"}
	stub := &stubAuditEventReceiptQuery{
		listBySession: []*models.EventReceiptRecord{eventReceipt},
	}
	handler, err := NewOperatorNativeToolHandler(nil, "op-1", stub, newTestFsGrep(t, testutil.TempDir(t)))
	require.NoError(t, err)
	args, err := json.Marshal(AuditEventReceiptListRequest{
		InvestigationID:   "investigation-1",
		ExcludeAuditReads: true,
	})
	require.NoError(t, err)

	result, err := handler.HandleTool(context.Background(), "audit_event_receipt_list", args)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	var list OperatorAuditEventReceiptListResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &list))
	assert.Equal(t, "op-1", list.OperatorID)
	assert.Equal(t, "operator_local_ledger", list.Authority)
	assert.Equal(t, "investigation-1", stub.lastListInvestigation)
	assert.Equal(t, "op-1", stub.lastListOperator)
	assert.True(t, stub.lastExcludeAuditReads)
	assert.Empty(t, stub.lastListSession, "local queries span Operator sessions")
	assert.Equal(t, "tx-command", list.EventReceipts[0].GetTransactionId())
}

func TestOperatorNativeToolHandler_AuditEventReceiptListRequiresInvestigation(t *testing.T) {
	stub := &stubAuditEventReceiptQuery{}
	handler, err := NewOperatorNativeToolHandler(nil, "op-1", stub, newTestFsGrep(t, testutil.TempDir(t)))
	require.NoError(t, err)

	_, err = handler.HandleTool(context.Background(), "audit_event_receipt_list", json.RawMessage(`{"operator_session_id":"session-1"}`))
	require.ErrorIs(t, err, constants.ErrMCPAuditEventReceiptListInvestigationReq)
	assert.Zero(t, stub.listCalls)

	_, err = handler.HandleTool(context.Background(), "audit_event_receipt_list", json.RawMessage(`{"investigation_id":"investigation-1"}`))
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	assert.Zero(t, stub.listCalls)

	_, err = handler.HandleTool(context.Background(), "audit_event_receipt_list", json.RawMessage(`{"investigation_id":"investigation-1","exclude_audit_reads":false}`))
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	assert.Zero(t, stub.listCalls)

	_, err = handler.HandleTool(context.Background(), "read_field", nil)
	require.ErrorIs(t, err, constants.ErrMCPNativeToolUnknown, "Gateway-local tools are not native Operator tools")
}

func TestNewOperatorNativeToolHandler_RequiresIdentityAndLocalStore(t *testing.T) {
	grep := newTestFsGrep(t, testutil.TempDir(t))
	_, err := NewOperatorNativeToolHandler(nil, "", &stubAuditEventReceiptQuery{}, grep)
	require.ErrorIs(t, err, constants.ErrOperatorIDMissing)
	_, err = NewOperatorNativeToolHandler(nil, "op-1", nil, grep)
	require.ErrorIs(t, err, constants.ErrMCPAuditEventReceiptQueryNotConfigured)
	_, err = NewOperatorNativeToolHandler(nil, "op-1", &stubAuditEventReceiptQuery{}, nil)
	require.ErrorIs(t, err, constants.ErrMCPFsGrepNotConfigured)
}

func TestAuditEventReceiptListTool_Execute_ActionTypeFilter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	stub := &stubAuditEventReceiptQuery{
		listBySession: []*models.EventReceiptRecord{
			sampleEventReceiptRecord("tx-1", "sess-1", "FILE_EDIT", now, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED),
			sampleEventReceiptRecord("tx-2", "sess-1", "DOCUMENT_UPDATE", now, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED),
		},
	}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1", ActionType: "FILE_EDIT"})

	tool := &AuditEventReceiptListTool{}
	result, err := tool.Execute(ctx, args)
	require.NoError(t, err)

	var lr AuditEventReceiptListResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &lr))
	require.Len(t, lr.EventReceipts, 1)
	assert.Equal(t, "tx-1", lr.EventReceipts[0].TransactionID)
	assert.Equal(t, "FILE_EDIT", string(lr.EventReceipts[0].ActionType))
}

func TestAuditEventReceiptListTool_Execute_NotBeforeFilter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	stub := &stubAuditEventReceiptQuery{
		listBySession: []*models.EventReceiptRecord{
			sampleEventReceiptRecord("tx-old", "sess-1", "FILE_EDIT", now.Add(-2*time.Hour), operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED),
			sampleEventReceiptRecord("tx-new", "sess-1", "FILE_EDIT", now, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED),
		},
	}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptListRequest{
		OperatorSessionID: "sess-1",
		ActionType:        "FILE_EDIT",
		NotBefore:         now.Add(-time.Hour).Format(time.RFC3339),
	})

	tool := &AuditEventReceiptListTool{}
	result, err := tool.Execute(ctx, args)
	require.NoError(t, err)

	var lr AuditEventReceiptListResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &lr))
	require.Len(t, lr.EventReceipts, 1)
	assert.Equal(t, "tx-new", lr.EventReceipts[0].TransactionID)
}

// The egress scrub keeps an event receipt list decodable: deterministic stage timings
// shaped like phone (10-digit) and card (16-digit) numbers stay numbers.
func TestAuditEventReceiptListTool_Execute_ScrubbedResultDecodes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := sampleEventReceiptRecord("tx-1", "sess-1", "FILE_EDIT", now, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED)
	record.EventReceipt = &operatorv1.EventReceipt{
		TransactionId:    "tx-1",
		ExecutedAtUnixMs: now.UnixMilli(),
		SignerKeyId:      "key-1",
		Signature:        "sig-tx-1",
		DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{{
			StageId:          "tx-1:DETERMINISTIC_STAGE_KIND_L1_DOCTRINE",
			Kind:             operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE,
			MonotonicStartNs: 5551234567,
			MonotonicEndNs:   4111111111111111,
			ClockDomain:      "ztd-operator-process",
		}},
	}
	stub := &stubAuditEventReceiptQuery{listBySession: []*models.EventReceiptRecord{record}}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1"})

	result, err := (&AuditEventReceiptListTool{}).Execute(ctx, args)
	require.NoError(t, err)
	scrubber, err := scrubbing.NewScrubbingService(context.Background(), scrubbing.DefaultConfig(), slog.Default(), storagetest.NewTestTokenStore())
	require.NoError(t, err)
	scrubbed := scrubber.ScrubText(context.Background(), result.Content[0].Text)

	var lr AuditEventReceiptListResult
	require.NoError(t, json.Unmarshal([]byte(scrubbed), &lr), scrubbed)
	require.Len(t, lr.EventReceipts, 1)
	stage := lr.EventReceipts[0].EventReceipt.GetDeterministicStageEvidence()[0]
	assert.Equal(t, int64(5551234567), stage.GetMonotonicStartNs())
	assert.Equal(t, int64(4111111111111111), stage.GetMonotonicEndNs())
}

func TestAuditEventReceiptListTool_Execute_Pagination(t *testing.T) {
	stub := &stubAuditEventReceiptQuery{listBySession: nil}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1", Limit: 10, Offset: 20})

	tool := &AuditEventReceiptListTool{}
	_, err := tool.Execute(ctx, args)
	require.NoError(t, err)
	assert.Equal(t, 10, stub.lastListLimit)
	assert.Equal(t, 20, stub.lastListOffset)
}

func TestAuditEventReceiptListTool_Execute_MissingOperatorSessionID(t *testing.T) {
	stub := &stubAuditEventReceiptQuery{}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptListRequest{})

	tool := &AuditEventReceiptListTool{}
	_, err := tool.Execute(ctx, args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrMCPAuditEventReceiptListOperatorSessionReq))
	assert.Zero(t, stub.listCalls)
}

func TestAuditEventReceiptListTool_Execute_QueryNotConfigured(t *testing.T) {
	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1"})

	tool := &AuditEventReceiptListTool{}
	_, err := tool.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrMCPAuditEventReceiptQueryNotConfigured))
}

func TestAuditEventReceiptListTool_Execute_InvalidNotBefore(t *testing.T) {
	ctx := withAuditEventReceiptQuery(context.Background(), &stubAuditEventReceiptQuery{})
	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1", NotBefore: "not-a-timestamp"})

	tool := &AuditEventReceiptListTool{}
	_, err := tool.Execute(ctx, args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrMCPAuditEventReceiptParseNotBefore))
}

func TestAuditEventReceiptListTool_Execute_QueryError(t *testing.T) {
	queryErr := errors.New("db offline")
	stub := &stubAuditEventReceiptQuery{listErr: queryErr}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1"})

	tool := &AuditEventReceiptListTool{}
	_, err := tool.Execute(ctx, args)
	require.Error(t, err)
	assert.ErrorIs(t, err, queryErr)
}

func TestAuditEventReceiptListTool_Execute_InvalidJSON(t *testing.T) {
	ctx := withAuditEventReceiptQuery(context.Background(), &stubAuditEventReceiptQuery{})
	tool := &AuditEventReceiptListTool{}
	_, err := tool.Execute(ctx, json.RawMessage(`{invalid`))
	require.Error(t, err)
}

func TestAuditEventReceiptGetTool_Name_Description_Schema(t *testing.T) {
	tool := &AuditEventReceiptGetTool{}
	assert.Equal(t, "audit_event_receipt_get", tool.Name())
	assert.NotEmpty(t, tool.Description())
	schema := tool.InputSchema()
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
	assert.Contains(t, schema.Properties, "transaction_id")
	assert.Contains(t, schema.Required, "transaction_id")
}

func TestAuditEventReceiptGetTool_Execute_Success(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := sampleEventReceiptRecord("tx-1", "sess-1", "FILE_EDIT", now, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED)
	stub := &stubAuditEventReceiptQuery{getByID: map[string]*models.EventReceiptRecord{"tx-1": rec}}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptGetRequest{TransactionID: "tx-1"})

	tool := &AuditEventReceiptGetTool{}
	result, err := tool.Execute(ctx, args)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.False(t, result.IsError)

	var gr AuditEventReceiptGetResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &gr))
	require.NotNil(t, gr.EventReceipt)
	assert.Equal(t, "tx-1", gr.EventReceipt.TransactionID)
	assert.Equal(t, "sess-1", gr.EventReceipt.OperatorSessionID)
	assert.Equal(t, "tx-1", stub.lastGetID)
}

func TestAuditEventReceiptGetTool_Execute_NotFound(t *testing.T) {
	stub := &stubAuditEventReceiptQuery{getByID: map[string]*models.EventReceiptRecord{}}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptGetRequest{TransactionID: "missing"})

	tool := &AuditEventReceiptGetTool{}
	result, err := tool.Execute(ctx, args)
	require.NoError(t, err)

	var gr AuditEventReceiptGetResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &gr))
	assert.Nil(t, gr.EventReceipt)
}

func TestAuditEventReceiptGetTool_Execute_MissingTransactionID(t *testing.T) {
	ctx := withAuditEventReceiptQuery(context.Background(), &stubAuditEventReceiptQuery{})
	args, _ := json.Marshal(AuditEventReceiptGetRequest{})

	tool := &AuditEventReceiptGetTool{}
	_, err := tool.Execute(ctx, args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrMCPAuditEventReceiptGetTransactionIDReq))
}

func TestAuditEventReceiptGetTool_Execute_QueryNotConfigured(t *testing.T) {
	args, _ := json.Marshal(AuditEventReceiptGetRequest{TransactionID: "tx-1"})

	tool := &AuditEventReceiptGetTool{}
	_, err := tool.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrMCPAuditEventReceiptQueryNotConfigured))
}

func TestAuditEventReceiptGetTool_Execute_QueryError(t *testing.T) {
	queryErr := errors.New("db offline")
	stub := &stubAuditEventReceiptQuery{getErr: queryErr}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)
	args, _ := json.Marshal(AuditEventReceiptGetRequest{TransactionID: "tx-1"})

	tool := &AuditEventReceiptGetTool{}
	_, err := tool.Execute(ctx, args)
	require.Error(t, err)
	assert.ErrorIs(t, err, queryErr)
}

func TestAuditEventReceiptGetTool_Execute_InvalidJSON(t *testing.T) {
	ctx := withAuditEventReceiptQuery(context.Background(), &stubAuditEventReceiptQuery{})
	tool := &AuditEventReceiptGetTool{}
	_, err := tool.Execute(ctx, json.RawMessage(`{invalid`))
	require.Error(t, err)
}

// TestNativeToolHandler_SetAuditEventReceiptQuery verifies the handler injects the
// configured AuditEventReceiptQuery into the execution context so the audit
// event receipt tools can access the configured local audit store.
func TestNativeToolHandler_SetAuditEventReceiptQuery(t *testing.T) {
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Millisecond)
	stub := &stubAuditEventReceiptQuery{
		listBySession: []*models.EventReceiptRecord{
			sampleEventReceiptRecord("tx-1", "sess-1", "FILE_EDIT", now, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED),
		},
	}
	handler.SetAuditEventReceiptQuery(stub)

	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1"})
	result, err := handler.HandleTool(context.Background(), "audit_event_receipt_list", args)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	var lr AuditEventReceiptListResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &lr))
	assert.Len(t, lr.EventReceipts, 1)
}

// TestNativeToolHandler_AuditEventReceiptQueryNotConfigured verifies the handler
// fails closed when no AuditEventReceiptQuery is wired, proving the audit event_receipt
// tools cannot execute without a configured audit vault.
func TestNativeToolHandler_AuditEventReceiptQueryNotConfigured(t *testing.T) {
	handler, err := NewNativeToolHandler(nil)
	require.NoError(t, err)

	args, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1"})
	_, err = handler.HandleTool(context.Background(), "audit_event_receipt_list", args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrMCPAuditEventReceiptQueryNotConfigured))
}

// TestAuditEventReceiptTools_VaultLockedIsSurfaced asserts that a locked runtime
// vault reaches the caller as ErrAuditStoreVaultLocked on both read tools, never
// as an empty event receipt list or a silent not-found.
func TestAuditEventReceiptTools_VaultLockedIsSurfaced(t *testing.T) {
	stub := &stubAuditEventReceiptQuery{listErr: constants.ErrAuditStoreVaultLocked, getErr: constants.ErrAuditStoreVaultLocked}
	ctx := withAuditEventReceiptQuery(context.Background(), stub)

	listArgs, _ := json.Marshal(AuditEventReceiptListRequest{OperatorSessionID: "sess-1"})
	_, err := (&AuditEventReceiptListTool{}).Execute(ctx, listArgs)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)

	getArgs, _ := json.Marshal(AuditEventReceiptGetRequest{TransactionID: "tx-1"})
	_, err = (&AuditEventReceiptGetTool{}).Execute(ctx, getArgs)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
}
