// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// JSONRPCRequest / JSONRPCResponse mirror the Gateway's MCP/A2A ingress shape.
type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type DispatchCommandRequest struct {
	TargetOperatorSessionID string `json:"target_operator_session_id"`
	ActionType              string `json:"action_type,omitempty"`
	EventType               string `json:"event_type,omitempty"`
	Payload                 []byte `json:"payload"`
	TargetResource          string `json:"target_resource,omitempty"`
	CaseID                  string `json:"case_id,omitempty"`
	InvestigationID         string `json:"investigation_id,omitempty"`
	TaskID                  string `json:"task_id,omitempty"`
	WebSessionID            string `json:"web_session_id,omitempty"`
	CLISessionID            string `json:"cli_session_id,omitempty"`
}

func (c *Client) Health(ctx context.Context) (*models.HealthResponse, []byte, error) {
	status, raw, err := c.do(ctx, c.auditorPersona(), http.MethodGet, c.cfg.MTLSBaseURL+constants.APIPaths.Health, nil)
	if err != nil {
		return nil, raw, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, raw, fmt.Errorf("%w: gateway health returned status %d", constants.ErrHTTPStatusError, status)
	}
	health := &models.HealthResponse{}
	if err := json.Unmarshal(raw, health); err != nil {
		return nil, raw, fmt.Errorf("%w: decode gateway health: %v", constants.ErrInvalidJSONResponse, err)
	}
	if health.Posture == "" {
		return nil, raw, fmt.Errorf("%w: gateway health omits posture", constants.ErrInvalidJSONResponse)
	}
	return health, raw, nil
}

// DispatchCommand submits one governed operator command through the CLI-cert
// client and returns the typed outcome of the Gateway's answer. A transport
// failure is returned as an error, never as an outcome.
func (c *Client) DispatchCommand(ctx context.Context, persona Persona, request DispatchCommandRequest) (*GovernedOutcome, error) {
	if request.TargetOperatorSessionID == "" || (request.ActionType == "" && request.EventType == "") || len(request.Payload) == 0 {
		return nil, constants.ErrMissingRequiredField
	}
	if request.EventType == "" && request.ActionType != "" {
		eventType, err := constants.RequestEventForAction(constants.ActionType(request.ActionType))
		if err != nil {
			return nil, fmt.Errorf("agent harness: resolve request event: %w", err)
		}
		request.EventType = string(eventType)
	}
	if request.ActionType == "" && request.EventType != "" {
		actionType, err := constants.ValidateGovernedRequest(constants.EventType(request.EventType))
		if err != nil {
			return nil, fmt.Errorf("agent harness: resolve action type: %w", err)
		}
		request.ActionType = string(actionType)
	}
	if err := constants.ValidateGovernedEnvelopeFields(constants.EventType(request.EventType), constants.ActionType(request.ActionType)); err != nil {
		return nil, fmt.Errorf("agent harness: validate request fields: %w", err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("agent harness: marshal dispatch command: %w", err)
	}
	return c.DispatchCommandBody(ctx, persona, body)
}

// DispatchCommandBody posts body to the governed dispatch ingress unchanged and
// classifies the answer. It performs no validation: conformance cases use it to
// send malformed requests.
func (c *Client) DispatchCommandBody(ctx context.Context, p Persona, body []byte) (*GovernedOutcome, error) {
	status, raw, err := c.doWithCLI(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.OperatorsCommands, body)
	if err != nil {
		return nil, fmt.Errorf("agent harness: submit dispatch command: %w", err)
	}
	return decodeHTTPOutcome(status, raw, &operatorv1.GovernedDispatchResult{}), nil
}

// marshalJSONRPC renders one JSON-RPC 2.0 request for method.
func marshalJSONRPC(method string, params any) ([]byte, error) {
	body, err := json.Marshal(JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("agent harness: marshal json-rpc %s: %w", method, err)
	}
	return body, nil
}

// rpc posts a JSON-RPC request to an MCP/A2A route and returns the raw answer.
func (c *Client) rpc(ctx context.Context, p Persona, path, method string, params any) (int, []byte, error) {
	body, err := marshalJSONRPC(method, params)
	if err != nil {
		return 0, nil, err
	}
	return c.do(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+path, body)
}

// rpcWithCLI is like rpc but uses the CLI-cert TLS client so handleCLIAuth
// stamps the host user's identity onto the suspended transaction.
func (c *Client) rpcWithCLI(ctx context.Context, p Persona, path, method string, params any) (int, []byte, error) {
	body, err := marshalJSONRPC(method, params)
	if err != nil {
		return 0, nil, err
	}
	return c.doWithCLI(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+path, body)
}

// rpcResponse posts a JSON-RPC request for a caller that reads the envelope
// itself (list and read methods). The answer must be a strict JSON-RPC envelope.
func (c *Client) rpcResponse(ctx context.Context, p Persona, path, method string, params any) (*JSONRPCResponse, error) {
	_, raw, err := c.rpc(ctx, p, path, method, params)
	if err != nil {
		return nil, err
	}
	resp := &JSONRPCResponse{}
	if err := decodeStrictJSON(raw, resp); err != nil {
		return nil, fmt.Errorf("agent harness: decode %s response: %w", method, err)
	}
	return resp, nil
}

// ---- MCP --------------------------------------------------------------------

// ToolArgs is the typed argument payload for an MCP tools/call invocation.
// Each implementation is a JSON-serializable struct matching a tool's
// argument schema. The harness client marshals the value under the
// "arguments" key of the JSON-RPC params, mirroring the wire shape that
// loose map[string]any values produced previously.
type ToolArgs interface {
	isToolArgs()
}

// ShellCommandArgs are the arguments for the run_shell_command tool.
type ShellCommandArgs struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Timeout int      `json:"timeout"`
}

