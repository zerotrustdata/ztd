// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/governance"
)

// Streamable HTTP transport constants for the unified MCP endpoint.
//
// This endpoint implements the single-URL JSON-RPC dispatch contract that
// standard MCP clients (e.g. Claude Code custom connectors) expect:
// one path that accepts POST messages and routes by the JSON-RPC "method"
// field, anchored by the "initialize" handshake.
const (
	// mcpDefaultProtocolVersion is advertised when a client does not request a
	// specific protocol version during initialize. Clients negotiate by
	// sending their own protocolVersion, which we echo when present.
	mcpDefaultProtocolVersion = "2025-06-18"
	mcpServerName             = "ztd-gateway"
	mcpServerVersion          = "1.0"
)

// InitializeResult is the response to the MCP "initialize" handshake.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      MCPServerInfo      `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

type MCPServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCapabilities describes the server's supported MCP features.
type ServerCapabilities struct {
	Tools     ToolsCapability     `json:"tools"`
	Resources ResourcesCapability `json:"resources"`
	Prompts   PromptsCapability   `json:"prompts"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type ResourcesCapability struct{}

type PromptsCapability struct{}

// resourceTemplatesList is the result for "resources/templates/list".
type resourceTemplatesList struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
}

// initializeParams captures the subset of initialize params we negotiate on.
type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// HandleMCP is the unified MCP Streamable HTTP endpoint. It accepts a single
// JSON-RPC 2.0 request via POST and dispatches by method through the governed
// execution pipeline. GET is used for SSE streaming support.
//
//	@Summary		MCP endpoint
//	@Description	Unified MCP JSON-RPC endpoint for AI IDE integration
//	@Tags			mcp
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	response.JSONRPCResponse
//	@Router			/mcp [post]
func (g *GatewayService) HandleMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		// handled below
	case http.MethodGet:
		// SSE endpoint for server-sent events
		g.handleMCPSSE(w, r)
		return
	default:
		w.Header().Set("Allow", "POST, GET")
		g.responder.ProtoJSONCanonical(w, http.StatusMethodNotAllowed,
			governance.NewIngressRefusal(fmt.Errorf("mcp endpoint: %w", constants.ErrMethodNotAllowed)))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, g.maxPayloadBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			g.writeDispatchError(w, nil, g.rpcRefusal(-32600, fmt.Errorf("mcp endpoint: read body: %w", constants.ErrPayloadExceedsLimit)))
			return
		}
		g.writeDispatchError(w, nil, g.rpcRefusal(-32603, fmt.Errorf("mcp endpoint: read body: %w", constants.ErrRequestBodyRead)))
		return
	}

	// Reject JSON-RPC batches (an array). The current MCP spec removed batch
	// support; clients send a single message per request.
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		g.writeDispatchError(w, nil, g.rpcRefusal(-32600, fmt.Errorf("mcp endpoint: %w", constants.ErrJSONRPCBatchUnsupported)))
		return
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		g.writeDispatchError(w, nil, g.rpcRefusal(constants.JSONRPCErrorCodeParseError, fmt.Errorf("mcp endpoint: decode: %w", constants.ErrInvalidJSONBody)))
		return
	}

	if req.JSONRPC != constants.JSONRPCVersion {
		g.writeDispatchError(w, req.ID, g.rpcRefusal(-32600, fmt.Errorf("mcp endpoint: %w", constants.ErrJSONRPCVersionInvalid)))
		return
	}

	if req.Method == "" {
		g.writeDispatchError(w, req.ID, g.rpcRefusal(-32600, fmt.Errorf("mcp endpoint: %w", constants.ErrJSONRPCMethodRequired)))
		return
	}

	// Notifications (including notifications/initialized) expect no response body,
	// only acknowledgement (202 Accepted). They may carry an id or not.
	if strings.HasPrefix(req.Method, "notifications/") || req.Method == "initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, rpcErr := g.dispatchMCP(r, &req)
	if rpcErr != nil {
		g.writeDispatchError(w, req.ID, rpcErr)
		return
	}
	if result == nil {
		// A handled notification-style method with an id but no payload.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	g.responder.RPCResponse(w, req.ID, result)
}

// mcpDispatchError carries a JSON-RPC error code, message, and typed data.
// The data is always a marshaled GovernanceRejection (a signed L4 rejection)
// or IngressRefusal (every other refusal); the message is the sentinel text.
type mcpDispatchError struct {
	code    int
	message string
	data    json.RawMessage
}

// rpcRefusal builds the dispatcher error for a pre-envelope refusal. The
// message is the sentinel text only; the typed IngressRefusal is error.data.
func (g *GatewayService) rpcRefusal(code int, err error) *mcpDispatchError {
	refusal := governance.NewIngressRefusal(err)
	data, marshalErr := response.MarshalProtoJSONCanonical(refusal)
	if marshalErr != nil {
		g.logger.Error("Failed to marshal ingress refusal", "error", marshalErr)
		return &mcpDispatchError{code: -32603, message: constants.ErrInternal.Error()}
	}
	return &mcpDispatchError{code: code, message: refusal.GetError(), data: data}
}

// writeDispatchError writes a dispatcher error as a JSON-RPC error response,
// carrying its typed data when present.
func (g *GatewayService) writeDispatchError(w http.ResponseWriter, id interface{}, rpcErr *mcpDispatchError) {
	if len(rpcErr.data) > 0 {
		g.responder.RPCErrorWithData(w, id, rpcErr.code, rpcErr.message, rpcErr.data)
		return
	}
	g.responder.RPCError(w, id, rpcErr.code, rpcErr.message)
}

// dispatchMCP routes a parsed JSON-RPC request to the appropriate governed
// handler and returns either a result or a structured JSON-RPC error.
func (g *GatewayService) dispatchMCP(r *http.Request, req *JSONRPCRequest) (interface{}, *mcpDispatchError) {
	ctx := r.Context()

	// Methods that do not require runtime dependencies
	switch req.Method {
	case "initialize":
		return g.mcpInitialize(req.Params), nil

	case "ping":
		return struct{}{}, nil

	case "resources/templates/list":
		return resourceTemplatesList{ResourceTemplates: []ResourceTemplate{}}, nil
	}

	// All remaining methods require runtime dependencies
	if g.envProc == nil {
		return nil, g.rpcRefusal(-32603, fmt.Errorf("mcp dispatch: %w", constants.ErrGatewayNotReady))
	}

	switch req.Method {
	case "tools/list":
		res, err := g.listToolsResult(ctx)
		return g.wrapDispatch(res, err)

	case "tools/call":
		res, err := g.callTool(ctx, r, req.Params)
		return g.wrapDispatch(res, err)

	case "resources/list":
		res, err := g.listResourcesResult(ctx)
		return g.wrapDispatch(res, err)

	case "resources/read":
		res, err := g.readResource(ctx, req.Params)
		return g.wrapDispatch(res, err)

	case "prompts/list":
		res, err := g.listPromptsResult(ctx)
		return g.wrapDispatch(res, err)

	case "prompts/get":
		res, err := g.getPrompt(ctx, req.Params)
		return g.wrapDispatch(res, err)

	case "a2a/call":
		// A2A is opt-in: without a downstream the method does not exist.
		if g.a2aDownstreamURL == "" {
			return nil, g.rpcRefusal(-32601, fmt.Errorf("mcp dispatch: %w", constants.ErrJSONRPCMethodNotFound))
		}
		res, err := g.a2aCall(ctx, r, req.Params)
		return g.wrapDispatch(res, err)

	default:
		return nil, g.rpcRefusal(-32601, fmt.Errorf("mcp dispatch: %w", constants.ErrJSONRPCMethodNotFound))
	}
}

// wrapDispatch converts a (result, error) pair from a governed handler into the
// dispatcher's (result, *mcpDispatchError) contract, mapping governance errors
// to granular JSON-RPC codes. When the error carries the Operator's signed
// rejection receipt (*governance.ReceiptError), the typed
// GovernanceRejection is the JSON-RPC error data, so the caller holds the
// signed receipt and its rejection_code. Every other error is an
// IngressRefusal.
func (g *GatewayService) wrapDispatch(result interface{}, err error) (interface{}, *mcpDispatchError) {
	if err == nil {
		return result, nil
	}
	code := g.mapGatewayError(err)
	rejection := governance.RejectionResponse(err)
	if rejection == nil {
		return nil, g.rpcRefusal(code, err)
	}
	data, marshalErr := response.MarshalProtoJSONCanonical(rejection)
	if marshalErr != nil {
		g.logger.Error("Failed to marshal governance rejection", "error", marshalErr)
		return nil, &mcpDispatchError{code: -32603, message: constants.ErrInternal.Error()}
	}
	return nil, &mcpDispatchError{code: code, message: rejection.GetError(), data: data}
}

// mcpInitialize builds the initialize handshake response, echoing the client's
// requested protocol version when supplied.
func (g *GatewayService) mcpInitialize(params json.RawMessage) InitializeResult {
	protocolVersion := mcpDefaultProtocolVersion
	if len(params) > 0 {
		var p initializeParams
		if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
			protocolVersion = p.ProtocolVersion
		}
	}

	return InitializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities: ServerCapabilities{
			Tools:     ToolsCapability{ListChanged: true},
			Resources: ResourcesCapability{},
			Prompts:   PromptsCapability{},
		},
		ServerInfo: MCPServerInfo{
			Name:    mcpServerName,
			Version: mcpServerVersion,
		},
		Instructions: "ztd ztd Gateway. Tool calls are executed through the platform's fail-closed governance pipeline; some calls may pause for out-of-band (WebAuthn) authorization.",
	}
}

// listToolsResult returns the tool catalog: the native tools compiled into the
// Operator, then, when a downstream MCP server is configured, the downstream's
// tools. Each downstream entry is passed through byte for byte; only its name
// is read. A downstream entry whose name matches a native tool is dropped,
// because tools/call runs the native tool for that name. A downstream error or
// a malformed downstream result is returned as an error; the list never falls
// back to native tools alone.
func (g *GatewayService) listToolsResult(ctx context.Context) (toolsListResponse, error) {
	var nativeTools []NativeTool
	if g.nativeToolHandler != nil {
		nativeTools = g.nativeToolHandler.registry.List()
	}
	tools := make([]json.RawMessage, 0, len(nativeTools))
	native := make(map[string]bool, len(nativeTools))
	for _, nt := range nativeTools {
		native[nt.Name()] = true
		raw, err := json.Marshal(Tool{
			Name:        nt.Name(),
			Description: nt.Description(),
			InputSchema: nt.InputSchema(),
		})
		if err != nil {
			return toolsListResponse{}, fmt.Errorf("mcp_endpoint: encode native tool %q: %w", nt.Name(), err)
		}
		tools = append(tools, raw)
	}
	if !g.hasDownstream() {
		return toolsListResponse{Tools: tools}, nil
	}
	raw, err := g.proxyListMethod(ctx, "tools/list")
	if err != nil {
		return toolsListResponse{}, err
	}
	var downstream toolsListResponse
	if err := json.Unmarshal(raw, &downstream); err != nil {
		return toolsListResponse{}, fmt.Errorf("mcp_endpoint: decode downstream tools/list result: %w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if downstream.Tools == nil {
		return toolsListResponse{}, fmt.Errorf("mcp_endpoint: downstream tools/list result has no tools array: %w", constants.ErrInvalidJSONResponse)
	}
	for _, entry := range downstream.Tools {
		var head struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(entry, &head); err != nil {
			return toolsListResponse{}, fmt.Errorf("mcp_endpoint: decode downstream tool entry: %w: %w", constants.ErrInvalidJSONResponse, err)
		}
		if head.Name == "" {
			return toolsListResponse{}, fmt.Errorf("mcp_endpoint: downstream tool entry has no name: %w", constants.ErrInvalidJSONResponse)
		}
		if native[head.Name] {
			continue
		}
		tools = append(tools, entry)
	}
	return toolsListResponse{Tools: tools}, nil
}

// listResourcesResult returns the resource catalog (empty with no downstream).
func (g *GatewayService) listResourcesResult(ctx context.Context) (interface{}, error) {
	if !g.hasDownstream() {
		return ResourcesListResult{Resources: []Resource{}}, nil
	}
	raw, err := g.proxyListMethod(ctx, "resources/list")
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// listPromptsResult returns the prompt catalog (empty with no downstream).
func (g *GatewayService) listPromptsResult(ctx context.Context) (interface{}, error) {
	if !g.hasDownstream() {
		return PromptsListResult{Prompts: []Prompt{}}, nil
	}
	raw, err := g.proxyListMethod(ctx, "prompts/list")
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// proxyListMethod forwards a discovery method to the downstream MCP server and
// returns the raw JSON-RPC result payload. It honours the circuit breaker.
func (g *GatewayService) proxyListMethod(ctx context.Context, method string) (json.RawMessage, error) {
	return g.proxyMCPMethod(ctx, method, nil)
}

// downstreamJSONRPCRequest is the JSON-RPC envelope sent to a downstream MCP server.
type downstreamJSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// proxyMCPMethod forwards a JSON-RPC method to the downstream MCP server.
func (g *GatewayService) proxyMCPMethod(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if !g.hasDownstream() {
		return nil, constants.ErrGatewayNoDownstreamConfigured
	}
	if g.isCircuitOpen() {
		return nil, fmt.Errorf("mcp_endpoint: %w", constants.ErrGatewayDownstreamUnavailable)
	}

	if g.subprocessDownstream != nil {
		result, err := g.subprocessDownstream.Call(ctx, method, params)
		if err != nil {
			g.recordFailure()
			return nil, fmt.Errorf("mcp_endpoint: query downstream MCP subprocess: %w: %w", constants.ErrGatewayDownstreamUnavailable, err)
		}
		g.recordSuccess()
		return result, nil
	}

	reqBody, err := json.Marshal(downstreamJSONRPCRequest{
		JSONRPC: constants.JSONRPCVersion,
		ID:      1,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp_endpoint: failed to encode downstream request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.downstreamURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("mcp_endpoint: failed to build downstream request: %w", err)
	}
	httpReq.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		g.recordFailure()
		return nil, fmt.Errorf("mcp_endpoint: query downstream MCP server: %w: %w", constants.ErrGatewayDownstreamUnavailable, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			g.logger.Error("mcp_endpoint: failed to close response body", "error", cerr)
		}
	}()

	if resp.StatusCode >= 500 {
		g.recordFailure()
		return nil, fmt.Errorf("mcp_endpoint: downstream MCP server returned status %d", resp.StatusCode)
	}
	g.recordSuccess()

	var rpcResp JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("mcp_endpoint: failed to decode downstream response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("mcp_endpoint: downstream MCP error: %s", rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// handleMCPSSE handles SSE GET requests on the unified /mcp endpoint.
// This is used by Claude Code and other MCP clients that support SSE transport.
func (g *GatewayService) handleMCPSSE(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set(constants.HeaderContentType, constants.HeaderValueTextEvent)
	w.Header().Set(constants.HeaderCacheControl, constants.HeaderValueNoCache)
	w.Header().Set(constants.HeaderConnection, constants.HeaderValueKeepAlive)
	w.Header().Set(constants.HeaderAccessControlAllowOrigin, "*")
	w.Header().Set(constants.HeaderXAccelBuffering, "no") // For Nginx

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// http.Server.WriteTimeout is an absolute deadline from the first response
	// write, not an idle-between-writes timeout. Without clearing it the server
	// closes this SSE stream 30s in regardless of the keepalive heartbeat below.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		g.logger.Warn("mcp_endpoint: failed to clear WriteTimeout, SSE stream may be killed by server deadline", "error", err)
	}

	// Send initial SSE event to confirm connection
	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
	flusher.Flush()

	// Keep connection alive with periodic heartbeats
	// In a full implementation, this would subscribe to events and push them
	// For now, we maintain the connection for tool call responses
	ctx := r.Context()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
