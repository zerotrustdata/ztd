// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// agentTraceSchemaVersion is the version every new trace is sealed with. v10
// records each pinned-judge verdict on a judged content criterion with its
// dispatched prompt and signed inference result; v9
// separates harness seed and final-state observations from model tool calls; v8
// records the Tribunal-approved arguments a run_shell_command call executed; v7
// carries each complete signed inference result and its per-call model identity;
// v6 binds scored investigation identity and authoritative context retrievals; v5
// added the held context a repetition continued; v4 added the exact model
// input, per-turn thinking, and provider tool-call IDs. v4 and v3 traces
// remain valid evidence; a v3 trace's model input was not captured and is
// never reconstructed.
const (
	agentTraceSchemaVersion   = "agent-trace.v10"
	agentTraceSchemaVersionV9 = "agent-trace.v9"
	agentTraceSchemaVersionV8 = "agent-trace.v8"
	agentTraceSchemaVersionV7 = "agent-trace.v7"
	agentTraceSchemaVersionV6 = "agent-trace.v6"
	agentTraceSchemaVersionV5 = "agent-trace.v5"
	agentTraceSchemaVersionV4 = "agent-trace.v4"
	agentTraceSchemaVersionV3 = "agent-trace.v3"
)

// agentModelCallPurposeTurn is the purpose of every governed agent turn. Each
// one is a scored step of the tool loop.
const agentModelCallPurposeTurn = "agent_turn"

// agentModelCallPurposeTribunalPrefix prefixes the purpose of every governed
// inference a Tribunal deliberation dispatches inside one intent-only
// Tribunal-authored run_shell_command call: tribunal_generate, tribunal_marshal, or
// tribunal_audit. These calls are not turns; they hang off their tool call.
const agentModelCallPurposeTribunalPrefix = "tribunal_"

// Policy decisions recorded on a governed tool call.
const (
	agentPolicyDecisionAllow = "allow"
	agentPolicyDecisionDeny  = "deny"
)

// Terminal classifications for homogeneous agent executor outcomes (D6).
const (
	AgentTerminalClassificationFinalDiagnosis           = "final_diagnosis"
	AgentTerminalClassificationClarification            = "clarification"
	AgentTerminalClassificationEscalationOrRefusal      = "escalation_or_refusal"
	AgentTerminalClassificationPolicyDenial             = "policy_denial"
	AgentTerminalClassificationPipelineOutcome          = "pipeline_outcome"
	AgentTerminalClassificationBudgetExhaustion         = "budget_exhaustion"
	AgentTerminalClassificationHarnessFailure           = "harness_failure"
	AgentTerminalClassificationUnsupportedClarification = "unsupported_clarification"
	AgentTerminalClassificationTimeout                  = "timeout"
)

// AgentTraceWorkspace is the attempt-scoped workspace one agent trace ran in.
type AgentTraceWorkspace struct {
	Root                     string `json:"root"`
	OperatorWorkingDirectory string `json:"operator_working_directory"`
}

// AgentModelCall is one governed inference the agent executor dispatched. A
// call the Inference Operator executed carries its signed receipt: COMPLETED
// with the result digest as result_summary, or FAILED when the provider
// failed after the Operator committed the execution (Error then names the
// failure). Either way the execution sits on the Inference runtime's ledger.
type AgentModelCall struct {
	ProviderAttemptID string `json:"provider_attempt_id"`
	TransactionID     string `json:"transaction_id"`
	Purpose           string `json:"purpose"`
	LoopTurn          int    `json:"loop_turn"`
	// Model identity is repeated per call so a trace can bind each inference
	// result to its selected frozen variant and role.
	ModelVariantID string `json:"model_variant_id,omitempty"`
	ModelRole      string `json:"model_role,omitempty"`
	ServedModelTag string `json:"served_model_tag,omitempty"`
	ModelDigest    string `json:"model_digest,omitempty"`
	// InferenceResult is the complete response covered by ResultDigest and the
	// signed receipt. Earlier trace versions did not retain it.
	InferenceResult *AgentTraceInferenceResult `json:"inference_result,omitempty"`
	// ToolCallID and Persona are set on a Tribunal call: the tool call whose
	// deliberation dispatched it and the persona that answered.
	ToolCallID              string   `json:"tool_call_id,omitempty"`
	Persona                 string   `json:"persona,omitempty"`
	ToolsDeclared           []string `json:"tools_declared"`
	ResultDigest            string   `json:"result_digest"`
	UsageReported           bool     `json:"usage_reported"`
	PromptTokens            uint32   `json:"prompt_tokens"`
	CompletionTokens        uint32   `json:"completion_tokens"`
	TTFTNanos               uint64   `json:"ttft_nanos,omitempty"`
	GenerationDurationNanos uint64   `json:"generation_duration_nanos,omitempty"`
	FinishReason            string   `json:"finish_reason,omitempty"`
	LoadState               string   `json:"load_state,omitempty"`
	RetryCount              uint32   `json:"retry_count"`
	// Thinking is the reasoning text the provider returned as thinking parts,
	// in order. It is never sent back to the model.
	Thinking      string `json:"thinking,omitempty"`
	Text          string `json:"text,omitempty"`
	ToolCallCount int    `json:"tool_call_count"`
	// Error is the public failure of a call that did not complete.
	Error   string             `json:"error,omitempty"`
	Receipt *AgentTraceReceipt `json:"receipt,omitempty"`
	// ObservationReceipts are the observers' signed final receipts for this
	// attempt's BEGIN and FINALIZE commands. The Gateway waits for each before
	// the dispatch returns, so a shared runtime claims their commitments in the
	// trace bracket.
	ObservationReceipts []AgentTraceReceipt `json:"observation_receipts,omitempty"`
}

