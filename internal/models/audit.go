// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
)

// AuditScope selects evidence by ingress identity and investigation. Empty
// fields leave that dimension unrestricted; app records need not have an
// Operator session. ExcludeAuditReads drops the audit-read tools' own MCP_CALL
// receipts before pagination; every other receipt stays in scope.
type AuditScope struct {
	OperatorID        string
	OperatorSessionID string
	InvestigationID   string
	ExcludeAuditReads bool
	ActingAppID       string
}

// AuditEventReceiptsResponse is the typed response for GET /api/v1/audit/event-receipts.
type AuditEventReceiptsResponse struct {
	Success       bool                  `json:"success"`
	EventReceipts []*EventReceiptRecord `json:"event_receipts"`
}

// AuditEventRow is a single audit event row in the events response.
type AuditEventRow struct {
	ID                int64  `json:"id"`
	TransactionID     string `json:"transaction_id,omitempty"`
	OperatorSessionID string `json:"operator_session_id"`
	Timestamp         string `json:"timestamp"`
	Type              string `json:"type"`
	CommandRaw        string `json:"command_raw"`
	CommandExitCode   int    `json:"command_exit_code"`
}

// AuditEventsResponse is the typed response for GET /api/audit/events.
type AuditEventsResponse struct {
	Success bool            `json:"success"`
	Events  []AuditEventRow `json:"events"`
	Count   int             `json:"count"`
}

// AuditReportData is the report payload within AuditReportResponse.
type AuditReportData struct {
	GeneratedAt        string            `json:"generated_at"`
	OperatorSessionID  string            `json:"operator_session_id"`
	ActingAppID        string            `json:"acting_app_id,omitempty"`
	Events             []json.RawMessage `json:"events"`
	EventsCount        int               `json:"events_count"`
	EventReceipts      []json.RawMessage `json:"event_receipts"`
	EventReceiptsCount int               `json:"event_receipts_count"`
	TotalRecords       int               `json:"total_records"`
}

// AuditReportResponse is the typed response for GET /api/audit/report.
type AuditReportResponse struct {
	Success bool            `json:"success"`
	Report  AuditReportData `json:"report"`
}

// AuditSummaryResponse is the typed response for GET /api/audit/summary.
type AuditSummaryResponse struct {
	Success              bool           `json:"success"`
	EventsSummary        map[string]int `json:"events_summary"`
	EventsTotal          int            `json:"events_total"`
	EventReceiptsSummary map[string]int `json:"event_receipts_summary"`
	EventReceiptsTotal   int            `json:"event_receipts_total"`
	TotalRecords         int            `json:"total_records"`
}

// AuditVerifyResponse is the typed response for GET /api/v1/audit/verify.
type AuditVerifyResponse struct {
	Success         bool   `json:"success"`
	OK              bool   `json:"ok"`
	VerifiedFromSeq int64  `json:"verified_from_seq"`
	HeadSeq         int64  `json:"head_seq"`
	HeadHash        string `json:"head_hash"`
	Error           string `json:"error,omitempty"`
}
