// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// agentReceiptSchemaRef names the message a receipt evidence reference points at.
const agentReceiptSchemaRef = "ztd.operator.v1.ActionReceipt"

// VerifyAgentTraceEvidence proves, from the sealed trace alone, that every
// governed inference is backed by a receipt the Inference runtime signed,
// every governed tool call by a receipt the Data runtime signed, and that the
// Data runtime's ledger between the two sentinels holds exactly the executions
// the trace claims. When one Operator holds both capabilities its ledger also
// records every inference, so the bracket claims the model calls as well as
// the tool calls, in ledger order. An observer on the Data runtime commits its
// BEGIN and FINALIZE there too, and the bracket claims those receipts as well.
func VerifyAgentTraceEvidence(trace *AgentTrace) error {
	if trace == nil {
		return agentEvidenceError("trace", constants.ErrMissingRequiredField)
	}
	anchors, err := agentTraceAnchorSet(trace)
	if err != nil {
		return err
	}
	if trace.Bracket == nil {
		return agentEvidenceError("ledger bracket", constants.ErrMissingRequiredField)
	}
	before, err := verifiedSentinelOutcome(anchors, trace.Data, trace.Bracket.Before.ActionReceipt)
	if err != nil {
		return agentEvidenceError("ledger bracket before", err)
	}
	after, err := verifiedSentinelOutcome(anchors, trace.Data, trace.Bracket.After.ActionReceipt)
	if err != nil {
		return agentEvidenceError("ledger bracket after", err)
	}
	var modelClaims, toolClaims, observationClaims, consensusClaims []oracle.Outcome
	for _, call := range trace.ModelCalls {
		outcome, err := verifyModelCallEvidence(anchors, trace, call)
		if err != nil {
			return agentEvidenceError("model call "+call.ProviderAttemptID, err)
		}
		if trace.SharedRuntime() && outcome.Commitment != nil {
			modelClaims = append(modelClaims, outcome)
		}
		observed, err := verifyObservationEvidence(anchors, trace.Data, call)
		if err != nil {
			return agentEvidenceError("model call "+call.ProviderAttemptID+" observation", err)
		}
		observationClaims = append(observationClaims, observed...)
	}
	if trace.CarriesModelIdentity() {
		if err := verifyV7EpisodeOutputs(trace); err != nil {
			return agentEvidenceError("episode output", err)
		}
	}
	for _, call := range trace.ToolCalls {
		votes, err := verifyConsensusInferenceEvidence(anchors, trace, call)
		if err != nil {
			return agentEvidenceError("tool call "+call.CallID+" consensus", err)
		}
		consensusClaims = append(consensusClaims, votes...)
		if err := verifyContextRetrieval(trace, call); err != nil {
			return agentEvidenceError("context retrieval "+call.CallID, err)
		}
		outcome, err := verifyToolCallEvidence(anchors, trace.Data, call)
		if err != nil {
			return agentEvidenceError("tool call "+call.CallID, err)
		}
		if outcome != nil && outcome.Commitment != nil {
			toolClaims = append(toolClaims, *outcome)
		}
	}
	claims := append(slices.Clone(modelClaims), toolClaims...)
	claims = append(claims, consensusClaims...)
	ordered, err := oracle.OrderLedger(before, after, append(claims, observationClaims...))
	if err == nil {
		err = oracle.VerifyBracket(before, after, ordered)
	}
	if err != nil {
		return agentEvidenceError("ledger bracket", err)
	}
	// Grading reads each role's calls in trace order, so that order must be
	// the order the ledger committed them in.
	for _, claims := range [][]oracle.Outcome{modelClaims, toolClaims} {
		if !inLedgerOrder(ordered, claims) {
			return agentEvidenceError("ledger bracket", fmt.Errorf("trace records claimed executions out of ledger order: %w", constants.ErrConformanceLedgerGap))
		}
	}
	return nil
}