// AgentTraceInferenceResult carries a complete inference result as canonical
// protojson, like AgentTraceReceipt.
type AgentTraceInferenceResult struct{ *operatorv1.InferenceResult }

// MarshalJSON writes the result as canonical protojson.
func (r AgentTraceInferenceResult) MarshalJSON() ([]byte, error) {
	if r.InferenceResult == nil {
		return nil, fmt.Errorf("evaluation: encode agent trace inference result: %w", constants.ErrMissingRequiredField)
	}
	return evalv1.MarshalCanonical(r.InferenceResult)
}

// UnmarshalJSON decodes canonical protojson strictly.
func (r *AgentTraceInferenceResult) UnmarshalJSON(b []byte) error {
	result := &operatorv1.InferenceResult{}
	if err := unmarshalAgentTraceCanonical(b, result); err != nil {
		return fmt.Errorf("evaluation: decode agent trace inference result: %w", err)
	}
	r.InferenceResult = result
	return nil
}

// Completed reports whether the call returned a governed result.
func (c AgentModelCall) Completed() bool {
	return c.TransactionID != "" && c.Error == ""
}

// IsTurn reports whether the call is a scored agent turn rather than an
// inference a tool call dispatched.
func (c AgentModelCall) IsTurn() bool {
	return c.Purpose == agentModelCallPurposeTurn
}

// AgentToolCall is one tool call the model made and what it returned. An
// operator tool's CallID is the governed transaction ID when the Gateway
// admitted the dispatch, so governed actions resolve to their call.
type AgentToolCall struct {
	// ConsensusInferences are the governed L2 vote attempts for this action.
	ConsensusInferences []governance.ConsensusInference `json:"consensus_inferences,omitempty"`
	CallID              string                          `json:"call_id"`
	ModelCallID         string                          `json:"model_call_id"`
	// ProviderCallID is the call ID the model returned; the assistant and tool
	// result messages sent on the next turn carry it.
	ProviderCallID        string `json:"provider_call_id,omitempty"`
	ToolName              string `json:"tool_name"`
	ArgumentsJSON         string `json:"arguments_json"`
	ApprovedArgumentsJSON string `json:"approved_arguments_json,omitempty"`
	Command               string `json:"command,omitempty"`
	ResultJSON            string `json:"result_json,omitempty"`
	ErrorType             string `json:"error_type,omitempty"`
	Error                 string `json:"error,omitempty"`
	Success               bool   `json:"success"`
	IsOperatorTool        bool   `json:"is_operator_tool"`
	LoopTurn              int    `json:"loop_turn"`
	ActionType            string `json:"action_type,omitempty"`
	TransactionID         string `json:"transaction_id,omitempty"`
	PolicyDecision        string `json:"policy_decision,omitempty"`
	// Receipt is the signed receipt the Gateway answered the dispatch with: the
	// executed receipt, or a governance rejection's receipt. A refusal has none.
	Receipt *AgentTraceReceipt `json:"receipt,omitempty"`
	// Retrieval binds the authoritative query and the receipts it returned.
	Retrieval *AgentContextRetrieval `json:"retrieval,omitempty"`
	// RequestEnvelope binds native dispatch arguments to the receipt hash.
	RequestEnvelope *AgentTraceEnvelope `json:"request_envelope,omitempty"`
	// An ingress refusal precedes any envelope and is not an execution receipt.
	IngressRefusalCode operatorv1.IngressRefusalCode `json:"ingress_refusal_code,omitempty"`
}

// ShowedGuidance reports whether the call returned model-visible guidance.
func (c AgentToolCall) ShowedGuidance() bool {
	return c.Error != ""
}

// AgentClarificationTurn records one interrogation clarification turn within an episode.
type AgentClarificationTurn struct {
	LoopTurn       int      `json:"loop_turn"`
	Questions      []string `json:"questions"`
	SelectedBranch string   `json:"selected_branch,omitempty"`
	UserResponse   string   `json:"user_response,omitempty"`
}

// AgentTrace is the canonical persisted evidence for one homogeneous
// assignment run by the agent executor: every governed inference, every tool
// call and its governed outcome, and the designated role's final answer.
// Grading and verification both read only this envelope and the frozen
// scenario fixture.
type AgentTrace struct {
	SchemaVersion       string `json:"schema_version"`
	EvidenceDigest      string `json:"evidence_digest"`
	RunID               string `json:"run_id"`
	AssignmentID        string `json:"assignment_id"`
	EvaluationAttemptID string `json:"evaluation_attempt_id"`
	CampaignID          string `json:"campaign_id"`
	ScenarioID          string `json:"scenario_id"`
	InvestigationID     string `json:"investigation_id,omitempty"`
	DesignatedRole      string `json:"designated_role"`
	ServedModelTag      string `json:"served_model_tag"`
	ModelDigest         string `json:"model_digest"`
	ModelRegistryDigest string `json:"model_registry_digest"`
	// Inference and Data are the runtimes the roles executed on. When one
	// Operator holds both capabilities they name the same runtime, whose
	// ledger bracket must then account for both roles' executions.
	Inference AgentTraceRuntime   `json:"inference"`
	Data      AgentTraceRuntime   `json:"data"`
	Workspace AgentTraceWorkspace `json:"workspace"`
	// HeldContext is the previous repetition's conversation this repetition
	// continued, when its campaign holds context. The model received these
	// messages ahead of Input on every turn.
	HeldContext *AgentTraceHeldContext `json:"held_context,omitempty"`
	// Input is the exact model input of the first turn after any HeldContext.
	// Every later turn sent this input followed, per earlier turn, by one
	// assistant message (the call's Text, then its tool calls with
	// ProviderCallID, ToolName and ArgumentsJSON) and one tool message per
	// call carrying ResultJSON. v3 traces have none.
	Input                  *AgentTraceInput         `json:"input,omitempty"`
	ModelCalls             []AgentModelCall         `json:"model_calls"`
	ToolCalls              []AgentToolCall          `json:"tool_calls"`
	Clarifications         []AgentClarificationTurn `json:"clarifications,omitempty"`
	FinalOutput            string                   `json:"final_output"`
	TerminalClassification string                   `json:"terminal_classification,omitempty"`
	ToolTurnLimitReached   bool                     `json:"tool_turn_limit_reached"`
	ProviderFailure        string                   `json:"provider_failure,omitempty"`
	// ProviderFailureClass is the inference attempt failure class
	// (capability_unsupported, backend_timeout, ...) when the provider
	// classified the failure. Traces sealed before it existed omit it.
	ProviderFailureClass string `json:"provider_failure_class,omitempty"`
	// Bracket holds the harness sentinel receipts read on the Data runtime
	// before and after the attempt. Import fails closed when it is missing.
	Bracket *AgentTraceBracket `json:"bracket,omitempty"`
	Steps   AgentTraceSteps    `json:"steps,omitempty"`
	// Observations are harness-owned governed calls, excluded from model tool
	// scores and conversation continuation. Their receipts join the ledger bracket.
	Observations []AgentScenarioObservation `json:"observations,omitempty"`
	// ContentJudgments are the pinned judge's verdicts on the scenario's judged
	// content criteria, in criterion order, recorded after the closing bracket
	// and before sealing so the grader stays a pure function of the trace.
	ContentJudgments []AgentContentJudgment `json:"content_judgments,omitempty"`
}

