// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOllamaBackend_ReleaseModelsVerifiesWholeBatchAfterUnload(t *testing.T) {
	var requests []string
	var events []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/ps" {
			events = append(events, "residency")
			_, _ = w.Write([]byte(`{"models":[{"name":"unrelated:1b"}]}`))
			return
		}
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/generate", r.URL.Path)
		var request struct {
			Model     string          `json:"model"`
			KeepAlive json.RawMessage `json:"keep_alive"`
			Stream    bool            `json:"stream"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request.Model)
		events = append(events, "release:"+request.Model)
		assert.JSONEq(t, "0", string(request.KeepAlive))
		assert.False(t, request.Stream)
		assert.NoError(t, json.NewEncoder(w).Encode(ollamaModelReleaseResponse{Model: request.Model, Done: true, DoneReason: "unload"}))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, backend.ReleaseModels(context.Background(), []string{"qwen3:0.6b", "gemma3:4b"}))
	assert.Equal(t, []string{"qwen3:0.6b", "gemma3:4b"}, requests)
	assert.Equal(t, []string{"release:qwen3:0.6b", "release:gemma3:4b", "residency"}, events)
}

func TestOllamaBackend_ReleaseModelsValidatesEntireBatchBeforeUnload(t *testing.T) {
	var generateCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/generate" {
			generateCalls++
		}
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	err = backend.ReleaseModels(context.Background(), []string{"qwen3:0.6b", "bad;tag"})
	require.ErrorIs(t, err, constants.ErrInferenceModelTagInvalid)
	assert.Zero(t, generateCalls)
}

func TestOllamaBackend_ReleaseModelsFailsWhenRequestedModelRemainsResident(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/generate" {
			var request struct {
				Model string `json:"model"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.NoError(t, json.NewEncoder(w).Encode(ollamaModelReleaseResponse{Model: request.Model, Done: true, DoneReason: "unload"}))
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:0.6b"}]}`))
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = backend.ReleaseModels(ctx, []string{"qwen3:0.6b"})
	assert.ErrorIs(t, err, constants.ErrInferenceModelReleaseFailed)
}

func TestOllamaBackend_ReleaseModelsWaitsForAsynchronousUnload(t *testing.T) {
	var residencyReads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/generate" {
			var request struct {
				Model string `json:"model"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.NoError(t, json.NewEncoder(w).Encode(ollamaModelReleaseResponse{Model: request.Model, Done: true, DoneReason: "unload"}))
			return
		}
		residencyReads++
		if residencyReads == 1 {
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:0.6b"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, backend.ReleaseModels(context.Background(), []string{"qwen3:0.6b"}))
	assert.Equal(t, 2, residencyReads)
}

func TestOllamaBackend_ReleaseModelsFailsClosedOnMalformedResidency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/generate" {
			var request struct {
				Model string `json:"model"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.NoError(t, json.NewEncoder(w).Encode(ollamaModelReleaseResponse{Model: request.Model, Done: true, DoneReason: "unload"}))
			return
		}
		_, _ = w.Write([]byte(`{"models":[`))
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	err = backend.ReleaseModels(context.Background(), []string{"qwen3:0.6b"})
	assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
}

