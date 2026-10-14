// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Agent tool names the homogeneous executor declares to the model. Argument
// names and required arguments come from the generated agent tool registry;
// this file owns only their JSON types, descriptions, and governed mapping.
const (
	agentToolFileRead            = "file_read_on_operator"
	agentToolFileList            = "list_files_and_directories_with_detailed_metadata"
	agentToolGrep                = "recursive_grep_search"
	agentToolFileWrite           = "file_write_on_operator"
	agentToolFileUpdate          = "file_update_on_operator"
	agentToolFileCreate          = "file_create_on_operator"
	agentToolRunCommands         = "run_commands_with_operator"
	agentToolInvestigationCtx    = "query_investigation_context"
	agentToolCommandConstraints  = "get_command_constraints"
	agentToolErrorExecution      = "execution.error"
	agentToolErrorSecurity       = "security.violation"
	agentToolDefaultReadMaxLines = 400
	agentToolDefaultMaxEntries   = 200
	agentToolDefaultMaxMatches   = 100
	agentToolDefaultHistoryLimit = 20
)

// Investigation context data types query_investigation_context accepts.
const (
	investigationDataHistoryTrail    = "history_trail"
	investigationDataStatus          = "investigation_status"
	investigationDataOperatorActions = "operator_actions"
	investigationOperatorActor       = "ztdo"
)

type agentToolArgumentKind string

const (
	agentToolArgString     agentToolArgumentKind = "string"
	agentToolArgInteger    agentToolArgumentKind = "integer"
	agentToolArgBoolean    agentToolArgumentKind = "boolean"
	agentToolArgStringList agentToolArgumentKind = "string_list"
)

type agentToolArgument struct {
	Kind        agentToolArgumentKind
	Description string
}

// agentToolSpec is the Go-owned half of one tool declaration. Operator reports
// whether the tool executes as a governed action on the bound Data Operator.
type agentToolSpec struct {
	Name        string
	Description string
	Operator    bool
	Arguments   map[string]agentToolArgument
}

var agentToolArgumentCatalog = map[string]agentToolArgument{
	"command":               {agentToolArgString, "The single shell command to run in the working directory."},
	"content":               {agentToolArgString, "Full file content to write."},
	"create_backup":         {agentToolArgBoolean, "Keep a backup copy of the previous file content."},
	"create_if_missing":     {agentToolArgBoolean, "Create the file when it does not exist."},
	"data_type":             {agentToolArgString, "One of history_trail, investigation_status, operator_actions."},
	"end_line":              {agentToolArgInteger, "Last 1-based line to return (inclusive)."},
	"expected_output_lines": {agentToolArgInteger, "Approximate number of output lines you expect."},
	"file_path":             {agentToolArgString, "Absolute file path on the operator host."},
	"includes":              {agentToolArgStringList, "File name globs to search, for example *.log."},
	"justification":         {agentToolArgString, "Why this action is needed for the investigation."},
	"limit":                 {agentToolArgInteger, "Maximum number of entries to return."},
	"max_depth":             {agentToolArgInteger, "Maximum directory depth to list."},
	"max_entries":           {agentToolArgInteger, "Maximum number of entries to list."},
	"max_lines":             {agentToolArgInteger, "Maximum number of lines to return."},
	"max_matches":           {agentToolArgInteger, "Maximum number of matches to return."},
	"new_content":           {agentToolArgString, "Replacement text for old_content."},
	"old_content":           {agentToolArgString, "Exact existing text to replace."},
	"path":                  {agentToolArgString, "Absolute directory or file path on the operator host."},
	"pattern":               {agentToolArgString, "Regular expression to search for."},
	"request":               {agentToolArgString, "Plain-language description of the single command to run."},
	"start_line":            {agentToolArgInteger, "First 1-based line to return."},
	"target_operators":      {agentToolArgStringList, "Operator IDs to run on; use the bound operator ID."},
	"timeout_seconds":       {agentToolArgInteger, "Command timeout in seconds."},
}

var agentToolSpecs = []agentToolSpec{
	{Name: agentToolFileRead, Operator: true, Description: "Read a text file on an operator host. Use start_line/end_line or max_lines to bound large files."},
	{Name: agentToolFileList, Operator: true, Description: "List files and directories under a path on an operator host with size and type metadata."},
	{Name: agentToolGrep, Operator: true, Description: "Recursively search files under a path on an operator host for a regular expression."},
	{Name: agentToolFileWrite, Operator: true, Description: "Overwrite a file on an operator host with new content."},
	{Name: agentToolFileUpdate, Operator: true, Description: "Replace an exact text span in an existing file on an operator host."},
	{Name: agentToolFileCreate, Operator: true, Description: "Create a new file on an operator host."},
	{Name: agentToolRunCommands, Operator: true, Description: "Run one shell command on an operator host under governance. Pass the single command in command; use request for its purpose."},
	{Name: agentToolInvestigationCtx, Description: "Read the current investigation's history trail, status, or prior operator actions."},
	{Name: agentToolCommandConstraints, Description: "Describe the constraints governed commands must satisfy before you run one."},
}