// The final diagnosis and requested tool arguments are grader inputs too. Bind
// them to the signed responses rather than trusting the trace's repeated copy.
func verifyV7EpisodeOutputs(trace *AgentTrace) error {
	var lastTurn *AgentModelCall
	turns := make(map[string]*AgentModelCall)
	seenAttempts := make(map[string]bool)
	seenTransactions := make(map[string]bool)
	for i := range trace.ModelCalls {
		call := &trace.ModelCalls[i]
		if seenAttempts[call.ProviderAttemptID] || seenTransactions[call.TransactionID] {
			return fmt.Errorf("model call repeats an inference identity: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
		seenAttempts[call.ProviderAttemptID] = true
		seenTransactions[call.TransactionID] = true
		if call.IsTurn() && call.Completed() && call.InferenceResult != nil {
			lastTurn = call
			turns[call.ProviderAttemptID] = call
		}
	}
	if trace.FinalOutput != "" && (lastTurn == nil || lastTurn.Text != trace.FinalOutput) {
		return fmt.Errorf("final output differs from the last signed model turn: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	if len(trace.ToolCalls) > 0 && trace.Input == nil {
		return fmt.Errorf("tool calls have no recorded tool declarations: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	specs := make(map[string]agentToolSpec)
	if trace.Input != nil {
		for _, declaration := range trace.Input.Tools {
			spec, err := agentToolSpecFromDeclaration(declaration)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrEvaluationEvidenceTraceMismatch, err)
			}
			specs[declaration.Name] = spec
		}
	}
	for _, tool := range trace.ToolCalls {
		turn := turns[tool.ModelCallID]
		if turn == nil {
			return fmt.Errorf("tool call has no signed agent turn: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
		_, _, calls := agentResponseParts(turn.InferenceResult.InferenceResult)
		found := false
		for _, requested := range calls {
			spec, declared := specs[requested.GetName()]
			request := decodeAgentToolRequest(spec, declared, requested.GetArgumentsJson())
			if requested.GetCallId() == tool.ProviderCallID && requested.GetName() == tool.ToolName && request.ArgumentsJSON == tool.ArgumentsJSON {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("tool call differs from the signed model request: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
		if tool.ApprovedArgumentsJSON != "" {
			var approved models.RunShellCommandRequest
			if err := json.Unmarshal([]byte(tool.ApprovedArgumentsJSON), &approved); err != nil || approved.Command == "" || approved.CanonicalJSON() != tool.ApprovedArgumentsJSON {
				return fmt.Errorf("approved shell arguments are not canonical typed JSON: %w", constants.ErrEvaluationEvidenceTraceMismatch)
			}
			matchedReview := false
			for _, step := range trace.Steps {
				if review := step.GetReview(); review != nil && models.RunShellCommandRequestFromProto(review.GetArguments()).CanonicalJSON() == tool.ApprovedArgumentsJSON {
					matchedReview = true
					break
				}
			}
			if !matchedReview {
				return fmt.Errorf("approved shell arguments differ from every typed Tribunal review: %w", constants.ErrEvaluationEvidenceTraceMismatch)
			}
		}
	}
	return nil
}

// verifyObservationEvidence returns the ledger claims for the observer receipts
// a model call carries that the Data runtime signed. An observer on the Data
// runtime commits its BEGIN and FINALIZE inside the trace bracket, so the
// bracket must claim them; an observer on any other runtime commits to a ledger
// the bracket does not cover, so its receipt claims nothing. A claimed receipt
// must be a verified, executed observation: its signed action type binds it, so
// no other execution on the Data runtime can be passed off as one.
func verifyObservationEvidence(anchors *oracle.AnchorSet, data AgentTraceRuntime, call AgentModelCall) ([]oracle.Outcome, error) {
	var claims []oracle.Outcome
	for _, receipt := range call.ObservationReceipts {
		if receipt.ActionReceipt == nil {
			return nil, constants.ErrMissingRequiredField
		}
		if receipt.GetSignerKeyId() != data.Anchor.GetKeyId() {
			continue
		}
		switch action := constants.ActionType(receipt.GetActionType()); action {
		case constants.ActionTypeProviderBoundaryObservation, constants.ActionTypeModelProvenanceObservation:
		default:
			return nil, fmt.Errorf("receipt action %q is not an observation: %w", action, constants.ErrEvaluationEvidenceTraceMismatch)
		}
		outcome, err := verifiedOutcome(anchors, data.OperatorID, receipt.ActionReceipt)
		if err != nil {
			return nil, err
		}
		if err := verifyReceiptSession(receipt.ActionReceipt, data.SessionID); err != nil {
			return nil, err
		}
		if outcome.Kind != oracle.OutcomeExecuted || outcome.Commitment == nil {
			return nil, fmt.Errorf("observation receipt is not an executed commitment: %w", constants.ErrConformanceLedgerGap)
		}
		claims = append(claims, outcome)
	}
	return claims, nil
}

// inLedgerOrder reports whether claims appear in ordered in the same relative
// order.
func inLedgerOrder(ordered, claims []oracle.Outcome) bool {
	next := 0
	for _, outcome := range ordered {
		if next < len(claims) && outcome.Commitment.Hash == claims[next].Commitment.Hash {
			next++
		}
	}
	return next == len(claims)
}

// agentTraceAnchorSet pins each traced runtime's key. Both roles must carry an
// anchor bound to their own runtime; a shared runtime must pin one key for
// both, so a receipt verifies the same whichever role claims it.
func agentTraceAnchorSet(trace *AgentTrace) (*oracle.AnchorSet, error) {
	roles := []struct {
		name    string
		runtime AgentTraceRuntime
	}{{"inference runtime anchor", trace.Inference}, {"data runtime anchor", trace.Data}}
	for _, role := range roles {
		if role.runtime.OperatorID == "" || role.runtime.Anchor == nil || role.runtime.Anchor.ConformanceAnchor == nil {
			return nil, agentEvidenceError(role.name, constants.ErrConformanceAnchorMissing)
		}
		if runtimeID := role.runtime.Anchor.GetRuntimeId(); runtimeID != role.runtime.OperatorID {
			return nil, agentEvidenceError(role.name, fmt.Errorf("anchor runtime %q is not the traced runtime %q: %w", runtimeID, role.runtime.OperatorID, constants.ErrConformanceAnchorUnknown))
		}
	}
	pinned := []*evalv1.ConformanceAnchor{trace.Data.Anchor.ConformanceAnchor}
	if trace.SharedRuntime() {
		if !proto.Equal(trace.Inference.Anchor.ConformanceAnchor, trace.Data.Anchor.ConformanceAnchor) {
			return nil, agentEvidenceError("inference runtime anchor", fmt.Errorf("shared runtime %q pins two keys: %w", trace.Data.OperatorID, constants.ErrConformanceAnchorInvalid))
		}
	} else {
		pinned = append(pinned, trace.Inference.Anchor.ConformanceAnchor)
	}
	anchors, err := oracle.NewAnchorSet(pinned)
	if err != nil {
		return nil, agentEvidenceError("runtime anchors", err)
	}
	return anchors, nil
}

// verifyModelCallEvidence checks one governed inference against its receipt.
// Every recorded call must carry a receipt the Inference runtime signed for
// its own transaction. A completed call's receipt must be an execution whose
// result_summary is the call's result digest; a failed call's receipt may be
// a failed execution, which still holds a ledger commitment, or a governance
// rejection, which holds none.
func verifyModelCallEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, call AgentModelCall) (oracle.Outcome, error) {
	if call.Receipt == nil || call.Receipt.ActionReceipt == nil {
		return oracle.Outcome{}, fmt.Errorf("governed inference has no signed receipt: %w", constants.ErrActionReceiptMissing)
	}
	receipt := call.Receipt.ActionReceipt
	outcome, err := verifiedOutcome(anchors, trace.Inference.OperatorID, receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if err := verifyReceiptSession(receipt, trace.Inference.SessionID); err != nil {
		return oracle.Outcome{}, err
	}
	if call.TransactionID == "" || receipt.GetTransactionId() != call.TransactionID {
		return oracle.Outcome{}, fmt.Errorf("receipt transaction %q is not the call transaction %q: %w", receipt.GetTransactionId(), call.TransactionID, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	if trace.CarriesModelIdentity() {
		if err := verifyV7ModelCallIdentity(trace, call, outcome); err != nil {
			return oracle.Outcome{}, err
		}
	}
	switch {
	case call.Completed():
		if outcome.Kind != oracle.OutcomeExecuted {
			return oracle.Outcome{}, fmt.Errorf("completed inference has a receipt that is not an execution: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
		if receipt.GetResultSummary() != call.ResultDigest {
			return oracle.Outcome{}, fmt.Errorf("receipt result summary is not the call result digest: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
	case outcome.Kind == oracle.OutcomeExecuted, outcome.Kind == oracle.OutcomeExecutionFailed, outcome.Kind == oracle.OutcomeRejected:
	default:
		return oracle.Outcome{}, fmt.Errorf("receipt outcome %d is not an inference outcome: %w", outcome.Kind, constants.ErrConformanceReceiptUnclassifiable)
	}
	return outcome, nil
}

// verifyV7ModelCallIdentity proves the per-call transcript and model identity
// are the ones returned by the signed Inference Operator result. The frozen
// assignment-to-variant mapping is checked by the campaign verifier that has
// the run and campaign records; this layer ensures the trace's repeated claim
// is complete and internally bound to the result and its receipt.
func verifyV7ModelCallIdentity(trace *AgentTrace, call AgentModelCall, outcome oracle.Outcome) error {
	fail := func(reason string) error {
		return fmt.Errorf("%s: %w", reason, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	var result *operatorv1.InferenceResult
	if call.InferenceResult != nil {
		result = call.InferenceResult.InferenceResult
	}
	if call.ModelVariantID == "" || call.ModelRole == "" || call.ServedModelTag == "" || call.ModelDigest == "" {
		return fmt.Errorf("v7 inference model identity is incomplete: %w", constants.ErrMissingRequiredField)
	}
	if call.ProviderAttemptID == "" {
		return fmt.Errorf("v7 inference provider attempt is missing: %w", constants.ErrMissingRequiredField)
	}
	if trace.CampaignID == "" || trace.RunID == "" || trace.AssignmentID == "" || trace.EvaluationAttemptID == "" || trace.ScenarioID == "" || trace.DesignatedRole == "" || trace.ServedModelTag == "" || trace.ModelDigest == "" || trace.ModelRegistryDigest == "" {
		return fmt.Errorf("v7 trace model identity is incomplete: %w", constants.ErrMissingRequiredField)
	}
	if !models.IsSHA256Hex(trace.ModelDigest) || !models.IsSHA256Hex(trace.ModelRegistryDigest) {
		return fail("trace model or registry digest is malformed")
	}
	if !validInferenceModelRole(call.ModelRole) {
		return fail("model role is not a recognized inference role")
	}
	traceRole, roleKnown := inferenceModelRoleForTraceRole(trace.DesignatedRole)
	if roleKnown && call.ModelRole == traceRole {
		if call.ServedModelTag != trace.ServedModelTag || call.ModelDigest != trace.ModelDigest {
			return fail("per-call designated variant identity differs from the trace identity")
		}
	} else if !strings.HasPrefix(call.Purpose, agentModelCallPurposeTribunalPrefix) || call.ModelRole != operatorv1.ModelRole_MODEL_ROLE_ASSISTANT.String() {
		// A heterogeneous system assignment can dispatch Tribunal calls to its
		// assistant slot while the trace-level identity names the designated
		// primary slot. Exact variant-to-slot binding is checked against the
		// frozen assignment by the campaign verifier.
		return fail("model role differs from the designated campaign role")
	}
	if result == nil {
		if call.Completed() || outcome.Kind == oracle.OutcomeExecuted {
			return fmt.Errorf("completed v7 inference has no complete result: %w", constants.ErrMissingRequiredField)
		}
		// Failed providers may have a signed FAILED receipt and no result. The
		// campaign verifier checks this call's per-role variant against the
		// frozen assignment even though there is no response to bind.
		return nil
	}
	if call.ProviderAttemptID == "" ||
		result.GetProviderAttemptId() != call.ProviderAttemptID ||
		result.GetCampaignId() != trace.CampaignID ||
		result.GetRunId() != trace.RunID ||
		result.GetAssignmentId() != trace.AssignmentID ||
		result.GetEvaluationAttemptId() != trace.EvaluationAttemptID ||
		result.GetScenarioId() != trace.ScenarioID ||
		result.GetModelRegistryDigest() != trace.ModelRegistryDigest {
		return fail("signed inference result identity differs from the trace")
	}
	if result.GetRequestedModel() != call.ServedModelTag || result.GetModel() != call.ServedModelTag ||
		result.GetRequestedModelDigest() != call.ModelDigest || result.GetServedModelDigest() != call.ModelDigest {
		return fail("signed inference result model differs from the per-call model identity")
	}
	resultDigest, err := models.ComputeInferenceResultDigest(result)
	if err != nil {
		return fmt.Errorf("compute signed inference result digest: %w", err)
	}
	if result.GetResultDigest() == "" || result.GetResultDigest() != resultDigest || call.ResultDigest != resultDigest || call.Receipt.GetResultSummary() != resultDigest {
		return fmt.Errorf("signed inference result digest does not match the call and receipt: %w", constants.ErrInferenceResultDigest)
	}
	if err := verifyV7ModelCallTranscript(call, result); err != nil {
		return err
	}
	return nil
}

func inferenceModelRoleForTraceRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "primary":
		return operatorv1.ModelRole_MODEL_ROLE_PRIMARY.String(), true
	case "assistant":
		return operatorv1.ModelRole_MODEL_ROLE_ASSISTANT.String(), true
	case "lite":
		return operatorv1.ModelRole_MODEL_ROLE_LITE.String(), true
	default:
		return "", false
	}
}

func validInferenceModelRole(role string) bool {
	switch role {
	case operatorv1.ModelRole_MODEL_ROLE_PRIMARY.String(),
		operatorv1.ModelRole_MODEL_ROLE_ASSISTANT.String(),
		operatorv1.ModelRole_MODEL_ROLE_LITE.String():
		return true
	default:
		return false
	}
}

// verifyV7ModelCallTranscript binds grader-visible output and telemetry to the
// complete result covered by the signed receipt.
func verifyV7ModelCallTranscript(call AgentModelCall, result *operatorv1.InferenceResult) error {
	text, thinking, toolCalls := agentResponseParts(result)
	if call.Text != text || call.Thinking != thinking || call.ToolCallCount != len(toolCalls) {
		return fmt.Errorf("model transcript differs from the signed inference result: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	ttft, generation := uint64(0), uint64(0)
	if result.TimeToFirstTokenNs != nil && *result.TimeToFirstTokenNs > 0 {
		ttft = uint64(*result.TimeToFirstTokenNs)
	}
	if result.GenerationDurationNs != nil && *result.GenerationDurationNs > 0 {
		generation = uint64(*result.GenerationDurationNs)
	}
	if result.GetPromptTokens() < 0 || result.GetCompletionTokens() < 0 ||
		call.PromptTokens != uint32(result.GetPromptTokens()) || call.CompletionTokens != uint32(result.GetCompletionTokens()) ||
		call.UsageReported != result.GetUsageReported() || call.FinishReason != result.GetFinishReason() ||
		call.LoadState != operatorLoadStateToEvaluation(result.GetLoadState()).String() || call.RetryCount != result.GetRetryCount() ||
		call.TTFTNanos != ttft || call.GenerationDurationNanos != generation {
		return fmt.Errorf("model telemetry differs from the signed inference result: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	return nil
}

// verifyToolCallEvidence checks one tool call against its receipt. A call the
// Gateway admitted must carry a verified receipt for its own transaction, from
// the Data session the trace records. A refusal carries none. It returns the
// verified outcome, or nil when the call has no receipt.
func verifyToolCallEvidence(anchors *oracle.AnchorSet, data AgentTraceRuntime, call AgentToolCall) (*oracle.Outcome, error) {
	if call.Receipt == nil || call.Receipt.ActionReceipt == nil {
		if call.TransactionID != "" || call.PolicyDecision == agentPolicyDecisionAllow {
			return nil, fmt.Errorf("admitted call has no signed receipt: %w", constants.ErrActionReceiptMissing)
		}
		return nil, nil
	}
	receipt := call.Receipt.ActionReceipt
	if call.ApprovedArgumentsJSON != "" && (call.ActionType != string(constants.ActionTypeMcpCall) || receipt.GetActionType() != string(constants.ActionTypeMcpCall)) {
		return nil, fmt.Errorf("approved Tribunal arguments are not bound to an MCP_CALL receipt: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	outcome, err := verifiedOutcome(anchors, data.OperatorID, receipt)
	if err != nil {
		return nil, err
	}
	if err := verifyReceiptSession(receipt, data.SessionID); err != nil {
		return nil, err
	}
	if receipt.GetTransactionId() != call.TransactionID {
		return nil, fmt.Errorf("receipt transaction %q is not the call transaction %q: %w", receipt.GetTransactionId(), call.TransactionID, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	switch outcome.Kind {
	case oracle.OutcomeExecuted, oracle.OutcomeExecutionFailed:
		if call.PolicyDecision != agentPolicyDecisionAllow {
			return nil, fmt.Errorf("executed receipt on a call recorded as %q: %w", call.PolicyDecision, constants.ErrEvaluationEvidenceTraceMismatch)
		}
	case oracle.OutcomeRejected:
		if call.PolicyDecision != agentPolicyDecisionDeny {
			return nil, fmt.Errorf("rejected receipt on a call recorded as %q: %w", call.PolicyDecision, constants.ErrEvaluationEvidenceTraceMismatch)
		}
	default:
		return nil, fmt.Errorf("receipt outcome %d is not a tool call outcome: %w", outcome.Kind, constants.ErrConformanceReceiptUnclassifiable)
	}
	return &outcome, nil
}

// verifyReceiptSession requires every stage of the receipt to name the Operator
// session the trace records for the role that claimed it. The ledger bracket
// cannot make this check: a receipt from another session on the same runtime
// commits to the same ledger and still connects. The Warden stamps every stage
// from the envelope and rejects an envelope with no session, so a receipt with
// no stages, or a stage that names no session, is never a governed execution.
func verifyReceiptSession(receipt *operatorv1.ActionReceipt, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("trace records no operator session: %w", constants.ErrMissingRequiredField)
	}
	stages := receipt.GetDeterministicStageEvidence()
	if len(stages) == 0 {
		return fmt.Errorf("receipt has no deterministic stage evidence: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	for _, stage := range stages {
		if named := stage.GetOperatorSessionId(); named != sessionID {
			return fmt.Errorf("receipt names operator session %q, want %q: %w", named, sessionID, constants.ErrEvaluationEvidenceTraceMismatch)
		}
	}
	return nil
}

// verifiedSentinelOutcome verifies one harness sentinel receipt. A sentinel
// must be an executed read in the Data session: anything else leaves the
// bracket unconnected.
func verifiedSentinelOutcome(anchors *oracle.AnchorSet, data AgentTraceRuntime, receipt *operatorv1.ActionReceipt) (oracle.Outcome, error) {
	if receipt == nil {
		return oracle.Outcome{}, fmt.Errorf("sentinel receipt is missing: %w", constants.ErrActionReceiptMissing)
	}
	outcome, err := verifiedOutcome(anchors, data.OperatorID, receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if err := verifyReceiptSession(receipt, data.SessionID); err != nil {
		return oracle.Outcome{}, err
	}
	if outcome.Kind != oracle.OutcomeExecuted {
		return oracle.Outcome{}, fmt.Errorf("sentinel receipt is not an executed read: %w", constants.ErrConformanceLedgerGap)
	}
	return outcome, nil
}

// verifiedOutcome verifies a receipt against the runtime's pinned key and
// classifies what it says happened.
func verifiedOutcome(anchors *oracle.AnchorSet, runtimeID string, receipt *operatorv1.ActionReceipt) (oracle.Outcome, error) {
	if _, err := oracle.VerifyReceipt(anchors, runtimeID, receipt); err != nil {
		return oracle.Outcome{}, err
	}
	return oracle.Classify(receipt)
}

// verifyJudgeReceipt proves a judge score's governed inference receipt was
// signed by the Inference runtime's pinned key in the Inference session and is
// the transaction the score names. The judge's response checks do not verify the signature, so a judge
// score is labelled verified only through this check. The receipt's result
// summary binds the result digest, so the result the score was read from is
// recomputed and must match it: an altered score body under an intact digest is
// never verified. A failed judge has no scored body to bind, so it keeps only
// the receipt check when it carries no result.
func verifyJudgeReceipt(anchors *oracle.AnchorSet, inference AgentTraceRuntime, sc judge.Score) error {
	if _, err := oracle.VerifyReceipt(anchors, inference.OperatorID, sc.Receipt); err != nil {
		return err
	}
	if err := verifyReceiptSession(sc.Receipt, inference.SessionID); err != nil {
		return err
	}
	if sc.Receipt.GetTransactionId() != sc.TransactionID {
		return fmt.Errorf("receipt transaction %q is not the judge transaction %q: %w", sc.Receipt.GetTransactionId(), sc.TransactionID, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	if sc.Result == nil {
		if sc.Status == judge.StatusScored {
			return fmt.Errorf("scored judge response has no inference result: %w", constants.ErrMissingRequiredField)
		}
		return nil
	}
	resultDigest, err := models.ComputeInferenceResultDigest(sc.Result)
	if err != nil {
		return fmt.Errorf("compute judge inference result digest: %w", err)
	}
	if sc.Result.GetResultDigest() != resultDigest || sc.Receipt.GetResultSummary() != resultDigest {
		return fmt.Errorf("judge inference result digest does not match the result and receipt: %w", constants.ErrInferenceResultDigest)
	}
	return nil
}

// agentReceiptEvidenceReference names one verified receipt. The receipt lives
// inside the sealed trace, so the reference carries the trace artifact's ID and
// the digest of the receipt's canonical protojson.
func agentReceiptEvidenceReference(traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, transactionID string, receipt *AgentTraceReceipt) (*compliancev1.ComplianceEvidenceReference, error) {
	if traceEvidence == nil || receipt == nil || receipt.ActionReceipt == nil {
		return nil, fmt.Errorf("evaluation: agent receipt reference: %w", constants.ErrMissingRequiredField)
	}
	encoded, err := evalv1.MarshalCanonical(receipt.ActionReceipt)
	if err != nil {
		return nil, fmt.Errorf("evaluation: agent receipt reference: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return &compliancev1.ComplianceEvidenceReference{
		ArtifactId:    traceEvidence.GetArtifactId(),
		ArtifactType:  string(complianceevidence.ArtifactTypeEvalReceipt),
		SchemaRef:     agentReceiptSchemaRef,
		MediaType:     constants.MediaTypeJSON,
		Sha256:        hex.EncodeToString(digest[:]),
		TransactionId: transactionID,
		RunId:         trace.RunID,
		AttemptId:     trace.EvaluationAttemptID,
		ScenarioId:    trace.ScenarioID,
	}, nil
}

// agentToolGovernedAction binds one tool call's signed receipt as a governed
// action on the Data Operator. A refused call has no receipt and binds nothing.
func agentToolGovernedAction(traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, assignmentID string, call AgentToolCall) (*evalv1.GovernedActionBinding, error) {
	if call.Receipt == nil {
		return nil, nil
	}
	ref, err := agentReceiptEvidenceReference(traceEvidence, trace, call.TransactionID, call.Receipt)
	if err != nil {
		return nil, err
	}
	return &evalv1.GovernedActionBinding{
		BindingId:         call.CallID,
		AssignmentId:      assignmentID,
		TransactionId:     call.TransactionID,
		OperatorId:        trace.Data.OperatorID,
		OperatorSessionId: trace.Data.SessionID,
		ReceiptRef:        ref,
		PolicyDecision:    call.PolicyDecision,
		ActionType:        call.Receipt.GetActionType(),
	}, nil
}

func agentEvidenceError(subject string, cause error) error {
	return fmt.Errorf("%w: %s: %w", constants.ErrCampaignToolEvidenceInvalid, subject, cause)
}