func TestOllamaBackend_GenerateConstructsCorrectChatRequest(t *testing.T) {
	logger := testutil.NewTestLogger()
	topP := float32(0.8)
	topK := int32(40)
	seed := int32(424242)
	contextLimit := int32(8192)
	parallelToolCalls := true

	var capturedBody ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/chat", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.NoError(t, json.Unmarshal(body, &capturedBody))

		promptTokens := int32(12)
		completionTokens := int32(8)
		loadDuration := int64(2_000_000)
		promptEvalDuration := int64(10_000_000)
		generationDuration := int64(40_000_000)
		totalDuration := int64(52_000_000)
		resp := ollamaChatResponse{
			Model: capturedBody.Model,
			Message: ollamaChatMessage{
				Role:    "assistant",
				Content: "generated text",
				ToolCalls: []ollamaToolCall{{
					ID: "call-2",
					Function: ollamaToolFunction{
						Name:      "inspect",
						Arguments: json.RawMessage(`{"path":"next.txt"}`),
					},
				}},
			},
			Done:               true,
			DoneReason:         "stop",
			PromptEvalCount:    &promptTokens,
			EvalCount:          &completionTokens,
			LoadDuration:       &loadDuration,
			PromptEvalDuration: &promptEvalDuration,
			EvalDuration:       &generationDuration,
			TotalDuration:      &totalDuration,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "gemma3:4b",
		Messages: []*operatorv1.InferenceMessage{
			{
				Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM,
				Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "Be precise"}}},
			},
			{
				Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
				Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "Hello, world"}}},
			},
			{
				Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT,
				Parts: []*operatorv1.InferenceMessagePart{
					{Part: &operatorv1.InferenceMessagePart_Text{Text: "Checking both targets"}},
					{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
						CallId:        "call-1",
						Name:          "inspect",
						ArgumentsJson: `{"path":"target.txt"}`,
					}}},
					{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
						CallId:        "call-2",
						Name:          "inspect",
						ArgumentsJson: `{"path":"second.txt"}`,
					}}},
				},
			},
			{
				Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL,
				Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{
					CallId:     "call-1",
					Name:       "inspect",
					ResultJson: `{"ok":true}`,
				}}}},
			},
		},
		Tools: []*operatorv1.InferenceToolDeclaration{{
			Name:        "inspect",
			Description: "Inspect a target",
			JsonSchema:  `{"properties":{"path":{"type":"string"}},"type":"object"}`,
		}},
		Temperature:   0.7,
		MaxTokens:     100,
		KeepAlive:     "-1",
		TopP:          &topP,
		TopK:          &topK,
		Seed:          &seed,
		StopSequences: []string{"END", "STOP"},
		ResponseFormat: &operatorv1.InferenceResponseFormat{
			MediaType:  "application/json",
			JsonSchema: `{"properties":{"answer":{"type":"string"}},"type":"object"}`,
		},
		ToolChoice: &operatorv1.InferenceToolChoice{
			Mode:             operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO,
			AllowedToolNames: []string{"inspect"},
		},
		ParallelToolCalls: &parallelToolCalls,
		Thinking: &operatorv1.InferenceThinkingControl{
			Mode:            &operatorv1.InferenceThinkingControl_Enabled{Enabled: true},
			IncludeThoughts: true,
		},
		ContextLimit: &contextLimit,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Parts, 2)
	assert.Equal(t, "generated text", resp.Parts[0].GetText())
	assert.Equal(t, "call-2", resp.Parts[1].GetToolCall().GetCallId())
	assert.Equal(t, "inspect", resp.Parts[1].GetToolCall().GetName())
	assert.Equal(t, `{"path":"next.txt"}`, resp.Parts[1].GetToolCall().GetArgumentsJson()) //nolint:testifylint // exact bytes, not JSON equivalence
	assert.Equal(t, "gemma3:4b", resp.Model)
	assert.Equal(t, int32(12), resp.PromptTokens)
	assert.Equal(t, int32(8), resp.CompletionTokens)
	assert.Equal(t, int32(20), resp.TotalTokens)
	assert.True(t, resp.UsageReported)
	require.NotNil(t, resp.LoadDurationNS)
	assert.Equal(t, int64(2_000_000), *resp.LoadDurationNS)
	require.NotNil(t, resp.PromptEvalDurationNS)
	assert.Equal(t, int64(10_000_000), *resp.PromptEvalDurationNS)
	require.NotNil(t, resp.GenerationDurationNS)
	assert.Equal(t, int64(40_000_000), *resp.GenerationDurationNS)
	require.NotNil(t, resp.TotalDurationNS)
	assert.Equal(t, int64(52_000_000), *resp.TotalDurationNS)
	assert.Equal(t, operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER, resp.TimingSource)
	assert.Equal(t, "stop", resp.FinishReason)

	// Verify the request was constructed correctly
	assert.Equal(t, "gemma3:4b", capturedBody.Model)
	assert.True(t, capturedBody.Stream)
	require.Len(t, capturedBody.Messages, 4)
	assert.Equal(t, ollamaChatMessage{Role: "system", Content: "Be precise"}, capturedBody.Messages[0])
	assert.Equal(t, ollamaChatMessage{Role: "user", Content: "Hello, world"}, capturedBody.Messages[1])
	require.Len(t, capturedBody.Messages[2].ToolCalls, 2)
	assert.Equal(t, "assistant", capturedBody.Messages[2].Role)
	assert.Equal(t, "Checking both targets", capturedBody.Messages[2].Content)
	assert.Equal(t, "call-1", capturedBody.Messages[2].ToolCalls[0].ID)
	assert.Equal(t, "inspect", capturedBody.Messages[2].ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"path":"target.txt"}`, string(capturedBody.Messages[2].ToolCalls[0].Function.Arguments))
	assert.Equal(t, "call-2", capturedBody.Messages[2].ToolCalls[1].ID)
	assert.JSONEq(t, `{"path":"second.txt"}`, string(capturedBody.Messages[2].ToolCalls[1].Function.Arguments))
	assert.Equal(t, "tool", capturedBody.Messages[3].Role)
	assert.Equal(t, "call-1", capturedBody.Messages[3].ToolCallID)
	assert.Equal(t, "inspect", capturedBody.Messages[3].ToolName)
	assert.Equal(t, `{"ok":true}`, capturedBody.Messages[3].Content)
	require.Len(t, capturedBody.Tools, 1)
	assert.Equal(t, "function", capturedBody.Tools[0].Type)
	assert.Equal(t, "inspect", capturedBody.Tools[0].Function.Name)
	assert.Equal(t, "Inspect a target", capturedBody.Tools[0].Function.Description)
	assert.JSONEq(t, `{"properties":{"path":{"type":"string"}},"type":"object"}`, string(capturedBody.Tools[0].Function.Parameters))
	assert.InDelta(t, float32(0.7), capturedBody.Options.Temperature, 0)
	assert.Equal(t, int32(100), capturedBody.Options.NumPredict)
	require.NotNil(t, capturedBody.Options.TopP)
	assert.InDelta(t, topP, *capturedBody.Options.TopP, 0)
	require.NotNil(t, capturedBody.Options.TopK)
	assert.Equal(t, topK, *capturedBody.Options.TopK)
	require.NotNil(t, capturedBody.Options.Seed)
	assert.Equal(t, seed, *capturedBody.Options.Seed)
	assert.Equal(t, []string{"END", "STOP"}, capturedBody.Options.Stop)
	require.NotNil(t, capturedBody.Options.NumCtx)
	assert.Equal(t, contextLimit, *capturedBody.Options.NumCtx)
	assert.Equal(t, "true", string(capturedBody.Think))
	assert.JSONEq(t, `{"properties":{"answer":{"type":"string"}},"type":"object"}`, string(capturedBody.Format))
	assert.Equal(t, json.RawMessage("-1"), capturedBody.KeepAlive) //nolint:testifylint // exact bytes, not JSON equivalence
}

func TestOllamaBackend_GeneratePreservesOrderedStreamPartsAndMeasuresFirstToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ollamaChatRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.True(t, request.Stream)
		flusher, ok := w.(http.Flusher)
		assert.True(t, ok)
		events := []string{
			`{"model":"test-model","message":{"role":"assistant","thinking":"considering"},"done":false}`,
			`{"model":"test-model","message":{"role":"assistant","content":"answer"},"done":false}`,
			`{"model":"test-model","message":{"role":"assistant","tool_calls":[{"id":"call-1","function":{"name":"inspect","arguments":{"path":"target.txt"}}}]},"done":false}`,
			`{"model":"test-model","message":{"role":"assistant","content":""},"done":true,"done_reason":"tool_calls","prompt_eval_count":4,"eval_count":3,"load_duration":1,"prompt_eval_duration":2,"eval_duration":3,"total_duration":6}`,
		}
		for _, event := range events {
			time.Sleep(time.Millisecond)
			_, err := io.WriteString(w, event+"\n")
			assert.NoError(t, err)
			flusher.Flush()
		}
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})

	require.NoError(t, err)
	require.Len(t, response.Parts, 3)
	assert.Equal(t, "considering", response.Parts[0].GetThinking())
	assert.Equal(t, "answer", response.Parts[1].GetText())
	assert.Equal(t, "call-1", response.Parts[2].GetToolCall().GetCallId())
	assert.Equal(t, `{"path":"target.txt"}`, response.Parts[2].GetToolCall().GetArgumentsJson()) //nolint:testifylint // exact bytes, not JSON equivalence
	require.NotNil(t, response.TimeToFirstTokenNS)
	assert.Positive(t, *response.TimeToFirstTokenNS)
	assert.Equal(t, operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER, response.TimingSource)
	assert.Equal(t, "tool_calls", response.FinishReason)
	assert.Equal(t, int32(7), response.TotalTokens)
}

func TestOllamaBackend_GenerateRejectsStreamWithoutOneTerminalEvent(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing terminal",
			body: `{"model":"test-model","message":{"role":"assistant","content":"partial"},"done":false}` + "\n",
		},
		{
			name: "event after terminal",
			body: `{"model":"test-model","message":{"role":"assistant","content":"complete"},"done":true}` + "\n" +
				`{"model":"test-model","message":{"role":"assistant","content":"extra"},"done":false}` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, err := io.WriteString(w, tt.body)
				assert.NoError(t, err)
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)

			response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})

			require.Error(t, err)
			assert.Nil(t, response)
			assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
		})
	}
}

// An empty completion is the served model's answer, not a provider fault: it
// returns an empty result so the evaluation grades it as a model failure.
func TestOllamaBackend_GenerateEmptyCompletionReturnsEmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := io.WriteString(w, `{"model":"test-model","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")
		assert.NoError(t, err)
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})

	require.NoError(t, err)
	assert.Empty(t, response.Parts)
	assert.Equal(t, "stop", response.FinishReason)
	wantHash, err := models.ComputeInferenceOutputHash(nil, "stop")
	require.NoError(t, err)
	assert.Equal(t, wantHash, response.OutputHash)
}

