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
	"fmt"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// AuditReceiptQuery is the read interface into the configured local audit
// store for the governed native audit receipt tools. The Gateway production
// handler uses its local SQLAuditStore, which may contain best-effort remote
// Operator receipt projections. The interface exposes only receipt reads.
type AuditReceiptQuery interface {
	ListActionReceipts(scope models.AuditScope, limit, offset int) ([]*models.ActionReceiptRecord, error)
	ListActionReceiptsSince(since time.Time, limit int) ([]*models.ActionReceiptRecord, error)
	GetActionReceipt(transactionID string) (*models.ActionReceiptRecord, error)
}

// auditReceiptQueryCtxKey is the context key under which the
// NativeToolHandler injects the configured AuditReceiptQuery so the audit
// receipt tools can retrieve it during execution. It is unexported to keep
// the injection mechanism internal to the mcp package.
type auditReceiptQueryCtxKey struct{}

type auditReceiptListAuthority struct {
	operatorID string
	name       string
}

type auditReceiptListAuthorityCtxKey struct{}

// withAuditReceiptQuery returns a context carrying the provided
// AuditReceiptQuery so audit receipt tools can access the configured local
// audit store during native tool execution.
func withAuditReceiptQuery(ctx context.Context, q AuditReceiptQuery) context.Context {
	return context.WithValue(ctx, auditReceiptQueryCtxKey{}, q)
}

// auditReceiptQueryFromContext returns the AuditReceiptQuery injected by the
// NativeToolHandler, or nil if none is configured. Tools fail closed when
// this returns nil — a missing audit store is a wiring bug, not a no-op
// condition.
func auditReceiptQueryFromContext(ctx context.Context) AuditReceiptQuery {
	q, _ := ctx.Value(auditReceiptQueryCtxKey{}).(AuditReceiptQuery)
	return q
}

func withAuditReceiptListAuthority(ctx context.Context, authority auditReceiptListAuthority) context.Context {
	return context.WithValue(ctx, auditReceiptListAuthorityCtxKey{}, authority)
}

// AuditReceiptListTool queries signed ActionReceipt records from the operator
// audit vault under governed MCP tool dispatch. It is the governed
// replacement for unauthenticated HTTP audit polling: every invocation
// traverses the L1–L5 gauntlet and produces its own audit record.
type AuditReceiptListTool struct{}

// Name returns the tool identifier.
func (t *AuditReceiptListTool) Name() string {
	return "audit_receipt_list"
}

// Description returns a human-readable description.
func (t *AuditReceiptListTool) Description() string {
	return "Lists signed ActionReceipt records from the Gateway's local audit store, scoped to a required operator session and optionally narrowed to an investigation. When both filters are supplied they are intersected. Remote Operator receipts in this store are best-effort projections, not authoritative evidence. Supports optional action_type and not_before (RFC3339) filtering with limit/offset pagination. This read-only query runs as a governed MCP_CALL."
}

// InputSchema returns the JSON Schema for tool validation.
func (t *AuditReceiptListTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"operator_session_id": {
				Type:        "string",
				Description: "Operator session ID to scope the receipt query (required).",
			},
			"investigation_id": {
				Type:        "string",
				Description: "Optional investigation ID to further scope receipts within the operator session.",
			},
			"action_type": {
				Type:        "string",
				Description: "Optional action type filter (e.g. FILE_EDIT, DOCUMENT_UPDATE).",
			},
			"exclude_action_type": {
				Type:        "string",
				Description: "Optional action type to exclude before pagination.",
			},
			"not_before": {
				Type:        "string",
				Description: "Optional RFC3339 timestamp; only receipts executed at or after this time are returned.",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of receipts to return (default 50).",
			},
			"offset": {
				Type:        "integer",
				Description: "Pagination offset (default 0).",
			},
		},
		Required: []string{"operator_session_id"},
	}
}

