// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	openaiTestModel         = "gpt-5-2025-08-07"
	openaiTestCreated       = int64(1754524800)
	openaiTestVersion       = "1754524800"
	openaiTestKey           = "test-openai-key"
	openaiTestResponseID    = "resp_01abc123def456"
	openaiTestCallID        = "call_openai_weather_1"
	openaiTestReasoningID   = "rs_01encrypted999"
	openaiTestEncryptedBlob = "gAAAAABnTestEncryptedReasoningPayload=="
)

func openaiToolCallStream(servedModel string) string {
	return fmt.Sprintf(
		"event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"%[1]s\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"%[2]s\"}}\r\n\r\n"+
			"event: response.in_progress\r\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":1,\"response\":{\"id\":\"%[1]s\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"%[2]s\"}}\r\n\r\n"+
			"event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"id\":\"%[3]s\",\"type\":\"reasoning\",\"summary\":[]}}\r\n\r\n"+
			"event: response.output_item.done\r\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":0,\"item\":{\"id\":\"%[3]s\",\"type\":\"reasoning\",\"encrypted_content\":\"%[4]s\",\"summary\":[]}}\r\n\r\n"+
			"event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":4,\"output_index\":1,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"in_progress\",\"call_id\":\"%[5]s\",\"name\":\"get_weather\",\"arguments\":\"\"}}\r\n\r\n"+
			"event: response.function_call_arguments.delta\r\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":5,\"item_id\":\"fc_1\",\"output_index\":1,\"delta\":\"{\\\"city\\\":\"}\r\n\r\n"+
			"event: response.function_call_arguments.delta\r\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":6,\"item_id\":\"fc_1\",\"output_index\":1,\"delta\":\"\\\"Paris\\\"}\"}\r\n\r\n"+
			"event: response.function_call_arguments.done\r\ndata: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":7,\"item_id\":\"fc_1\",\"output_index\":1,\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}\r\n\r\n"+
			"event: response.output_item.done\r\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":8,\"output_index\":1,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"completed\",\"call_id\":\"%[5]s\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\r\n\r\n"+
			"event: response.completed\r\ndata: {\"type\":\"response.completed\",\"sequence_number\":9,\"response\":{\"id\":\"%[1]s\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"%[2]s\",\"usage\":{\"input_tokens\":55,\"input_tokens_details\":{\"cached_tokens\":10},\"output_tokens\":55,\"output_tokens_details\":{\"reasoning_tokens\":39},\"total_tokens\":110}}}\r\n\r\n",
		openaiTestResponseID, servedModel, openaiTestReasoningID, openaiTestEncryptedBlob, openaiTestCallID,
	)
}

func openaiTextStream(status, incompleteReason string) string {
	terminalEvent := "response.completed"
	incompleteJSON := ""
	if status == "incomplete" {
		terminalEvent = "response.incomplete"
		if incompleteReason != "" {
			incompleteJSON = fmt.Sprintf(`,"incomplete_details":{"reason":%q}`, incompleteReason)
		}
	}
	return fmt.Sprintf(
		"event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":\"%[1]s\"}}\r\n\r\n"+
			"event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\"}}\r\n\r\n"+
			"event: response.content_part.added\r\ndata: {\"type\":\"response.content_part.added\",\"sequence_number\":2,\"output_index\":0,\"part\":{\"type\":\"output_text\"}}\r\n\r\n"+
			"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"output_index\":0,\"delta\":\"Hello\"}\r\n\r\n"+
			"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":4,\"output_index\":0,\"delta\":\" there\"}\r\n\r\n"+
			"event: response.output_text.done\r\ndata: {\"type\":\"response.output_text.done\",\"sequence_number\":5,\"output_index\":0,\"text\":\"Hello there\"}\r\n\r\n"+
			"event: response.content_part.done\r\ndata: {\"type\":\"response.content_part.done\",\"sequence_number\":6,\"output_index\":0,\"part\":{\"type\":\"output_text\"}}\r\n\r\n"+
			"event: response.output_item.done\r\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":7,\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\"}}\r\n\r\n"+
			"event: %[2]s\r\ndata: {\"type\":\"%[2]s\",\"sequence_number\":8,\"response\":{\"id\":\"r1\",\"status\":\"%[3]s\",\"model\":\"%[1]s\"%[4]s,\"usage\":{\"input_tokens\":7,\"input_tokens_details\":{\"cached_tokens\":1},\"output_tokens\":5,\"output_tokens_details\":{\"reasoning_tokens\":3},\"total_tokens\":12}}}\r\n\r\n",
		openaiTestModel, terminalEvent, status, incompleteJSON,
	)
}

func openaiModelMetadata(model string, created int64) string {
	return fmt.Sprintf(`{"id":%q,"object":"model","created":%d,"owned_by":"openai"}`, model, created)
}

// openaiFake is an in-process OpenAI API fake: no listener, no network.
type openaiFake struct {
	mu        sync.Mutex
	modelID   string
	created   int64
	stream    string
	status    int
	errorBody string
	list      string
	metaCode  int
	metaError string
	redirect  string
	requests  []*http.Request
	bodies    [][]byte
}