func TestOllamaBackend_GenerateTerminalEventErrorNamesFailedCondition(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing terminal",
			body: `{"model":"test-model","message":{"role":"assistant","content":"partial"},"done":false}` + "\n",
			want: "stream ended without a done event",
		},
		{
			name: "terminal without model",
			body: `{"model":"test-model","message":{"role":"assistant","content":"x"},"done":false}` + "\n" +
				`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}` + "\n",
			want: "done event carries no model",
		},
		{
			name: "terminal model differs from streamed model",
			body: `{"model":"test-model","message":{"role":"assistant","content":"x"},"done":false}` + "\n" +
				`{"model":"other-model","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}` + "\n",
			want: "served model changed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, err := io.WriteString(w, tt.body)
				assert.NoError(t, err)
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)

			_, err = backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})

			require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestOllamaBackend_GenerateRejectsParallelToolCallsSplitAcrossEventsWhenDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		events := []string{
			`{"model":"test-model","message":{"role":"assistant","tool_calls":[{"function":{"name":"inspect","arguments":{}}}]},"done":false}`,
			`{"model":"test-model","message":{"role":"assistant","tool_calls":[{"function":{"name":"search","arguments":{}}}]},"done":true}`,
		}
		for _, event := range events {
			_, err := io.WriteString(w, event+"\n")
			assert.NoError(t, err)
		}
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	parallelToolCalls := false

	response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model", ParallelToolCalls: &parallelToolCalls})

	require.Error(t, err)
	assert.Nil(t, response)
	assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
}

func TestOllamaBackend_GenerateTimeoutDuringStreamReturnsBackendTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		assert.True(t, ok)
		_, err := io.WriteString(w, `{"model":"test-model","message":{"role":"assistant","content":"partial"},"done":false}`+"\n")
		assert.NoError(t, err)
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	response, err := backend.Generate(ctx, models.GenerateRequest{Model: "test-model"})

	require.Error(t, err)
	assert.Nil(t, response)
	assert.ErrorIs(t, err, constants.ErrInferenceBackendTimeout)
}

func TestOllamaBackend_GenerateAcceptsToolOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := ollamaChatResponse{
			Model: "test-model",
			Message: ollamaChatMessage{Role: "assistant", ToolCalls: []ollamaToolCall{{
				ID: "call-1",
				Function: ollamaToolFunction{
					Name:      "inspect",
					Arguments: json.RawMessage(`{"path":"target.txt"}`),
				},
			}}},
			Done:       true,
			DoneReason: "tool_calls",
		}
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})

	require.NoError(t, err)
	require.Len(t, response.Parts, 1)
	assert.Empty(t, response.Parts[0].GetText())
	assert.Equal(t, "inspect", response.Parts[0].GetToolCall().GetName())
	assert.Equal(t, "tool_calls", response.FinishReason)
}

func TestOllamaBackend_GenerateRejectsInvalidToolCalls(t *testing.T) {
	tests := []struct {
		name         string
		message      ollamaChatMessage
		responseBody []byte
	}{
		{name: "malformed arguments", responseBody: []byte(`{"model":"test-model","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"inspect","arguments":"{"}}]},"done":true}`)},
		{name: "duplicate argument key", message: ollamaChatMessage{Role: "assistant", ToolCalls: []ollamaToolCall{{Function: ollamaToolFunction{Name: "inspect", Arguments: json.RawMessage(`{"path":"a","path":"b"}`)}}}}},
		{name: "non-object arguments", message: ollamaChatMessage{Role: "assistant", ToolCalls: []ollamaToolCall{{Function: ollamaToolFunction{Name: "inspect", Arguments: json.RawMessage(`[]`)}}}}},
		{name: "missing tool name", message: ollamaChatMessage{Role: "assistant", ToolCalls: []ollamaToolCall{{Function: ollamaToolFunction{Arguments: json.RawMessage(`{}`)}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.responseBody != nil {
					_, err := w.Write(tt.responseBody)
					assert.NoError(t, err)
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(ollamaChatResponse{Model: "test-model", Message: tt.message, Done: true}))
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)

			response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})

			require.Error(t, err)
			assert.Nil(t, response)
			assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
		})
	}
}

