// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	investigationContextMaxEventReceipts = 50
	agentToolInvestigationCtx            = "query_investigation_context"
)

// AgentContextRetrieval records the exact query and event receipt references returned
// by the Data Operator. The signed query event receipt retains the full response.
type AgentContextRetrieval struct {
	Query         mcp.AuditEventReceiptListRequest `json:"query"`
	EventReceipts []AgentContextEventReceiptRef    `json:"event_receipts"`
}

type AgentContextEventReceiptRef struct {
	EventReceiptID string `json:"event_receipt_id"`
	Digest         string `json:"sha256"`
}

type agentInvestigationContextResult struct {
	Success         bool                      `json:"success"`
	DataType        string                    `json:"data_type"`
	InvestigationID string                    `json:"investigation_id"`
	OperatorID      string                    `json:"operator_id"`
	Authority       string                    `json:"authority"`
	CaseTitle       string                    `json:"case_title,omitempty"`
	CaseDescription string                    `json:"case_description,omitempty"`
	Events          []agentInvestigationEvent `json:"events,omitempty"`
}

type agentInvestigationEvent struct {
	EventReceiptID     string `json:"event_receipt_id"`
	EventReceiptDigest string `json:"event_receipt_digest"`
	EventType          string `json:"event_type"`
	Actor              string `json:"actor"`
	Summary            string `json:"summary"`
}

func (e *CampaignAgentExecutor) investigationID(req AssignmentExecutionRequest) (string, error) {
	if err := ValidateCampaignInvestigation(req.Investigation); err != nil {
		return "", err
	}
	if req.Investigation.GetMode() == evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT {
		if req.Binding.Topology.Data.OperatorID != req.Investigation.GetDataOperatorId() {
			return "", fmt.Errorf("evaluation: persistent investigation Data Operator binding: %w", constants.ErrEvaluationInvestigationInvalid)
		}
		return req.Investigation.GetInvestigationId(), nil
	}
	id := e.newID("investigation")
	if id == "" {
		return "", fmt.Errorf("evaluation: fresh investigation identity: %w", constants.ErrMissingRequiredField)
	}
	return id, nil
}

// decodeInvestigationEventReceipts trusts only the selected runtime's signed answer,
// then checks each returned event receipt against the same enrollment-bound key. The
// signed event receipt has no target resource, so the audit-read exclusion is the
// Operator's answer under the query's scope and is not re-derived here.
func decodeInvestigationEventReceipts(trace *AgentTrace, query mcp.AuditEventReceiptListRequest, eventReceipt *AgentTraceEventReceipt) (mcp.OperatorAuditEventReceiptListResult, []AgentContextEventReceiptRef, error) {
	fail := func(err error) (mcp.OperatorAuditEventReceiptListResult, []AgentContextEventReceiptRef, error) {
		return mcp.OperatorAuditEventReceiptListResult{}, nil, fmt.Errorf("evaluation: authoritative investigation context: %w", err)
	}
	if eventReceipt == nil || eventReceipt.EventReceipt == nil {
		return fail(constants.ErrEventReceiptMissing)
	}
	if trace.InvestigationID == "" || query.InvestigationID != trace.InvestigationID || query.OperatorSessionID != "" || query.Limit < 1 || query.Limit > investigationContextMaxEventReceipts {
		return fail(constants.ErrEvidenceScopeMismatch)
	}
	anchors, err := agentTraceAnchorSet(trace)
	if err != nil {
		return fail(err)
	}
	if _, err := oracle.VerifyEventReceipt(anchors, trace.Data.OperatorID, eventReceipt.EventReceipt); err != nil {
		return fail(err)
	}
	if eventReceipt.GetActionType() != string(constants.ActionTypeMcpCall) || eventReceipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fail(constants.ErrEvaluationEvidenceTraceMismatch)
	}
	var result mcp.OperatorAuditEventReceiptListResult
	if err := json.Unmarshal([]byte(eventReceipt.GetResultSummary()), &result); err != nil {
		return fail(err)
	}
	if result.OperatorID != trace.Data.OperatorID || result.Authority != "operator_local_ledger" || result.InvestigationID != query.InvestigationID || result.Count != len(result.EventReceipts) || result.Count > query.Limit {
		return fail(constants.ErrEvidenceScopeMismatch)
	}
	refs := make([]AgentContextEventReceiptRef, 0, len(result.EventReceipts))
	seen := make(map[string]bool, len(result.EventReceipts))
	for _, row := range result.EventReceipts {
		if row == nil {
			return fail(constants.ErrEventReceiptMissing)
		}
		r := row
		if r.GetTransactionId() == "" || seen[r.GetTransactionId()] {
			return fail(constants.ErrEvidenceScopeMismatch)
		}
		if _, err := oracle.VerifyEventReceipt(anchors, trace.Data.OperatorID, r); err != nil {
			return fail(err)
		}
		body, err := evalv1.MarshalCanonical(r)
		if err != nil {
			return fail(err)
		}
		refs = append(refs, AgentContextEventReceiptRef{EventReceiptID: r.GetTransactionId(), Digest: models.SHA256Hex(body)})
		seen[r.GetTransactionId()] = true
	}
	return result, refs, nil
}