func (f *openaiFake) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, body)
	respond := func(status int, payload string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
	}
	if f.redirect != "" {
		resp, _ := respond(http.StatusFound, "")
		resp.Header.Set("Location", f.redirect)
		return resp, nil
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/v1/models":
		return respond(http.StatusOK, f.list)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/models/"):
		if f.metaCode != 0 {
			errBody := f.metaError
			if errBody == "" {
				errBody = `{"error":{"code":"model_not_found"}}`
			}
			return respond(f.metaCode, errBody)
		}
		model := strings.TrimPrefix(path, "/v1/models/")
		if f.modelID != "" {
			model = f.modelID
		}
		return respond(http.StatusOK, openaiModelMetadata(model, f.created))
	case r.Method == http.MethodPost && path == "/v1/responses":
		if f.status != 0 && f.status != http.StatusOK {
			return respond(f.status, f.errorBody)
		}
		return respond(http.StatusOK, f.stream)
	default:
		return respond(http.StatusTeapot, "")
	}
}

func (f *openaiFake) posts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, r := range f.requests {
		if r.Method == http.MethodPost {
			count++
		}
	}
	return count
}

func (f *openaiFake) lastPostBody(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].Method == http.MethodPost {
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(f.bodies[i], &decoded))
			return decoded
		}
	}
	t.Fatal("no generation request was sent")
	return nil
}

func openaiOrigin(t *testing.T) string {
	t.Helper()
	origin, ok := constants.HostedInferenceProviderOrigin(constants.InferenceProviderOpenAI)
	require.True(t, ok)
	return origin
}

func newFakeOpenAIBackend(t *testing.T, fake *openaiFake) *OpenAIBackend {
	t.Helper()
	if fake.created == 0 {
		fake.created = openaiTestCreated
	}
	backend, err := NewOpenAIBackend(openaiOrigin(t), openaiTestKey, testutil.NewTestLogger())
	require.NoError(t, err)
	backend.client.Transport = fake
	return backend
}

func openaiIdentityDigest(t *testing.T, model string) string {
	t.Helper()
	digest, err := models.ComputeHostedModelIdentityDigest(constants.InferenceProviderOpenAI, openaiOrigin(t), model, openaiTestVersion)
	require.NoError(t, err)
	return digest
}

func openaiRequest(model string) models.GenerateRequest {
	return models.GenerateRequest{
		ProviderClass:     constants.InferenceProviderOpenAI,
		Model:             model,
		ProviderAttemptID: "attempt-1",
		Messages: []*operatorv1.InferenceMessage{
			{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "You are terse."}}}},
			{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "What is the weather in Paris?"}}}},
		},
		Tools: []*operatorv1.InferenceToolDeclaration{weatherTool()},
	}
}

func TestOpenAIBackend_GenerateDecodesReasoningAndToolCallStream(t *testing.T) {
	t.Parallel()
	fake := &openaiFake{stream: openaiToolCallStream(openaiTestModel)}
	backend := newFakeOpenAIBackend(t, fake)

	resp, err := backend.Generate(testutil.Context(t), openaiRequest(openaiTestModel))
	require.NoError(t, err)

	require.Len(t, resp.Parts, 2)
	reasoning := resp.Parts[0].GetProviderReasoning()
	require.NotNil(t, reasoning)
	assert.Equal(t, constants.InferenceProviderOpenAI, reasoning.GetProviderClass())
	wantCanonical := fmt.Sprintf(`{"encrypted_content":%q,"id":%q,"summary":[],"type":"reasoning"}`, openaiTestEncryptedBlob, openaiTestReasoningID)
	assert.Equal(t, wantCanonical, reasoning.GetItemJson())

	call := resp.Parts[1].GetToolCall()
	require.NotNil(t, call)
	assert.Equal(t, openaiTestCallID, call.GetCallId())
	assert.Equal(t, "get_weather", call.GetName())
	assert.JSONEq(t, `{"city":"Paris"}`, call.GetArgumentsJson())

	assert.Equal(t, "stop", resp.FinishReason)
	assert.True(t, resp.UsageReported)
	assert.Equal(t, int32(55), resp.PromptTokens)
	assert.Equal(t, int32(55), resp.CompletionTokens)
	assert.Equal(t, int32(110), resp.TotalTokens)
	require.NotNil(t, resp.ThinkingTokens)
	assert.Equal(t, int32(39), *resp.ThinkingTokens)
	require.NotNil(t, resp.CacheTokens)
	assert.Equal(t, int32(10), *resp.CacheTokens)
	assert.Zero(t, resp.ContextWindow, "OpenAI does not expose context window")
	assert.Equal(t, openaiTestModel, resp.Model)
	assert.Empty(t, resp.ServedModelDigest, "no digest was requested")
	assert.True(t, models.IsSHA256Hex(resp.NormalizedRequestHash))
	assert.True(t, models.IsSHA256Hex(resp.OutputHash))

	egress := resp.HostedEgress
	require.NotNil(t, egress)
	assert.Equal(t, openaiOrigin(t), egress.GetProviderOrigin())
	assert.Equal(t, constants.InferenceProviderOpenAI, egress.GetProviderClass())
	assert.Equal(t, openaiTestModel, egress.GetRequestedModel())
	assert.Equal(t, openaiTestModel, egress.GetModelVersion())
	assert.Equal(t, openaiTestVersion, egress.GetProviderModelVersion())
	assert.Equal(t, openaiIdentityDigest(t, openaiTestModel), egress.GetModelIdentityDigest())
	assert.Equal(t, openaiTestResponseID, egress.GetProviderResponseId())
	assert.Equal(t, models.SHA256Hex([]byte(fake.stream)), egress.GetRawResponseDigest())
	assert.Equal(t, uint64(len(fake.stream)), egress.GetRawResponseSizeBytes())
	assert.Equal(t, resp.NormalizedRequestHash, egress.GetNormalizedRequestHash())
	assert.True(t, egress.GetExplicitConsent())
	require.NoError(t, models.ValidateHostedEgress(constants.InferenceProviderOpenAI, openaiTestModel, "", resp.ToProtoInferenceResult()))
}

