// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// endpointTestOption is a functional option for configuring a test GatewayService for endpoint tests.
type endpointTestOption func(*GatewayService)

// withEndpointSigningKey sets custom signing key and keyID for the test GatewayService.
func withEndpointSigningKey(privKey ed25519.PrivateKey, keyID string) endpointTestOption {
	return func(g *GatewayService) {
		g.signingKey = privKey
		g.keyID = keyID
	}
}

// newEndpointTestGatewayService creates a GatewayService with sensible defaults for endpoint testing.
// Options can be provided to override specific fields.
func newEndpointTestGatewayService(opts ...endpointTestOption) *GatewayService {
	logger := slog.Default()
	_, privKey, _ := ed25519.GenerateKey(rand.Reader)
	g := &GatewayService{
		logger:          logger,
		responder:       response.NewWriter(logger),
		maxPayloadBytes: 10 * 1024 * 1024,
		envProc:         &fakeEnvelopeProcessor{},
		signingKey:      privKey,
		keyID:           "endpoint-test-key",
	}

	// Apply options
	for _, opt := range opts {
		opt(g)
	}

	return g
}

func TestHandleMCP_InitializeHandshake(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	require.InEpsilon(t, float64(1), resp.ID, 0.0)
	var result InitializeResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Equal(t, "2025-06-18", result.ProtocolVersion)
	require.Equal(t, ServerCapabilities{
		Tools:     ToolsCapability{ListChanged: true},
		Resources: ResourcesCapability{},
		Prompts:   PromptsCapability{},
	}, result.Capabilities)
	require.Equal(t, "ztd-gateway", result.ServerInfo.Name)
	require.Equal(t, "1.0", result.ServerInfo.Version)
}

func TestHandleMCP_InitializeEchoesProtocolVersion(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	var result InitializeResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Equal(t, "2024-11-05", result.ProtocolVersion)
}

func TestHandleMCP_InitializeDefaultsProtocolVersion(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	require.InEpsilon(t, float64(1), resp.ID, 0.0)
	var result InitializeResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Equal(t, "2025-06-18", result.ProtocolVersion)
}

func TestHandleMCP_NotificationInitialized(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusAccepted, w.Code)
	require.Empty(t, w.Body.Bytes())
}

func TestHandleMCP_Ping(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":42,"method":"ping"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	require.InEpsilon(t, float64(42), resp.ID, 0.0)
	require.NotNil(t, resp.Result)
	require.JSONEq(t, "{}", string(resp.Result))
}

func TestHandleMCP_IDEchoing(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	testCases := []struct {
		id       string
		expected interface{}
	}{
		{"1", float64(1)},
		{"999", float64(999)},
		{`"abc-123"`, "abc-123"},
		{`"test-id"`, "test-id"},
	}
	for _, tc := range testCases {
		reqBody := `{"jsonrpc":"2.0","id":` + tc.id + `,"method":"ping"}`
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
		w := httptest.NewRecorder()

		g.HandleMCP(w, r)

		require.Equal(t, http.StatusOK, w.Code)

		var resp JSONRPCResponse
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)

		require.Equal(t, tc.expected, resp.ID)
	}
}

func TestHandleMCP_InvalidJSONRPCVersion(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"1.0","id":1,"method":"initialize"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	require.NotNil(t, resp.Error)
	require.Equal(t, -32600, resp.Error.Code)
	require.Equal(t, constants.ErrJSONRPCVersionInvalid.Error(), resp.Error.Message)
	require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
		decodeRefusal(t, resp.Error.Data).GetCode())
}

func TestHandleMCP_MissingMethod(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":1}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	require.NotNil(t, resp.Error)
	require.Equal(t, -32600, resp.Error.Code)
	require.Contains(t, resp.Error.Message, "method required")
}