func TestOllamaBackend_GenerateFiltersToolsUsingAutoChoice(t *testing.T) {
	var capturedBody ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		assert.NoError(t, json.NewEncoder(w).Encode(ollamaChatResponse{Model: "test-model", Message: ollamaChatMessage{Role: "assistant", Content: "ok"}, Done: true}))
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	_, err = backend.Generate(context.Background(), models.GenerateRequest{
		Model: "test-model",
		Tools: []*operatorv1.InferenceToolDeclaration{
			{Name: "inspect", JsonSchema: `{"type":"object"}`},
			{Name: "search", JsonSchema: `{"type":"object"}`},
		},
		ToolChoice: &operatorv1.InferenceToolChoice{
			Mode:             operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO,
			AllowedToolNames: []string{"inspect"},
		},
	})

	require.NoError(t, err)
	require.Len(t, capturedBody.Tools, 1)
	assert.Equal(t, "inspect", capturedBody.Tools[0].Function.Name)
}

func TestOllamaBackend_GenerateEmptyAutoChoiceAllowsAllDeclaredTools(t *testing.T) {
	var capturedBody ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		assert.NoError(t, json.NewEncoder(w).Encode(ollamaChatResponse{Model: "test-model", Message: ollamaChatMessage{Role: "assistant", Content: "ok"}, Done: true}))
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	_, err = backend.Generate(context.Background(), models.GenerateRequest{
		Model: "test-model",
		Tools: []*operatorv1.InferenceToolDeclaration{
			{Name: "inspect", JsonSchema: `{"type":"object"}`},
			{Name: "search", JsonSchema: `{"type":"object"}`},
		},
		ToolChoice: &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO},
	})

	require.NoError(t, err)
	assert.Len(t, capturedBody.Tools, 2)
}

func TestOllamaBackend_GenerateOutputLimitIsStatedValueOrPlatformCeiling(t *testing.T) {
	for _, tt := range []struct {
		name      string
		requested int32
		want      int32
	}{
		{name: "unset runs under the ceiling", requested: 0, want: constants.InferenceMaxOutputTokens},
		{name: "stated value passes through", requested: 512, want: 512},
		{name: "ceiling passes through", requested: constants.InferenceMaxOutputTokens, want: constants.InferenceMaxOutputTokens},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var captured ollamaChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
				assert.NoError(t, json.NewEncoder(w).Encode(ollamaChatResponse{
					Model:   "test-model",
					Message: ollamaChatMessage{Role: "assistant", Content: "ok"},
					Done:    true,
				}))
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)

			_, err = backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model", MaxTokens: tt.requested})

			require.NoError(t, err)
			assert.Equal(t, tt.want, captured.Options.NumPredict)
		})
	}
}

func TestOllamaBackend_GenerateRejectsParallelToolCallsWhenDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, json.NewEncoder(w).Encode(ollamaChatResponse{
			Model: "test-model",
			Message: ollamaChatMessage{Role: "assistant", ToolCalls: []ollamaToolCall{
				{Function: ollamaToolFunction{Name: "inspect", Arguments: json.RawMessage(`{}`)}},
				{Function: ollamaToolFunction{Name: "search", Arguments: json.RawMessage(`{}`)}},
			}},
			Done: true,
		}))
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	parallelToolCalls := false

	response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model", ParallelToolCalls: &parallelToolCalls})

	require.Error(t, err)
	assert.Nil(t, response)
	assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
}

func TestOllamaBackend_GenerateAllowsParallelToolCallsWhenEnabledOrUnspecified(t *testing.T) {
	for _, tt := range []struct {
		name     string
		parallel *bool
	}{
		{name: "enabled", parallel: func() *bool { value := true; return &value }()},
		{name: "unspecified"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				assert.NoError(t, json.NewEncoder(w).Encode(ollamaChatResponse{
					Model: "test-model",
					Message: ollamaChatMessage{Role: "assistant", ToolCalls: []ollamaToolCall{
						{Function: ollamaToolFunction{Name: "inspect", Arguments: json.RawMessage(`{}`)}},
						{Function: ollamaToolFunction{Name: "search", Arguments: json.RawMessage(`{}`)}},
					}},
					Done: true,
				}))
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)

			response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model", ParallelToolCalls: tt.parallel})

			require.NoError(t, err)
			assert.Len(t, response.Parts, 2)
		})
	}
}

func TestOllamaBackend_GenerateHandlesAllThreeRoles(t *testing.T) {
	logger := testutil.NewTestLogger()

	roles := []models.InferenceModelRole{
		models.InferenceModelRolePrimary,
		models.InferenceModelRoleAssistant,
		models.InferenceModelRoleLite,
	}
	modelsList := []string{"gemma3:4b", "llama3.2:3b", "qwen3:1.5b"}

	for i, role := range roles {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := ollamaChatResponse{
				Model:      modelsList[i],
				Message:    ollamaChatMessage{Role: "assistant", Content: "ok"},
				Done:       true,
				DoneReason: "stop",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		backend, err := NewOllamaBackend(server.URL, logger)
		require.NoError(t, err)
		resp, err := backend.Generate(context.Background(), models.GenerateRequest{
			Role:  role,
			Model: modelsList[i],
		})

		require.NoError(t, err)
		assert.Equal(t, modelsList[i], resp.Model, "role %d should use model %s", i, modelsList[i])
	}
}

func TestOllamaBackend_GenerateNonOKStatusReturnsErrInferenceGenerateFailed(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, constants.ErrInferenceGenerateFailed)
}