func (ShellCommandArgs) isToolArgs() {}

// FSPathArgs are the arguments for path-only filesystem tools (fs_list, fs_read).
type FSPathArgs struct {
	Path string `json:"path"`
}

func (FSPathArgs) isToolArgs() {}

// FSGrepArgs are the arguments for the fs_grep tool.
type FSGrepArgs struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
}

func (FSGrepArgs) isToolArgs() {}

// FSWriteArgs are the arguments for the fs_write tool.
type FSWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (FSWriteArgs) isToolArgs() {}

// ExecuteBashArgs are the arguments for the execute_bash tool.
type ExecuteBashArgs struct {
	Command string `json:"command"`
}

func (ExecuteBashArgs) isToolArgs() {}

// AuditReceiptListArgs are the arguments for the audit_receipt_list tool.
type AuditReceiptListArgs struct {
	OperatorSessionID string `json:"operator_session_id"`
	ActionType        string `json:"action_type,omitempty"`
	NotBefore         string `json:"not_before,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	Offset            int    `json:"offset,omitempty"`
}

func (AuditReceiptListArgs) isToolArgs() {}

// AuditReceiptGetArgs are the arguments for the audit_receipt_get tool.
type AuditReceiptGetArgs struct {
	TransactionID string `json:"transaction_id"`
}

func (AuditReceiptGetArgs) isToolArgs() {}

// PromptArgs is the typed argument payload for an MCP prompts/get invocation.
// Each implementation matches one prompt's argument schema. The harness client
// marshals the value under the "arguments" key, the same way ToolArgs replaced
// loose maps for tools/call.
type PromptArgs interface {
	isPromptArgs()
}

// SummarizePromptArgs are the arguments for the summarize prompt.
type SummarizePromptArgs struct {
	Target string `json:"target"`
}

func (SummarizePromptArgs) isPromptArgs() {}

// SkillPayload is the typed JSON payload for an A2A skill invocation.
// Each implementation matches one skill's payload schema.
type SkillPayload interface {
	isSkillPayload()
}

// ListDirectorySkillPayload is the list_directory skill payload.
// Recursive is omitted when unset so a path-only call stays path-only.
type ListDirectorySkillPayload struct {
	Path      string `json:"path"`
	Recursive *bool  `json:"recursive,omitempty"`
}

func (ListDirectorySkillPayload) isSkillPayload() {}

// ReadFileSkillPayload is the read_file skill payload.
type ReadFileSkillPayload struct {
	Path string `json:"path"`
}

func (ReadFileSkillPayload) isSkillPayload() {}

// CommandSkillPayload is the payload for a skill that carries a shell command.
type CommandSkillPayload struct {
	Cmd string `json:"cmd"`
}

func (CommandSkillPayload) isSkillPayload() {}

// ---- MCP params envelopes ---------------------------------------------------

// toolsCallParams is the JSON-RPC params envelope for tools/call.
type toolsCallParams struct {
	Name      string   `json:"name"`
	Arguments ToolArgs `json:"arguments"`
}

// resourcesReadParams is the JSON-RPC params envelope for resources/read.
type resourcesReadParams struct {
	URI string `json:"uri"`
}

// promptsGetParams is the JSON-RPC params envelope for prompts/get.
type promptsGetParams struct {
	Name      string     `json:"name"`
	Arguments PromptArgs `json:"arguments"`
}

func (c *Client) MCPToolsList(ctx context.Context, p Persona) (*JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "tools/list", struct{}{})
}

// MCPToolsCall invokes a tool. The Gateway wraps this into a governed MCP_CALL
// envelope, runs the interlock sequence, and dispatches to the real Operator.
func (c *Client) MCPToolsCall(ctx context.Context, p Persona, tool string, args ToolArgs) (*GovernedOutcome, error) {
	status, raw, err := c.rpc(ctx, p, "/mcp", "tools/call", toolsCallParams{
		Name:      tool,
		Arguments: args,
	})
	if err != nil {
		return nil, err
	}
	return decodeRPCOutcome(status, raw, decodeMCPResult), nil
}

// MCPToolsCallWithCLI is like MCPToolsCall but routes through the CLI-cert
// TLS client so the gateway's handleCLIAuth stamps the host user's identity
// onto the suspended transaction. Use this for notary-scenario submits.
func (c *Client) MCPToolsCallWithCLI(ctx context.Context, p Persona, tool string, args ToolArgs) (*GovernedOutcome, error) {
	status, raw, err := c.rpcWithCLI(ctx, p, "/mcp", "tools/call", toolsCallParams{
		Name:      tool,
		Arguments: args,
	})
	if err != nil {
		return nil, err
	}
	return decodeRPCOutcome(status, raw, decodeMCPResult), nil
}

func (c *Client) MCPResourcesList(ctx context.Context, p Persona) (*JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "resources/list", struct{}{})
}

func (c *Client) MCPResourcesRead(ctx context.Context, p Persona, uri string) (*JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "resources/read", resourcesReadParams{URI: uri})
}

func (c *Client) MCPPromptsList(ctx context.Context, p Persona) (*JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "prompts/list", struct{}{})
}

// MCPPromptsGet invokes a named prompt. args is a PromptArgs value matching
// that prompt's argument schema.
func (c *Client) MCPPromptsGet(ctx context.Context, p Persona, name string, args PromptArgs) (*JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "prompts/get", promptsGetParams{
		Name:      name,
		Arguments: args,
	})
}

// ---- A2A --------------------------------------------------------------------

// a2aCallParams is the JSON-RPC params envelope for a2a/call with a JSON payload.
type a2aCallParams struct {
	SkillName   string       `json:"skill_name"`
	Payload     SkillPayload `json:"payload"`
	ExecutionID string       `json:"execution_id"`
}

// A2ACall invokes an A2A skill with a typed JSON payload. payload matches
// the skill's schema; a nil payload is forwarded as JSON null.
func (c *Client) A2ACall(ctx context.Context, p Persona, skill string, payload SkillPayload, execID string) (*GovernedOutcome, error) {
	status, raw, err := c.rpc(ctx, p, constants.APIPaths.A2ACall, "a2a/call", a2aCallParams{
		SkillName:   skill,
		Payload:     payload,
		ExecutionID: execID,
	})
	if err != nil {
		return nil, err
	}
	return decodeRPCOutcome(status, raw, decodeA2AResult), nil
}
