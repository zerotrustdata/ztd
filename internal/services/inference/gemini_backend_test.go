// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"bufio"
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
	geminiTestModel         = "gemini-3.6-flash"
	geminiTestVersion       = "3.6-flash-07-2026"
	geminiTestKey           = "test-gemini-key"
	geminiTestResponseID    = "gwD7apD2G_qcqtsPhaKpsAI"
	geminiCapturedCallID    = "call_8719570727837245486"
	geminiCapturedSignature = "EqACCp0CAWkUfRNi/1NGqaZv5f+F29UcIpE+xrm85Hx19SuymElAb9tmUeW1TKQ4uWRui/XMnpjMQh4sK2g0LFSgqOBrkabw2D+wMGvePojGhPl7KumT0tE8mFwIgw47Pj8UpXWELPvBjtx4PIop85Z88CloRRlpMqabgZM3tuGsYexB5QxAyJpvJpU96ONi4Cd+Zi4xCKpG2mhggzeA+1gxRcbOm/IYRnJSatuk3j9JZ83hnlWY4I2uhPp+bjYSWBYyiJlmH2zTixzveJfXV+GX5XWjaLfg/PwNsAmAH5urH6z8h+V8/5Yd2LGS6Kg7yGsFb+e/1kTneBjvnZwJ7yKsf9yoJjr9Q6KSlamrqRxjHfg9BG+JxBuPoja8E5ukt6x/"
)

// geminiToolCallStream is the streamGenerateContent response the Gemini API
// returned for a get_weather tool call, with servedModel as its modelVersion.
func geminiToolCallStream(servedModel string) string {
	return fmt.Sprintf(`data: {"candidates": [{"content": {"parts": [{"functionCall": {"name": "get_weather","args": {"city": "Paris"},"id": "%s"},"thoughtSignature": "%s"}],"role": "model"},"index": 0}],"usageMetadata": {"promptTokenCount": 55,"candidatesTokenCount": 16,"totalTokenCount": 110,"promptTokensDetails": [{"modality": "TEXT","tokenCount": 55}],"thoughtsTokenCount": 39,"serviceTier": "standard"},"modelVersion": "%s","responseId": "%s"}`+"\r\n\r\n"+
		`data: {"candidates": [{"content": {"parts": [{"text": ""}],"role": "model"},"finishReason": "STOP","index": 0}],"usageMetadata": {"promptTokenCount": 55,"candidatesTokenCount": 16,"totalTokenCount": 110,"promptTokensDetails": [{"modality": "TEXT","tokenCount": 55}],"thoughtsTokenCount": 39,"serviceTier": "standard"},"modelVersion": "%s","responseId": "%s"}`+"\r\n\r\n",
		geminiCapturedCallID, geminiCapturedSignature, servedModel, geminiTestResponseID, servedModel, geminiTestResponseID)
}

// geminiTextStream streams a thought, then text from geminiTestModel, ending
// with finishReason.
func geminiTextStream(finishReason string) string {
	return fmt.Sprintf(`data: {"candidates":[{"content":{"parts":[{"text":"weighing it","thought":true}],"role":"model"},"index":0}],"modelVersion":"%[1]s","responseId":"r1"}`+"\n\n"+
		`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"index":0}],"modelVersion":"%[1]s","responseId":"r1"}`+"\n\n"+
		`data: {"candidates":[{"content":{"parts":[{"text":" there"}],"role":"model"},"finishReason":"%[2]s","index":0}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"thoughtsTokenCount":3,"cachedContentTokenCount":1},"modelVersion":"%[1]s","responseId":"r1"}`+"\n\n",
		geminiTestModel, finishReason)
}

func geminiModelMetadata(model, version string) string {
	return fmt.Sprintf(`{"name":"models/%s","version":"%s","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent","countTokens"],"thinking":true}`, model, version)
}

// geminiFake is an in-process Gemini API: no listener, no network.
type geminiFake struct {
	mu        sync.Mutex
	version   string
	stream    string
	status    int
	errorBody string
	list      string
	metaCode  int
	redirect  string
	requests  []*http.Request
	bodies    [][]byte
}