func TestOpenAIBackend_KeyTravelsOnlyInTheHeader(t *testing.T) {
	t.Parallel()
	fake := &openaiFake{stream: openaiToolCallStream(openaiTestModel)}
	backend := newFakeOpenAIBackend(t, fake)
	_, err := backend.Generate(testutil.Context(t), openaiRequest(openaiTestModel))
	require.NoError(t, err)

	require.Len(t, fake.requests, 2, "one metadata read and one generation")
	for _, r := range fake.requests {
		assert.Equal(t, constants.BearerScheme+openaiTestKey, r.Header.Get(constants.HeaderAuthorization))
		assert.NotContains(t, r.URL.String(), openaiTestKey)
		assert.Equal(t, "api.openai.com", r.URL.Host)
	}
	assert.Equal(t, "/v1/models/"+openaiTestModel, fake.requests[0].URL.Path)
	assert.Equal(t, "/v1/responses", fake.requests[1].URL.Path)
	assert.Empty(t, fake.requests[1].URL.RawQuery)
}

func TestOpenAIBackend_RedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	fake := &openaiFake{redirect: "https://elsewhere.example/steal"}
	backend := newFakeOpenAIBackend(t, fake)
	_, err := backend.Generate(testutil.Context(t), openaiRequest(openaiTestModel))
	require.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
	require.Len(t, fake.requests, 1, "the redirect target must never be requested")
	assert.Equal(t, "api.openai.com", fake.requests[0].URL.Host)
}

func TestNewOpenAIBackend_RejectsInvalidCredentialTargets(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{
		"http://api.openai.com",
		"https://proxy.example",
		"https://user:password@api.openai.com",
		"https://api.openai.com/prefix",
		"https://api.openai.com?key=secret",
		"https://api.openai.com#fragment",
	} {
		t.Run(origin, func(t *testing.T) {
			backend, err := NewOpenAIBackend(origin, openaiTestKey, testutil.NewTestLogger())
			require.ErrorIs(t, err, constants.ErrInferenceEndpointInvalid)
			assert.Nil(t, backend)
		})
	}
	for _, key := range []string{" ", "abc\ndef", "abc\rdef"} {
		t.Run("invalid key "+fmt.Sprintf("%q", key), func(t *testing.T) {
			backend, err := NewOpenAIBackend(openaiOrigin(t), key, testutil.NewTestLogger())
			require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
			assert.Nil(t, backend)
		})
	}
	t.Run("nil logger", func(t *testing.T) {
		backend, err := NewOpenAIBackend(openaiOrigin(t), openaiTestKey, nil)
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		assert.Nil(t, backend)
	})
}

func TestOpenAIBackend_PinnedDigestBindsModelVersionAndServedModel(t *testing.T) {
	t.Parallel()
	pinned := openaiIdentityDigest(t, openaiTestModel)

	t.Run("matching created timestamp serves and reports the digest", func(t *testing.T) {
		fake := &openaiFake{stream: openaiToolCallStream(openaiTestModel)}
		req := openaiRequest(openaiTestModel)
		req.ModelDigest = pinned
		resp, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		assert.Equal(t, pinned, resp.ServedModelDigest)
		require.NoError(t, models.ValidateHostedEgress(constants.InferenceProviderOpenAI, openaiTestModel, pinned, resp.ToProtoInferenceResult()))
	})

	t.Run("a re-versioned model fails before generation", func(t *testing.T) {
		fake := &openaiFake{created: openaiTestCreated + 86400, stream: openaiToolCallStream(openaiTestModel)}
		req := openaiRequest(openaiTestModel)
		req.ModelDigest = pinned
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
		assert.Zero(t, fake.posts(), "no prompt may leave once the identity changed")
	})

	t.Run("an alias resolved by models endpoint fails before generation", func(t *testing.T) {
		fake := &openaiFake{modelID: "gpt-5-2025-09-01", stream: openaiToolCallStream("gpt-5-2025-09-01")}
		req := openaiRequest("gpt-5")
		req.ModelDigest = openaiIdentityDigest(t, "gpt-5")
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
		assert.Zero(t, fake.posts())
	})

	t.Run("a provider reroute on stream fails closed", func(t *testing.T) {
		fake := &openaiFake{stream: openaiToolCallStream("gpt-5-2025-09-01")}
		req := openaiRequest(openaiTestModel)
		req.ModelDigest = pinned
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
	})

	t.Run("an unpinned reroute on stream is recorded, not hidden", func(t *testing.T) {
		fake := &openaiFake{stream: openaiToolCallStream("gpt-5-2025-09-01")}
		resp, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), openaiRequest(openaiTestModel))
		require.NoError(t, err)
		assert.Equal(t, openaiTestModel, resp.Model)
		assert.Equal(t, "gpt-5-2025-09-01", resp.HostedEgress.GetModelVersion())
	})
}

