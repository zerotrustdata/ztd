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
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// agentTraceSchemaVersion is the version every new trace is sealed with. v4
// adds the exact model input, per-turn thinking, and provider tool-call IDs.
// A v3 trace remains valid evidence; its model input was not captured and is
// never reconstructed.
const (
	agentTraceSchemaVersion   = "agent-trace.v4"
	agentTraceSchemaVersionV3 = "agent-trace.v3"
)

// agentModelCallPurposeTurn is the purpose of every governed agent inference.
// Each one is a scored step of the tool loop.
const agentModelCallPurposeTurn = "agent_turn"

// Policy decisions recorded on a governed tool call.
const (
	agentPolicyDecisionAllow = "allow"
	agentPolicyDecisionDeny  = "deny"
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
	ProviderAttemptID       string   `json:"provider_attempt_id"`
	TransactionID           string   `json:"transaction_id"`
	Purpose                 string   `json:"purpose"`
	LoopTurn                int      `json:"loop_turn"`
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
}

// Completed reports whether the call returned a governed result.
func (c AgentModelCall) Completed() bool {
	return c.TransactionID != "" && c.Error == ""
}

// AgentToolCall is one tool call the model made and what it returned. An
// operator tool's CallID is the governed transaction ID when the Gateway
// admitted the dispatch, so governed actions resolve to their call.
type AgentToolCall struct {
	CallID      string `json:"call_id"`
	ModelCallID string `json:"model_call_id"`
	// ProviderCallID is the call ID the model returned; the assistant and tool
	// result messages sent on the next turn carry it.
	ProviderCallID string `json:"provider_call_id,omitempty"`
	ToolName       string `json:"tool_name"`
	ArgumentsJSON  string `json:"arguments_json"`
	Command        string `json:"command,omitempty"`
	ResultJSON     string `json:"result_json,omitempty"`
	ErrorType      string `json:"error_type,omitempty"`
	Error          string `json:"error,omitempty"`
	Success        bool   `json:"success"`
	IsOperatorTool bool   `json:"is_operator_tool"`
	LoopTurn       int    `json:"loop_turn"`
	ActionType     string `json:"action_type,omitempty"`
	TransactionID  string `json:"transaction_id,omitempty"`
	PolicyDecision string `json:"policy_decision,omitempty"`
	// Receipt is the signed receipt the Gateway answered the dispatch with: the
	// executed receipt, or a governance rejection's receipt. A refusal has none.
	Receipt *AgentTraceReceipt `json:"receipt,omitempty"`
	// Seeded marks a prior failing call replayed from the scenario seed for
	// trajectory analysis only; it is never persisted.
	Seeded bool `json:"-"`
}