// AgentContentJudgment is one governed judge dispatch on one judged content
// criterion: the exact prompt sent, the verdict decoded from the signed
// inference result, and the receipt that binds the result to the Inference
// Operator. The verifier re-renders the prompt and re-decodes the verdict, so a
// recorded verdict cannot disagree with what the judge returned.
type AgentContentJudgment struct {
	CriterionID       string                     `json:"criterion_id"`
	ProviderAttemptID string                     `json:"provider_attempt_id"`
	TransactionID     string                     `json:"transaction_id"`
	JudgeVariantID    string                     `json:"judge_variant_id"`
	ServedModelTag    string                     `json:"served_model_tag"`
	ModelDigest       string                     `json:"model_digest"`
	RubricVersion     string                     `json:"rubric_version"`
	Messages          []AgentTraceMessage        `json:"messages"`
	Met               bool                       `json:"met"`
	Rationale         string                     `json:"rationale"`
	InferenceResult   *AgentTraceInferenceResult `json:"inference_result"`
	Receipt           *AgentTraceReceipt         `json:"receipt"`
}

// AgentTraceInput is the rendered conversation and the complete tool
// declarations sent on the first agent turn, exactly as dispatched.
type AgentTraceInput struct {
	Messages []AgentTraceMessage `json:"messages"`
	Tools    []AgentTraceTool    `json:"tools"`
}

// AgentTraceMessage is one text message of the model input. Role is the
// InferenceMessageRole enum name.
type AgentTraceMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// AgentTraceTool is one tool declaration sent to the model.
type AgentTraceTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	JSONSchema  string `json:"json_schema"`
}

// AgentTraceHeldContext is the complete conversation of the previous
// repetition of the same cell, carried verbatim into the next repetition and
// bound to the sealed trace it was rebuilt from. Workspace is the held trace's
// workspace, which the continuing repetition reuses so every path in the held
// conversation still names the same files.
type AgentTraceHeldContext struct {
	AssignmentID   string                     `json:"assignment_id"`
	Repetition     uint32                     `json:"repetition"`
	EvidenceDigest string                     `json:"evidence_digest"`
	Workspace      AgentTraceWorkspace        `json:"workspace"`
	Messages       []AgentTraceContextMessage `json:"messages"`
}

// AgentTraceContextMessage is one message of a held conversation. Role is the
// InferenceMessageRole enum name. An assistant message carries its Text and
// any ToolCalls; a tool message carries exactly one ToolResult.
type AgentTraceContextMessage struct {
	Role       string                       `json:"role"`
	Text       string                       `json:"text,omitempty"`
	ToolCalls  []AgentTraceContextToolCall  `json:"tool_calls,omitempty"`
	ToolResult *AgentTraceContextToolResult `json:"tool_result,omitempty"`
}

// AgentTraceContextToolCall is one tool call of a held assistant message.
type AgentTraceContextToolCall struct {
	CallID        string `json:"call_id,omitempty"`
	Name          string `json:"name"`
	ArgumentsJSON string `json:"arguments_json"`
}

// AgentTraceContextToolResult is the result a held tool message returned.
type AgentTraceContextToolResult struct {
	CallID     string `json:"call_id,omitempty"`
	Name       string `json:"name"`
	ResultJSON string `json:"result_json"`
}

// clarificationAt returns the clarification turn recorded at loopTurn. The
// executor records one for every interrogation it detects, answered or not, so
// a recorded turn identifies an interrogation without re-reading the text.
func (t *AgentTrace) clarificationAt(loopTurn int) (AgentClarificationTurn, bool) {
	for _, clarification := range t.Clarifications {
		if clarification.LoopTurn == loopTurn {
			return clarification, true
		}
	}
	return AgentClarificationTurn{}, false
}

