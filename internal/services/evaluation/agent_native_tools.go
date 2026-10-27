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
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Agent tool names and error constants.
const (
	agentToolErrorExecution      = "execution.error"
	agentToolErrorSecurity       = "security.violation"
	agentToolErrorPipeline       = "pipeline.outcome"
	agentToolDefaultReadMaxLines = 400
	agentToolDefaultHistoryLimit = 20

	investigationDataHistoryTrail    = "history_trail"
	investigationDataStatus          = "investigation_status"
	investigationDataOperatorActions = "operator_actions"
	investigationOperatorActor       = "ztdo"

	agentToolArgCommand = "command"
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

// newAgentToolCatalog initializes the catalog from production MCP native tools.
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
			// For event_receipt_list, operator_session_id is only required in Gateway mode.
			// Operator-local execution requires only investigation_id, which the executor defaults.
			if name != constants.MCPToolEventReceiptList {
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

	// Sort declarations alphabetically by name for deterministic order
	sort.Slice(catalog.declarations, func(i, j int) bool {
		return catalog.declarations[i].GetName() < catalog.declarations[j].GetName()
	})

	return catalog, nil
}

// agentToolDeclarationProperty is one property of a tool's declared JSON schema.
// Fields are in JSON key order so the encoded schema is canonical.
type agentToolDeclarationProperty struct {
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Type        string   `json:"type"`
}

// agentToolDeclarationSchema is the JSON schema a model receives for one tool.
// Fields are in JSON key order so the encoded schema is canonical.
type agentToolDeclarationSchema struct {
	AdditionalProperties bool                                    `json:"additionalProperties"`
	Properties           map[string]agentToolDeclarationProperty `json:"properties"`
	Required             []string                                `json:"required,omitempty"`
	Type                 string                                  `json:"type"`
}

func buildInferenceToolDeclaration(name, description string, properties map[string]*mcp.PropertySchema, required []string) (*operatorv1.InferenceToolDeclaration, error) {
	propsMap := make(map[string]agentToolDeclarationProperty, len(properties))
	for k, v := range properties {
		propsMap[k] = agentToolDeclarationProperty{Description: v.Description, Enum: v.Enum, Type: v.Type}
	}
	doc := agentToolDeclarationSchema{
		AdditionalProperties: false,
		Properties:           propsMap,
		Type:                 "object",
	}
	if len(required) > 0 {
		reqNames := append([]string(nil), required...)
		sort.Strings(reqNames)
		doc.Required = reqNames
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
	if tools.TribunalCommands {
		if _, ok := surface[constants.MCPToolRunShellCommand]; !ok {
			return agentToolScope{}, fmt.Errorf("evaluation: agent tool scope: tribunal commands require allowed tool %s: %w", constants.MCPToolRunShellCommand, constants.ErrEvaluationScenarioContractInvalid)
		}
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

// agentToolArguments is one decoded model tool call. contract is the concrete
// MCP request type selected by the tool name and validates the decoded shape;
// rawJSON preserves the canonical model input for dispatch without reshaping
// optional fields.
type agentToolArguments struct {
	tool     string
	contract agentToolContract
	rawJSON  string
}

type agentToolContract interface {
	agentToolContract()
}

type typedAgentToolContract[T any] struct {
	value T
}

func (typedAgentToolContract[T]) agentToolContract() {}

func decodeTypedAgentToolContract[T any](raw string) (agentToolContract, error) {
	var value T
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	return typedAgentToolContract[T]{value: value}, nil
}

func (a *agentToolArguments) EventReceiptList() (mcp.EventReceiptListRequest, bool) {
	contract, ok := a.contract.(typedAgentToolContract[mcp.EventReceiptListRequest])
	return contract.value, ok
}

func (a *agentToolArguments) SetEventReceiptListDefaults(investigationID string) {
	contract, ok := a.contract.(typedAgentToolContract[mcp.EventReceiptListRequest])
	if !ok {
		return
	}
	if contract.value.InvestigationID == "" {
		contract.value.InvestigationID = investigationID
	}
	contract.value.ExcludeEventReceiptReads = true
	if contract.value.Limit <= 0 {
		contract.value.Limit = agentToolDefaultHistoryLimit
	}
	a.contract = contract
}

func (a *agentToolArguments) CanonicalJSON() (string, error) {
	if a.tool != constants.MCPToolEventReceiptList {
		return a.rawJSON, nil
	}
	contract, ok := a.contract.(typedAgentToolContract[mcp.EventReceiptListRequest])
	if !ok {
		return a.rawJSON, nil
	}
	// event_receipt_list is the one native request whose evaluation dispatch
	// injects defaults. Merge those typed values into the original JSON object
	// so absent and explicit null fields otherwise retain their model shape.
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(a.rawJSON), &values); err != nil {
		return "", fmt.Errorf("evaluation: decode %s arguments: %w", a.tool, err)
	}
	if values == nil {
		values = make(map[string]json.RawMessage)
	}
	if contract.value.InvestigationID != "" {
		value, err := json.Marshal(contract.value.InvestigationID)
		if err != nil {
			return "", fmt.Errorf("evaluation: encode %s investigation ID: %w", a.tool, err)
		}
		values["investigation_id"] = value
	}
	values["exclude_event_receipt_reads"] = json.RawMessage("true")
	if contract.value.Limit > 0 {
		value, err := json.Marshal(contract.value.Limit)
		if err != nil {
			return "", fmt.Errorf("evaluation: encode %s limit: %w", a.tool, err)
		}
		values["limit"] = value
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("evaluation: encode %s arguments: %w", a.tool, err)
	}
	return canonicalizeToolJSON(string(encoded))
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
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("%w: multiple JSON values", constants.ErrInvalidJSONBody)
		}
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
	// The declaration is derived from the native tool schema, so inspect its
	// dynamic property set as RawMessage values before decoding the concrete
	// request contract below.
	values := map[string]json.RawMessage{}
	if strings.TrimSpace(raw) != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&values); err != nil {
			return agentToolArguments{}, agentToolValidationFailure(spec.Name, []string{fmt.Sprintf("arguments\n  Invalid JSON object: %v", err)})
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				err = fmt.Errorf("%w: multiple JSON values", constants.ErrInvalidJSONBody)
			}
			return agentToolArguments{}, agentToolValidationFailure(spec.Name, []string{fmt.Sprintf("arguments\n  Invalid JSON object: %v", err)})
		}
		if values == nil {
			return agentToolArguments{}, agentToolValidationFailure(spec.Name, []string{"arguments\n  Invalid JSON object: null"})
		}
	}
	var problems []string
	requiredArgs := spec.RequiredArguments()
	for _, name := range requiredArgs {
		if value, ok := values[name]; !ok || isJSONNull(value) {
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
		if isJSONNull(values[name]) {
			continue
		}
		prop, ok := spec.Properties[name]
		if !ok {
			continue
		}
		if !propertyAcceptsRaw(prop, values[name]) {
			expected := prop.Type
			if prop.Type == "array" {
				expected = "array of strings"
			}
			problems = append(problems, fmt.Sprintf("%s\n  Input should be a valid %s", name, expected))
			continue
		}
		if len(prop.Enum) > 0 {
			var value string
			if err := json.Unmarshal(values[name], &value); err == nil && !slices.Contains(prop.Enum, value) {
				problems = append(problems, fmt.Sprintf("%s\n  Input should be one of %s", name, strings.Join(prop.Enum, ", ")))
			}
		}
	}
	if len(problems) > 0 {
		return agentToolArguments{}, agentToolValidationFailure(spec.Name, problems)
	}
	canonical, err := canonicalizeToolJSON(raw)
	if err != nil {
		return agentToolArguments{}, agentToolValidationFailure(spec.Name, []string{fmt.Sprintf("arguments\n  Invalid JSON object: %v", err)})
	}
	contract, err := decodeAgentToolContract(spec.Name, raw)
	if err != nil {
		return agentToolArguments{}, agentToolValidationFailure(spec.Name, []string{fmt.Sprintf("arguments\n  Input does not match the %s request contract: %v", spec.Name, err)})
	}
	return agentToolArguments{tool: spec.Name, contract: contract, rawJSON: canonical}, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func propertyAcceptsRaw(prop *mcp.PropertySchema, raw json.RawMessage) bool {
	if prop == nil {
		return true
	}
	switch prop.Type {
	case "string":
		var value string
		return json.Unmarshal(raw, &value) == nil
	case "integer":
		var value int64
		return json.Unmarshal(raw, &value) == nil
	case "boolean":
		var value bool
		return json.Unmarshal(raw, &value) == nil
	case "array":
		var value []json.RawMessage
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return false
		}
		for _, item := range value {
			if isJSONNull(item) {
				return false
			}
			var stringItem string
			if err := json.Unmarshal(item, &stringItem); err != nil {
				return false
			}
		}
		return true
	default:
		return true
	}
}