// AuditReceiptListRequest is the params for the "audit_receipt_list" tool.
type AuditReceiptListRequest struct {
	OperatorSessionID string `json:"operator_session_id"`
	InvestigationID   string `json:"investigation_id,omitempty"`
	ActionType        string `json:"action_type,omitempty"`
	ExcludeActionType string `json:"exclude_action_type,omitempty"`
	NotBefore         string `json:"not_before,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	Offset            int    `json:"offset,omitempty"`
}

// AuditReceiptListResult is the result for the "audit_receipt_list" tool.
type AuditReceiptListResult struct {
	Receipts   []*models.ActionReceiptRecord `json:"receipts"`
	Count      int                           `json:"count"`
	OperatorID string                        `json:"operator_id,omitempty"`
	Authority  string                        `json:"authority,omitempty"`
	Error      string                        `json:"error,omitempty"`
}

// OperatorAuditReceiptListResult is the allowlisted result returned by the
// Operator-local audit_receipt_list route. It contains canonical signed
// receipts and the identity/scope metadata needed to establish authority.
type OperatorAuditReceiptListResult = models.OperatorAuditReceiptListResult

// Execute implements the tool logic.
func (t *AuditReceiptListTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	var req AuditReceiptListRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CallToolResult{}, fmt.Errorf("audit_receipt_list: unmarshal arguments: %w", err)
	}

	authority, operatorLocal := ctx.Value(auditReceiptListAuthorityCtxKey{}).(auditReceiptListAuthority)
	if operatorLocal {
		if authority.operatorID == "" {
			return CallToolResult{}, fmt.Errorf("audit_receipt_list: %w", constants.ErrOperatorIDMissing)
		}
		if req.InvestigationID == "" {
			return CallToolResult{}, fmt.Errorf("audit_receipt_list: %w", constants.ErrMCPAuditReceiptListInvestigationReq)
		}
	} else if req.OperatorSessionID == "" {
		return CallToolResult{}, fmt.Errorf("audit_receipt_list: %w", constants.ErrMCPAuditReceiptListOperatorSessionReq)
	}

	q := auditReceiptQueryFromContext(ctx)
	if q == nil {
		return CallToolResult{}, fmt.Errorf("audit_receipt_list: %w", constants.ErrMCPAuditReceiptQueryNotConfigured)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}

	scope := models.AuditScope{
		OperatorSessionID: req.OperatorSessionID,
		InvestigationID:   req.InvestigationID,
		ExcludeActionType: req.ExcludeActionType,
	}
	if operatorLocal {
		scope.OperatorID = authority.operatorID
		scope.ExcludeActionType = string(constants.ActionTypeMcpCall)
	}
	receipts, err := q.ListActionReceipts(scope, limit, req.Offset)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("audit_receipt_list: query: %w", err)
	}
	if operatorLocal {
		for _, receipt := range receipts {
			if receipt.OperatorID != authority.operatorID {
				return CallToolResult{}, fmt.Errorf("audit_receipt_list: receipt belongs to Operator %q, expected %q", receipt.OperatorID, authority.operatorID)
			}
		}
	}

	var notBefore time.Time
	if req.NotBefore != "" {
		parsed, perr := time.Parse(time.RFC3339, req.NotBefore)
		if perr != nil {
			return CallToolResult{}, fmt.Errorf("audit_receipt_list: %w: %w", constants.ErrMCPAuditReceiptParseNotBefore, perr)
		}
		notBefore = parsed
	}

	filtered := make([]*models.ActionReceiptRecord, 0, len(receipts))
	for _, rec := range receipts {
		if req.ActionType != "" && string(rec.ActionType) != req.ActionType {
			continue
		}
		if !notBefore.IsZero() && !rec.ExecutedAt.IsZero() && rec.ExecutedAt.Before(notBefore) {
			continue
		}
		filtered = append(filtered, rec)
	}

	if operatorLocal {
		signedReceipts := make([]*operatorv1.ActionReceipt, 0, len(filtered))
		for _, record := range filtered {
			if record.ActionReceipt == nil {
				return CallToolResult{}, fmt.Errorf("audit_receipt_list: transaction %q has no canonical signed receipt", record.TransactionID)
			}
			signedReceipts = append(signedReceipts, record.ActionReceipt)
		}
		result := OperatorAuditReceiptListResult{
			Receipts:        signedReceipts,
			Count:           len(signedReceipts),
			OperatorID:      authority.operatorID,
			Authority:       authority.name,
			InvestigationID: req.InvestigationID,
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return CallToolResult{}, fmt.Errorf("audit_receipt_list: %w: %w", constants.ErrMCPAuditReceiptMarshalResult, err)
		}
		return CallToolResult{Content: []TextContent{{Type: "text", Text: string(resultJSON)}}, IsError: false}, nil
	}
	result := AuditReceiptListResult{Receipts: filtered, Count: len(filtered)}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("audit_receipt_list: %w: %w", constants.ErrMCPAuditReceiptMarshalResult, err)
	}

	return CallToolResult{
		Content: []TextContent{
			{Type: "text", Text: string(resultJSON)},
		},
	}, nil
}

// OperatorAuditReceiptHandler exposes only the audit_receipt_list native tool
// against the executing Operator's local receipt store. Its authority context
// makes investigation_id mandatory and scopes the indexed query to the
// Operator identity, independent of the session that produced each receipt.
type OperatorAuditReceiptHandler struct {
	operatorID string
	query      AuditReceiptQuery
	tool       *AuditReceiptListTool
}

// NewOperatorAuditReceiptHandler constructs the local-only governed receipt
// query route. Both dependencies are required because a missing local query or
// identity cannot establish authoritative evidence.
func NewOperatorAuditReceiptHandler(operatorID string, query AuditReceiptQuery) (*OperatorAuditReceiptHandler, error) {
	if operatorID == "" {
		return nil, fmt.Errorf("operator audit receipt handler: %w", constants.ErrOperatorIDMissing)
	}
	if query == nil {
		return nil, fmt.Errorf("operator audit receipt handler: %w", constants.ErrMCPAuditReceiptQueryNotConfigured)
	}
	return &OperatorAuditReceiptHandler{operatorID: operatorID, query: query, tool: &AuditReceiptListTool{}}, nil
}

// Handle dispatches a typed MCP tools/call request to the shared native
// audit_receipt_list tool. Other tools are rejected because this route is
// intentionally limited to authoritative Operator-local receipt reads.
func (h *OperatorAuditReceiptHandler) Handle(ctx context.Context, req CallToolRequest) (CallToolResult, error) {
	if h == nil || h.tool == nil || h.query == nil || h.operatorID == "" {
		return CallToolResult{}, fmt.Errorf("operator audit receipt handler: %w", constants.ErrMCPAuditReceiptQueryNotConfigured)
	}
	if req.Name != h.tool.Name() {
		return CallToolResult{}, fmt.Errorf("operator audit receipt handler: unsupported tool %q", req.Name)
	}
	args := req.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	ctx = withAuditReceiptQuery(ctx, h.query)
	ctx = withAuditReceiptListAuthority(ctx, auditReceiptListAuthority{
		operatorID: h.operatorID,
		name:       "operator_local_ledger",
	})
	return h.tool.Execute(ctx, args)
}

// AuditReceiptGetTool retrieves a single signed ActionReceipt by transaction
// ID from the configured local audit store under governed MCP tool dispatch.
type AuditReceiptGetTool struct{}

// Name returns the tool identifier.
func (t *AuditReceiptGetTool) Name() string {
	return "audit_receipt_get"
}

// Description returns a human-readable description.
func (t *AuditReceiptGetTool) Description() string {
	return "Retrieves a single signed ActionReceipt by transaction_id from the Gateway's local audit store. Remote Operator receipts in this store are best-effort projections, not authoritative evidence."
}

// InputSchema returns the JSON Schema for tool validation.
func (t *AuditReceiptGetTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"transaction_id": {
				Type:        "string",
				Description: "Transaction ID of the receipt to retrieve (required).",
			},
		},
		Required: []string{"transaction_id"},
	}
}

// AuditReceiptGetRequest is the params for the "audit_receipt_get" tool.
type AuditReceiptGetRequest struct {
	TransactionID string `json:"transaction_id"`
}

// AuditReceiptGetResult is the result for the "audit_receipt_get" tool.
type AuditReceiptGetResult struct {
	Receipt *models.ActionReceiptRecord `json:"receipt,omitempty"`
	Error   string                      `json:"error,omitempty"`
}

// Execute implements the tool logic.
func (t *AuditReceiptGetTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	var req AuditReceiptGetRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CallToolResult{}, fmt.Errorf("audit_receipt_get: unmarshal arguments: %w", err)
	}

	if req.TransactionID == "" {
		return CallToolResult{}, fmt.Errorf("audit_receipt_get: %w", constants.ErrMCPAuditReceiptGetTransactionIDReq)
	}

	q := auditReceiptQueryFromContext(ctx)
	if q == nil {
		return CallToolResult{}, fmt.Errorf("audit_receipt_get: %w", constants.ErrMCPAuditReceiptQueryNotConfigured)
	}

	receipt, err := q.GetActionReceipt(req.TransactionID)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("audit_receipt_get: query: %w", err)
	}

	result := AuditReceiptGetResult{Receipt: receipt}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("audit_receipt_get: %w: %w", constants.ErrMCPAuditReceiptMarshalResult, err)
	}

	return CallToolResult{
		Content: []TextContent{
			{Type: "text", Text: string(resultJSON)},
		},
	}, nil
}