func TestOpenAIBackend_GenerateRequestMappingAndReasoningRoundTrip(t *testing.T) {
	t.Parallel()
	fake := &openaiFake{stream: openaiTextStream("completed", "")}
	reasoningJSON := fmt.Sprintf(`{"encrypted_content":%q,"id":%q,"summary":[],"type":"reasoning"}`, openaiTestEncryptedBlob, openaiTestReasoningID)
	req := openaiRequest(openaiTestModel)
	req.Tools = append(req.Tools, &operatorv1.InferenceToolDeclaration{Name: "other", JsonSchema: `{"type":"object"}`})
	req.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO, AllowedToolNames: []string{"get_weather"}}
	parallel := false
	req.ParallelToolCalls = &parallel
	req.Temperature = 0.2
	topP := float32(0.9)
	req.TopP = &topP
	req.MaxTokens = 256
	req.Messages = append(req.Messages,
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{
				ProviderClass: constants.InferenceProviderOpenAI,
				ItemJson:      reasoningJSON,
			}}},
			{Part: &operatorv1.InferenceMessagePart_Text{Text: "Checking weather."}},
			{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c1", Name: "get_weather", ArgumentsJson: `{"city":"Paris"}`}}},
			{Part: &operatorv1.InferenceMessagePart_Text{Text: ""}},
		}},
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{CallId: "c1", Name: "get_weather", ResultJson: `{"temp":21}`}}},
		}},
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_Text{Text: "Be concise."}},
		}},
	)
	_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
	require.NoError(t, err)

	body := fake.lastPostBody(t)
	assert.Equal(t, openaiTestModel, body["model"])
	assert.Equal(t, false, body["store"])
	assert.Equal(t, true, body["stream"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
	assert.Equal(t, "You are terse.", body["instructions"])
	assert.InDelta(t, 0.2, body["temperature"], 1e-6)
	assert.InDelta(t, 0.9, body["top_p"], 1e-6)
	assert.EqualValues(t, 256, body["max_output_tokens"])
	assert.Equal(t, "auto", body["tool_choice"])
	assert.Equal(t, false, body["parallel_tool_calls"])
	assert.NotContains(t, body, "reasoning", "default thinking leaves reasoning unset and requests no summary")

	tools := body["tools"].([]any)
	require.Len(t, tools, 1, "the allowed-name list narrows the declarations")
	tool := tools[0].(map[string]any)
	assert.Equal(t, "function", tool["type"])
	assert.Equal(t, "get_weather", tool["name"])
	assert.Equal(t, true, tool["strict"])

	input := body["input"].([]any)
	require.Len(t, input, 6, "user, reasoning, assistant text, function_call, function_call_output, mid-history developer")
	assert.Equal(t, "user", input[0].(map[string]any)["role"])
	assert.Equal(t, map[string]any{
		"type":              "reasoning",
		"id":                openaiTestReasoningID,
		"encrypted_content": openaiTestEncryptedBlob,
		"summary":           []any{},
	}, input[1])
	assert.Equal(t, map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": "Checking weather."}},
	}, input[2])
	assert.Equal(t, map[string]any{
		"type":      "function_call",
		"call_id":   "c1",
		"name":      "get_weather",
		"arguments": `{"city":"Paris"}`,
	}, input[3])
	assert.Equal(t, map[string]any{
		"type":    "function_call_output",
		"call_id": "c1",
		"output":  `{"temp":21}`,
	}, input[4])
	assert.Equal(t, map[string]any{
		"type":    "message",
		"role":    "developer",
		"content": []any{map[string]any{"type": "input_text", "text": "Be concise."}},
	}, input[5])
}