func investigationEventReceiptContext(dataType, investigationID string, result mcp.OperatorAuditEventReceiptListResult, refs []AgentContextEventReceiptRef) agentInvestigationContextResult {
	answer := agentInvestigationContextResult{Success: true, DataType: dataType, InvestigationID: investigationID, OperatorID: result.OperatorID, Authority: result.Authority, Events: []agentInvestigationEvent{}}
	for i, row := range result.EventReceipts {
		answer.Events = append(answer.Events, agentInvestigationEvent{
			EventReceiptID: refs[i].EventReceiptID, EventReceiptDigest: refs[i].Digest,
			EventType: row.GetActionType(), Actor: investigationOperatorActor,
			Summary: truncateToolText(row.GetResultSummary()),
		})
	}
	return answer
}

// verifyContextRetrieval binds the model-visible view and references to the
// signed query answer, so a resealed trace cannot invent retrieved evidence.
func verifyContextRetrieval(trace *AgentTrace, call AgentToolCall) error {
	if call.Retrieval == nil {
		if (trace.CarriesModelIdentity() || trace.SchemaVersion == agentTraceSchemaVersionV6) && (call.ToolName == agentToolInvestigationCtx || call.ToolName == constants.MCPToolAuditEventReceiptList) {
			// A completed query cannot suppress its evidence by changing the
			// unsigned Success flag. Denied and failed calls have no retrieval.
			if call.EventReceipt != nil && call.EventReceipt.GetStatus() == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
				return constants.ErrEvaluationEvidenceTraceMismatch
			}
			if !call.Success {
				return nil
			}
			var args struct {
				DataType string `json:"data_type"`
			}
			if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
				return err
			}
			if args.DataType != investigationDataStatus {
				return constants.ErrEvaluationEvidenceTraceMismatch
			}
		}
		return nil
	}
	if (call.ToolName != agentToolInvestigationCtx && call.ToolName != constants.MCPToolAuditEventReceiptList) || !call.Success || call.ActionType != string(constants.ActionTypeMcpCall) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	result, refs, err := decodeInvestigationEventReceipts(trace, call.Retrieval.Query, call.EventReceipt)
	if err != nil {
		return err
	}
	if len(refs) != len(call.Retrieval.EventReceipts) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	for i := range refs {
		if refs[i] != call.Retrieval.EventReceipts[i] {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
	}
	if call.ToolName == constants.MCPToolAuditEventReceiptList {
		var req mcp.AuditEventReceiptListRequest
		if err := json.Unmarshal([]byte(call.ArgumentsJSON), &req); err != nil {
			return err
		}
		if req.Limit <= 0 {
			req.Limit = agentToolDefaultHistoryLimit
		}
		if req.InvestigationID == "" {
			req.InvestigationID = trace.InvestigationID
		}
		// Mirrors injectAuditEventReceiptListDefaults: the context query always excludes audit reads.
		req.ExcludeAuditReads = true
		if call.Retrieval.Query != req {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
		want, err := canonicalToolJSON(result)
		if err != nil {
			return err
		}
		if call.ResultJSON != want {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
		return nil
	}
	var args struct {
		DataType string `json:"data_type"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return err
	}
	if args.DataType != investigationDataHistoryTrail && args.DataType != investigationDataOperatorActions {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	limit := args.Limit
	if limit < 1 {
		limit = agentToolDefaultHistoryLimit
	}
	if limit > investigationContextMaxEventReceipts {
		limit = investigationContextMaxEventReceipts
	}
	if call.Retrieval.Query != (mcp.AuditEventReceiptListRequest{InvestigationID: trace.InvestigationID, Limit: limit, ExcludeAuditReads: true}) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	want, err := canonicalToolJSON(investigationEventReceiptContext(args.DataType, trace.InvestigationID, result, refs))
	if err != nil {
		return err
	}
	if call.ResultJSON != want {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	return nil
}