// Conversation rebuilds the complete conversation this trace's model saw and
// produced: its held context, its input, and per completed turn the assistant
// message and its tool results, ending with the final answer. A v3 trace
// recorded no input and cannot be rebuilt.
func (t *AgentTrace) Conversation() ([]AgentTraceContextMessage, error) {
	if t == nil || t.Input == nil || len(t.Input.Messages) == 0 {
		return nil, fmt.Errorf("evaluation: agent trace conversation: input: %w", constants.ErrMissingRequiredField)
	}
	var messages []AgentTraceContextMessage
	if t.HeldContext != nil {
		messages = append(messages, t.HeldContext.Messages...)
	}
	for _, message := range t.Input.Messages {
		messages = append(messages, AgentTraceContextMessage{Role: message.Role, Text: message.Text})
	}
	assistantRole := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT.String()
	toolRole := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL.String()
	userRole := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String()
	for _, call := range t.ModelCalls {
		if !call.IsTurn() {
			continue
		}
		if !call.Completed() {
			break
		}
		clarification, interrogated := t.clarificationAt(call.LoopTurn)
		if call.ToolCallCount == 0 || interrogated {
			if call.Text != "" {
				messages = append(messages, AgentTraceContextMessage{Role: assistantRole, Text: call.Text})
			}
			if clarification.UserResponse != "" {
				messages = append(messages, AgentTraceContextMessage{Role: userRole, Text: clarification.UserResponse})
				continue
			}
			break
		}
		assistant := AgentTraceContextMessage{Role: assistantRole, Text: call.Text}
		var results []AgentTraceContextMessage
		for _, tool := range t.ToolCalls {
			if tool.ModelCallID != call.ProviderAttemptID {
				continue
			}
			assistant.ToolCalls = append(assistant.ToolCalls, AgentTraceContextToolCall{CallID: tool.ProviderCallID, Name: tool.ToolName, ArgumentsJSON: tool.ArgumentsJSON})
			results = append(results, AgentTraceContextMessage{Role: toolRole, ToolResult: &AgentTraceContextToolResult{CallID: tool.ProviderCallID, Name: tool.ToolName, ResultJSON: tool.ResultJSON}})
		}
		if len(assistant.ToolCalls) < call.ToolCallCount && t.ProviderFailure != "" {
			// A Tribunal inference failed inside this turn's tool calls and
			// ended the loop; the turn never completed.
			break
		}
		if len(assistant.ToolCalls) != call.ToolCallCount {
			return nil, fmt.Errorf("evaluation: agent trace conversation: turn %d recorded %d of %d tool calls: %w", call.LoopTurn, len(assistant.ToolCalls), call.ToolCallCount, constants.ErrEvidenceArtifactMalformed)
		}
		messages = append(messages, assistant)
		messages = append(messages, results...)
	}
	return messages, nil
}

// inferenceMessage converts one held message back into the model input part
// shape the executor dispatched it in.
func (m AgentTraceContextMessage) inferenceMessage() (*operatorv1.InferenceMessage, error) {
	role, ok := operatorv1.InferenceMessageRole_value[m.Role]
	if !ok || role == int32(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_UNSPECIFIED) {
		return nil, fmt.Errorf("evaluation: held context message role %q: %w", m.Role, constants.ErrEvidenceArtifactMalformed)
	}
	message := &operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole(role)}
	if m.Text != "" {
		message.Parts = append(message.Parts, &operatorv1.InferenceMessagePart{Part: &operatorv1.InferenceMessagePart_Text{Text: m.Text}})
	}
	for _, call := range m.ToolCalls {
		message.Parts = append(message.Parts, &operatorv1.InferenceMessagePart{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId: call.CallID, Name: call.Name, ArgumentsJson: call.ArgumentsJSON,
		}}})
	}
	if m.ToolResult != nil {
		message.Parts = append(message.Parts, &operatorv1.InferenceMessagePart{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{
			CallId: m.ToolResult.CallID, Name: m.ToolResult.Name, ResultJson: m.ToolResult.ResultJSON,
		}}})
	}
	if len(message.Parts) == 0 {
		return nil, fmt.Errorf("evaluation: held context %s message is empty: %w", m.Role, constants.ErrEvidenceArtifactMalformed)
	}
	return message, nil
}

// newAgentTraceInput records the dispatched first-turn input. Only text
// parts occur in it; any other part is a harness defect.
func newAgentTraceInput(messages []*operatorv1.InferenceMessage, tools []*operatorv1.InferenceToolDeclaration) (*AgentTraceInput, error) {
	recorded, err := agentTraceMessages(messages)
	if err != nil {
		return nil, fmt.Errorf("evaluation: record agent trace input: %w", err)
	}
	input := &AgentTraceInput{Messages: recorded, Tools: make([]AgentTraceTool, 0, len(tools))}
	for _, tool := range tools {
		input.Tools = append(input.Tools, AgentTraceTool{Name: tool.GetName(), Description: tool.GetDescription(), JSONSchema: tool.GetJsonSchema()})
	}
	return input, nil
}

// agentTraceMessages records dispatched text messages exactly as sent. Only
// text parts occur in a recorded prompt; any other part is a harness defect.
func agentTraceMessages(messages []*operatorv1.InferenceMessage) ([]AgentTraceMessage, error) {
	recorded := make([]AgentTraceMessage, 0, len(messages))
	for _, message := range messages {
		var text strings.Builder
		for _, part := range message.GetParts() {
			value, ok := part.GetPart().(*operatorv1.InferenceMessagePart_Text)
			if !ok {
				return nil, fmt.Errorf("non-text message part: %w", constants.ErrEvidenceArtifactMalformed)
			}
			text.WriteString(value.Text)
		}
		recorded = append(recorded, AgentTraceMessage{Role: message.GetRole().String(), Text: text.String()})
	}
	return recorded, nil
}

// AgentTraceRuntime is one Operator runtime a role executed on, with its
// actuator key pinned as bound at enrollment. VerifyAgentTraceEvidence checks
// each receipt against the anchor of the runtime that signed it.
type AgentTraceRuntime struct {
	OperatorID string            `json:"operator_id"`
	SessionID  string            `json:"session_id"`
	Anchor     *AgentTraceAnchor `json:"anchor,omitempty"`
}

