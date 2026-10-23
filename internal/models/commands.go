// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type ExecutionRequestPayload struct {
	ExecutionID       string            `json:"execution_id"`
	CaseID            string            `json:"case_id"`
	TaskID            *string           `json:"task_id,omitempty"`
	InvestigationID   string            `json:"investigation_id"`
	Command           string            `json:"command"`
	Args              []string          `json:"args,omitempty"`
	TimeoutSeconds    int               `json:"timeout_seconds"`
	RequestedBy       string            `json:"requested_by"`
	Environment       map[string]string `json:"environment,omitempty"`
	WorkingDirectory  *string           `json:"working_directory,omitempty"`
	NetworkTopologyID *string           `json:"network_topology_id,omitempty"`
	TargetAddress     *string           `json:"target_address,omitempty"`
	Priority          int               `json:"priority,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	Justification     string            `json:"justification,omitempty"`
	Intent            string            `json:"intent,omitempty"`
}

// RunShellCommandRequest is the typed argument contract of the native
// run_shell_command MCP tool. L1 doctrine decodes it to screen the command
// line the tool executes.
type RunShellCommandRequest struct {
	Command    string   `json:"command"`
	Args       []string `json:"args,omitempty"`
	Timeout    int      `json:"timeout,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
	Hostnames  []string `json:"hostnames,omitempty"`
}

// ValidExecutableName reports whether command names one executable that
// runs directly without a shell: non-empty and free of whitespace.
func ValidExecutableName(command string) bool {
	return command != "" && !strings.ContainsFunc(command, unicode.IsSpace)
}

// CommandLine returns the command and its arguments as the single
// space-joined line that safety and doctrine checks screen.
func (r RunShellCommandRequest) CommandLine() string {
	return strings.Join(append([]string{r.Command}, r.Args...), " ")
}

// CanonicalJSON returns the stable JSON representation of this request.
// Hostnames are retained when supplied; Tribunal authoring rejects them
// because the shared typed protocol contract intentionally excludes hosts.
func (r RunShellCommandRequest) CanonicalJSON() string {
	// This fixed struct contains only JSON-safe scalar and slice types, so
	// json.Marshal cannot fail. Re-marshalling through a map sorts keys, the
	// canonical object form the inference and evaluation verifiers require.
	b, _ := json.Marshal(r)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	canonical, _ := json.Marshal(fields)
	return string(canonical)
}

// DoctrineCheckPayload returns the run_shell_command MCP_CALL payload that L1
// doctrine screens for this request, the same shape live dispatch carries.
func (r RunShellCommandRequest) DoctrineCheckPayload() *operatorv1.McpCallRequested {
	return &operatorv1.McpCallRequested{ToolName: constants.MCPToolRunShellCommand, ArgumentsJson: r.CanonicalJSON()}
}

// Proto converts the native request to the shared execution argument type.
// Hostnames are intentionally not represented in that shared contract.
func (r RunShellCommandRequest) Proto() *operatorv1.RunShellCommandArguments {
	return &operatorv1.RunShellCommandArguments{
		Command: r.Command, Args: append([]string(nil), r.Args...),
		Timeout: int32(r.Timeout), WorkingDir: r.WorkingDir,
	}
}

// RunShellCommandRequestFromProto converts shared typed arguments to the
// native tool request.
func RunShellCommandRequestFromProto(p *operatorv1.RunShellCommandArguments) RunShellCommandRequest {
	if p == nil {
		return RunShellCommandRequest{}
	}
	return RunShellCommandRequest{
		Command: p.Command, Args: append([]string(nil), p.Args...),
		Timeout: int(p.Timeout), WorkingDir: p.WorkingDir,
	}
}

// ExecutionResult is the domain type for command execution results.
// It uses value types (int, string, time.Time) with zero values representing "not set".
// This is the type returned by ExecutionService.ExecuteCommand and used internally.
type ExecutionResult struct {
	ExecutionID       string
	CaseID            string
	TaskID            string
	InvestigationID   string
	Command           string
	Args              []string
	Status            operatorv1.ExecutionStatus
	DurationSeconds   float64
	OperatorID        string
	OperatorSessionID string
	Stdout            string
	Stderr            string
	StdoutSize        int
	StderrSize        int
	StdoutHash        string
	StderrHash        string
	StoredLocally     bool
	ReturnCode        int
	ErrorMessage      string
	ErrorType         string
	StartTime         time.Time
	EndTime           time.Time
	TerminalOutput    *TerminalOutput
	SystemInfo        *ExecutionSystemInfo
	EnvironmentInfo   *ExecutionEnvironmentInfo
}