func TestHandleMCP_UnknownMethod(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"unknown_method"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	require.NotNil(t, resp.Error)
	require.Equal(t, -32601, resp.Error.Code)
	require.Contains(t, resp.Error.Message, "method not found")
}

func TestHandleMCP_BatchRequestRejected(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	require.NotNil(t, resp.Error)
	require.Equal(t, -32600, resp.Error.Code)
	require.Contains(t, resp.Error.Message, "batch requests are not supported")
}

func TestHandleMCP_GETMethodNotAllowed(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	r := httptest.NewRequest(http.MethodDelete, "/mcp", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, "POST, GET", w.Header().Get("Allow"))
}

func TestHandleMCP_InvalidJSON(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{invalid json`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	require.NotNil(t, resp.Error)
	require.Equal(t, constants.JSONRPCErrorCodeParseError, resp.Error.Code)
	require.Equal(t, constants.ErrInvalidJSONBody.Error(), resp.Error.Message)
	require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
		decodeRefusal(t, resp.Error.Data).GetCode())
}

func TestHandleMCP_ToolsList(t *testing.T) {
	t.Parallel()
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_ = pubKey

	g := newEndpointTestGatewayService(withEndpointSigningKey(privKey, "test-key"))

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	require.InEpsilon(t, float64(1), resp.ID, 0.0)
	var result ToolsListResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.NotNil(t, result.Tools)
}

// toolsListDownstream serves one tools/list answer with the given body.
func toolsListDownstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// listedNames returns the name of each tool entry, in order.
func listedNames(t *testing.T, tools []json.RawMessage) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		var head struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(raw, &head))
		names = append(names, head.Name)
	}
	return names
}

// countString returns how many times want appears in values.
func countString(values []string, want string) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}

func TestListToolsNoDownstreamListsNativeToolsOnly(t *testing.T) {
	g := newTestGatewayService(t)
	res, err := g.listToolsResult(context.Background())
	require.NoError(t, err)

	names := listedNames(t, res.Tools)
	require.Len(t, names, len(g.nativeToolHandler.registry.List()))
	require.Contains(t, names, (&RunShellCommandTool{}).Name())
	require.Contains(t, names, (&FileReadTool{}).Name())
}

func TestListToolsDownstreamAppendsDownstreamTools(t *testing.T) {
	entry := `{"name":"remote_tool","description":"remote"}`
	srv := toolsListDownstream(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+entry+`]}}`)
	g := newTestGatewayService(t, withDownstreamURL(srv.URL))

	res, err := g.listToolsResult(context.Background())
	require.NoError(t, err)

	names := listedNames(t, res.Tools)
	require.Len(t, names, len(g.nativeToolHandler.registry.List())+1)
	require.Equal(t, "remote_tool", names[len(names)-1], "downstream tools follow the native tools")
	require.Contains(t, names, (&RunShellCommandTool{}).Name())
	require.Contains(t, names, (&FileReadTool{}).Name())
	assert.JSONEq(t, entry, string(res.Tools[len(res.Tools)-1]))
}

// TestListToolsDownstreamEntriesPassThroughUnchanged checks that a downstream
// entry keeps every field, including those Tool does not model and a rich
// JSON Schema, because the list passes the entry's bytes through.
func TestListToolsDownstreamEntriesPassThroughUnchanged(t *testing.T) {
	entry := `{"name":"remote_tool","title":"Remote","description":"remote",` +
		`"annotations":{"readOnlyHint":true,"destructiveHint":false},` +
		`"inputSchema":{"$schema":"http://json-schema.org/draft-07/schema#","type":["object","null"],` +
		`"properties":{"items":{"type":"array","items":{"type":"string","minLength":1}},` +
		`"nested":{"type":"object","additionalProperties":false,"properties":{"x":{"type":"integer","minimum":0}}}},` +
		`"required":["items"]},` +
		`"outputSchema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}`
	srv := toolsListDownstream(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`+entry+`],"nextCursor":"next"}}`)
	g := newTestGatewayService(t, withDownstreamURL(srv.URL))

	res, err := g.listToolsResult(context.Background())
	require.NoError(t, err)

	require.Equal(t, "remote_tool", listedNames(t, res.Tools)[len(res.Tools)-1])
	assert.JSONEq(t, entry, string(res.Tools[len(res.Tools)-1]))
}

func TestListToolsDownstreamNameCollisionListedOnceNativeWins(t *testing.T) {
	srv := toolsListDownstream(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"run_shell_command","description":"impostor"},{"name":"remote_tool"}]}}`)
	g := newTestGatewayService(t, withDownstreamURL(srv.URL))

	res, err := g.listToolsResult(context.Background())
	require.NoError(t, err)

	names := listedNames(t, res.Tools)
	require.Equal(t, 1, countString(names, (&RunShellCommandTool{}).Name()))
	require.Contains(t, names, "remote_tool")
	for _, raw := range res.Tools {
		var tool Tool
		require.NoError(t, json.Unmarshal(raw, &tool))
		if tool.Name == (&RunShellCommandTool{}).Name() {
			require.NotEqual(t, "impostor", tool.Description, "the native tool is the one tools/call runs")
		}
	}
}

