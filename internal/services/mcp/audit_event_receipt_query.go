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

// AuditEventReceiptQuery is the read interface into the configured local audit
// store for the governed native audit event receipt tools. The Gateway production
// handler uses its local SQLAuditStore, which may contain best-effort remote
// Operator event receipt projections. The interface exposes only event receipt reads.
type AuditEventReceiptQuery interface {
	ListEventReceipts(scope models.AuditScope, limit, offset int) ([]*models.EventReceiptRecord, error)
	ListEventReceiptsSince(since time.Time, limit int) ([]*models.EventReceiptRecord, error)
	GetEventReceipt(transactionID string) (*models.EventReceiptRecord, error)
}

// auditEventReceiptQueryCtxKey is the context key under which the
// NativeToolHandler injects the configured AuditEventReceiptQuery so the audit
// event receipt tools can retrieve it during execution. It is unexported to keep
// the injection mechanism internal to the mcp package.
type auditEventReceiptQueryCtxKey struct{}

type auditEventReceiptListAuthority struct {
	operatorID string
	name       string
}

type auditEventReceiptListAuthorityCtxKey struct{}

// withAuditEventReceiptQuery returns a context carrying the provided
// AuditEventReceiptQuery so audit event receipt tools can access the configured local
// audit store during native tool execution.
func withAuditEventReceiptQuery(ctx context.Context, q AuditEventReceiptQuery) context.Context {
	return context.WithValue(ctx, auditEventReceiptQueryCtxKey{}, q)
}

// auditEventReceiptQueryFromContext returns the AuditEventReceiptQuery injected by the
// NativeToolHandler, or nil if none is configured. Tools fail closed when
// this returns nil — a missing audit store is a wiring bug, not a no-op
// condition.
func auditEventReceiptQueryFromContext(ctx context.Context) AuditEventReceiptQuery {
	q, _ := ctx.Value(auditEventReceiptQueryCtxKey{}).(AuditEventReceiptQuery)
	return q
}

func withAuditEventReceiptListAuthority(ctx context.Context, authority auditEventReceiptListAuthority) context.Context {
	return context.WithValue(ctx, auditEventReceiptListAuthorityCtxKey{}, authority)
}

// AuditEventReceiptListTool queries signed EventReceipt records from the executing
// runtime's local audit store under governed MCP tool dispatch. It is the governed
// replacement for unauthenticated HTTP audit polling: every invocation
// traverses the L1–L5 gauntlet and produces its own audit record.
type AuditEventReceiptListTool struct{}

// Name returns the tool identifier.
func (t *AuditEventReceiptListTool) Name() string {
	return constants.MCPToolAuditEventReceiptList
}

// Description returns a human-readable description.
func (t *AuditEventReceiptListTool) Description() string {
	return "Lists signed EventReceipt records. Gateway-native queries require an operator session and optionally intersect an investigation; remote event receipts there are best-effort projections. Explicit Operator-local queries require investigation_id and exclude_audit_reads=true, return the executing Operator's authoritative signed event receipts, and may omit the session filter to span reconnects. Supports optional action_type and not_before (RFC3339) filtering with limit/offset pagination. This read-only query runs as a governed MCP_CALL."
}

// InputSchema returns the JSON Schema for tool validation.
func (t *AuditEventReceiptListTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"operator_session_id": {
				Type:        "string",
				Description: "Operator session ID to scope Gateway-native queries (required there); omit for Operator-local queries to span reconnects.",
			},
			"investigation_id": {
				Type:        "string",
				Description: "Optional for Gateway-native queries; required for Operator-local queries.",
			},
			"action_type": {
				Type:        "string",
				Description: "Optional action type filter (e.g. FILE_EDIT, DOCUMENT_UPDATE).",
			},
			"exclude_audit_reads": {
				Type:        "boolean",
				Description: "Exclude audit event receipt read event receipts before pagination. Operator-local queries require true.",
			},
			"not_before": {
				Type:        "string",
				Description: "Optional RFC3339 timestamp; only event receipts executed at or after this time are returned.",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of event receipts to return (default 50).",
			},
			"offset": {
				Type:        "integer",
				Description: "Pagination offset (default 0).",
			},
		},
		Required: []string{},
	}
}

