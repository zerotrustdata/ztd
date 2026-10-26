// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newUnitLlamaCppBackend(t *testing.T) *LlamaCppBackend {
	t.Helper()
	// The model root is never read by the request mapping and stream decoding under test.
	backend, err := NewLlamaCppBackend("http://127.0.0.1:1", "unread-model-root", testutil.NewTestLogger())
	require.NoError(t, err)
	return backend
}

func TestNewLlamaCppBackend_RequiresEndpointAndModelRoot(t *testing.T) {
	_, err := NewLlamaCppBackend("not-a-url", "/models", testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrInferenceEndpointInvalid)
	_, err = NewLlamaCppBackend("http://127.0.0.1:8080", "", testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrInferenceLlamaCppModelRootRequired)
}

func TestLlamaCppUnloadAfterRequest(t *testing.T) {
	for keepAlive, want := range map[string]bool{"": false, "-1": false, "0": true} {
		unload, err := llamaCppUnloadAfterRequest(keepAlive)
		require.NoError(t, err, keepAlive)
		assert.Equal(t, want, unload, keepAlive)
	}
	for _, keepAlive := range []string{"5m", "30", "-2"} {
		_, err := llamaCppUnloadAfterRequest(keepAlive)
		require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported, keepAlive)
	}
}

func TestLlamaCppChatRequestFor_MapsGenerationOptions(t *testing.T) {
	topK := int32(40)
	seed := int32(7)
	parallel := false
	req := models.GenerateRequest{
		Model:             "qwen3-8b",
		Messages:          textInferenceMessages("hello"),
		Temperature:       0.2,
		TopK:              &topK,
		Seed:              &seed,
		StopSequences:     []string{"</s>"},
		ParallelToolCalls: &parallel,
		Thinking:          &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: false}},
		ResponseFormat: &operatorv1.InferenceResponseFormat{
			MediaType:  inferenceJSONMediaType,
			JsonSchema: `{"type":"object"}`,
		},
	}

	chatReq, err := llamaCppChatRequestFor(req)
	require.NoError(t, err)
	body, err := json.Marshal(chatReq)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"model":"qwen3-8b",
		"messages":[{"role":"user","content":"hello"}],
		"parallel_tool_calls":false,
		"stream":true,
		"stream_options":{"include_usage":true},
		"temperature":0.2,
		"max_tokens":`+jsonInt(resolveOutputTokenLimit(0))+`,
		"top_k":40,
		"seed":7,
		"stop":["</s>"],
		"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}},
		"chat_template_kwargs":{"enable_thinking":false}
	}`, string(body))
}

func TestLlamaCppChatRequestFor_RejectsUnsupportedOptions(t *testing.T) {
	base := models.GenerateRequest{Model: "m", Messages: textInferenceMessages("hi")}

	level := base
	level.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Level{Level: "high"}}
	_, err := llamaCppChatRequestFor(level)
	require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)

	format := base
	format.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: "text/plain"}
	_, err = llamaCppChatRequestFor(format)
	require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)

	empty := base
	empty.Messages = nil
	_, err = llamaCppChatRequestFor(empty)
	require.NoError(t, err, "an empty conversation is the provider's to reject")
}

func TestLlamaCppMessages_EncodeToolTurnsInOpenAIShape(t *testing.T) {
	messages := llamaCppMessages([]ollamaChatMessage{
		{Role: "assistant", ToolCalls: []ollamaToolCall{{ID: "call-1", Function: ollamaToolFunction{Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}}}},
		{Role: "tool", Content: "result", ToolName: "lookup", ToolCallID: "call-1"},
	})
	body, err := json.Marshal(messages)
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
		{"role":"tool","content":"result","tool_call_id":"call-1","name":"lookup"}
	]`, string(body))
}

func TestLlamaCppGenerateStatusError_Classifies(t *testing.T) {
	backend := newUnitLlamaCppBackend(t)
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusRequestTimeout, ``, constants.ErrInferenceBackendTimeout},
		{http.StatusBadRequest, `{"error":{"type":"exceed_context_size_error","message":"too long"}}`, constants.ErrInferenceContextOverflow},
		{http.StatusNotFound, `{}`, constants.ErrInferenceModelNotFound},
		{http.StatusBadRequest, `{"error":{"message":"model not found"}}`, constants.ErrInferenceModelNotFound},
		{http.StatusBadRequest, `{"error":{"message":"tools param requires --jinja flag"}}`, constants.ErrInferenceToolsUnsupported},
		{http.StatusInternalServerError, `oops`, constants.ErrInferenceGenerateFailed},
	}
	for _, tc := range cases {
		err := backend.generateStatusError(tc.status, []byte(tc.body), "m")
		require.ErrorIs(t, err, tc.want, "%d %s", tc.status, tc.body)
	}
}

func llamaCppSSE(events ...string) string {
	var out strings.Builder
	for _, event := range events {
		out.WriteString("data: " + event + "\n\n")
	}
	return out.String()
}

func decodeLlamaCppTestStream(t *testing.T, body string, allowParallel bool) (llamaCppStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
	t.Helper()
	backend := newUnitLlamaCppBackend(t)
	arrival := func(offset int64) *int64 { return &offset }
	return backend.decodeChatStream(t.Context(), strings.NewReader(body), arrival, "m", allowParallel, "attempt", nil)
}