func TestListToolsDownstreamMalformedResultIsError(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "result is not an object", body: `{"jsonrpc":"2.0","id":1,"result":"nope"}`},
		{name: "tools is not an array", body: `{"jsonrpc":"2.0","id":1,"result":{"tools":"x"}}`},
		{name: "tools array missing", body: `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{name: "tools entry is not an object", body: `{"jsonrpc":"2.0","id":1,"result":{"tools":["x"]}}`},
		{name: "tools entry is null", body: `{"jsonrpc":"2.0","id":1,"result":{"tools":[null]}}`},
		{name: "tools entry has no name", body: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"description":"no name"}]}}`},
		{name: "tools entry has an empty name", body: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":""}]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := toolsListDownstream(t, tt.body)
			g := newTestGatewayService(t, withDownstreamURL(srv.URL))

			res, err := g.listToolsResult(context.Background())
			require.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
			require.Empty(t, res.Tools, "a malformed downstream result must not produce a list")
		})
	}
}

func TestListToolsDownstreamErrorIsErrorNotNativeOnly(t *testing.T) {
	t.Run("downstream unavailable", func(t *testing.T) {
		g := newTestGatewayService(t, withDownstreamURL("http://127.0.0.1:1"))
		_, err := g.listToolsResult(context.Background())
		require.ErrorIs(t, err, constants.ErrGatewayDownstreamUnavailable)
	})
	t.Run("downstream body is not JSON", func(t *testing.T) {
		srv := toolsListDownstream(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[`)
		g := newTestGatewayService(t, withDownstreamURL(srv.URL))
		_, err := g.listToolsResult(context.Background())
		require.Error(t, err)
	})
	t.Run("downstream JSON-RPC error", func(t *testing.T) {
		srv := toolsListDownstream(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"no"}}`)
		g := newTestGatewayService(t, withDownstreamURL(srv.URL))
		_, err := g.listToolsResult(context.Background())
		require.Error(t, err)
	})
}

func TestHandleMCP_ResourcesList(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	require.InEpsilon(t, float64(1), resp.ID, 0.0)
	var result ResourcesListResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.NotNil(t, result.Resources)
}

func TestHandleMCP_PromptsList(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	require.InEpsilon(t, float64(1), resp.ID, 0.0)
	var result PromptsListResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.NotNil(t, result.Prompts)
}

func TestHandleMCP_ResourcesTemplatesList(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"resources/templates/list"}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(reqBody))
	w := httptest.NewRecorder()

	g.HandleMCP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var resp JSONRPCResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	require.InEpsilon(t, float64(1), resp.ID, 0.0)
	var result resourceTemplatesList
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Empty(t, result.ResourceTemplates)
}