// agentToolCatalog is the declared tool surface for one agent turn: the
// generated registry's argument contract joined with Go-owned types.
type agentToolCatalog struct {
	registry     *AgentToolRegistry
	specs        map[string]agentToolSpec
	declarations []*operatorv1.InferenceToolDeclaration
}

// newAgentToolCatalog joins the generated registry with the Go-owned specs and
// fails closed when the registry names an argument this file does not type, so
// registry drift is caught before any model call.
func newAgentToolCatalog(registry *AgentToolRegistry) (*agentToolCatalog, error) {
	if registry == nil {
		return nil, fmt.Errorf("evaluation: agent tool catalog: registry: %w", constants.ErrMissingRequiredField)
	}
	catalog := &agentToolCatalog{registry: registry, specs: make(map[string]agentToolSpec, len(agentToolSpecs))}
	for _, spec := range agentToolSpecs {
		schema, ok := registry.Tool(spec.Name)
		if !ok {
			return nil, fmt.Errorf("evaluation: agent tool catalog: %w: tool %s is not in the generated registry", constants.ErrMissingRequiredField, spec.Name)
		}
		spec.Arguments = make(map[string]agentToolArgument, len(schema.Arguments))
		properties := make(map[string]any, len(schema.Arguments))
		for _, name := range schema.Arguments {
			argument, ok := agentToolArgumentCatalog[name]
			if !ok {
				return nil, fmt.Errorf("evaluation: agent tool catalog: %w: argument %s.%s has no declared type", constants.ErrMissingRequiredField, spec.Name, name)
			}
			spec.Arguments[name] = argument
			properties[name] = argument.jsonSchema()
		}
		required := make([]any, 0, len(schema.RequiredArguments))
		for _, name := range schema.RequiredArguments {
			required = append(required, name)
		}
		document := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			document["required"] = required
		}
		encoded, err := MarshalCanonicalJSONObject(document)
		if err != nil {
			return nil, fmt.Errorf("evaluation: agent tool catalog: encode %s schema: %w", spec.Name, err)
		}
		catalog.specs[spec.Name] = spec
		catalog.declarations = append(catalog.declarations, &operatorv1.InferenceToolDeclaration{Name: spec.Name, Description: spec.Description, JsonSchema: string(encoded)})
	}
	return catalog, nil
}

func (a agentToolArgument) jsonSchema() map[string]any {
	switch a.Kind {
	case agentToolArgInteger:
		return map[string]any{"type": "integer", "description": a.Description}
	case agentToolArgBoolean:
		return map[string]any{"type": "boolean", "description": a.Description}
	case agentToolArgStringList:
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": a.Description}
	default:
		return map[string]any{"type": "string", "description": a.Description}
	}
}

// Declarations returns the tool declarations sent on every agent turn.
func (c *agentToolCatalog) Declarations() []*operatorv1.InferenceToolDeclaration {
	return c.declarations
}

// DeclaredNames returns the declared tool names in declaration order.
func (c *agentToolCatalog) DeclaredNames() []string {
	names := make([]string, 0, len(c.declarations))
	for _, declaration := range c.declarations {
		names = append(names, declaration.GetName())
	}
	return names
}

// Spec returns the named declared tool.
func (c *agentToolCatalog) Spec(name string) (agentToolSpec, bool) {
	spec, ok := c.specs[name]
	return spec, ok
}

// agentToolArguments is one decoded model tool call. Model output is untrusted
// JSON, so it is decoded generically and read through typed accessors that
// report a type mismatch as a model-visible validation failure.
type agentToolArguments struct {
	tool   string
	values map[string]any
}

// parseAgentToolArguments decodes and checks one tool call against its declared
// spec. The returned *agentToolFailure is the model-visible answer when the
// call is malformed; it is not a harness error.
func parseAgentToolArguments(spec agentToolSpec, required []string, raw string) (agentToolArguments, *agentToolFailure) {
	values := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&values); err != nil {
			return agentToolArguments{}, agentToolValidationFailure(spec.Name, []string{fmt.Sprintf("arguments\n  Invalid JSON object: %v", err)})
		}
	}
	var problems []string
	for _, name := range required {
		if value, ok := values[name]; !ok || value == nil {
			problems = append(problems, name+"\n  Field required")
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		argument, ok := spec.Arguments[name]
		if !ok {
			problems = append(problems, name+"\n  Extra inputs are not permitted")
			continue
		}
		if values[name] == nil {
			continue
		}
		if !argument.accepts(values[name]) {
			problems = append(problems, fmt.Sprintf("%s\n  Input should be a valid %s", name, argument.Kind))
		}
	}
	if len(problems) > 0 {
		return agentToolArguments{}, agentToolValidationFailure(spec.Name, problems)
	}
	return agentToolArguments{tool: spec.Name, values: values}, nil
}