func (f *geminiFake) RoundTrip(r *http.Request) (*http.Response, error) {
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
	case r.Method == http.MethodGet && path == "/v1beta/models":
		return respond(http.StatusOK, f.list)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1beta/models/"):
		if f.metaCode != 0 {
			return respond(f.metaCode, `{"error":{"code":404}}`)
		}
		model := strings.TrimPrefix(path, "/v1beta/models/")
		return respond(http.StatusOK, geminiModelMetadata(model, f.version))
	case r.Method == http.MethodPost && strings.HasSuffix(path, geminiStreamMethod):
		if f.status != 0 && f.status != http.StatusOK {
			return respond(f.status, f.errorBody)
		}
		return respond(http.StatusOK, f.stream)
	default:
		return respond(http.StatusTeapot, "")
	}
}

func (f *geminiFake) posts() int {
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

func (f *geminiFake) lastPostBody(t *testing.T) map[string]any {
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

func geminiOrigin(t *testing.T) string {
	t.Helper()
	origin, ok := constants.HostedInferenceProviderOrigin(constants.InferenceProviderGemini)
	require.True(t, ok)
	return origin
}

func newFakeGeminiBackend(t *testing.T, fake *geminiFake) *GeminiBackend {
	t.Helper()
	if fake.version == "" {
		fake.version = geminiTestVersion
	}
	backend, err := NewGeminiBackend(geminiOrigin(t), geminiTestKey, testutil.NewTestLogger())
	require.NoError(t, err)
	backend.client.Transport = fake
	return backend
}

func geminiIdentityDigest(t *testing.T, model, version string) string {
	t.Helper()
	digest, err := models.ComputeHostedModelIdentityDigest(constants.InferenceProviderGemini, geminiOrigin(t), model, version)
	require.NoError(t, err)
	return digest
}

func weatherTool() *operatorv1.InferenceToolDeclaration {
	return &operatorv1.InferenceToolDeclaration{
		Name:        "get_weather",
		Description: "Get weather",
		JsonSchema:  `{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}`,
	}
}

func geminiRequest(model string) models.GenerateRequest {
	return models.GenerateRequest{
		ProviderClass:     constants.InferenceProviderGemini,
		Model:             model,
		ProviderAttemptID: "attempt-1",
		Messages: []*operatorv1.InferenceMessage{
			{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "You are terse."}}}},
			{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "What is the weather in Paris?"}}}},
		},
		Tools: []*operatorv1.InferenceToolDeclaration{weatherTool()},
	}
}

func TestGeminiBackend_GenerateDecodesCapturedToolCallStream(t *testing.T) {
	t.Parallel()
	fake := &geminiFake{stream: geminiToolCallStream(geminiTestModel)}
	backend := newFakeGeminiBackend(t, fake)

	resp, err := backend.Generate(testutil.Context(t), geminiRequest(geminiTestModel))
	require.NoError(t, err)

	require.Len(t, resp.Parts, 1)
	call := resp.Parts[0].GetToolCall()
	require.NotNil(t, call)
	assert.Equal(t, geminiCapturedCallID, call.GetCallId())
	assert.Equal(t, "get_weather", call.GetName())
	assert.JSONEq(t, `{"city":"Paris"}`, call.GetArgumentsJson())
	assert.Equal(t, geminiCapturedSignature, call.GetProviderSignature(), "the thought signature must survive decoding")

	assert.Equal(t, "stop", resp.FinishReason)
	assert.True(t, resp.UsageReported)
	assert.Equal(t, int32(55), resp.PromptTokens)
	assert.Equal(t, int32(55), resp.CompletionTokens, "completion counts candidate plus thought tokens")
	assert.Equal(t, int32(110), resp.TotalTokens)
	require.NotNil(t, resp.ThinkingTokens)
	assert.Equal(t, int32(39), *resp.ThinkingTokens)
	assert.Equal(t, int32(1048576), resp.ContextWindow)
	assert.Equal(t, geminiTestModel, resp.Model)
	assert.Empty(t, resp.ServedModelDigest, "no digest was requested")
	assert.True(t, models.IsSHA256Hex(resp.NormalizedRequestHash))
	assert.True(t, models.IsSHA256Hex(resp.OutputHash))

	egress := resp.HostedEgress
	require.NotNil(t, egress)
	assert.Equal(t, geminiOrigin(t), egress.GetProviderOrigin())
	assert.Equal(t, constants.InferenceProviderGemini, egress.GetProviderClass())
	assert.Equal(t, geminiTestModel, egress.GetRequestedModel())
	assert.Equal(t, geminiTestModel, egress.GetModelVersion())
	assert.Equal(t, geminiTestVersion, egress.GetProviderModelVersion())
	assert.Equal(t, geminiIdentityDigest(t, geminiTestModel, geminiTestVersion), egress.GetModelIdentityDigest())
	assert.Equal(t, geminiTestResponseID, egress.GetProviderResponseId())
	assert.Equal(t, models.SHA256Hex([]byte(fake.stream)), egress.GetRawResponseDigest())
	assert.Equal(t, uint64(len(fake.stream)), egress.GetRawResponseSizeBytes())
	assert.Equal(t, resp.NormalizedRequestHash, egress.GetNormalizedRequestHash())
	assert.True(t, egress.GetExplicitConsent())
	require.NoError(t, models.ValidateHostedEgress(constants.InferenceProviderGemini, geminiTestModel, "", resp.ToProtoInferenceResult()))
}