func TestOpenAIBackend_GenerationControls(t *testing.T) {
	t.Parallel()
	t.Run("default request omits max_output_tokens, temperature, top_p", func(t *testing.T) {
		fake := &openaiFake{stream: openaiTextStream("completed", "")}
		req := openaiRequest(openaiTestModel)
		req.Tools = nil
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		body := fake.lastPostBody(t)
		assert.NotContains(t, body, "max_output_tokens")
		assert.NotContains(t, body, "temperature")
		assert.NotContains(t, body, "top_p")
		assert.NotContains(t, body, "parallel_tool_calls")
	})

	t.Run("enabled thinking sends reasoning effort without summary", func(t *testing.T) {
		fake := &openaiFake{stream: openaiTextStream("completed", "")}
		req := openaiRequest(openaiTestModel)
		req.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: true}}
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		body := fake.lastPostBody(t)
		assert.Equal(t, map[string]any{"effort": "medium"}, body["reasoning"])
	})

	t.Run("a JSON response format maps to text.format json_schema", func(t *testing.T) {
		fake := &openaiFake{stream: openaiTextStream("completed", "")}
		req := openaiRequest(openaiTestModel)
		req.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: "application/json", JsonSchema: `{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`}
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		body := fake.lastPostBody(t)
		textCfg := body["text"].(map[string]any)
		format := textCfg["format"].(map[string]any)
		assert.Equal(t, "json_schema", format["type"])
		assert.Equal(t, "response", format["name"])
		assert.Equal(t, true, format["strict"])
	})

	for name, mutate := range map[string]func(*models.GenerateRequest){
		"seed":           func(r *models.GenerateRequest) { seed := int32(42); r.Seed = &seed },
		"stop sequences": func(r *models.GenerateRequest) { r.StopSequences = []string{"DONE"} },
		"top_k":          func(r *models.GenerateRequest) { topK := int32(40); r.TopK = &topK },
		"context limit":  func(r *models.GenerateRequest) { limit := int32(8192); r.ContextLimit = &limit },
		"forced tool choice": func(r *models.GenerateRequest) {
			r.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_REQUIRED}
		},
		"non-JSON response format": func(r *models.GenerateRequest) {
			r.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: "text/plain"}
		},
		"thinking include thoughts": func(r *models.GenerateRequest) {
			r.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: true}, IncludeThoughts: true}
		},
		"thinking disabled": func(r *models.GenerateRequest) {
			r.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: false}}
		},
		"thinking level": func(r *models.GenerateRequest) {
			r.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Level{Level: "high"}}
		},
	} {
		t.Run(name+" is refused before the prompt leaves", func(t *testing.T) {
			fake := &openaiFake{stream: openaiTextStream("completed", "")}
			req := openaiRequest(openaiTestModel)
			mutate(&req)
			_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
			require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)
			assert.Zero(t, fake.posts())
		})
	}

	t.Run("max_output_tokens below 16 is refused", func(t *testing.T) {
		fake := &openaiFake{stream: openaiTextStream("completed", "")}
		req := openaiRequest(openaiTestModel)
		req.MaxTokens = 15
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceGenerationOptionsInvalid)
		assert.Zero(t, fake.posts())
	})

	t.Run("model ID that is not a bare ID is refused", func(t *testing.T) {
		fake := &openaiFake{}
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), openaiRequest("../files/x"))
		require.ErrorIs(t, err, constants.ErrInferenceModelRefInvalid)
		assert.Empty(t, fake.requests)
	})

	t.Run("request for another class is refused", func(t *testing.T) {
		req := openaiRequest(openaiTestModel)
		req.ProviderClass = constants.InferenceProviderGemini
		_, err := newFakeOpenAIBackend(t, &openaiFake{}).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceHostedNotAllowed)
	})
}