// decodeAgentToolContract maps every registered native MCP tool to its
// concrete request type. Schema validation above rejects undeclared fields;
// decoding here validates nested fields, such as string-array items.
func decodeAgentToolContract(tool, raw string) (agentToolContract, error) {
	switch tool {
	case constants.MCPToolDBDiscoverTopology:
		return decodeTypedAgentToolContract[mcp.DBDiscoverTopologyRequest](raw)
	case constants.MCPToolDBQueryValidate:
		return decodeTypedAgentToolContract[mcp.DBQueryValidateRequest](raw)
	case constants.MCPToolDBIsolatedRead:
		return decodeTypedAgentToolContract[mcp.DBIsolatedReadRequest](raw)
	case constants.MCPToolDBIndexTriage:
		return decodeTypedAgentToolContract[mcp.DBIndexTriageRequest](raw)
	case constants.MCPToolSysOOMDetect:
		return decodeTypedAgentToolContract[mcp.SysOOMDetectRequest](raw)
	case constants.MCPToolConfigDiffMask:
		return decodeTypedAgentToolContract[mcp.ConfigDiffMaskRequest](raw)
	case constants.MCPToolProcMetricTop:
		return decodeTypedAgentToolContract[mcp.ProcMetricTopRequest](raw)
	case constants.MCPToolFsDiskProfile:
		return decodeTypedAgentToolContract[mcp.FSDiskProfileRequest](raw)
	case constants.MCPToolProcSignalSafe:
		return decodeTypedAgentToolContract[mcp.ProcSignalSafeRequest](raw)
	case constants.MCPToolNetSocketAudit:
		return decodeTypedAgentToolContract[mcp.NetSocketAuditRequest](raw)
	case constants.MCPToolNetEndpointPing:
		return decodeTypedAgentToolContract[mcp.NetEndpointPingRequest](raw)
	case constants.MCPToolNetHTTPProbe:
		return decodeTypedAgentToolContract[mcp.NetHTTPProbeRequest](raw)
	case constants.MCPToolSysInfo:
		return decodeTypedAgentToolContract[mcp.SysInfoRequest](raw)
	case constants.MCPToolNetDNSResolve:
		return decodeTypedAgentToolContract[mcp.NetDNSResolveRequest](raw)
	case constants.MCPToolTLSCertInspect:
		return decodeTypedAgentToolContract[mcp.TLSCertInspectRequest](raw)
	case constants.MCPToolSysEnvVars:
		return decodeTypedAgentToolContract[mcp.SysEnvVarsRequest](raw)
	case constants.MCPToolFsFileChecksum:
		return decodeTypedAgentToolContract[mcp.FSFileChecksumRequest](raw)
	case constants.MCPToolSysServiceStatus:
		return decodeTypedAgentToolContract[mcp.SysServiceStatusRequest](raw)
	case constants.MCPToolSysContainerStatus:
		return decodeTypedAgentToolContract[mcp.SysContainerStatusRequest](raw)
	case constants.MCPToolFsDiskUsage:
		return decodeTypedAgentToolContract[mcp.FSDiskUsageRequest](raw)
	case constants.MCPToolSysTimeClock:
		return decodeTypedAgentToolContract[mcp.SysTimeClockRequest](raw)
	case constants.MCPToolProcTree:
		return decodeTypedAgentToolContract[mcp.ProcTreeRequest](raw)
	case constants.MCPToolGitOps:
		return decodeTypedAgentToolContract[mcp.GitOpsRequest](raw)
	case constants.MCPToolCloudMetadata:
		return decodeTypedAgentToolContract[mcp.CloudMetadataRequest](raw)
	case constants.MCPToolK8sInspect:
		return decodeTypedAgentToolContract[mcp.K8sInspectRequest](raw)
	case constants.MCPToolRunShellCommand:
		return decodeTypedAgentToolContract[models.RunShellCommandRequest](raw)
	case constants.MCPToolNetSSHKnownHosts:
		return decodeTypedAgentToolContract[mcp.NetSSHKnownHostsRequest](raw)
	case constants.MCPToolOperatorDeploy:
		return decodeTypedAgentToolContract[mcp.OperatorDeployRequest](raw)
	case constants.MCPToolReadFile:
		return decodeTypedAgentToolContract[mcp.FileReadRequest](raw)
	case constants.MCPToolSearchFile:
		return decodeTypedAgentToolContract[mcp.SearchFileRequest](raw)
	case constants.MCPToolSearchDirectory:
		return decodeTypedAgentToolContract[mcp.SearchDirectoryRequest](raw)
	case constants.MCPToolEventReceiptList:
		return decodeTypedAgentToolContract[mcp.EventReceiptListRequest](raw)
	case constants.MCPToolEventReceiptGet:
		return decodeTypedAgentToolContract[mcp.EventReceiptGetRequest](raw)
	default:
		return nil, fmt.Errorf("unknown native tool %q", tool)
	}
}