func TestOllamaBackend_GenerateToolsUnsupportedReturnsErrInferenceToolsUnsupported(t *testing.T) {
	logger := testutil.NewTestLogger()
	requests := make(chan ollamaChatRequest, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ollamaChatRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests <- request
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"registry.ollama.ai/sam860/LFM2:350m does not support tools"}`))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "sam860/LFM2:350m",
		Tools: []*operatorv1.InferenceToolDeclaration{{
			Name:        "get_command_constraints",
			Description: "constraints",
			JsonSchema:  `{"type":"object"}`,
		}},
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, constants.ErrInferenceToolsUnsupported)
	select {
	case request := <-requests:
		assert.Equal(t, "sam860/LFM2:350m", request.Model)
		require.Len(t, request.Tools, 1, "the provider must receive the tool declaration before reporting unsupported")
		assert.Equal(t, "get_command_constraints", request.Tools[0].Function.Name)
	default:
		t.Fatal("tool support must be rejected by the provider response, never by a local model capability assumption")
	}
}

func TestOllamaBackend_GenerateThinkingUnsupportedReturnsErrInferenceCapabilityUnsupported(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"\"gemma3:1b\" does not support thinking"}`))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "gemma3:1b",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)
	assert.NotErrorIs(t, err, constants.ErrInferenceGenerateFailed)
}

func TestOllamaBackend_GenerateRequestTimeoutReturnsErrInferenceBackendTimeout(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, constants.ErrInferenceBackendTimeout)
}

func TestOllamaBackend_GenerateContextCancelledReturnsErrInferenceBackendTimeout(t *testing.T) {
	logger := testutil.NewTestLogger()

	// Server that hangs until client cancels or disconnects
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := backend.Generate(ctx, models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, constants.ErrInferenceBackendTimeout)
}

func TestOllamaBackend_GenerateParsesResponseWithoutDoneReason(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		promptTokens := int32(5)
		completionTokens := int32(3)
		resp := ollamaChatResponse{
			Model:           "test-model",
			Message:         ollamaChatMessage{Role: "assistant", Content: "result"},
			Done:            true,
			PromptEvalCount: &promptTokens,
			EvalCount:       &completionTokens,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.NoError(t, err)
	require.Len(t, resp.Parts, 1)
	assert.Equal(t, "result", resp.Parts[0].GetText())
	assert.Equal(t, "stop", resp.FinishReason, "empty done_reason with done=true should default to stop")
}

func TestOllamaBackend_GenerateDistinguishesUnavailableUsageFromReportedZero(t *testing.T) {
	for _, tt := range []struct {
		name          string
		responseBody  string
		usageReported bool
	}{
		{
			name:         "unavailable",
			responseBody: `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true}`,
		},
		{
			name:          "reported zero",
			responseBody:  `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":0,"eval_count":0,"load_duration":0}`,
			usageReported: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte(tt.responseBody))
				assert.NoError(t, err)
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)

			response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})

			require.NoError(t, err)
			assert.Equal(t, tt.usageReported, response.UsageReported)
			assert.Zero(t, response.TotalTokens)
			if tt.usageReported {
				require.NotNil(t, response.LoadDurationNS)
				assert.Zero(t, *response.LoadDurationNS)
				assert.Equal(t, operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER, response.TimingSource)
			} else {
				assert.Nil(t, response.LoadDurationNS)
				assert.Equal(t, operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER, response.TimingSource)
			}
			require.NotNil(t, response.TimeToFirstTokenNS)
			assert.GreaterOrEqual(t, *response.TimeToFirstTokenNS, int64(0))
		})
	}
}

func TestOllamaBackend_GenerateParsesCacheTokensWithoutInferringThinkingTokens(t *testing.T) {
	tests := []struct {
		name            string
		responseBody    string
		wantCacheTokens *int32
		wantTotalTokens int32
		wantError       bool
	}{
		{
			name:            "positive",
			responseBody:    `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":10,"prompt_eval_cached_count":4,"eval_count":3}`,
			wantCacheTokens: func() *int32 { value := int32(4); return &value }(),
			wantTotalTokens: 13,
		},
		{
			name:            "explicit zero",
			responseBody:    `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":10,"prompt_eval_cached_count":0,"eval_count":3}`,
			wantCacheTokens: func() *int32 { value := int32(0); return &value }(),
			wantTotalTokens: 13,
		},
		{
			name:            "absent",
			responseBody:    `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":10,"eval_count":3}`,
			wantTotalTokens: 13,
		},
		{
			name:            "thinking content without thinking count",
			responseBody:    `{"model":"test-model","message":{"role":"assistant","thinking":"private thought","content":"ok"},"done":true,"prompt_eval_count":10,"eval_count":3}`,
			wantTotalTokens: 13,
		},
		{
			name:         "negative",
			responseBody: `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":10,"prompt_eval_cached_count":-1,"eval_count":3}`,
			wantError:    true,
		},
		{
			name: "streaming terminal",
			responseBody: `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":false}
{"model":"test-model","message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":10,"prompt_eval_cached_count":4,"eval_count":3}`,
			wantCacheTokens: func() *int32 { value := int32(4); return &value }(),
			wantTotalTokens: 13,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, err := io.WriteString(w, tt.responseBody)
				assert.NoError(t, err)
			}))
			defer server.Close()

			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)
			response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})
			if tt.wantError {
				require.Error(t, err)
				assert.Nil(t, response)
				assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, response)
			assert.Equal(t, tt.wantTotalTokens, response.TotalTokens)
			if tt.wantCacheTokens == nil {
				assert.Nil(t, response.CacheTokens)
			} else {
				require.NotNil(t, response.CacheTokens)
				assert.Equal(t, *tt.wantCacheTokens, *response.CacheTokens)
				result := response.ToProtoInferenceResult()
				require.NotNil(t, result.CacheTokens)
				assert.Equal(t, *tt.wantCacheTokens, *result.CacheTokens)
			}
			assert.Nil(t, response.ThinkingTokens)
		})
	}
}

