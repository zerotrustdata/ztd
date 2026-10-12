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
	"path/filepath"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

const agentTraceSchemaVersion = "agent-trace.v1"

// Agent model call purposes. A turn is one scored step of the tool loop; a
// command call turns a run_commands_with_operator request into the exact
// command the Data Operator executes.
const (
	agentModelCallPurposeTurn    = "agent_turn"
	agentModelCallPurposeCommand = "command_generation"
)

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

// AgentModelCall is one governed inference the agent executor dispatched.
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
	Text                    string   `json:"text,omitempty"`
	ToolCallCount           int      `json:"tool_call_count"`
}

// AgentToolCall is one tool call the model made and what it returned. An
// operator tool's CallID is the governed transaction ID when the Gateway
// admitted the dispatch, so governed actions resolve to their call.
type AgentToolCall struct {
	CallID         string `json:"call_id"`
	ModelCallID    string `json:"model_call_id"`
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
	SchemaVersion        string              `json:"schema_version"`
	EvidenceDigest       string              `json:"evidence_digest"`
	RunID                string              `json:"run_id"`
	AssignmentID         string              `json:"assignment_id"`
	EvaluationAttemptID  string              `json:"evaluation_attempt_id"`
	CampaignID           string              `json:"campaign_id"`
	ScenarioID           string              `json:"scenario_id"`
	DesignatedRole       string              `json:"designated_role"`
	ServedModelTag       string              `json:"served_model_tag"`
	ModelDigest          string              `json:"model_digest"`
	ModelRegistryDigest  string              `json:"model_registry_digest"`
	InferenceSessionID   string              `json:"inference_session_id"`
	DataOperatorID       string              `json:"data_operator_id"`
	DataSessionID        string              `json:"data_session_id"`
	Workspace            AgentTraceWorkspace `json:"workspace"`
	ModelCalls           []AgentModelCall    `json:"model_calls"`
	ToolCalls            []AgentToolCall     `json:"tool_calls"`
	FinalOutput          string              `json:"final_output"`
	ToolTurnLimitReached bool                `json:"tool_turn_limit_reached"`
	ProviderFailure      string              `json:"provider_failure,omitempty"`
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

// HasGovernedModelCalls reports whether any recorded inference carries a
// governed transaction.
func (t *AgentTrace) HasGovernedModelCalls() bool {
	if t == nil {
		return false
	}
	for _, call := range t.ModelCalls {
		if call.Purpose == agentModelCallPurposeTurn && call.TransactionID != "" {
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
	if trace.SchemaVersion != agentTraceSchemaVersion {
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
	if s == nil || s.files == nil {
		return nil, fmt.Errorf("evaluation: load assignment agent trace: %w", constants.ErrMissingRequiredField)
	}
	return loadAssignmentAgentTrace(ctx, s.files, s.layout, runID, assignmentID)
}

func loadAssignmentAgentTrace(ctx context.Context, reader complianceevidence.ArtifactReader, layout evalLayout, runID, assignmentID string) (*AgentTrace, error) {
	if reader == nil || !complianceevidence.ValidPathElement(runID) || !complianceevidence.ValidPathElement(assignmentID) {
		return nil, fmt.Errorf("evaluation: load assignment agent trace: %w", constants.ErrMissingRequiredField)
	}
	body, err := reader.ReadFile(ctx, layout.assignmentAgentTracePath(runID, assignmentID))
	if err != nil {
		return nil, err
	}
	if err := complianceevidence.ValidateCanonicalJSON(body); err != nil {
		return nil, fmt.Errorf("evaluation: load assignment agent trace: %w", err)
	}
	trace := &AgentTrace{}
	if err := json.Unmarshal(body, trace); err != nil {
		return nil, fmt.Errorf("evaluation: load assignment agent trace: %w", err)
	}
	if trace.RunID != runID || trace.AssignmentID != assignmentID {
		return nil, fmt.Errorf("evaluation: load assignment agent trace: %w", constants.ErrEvidenceScopeMismatch)
	}
	if err := ValidateAgentTraceDigest(trace); err != nil {
		return nil, err
	}
	return trace, nil
}