func TestGeminiBackend_KeyTravelsOnlyInTheHeader(t *testing.T) {
	t.Parallel()
	fake := &geminiFake{stream: geminiToolCallStream(geminiTestModel)}
	backend := newFakeGeminiBackend(t, fake)
	_, err := backend.Generate(testutil.Context(t), geminiRequest(geminiTestModel))
	require.NoError(t, err)

	require.Len(t, fake.requests, 2, "one metadata read and one generation")
	for _, r := range fake.requests {
		assert.Equal(t, geminiTestKey, r.Header.Get(constants.HeaderGoogAPIKey))
		assert.NotContains(t, r.URL.String(), geminiTestKey)
		assert.NotContains(t, r.URL.RawQuery, "key=")
		assert.Equal(t, "generativelanguage.googleapis.com", r.URL.Host)
	}
	post := fake.requests[1]
	assert.Equal(t, "/v1beta/models/"+geminiTestModel+geminiStreamMethod, post.URL.Path)
	assert.Equal(t, "alt=sse", post.URL.RawQuery)
}

func TestGeminiBackend_RedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	fake := &geminiFake{redirect: "https://elsewhere.example/steal"}
	backend := newFakeGeminiBackend(t, fake)
	_, err := backend.Generate(testutil.Context(t), geminiRequest(geminiTestModel))
	require.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
	require.Len(t, fake.requests, 1, "the redirect target must never be requested")
	assert.Equal(t, "generativelanguage.googleapis.com", fake.requests[0].URL.Host)
}

func TestNewGeminiBackend_RejectsInvalidCredentialTargets(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{
		"http://generativelanguage.googleapis.com",
		"https://proxy.example",
		"https://user:password@generativelanguage.googleapis.com",
		"https://generativelanguage.googleapis.com/prefix",
		"https://generativelanguage.googleapis.com?key=secret",
		"https://generativelanguage.googleapis.com#fragment",
	} {
		t.Run(origin, func(t *testing.T) {
			backend, err := NewGeminiBackend(origin, geminiTestKey, testutil.NewTestLogger())
			require.ErrorIs(t, err, constants.ErrInferenceEndpointInvalid)
			assert.Nil(t, backend)
		})
	}
	for _, key := range []string{" ", "abc\ndef", "abc\rdef"} {
		t.Run("invalid key "+fmt.Sprintf("%q", key), func(t *testing.T) {
			backend, err := NewGeminiBackend(geminiOrigin(t), key, testutil.NewTestLogger())
			require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
			assert.Nil(t, backend)
		})
	}
}