func TestOpenAIBackend_RejectsInvalidOrReadableReasoningItems(t *testing.T) {
	t.Parallel()
	t.Run("streamed reasoning items with readable text or missing fields fail closed", func(t *testing.T) {
		for name, itemJSON := range map[string]string{
			"non-empty summary": `{"encrypted_content":"opaque","id":"rs_1","summary":[{"text":"thought","type":"summary_text"}],"type":"reasoning"}`,
			"non-empty content": `{"content":[{"text":"thought","type":"reasoning_text"}],"encrypted_content":"opaque","id":"rs_1","summary":[],"type":"reasoning"}`,
			"missing id":        `{"encrypted_content":"opaque","summary":[],"type":"reasoning"}`,
			"empty encrypted":   `{"encrypted_content":"","id":"rs_1","summary":[],"type":"reasoning"}`,
			"missing encrypted": `{"id":"rs_1","summary":[],"type":"reasoning"}`,
			"unknown field":     `{"encrypted_content":"opaque","extra":"leak","id":"rs_1","summary":[],"type":"reasoning"}`,
		} {
			t.Run(name, func(t *testing.T) {
				stream := fmt.Sprintf(
					"event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n"+
						"event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[]}}\r\n\r\n"+
						"event: response.output_item.done\r\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":%s}\r\n\r\n"+
						"event: response.completed\r\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"model\":%q}}\r\n\r\n",
					openaiTestModel, itemJSON, openaiTestModel,
				)
				_, err := newFakeOpenAIBackend(t, &openaiFake{stream: stream}).Generate(testutil.Context(t), openaiRequest(openaiTestModel))
				require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
			})
		}
	})

	t.Run("incoming reasoning items are validated before egress", func(t *testing.T) {
		for name, tc := range map[string]struct {
			provider string
			itemJSON string
			want     error
		}{
			"readable summary":  {constants.InferenceProviderOpenAI, `{"encrypted_content":"opaque","id":"rs_1","summary":[{"text":"thought","type":"summary_text"}],"type":"reasoning"}`, constants.ErrInferenceMessageInvalid},
			"readable content":  {constants.InferenceProviderOpenAI, `{"content":[{"text":"thought","type":"reasoning_text"}],"encrypted_content":"opaque","id":"rs_1","summary":[],"type":"reasoning"}`, constants.ErrInferenceMessageInvalid},
			"missing id":        {constants.InferenceProviderOpenAI, `{"encrypted_content":"opaque","summary":[],"type":"reasoning"}`, constants.ErrInferenceMessageInvalid},
			"missing encrypted": {constants.InferenceProviderOpenAI, `{"id":"rs_1","summary":[],"type":"reasoning"}`, constants.ErrInferenceMessageInvalid},
			"unknown field":     {constants.InferenceProviderOpenAI, `{"encrypted_content":"opaque","extra":"x","id":"rs_1","summary":[],"type":"reasoning"}`, constants.ErrInferenceMessageInvalid},
			"non-canonical":     {constants.InferenceProviderOpenAI, `{"id":"rs_1","encrypted_content":"opaque","type":"reasoning"}`, constants.ErrInferenceJSONNonCanonical},
			"wrong provider":    {constants.InferenceProviderAnthropic, `{"encrypted_content":"opaque","id":"rs_1","summary":[],"type":"reasoning"}`, constants.ErrInferenceMessageInvalid},
		} {
			t.Run(name, func(t *testing.T) {
				fake := &openaiFake{stream: openaiTextStream("completed", "")}
				req := openaiRequest(openaiTestModel)
				req.Messages = append(req.Messages, &operatorv1.InferenceMessage{
					Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT,
					Parts: []*operatorv1.InferenceMessagePart{{
						Part: &operatorv1.InferenceMessagePart_ProviderReasoning{
							ProviderReasoning: &operatorv1.InferenceProviderReasoning{
								ProviderClass: tc.provider,
								ItemJson:      tc.itemJSON,
							},
						},
					}},
				})
				_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), req)
				require.ErrorIs(t, err, tc.want)
				assert.Zero(t, fake.posts())
			})
		}
	})
}

func TestOpenAIBackend_DecodesTextAndFinishReasons(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status, reason, want string
	}{
		"completed":         {"completed", "", "stop"},
		"max_output_tokens": {"incomplete", "max_output_tokens", "length"},
		"content_filter":    {"incomplete", "content_filter", "error"},
		"max_messages":      {"incomplete", "max_messages", "error"},
		"steered":           {"incomplete", "steered", "error"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &openaiFake{stream: openaiTextStream(tc.status, tc.reason)}
			resp, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), openaiRequest(openaiTestModel))
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.FinishReason)
			require.Len(t, resp.Parts, 2)
			assert.Equal(t, "Hello", resp.Parts[0].GetText())
			assert.Equal(t, " there", resp.Parts[1].GetText())
			assert.Equal(t, int32(7), resp.PromptTokens)
			assert.Equal(t, int32(5), resp.CompletionTokens)
			assert.Equal(t, int32(12), resp.TotalTokens)
			require.NotNil(t, resp.ThinkingTokens)
			assert.Equal(t, int32(3), *resp.ThinkingTokens)
			require.NotNil(t, resp.CacheTokens)
			assert.Equal(t, int32(1), *resp.CacheTokens)
			assertHostedTiming(t, resp)
			assert.Equal(t, operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_UNAVAILABLE, models.ClassifyLoadState(resp.LoadDurationNS))
		})
	}
}