func TestOllamaBackend_GenerateFailsTypedWhenPromptFillsContextWindow(t *testing.T) {
	limit := func(v int32) *int32 { return &v }
	tests := []struct {
		name         string
		contextLimit *int32
		content      string
		wantOverflow bool
	}{
		{name: "empty output at limit", contextLimit: limit(8192), wantOverflow: true},
		{name: "empty output over limit", contextLimit: limit(4096), wantOverflow: true},
		{name: "answered output at limit", contextLimit: limit(8192), content: "ok", wantOverflow: true},
		{name: "prompt below limit", contextLimit: limit(32768), content: "ok"},
		{name: "limit unset", contextLimit: nil, content: "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, err := fmt.Fprintf(w, `{"model":"test-model","message":{"role":"assistant","content":%q},"done":true,"prompt_eval_count":8192,"eval_count":1}`, tt.content)
				assert.NoError(t, err)
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)

			response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model", ContextLimit: tt.contextLimit})

			if tt.wantOverflow {
				require.Error(t, err)
				assert.Nil(t, response)
				require.ErrorIs(t, err, constants.ErrInferenceContextOverflow)
				assert.NotErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, int32(8192), response.PromptTokens)
		})
	}
}

func TestOllamaBackend_GenerateRejectsContradictoryOrInvalidMetadata(t *testing.T) {
	tests := []struct {
		name         string
		responseBody string
	}{
		{name: "missing served model", responseBody: `{"message":{"role":"assistant","content":"ok"},"done":true}`},
		{name: "partial usage", responseBody: `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":1}`},
		{name: "negative usage", responseBody: `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":-1,"eval_count":1}`},
		{name: "negative timing", responseBody: `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"load_duration":-1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte(tt.responseBody))
				assert.NoError(t, err)
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)

			response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model"})

			require.Error(t, err)
			assert.Nil(t, response)
			assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
		})
	}
}

func TestOllamaBackend_GenerateVerifiesFrozenModelDigestBeforeAndAfterProviderCall(t *testing.T) {
	modelDigest := strings.Repeat("a", 64)
	tagsCalls := 0
	var providerRequest []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			tagsCalls++
			assert.NoError(t, json.NewEncoder(w).Encode(ollamaTagsResponse{Models: []ollamaTagModel{{Name: "test-model", Digest: modelDigest}}}))
		case "/api/chat":
			var err error
			providerRequest, err = io.ReadAll(r.Body)
			assert.NoError(t, err)
			_, err = w.Write([]byte(`{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected provider path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model", ModelDigest: modelDigest})

	require.NoError(t, err)
	assert.Equal(t, 2, tagsCalls)
	assert.Equal(t, modelDigest, response.ServedModelDigest)
	assert.Equal(t, models.SHA256Hex(providerRequest), response.NormalizedRequestHash)
	wantOutputHash, err := models.ComputeInferenceOutputHash(response.Parts, response.FinishReason)
	require.NoError(t, err)
	assert.Equal(t, wantOutputHash, response.OutputHash)
}

func TestOllamaBackend_GenerateRejectsModelDigestDriftAfterProviderCall(t *testing.T) {
	requestedDigest := strings.Repeat("a", 64)
	tagsCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			tagsCalls++
			digest := requestedDigest
			if tagsCalls == 2 {
				digest = strings.Repeat("b", 64)
			}
			assert.NoError(t, json.NewEncoder(w).Encode(ollamaTagsResponse{Models: []ollamaTagModel{{Name: "test-model", Digest: digest}}}))
		case "/api/chat":
			_, err := w.Write([]byte(`{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true}`))
			assert.NoError(t, err)
		}
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	response, err := backend.Generate(context.Background(), models.GenerateRequest{Model: "test-model", ModelDigest: requestedDigest})

	require.Error(t, err)
	assert.Nil(t, response)
	assert.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
}

func TestOllamaBackend_StatusReturnsAvailableAndModels(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)

		resp := ollamaTagsResponse{
			Models: []ollamaTagModel{
				{Name: "gemma3:4b"},
				{Name: "llama3.2:3b"},
				{Name: "qwen3:1.5b"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	status, err := backend.Status(context.Background())

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.True(t, status.Available)
	assert.Len(t, status.Models, 3)
	assert.Contains(t, status.Models, "gemma3:4b")
	assert.Contains(t, status.Models, "llama3.2:3b")
	assert.Contains(t, status.Models, "qwen3:1.5b")
}

func TestOllamaBackend_ListModelVariantsNormalizesOllamaDigests(t *testing.T) {
	digest := strings.Repeat("c", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		assert.NoError(t, json.NewEncoder(w).Encode(ollamaTagsResponse{
			Models: []ollamaTagModel{{Name: "probe-model", Digest: "sha256:" + digest}},
		}))
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	variants, err := backend.ListModelVariants(context.Background())
	require.NoError(t, err)
	require.Len(t, variants, 1)
	assert.Equal(t, "probe-model", variants[0].GetModel())
	assert.Equal(t, digest, variants[0].GetDigest())
}

func TestOllamaBackend_StatusNonOKReturnsErrInferenceBackendUnavailable(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	status, err := backend.Status(context.Background())

	require.Error(t, err)
	assert.Nil(t, status)
	assert.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
}

func TestOllamaBackend_StatusContextCancelledReturnsErrInferenceBackendTimeout(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	status, err := backend.Status(ctx)

	require.Error(t, err)
	assert.Nil(t, status)
	assert.ErrorIs(t, err, constants.ErrInferenceBackendTimeout)
}

func TestOllamaBackend_TrimsTrailingSlashFromEndpoint(t *testing.T) {
	logger := testutil.NewTestLogger()

	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		resp := ollamaTagsResponse{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL+"/", logger)
	require.NoError(t, err)
	_, err = backend.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/api/tags", capturedPath, "trailing slash should be trimmed")
}

func TestOllamaBackend_GenerateErrorDoesNotLeakProviderResponseBody(t *testing.T) {
	logger := testutil.NewTestLogger()

	const providerCanary = "provider-secret-canary-text"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"` + providerCanary + `"}`))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, constants.ErrInferenceGenerateFailed)
	assert.NotContains(t, err.Error(), providerCanary, "provider response text must not leak into errors")
}

func TestOllamaBackend_GenerateCallerCancellationReturnsContextCanceled(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := backend.Generate(ctx, models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, context.Canceled, "caller cancellation must stay distinguishable from a deadline")
	assert.NotErrorIs(t, err, constants.ErrInferenceBackendTimeout)
}

func TestOllamaBackend_GenerateMalformedResponseReturnsErrInferenceProviderResponseInvalid(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{not json"))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
}

func TestOllamaBackend_GenerateOversizedResponseReturnsErrInferenceProviderResponseInvalid(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	backend.maxResponseBytes = 1024

	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
}

func TestOllamaBackend_GenerateModelMissingReturnsErrInferenceModelNotFound(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model 'missing:1b' not found"}`))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "missing:1b",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
}

func TestOllamaBackend_StatusMalformedResponseReturnsErrInferenceProviderResponseInvalid(t *testing.T) {
	logger := testutil.NewTestLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{not json"))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)
	status, err := backend.Status(context.Background())

	require.Error(t, err)
	assert.Nil(t, status)
	assert.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
}

func TestOllamaBackend_EndpointPathPrefixPreserved(t *testing.T) {
	logger := testutil.NewTestLogger()

	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		resp := ollamaTagsResponse{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL+"/ollama", logger)
	require.NoError(t, err)
	_, err = backend.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/ollama/api/tags", capturedPath, "a path-prefixed endpoint must keep its prefix")
}

func TestOllamaBackend_GenerateStreamingPublishesProgressEvents(t *testing.T) {
	logger := testutil.NewTestLogger()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(w)
		assert.NoError(t, encoder.Encode(ollamaChatResponse{
			Model:   "test-model",
			Message: ollamaChatMessage{Role: "assistant", Content: "hel\n"},
		}))
		assert.NoError(t, encoder.Encode(ollamaChatResponse{
			Model:      "test-model",
			Message:    ollamaChatMessage{Role: "assistant", Content: "lo\n"},
			Done:       true,
			DoneReason: "stop",
		}))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)

	var progressEvents []*operatorv1.InferenceProgressEvent
	ctx := WithProgressReporter(context.Background(), func(event *operatorv1.InferenceProgressEvent) error {
		progressEvents = append(progressEvents, event)
		return nil
	})

	resp, err := backend.Generate(ctx, models.GenerateRequest{
		Role:              models.InferenceModelRolePrimary,
		Model:             "test-model",
		Stream:            true,
		Scrubber:          mustNewScrubbingSvc(t),
		ProviderAttemptID: "attempt-1",
		Messages: []*operatorv1.InferenceMessage{{
			Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
			Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, progressEvents, 2)
	assert.Equal(t, uint32(1), progressEvents[0].GetSequence())
	assert.Equal(t, "hel\n", progressEvents[0].GetParts()[0].GetText())
	assert.NotNil(t, progressEvents[0].TimeToFirstTokenNs)
	assert.Equal(t, uint32(2), progressEvents[1].GetSequence())
	assert.Equal(t, "lo\n", progressEvents[1].GetParts()[0].GetText())
	resp.ProviderAttemptID = "attempt-1"
	require.NoError(t, models.ReconcileInferenceProgress(progressEvents, resp.ToProtoInferenceResult()))
}

func TestOllamaBackend_GenerateStreamScrubsSecretsSplitAcrossDeltasAtSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoder := json.NewEncoder(w)
		for _, content := range []string{"AKIAIOSFOD", "NN7EXAMPLE ", "mail admin@exa", "mple.com now"} {
			_ = encoder.Encode(ollamaChatResponse{Model: "test-model", Message: ollamaChatMessage{Role: "assistant", Content: content}})
		}
		_ = encoder.Encode(ollamaChatResponse{Model: "test-model", Done: true, DoneReason: "stop"})
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	var events []*operatorv1.InferenceProgressEvent
	ctx := WithProgressReporter(context.Background(), func(event *operatorv1.InferenceProgressEvent) error {
		events = append(events, event)
		return nil
	})
	response, err := backend.Generate(ctx, models.GenerateRequest{
		Model: "test-model", Stream: true, Scrubber: mustNewScrubbingSvc(t), ProviderAttemptID: "attempt-split",
		Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
			Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}}}},
	})
	require.NoError(t, err)

	streamed := progressPartsText(events)
	assert.NotContains(t, streamed, "AKIAIOSFODNN7EXAMPLE")
	assert.NotContains(t, streamed, "admin@example.com")
	assert.NotContains(t, response.Parts[0].GetText(), "AKIAIOSFODNN7EXAMPLE")
	require.NotEmpty(t, events)
	response.ProviderAttemptID = "attempt-split"
	require.NoError(t, models.ReconcileInferenceProgress(events, response.ToProtoInferenceResult()))
}