// ShowedGuidance reports whether the call returned model-visible guidance.
func (c AgentToolCall) ShowedGuidance() bool {
	return c.Error != ""
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
	// Input is the exact model input of the first turn. Every later turn sent
	// this input followed, per earlier turn, by one assistant message (the
	// call's Text, then its tool calls with ProviderCallID, ToolName and
	// ArgumentsJSON) and one tool message per call carrying ResultJSON. v3
	// traces have none.
	Input                *AgentTraceInput `json:"input,omitempty"`
	ModelCalls           []AgentModelCall `json:"model_calls"`
	ToolCalls            []AgentToolCall  `json:"tool_calls"`
	FinalOutput          string           `json:"final_output"`
	ToolTurnLimitReached bool             `json:"tool_turn_limit_reached"`
	ProviderFailure      string           `json:"provider_failure,omitempty"`
	// ProviderFailureClass is the inference attempt failure class
	// (capability_unsupported, backend_timeout, ...) when the provider
	// classified the failure. Traces sealed before it existed omit it.
	ProviderFailureClass string `json:"provider_failure_class,omitempty"`
	// Bracket holds the harness sentinel receipts read on the Data runtime
	// before and after the attempt. Import fails closed when it is missing.
	Bracket *AgentTraceBracket `json:"bracket,omitempty"`
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

// newAgentTraceInput records the dispatched first-turn input. Only text
// parts occur in it; any other part is a harness defect.
func newAgentTraceInput(messages []*operatorv1.InferenceMessage, tools []*operatorv1.InferenceToolDeclaration) (*AgentTraceInput, error) {
	input := &AgentTraceInput{Messages: make([]AgentTraceMessage, 0, len(messages)), Tools: make([]AgentTraceTool, 0, len(tools))}
	for _, message := range messages {
		var text strings.Builder
		for _, part := range message.GetParts() {
			value, ok := part.GetPart().(*operatorv1.InferenceMessagePart_Text)
			if !ok {
				return nil, fmt.Errorf("evaluation: record agent trace input: non-text input part: %w", constants.ErrEvidenceArtifactMalformed)
			}
			text.WriteString(value.Text)
		}
		input.Messages = append(input.Messages, AgentTraceMessage{Role: message.GetRole().String(), Text: text.String()})
	}
	for _, tool := range tools {
		input.Tools = append(input.Tools, AgentTraceTool{Name: tool.GetName(), Description: tool.GetDescription(), JSONSchema: tool.GetJsonSchema()})
	}
	return input, nil
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

// SharedRuntime reports whether both roles executed on one runtime.
func (t *AgentTrace) SharedRuntime() bool {
	return t != nil && t.Inference.OperatorID == t.Data.OperatorID
}

// AgentTraceReceipt is a signed ActionReceipt carried in the trace as
// canonical protojson. It is decoded strictly; unknown fields fail.
type AgentTraceReceipt struct{ *operatorv1.ActionReceipt }

// MarshalJSON writes the receipt as canonical protojson.
func (r AgentTraceReceipt) MarshalJSON() ([]byte, error) {
	if r.ActionReceipt == nil {
		return nil, fmt.Errorf("evaluation: encode agent trace receipt: %w", constants.ErrMissingRequiredField)
	}
	return evalv1.MarshalCanonical(r.ActionReceipt)
}

// UnmarshalJSON decodes canonical protojson strictly.
func (r *AgentTraceReceipt) UnmarshalJSON(b []byte) error {
	receipt := &operatorv1.ActionReceipt{}
	if err := unmarshalAgentTraceCanonical(b, receipt); err != nil {
		return fmt.Errorf("evaluation: decode agent trace receipt: %w", err)
	}
	r.ActionReceipt = receipt
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
		if call.Purpose == agentModelCallPurposeTurn && call.Completed() {
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
		if call.Purpose != agentModelCallPurposeTurn {
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
	trace.SchemaVersion = agentTraceSchemaVersion
	digest, err := ComputeAgentTraceDigest(trace)
	if err != nil {
		return nil, err
	}
	trace.EvidenceDigest = digest
	return MarshalCanonicalJSONObject(trace)
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

// ValidateAgentTraceDigest verifies one trace's digest binding.
func ValidateAgentTraceDigest(trace *AgentTrace) error {
	if trace == nil || trace.EvidenceDigest == "" {
		return fmt.Errorf("evaluation: validate agent trace: evidence_digest: %w", constants.ErrMissingRequiredField)
	}
	switch trace.SchemaVersion {
	case agentTraceSchemaVersion:
		if trace.Input == nil || len(trace.Input.Messages) == 0 {
			return fmt.Errorf("evaluation: validate agent trace: input: %w", constants.ErrMissingRequiredField)
		}
	case agentTraceSchemaVersionV3:
		if trace.Input != nil {
			return fmt.Errorf("evaluation: validate agent trace: %s carries input: %w", agentTraceSchemaVersionV3, constants.ErrEvidenceArtifactMalformed)
		}
	default:
		return fmt.Errorf("evaluation: validate agent trace: schema version %q: %w", trace.SchemaVersion, constants.ErrEvidenceArtifactMalformed)
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
	if s == nil || s.files == nil || !complianceevidence.ValidPathElement(runID) || !complianceevidence.ValidPathElement(assignmentID) || len(body) == 0 {
		return fmt.Errorf("%w: assignment agent trace, run ID, assignment ID, and body are required", constants.ErrEvaluationReportPersistFailed)
	}
	if err := complianceevidence.ValidateCanonicalJSON(body); err != nil {
		return fmt.Errorf("%w: assignment agent trace is not canonical JSON: %v", constants.ErrEvaluationReportPersistFailed, err)
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
	if reader == nil || !complianceevidence.ValidPathElement(runID) || !complianceevidence.ValidPathElement(assignmentID) {
		return nil, nil, fmt.Errorf("evaluation: load assignment agent trace: %w", constants.ErrMissingRequiredField)
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
		return nil, fmt.Errorf("evaluation: decode agent trace: %w: %v", constants.ErrEvidenceArtifactMalformed, err)
	}
	trace := &AgentTrace{}
	if err := json.Unmarshal(body, trace); err != nil {
		return nil, fmt.Errorf("evaluation: decode agent trace: %w", err)
	}
	if err := ValidateAgentTraceDigest(trace); err != nil {
		return nil, err
	}
	return trace, nil
}