func TestOpenAIBackend_StreamFailuresFailClosed(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		stream string
		want   error
	}{
		"error event": {
			"event: error\r\ndata: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"internal\",\"sequence_number\":0}\r\n\r\n",
			constants.ErrInferenceProviderStreamError,
		},
		"response failed event": {
			fmt.Sprintf("event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n"+
				"event: response.failed\r\ndata: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"id\":\"r1\",\"status\":\"failed\",\"model\":%q,\"error\":{\"code\":\"server_error\",\"message\":\"failed\"}}}\r\n\r\n", openaiTestModel, openaiTestModel),
			constants.ErrInferenceProviderStreamError,
		},
		"missing terminal event": {
			fmt.Sprintf("event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n", openaiTestModel),
			constants.ErrInferenceProviderResponseInvalid,
		},
		"missing sequence_number": {
			fmt.Sprintf("event: response.created\r\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n", openaiTestModel),
			constants.ErrInferenceProviderResponseInvalid,
		},
		"duplicate sequence_number": {
			fmt.Sprintf("event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n"+
				"event: response.in_progress\r\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n", openaiTestModel, openaiTestModel),
			constants.ErrInferenceProviderResponseInvalid,
		},
		"decreasing sequence_number": {
			fmt.Sprintf("event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":2,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n"+
				"event: response.in_progress\r\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":1,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n", openaiTestModel, openaiTestModel),
			constants.ErrInferenceProviderResponseInvalid,
		},
		"changed served model": {
			fmt.Sprintf("event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n"+
				"event: response.completed\r\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"model\":\"gpt-other\"}}\r\n\r\n", openaiTestModel),
			constants.ErrInferenceProviderResponseInvalid,
		},
		"events after response.completed": {
			openaiTextStream("completed", "") +
				fmt.Sprintf("event: response.completed\r\ndata: {\"type\":\"response.completed\",\"sequence_number\":9,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"model\":%q}}\r\n\r\n", openaiTestModel),
			constants.ErrInferenceProviderResponseInvalid,
		},
		"unsupported output item type": {
			fmt.Sprintf("event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n"+
				"event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"ws_1\",\"type\":\"web_search_call\"}}\r\n\r\n", openaiTestModel),
			constants.ErrInferenceProviderResponseInvalid,
		},
		"malformed JSON": {
			"data: {not json\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newFakeOpenAIBackend(t, &openaiFake{stream: tc.stream}).Generate(testutil.Context(t), openaiRequest(openaiTestModel))
			require.ErrorIs(t, err, tc.want)
		})
	}

	t.Run("parallel calls when the request forbids them", func(t *testing.T) {
		stream := fmt.Sprintf(
			"event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n"+
				"event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"get_weather\"}}\r\n\r\n"+
				"event: response.output_item.done\r\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"get_weather\",\"arguments\":\"{}\"}}\r\n\r\n"+
				"event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":3,\"output_index\":1,\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"c2\",\"name\":\"get_weather\"}}\r\n\r\n",
			openaiTestModel,
		)
		req := openaiRequest(openaiTestModel)
		parallel := false
		req.ParallelToolCalls = &parallel
		_, err := newFakeOpenAIBackend(t, &openaiFake{stream: stream}).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	})
}

func TestOpenAIBackend_ProviderErrorsDoNotLogContent(t *testing.T) {
	t.Parallel()
	const sensitive = "private-prompt-and-api-key"
	for name, tc := range map[string]struct {
		fake *openaiFake
		want error
	}{
		"HTTP error": {
			&openaiFake{status: http.StatusBadRequest, errorBody: `{"error":{"type":"invalid_request_error","code":"invalid_value","message":"` + sensitive + `","param":"` + sensitive + `"}}`},
			constants.ErrInferenceGenerateFailed,
		},
		"stream error": {
			&openaiFake{stream: `event: error` + "\r\n" + `data: {"type":"error","code":"` + sensitive + `","message":"` + sensitive + `","sequence_number":0}` + "\r\n\r\n"},
			constants.ErrInferenceProviderStreamError,
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := newFakeOpenAIBackend(t, tc.fake)
			logger, sink := testutil.NewBufferLogger(t)
			backend.logger = logger
			_, err := backend.Generate(testutil.Context(t), openaiRequest(openaiTestModel))
			require.ErrorIs(t, err, tc.want)
			assert.NotContains(t, err.Error(), sensitive)
			assert.NotContains(t, sink.String(), sensitive)
			assert.Contains(t, sink.String(), "provider_attempt_id=attempt-1")
		})
	}
}

func TestOpenAIBackend_ProgressAndCapture(t *testing.T) {
	t.Parallel()
	store := &hostedRawResponseRecorder{}
	fake := &openaiFake{stream: openaiToolCallStream(openaiTestModel)}
	backend := newFakeOpenAIBackend(t, fake)
	req := openaiRequest(openaiTestModel)
	req.Stream = true
	req.Scrubber = mustNewScrubbingSvc(t)

	var published []*operatorv1.InferenceProgressEvent
	ctx := WithProgressReporter(WithAttemptStore(testutil.Context(t), store), func(ev *operatorv1.InferenceProgressEvent) error {
		published = append(published, ev)
		return nil
	})
	resp, err := backend.Generate(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 1, store.calls)
	assert.Equal(t, fake.stream, string(store.raw))
	require.NotEmpty(t, published)
	protoRes := resp.ToProtoInferenceResult()
	protoRes.ProviderAttemptId = req.ProviderAttemptID
	require.NoError(t, models.ReconcileInferenceProgress(published, protoRes))
}

func TestOpenAIBackend_RejectsOverflowingTotalTokens(t *testing.T) {
	t.Parallel()
	stream := fmt.Sprintf(
		"event: response.created\r\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":%q}}\r\n\r\n"+
			"event: response.completed\r\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"model\":%q,\"usage\":{\"input_tokens\":%d,\"output_tokens\":1}}}\r\n\r\n",
		openaiTestModel, openaiTestModel, math.MaxInt32,
	)
	resp, err := newFakeOpenAIBackend(t, &openaiFake{stream: stream}).Generate(testutil.Context(t), openaiRequest(openaiTestModel))
	require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	assert.Nil(t, resp)
}