// agentToolRequest is one model tool request decoded against the contract
// declared to the model. decodeAgentToolRequest is its only constructor: the
// executor records its ArgumentsJSON and the evidence verifier recomputes it
// from the signed model response, so the recorded form has one owner.
type agentToolRequest struct {
	// ArgumentsJSON is the recorded form: the typed canonical request of a
	// run_shell_command call that satisfies its contract, otherwise the
	// canonical JSON the model sent, or "{}" when that is not a JSON object.
	ArgumentsJSON string
	Arguments     agentToolArguments
	// Shell is the typed request of a contract-valid run_shell_command call.
	Shell *models.RunShellCommandRequest
	// Failure is the model-visible rejection of a request that does not
	// satisfy its contract. It is nil for an undeclared tool, which the
	// caller rejects with the declared tool names.
	Failure *agentToolFailure
}

func decodeAgentToolRequest(spec agentToolSpec, declared bool, raw string) agentToolRequest {
	request := agentToolRequest{ArgumentsJSON: "{}"}
	if canonical, err := canonicalizeToolJSON(raw); err == nil {
		request.ArgumentsJSON = canonical
	}
	if !declared {
		return request
	}
	request.Arguments, request.Failure = parseAgentToolArguments(spec, raw)
	if request.Failure != nil || spec.Name != constants.MCPToolRunShellCommand {
		return request
	}
	shell, err := decodeRunShellCommandRequest(raw)
	if err != nil {
		request.Failure = &agentToolFailure{ErrorType: agentToolErrorExecution, Error: fmt.Sprintf("invalid %s request: %v", spec.Name, err)}
		return request
	}
	request.Shell = &shell
	request.ArgumentsJSON = shell.CanonicalJSON()
	return request
}

// decodeRunShellCommandRequest decodes the raw arguments that parseAgentToolArguments
// already validated. Empty arguments yield the zero request.
func decodeRunShellCommandRequest(raw string) (models.RunShellCommandRequest, error) {
	var request models.RunShellCommandRequest
	if strings.TrimSpace(raw) == "" {
		return request, nil
	}
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return request, fmt.Errorf("evaluation: decode %s arguments: %w", constants.MCPToolRunShellCommand, err)
	}
	return request, nil
}

// agentToolSpecFromDeclaration rebuilds a tool's argument contract from the
// declaration the model received, so verification judges each request by the
// contract the executor applied when the trace was sealed.
func agentToolSpecFromDeclaration(tool AgentTraceTool) (agentToolSpec, error) {
	var schema mcp.InputSchema
	if err := json.Unmarshal([]byte(tool.JSONSchema), &schema); err != nil {
		return agentToolSpec{}, fmt.Errorf("evaluation: decode %s declaration: %w", tool.Name, err)
	}
	return agentToolSpec{Name: tool.Name, Description: tool.Description, Properties: schema.Properties, Required: schema.Required}, nil
}