func TestGeminiBackend_PinnedDigestBindsModelVersionAndServedModel(t *testing.T) {
	t.Parallel()
	pinned := geminiIdentityDigest(t, geminiTestModel, geminiTestVersion)

	t.Run("matching version serves and reports the digest", func(t *testing.T) {
		fake := &geminiFake{stream: geminiToolCallStream(geminiTestModel)}
		req := geminiRequest(geminiTestModel)
		req.ModelDigest = pinned
		resp, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		assert.Equal(t, pinned, resp.ServedModelDigest)
		result := resp.ToProtoInferenceResult()
		require.NoError(t, models.ValidateHostedEgress(constants.InferenceProviderGemini, geminiTestModel, pinned, result))
	})

	t.Run("a re-versioned model fails before generation", func(t *testing.T) {
		fake := &geminiFake{version: "3.6-flash-09-2026", stream: geminiToolCallStream(geminiTestModel)}
		req := geminiRequest(geminiTestModel)
		req.ModelDigest = pinned
		_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
		assert.Zero(t, fake.posts(), "no prompt may leave once the identity changed")
	})

	t.Run("a provider reroute fails closed", func(t *testing.T) {
		fake := &geminiFake{stream: geminiToolCallStream("gemini-3.7-flash")}
		req := geminiRequest(geminiTestModel)
		req.ModelDigest = pinned
		_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
	})

	t.Run("an unpinned reroute is recorded, not hidden", func(t *testing.T) {
		fake := &geminiFake{stream: geminiToolCallStream("gemini-3.7-flash")}
		resp, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), geminiRequest(geminiTestModel))
		require.NoError(t, err)
		assert.Equal(t, geminiTestModel, resp.Model)
		assert.Equal(t, "gemini-3.7-flash", resp.HostedEgress.GetModelVersion())
	})
}

func TestGeminiBackend_GenerateRequestMapping(t *testing.T) {
	t.Parallel()
	fake := &geminiFake{stream: geminiTextStream("STOP")}
	req := geminiRequest(geminiTestModel)
	req.Tools = append(req.Tools, &operatorv1.InferenceToolDeclaration{Name: "other", JsonSchema: `{"type":"object"}`})
	req.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO, AllowedToolNames: []string{"get_weather"}}
	req.Temperature = 0.2
	req.Messages = append(req.Messages,
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c1", Name: "get_weather", ArgumentsJson: `{"city":"Paris"}`, ProviderSignature: geminiCapturedSignature}}},
			{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c2", Name: "get_weather", ArgumentsJson: `{"city":"Lyon"}`}}},
		}},
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{CallId: "c1", Name: "get_weather", ResultJson: `{"temp":21}`}}},
		}},
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{CallId: "c2", Name: "get_weather", ResultJson: `"sunny"`}}},
		}},
	)
	_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), req)
	require.NoError(t, err)

	body := fake.lastPostBody(t)
	assert.Equal(t, map[string]any{"parts": []any{map[string]any{"text": "You are terse."}}}, body["systemInstruction"])
	contents := body["contents"].([]any)
	require.Len(t, contents, 3, "user, model, and one user turn holding both tool results")
	model := contents[1].(map[string]any)
	assert.Equal(t, "model", model["role"])
	calls := model["parts"].([]any)
	require.Len(t, calls, 2)
	first := calls[0].(map[string]any)
	assert.Equal(t, geminiCapturedSignature, first["thoughtSignature"], "the signature returns verbatim on the call it was issued with")
	assert.Equal(t, map[string]any{"id": "c1", "name": "get_weather", "args": map[string]any{"city": "Paris"}}, first["functionCall"])
	assert.NotContains(t, calls[1].(map[string]any), "thoughtSignature")
	results := contents[2].(map[string]any)
	assert.Equal(t, "user", results["role"])
	responses := results["parts"].([]any)
	require.Len(t, responses, 2)
	assert.Equal(t, map[string]any{"id": "c1", "name": "get_weather", "response": map[string]any{"output": map[string]any{"temp": float64(21)}}}, responses[0].(map[string]any)["functionResponse"])
	assert.Equal(t, map[string]any{"output": "sunny"}, responses[1].(map[string]any)["functionResponse"].(map[string]any)["response"])

	tools := body["tools"].([]any)
	declarations := tools[0].(map[string]any)["functionDeclarations"].([]any)
	require.Len(t, declarations, 1, "the allowed-name list narrows the declarations")
	assert.Equal(t, "get_weather", declarations[0].(map[string]any)["name"])
	assert.Equal(t, map[string]any{"functionCallingConfig": map[string]any{"mode": "AUTO"}}, body["toolConfig"])
	config := body["generationConfig"].(map[string]any)
	assert.InDelta(t, 0.2, config["temperature"], 1e-6)
	assert.NotContains(t, config, "maxOutputTokens", "no output cap unless the request sets one")
	assert.Equal(t, map[string]any{"includeThoughts": true}, config["thinkingConfig"])
}