// ExecutionResultsPayload is the wire type for JSON/protobuf serialization.
// It uses pointer types (*int, *string, *time.Time) to represent optional fields.
// This is used at the HTTP/pubsub boundary only.
type ExecutionResultsPayload struct {
	PayloadType       string                     `json:"payload_type"`
	ExecutionID       string                     `json:"execution_id"`
	CaseID            string                     `json:"case_id"`
	TaskID            *string                    `json:"task_id,omitempty"`
	InvestigationID   string                     `json:"investigation_id"`
	Command           string                     `json:"command"`
	Args              []string                   `json:"args,omitempty"`
	Status            operatorv1.ExecutionStatus `json:"status"`
	DurationSeconds   float64                    `json:"duration_seconds"`
	OperatorID        string                     `json:"operator_id"`
	OperatorSessionID string                     `json:"operator_session_id"`
	Stdout            string                     `json:"stdout,omitempty"`
	Stderr            string                     `json:"stderr,omitempty"`
	StdoutSize        int                        `json:"stdout_size"`
	StderrSize        int                        `json:"stderr_size"`
	StdoutHash        string                     `json:"stdout_hash,omitempty"`
	StderrHash        string                     `json:"stderr_hash,omitempty"`
	StoredLocally     bool                       `json:"stored_locally"`
	ReturnCode        *int                       `json:"return_code,omitempty"`
	ErrorMessage      *string                    `json:"error_message,omitempty"`
	ErrorType         *string                    `json:"error_type,omitempty"`
	StartTime         *time.Time                 `json:"start_time,omitempty"`
	EndTime           *time.Time                 `json:"end_time,omitempty"`
	TerminalOutput    *TerminalOutput            `json:"terminal_output,omitempty"`
	SystemInfo        *ExecutionSystemInfo       `json:"system_info,omitempty"`
	EnvironmentInfo   *ExecutionEnvironmentInfo  `json:"environment_info,omitempty"`
}

// PayloadFromDomainResult converts a domain ExecutionResult to a wire ExecutionResultsPayload.
// This is used when publishing results to the wire.
func PayloadFromDomainResult(r *ExecutionResult) *ExecutionResultsPayload {
	if r == nil {
		return nil
	}
	payload := &ExecutionResultsPayload{
		PayloadType:       "execution_result",
		ExecutionID:       r.ExecutionID,
		CaseID:            r.CaseID,
		InvestigationID:   r.InvestigationID,
		Command:           r.Command,
		Args:              r.Args,
		Status:            r.Status,
		DurationSeconds:   r.DurationSeconds,
		OperatorID:        r.OperatorID,
		OperatorSessionID: r.OperatorSessionID,
		Stdout:            r.Stdout,
		Stderr:            r.Stderr,
		StdoutSize:        r.StdoutSize,
		StderrSize:        r.StderrSize,
		StdoutHash:        r.StdoutHash,
		StderrHash:        r.StderrHash,
		StoredLocally:     r.StoredLocally,
		TerminalOutput:    r.TerminalOutput,
		SystemInfo:        r.SystemInfo,
		EnvironmentInfo:   r.EnvironmentInfo,
	}
	if r.TaskID != "" {
		payload.TaskID = &r.TaskID
	}
	if r.ReturnCode != 0 {
		payload.ReturnCode = &r.ReturnCode
	}
	if r.ErrorMessage != "" {
		payload.ErrorMessage = &r.ErrorMessage
	}
	if r.ErrorType != "" {
		payload.ErrorType = &r.ErrorType
	}
	if !r.StartTime.IsZero() {
		payload.StartTime = &r.StartTime
	}
	if !r.EndTime.IsZero() {
		payload.EndTime = &r.EndTime
	}
	return payload
}
