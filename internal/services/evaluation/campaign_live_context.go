// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const investigationContextMaxReceipts = 50

// AgentContextRetrieval records the exact query and receipt references returned
// by the Data Operator. The signed query receipt retains the full response.
type AgentContextRetrieval struct {
	Query    mcp.AuditReceiptListRequest `json:"query"`
	Receipts []AgentContextReceiptRef    `json:"receipts"`
}

type AgentContextReceiptRef struct {
	ReceiptID string `json:"receipt_id"`
	Digest    string `json:"sha256"`
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

func (r *agentRun) runInvestigationContext(ctx context.Context, record AgentToolCall, args agentToolArguments) (AgentToolCall, error) {
	dataType := args.String("data_type")
	if dataType == investigationDataStatus {
		seed := r.req.ScenarioInput.Seed
		return r.succeed(record, agentInvestigationContextResult{
			Success: true, DataType: dataType, InvestigationID: r.trace.InvestigationID,
			OperatorID: r.target.OperatorID, CaseTitle: seed.CaseTitle, CaseDescription: seed.CaseDescription,
		})
	}
	if dataType != investigationDataHistoryTrail && dataType != investigationDataOperatorActions {
		return r.fail(record, *agentToolValidationFailure(agentToolInvestigationCtx, []string{
			fmt.Sprintf("data_type\n  Input should be '%s', '%s' or '%s'", investigationDataHistoryTrail, investigationDataStatus, investigationDataOperatorActions),
		}))
	}
	limit := args.Int("limit", agentToolDefaultHistoryLimit)
	if limit < 1 {
		limit = agentToolDefaultHistoryLimit
	}
	if limit > investigationContextMaxReceipts {
		limit = investigationContextMaxReceipts
	}
	query := mcp.AuditReceiptListRequest{InvestigationID: r.trace.InvestigationID, Limit: limit}
	queryJSON, err := json.Marshal(query)
	if err != nil {
		return AgentToolCall{}, fmt.Errorf("evaluation: encode investigation query: %w", err)
	}
	payload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: "audit_receipt_list", ArgumentsJson: string(queryJSON), ExecutionId: record.CallID})
	if err != nil {
		return AgentToolCall{}, fmt.Errorf("evaluation: encode investigation MCP call: %w", err)
	}
	record.IsOperatorTool = true
	record.ActionType = string(constants.ActionTypeMcpCall)
	dispatched, denied, err := r.dispatch(ctx, &record, constants.ActionTypeMcpCall, "audit_receipt_list", payload)
	if err != nil || denied {
		return record, err
	}
	if err := requireExecuted(dispatched.Outcome); err != nil {
		return AgentToolCall{}, fmt.Errorf("evaluation: investigation query: %w", err)
	}
	result, refs, err := decodeInvestigationReceipts(r.trace, query, record.Receipt)
	if err != nil {
		return AgentToolCall{}, err
	}
	record.Retrieval = &AgentContextRetrieval{Query: query, Receipts: refs}
	return r.succeed(record, investigationReceiptContext(dataType, query.InvestigationID, result, refs))
}