func TestGeminiBackend_GenerationControls(t *testing.T) {
	t.Parallel()
	t.Run("disabled thinking sends a zero budget", func(t *testing.T) {
		fake := &geminiFake{stream: geminiTextStream("STOP")}
		req := geminiRequest(geminiTestModel)
		req.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: false}}
		_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		config := fake.lastPostBody(t)["generationConfig"].(map[string]any)
		assert.Equal(t, map[string]any{"thinkingBudget": float64(0)}, config["thinkingConfig"])
	})
	t.Run("a JSON response format maps to a response schema", func(t *testing.T) {
		fake := &geminiFake{stream: geminiTextStream("STOP")}
		req := geminiRequest(geminiTestModel)
		req.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: "application/json", JsonSchema: `{"type":"object"}`}
		_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		config := fake.lastPostBody(t)["generationConfig"].(map[string]any)
		assert.Equal(t, "application/json", config["responseMimeType"])
		assert.Equal(t, map[string]any{"type": "object"}, config["responseJsonSchema"])
	})
	for name, mutate := range map[string]func(*models.GenerateRequest){
		"a context limit cannot be applied": func(r *models.GenerateRequest) { limit := int32(8192); r.ContextLimit = &limit },
		"a forced tool choice": func(r *models.GenerateRequest) {
			r.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_REQUIRED}
		},
		"a non-JSON response format": func(r *models.GenerateRequest) {
			r.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: "text/plain"}
		},
	} {
		t.Run(name+" is refused before the prompt leaves", func(t *testing.T) {
			fake := &geminiFake{stream: geminiTextStream("STOP")}
			req := geminiRequest(geminiTestModel)
			mutate(&req)
			_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), req)
			require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)
			assert.Zero(t, fake.posts())
		})
	}
	t.Run("a model ID that is not a bare ID is refused", func(t *testing.T) {
		fake := &geminiFake{}
		_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), geminiRequest("../files/x"))
		require.ErrorIs(t, err, constants.ErrInferenceModelRefInvalid)
		assert.Empty(t, fake.requests)
	})
	t.Run("a request for another class is refused", func(t *testing.T) {
		req := geminiRequest(geminiTestModel)
		req.ProviderClass = ""
		_, err := newFakeGeminiBackend(t, &geminiFake{}).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceHostedNotAllowed)
	})
}

func TestGeminiBackend_DecodesThoughtsTextAndFinishReasons(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{"STOP": "stop", "MAX_TOKENS": "length", "SAFETY": "error", "MALFORMED_FUNCTION_CALL": "error"} {
		t.Run(reason, func(t *testing.T) {
			fake := &geminiFake{stream: geminiTextStream(reason)}
			resp, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), geminiRequest(geminiTestModel))
			require.NoError(t, err)
			assert.Equal(t, want, resp.FinishReason)
			require.Len(t, resp.Parts, 3)
			assert.Equal(t, "weighing it", resp.Parts[0].GetThinking())
			assert.Equal(t, "Hello", resp.Parts[1].GetText())
			assert.Equal(t, " there", resp.Parts[2].GetText())
			assert.Equal(t, int32(7), resp.PromptTokens)
			assert.Equal(t, int32(5), resp.CompletionTokens)
			require.NotNil(t, resp.CacheTokens)
			assert.Equal(t, int32(1), *resp.CacheTokens)
			assertHostedTiming(t, resp)
			assert.Equal(t, operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_UNAVAILABLE, models.ClassifyLoadState(resp.LoadDurationNS), "a hosted provider reports no load")
		})
	}
}

