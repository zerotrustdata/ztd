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
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

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
func (c *Client) DispatchCommand(ctx context.Context, persona Persona, request models.OperatorCommandRequest) (*GovernedOutcome, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("agent harness: validate dispatch command: %w", err)
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
	httpClient := *c.http
	if c.cliHTTP != nil {
		httpClient = *c.cliHTTP
	}
	httpClient.Timeout = mcp.ApprovalRequestTTL + dispatch.RequestDeadline
	status, raw, err := c.doWithClient(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.OperatorsCommands, body, &httpClient, constants.HeaderValueApplicationJSON)
	if err != nil {
		return nil, fmt.Errorf("agent harness: submit dispatch command: %w", err)
	}
	return decodeHTTPOutcome(status, raw, &operatorv1.GovernedDispatchResult{}), nil
}

// marshalJSONRPC renders one JSON-RPC 2.0 request for method. A nil params is
// omitted from the envelope, as the production request omits an empty params.
func marshalJSONRPC(method string, params any) ([]byte, error) {
	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("agent harness: marshal json-rpc %s params: %w", method, err)
		}
		raw = encoded
	}
	body, err := json.Marshal(mcp.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: raw})
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
func (c *Client) rpcResponse(ctx context.Context, p Persona, path, method string, params any) (*mcp.JSONRPCResponse, error) {
	_, raw, err := c.rpc(ctx, p, path, method, params)
	if err != nil {
		return nil, err
	}
	resp := &mcp.JSONRPCResponse{}
	if err := decodeStrictJSON(raw, resp); err != nil {
		return nil, fmt.Errorf("agent harness: decode %s response: %w", method, err)
	}
	return resp, nil
}

// ---- MCP --------------------------------------------------------------------

func (c *Client) MCPToolsList(ctx context.Context, p Persona) (*mcp.JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "tools/list", struct{}{})
}

// MCPToolsCall invokes a tool. request is the tools/call params: its Arguments
// are the tool's production request type, marshaled by the caller. The Gateway
// wraps this into a governed MCP_CALL envelope, runs the interlock sequence, and
// dispatches to the real Operator.
func (c *Client) MCPToolsCall(ctx context.Context, p Persona, request mcp.CallToolRequest) (*GovernedOutcome, error) {
	status, raw, err := c.rpc(ctx, p, "/mcp", "tools/call", request)
	if err != nil {
		return nil, err
	}
	return decodeRPCOutcome(status, raw, decodeMCPResult), nil
}

// MCPToolsCallWithCLI is like MCPToolsCall but routes through the CLI-cert
// TLS client so the gateway's handleCLIAuth stamps the host user's identity
// onto the suspended transaction. Use this for notary-scenario submits.
func (c *Client) MCPToolsCallWithCLI(ctx context.Context, p Persona, request mcp.CallToolRequest) (*GovernedOutcome, error) {
	status, raw, err := c.rpcWithCLI(ctx, p, "/mcp", "tools/call", request)
	if err != nil {
		return nil, err
	}
	return decodeRPCOutcome(status, raw, decodeMCPResult), nil
}

func (c *Client) MCPResourcesList(ctx context.Context, p Persona) (*mcp.JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "resources/list", struct{}{})
}

func (c *Client) MCPResourcesRead(ctx context.Context, p Persona, request mcp.ReadResourceRequest) (*mcp.JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "resources/read", request)
}

func (c *Client) MCPPromptsList(ctx context.Context, p Persona) (*mcp.JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "prompts/list", struct{}{})
}

// MCPPromptsGet invokes a named prompt. request is the prompts/get params.
func (c *Client) MCPPromptsGet(ctx context.Context, p Persona, request mcp.GetPromptRequest) (*mcp.JSONRPCResponse, error) {
	return c.rpcResponse(ctx, p, "/mcp", "prompts/get", request)
}

// ---- A2A --------------------------------------------------------------------

// A2ACall invokes an A2A skill. request is the a2a/call params; a nil Payload is
// forwarded as JSON null.
func (c *Client) A2ACall(ctx context.Context, p Persona, request mcp.A2ACallRequest) (*GovernedOutcome, error) {
	status, raw, err := c.rpc(ctx, p, constants.APIPaths.A2ACall, "a2a/call", request)
	if err != nil {
		return nil, err
	}
	return decodeRPCOutcome(status, raw, decodeA2AResult), nil
}