func TestOllamaBackend_GenerateStreamRefusedWithoutScrubberBeforeProviderCall(t *testing.T) {
	var providerCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{Model: "test-model", Done: true, DoneReason: "stop"})
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)

	ctx := WithProgressReporter(context.Background(), func(*operatorv1.InferenceProgressEvent) error {
		t.Error("no progress may be published without a scrubber")
		return nil
	})
	response, err := backend.Generate(ctx, models.GenerateRequest{
		Model: "test-model", Stream: true, ProviderAttemptID: "attempt-noscrub",
		Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
			Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}}}},
	})
	require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
	assert.Nil(t, response)
	assert.Zero(t, providerCalls, "the provider must not be called when the stream cannot be scrubbed")
}

func TestOllamaBackend_GenerateDeliversProgressBeforeProviderCompletes(t *testing.T) {
	progress := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoder := json.NewEncoder(w)
		// "hello" is released only once the next line ("world") is buffered as lookahead.
		_ = encoder.Encode(ollamaChatResponse{Model: "test-model", Message: ollamaChatMessage{Content: "hello\n"}})
		_ = encoder.Encode(ollamaChatResponse{Model: "test-model", Message: ollamaChatMessage{Content: "world\n"}})
		w.(http.Flusher).Flush()
		select {
		case <-progress:
			_ = encoder.Encode(ollamaChatResponse{Model: "test-model", Done: true, DoneReason: "stop"})
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
	require.NoError(t, err)
	var events []*operatorv1.InferenceProgressEvent
	closeProgress := sync.OnceFunc(func() { close(progress) })
	ctx = WithProgressReporter(ctx, func(event *operatorv1.InferenceProgressEvent) error {
		events = append(events, event)
		closeProgress()
		return nil
	})
	response, err := backend.Generate(ctx, models.GenerateRequest{
		Model: "test-model", Stream: true, Scrubber: mustNewScrubbingSvc(t), ProviderAttemptID: "attempt-live",
		Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
			Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}}}},
	})
	require.NoError(t, err, "provider completion depends on receiving live progress")
	require.Len(t, events, 2, "hello is released live; world is released at the terminal event")
	assert.Equal(t, "hello\n", events[0].GetParts()[0].GetText())
	response.ProviderAttemptID = "attempt-live"
	require.NoError(t, models.ReconcileInferenceProgress(events, response.ToProtoInferenceResult()))
}