func TestGeminiBackend_StreamFailuresFailClosed(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		stream string
		want   error
	}{
		"a transient error event": {`data: {"error":{"code":503,"message":"overloaded","status":"UNAVAILABLE"}}` + "\n\n", constants.ErrInferenceBackendTransient},
		"an error event":          {`data: {"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}` + "\n\n", constants.ErrInferenceProviderStreamError},
		"a blocked prompt":        {`data: {"promptFeedback":{"blockReason":"SAFETY"},"modelVersion":"gemini-3.6-flash"}` + "\n\n", constants.ErrInferenceGenerateFailed},
		"no finish reason":        {`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]},"index":0}],"modelVersion":"gemini-3.6-flash"}` + "\n\n", constants.ErrInferenceProviderResponseInvalid},
		"no model version":        {`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP","index":0}]}` + "\n\n", constants.ErrInferenceProviderResponseInvalid},
		"two candidates":          {`data: {"candidates":[{"index":0},{"index":1}],"modelVersion":"gemini-3.6-flash"}` + "\n\n", constants.ErrInferenceProviderResponseInvalid},
		"a changed served model":  {`data: {"candidates":[{"content":{"parts":[{"text":"a"}]},"index":0}],"modelVersion":"gemini-3.6-flash"}` + "\n\n" + `data: {"candidates":[{"content":{"parts":[{"text":"b"}]},"finishReason":"STOP","index":0}],"modelVersion":"gemini-x"}` + "\n\n", constants.ErrInferenceProviderResponseInvalid},
		"parts after the finish":  {`data: {"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"STOP","index":0}],"modelVersion":"gemini-3.6-flash"}` + "\n\n" + `data: {"candidates":[{"content":{"parts":[{"text":"b"}]},"index":0}],"modelVersion":"gemini-3.6-flash"}` + "\n\n", constants.ErrInferenceProviderResponseInvalid},
		"a contradictory finish":  {geminiTextStream("STOP") + `data: {"candidates":[{"finishReason":"SAFETY","index":0}],"modelVersion":"gemini-3.6-flash"}` + "\n\n", constants.ErrInferenceProviderResponseInvalid},
		"an inline data part":     {`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"finishReason":"STOP","index":0}],"modelVersion":"gemini-3.6-flash"}` + "\n\n", constants.ErrInferenceProviderResponseInvalid},
		"malformed JSON":          {"data: {not json\n\n", constants.ErrInferenceProviderResponseInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newFakeGeminiBackend(t, &geminiFake{stream: tc.stream}).Generate(testutil.Context(t), geminiRequest(geminiTestModel))
			require.ErrorIs(t, err, tc.want)
		})
	}
	t.Run("parallel calls when the request forbids them", func(t *testing.T) {
		stream := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"a","args":{}}},{"functionCall":{"name":"b","args":{}}}]},"finishReason":"STOP","index":0}],"modelVersion":"gemini-3.6-flash"}` + "\n\n"
		req := geminiRequest(geminiTestModel)
		parallel := false
		req.ParallelToolCalls = &parallel
		_, err := newFakeGeminiBackend(t, &geminiFake{stream: stream}).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	})
}

func TestGeminiBackend_ProviderErrorsDoNotLogContent(t *testing.T) {
	t.Parallel()
	const sensitive = "private-prompt-and-api-key"
	for name, tc := range map[string]struct {
		fake *geminiFake
		want error
	}{
		"HTTP error": {
			&geminiFake{status: http.StatusBadRequest, errorBody: `{"error":{"code":400,"message":"` + sensitive + `","status":"` + sensitive + `"}}`},
			constants.ErrInferenceGenerateFailed,
		},
		"stream error": {
			&geminiFake{stream: `data: {"error":{"code":500,"message":"` + sensitive + `","status":"` + sensitive + `"}}` + "\n\n"},
			constants.ErrInferenceBackendTransient,
		},
		"blocked prompt": {
			&geminiFake{stream: `data: {"promptFeedback":{"blockReason":"` + sensitive + `"}}` + "\n\n"},
			constants.ErrInferenceGenerateFailed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := newFakeGeminiBackend(t, tc.fake)
			logger, sink := testutil.NewBufferLogger(t)
			backend.logger = logger
			_, err := backend.Generate(testutil.Context(t), geminiRequest(geminiTestModel))
			require.ErrorIs(t, err, tc.want)
			assert.NotContains(t, err.Error(), sensitive)
			assert.NotContains(t, sink.String(), sensitive)
			assert.Contains(t, sink.String(), "provider_attempt_id=attempt-1", "failures remain diagnosable by attempt ID")
		})
	}
}

func TestGeminiBackend_ProgressFailuresDoNotLogContent(t *testing.T) {
	t.Parallel()
	const sensitive = "private-progress-content"
	backend := newFakeGeminiBackend(t, &geminiFake{stream: geminiTextStream("STOP")})
	logger, sink := testutil.NewBufferLogger(t)
	backend.logger = logger
	req := geminiRequest(geminiTestModel)
	req.Stream = true
	req.Scrubber = mustNewScrubbingSvc(t)
	ctx := WithProgressReporter(testutil.Context(t), func(*operatorv1.InferenceProgressEvent) error {
		return fmt.Errorf("delivery failed: %s", sensitive)
	})
	_, err := backend.Generate(ctx, req)
	require.NoError(t, err, "progress delivery is best effort")
	assert.Contains(t, sink.String(), "Inference progress delivery failed")
	assert.NotContains(t, sink.String(), sensitive)
}

func TestGeminiBackend_RejectsOverflowingTotalTokens(t *testing.T) {
	t.Parallel()
	stream := geminiTextStream("STOP") + fmt.Sprintf(`data: {"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":1}}`+"\n\n", math.MaxInt32)
	response, err := newFakeGeminiBackend(t, &geminiFake{stream: stream}).Generate(testutil.Context(t), geminiRequest(geminiTestModel))
	require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	assert.Nil(t, response, "overflow must fail before a negative total can be signed")
}

func TestGeminiBackend_StatusErrorsAreTyped(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"unknown model":          {http.StatusNotFound, `{"error":{"code":404,"status":"NOT_FOUND"}}`, constants.ErrInferenceModelNotFound},
		"rate limit":             {http.StatusTooManyRequests, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`, constants.ErrInferenceBackendTransient},
		"overloaded":             {http.StatusServiceUnavailable, `{"error":{"code":503,"status":"UNAVAILABLE"}}`, constants.ErrInferenceBackendTransient},
		"internal error":         {http.StatusInternalServerError, `{"error":{"code":500,"status":"INTERNAL"}}`, constants.ErrInferenceBackendTransient},
		"rejected key":           {http.StatusForbidden, `{"error":{"code":403,"status":"PERMISSION_DENIED"}}`, constants.ErrInferenceBackendUnavailable},
		"unauthenticated":        {http.StatusUnauthorized, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`, constants.ErrInferenceBackendUnavailable},
		"not implemented":        {http.StatusNotImplemented, `{"error":{"code":501}}`, constants.ErrInferenceBackendUnavailable},
		"provider deadline":      {http.StatusGatewayTimeout, `{"error":{"code":504}}`, constants.ErrInferenceBackendTimeout},
		"prompt over the window": {http.StatusBadRequest, `{"error":{"code":400,"message":"The input token count (2000000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`, constants.ErrInferenceContextOverflow},
		"other bad request":      {http.StatusBadRequest, `{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`, constants.ErrInferenceGenerateFailed},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newFakeGeminiBackend(t, &geminiFake{status: tc.status, errorBody: tc.body}).Generate(testutil.Context(t), geminiRequest(geminiTestModel))
			require.ErrorIs(t, err, tc.want)
		})
	}
	t.Run("an unknown model fails at the metadata read", func(t *testing.T) {
		fake := &geminiFake{metaCode: http.StatusNotFound}
		_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), geminiRequest(geminiTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
		assert.Zero(t, fake.posts())
	})
	t.Run("a rate-limited metadata read is transient", func(t *testing.T) {
		fake := &geminiFake{metaCode: http.StatusTooManyRequests}
		_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), geminiRequest(geminiTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceBackendTransient)
		assert.Zero(t, fake.posts())
	})
	t.Run("a forbidden metadata read is unavailable", func(t *testing.T) {
		fake := &geminiFake{metaCode: http.StatusForbidden}
		_, err := newFakeGeminiBackend(t, fake).Generate(testutil.Context(t), geminiRequest(geminiTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
	})
}

func TestGeminiBackend_InventoryListsGenerationModelsWithIdentityDigests(t *testing.T) {
	t.Parallel()
	fake := &geminiFake{list: `{"models":[` +
		`{"name":"models/gemini-3.6-flash","version":"3.6-flash-07-2026","inputTokenLimit":1048576,"supportedGenerationMethods":["generateContent"],"thinking":true},` +
		`{"name":"models/gemma-4-31b-it","version":"001","inputTokenLimit":262144,"supportedGenerationMethods":["generateContent"]},` +
		`{"name":"models/text-embedding-004","version":"004","inputTokenLimit":2048,"supportedGenerationMethods":["embedContent"]}]}`}
	entries, err := newFakeGeminiBackend(t, fake).ListProviderModelInventory(testutil.Context(t))
	require.NoError(t, err)
	require.Len(t, entries, 2, "a model that cannot generate content is not inventory")
	assert.Equal(t, ProviderModelInventoryEntry{
		ProviderClass:          constants.InferenceProviderGemini,
		ServedModelTag:         geminiTestModel,
		ModelDigest:            geminiIdentityDigest(t, geminiTestModel, geminiTestVersion),
		ModelFamily:            "gemini",
		Format:                 constants.HostedModelFormat,
		ContextLimit:           1048576,
		AdvertisedCapabilities: []string{"completion", "thinking"},
	}, entries[0])
	assert.Equal(t, "gemma", entries[1].ModelFamily)
	assert.Equal(t, []string{"completion"}, entries[1].AdvertisedCapabilities)
	for _, r := range fake.requests {
		assert.Equal(t, "pageSize=1000", r.URL.RawQuery)
	}
}

func TestNewHostedBackend_RequiresKeyAndClient(t *testing.T) {
	t.Run("a missing key fails closed", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.GeminiAPIKey), "")
		_, err := NewHostedBackend(constants.InferenceProviderGemini, testutil.NewTestLogger())
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
		assert.NotContains(t, err.Error(), geminiTestKey)
	})
	t.Run("a key split across lines fails closed", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.GeminiAPIKey), "abc\ndef")
		_, err := NewHostedBackend(constants.InferenceProviderGemini, testutil.NewTestLogger())
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
	})
	t.Run("a trailing newline in a key fails closed", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.GeminiAPIKey), geminiTestKey+"\n")
		_, err := NewHostedBackend(constants.InferenceProviderGemini, testutil.NewTestLogger())
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
	})
	t.Run("gemini builds against its fixed origin", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.GeminiAPIKey), geminiTestKey)
		backend, err := NewHostedBackend(constants.InferenceProviderGemini, testutil.NewTestLogger())
		require.NoError(t, err)
		assert.Equal(t, constants.InferenceProviderGemini, backend.ProviderClass())
		assert.Equal(t, geminiOrigin(t), backend.(*GeminiBackend).origin)
	})
	t.Run("a local class is not hosted", func(t *testing.T) {
		_, err := NewHostedBackend(constants.InferenceProviderOllama, testutil.NewTestLogger())
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderInvalid)
	})
}