// agentTraceRuntime records one topology runtime in the trace.
func agentTraceRuntime(runtime CampaignOperatorRuntime) AgentTraceRuntime {
	traced := AgentTraceRuntime{OperatorID: runtime.OperatorID, SessionID: runtime.OperatorSessionID}
	if runtime.Anchor != nil {
		traced.Anchor = &AgentTraceAnchor{ConformanceAnchor: runtime.Anchor}
	}
	return traced
}

// CarriesModelIdentity reports whether the trace's schema seals each model
// call's signed inference result and model identity (v7 and later).
func (t *AgentTrace) CarriesModelIdentity() bool {
	return t != nil && (t.SchemaVersion == agentTraceSchemaVersion || t.SchemaVersion == agentTraceSchemaVersionV9 || t.SchemaVersion == agentTraceSchemaVersionV8 || t.SchemaVersion == agentTraceSchemaVersionV7)
}

// carriesObservationEvidence reports whether the trace's schema records
// harness observations and native tool request evidence (v9 and later).
func (t *AgentTrace) carriesObservationEvidence() bool {
	return t != nil && (t.SchemaVersion == agentTraceSchemaVersion || t.SchemaVersion == agentTraceSchemaVersionV9)
}

// SharedRuntime reports whether both roles executed on one runtime.
func (t *AgentTrace) SharedRuntime() bool {
	return t != nil && t.Inference.OperatorID == t.Data.OperatorID
}

// ToProto converts the canonical agent trace to its protobuf contract form
// for persona and judge grading.
func (t *AgentTrace) ToProto() *evalv1.AgentTrace {
	if t == nil {
		return nil
	}
	finishReason := evalv1.AgentFinishReason_AGENT_FINISH_REASON_STOPPED
	if t.ToolTurnLimitReached {
		finishReason = evalv1.AgentFinishReason_AGENT_FINISH_REASON_TOOL_TURN_LIMIT
	} else if t.TerminalClassification == AgentTerminalClassificationTimeout {
		finishReason = evalv1.AgentFinishReason_AGENT_FINISH_REASON_WALL_CLOCK_LIMIT
	} else if t.ProviderFailure != "" {
		finishReason = evalv1.AgentFinishReason_AGENT_FINISH_REASON_ERROR
	}
	var claims []string
	for _, call := range t.ModelCalls {
		if call.TransactionID != "" {
			claims = append(claims, call.TransactionID)
		}
	}
	for _, tool := range t.ToolCalls {
		if tool.TransactionID != "" {
			claims = append(claims, tool.TransactionID)
		}
	}
	for _, observation := range t.Observations {
		if observation.Call.TransactionID != "" {
			claims = append(claims, observation.Call.TransactionID)
		}
	}
	return &evalv1.AgentTrace{
		AttemptId:             t.EvaluationAttemptID,
		Steps:                 t.Steps,
		ClaimedTransactionIds: claims,
		FinishReason:          finishReason,
		FinalReply:            t.FinalOutput,
	}
}

// AgentTraceReceipt is a signed EventReceipt carried in the trace as
// canonical protojson. It is decoded strictly; unknown fields fail.
type AgentTraceReceipt struct{ *operatorv1.EventReceipt }

// AgentTraceEnvelope retains canonical typed intent for offline verification.
type AgentTraceEnvelope struct{ *commonv1.GovernanceEnvelope }

func (e AgentTraceEnvelope) MarshalJSON() ([]byte, error) {
	return evalv1.MarshalCanonical(e.GovernanceEnvelope)
}

func (e *AgentTraceEnvelope) UnmarshalJSON(body []byte) error {
	envelope := &commonv1.GovernanceEnvelope{}
	if err := protojson.Unmarshal(body, envelope); err != nil {
		return fmt.Errorf("evaluation: decode request envelope: %w", err)
	}
	e.GovernanceEnvelope = envelope
	return nil
}

// MarshalJSON writes the receipt as canonical protojson.
func (r AgentTraceReceipt) MarshalJSON() ([]byte, error) {
	if r.EventReceipt == nil {
		return nil, fmt.Errorf("evaluation: encode agent trace receipt: %w", constants.ErrMissingRequiredField)
	}
	return evalv1.MarshalCanonical(r.EventReceipt)
}

// UnmarshalJSON decodes canonical protojson strictly.
func (r *AgentTraceReceipt) UnmarshalJSON(b []byte) error {
	receipt := &operatorv1.EventReceipt{}
	if err := unmarshalAgentTraceCanonical(b, receipt); err != nil {
		return fmt.Errorf("evaluation: decode agent trace receipt: %w", err)
	}
	r.EventReceipt = receipt
	return nil
}

// AgentTraceAnchor is one runtime's pinned actuator key, carried in the trace
// as canonical protojson like a receipt.
type AgentTraceAnchor struct{ *evalv1.ConformanceAnchor }

// MarshalJSON writes the anchor as canonical protojson.
func (a AgentTraceAnchor) MarshalJSON() ([]byte, error) {
	if a.ConformanceAnchor == nil {
		return nil, fmt.Errorf("evaluation: encode agent trace anchor: %w", constants.ErrMissingRequiredField)
	}
	return evalv1.MarshalCanonical(a.ConformanceAnchor)
}

// UnmarshalJSON decodes canonical protojson strictly.
func (a *AgentTraceAnchor) UnmarshalJSON(b []byte) error {
	anchor := &evalv1.ConformanceAnchor{}
	if err := unmarshalAgentTraceCanonical(b, anchor); err != nil {
		return fmt.Errorf("evaluation: decode agent trace anchor: %w", err)
	}
	a.ConformanceAnchor = anchor
	return nil
}