func (a agentToolArgument) accepts(value any) bool {
	switch a.Kind {
	case agentToolArgInteger:
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case agentToolArgBoolean:
		_, ok := value.(bool)
		return ok
	case agentToolArgStringList:
		items, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	default:
		_, ok := value.(string)
		return ok
	}
}

func (a agentToolArguments) String(name string) string {
	value, _ := a.values[name].(string)
	return value
}

func (a agentToolArguments) Int(name string, fallback int) int {
	number, ok := a.values[name].(json.Number)
	if !ok {
		return fallback
	}
	value, err := number.Int64()
	if err != nil {
		return fallback
	}
	return int(value)
}

func (a agentToolArguments) Bool(name string) bool {
	value, _ := a.values[name].(bool)
	return value
}

func (a agentToolArguments) Strings(name string) []string {
	items, _ := a.values[name].([]any)
	values := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			values = append(values, value)
		}
	}
	return values
}

// agentToolFailure is a model-visible tool failure. ErrorType uses the tool
// layer's error vocabulary, so the registry's policy deny set classifies it.
type agentToolFailure struct {
	ErrorType string
	Error     string
}

func agentToolValidationFailure(tool string, problems []string) *agentToolFailure {
	noun := "validation error"
	if len(problems) != 1 {
		noun = "validation errors"
	}
	return &agentToolFailure{
		ErrorType: agentToolErrorExecution,
		Error:     fmt.Sprintf("ZTD-1607: Tool execution failed for %s: %d %s\n%s", tool, len(problems), noun, strings.Join(problems, "\n")),
	}
}

// agentToolErrorResult is the model-visible JSON for a failed tool call.
type agentToolErrorResult struct {
	Success   bool   `json:"success"`
	ErrorType string `json:"error_type"`
	Error     string `json:"error"`
}

type agentFileReadResult struct {
	Success    bool   `json:"success"`
	FilePath   string `json:"file_path"`
	Content    string `json:"content"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
}

type agentFileListEntry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type agentFileListResult struct {
	Success    bool                 `json:"success"`
	Path       string               `json:"path"`
	Entries    []agentFileListEntry `json:"entries"`
	TotalCount int32                `json:"total_count"`
	Truncated  bool                 `json:"truncated"`
}

type agentGrepMatch struct {
	Path       string `json:"path"`
	LineNumber int32  `json:"line_number"`
	Content    string `json:"content"`
}

type agentGrepResult struct {
	Success      bool             `json:"success"`
	Path         string           `json:"path"`
	Pattern      string           `json:"pattern"`
	Matches      []agentGrepMatch `json:"matches"`
	TotalMatches int32            `json:"total_matches"`
	Truncated    bool             `json:"truncated"`
}

type agentFileEditResult struct {
	Success      bool   `json:"success"`
	FilePath     string `json:"file_path"`
	Operation    string `json:"operation"`
	BytesWritten int64  `json:"bytes_written"`
	LinesChanged int32  `json:"lines_changed"`
}

type agentCommandResult struct {
	Success    bool   `json:"success"`
	Command    string `json:"command"`
	ReturnCode int32  `json:"return_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
}

type agentCommandConstraintsResult struct {
	Success           bool     `json:"success"`
	OperatorID        string   `json:"operator_id"`
	WorkingDirectory  string   `json:"working_directory"`
	ForbiddenPatterns []string `json:"forbidden_patterns"`
	Rules             []string `json:"rules"`
}

type agentInvestigationEvent struct {
	EventType string `json:"event_type"`
	Actor     string `json:"actor"`
	Summary   string `json:"summary"`
	ToolName  string `json:"tool_name,omitempty"`
	Command   string `json:"command,omitempty"`
	ErrorType string `json:"error_type,omitempty"`
	Error     string `json:"error,omitempty"`
}

type agentInvestigationContextResult struct {
	Success         bool                      `json:"success"`
	DataType        string                    `json:"data_type"`
	CaseTitle       string                    `json:"case_title,omitempty"`
	CaseDescription string                    `json:"case_description,omitempty"`
	Summary         string                    `json:"summary,omitempty"`
	Events          []agentInvestigationEvent `json:"events,omitempty"`
}