func TestNextSSEData_JoinsDataLinesAndSkipsComments(t *testing.T) {
	t.Parallel()
	stream := ": keepalive\r\nevent: message\r\ndata: {\"a\":\r\ndata: 1}\r\n\r\ndata: last"
	reader := bufio.NewReader(strings.NewReader(stream))
	data, consumed, err := nextSSEData(reader)
	require.NoError(t, err)
	assert.Equal(t, "{\"a\":\n1}", string(data))
	assert.Equal(t, int64(len(stream)-len("data: last")), consumed)
	data, _, err = nextSSEData(reader)
	require.NoError(t, err)
	assert.Equal(t, "last", string(data))
	_, _, err = nextSSEData(reader)
	require.ErrorIs(t, err, io.EOF)
}

func TestStreamSegmenter_ToolCallKeepsProviderSignature(t *testing.T) {
	t.Parallel()
	segmenter := &streamSegmenter{scrubber: mustNewScrubbingSvc(t)}
	part := segmenter.toolCallPart(testutil.Context(t), &operatorv1.InferenceToolCall{CallId: "c1", Name: "n", ArgumentsJson: "{}", ProviderSignature: geminiCapturedSignature})
	assert.Equal(t, geminiCapturedSignature, part.GetToolCall().GetProviderSignature())
}