// AgentTraceSteps are the persona steps of the attempt, carried in the trace
// as an array of canonical protojson values like a receipt: a step's output is
// a protobuf oneof, which encoding/json cannot decode.
type AgentTraceSteps []*evalv1.AgentStep

// MarshalJSON writes each step as canonical protojson.
func (s AgentTraceSteps) MarshalJSON() ([]byte, error) {
	encoded := make([]json.RawMessage, 0, len(s))
	for i, step := range s {
		if step == nil {
			return nil, fmt.Errorf("evaluation: encode agent trace step %d: %w", i, constants.ErrMissingRequiredField)
		}
		canonical, err := evalv1.MarshalCanonical(step)
		if err != nil {
			return nil, fmt.Errorf("evaluation: encode agent trace step %d: %w", i, err)
		}
		encoded = append(encoded, canonical)
	}
	return json.Marshal(encoded)
}

// UnmarshalJSON decodes each step's canonical protojson strictly.
func (s *AgentTraceSteps) UnmarshalJSON(b []byte) error {
	var encoded []json.RawMessage
	if err := json.Unmarshal(b, &encoded); err != nil {
		return fmt.Errorf("evaluation: decode agent trace steps: %w: %w", constants.ErrEvidenceArtifactMalformed, err)
	}
	steps := make(AgentTraceSteps, 0, len(encoded))
	for i, raw := range encoded {
		step := &evalv1.AgentStep{}
		if err := unmarshalAgentTraceCanonical(raw, step); err != nil {
			return fmt.Errorf("evaluation: decode agent trace step %d: %w", i, err)
		}
		steps = append(steps, step)
	}
	*s = steps
	return nil
}

// AgentTraceBracket is the pair of harness sentinels around the attempt.
type AgentTraceBracket struct {
	Before AgentTraceReceipt `json:"before"`
	After  AgentTraceReceipt `json:"after"`
}

// unmarshalAgentTraceCanonical decodes one canonical protojson value embedded in
// the trace. encoding/json HTML-escapes <, > and & in the bytes it writes for
// a nested value, so the trace holds those characters escaped while protojson
// writes them raw. The value is therefore compared with the trace's own
// encoding of its canonical form. Any other spelling still fails.
func unmarshalAgentTraceCanonical(encoded []byte, message proto.Message) error {
	if err := (protojson.UnmarshalOptions{}).Unmarshal(encoded, message); err != nil {
		return fmt.Errorf("%w: unmarshal canonical protojson: %w", constants.ErrEvidenceArtifactMalformed, err)
	}
	canonical, err := evalv1.MarshalCanonical(message)
	if err != nil {
		return err
	}
	expected, err := json.Marshal(json.RawMessage(canonical))
	if err != nil {
		return fmt.Errorf("evaluation: encode canonical protojson: %w", err)
	}
	if !bytes.Equal(encoded, expected) {
		return fmt.Errorf("%w: input is not canonical protojson", constants.ErrEvidenceArtifactMalformed)
	}
	return nil
}

// ScenarioWorkspace returns the workspace the trace ran in.
func (t *AgentTrace) ScenarioWorkspace() ScenarioWorkspace {
	if t == nil {
		return ScenarioWorkspace{}
	}
	return ScenarioWorkspace{Root: t.Workspace.Root, OperatorWorkingDirectory: t.Workspace.OperatorWorkingDirectory}
}

// RoleInvoked reports whether the designated role produced at least one
// governed agent turn.
func (t *AgentTrace) RoleInvoked() bool {
	return t != nil && t.DesignatedRole != "" && t.HasGovernedModelCalls()
}

// HasGovernedModelCalls reports whether any recorded inference completed as a
// governed transaction.
func (t *AgentTrace) HasGovernedModelCalls() bool {
	if t == nil {
		return false
	}
	for _, call := range t.ModelCalls {
		if call.IsTurn() && call.Completed() {
			return true
		}
	}
	return false
}

// DeclaredTool reports whether a scored agent turn declared tool to the model.
func (t *AgentTrace) DeclaredTool(tool string) bool {
	if t == nil {
		return false
	}
	for _, call := range t.ModelCalls {
		if !call.IsTurn() {
			continue
		}
		for _, declared := range call.ToolsDeclared {
			if declared == tool {
				return true
			}
		}
	}
	return false
}

// AgentTraceStore persists canonical agent trace evidence for one assignment.
type AgentTraceStore interface {
	SaveAssignmentAgentTrace(ctx context.Context, runID, assignmentID string, body []byte) error
}

// SealAgentTrace stamps the evidence digest and returns the canonical body.
func SealAgentTrace(trace *AgentTrace) ([]byte, error) {
	if trace == nil || trace.RunID == "" || trace.AssignmentID == "" || trace.EvaluationAttemptID == "" {
		return nil, fmt.Errorf("evaluation: seal agent trace: %w", constants.ErrMissingRequiredField)
	}
	// An earlier version is sealed evidence; restamping it would claim
	// captures it never had.
	if trace.SchemaVersion != "" && trace.SchemaVersion != agentTraceSchemaVersion {
		return nil, fmt.Errorf("evaluation: seal agent trace: schema version %q: %w", trace.SchemaVersion, constants.ErrEvidenceArtifactMalformed)
	}
	if trace.Input == nil || len(trace.Input.Messages) == 0 {
		return nil, fmt.Errorf("evaluation: seal agent trace: input: %w", constants.ErrMissingRequiredField)
	}
	if err := rejectShellTextSteps(trace.Steps); err != nil {
		return nil, fmt.Errorf("evaluation: seal agent trace: %w", err)
	}
	trace.SchemaVersion = agentTraceSchemaVersion
	digest, err := ComputeAgentTraceDigest(trace)
	if err != nil {
		return nil, err
	}
	trace.EvidenceDigest = digest
	return MarshalCanonicalJSONObject(trace)
}