func TestOllamaBackend_GenerateStreamingReporterErrorDoesNotFailGeneration(t *testing.T) {
	logger := testutil.NewTestLogger()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(ollamaChatResponse{
			Model:      "test-model",
			Message:    ollamaChatMessage{Role: "assistant", Content: "hello"},
			Done:       true,
			DoneReason: "stop",
		}))
	}))
	defer server.Close()

	backend, err := NewOllamaBackend(server.URL, logger)
	require.NoError(t, err)

	var reportedEvents int
	ctx := WithProgressReporter(context.Background(), func(event *operatorv1.InferenceProgressEvent) error {
		reportedEvents++
		return constants.ErrInferenceProgressBackpressure
	})
	resp, err := backend.Generate(ctx, models.GenerateRequest{
		Role:              models.InferenceModelRolePrimary,
		Model:             "test-model",
		Stream:            true,
		Scrubber:          mustNewScrubbingSvc(t),
		ProviderAttemptID: "attempt-1",
		Messages: []*operatorv1.InferenceMessage{{
			Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
			Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "hello", resp.Parts[0].GetText())
	assert.Equal(t, 1, reportedEvents)
}

func TestOllamaBackend_GeneratePreservesRawTailAfterParserOrReporterFailure(t *testing.T) {
	for _, parserFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("parser_failure_%t", parserFailure), func(t *testing.T) {
			first := `{"model":"test-model","message":{"content":"hello"}}`
			if parserFailure {
				first = `{"model":"test-model","message":{"tool_calls":[{"function":{"name":"inspect","arguments":[]}}]}}`
			}
			raw := first + "\n" + `{"model":"test-model","message":{"content":"tail"},"done":true,"done_reason":"stop"}` + "\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, raw)
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)
			store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			require.NoError(t, store.Begin(ctx, &operatorv1.InferenceProviderAttemptRecord{
				ProviderAttemptId: "attempt-tail", TransactionId: "transaction-tail",
			}))
			ctx = WithAttemptStore(ctx, store)
			ctx = WithProgressReporter(ctx, func(*operatorv1.InferenceProgressEvent) error {
				return constants.ErrInferenceProgressBackpressure
			})
			response, err := backend.Generate(ctx, models.GenerateRequest{
				Model: "test-model", Stream: true, Scrubber: mustNewScrubbingSvc(t), ProviderAttemptID: "attempt-tail",
				Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
					Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}}}},
			})
			if parserFailure {
				require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
				require.Nil(t, response)
			} else {
				require.NoError(t, err)
				// Text deltas with no whitespace are released as one segment at the terminal event.
				require.Len(t, response.Parts, 1)
				assert.Equal(t, "hellotail", response.Parts[0].GetText())
			}
			// The full byte stream, tail included, is bound by digest and size.
			record, err := store.Get(ctx, "attempt-tail")
			require.NoError(t, err)
			sum := sha256.Sum256([]byte(raw))
			assert.Equal(t, hex.EncodeToString(sum[:]), record.GetRawResponseDigest())
			assert.Equal(t, uint64(len(raw)), record.GetRawResponseSizeBytes())
		})
	}
}

// Model output and provider error bodies must never reach the attempt files:
// a 200 stream is recorded as digest and size, a non-200 body not at all.
func TestOllamaBackend_GenerateNeverPersistsProviderBytesInAttemptFiles(t *testing.T) {
	const marker = "PLAINTEXT-MARKER-9c1e"
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"ok_stream", http.StatusOK, `{"model":"test-model","message":{"content":"` + marker + `"},"done":true,"done_reason":"stop"}` + "\n"},
		{"error_body_echoes_prompt", http.StatusInternalServerError, `{"error":"bad prompt ` + marker + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)
			root := testutil.TempDir(t)
			store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, root))
			require.NoError(t, err)
			ctx := context.Background()
			require.NoError(t, store.Begin(ctx, &operatorv1.InferenceProviderAttemptRecord{ProviderAttemptId: "attempt-files", TransactionId: "tx-files"}))
			_, genErr := backend.Generate(WithAttemptStore(ctx, store), models.GenerateRequest{
				Model: "test-model", Scrubber: mustNewScrubbingSvc(t), ProviderAttemptID: "attempt-files",
				Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
					Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}}}},
			})
			record, err := store.Get(ctx, "attempt-files")
			require.NoError(t, err)
			if tc.status == http.StatusOK {
				require.NoError(t, genErr)
				assert.NotEmpty(t, record.GetRawResponseDigest())
				assert.Equal(t, uint64(len(tc.body)), record.GetRawResponseSizeBytes())
			} else {
				require.Error(t, genErr)
				assert.Empty(t, record.GetRawResponseDigest())
				assert.Zero(t, record.GetRawResponseSizeBytes())
			}
			assertNoFileContains(t, root, marker)
		})
	}
}