// canonicalToolJSON renders a typed tool value as the canonical JSON object the
// Inference Operator requires for tool arguments and results.
func canonicalToolJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("evaluation: encode tool json: %w", err)
	}
	return canonicalizeToolJSON(string(encoded))
}

// canonicalizeToolJSON re-encodes a JSON object (for example model-emitted tool
// arguments) in canonical form, preserving number literals.
func canonicalizeToolJSON(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("evaluation: decode tool json: %w", err)
	}
	if value == nil {
		value = map[string]any{}
	}
	canonical, err := MarshalCanonicalJSONObject(value)
	if err != nil {
		return "", fmt.Errorf("evaluation: canonicalize tool json: %w", err)
	}
	return string(canonical), nil
}

func agentToolFailureJSON(failure agentToolFailure) (string, error) {
	return canonicalToolJSON(agentToolErrorResult{ErrorType: failure.ErrorType, Error: failure.Error})
}

// sliceFileLines applies the file_read_on_operator line window to content.
func sliceFileLines(content string, args agentToolArguments) agentFileReadResult {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	start := args.Int("start_line", 1)
	if start < 1 {
		start = 1
	}
	end := args.Int("end_line", total)
	if end > total {
		end = total
	}
	maxLines := args.Int("max_lines", agentToolDefaultReadMaxLines)
	if maxLines < 1 {
		maxLines = agentToolDefaultReadMaxLines
	}
	truncated := false
	if end-start+1 > maxLines {
		end = start + maxLines - 1
		truncated = true
	}
	result := agentFileReadResult{Success: true, StartLine: start, EndLine: end, TotalLines: total, Truncated: truncated || end < total}
	if start <= end && start <= total {
		result.Content = strings.Join(lines[start-1:end], "")
	} else {
		result.EndLine = start - 1
	}
	return result
}

// commandConstraints answers get_command_constraints from the governed
// contract itself: the forbidden patterns are read from the CommandRequested
// field options that L1 doctrine enforces, never restated by hand.
func commandConstraints(operatorID, workingDirectory string) agentCommandConstraintsResult {
	result := agentCommandConstraintsResult{
		Success:          true,
		OperatorID:       operatorID,
		WorkingDirectory: workingDirectory,
		Rules: []string{
			"Each command is one governed EXECUTE_BASH action on the operator and is screened by L1 doctrine before execution.",
			"Commands matching a forbidden pattern or a doctrine threat heuristic are denied and never execute.",
			"Prefer read-only commands; file changes should use the file tools.",
			"Relative paths resolve against the operator working directory.",
		},
	}
	field := (&operatorv1.CommandRequested{}).ProtoReflect().Descriptor().Fields().ByName("command")
	if field == nil || field.Options() == nil || !proto.HasExtension(field.Options(), commonv1.E_ForbiddenPatterns) {
		return result
	}
	patterns, _ := proto.GetExtension(field.Options(), commonv1.E_ForbiddenPatterns).(string)
	for _, pattern := range strings.Split(patterns, ",") {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			result.ForbiddenPatterns = append(result.ForbiddenPatterns, pattern)
		}
	}
	return result
}

// investigationContext answers query_investigation_context from the frozen
// investigation seed the scored turn runs in.
func investigationContext(seed InvestigationSeed, args agentToolArguments) (agentInvestigationContextResult, *agentToolFailure) {
	dataType := args.String("data_type")
	limit := args.Int("limit", agentToolDefaultHistoryLimit)
	if limit < 1 {
		limit = agentToolDefaultHistoryLimit
	}
	result := agentInvestigationContextResult{Success: true, DataType: dataType}
	switch dataType {
	case investigationDataStatus:
		result.CaseTitle = seed.CaseTitle
		result.CaseDescription = seed.CaseDescription
		if seed.CaseMemory != nil {
			result.Summary = seed.CaseMemory.InvestigationSummary
		}
	case investigationDataHistoryTrail, investigationDataOperatorActions:
		for _, event := range seed.HistoryEvents {
			if dataType == investigationDataOperatorActions && event.Actor != investigationOperatorActor {
				continue
			}
			result.Events = append(result.Events, agentInvestigationEvent{
				EventType: event.EventType, Actor: event.Actor, Summary: event.Summary, ToolName: event.ToolName,
				Command: event.Command, ErrorType: event.ErrorType, Error: event.Error,
			})
		}
		if len(result.Events) > limit {
			result.Events = result.Events[len(result.Events)-limit:]
		}
	default:
		return agentInvestigationContextResult{}, agentToolValidationFailure(agentToolInvestigationCtx, []string{
			fmt.Sprintf("data_type\n  Input should be '%s', '%s' or '%s'", investigationDataHistoryTrail, investigationDataStatus, investigationDataOperatorActions),
		})
	}
	return result, nil
}