// rejectShellTextSteps refuses v8 steps that carry shell text: those step
// fields exist only to decode agent-trace.v7 and earlier history.
func rejectShellTextSteps(steps AgentTraceSteps) error {
	for i, step := range steps {
		if step.GetCandidate().GetCommand() != "" || step.GetReview().GetCommand() != "" {
			return fmt.Errorf("step %d carries shell text: %w", i, constants.ErrEvidenceArtifactMalformed)
		}
	}
	return nil
}

// ComputeAgentTraceDigest returns the SHA-256 digest of one trace with
// evidence_digest cleared before canonicalization.
func ComputeAgentTraceDigest(trace *AgentTrace) (string, error) {
	if trace == nil {
		return "", fmt.Errorf("evaluation: compute agent trace digest: %w", constants.ErrMissingRequiredField)
	}
	payload := *trace
	payload.EvidenceDigest = ""
	body, err := MarshalCanonicalJSONObject(payload)
	if err != nil {
		return "", fmt.Errorf("evaluation: compute agent trace digest: %w", err)
	}
	return models.SHA256Hex(body), nil
}

// validateAgentTraceRules checks structural requirements for a trace's schema version.
func validateAgentTraceRules(trace *AgentTrace) error {
	if trace == nil || trace.EvidenceDigest == "" {
		return fmt.Errorf("evaluation: validate agent trace: evidence_digest: %w", constants.ErrMissingRequiredField)
	}
	switch trace.SchemaVersion {
	case agentTraceSchemaVersion, agentTraceSchemaVersionV9, agentTraceSchemaVersionV8, agentTraceSchemaVersionV7, agentTraceSchemaVersionV6, agentTraceSchemaVersionV5, agentTraceSchemaVersionV4:
		if trace.Input == nil || len(trace.Input.Messages) == 0 {
			return fmt.Errorf("evaluation: validate agent trace: input: %w", constants.ErrMissingRequiredField)
		}
		if trace.HeldContext != nil && trace.SchemaVersion == agentTraceSchemaVersionV4 {
			return fmt.Errorf("evaluation: validate agent trace: %s carries held context: %w", agentTraceSchemaVersionV4, constants.ErrEvidenceArtifactMalformed)
		}
		if trace.HeldContext != nil && (trace.HeldContext.AssignmentID == "" || trace.HeldContext.EvidenceDigest == "" || len(trace.HeldContext.Messages) == 0) {
			return fmt.Errorf("evaluation: validate agent trace: held context: %w", constants.ErrMissingRequiredField)
		}
	case agentTraceSchemaVersionV3:
		if trace.Input != nil || trace.HeldContext != nil {
			return fmt.Errorf("evaluation: validate agent trace: %s carries input: %w", agentTraceSchemaVersionV3, constants.ErrEvidenceArtifactMalformed)
		}
	default:
		return fmt.Errorf("evaluation: validate agent trace: schema version %q: %w", trace.SchemaVersion, constants.ErrEvidenceArtifactMalformed)
	}
	if trace.SchemaVersion != agentTraceSchemaVersion && len(trace.ContentJudgments) > 0 {
		return fmt.Errorf("evaluation: validate agent trace: historical trace carries content judgments: %w", constants.ErrEvidenceArtifactMalformed)
	}
	if !trace.carriesObservationEvidence() && len(trace.Observations) > 0 {
		return fmt.Errorf("evaluation: validate agent trace: historical trace carries scenario observations: %w", constants.ErrEvidenceArtifactMalformed)
	}
	if !trace.carriesObservationEvidence() {
		for _, call := range trace.ToolCalls {
			if call.RequestEnvelope != nil || call.IngressRefusalCode != operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED {
				return fmt.Errorf("evaluation: validate agent trace: historical trace carries v9 tool evidence: %w", constants.ErrEvidenceArtifactMalformed)
			}
		}
	}
	if trace.carriesObservationEvidence() || trace.SchemaVersion == agentTraceSchemaVersionV8 {
		if err := rejectShellTextSteps(trace.Steps); err != nil {
			return fmt.Errorf("evaluation: validate agent trace: %w", err)
		}
	} else {
		for _, call := range trace.ToolCalls {
			if call.ApprovedArgumentsJSON != "" {
				return fmt.Errorf("evaluation: validate agent trace: %s carries approved arguments: %w", trace.SchemaVersion, constants.ErrEvidenceArtifactMalformed)
			}
		}
	}
	return nil
}

// ValidateAgentTraceDigest verifies one trace's digest binding.
func ValidateAgentTraceDigest(trace *AgentTrace) error {
	if err := validateAgentTraceRules(trace); err != nil {
		return err
	}
	expected, err := ComputeAgentTraceDigest(trace)
	if err != nil {
		return err
	}
	if trace.EvidenceDigest != expected {
		return fmt.Errorf("evaluation: validate agent trace: evidence digest: %w", constants.ErrChecksumMismatch)
	}
	return nil
}