// decodeInvestigationReceipts trusts only the selected runtime's signed answer,
// then checks each returned receipt against the same enrollment-bound key.
func decodeInvestigationReceipts(trace *AgentTrace, query mcp.AuditReceiptListRequest, receipt *AgentTraceReceipt) (mcp.OperatorAuditReceiptListResult, []AgentContextReceiptRef, error) {
	fail := func(err error) (mcp.OperatorAuditReceiptListResult, []AgentContextReceiptRef, error) {
		return mcp.OperatorAuditReceiptListResult{}, nil, fmt.Errorf("evaluation: authoritative investigation context: %w", err)
	}
	if receipt == nil || receipt.ActionReceipt == nil {
		return fail(constants.ErrActionReceiptMissing)
	}
	if trace.InvestigationID == "" || query.InvestigationID != trace.InvestigationID || query.OperatorSessionID != "" || query.Limit < 1 || query.Limit > investigationContextMaxReceipts {
		return fail(constants.ErrEvidenceScopeMismatch)
	}
	anchors, err := agentTraceAnchorSet(trace)
	if err != nil {
		return fail(err)
	}
	if _, err := oracle.VerifyReceipt(anchors, trace.Data.OperatorID, receipt.ActionReceipt); err != nil {
		return fail(err)
	}
	if receipt.GetActionType() != string(constants.ActionTypeMcpCall) || receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fail(constants.ErrEvaluationEvidenceTraceMismatch)
	}
	var result mcp.OperatorAuditReceiptListResult
	if err := json.Unmarshal([]byte(receipt.GetResultSummary()), &result); err != nil {
		return fail(err)
	}
	if result.OperatorID != trace.Data.OperatorID || result.Authority != "operator_local_ledger" || result.InvestigationID != query.InvestigationID || result.Count != len(result.Receipts) || result.Count > query.Limit {
		return fail(constants.ErrEvidenceScopeMismatch)
	}
	refs := make([]AgentContextReceiptRef, 0, len(result.Receipts))
	seen := make(map[string]bool, len(result.Receipts))
	for _, row := range result.Receipts {
		if row == nil {
			return fail(constants.ErrActionReceiptMissing)
		}
		r := row
		if r.GetTransactionId() == "" || seen[r.GetTransactionId()] {
			return fail(constants.ErrEvidenceScopeMismatch)
		}
		if _, err := oracle.VerifyReceipt(anchors, trace.Data.OperatorID, r); err != nil {
			return fail(err)
		}
		body, err := evalv1.MarshalCanonical(r)
		if err != nil {
			return fail(err)
		}
		refs = append(refs, AgentContextReceiptRef{ReceiptID: r.GetTransactionId(), Digest: models.SHA256Hex(body)})
		seen[r.GetTransactionId()] = true
	}
	return result, refs, nil
}

func investigationReceiptContext(dataType, investigationID string, result mcp.OperatorAuditReceiptListResult, refs []AgentContextReceiptRef) agentInvestigationContextResult {
	answer := agentInvestigationContextResult{Success: true, DataType: dataType, InvestigationID: investigationID, OperatorID: result.OperatorID, Authority: result.Authority, Events: []agentInvestigationEvent{}}
	for i, row := range result.Receipts {
		answer.Events = append(answer.Events, agentInvestigationEvent{
			ReceiptID: refs[i].ReceiptID, ReceiptDigest: refs[i].Digest,
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
		if trace.SchemaVersion == agentTraceSchemaVersion && call.ToolName == agentToolInvestigationCtx && call.Success {
			var args struct { DataType string `json:"data_type"` }
			if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil { return err }
			if args.DataType != investigationDataStatus { return constants.ErrEvaluationEvidenceTraceMismatch }
		}
		return nil
	}
	if call.ToolName != agentToolInvestigationCtx || !call.Success || call.ActionType != string(constants.ActionTypeMcpCall) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	result, refs, err := decodeInvestigationReceipts(trace, call.Retrieval.Query, call.Receipt)
	if err != nil {
		return err
	}
	if len(refs) != len(call.Retrieval.Receipts) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	for i := range refs {
		if refs[i] != call.Retrieval.Receipts[i] {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
	}
	var args struct {
		DataType string `json:"data_type"`
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return err
	}
	if args.DataType != investigationDataHistoryTrail && args.DataType != investigationDataOperatorActions {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	limit := args.Limit
	if limit < 1 { limit = agentToolDefaultHistoryLimit }
	if limit > investigationContextMaxReceipts { limit = investigationContextMaxReceipts }
	if call.Retrieval.Query != (mcp.AuditReceiptListRequest{InvestigationID: trace.InvestigationID, Limit: limit}) { return constants.ErrEvaluationEvidenceTraceMismatch }
	want, err := canonicalToolJSON(investigationReceiptContext(args.DataType, trace.InvestigationID, result, refs))
	if err != nil {
		return err
	}
	if call.ResultJSON != want {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	return nil
}
