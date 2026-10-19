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

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Agent tool names and error constants.
const (
	agentToolRunCommands         = "run_commands_with_operator"
	agentToolErrorExecution      = "execution.error"
	agentToolErrorSecurity       = "security.violation"
	agentToolErrorPipeline       = "pipeline.outcome"
	agentToolDefaultReadMaxLines = 400
	agentToolDefaultHistoryLimit = 20

	investigationDataHistoryTrail    = "history_trail"
	investigationDataStatus          = "investigation_status"
	investigationDataOperatorActions = "operator_actions"
	investigationOperatorActor       = "ztdo"

	agentToolArgCommand    = "command"
	agentToolArgRequest    = "request"
	agentToolArgGuidelines = "guidelines"
)

// agentToolSpec represents the contract of one tool declared to the model.
type agentToolSpec struct {
	Name        string
	Description string
	Operator    bool
	Properties  map[string]*mcp.PropertySchema
	Required    []string
}

// Arguments returns the sorted property names declared to the model.
func (s agentToolSpec) Arguments() []string {
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RequiredArguments returns the required arguments declared to the model.
func (s agentToolSpec) RequiredArguments() []string {
	return append([]string(nil), s.Required...)
}

// agentToolCatalog manages the available tool specifications and declarations.
type agentToolCatalog struct {
	specs        map[string]agentToolSpec
	declarations []*operatorv1.InferenceToolDeclaration
}

// newAgentToolCatalog initializes the catalog from production MCP native tools
// and registers the intent-only deliberation tool.
func newAgentToolCatalog() (*agentToolCatalog, error) {
	registry := mcp.NewToolRegistry()
	if err := mcp.RegisterNativeTools(registry); err != nil {
		return nil, fmt.Errorf("evaluation: agent tool catalog: register native tools: %w", err)
	}

	catalog := &agentToolCatalog{
		specs: make(map[string]agentToolSpec),
	}

	// 1. Register production native tools
	for _, tool := range registry.List() {
		name := tool.Name()
		schema := tool.InputSchema()
		props := make(map[string]*mcp.PropertySchema)
		var required []string
		if schema != nil {
			for k, v := range schema.Properties {
				props[k] = v
			}
			// For audit_receipt_list, operator_session_id is only required in Gateway mode.
			// Operator-local execution requires only investigation_id, which the executor defaults.
			if name != constants.MCPToolAuditReceiptList {
				required = append([]string(nil), schema.Required...)
			}
		}

		spec := agentToolSpec{
			Name:        name,
			Description: tool.Description(),
			Operator:    true,
			Properties:  props,
			Required:    required,
		}

		decl, err := buildInferenceToolDeclaration(name, spec.Description, props, required)
		if err != nil {
			return nil, fmt.Errorf("evaluation: agent tool catalog: build %s declaration: %w", name, err)
		}

		catalog.specs[name] = spec
		catalog.declarations = append(catalog.declarations, decl)
	}

	// 2. Register the intent-only run_commands_with_operator; the Gateway's
	// Tribunal writes the command. Model-written commands use the native
	// run_shell_command tool.
	intentProps := map[string]*mcp.PropertySchema{
		agentToolArgRequest:    {Type: "string", Description: "Plain-language description of the single command to run."},
		agentToolArgGuidelines: {Type: "string", Description: "Constraints the command must satisfy, for example no writes outside /srv."},
	}
	intentRequired := []string{agentToolArgRequest}
	intentSpec := agentToolSpec{
		Name:        agentToolRunCommands,
		Description: "Run one shell command on an operator host under governance. Describe in request the single command you need and in guidelines any constraints it must satisfy; the Tribunal writes the command and it runs like any other governed command.",
		Operator:    true,
		Properties:  intentProps,
		Required:    intentRequired,
	}
	intentDecl, err := buildInferenceToolDeclaration(agentToolRunCommands, intentSpec.Description, intentProps, intentRequired)
	if err != nil {
		return nil, fmt.Errorf("evaluation: agent tool catalog: build %s declaration: %w", agentToolRunCommands, err)
	}
	catalog.specs[agentToolRunCommands] = intentSpec
	catalog.declarations = append(catalog.declarations, intentDecl)

	// Sort declarations alphabetically by name for deterministic order
	sort.Slice(catalog.declarations, func(i, j int) bool {
		return catalog.declarations[i].GetName() < catalog.declarations[j].GetName()
	})

	return catalog, nil
}

func buildInferenceToolDeclaration(name, description string, properties map[string]*mcp.PropertySchema, required []string) (*operatorv1.InferenceToolDeclaration, error) {
	propsMap := make(map[string]any, len(properties))
	for k, v := range properties {
		p := map[string]any{"type": v.Type}
		if v.Description != "" {
			p["description"] = v.Description
		}
		if len(v.Enum) > 0 {
			p["enum"] = v.Enum
		}
		propsMap[k] = p
	}
	doc := map[string]any{
		"type":                 "object",
		"properties":           propsMap,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		reqNames := append([]string(nil), required...)
		sort.Strings(reqNames)
		doc["required"] = reqNames
	}
	encoded, err := MarshalCanonicalJSONObject(doc)
	if err != nil {
		return nil, err
	}
	return &operatorv1.InferenceToolDeclaration{
		Name:        name,
		Description: description,
		JsonSchema:  string(encoded),
	}, nil
}

// Tool returns the tool spec by name.
func (c *agentToolCatalog) Tool(name string) (agentToolSpec, bool) {
	spec, ok := c.specs[name]
	return spec, ok
}

// ToolNames returns all registered tool names in sorted order.
func (c *agentToolCatalog) ToolNames() []string {
	names := make([]string, 0, len(c.specs))
	for name := range c.specs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Scope creates an agentToolScope matching scenario expectations.
func (c *agentToolCatalog) Scope(tools ScenarioToolExpectations) (agentToolScope, error) {
	surface := stringSet(tools.AllowedTools)
	if _, ok := surface[agentToolRunCommands]; tools.TribunalCommands != ok {
		return agentToolScope{}, fmt.Errorf("evaluation: agent tool scope: tribunal commands require exactly allowed tool %s: %w", agentToolRunCommands, constants.ErrEvaluationScenarioContractInvalid)
	}
	for _, name := range tools.ForbiddenTools {
		surface[name] = struct{}{}
	}
	for name := range surface {
		if _, ok := c.specs[name]; !ok {
			return agentToolScope{}, fmt.Errorf("evaluation: agent tool scope: tool %q is not in the agent tool catalog: %w", name, constants.ErrEvaluationScenarioContractInvalid)
		}
	}
	scope := agentToolScope{
		specs:        make(map[string]agentToolSpec, len(surface)),
		declarations: make([]*operatorv1.InferenceToolDeclaration, 0, len(surface)),
	}
	for _, declaration := range c.declarations {
		name := declaration.GetName()
		if _, ok := surface[name]; !ok {
			continue
		}
		scope.specs[name] = c.specs[name]
		scope.declarations = append(scope.declarations, declaration)
	}
	return scope, nil
}

// agentToolScope represents the scoped tool definitions for one scenario.
type agentToolScope struct {
	specs        map[string]agentToolSpec
	declarations []*operatorv1.InferenceToolDeclaration
}

func (s agentToolScope) Declarations() []*operatorv1.InferenceToolDeclaration {
	return s.declarations
}

func (s agentToolScope) DeclaredNames() []string {
	names := make([]string, 0, len(s.declarations))
	for _, d := range s.declarations {
		names = append(names, d.GetName())
	}
	return names
}

func (s agentToolScope) Spec(name string) (agentToolSpec, bool) {
	spec, ok := s.specs[name]
	return spec, ok
}

// agentToolArguments is one decoded model tool call.
type agentToolArguments struct {
	tool   string
	values map[string]any
}

func (a agentToolArguments) Set(name string, value any) {
	if a.values == nil {
		a.values = make(map[string]any)
	}
	a.values[name] = value
}

func (a agentToolArguments) Values() map[string]any {
	return a.values
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

// agentToolFailure is a model-visible tool failure.
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

type agentToolErrorResult struct {
	Success   bool   `json:"success"`
	ErrorType string `json:"error_type"`
	Error     string `json:"error"`
}

func canonicalToolJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("evaluation: encode tool json: %w", err)
	}
	return canonicalizeToolJSON(string(encoded))
}

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

// parseAgentToolArguments decodes and validates tool arguments against its spec.
func parseAgentToolArguments(spec agentToolSpec, raw string) (agentToolArguments, *agentToolFailure) {
	values := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&values); err != nil {
			return agentToolArguments{}, agentToolValidationFailure(spec.Name, []string{fmt.Sprintf("arguments\n  Invalid JSON object: %v", err)})
		}
	}
	var problems []string
	requiredArgs := spec.RequiredArguments()
	for _, name := range requiredArgs {
		if value, ok := values[name]; !ok || value == nil {
			problems = append(problems, name+"\n  Field required")
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	allowedArgs := stringSet(spec.Arguments())
	for _, name := range names {
		if _, ok := allowedArgs[name]; !ok {
			problems = append(problems, name+"\n  Extra inputs are not permitted")
			continue
		}
		if values[name] == nil {
			continue
		}
		prop, ok := spec.Properties[name]
		if !ok {
			continue
		}
		if !propertyAccepts(prop, values[name]) {
			problems = append(problems, fmt.Sprintf("%s\n  Input should be a valid %s", name, prop.Type))
		}
	}
	if len(problems) > 0 {
		return agentToolArguments{}, agentToolValidationFailure(spec.Name, problems)
	}
	return agentToolArguments{tool: spec.Name, values: values}, nil
}

func propertyAccepts(prop *mcp.PropertySchema, value any) bool {
	if prop == nil {
		return true
	}
	switch prop.Type {
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	default:
		return true
	}
}