// BuildAssignmentAgentTraceEvidenceReference returns a content-addressed
// evidence reference for one persisted agent trace body.
func BuildAssignmentAgentTraceEvidenceReference(runID, attemptID string, body []byte, producedAt time.Time) (*compliancev1.ComplianceEvidenceReference, error) {
	if runID == "" || attemptID == "" || len(body) == 0 {
		return nil, fmt.Errorf("evaluation: build agent trace evidence reference: %w", constants.ErrMissingRequiredField)
	}
	if err := complianceevidence.ValidateCanonicalJSON(body); err != nil {
		return nil, fmt.Errorf("evaluation: build agent trace evidence reference: %w", err)
	}
	artifactID := complianceevidence.ContentAddress(complianceevidence.ArtifactTypeEvaluationAssignmentTrace, body)
	_, digest, ok := complianceevidence.ParseContentAddress(artifactID)
	if !ok {
		return nil, fmt.Errorf("evaluation: build agent trace evidence reference: content address: %w", constants.ErrEvidenceArtifactMalformed)
	}
	if producedAt.IsZero() {
		producedAt = time.Now().UTC()
	}
	return &compliancev1.ComplianceEvidenceReference{
		ArtifactId:         artifactID,
		ArtifactType:       string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace),
		Sha256:             digest,
		MediaType:          constants.MediaTypeJSON,
		SchemaRef:          "ztd.eval.v1.EvaluationAssignmentAgentTrace",
		ProducerIdentity:   "ztd-eval-campaign",
		ProducedAt:         timestamppb.New(producedAt),
		ScopeId:            runID,
		RunId:              runID,
		AttemptId:          attemptID,
		VerificationStatus: "imported",
	}, nil
}

// SaveAssignmentAgentTrace persists one canonical agent trace for one
// assignment.
func (s *Store) SaveAssignmentAgentTrace(ctx context.Context, runID, assignmentID string, body []byte) error {
	if len(body) == 0 {
		return missingField(constants.ErrEvaluationReportPersistFailed, "assignment agent trace body")
	}
	if err := s.validateAccess(constants.ErrEvaluationReportPersistFailed, runIDElement(runID), assignmentIDElement(assignmentID)); err != nil {
		return err
	}
	if err := complianceevidence.ValidateCanonicalJSON(body); err != nil {
		return fmt.Errorf("%w: assignment agent trace is not canonical JSON: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	path := s.layout.assignmentAgentTracePath(runID, assignmentID)
	if err := s.files.MkdirAll(ctx, filepath.Dir(path), constants.PermDirPrivate); err != nil {
		return fmt.Errorf("%w: create assignment agent trace directory: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	if err := s.files.WriteFile(ctx, path, body, constants.PermFileReadOnly); err != nil {
		return fmt.Errorf("%w: write assignment agent trace: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	return nil
}

// LoadAssignmentAgentTrace reads and digest-checks one persisted agent trace.
func (s *Store) LoadAssignmentAgentTrace(ctx context.Context, runID, assignmentID string) (*AgentTrace, error) {
	trace, _, err := s.LoadAssignmentAgentTraceArtifact(ctx, runID, assignmentID)
	return trace, err
}

// LoadAssignmentAgentTraceArtifact reads and digest-checks one persisted agent
// trace and returns the exact persisted canonical body alongside it, so public
// proof publication ships the bytes the evidence ref hashes.
func (s *Store) LoadAssignmentAgentTraceArtifact(ctx context.Context, runID, assignmentID string) (*AgentTrace, []byte, error) {
	if s == nil || s.files == nil {
		return nil, nil, fmt.Errorf("evaluation: load assignment agent trace: %w", constants.ErrMissingRequiredField)
	}
	return loadAssignmentAgentTrace(ctx, s.files, s.layout, runID, assignmentID)
}

func loadAssignmentAgentTrace(ctx context.Context, reader complianceevidence.ArtifactReader, layout evalLayout, runID, assignmentID string) (*AgentTrace, []byte, error) {
	if reader == nil {
		return nil, nil, fmt.Errorf("evaluation: load assignment agent trace: reader: %w", constants.ErrMissingRequiredField)
	}
	if err := validatePathElements(runIDElement(runID), assignmentIDElement(assignmentID)); err != nil {
		return nil, nil, fmt.Errorf("evaluation: load assignment agent trace: %w", err)
	}
	body, err := reader.ReadFile(ctx, layout.assignmentAgentTracePath(runID, assignmentID))
	if err != nil {
		return nil, nil, err
	}
	trace, err := DecodeAgentTrace(body)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: load assignment agent trace: %w", err)
	}
	if trace.RunID != runID || trace.AssignmentID != assignmentID {
		return nil, nil, fmt.Errorf("evaluation: load assignment agent trace: %w", constants.ErrEvidenceScopeMismatch)
	}
	return trace, body, nil
}

// DecodeAgentTrace decodes one sealed trace body, such as a published
// assignment_agent_trace proof artifact, and checks its canonical encoding
// and evidence digest.
func DecodeAgentTrace(body []byte) (*AgentTrace, error) {
	if err := complianceevidence.ValidateCanonicalJSON(body); err != nil {
		return nil, fmt.Errorf("evaluation: decode agent trace: %w: %w", constants.ErrEvidenceArtifactMalformed, err)
	}
	trace := &AgentTrace{}
	if err := json.Unmarshal(body, trace); err != nil {
		return nil, fmt.Errorf("evaluation: decode agent trace: %w", err)
	}
	if err := validateAgentTraceRules(trace); err != nil {
		return nil, err
	}
	prefix := `{"schema_version":"` + trace.SchemaVersion + `","evidence_digest":"` + trace.EvidenceDigest + `",`
	if strings.HasPrefix(string(body), prefix) {
		preimage := `{"schema_version":"` + trace.SchemaVersion + `","evidence_digest":"",` + strings.TrimPrefix(string(body), prefix)
		if models.SHA256Hex([]byte(preimage)) != trace.EvidenceDigest {
			return nil, fmt.Errorf("evaluation: validate agent trace: evidence digest: %w", constants.ErrChecksumMismatch)
		}
		return trace, nil
	}
	if err := ValidateAgentTraceDigest(trace); err != nil {
		return nil, err
	}
	return trace, nil
}