func TestOpenAIBackend_StatusErrorsAreTyped(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"not found":                     {http.StatusNotFound, `{"error":{"code":"model_not_found","message":"no such model"}}`, constants.ErrInferenceModelNotFound},
		"rate limit":                    {http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`, constants.ErrInferenceBackendUnavailable},
		"unauthorized":                  {http.StatusUnauthorized, `{"error":{"type":"invalid_request_error","code":"invalid_api_key"}}`, constants.ErrInferenceBackendUnavailable},
		"server error":                  {http.StatusInternalServerError, `{"error":{"type":"server_error"}}`, constants.ErrInferenceBackendUnavailable},
		"timeout":                       {http.StatusGatewayTimeout, `{"error":{"code":"timeout"}}`, constants.ErrInferenceBackendTimeout},
		"payload too large":             {http.StatusRequestEntityTooLarge, `{}`, constants.ErrInferenceContextOverflow},
		"context length exceeded":       {http.StatusBadRequest, `{"error":{"code":"context_length_exceeded","message":"too long"}}`, constants.ErrInferenceContextOverflow},
		"temperature unsupported param": {http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"unsupported_parameter","param":"temperature","message":"Unsupported parameter: 'temperature'"}}`, constants.ErrInferenceCapabilityUnsupported},
		"top_p unsupported message":     {http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"Unsupported parameter: 'top_p' is not supported with this model."}}`, constants.ErrInferenceCapabilityUnsupported},
		"tools unsupported":             {http.StatusBadRequest, `{"error":{"type":"invalid_request_error","param":"tools","message":"tools unsupported"}}`, constants.ErrInferenceToolsUnsupported},
		"max_output_tokens invalid":     {http.StatusBadRequest, `{"error":{"type":"invalid_request_error","param":"max_output_tokens","message":"must be >= 16"}}`, constants.ErrInferenceGenerationOptionsInvalid},
		"other bad request":             {http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"bad"}}`, constants.ErrInferenceGenerateFailed},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newFakeOpenAIBackend(t, &openaiFake{status: tc.status, errorBody: tc.body}).Generate(testutil.Context(t), openaiRequest(openaiTestModel))
			require.ErrorIs(t, err, tc.want)
		})
	}
	t.Run("unknown model fails at metadata read", func(t *testing.T) {
		fake := &openaiFake{metaCode: http.StatusNotFound}
		_, err := newFakeOpenAIBackend(t, fake).Generate(testutil.Context(t), openaiRequest(openaiTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
		assert.Zero(t, fake.posts())
	})
}

func TestOpenAIBackend_InventoryListsModelsWithIdentityDigests(t *testing.T) {
	t.Parallel()
	fake := &openaiFake{list: fmt.Sprintf(`{"object":"list","data":[`+
		`{"id":%q,"object":"model","created":%d,"owned_by":"openai"},`+
		`{"id":"gpt-4.1-mini-2025-04-14","object":"model","created":1744588800,"owned_by":"openai"},`+
		`{"id":"ft:gpt-4o:org:custom:123","object":"model","created":1744588800,"owned_by":"org"}`+
		`]}`, openaiTestModel, openaiTestCreated)}
	entries, err := newFakeOpenAIBackend(t, fake).ListProviderModelInventory(testutil.Context(t))
	require.NoError(t, err)
	require.Len(t, entries, 2, "fine-tuned IDs failing bare model ID validation are skipped")
	assert.Equal(t, ProviderModelInventoryEntry{
		ProviderClass:          constants.InferenceProviderOpenAI,
		ServedModelTag:         openaiTestModel,
		ModelDigest:            openaiIdentityDigest(t, openaiTestModel),
		ModelFamily:            "gpt",
		Format:                 constants.HostedModelFormat,
		ContextLimit:           0,
		AdvertisedCapabilities: []string{"completion"},
	}, entries[0])
	assert.Equal(t, "gpt-4.1-mini-2025-04-14", entries[1].ServedModelTag)
	assert.Empty(t, fake.requests[0].URL.RawQuery, "OpenAI models list has no pagination query")

	status, err := newFakeOpenAIBackend(t, fake).Status(testutil.Context(t))
	require.NoError(t, err)
	assert.True(t, status.Available)
	assert.Equal(t, []string{openaiTestModel, "gpt-4.1-mini-2025-04-14"}, status.Models)
}

func TestNewHostedBackend_OpenAI(t *testing.T) {
	t.Run("missing key fails closed", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.OpenAIAPIKey), "")
		_, err := NewHostedBackend(constants.InferenceProviderOpenAI, testutil.NewTestLogger())
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
		assert.NotContains(t, err.Error(), openaiTestKey)
	})
	t.Run("key with newline fails closed", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.OpenAIAPIKey), openaiTestKey+"\n")
		_, err := NewHostedBackend(constants.InferenceProviderOpenAI, testutil.NewTestLogger())
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
	})
	t.Run("valid key builds OpenAIBackend against fixed origin", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.OpenAIAPIKey), openaiTestKey)
		backend, err := NewHostedBackend(constants.InferenceProviderOpenAI, testutil.NewTestLogger())
		require.NoError(t, err)
		assert.Equal(t, constants.InferenceProviderOpenAI, backend.ProviderClass())
		assert.Equal(t, openaiOrigin(t), backend.(*OpenAIBackend).origin)
	})
}