// AuditEventReceiptListRequest is the params for the "audit_event_receipt_list" tool.
type AuditEventReceiptListRequest struct {
	OperatorSessionID string `json:"operator_session_id"`
	InvestigationID   string `json:"investigation_id,omitempty"`
	ActionType        string `json:"action_type,omitempty"`
	ExcludeAuditReads bool   `json:"exclude_audit_reads,omitempty"`
	NotBefore         string `json:"not_before,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	Offset            int    `json:"offset,omitempty"`
}

// AuditEventReceiptListResult is the result for the "audit_event_receipt_list" tool.
type AuditEventReceiptListResult struct {
	EventReceipts []*models.EventReceiptRecord `json:"event_receipts"`
	Count         int                          `json:"count"`
	OperatorID    string                       `json:"operator_id,omitempty"`
	Authority     string                       `json:"authority,omitempty"`
	Error         string                       `json:"error,omitempty"`
}

// OperatorAuditEventReceiptListResult is the allowlisted result returned by the
// Operator-local audit_event_receipt_list route. It contains canonical signed
// event receipts and the identity/scope metadata needed to establish authority.
type OperatorAuditEventReceiptListResult = models.OperatorAuditEventReceiptListResult

// Execute implements the tool logic.
func (t *AuditEventReceiptListTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	var req AuditEventReceiptListRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: unmarshal arguments: %w", err)
	}

	authority, operatorLocal := ctx.Value(auditEventReceiptListAuthorityCtxKey{}).(auditEventReceiptListAuthority)
	if operatorLocal {
		if authority.operatorID == "" {
			return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: %w", constants.ErrOperatorIDMissing)
		}
		if req.InvestigationID == "" {
			return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: %w", constants.ErrMCPAuditEventReceiptListInvestigationReq)
		}
		if !req.ExcludeAuditReads {
			return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: exclude_audit_reads: %w", constants.ErrMissingRequiredField)
		}
	} else if req.OperatorSessionID == "" {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: %w", constants.ErrMCPAuditEventReceiptListOperatorSessionReq)
	}

	q := auditEventReceiptQueryFromContext(ctx)
	if q == nil {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: %w", constants.ErrMCPAuditEventReceiptQueryNotConfigured)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}

	scope := models.AuditScope{
		OperatorSessionID: req.OperatorSessionID,
		InvestigationID:   req.InvestigationID,
		ExcludeAuditReads: req.ExcludeAuditReads,
	}
	if operatorLocal {
		scope.OperatorID = authority.operatorID
	}
	eventReceipts, err := q.ListEventReceipts(scope, limit, req.Offset)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: query: %w", err)
	}
	if operatorLocal {
		for _, eventReceipt := range eventReceipts {
			if eventReceipt.OperatorID != authority.operatorID {
				return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: event receipt belongs to Operator %q, expected %q", eventReceipt.OperatorID, authority.operatorID)
			}
		}
	}

	var notBefore time.Time
	if req.NotBefore != "" {
		parsed, perr := time.Parse(time.RFC3339, req.NotBefore)
		if perr != nil {
			return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: %w: %w", constants.ErrMCPAuditEventReceiptParseNotBefore, perr)
		}
		notBefore = parsed
	}

	filtered := make([]*models.EventReceiptRecord, 0, len(eventReceipts))
	for _, rec := range eventReceipts {
		if req.ActionType != "" && string(rec.ActionType) != req.ActionType {
			continue
		}
		if !notBefore.IsZero() && !rec.ExecutedAt.IsZero() && rec.ExecutedAt.Before(notBefore) {
			continue
		}
		filtered = append(filtered, rec)
	}

	if operatorLocal {
		signedEventReceipts := make([]*operatorv1.EventReceipt, 0, len(filtered))
		for _, record := range filtered {
			if record.EventReceipt == nil {
				return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: transaction %q has no canonical signed event receipt", record.TransactionID)
			}
			signedEventReceipts = append(signedEventReceipts, record.EventReceipt)
		}
		result := OperatorAuditEventReceiptListResult{
			EventReceipts:   signedEventReceipts,
			Count:           len(signedEventReceipts),
			OperatorID:      authority.operatorID,
			Authority:       authority.name,
			InvestigationID: req.InvestigationID,
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: %w: %w", constants.ErrMCPAuditEventReceiptMarshalResult, err)
		}
		return CallToolResult{Content: []TextContent{{Type: "text", Text: string(resultJSON)}}, IsError: false}, nil
	}
	result := AuditEventReceiptListResult{EventReceipts: filtered, Count: len(filtered)}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_list: %w: %w", constants.ErrMCPAuditEventReceiptMarshalResult, err)
	}

	return CallToolResult{
		Content: []TextContent{
			{Type: "text", Text: string(resultJSON)},
		},
	}, nil
}

// AuditEventReceiptGetTool retrieves a single signed EventReceipt by transaction
// ID from the executing runtime's local audit store under governed MCP tool
// dispatch.
type AuditEventReceiptGetTool struct{}

// Name returns the tool identifier.
func (t *AuditEventReceiptGetTool) Name() string {
	return constants.MCPToolAuditEventReceiptGet
}

// Description returns a human-readable description.
func (t *AuditEventReceiptGetTool) Description() string {
	return "Retrieves a single signed EventReceipt by transaction_id from the executing runtime's local audit store. An Operator's store is authoritative for its own event receipts; remote Operator event receipts in the Gateway's store are best-effort projections, not authoritative evidence."
}

// InputSchema returns the JSON Schema for tool validation.
func (t *AuditEventReceiptGetTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"transaction_id": {
				Type:        "string",
				Description: "Transaction ID of the event receipt to retrieve (required).",
			},
		},
		Required: []string{"transaction_id"},
	}
}

// AuditEventReceiptGetRequest is the params for the "audit_event_receipt_get" tool.
type AuditEventReceiptGetRequest struct {
	TransactionID string `json:"transaction_id"`
}

// AuditEventReceiptGetResult is the result for the "audit_event_receipt_get" tool.
type AuditEventReceiptGetResult struct {
	EventReceipt *models.EventReceiptRecord `json:"event_receipt,omitempty"`
	Error        string                     `json:"error,omitempty"`
}

// Execute implements the tool logic.
func (t *AuditEventReceiptGetTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	var req AuditEventReceiptGetRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_get: unmarshal arguments: %w", err)
	}

	if req.TransactionID == "" {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_get: %w", constants.ErrMCPAuditEventReceiptGetTransactionIDReq)
	}

	q := auditEventReceiptQueryFromContext(ctx)
	if q == nil {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_get: %w", constants.ErrMCPAuditEventReceiptQueryNotConfigured)
	}

	eventReceipt, err := q.GetEventReceipt(req.TransactionID)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_get: query: %w", err)
	}

	result := AuditEventReceiptGetResult{EventReceipt: eventReceipt}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("audit_event_receipt_get: %w: %w", constants.ErrMCPAuditEventReceiptMarshalResult, err)
	}

	return CallToolResult{
		Content: []TextContent{
			{Type: "text", Text: string(resultJSON)},
		},
	}, nil
}