func TestLlamaCppDecodeChatStream_AssemblesTextThinkingAndToolCalls(t *testing.T) {
	body := llamaCppSSE(
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"model":"m","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2}},"timings":{"prompt_ms":1.5,"predicted_ms":2}}`,
		`[DONE]`,
	)

	result, parts, ttft, err := decodeLlamaCppTestStream(t, body, false)
	require.NoError(t, err)
	assert.Equal(t, "tool_calls", result.finishReason)
	require.NotNil(t, ttft)
	require.Len(t, parts, 3)
	assert.Equal(t, "think", parts[0].GetThinking())
	assert.Equal(t, "hi", parts[1].GetText())
	call := parts[2].GetToolCall()
	require.NotNil(t, call)
	assert.Equal(t, "c1", call.GetCallId())
	assert.Equal(t, "lookup", call.GetName())
	assert.JSONEq(t, `{"q":"x"}`, call.GetArgumentsJson())

	prompt, completion, cache, reported, err := llamaCppUsageCounts(result.usage, result.timings)
	require.NoError(t, err)
	assert.True(t, reported)
	assert.Equal(t, int32(5), prompt)
	assert.Equal(t, int32(3), completion)
	require.NotNil(t, cache)
	assert.Equal(t, int32(2), *cache)
	promptNS, generationNS, err := llamaCppDurations(result.timings)
	require.NoError(t, err)
	assert.Equal(t, int64(1_500_000), *promptNS)
	assert.Equal(t, int64(2_000_000), *generationNS)
}

func TestLlamaCppDecodeChatStream_FailsClosed(t *testing.T) {
	text := `{"model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`
	call := func(index int) string {
		return `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":` + jsonInt(int32(index)) + `,"id":"c","function":{"name":"f","arguments":"{}"}}]}}]}`
	}
	cases := map[string]struct {
		body          string
		allowParallel bool
		want          error
	}{
		"missing done":          {body: llamaCppSSE(text), want: constants.ErrInferenceProviderResponseInvalid},
		"missing finish reason": {body: llamaCppSSE(`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`, `[DONE]`), want: constants.ErrInferenceProviderResponseInvalid},
		"event after done":      {body: llamaCppSSE(text, `[DONE]`, text), want: constants.ErrInferenceProviderResponseInvalid},
		"served model differs":  {body: llamaCppSSE(strings.Replace(text, `"model":"m"`, `"model":"other"`, 1), `[DONE]`), want: constants.ErrInferenceProviderResponseInvalid},
		"second choice":         {body: llamaCppSSE(`{"choices":[{"index":1,"delta":{"content":"x"}}]}`, `[DONE]`), want: constants.ErrInferenceProviderResponseInvalid},
		"malformed chunk":       {body: llamaCppSSE(`{`), want: constants.ErrInferenceProviderResponseInvalid},
		"unknown field":         {body: "event: ping\n\n", want: constants.ErrInferenceProviderResponseInvalid},
		"parallel tool calls":   {body: llamaCppSSE(call(0), call(1), `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`), want: constants.ErrInferenceProviderResponseInvalid},
		"invalid tool args":     {body: llamaCppSSE(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"f","arguments":"{"}}]},"finish_reason":"tool_calls"}]}`, `[DONE]`), allowParallel: true, want: constants.ErrInferenceProviderResponseInvalid},
		"stream overflow":       {body: llamaCppSSE(`{"error":{"code":400,"type":"exceed_context_size_error","message":"x"}}`), want: constants.ErrInferenceContextOverflow},
		"stream error event":    {body: "error: {\"code\":500,\"type\":\"server_error\",\"message\":\"x\"}\n\n", want: constants.ErrInferenceGenerateFailed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := decodeLlamaCppTestStream(t, tc.body, tc.allowParallel)
			require.ErrorIs(t, err, tc.want)
		})
	}

	_, parts, _, err := decodeLlamaCppTestStream(t, llamaCppSSE(call(0), call(1), `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`), true)
	require.NoError(t, err, "parallel tool calls are accepted when allowed")
	assert.Len(t, parts, 2)
}

func TestLlamaCppUsageCounts_ValidatesReportedUsage(t *testing.T) {
	cacheN := int32(4)
	_, _, cache, reported, err := llamaCppUsageCounts(nil, &llamaCppTimings{CacheN: &cacheN})
	require.NoError(t, err)
	assert.False(t, reported)
	assert.Nil(t, cache)

	prompt, completion := int32(3), int32(1)
	_, _, cache, reported, err = llamaCppUsageCounts(&llamaCppUsage{PromptTokens: &prompt, CompletionTokens: &completion}, &llamaCppTimings{CacheN: &cacheN})
	require.NoError(t, err)
	assert.True(t, reported)
	require.NotNil(t, cache)
	assert.Equal(t, int32(4), *cache, "timings cache_n is the fallback cache count")

	negative := int32(-1)
	for _, usage := range []*llamaCppUsage{
		{PromptTokens: &prompt},
		{PromptTokens: &negative, CompletionTokens: &completion},
	} {
		_, _, _, _, err = llamaCppUsageCounts(usage, nil)
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	}
	negativeMS := -1.0
	_, _, err = llamaCppDurations(&llamaCppTimings{PromptMS: &negativeMS})
	require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
}

func TestFormatParameterCount(t *testing.T) {
	assert.Empty(t, formatParameterCount(0))
	assert.Equal(t, "950", formatParameterCount(950))
	assert.Equal(t, "1.5K", formatParameterCount(1_500))
	assert.Equal(t, "596.0M", formatParameterCount(596_000_000))
	assert.Equal(t, "8.2B", formatParameterCount(8_190_000_000))
}

func jsonInt(value int32) string {
	body, _ := json.Marshal(value)
	return string(body)
}
