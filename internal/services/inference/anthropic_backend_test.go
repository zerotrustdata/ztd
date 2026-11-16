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
	anthropicTestModel              = "claude-opus-5-5"
	anthropicTestCreatedAt          = "2026-09-01T00:00:00Z"
	anthropicTestKey                = "test-anthropic-key"
	anthropicTestResponseID         = "msg_01XFDUDYJgAACzvnptvVer6J"
	anthropicCapturedCallID         = "toolu_01A09q90qw90lq917835lq9"
	anthropicCapturedSignaturePart1 = "EqACCp0CAWkUfRNi/1NGqaZv5f+F29UcIpE+xrm85Hx19SuymElAb9tmUeW1TKQ4uWRui/XMnpjMQh4sK2g0LFSgqOB"
	anthropicCapturedSignaturePart2 = "rkabw2D+wMGvePojGhPl7KumT0tE8mFwIgw47Pj8UpXWELPvBjtx4PIop85Z88CloRRlpMqabgZM3tuGsYexB5QxAyJ"
	anthropicCapturedSignature      = anthropicCapturedSignaturePart1 + anthropicCapturedSignaturePart2
	anthropicCapturedRedactedData   = "EmwKAhgBEgy3va3x1234567890abcdef"
)

// anthropicToolCallStream is a captured-shape Messages SSE response with CRLF
// framing containing thinking, redacted_thinking, text, and a tool_use block.
func anthropicToolCallStream(servedModel string) string {
	events := []string{
		fmt.Sprintf("event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":%q,\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":55,\"cache_creation_input_tokens\":10,\"cache_read_input_tokens\":5,\"output_tokens\":1}}}\r\n\r\n", anthropicTestResponseID, servedModel),
		"event: ping\r\ndata: {\"type\":\"ping\"}\r\n\r\n",
		"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\r\n\r\n",
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"\"}}\r\n\r\n",
		fmt.Sprintf("event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":%q}}\r\n\r\n", anthropicCapturedSignaturePart1),
		fmt.Sprintf("event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":%q}}\r\n\r\n", anthropicCapturedSignaturePart2),
		"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":0}\r\n\r\n",
		fmt.Sprintf("event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":%q}}\r\n\r\n", anthropicCapturedRedactedData),
		"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":1}\r\n\r\n",
		"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\r\n\r\n",
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"text_delta\",\"text\":\"Checking Paris.\"}}\r\n\r\n",
		"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":2}\r\n\r\n",
		fmt.Sprintf("event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":3,\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":\"get_weather\",\"input\":{}}}\r\n\r\n", anthropicCapturedCallID),
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":3,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\r\n\r\n",
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":3,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Paris\\\"}\"}}\r\n\r\n",
		"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":3}\r\n\r\n",
		"event: message_delta\r\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":55,\"output_tokens_details\":{\"thinking_tokens\":39}}}\r\n\r\n",
		"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n",
	}
	return strings.Join(events, "")
}

// anthropicTextStream streams an omitted-thinking block and text from
// anthropicTestModel, ending with stopReason and optional stopDetailsJSON.
func anthropicTextStream(stopReason, stopDetailsJSON string) string {
	stopDetails := ""
	if stopDetailsJSON != "" {
		stopDetails = fmt.Sprintf(",\"stop_details\":%s", stopDetailsJSON)
	}
	events := []string{
		fmt.Sprintf("event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":7,\"cache_read_input_tokens\":1,\"output_tokens\":1}}}\r\n\r\n", anthropicTestModel),
		"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\r\n\r\n",
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig-1\"}}\r\n\r\n",
		"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":0}\r\n\r\n",
		"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\r\n\r\n",
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\r\n\r\n",
		"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\" there\"}}\r\n\r\n",
		"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":1}\r\n\r\n",
		fmt.Sprintf("event: message_delta\r\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q%s},\"usage\":{\"output_tokens\":5,\"output_tokens_details\":{\"thinking_tokens\":3}}}\r\n\r\n", stopReason, stopDetails),
		"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n",
	}
	return strings.Join(events, "")
}

func anthropicModelMetadata(id, createdAt string, thinkingSupported, adaptive, disabled, structured bool) string {
	return fmt.Sprintf(
		`{"type":"model","id":%q,"display_name":%q,"created_at":%q,"max_input_tokens":1000000,"max_tokens":32000,"capabilities":{"thinking":{"supported":%t,"types":{"adaptive":{"supported":%t},"disabled":{"supported":%t}}},"structured_outputs":{"supported":%t}}}`,
		id, id, createdAt, thinkingSupported, adaptive, disabled, structured,
	)
}

// anthropicFake is an in-process Anthropic API: no listener, no network.
type anthropicFake struct {
	mu                sync.Mutex
	createdAt         string
	servedMetaID      string
	thinkingSupported *bool
	adaptiveThinking  *bool
	disabledThinking  *bool
	structuredOutputs *bool
	stream            string
	status            int
	errorBody         string
	listPages         []string
	metaCode          int
	redirect          string
	redirectOnPost    bool
	requests          []*http.Request
	bodies            [][]byte
}

func (f *anthropicFake) RoundTrip(r *http.Request) (*http.Response, error) {
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
	if f.redirect != "" && (!f.redirectOnPost || r.Method == http.MethodPost) {
		resp, _ := respond(http.StatusFound, "")
		resp.Header.Set("Location", f.redirect)
		return resp, nil
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/v1/models":
		if len(f.listPages) == 0 {
			return respond(http.StatusOK, `{"data":[],"has_more":false,"last_id":""}`)
		}
		after := r.URL.Query().Get("after_id")
		if after == "" {
			return respond(http.StatusOK, f.listPages[0])
		}
		for i := 0; i+1 < len(f.listPages); i++ {
			var page anthropicModelList
			if json.Unmarshal([]byte(f.listPages[i]), &page) == nil && page.LastID == after {
				return respond(http.StatusOK, f.listPages[i+1])
			}
		}
		return respond(http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"bad cursor"}}`)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/models/"):
		if f.metaCode != 0 {
			return respond(f.metaCode, `{"type":"error","error":{"type":"not_found_error","message":"model not found"}}`)
		}
		model := strings.TrimPrefix(path, "/v1/models/")
		if f.servedMetaID != "" {
			model = f.servedMetaID
		}
		thinkingSupported := f.thinkingSupported == nil || *f.thinkingSupported
		adaptive := f.adaptiveThinking == nil || *f.adaptiveThinking
		disabled := f.disabledThinking != nil && *f.disabledThinking
		structured := f.structuredOutputs == nil || *f.structuredOutputs
		return respond(http.StatusOK, anthropicModelMetadata(model, f.createdAt, thinkingSupported, adaptive, disabled, structured))
	case r.Method == http.MethodPost && path == "/v1/messages":
		if f.status != 0 && f.status != http.StatusOK {
			return respond(f.status, f.errorBody)
		}
		return respond(http.StatusOK, f.stream)
	default:
		return respond(http.StatusTeapot, "")
	}
}

func (f *anthropicFake) posts() int {
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

func (f *anthropicFake) lastPostBody(t *testing.T) map[string]any {
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

func anthropicOrigin(t *testing.T) string {
	t.Helper()
	origin, ok := constants.HostedInferenceProviderOrigin(constants.InferenceProviderAnthropic)
	require.True(t, ok)
	return origin
}

func newFakeAnthropicBackend(t *testing.T, fake *anthropicFake) *AnthropicBackend {
	t.Helper()
	if fake.createdAt == "" {
		fake.createdAt = anthropicTestCreatedAt
	}
	backend, err := NewAnthropicBackend(anthropicOrigin(t), anthropicTestKey, testutil.NewTestLogger())
	require.NoError(t, err)
	backend.client.Transport = fake
	return backend
}

func anthropicIdentityDigest(t *testing.T, model, createdAt string) string {
	t.Helper()
	digest, err := models.ComputeHostedModelIdentityDigest(constants.InferenceProviderAnthropic, anthropicOrigin(t), model, createdAt)
	require.NoError(t, err)
	return digest
}

func anthropicRequest(model string) models.GenerateRequest {
	return models.GenerateRequest{
		ProviderClass:     constants.InferenceProviderAnthropic,
		Model:             model,
		ProviderAttemptID: "attempt-1",
		Messages: []*operatorv1.InferenceMessage{
			{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "You are terse."}}}},
			{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "What is the weather in Paris?"}}}},
		},
		Tools: []*operatorv1.InferenceToolDeclaration{weatherTool()},
	}
}

func boolPtr(v bool) *bool { return &v }

func TestAnthropicBackend_GenerateDecodesCapturedToolCallStream(t *testing.T) {
	t.Parallel()
	fake := &anthropicFake{stream: anthropicToolCallStream(anthropicTestModel)}
	backend := newFakeAnthropicBackend(t, fake)

	resp, err := backend.Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
	require.NoError(t, err)

	require.Len(t, resp.Parts, 4)

	thinkingPart := resp.Parts[0].GetProviderReasoning()
	require.NotNil(t, thinkingPart)
	assert.Equal(t, constants.InferenceProviderAnthropic, thinkingPart.GetProviderClass())
	expectedThinkingJSON := fmt.Sprintf(`{"signature":%q,"thinking":"","type":"thinking"}`, anthropicCapturedSignature)
	assert.JSONEq(t, expectedThinkingJSON, thinkingPart.GetItemJson())
	if thinkingPart.GetItemJson() != expectedThinkingJSON {
		t.Fatalf("expected canonical thinking JSON %s, got %s", expectedThinkingJSON, thinkingPart.GetItemJson())
	}

	redactedPart := resp.Parts[1].GetProviderReasoning()
	require.NotNil(t, redactedPart)
	assert.Equal(t, constants.InferenceProviderAnthropic, redactedPart.GetProviderClass())
	expectedRedactedJSON := fmt.Sprintf(`{"data":%q,"type":"redacted_thinking"}`, anthropicCapturedRedactedData)
	assert.JSONEq(t, expectedRedactedJSON, redactedPart.GetItemJson())
	if redactedPart.GetItemJson() != expectedRedactedJSON {
		t.Fatalf("expected canonical redacted_thinking JSON %s, got %s", expectedRedactedJSON, redactedPart.GetItemJson())
	}

	assert.Equal(t, "Checking Paris.", resp.Parts[2].GetText())

	call := resp.Parts[3].GetToolCall()
	require.NotNil(t, call)
	assert.Equal(t, anthropicCapturedCallID, call.GetCallId())
	assert.Equal(t, "get_weather", call.GetName())
	assert.JSONEq(t, `{"city":"Paris"}`, call.GetArgumentsJson())
	assert.Empty(t, call.GetProviderSignature(), "Anthropic uses provider_reasoning, not tool_call.provider_signature")

	assert.Equal(t, "stop", resp.FinishReason)
	assert.True(t, resp.UsageReported)
	assert.Equal(t, int32(70), resp.PromptTokens, "prompt includes input plus cache read and creation tokens")
	assert.Equal(t, int32(55), resp.CompletionTokens)
	assert.Equal(t, int32(125), resp.TotalTokens)
	require.NotNil(t, resp.ThinkingTokens)
	assert.Equal(t, int32(39), *resp.ThinkingTokens)
	require.NotNil(t, resp.CacheTokens)
	assert.Equal(t, int32(5), *resp.CacheTokens)
	assert.Equal(t, int32(1000000), resp.ContextWindow)
	assert.Equal(t, anthropicTestModel, resp.Model)
	assert.Empty(t, resp.ServedModelDigest, "no digest was requested")
	assert.True(t, models.IsSHA256Hex(resp.NormalizedRequestHash))
	assert.True(t, models.IsSHA256Hex(resp.OutputHash))

	egress := resp.HostedEgress
	require.NotNil(t, egress)
	assert.Equal(t, anthropicOrigin(t), egress.GetProviderOrigin())
	assert.Equal(t, constants.InferenceProviderAnthropic, egress.GetProviderClass())
	assert.Equal(t, anthropicTestModel, egress.GetRequestedModel())
	assert.Equal(t, anthropicTestModel, egress.GetModelVersion())
	assert.Equal(t, anthropicTestCreatedAt, egress.GetProviderModelVersion())
	assert.Equal(t, anthropicIdentityDigest(t, anthropicTestModel, anthropicTestCreatedAt), egress.GetModelIdentityDigest())
	assert.Equal(t, anthropicTestResponseID, egress.GetProviderResponseId())
	assert.Equal(t, models.SHA256Hex([]byte(fake.stream)), egress.GetRawResponseDigest())
	assert.Equal(t, uint64(len(fake.stream)), egress.GetRawResponseSizeBytes())
	assert.Equal(t, resp.NormalizedRequestHash, egress.GetNormalizedRequestHash())
	assert.True(t, egress.GetExplicitConsent())
	require.NoError(t, models.ValidateHostedEgress(constants.InferenceProviderAnthropic, anthropicTestModel, "", resp.ToProtoInferenceResult()))
}

func TestAnthropicBackend_KeyTravelsOnlyInTheHeader(t *testing.T) {
	t.Parallel()
	fake := &anthropicFake{stream: anthropicToolCallStream(anthropicTestModel)}
	backend := newFakeAnthropicBackend(t, fake)
	_, err := backend.Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
	require.NoError(t, err)

	require.Len(t, fake.requests, 2, "one metadata read and one generation")
	for _, r := range fake.requests {
		assert.Equal(t, anthropicTestKey, r.Header.Get(constants.HeaderAnthropicAPIKey))
		assert.Equal(t, anthropicAPIVersion, r.Header.Get(constants.HeaderAnthropicVersion))
		assert.NotContains(t, r.URL.String(), anthropicTestKey)
		assert.Empty(t, r.URL.RawQuery)
		assert.Equal(t, "api.anthropic.com", r.URL.Host)
	}
	assert.Equal(t, "/v1/models/"+anthropicTestModel, fake.requests[0].URL.Path)
	assert.Equal(t, "/v1/messages", fake.requests[1].URL.Path)
}

func TestAnthropicBackend_RedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	t.Run("metadata read refuses redirect", func(t *testing.T) {
		fake := &anthropicFake{redirect: "https://elsewhere.example/steal"}
		backend := newFakeAnthropicBackend(t, fake)
		_, err := backend.Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
		require.Len(t, fake.requests, 1, "the redirect target must never be requested")
		assert.Equal(t, "api.anthropic.com", fake.requests[0].URL.Host)
	})
	t.Run("messages post refuses redirect", func(t *testing.T) {
		fake := &anthropicFake{redirect: "https://elsewhere.example/steal", redirectOnPost: true}
		backend := newFakeAnthropicBackend(t, fake)
		_, err := backend.Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
		require.Len(t, fake.requests, 2, "only metadata GET and messages POST may run")
		assert.Equal(t, "api.anthropic.com", fake.requests[1].URL.Host)
	})
}

func TestNewAnthropicBackend_RejectsInvalidCredentialTargets(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{
		"http://api.anthropic.com",
		"https://proxy.example",
		"https://user:password@api.anthropic.com",
		"https://api.anthropic.com/prefix",
		"https://api.anthropic.com?key=secret",
		"https://api.anthropic.com#fragment",
	} {
		t.Run(origin, func(t *testing.T) {
			backend, err := NewAnthropicBackend(origin, anthropicTestKey, testutil.NewTestLogger())
			require.ErrorIs(t, err, constants.ErrInferenceEndpointInvalid)
			assert.Nil(t, backend)
		})
	}
	for _, key := range []string{" ", "abc\ndef", "abc\rdef"} {
		t.Run("invalid key "+fmt.Sprintf("%q", key), func(t *testing.T) {
			backend, err := NewAnthropicBackend(anthropicOrigin(t), key, testutil.NewTestLogger())
			require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
			assert.Nil(t, backend)
		})
	}
	t.Run("nil logger", func(t *testing.T) {
		backend, err := NewAnthropicBackend(anthropicOrigin(t), anthropicTestKey, nil)
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		assert.Nil(t, backend)
	})
}

func TestAnthropicBackend_PinnedDigestBindsCreatedAtAndServedModel(t *testing.T) {
	t.Parallel()
	pinned := anthropicIdentityDigest(t, anthropicTestModel, anthropicTestCreatedAt)

	t.Run("matching created_at serves and reports the digest", func(t *testing.T) {
		fake := &anthropicFake{stream: anthropicToolCallStream(anthropicTestModel)}
		req := anthropicRequest(anthropicTestModel)
		req.ModelDigest = pinned
		resp, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		assert.Equal(t, pinned, resp.ServedModelDigest)
		result := resp.ToProtoInferenceResult()
		require.NoError(t, models.ValidateHostedEgress(constants.InferenceProviderAnthropic, anthropicTestModel, pinned, result))
	})

	t.Run("a re-released created_at fails before generation", func(t *testing.T) {
		fake := &anthropicFake{createdAt: "2026-10-01T00:00:00Z", stream: anthropicToolCallStream(anthropicTestModel)}
		req := anthropicRequest(anthropicTestModel)
		req.ModelDigest = pinned
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
		assert.Zero(t, fake.posts(), "no prompt may leave once the identity changed")
	})

	t.Run("a pinned alias resolved by models API fails closed", func(t *testing.T) {
		fake := &anthropicFake{servedMetaID: "claude-haiku-4-5-20251001", stream: anthropicToolCallStream("claude-haiku-4-5-20251001")}
		req := anthropicRequest("claude-haiku-4-5")
		req.ModelDigest = anthropicIdentityDigest(t, "claude-haiku-4-5", anthropicTestCreatedAt)
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
		assert.Zero(t, fake.posts())
	})

	t.Run("a provider reroute in message_start fails closed", func(t *testing.T) {
		fake := &anthropicFake{stream: anthropicToolCallStream("claude-sonnet-5-5")}
		req := anthropicRequest(anthropicTestModel)
		req.ModelDigest = pinned
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
	})

	t.Run("an unpinned reroute is recorded, not hidden", func(t *testing.T) {
		fake := &anthropicFake{stream: anthropicToolCallStream("claude-sonnet-5-5")}
		resp, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
		require.NoError(t, err)
		assert.Equal(t, anthropicTestModel, resp.Model)
		assert.Equal(t, "claude-sonnet-5-5", resp.HostedEgress.GetModelVersion())
	})
}

func TestAnthropicBackend_GenerateRequestMapping(t *testing.T) {
	t.Parallel()
	fake := &anthropicFake{stream: anthropicTextStream("end_turn", "")}
	req := anthropicRequest(anthropicTestModel)
	parallel := false
	req.ParallelToolCalls = &parallel
	req.StopSequences = []string{"DONE"}
	req.Tools = append(req.Tools, &operatorv1.InferenceToolDeclaration{Name: "other", JsonSchema: `{"type":"object"}`})
	req.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO, AllowedToolNames: []string{"get_weather"}}
	thinkingJSON := fmt.Sprintf(`{"signature":%q,"thinking":"","type":"thinking"}`, anthropicCapturedSignature)
	redactedJSON := fmt.Sprintf(`{"data":%q,"type":"redacted_thinking"}`, anthropicCapturedRedactedData)
	req.Messages = append(req.Messages,
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{ProviderClass: constants.InferenceProviderAnthropic, ItemJson: thinkingJSON}}},
			{Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{ProviderClass: constants.InferenceProviderAnthropic, ItemJson: redactedJSON}}},
			{Part: &operatorv1.InferenceMessagePart_Text{Text: "Checking."}},
			{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c1", Name: "get_weather", ArgumentsJson: `{"city":"Paris"}`}}},
			{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c2", Name: "get_weather", ArgumentsJson: `{"city":"Lyon"}`}}},
		}},
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{CallId: "c1", Name: "get_weather", ResultJson: `{"temp":21}`}}},
		}},
		&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL, Parts: []*operatorv1.InferenceMessagePart{
			{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{CallId: "c2", Name: "get_weather", ResultJson: `"sunny"`}}},
		}},
	)
	_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
	require.NoError(t, err)

	body := fake.lastPostBody(t)
	assert.Equal(t, anthropicTestModel, body["model"])
	assert.InDelta(t, float64(32000), body["max_tokens"], 1e-6, "max_tokens falls back to model metadata max_tokens when req.MaxTokens is 0")
	assert.Equal(t, true, body["stream"])
	assert.Equal(t, []any{map[string]any{"type": "text", "text": "You are terse."}}, body["system"])
	assert.Equal(t, []any{"DONE"}, body["stop_sequences"])
	assert.Equal(t, map[string]any{"type": "adaptive", "display": "omitted"}, body["thinking"])
	for _, forbidden := range []string{"temperature", "top_p", "top_k", "seed", "fallbacks", "eager_input_streaming"} {
		assert.NotContains(t, body, forbidden)
	}

	messages := body["messages"].([]any)
	require.Len(t, messages, 3, "user, assistant, and one user turn holding both tool results")
	assistant := messages[1].(map[string]any)
	assert.Equal(t, "assistant", assistant["role"])
	blocks := assistant["content"].([]any)
	require.Len(t, blocks, 5, "opaque reasoning blocks replay in exact order before text and tool_use")
	assert.Equal(t, map[string]any{"type": "thinking", "thinking": "", "signature": anthropicCapturedSignature}, blocks[0])
	assert.Equal(t, map[string]any{"type": "redacted_thinking", "data": anthropicCapturedRedactedData}, blocks[1])
	assert.Equal(t, map[string]any{"type": "text", "text": "Checking."}, blocks[2])
	assert.Equal(t, map[string]any{"type": "tool_use", "id": "c1", "name": "get_weather", "input": map[string]any{"city": "Paris"}}, blocks[3])
	assert.Equal(t, map[string]any{"type": "tool_use", "id": "c2", "name": "get_weather", "input": map[string]any{"city": "Lyon"}}, blocks[4])

	results := messages[2].(map[string]any)
	assert.Equal(t, "user", results["role"])
	toolResults := results["content"].([]any)
	require.Len(t, toolResults, 2)
	assert.Equal(t, map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": `{"temp":21}`}, toolResults[0])
	assert.Equal(t, map[string]any{"type": "tool_result", "tool_use_id": "c2", "content": `"sunny"`}, toolResults[1])

	tools := body["tools"].([]any)
	require.Len(t, tools, 1, "the allowed-name list narrows the declarations")
	assert.Equal(t, map[string]any{
		"name":         "get_weather",
		"description":  "Get weather",
		"input_schema": map[string]any{"additionalProperties": false, "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []any{"city"}, "type": "object"},
		"strict":       true,
	}, tools[0])
	assert.NotContains(t, tools[0].(map[string]any), "eager_input_streaming")
	assert.Equal(t, map[string]any{"type": "auto", "disable_parallel_tool_use": true}, body["tool_choice"])
}

func TestAnthropicBackend_GenerationControls(t *testing.T) {
	t.Parallel()
	t.Run("explicit max_tokens overrides metadata default", func(t *testing.T) {
		fake := &anthropicFake{stream: anthropicTextStream("end_turn", "")}
		req := anthropicRequest(anthropicTestModel)
		req.MaxTokens = 1024
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		assert.InDelta(t, float64(1024), fake.lastPostBody(t)["max_tokens"], 1e-6)
	})
	t.Run("disabled thinking when model supports disabled", func(t *testing.T) {
		fake := &anthropicFake{disabledThinking: boolPtr(true), stream: anthropicTextStream("end_turn", "")}
		req := anthropicRequest(anthropicTestModel)
		req.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: false}}
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"type": "disabled"}, fake.lastPostBody(t)["thinking"])
	})
	t.Run("non-thinking model omits thinking config", func(t *testing.T) {
		fake := &anthropicFake{thinkingSupported: boolPtr(false), adaptiveThinking: boolPtr(false), stream: anthropicTextStream("end_turn", "")}
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
		require.NoError(t, err)
		assert.NotContains(t, fake.lastPostBody(t), "thinking")
	})
	t.Run("disabled thinking on model that cannot disable is refused", func(t *testing.T) {
		fake := &anthropicFake{disabledThinking: boolPtr(false), stream: anthropicTextStream("end_turn", "")}
		req := anthropicRequest(anthropicTestModel)
		req.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: false}}
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)
		assert.Zero(t, fake.posts())
	})
	t.Run("a JSON response format maps to output_config", func(t *testing.T) {
		fake := &anthropicFake{stream: anthropicTextStream("end_turn", "")}
		req := anthropicRequest(anthropicTestModel)
		req.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: "application/json", JsonSchema: `{"additionalProperties":false,"type":"object"}`}
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"schema": map[string]any{"additionalProperties": false, "type": "object"},
			},
		}, fake.lastPostBody(t)["output_config"])
	})

	unsupportedCases := map[string]struct {
		fake   func() *anthropicFake
		mutate func(*models.GenerateRequest)
	}{
		"temperature": {
			fake:   func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) { r.Temperature = 0.2 },
		},
		"top_p": {
			fake:   func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) { v := float32(0.9); r.TopP = &v },
		},
		"top_k": {
			fake:   func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) { v := int32(40); r.TopK = &v },
		},
		"seed": {
			fake:   func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) { v := int32(42); r.Seed = &v },
		},
		"context limit": {
			fake:   func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) { v := int32(8192); r.ContextLimit = &v },
		},
		"forced tool choice": {
			fake: func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) {
				r.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_REQUIRED}
			},
		},
		"non-JSON response format": {
			fake: func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) {
				r.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: "text/plain"}
			},
		},
		"structured output unsupported by model": {
			fake: func() *anthropicFake {
				return &anthropicFake{structuredOutputs: boolPtr(false), stream: anthropicTextStream("end_turn", "")}
			},
			mutate: func(r *models.GenerateRequest) {
				r.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: "application/json", JsonSchema: `{"type":"object"}`}
			},
		},
		"thinking include_thoughts requested": {
			fake: func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) {
				r.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: true}, IncludeThoughts: true}
			},
		},
		"thinking effort level requested": {
			fake: func() *anthropicFake { return &anthropicFake{stream: anthropicTextStream("end_turn", "")} },
			mutate: func(r *models.GenerateRequest) {
				r.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Level{Level: "high"}}
			},
		},
		"thinking enabled on non-adaptive model": {
			fake: func() *anthropicFake {
				return &anthropicFake{adaptiveThinking: boolPtr(false), disabledThinking: boolPtr(true), stream: anthropicTextStream("end_turn", "")}
			},
			mutate: func(r *models.GenerateRequest) {
				r.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: true}}
			},
		},
	}
	for name, tc := range unsupportedCases {
		t.Run(name+" is refused before the prompt leaves", func(t *testing.T) {
			fake := tc.fake()
			req := anthropicRequest(anthropicTestModel)
			tc.mutate(&req)
			_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
			require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)
			assert.Zero(t, fake.posts())
		})
	}

	t.Run("a model ID that is not a bare ID is refused", func(t *testing.T) {
		fake := &anthropicFake{}
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), anthropicRequest("../files/x"))
		require.ErrorIs(t, err, constants.ErrInferenceModelRefInvalid)
		assert.Empty(t, fake.requests)
	})
	t.Run("a request for another class is refused", func(t *testing.T) {
		req := anthropicRequest(anthropicTestModel)
		req.ProviderClass = ""
		_, err := newFakeAnthropicBackend(t, &anthropicFake{}).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceHostedNotAllowed)
	})
}

func TestAnthropicBackend_RejectsReadableThinkingTextAndInvalidReplay(t *testing.T) {
	t.Parallel()
	t.Run("response rejects readable thinking text", func(t *testing.T) {
		for name, stream := range map[string]string{
			"non-empty thinking in content_block_start": fmt.Sprintf(
				"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null}}\r\n\r\n"+
					"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"secret\",\"signature\":\"\"}}\r\n\r\n",
				anthropicTestModel,
			),
			"non-empty thinking_delta": fmt.Sprintf(
				"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null}}\r\n\r\n"+
					"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\r\n\r\n"+
					"event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"secret\"}}\r\n\r\n",
				anthropicTestModel,
			),
			"thinking block missing signature on stop": fmt.Sprintf(
				"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null}}\r\n\r\n"+
					"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\r\n\r\n"+
					"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":0}\r\n\r\n",
				anthropicTestModel,
			),
			"redacted_thinking with empty data": fmt.Sprintf(
				"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null}}\r\n\r\n"+
					"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"\"}}\r\n\r\n",
				anthropicTestModel,
			),
			"thinking block with unknown field": fmt.Sprintf(
				"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null}}\r\n\r\n"+
					"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\",\"extra\":\"nope\"}}\r\n\r\n",
				anthropicTestModel,
			),
		} {
			t.Run(name, func(t *testing.T) {
				_, err := newFakeAnthropicBackend(t, &anthropicFake{stream: stream}).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
				require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
			})
		}
	})

	t.Run("request replay rejects invalid or readable reasoning and prefill", func(t *testing.T) {
		cases := map[string]func(*models.GenerateRequest){
			"readable thinking text": func(r *models.GenerateRequest) {
				r.Messages = append(r.Messages,
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{
						{Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{
							ProviderClass: constants.InferenceProviderAnthropic,
							ItemJson:      `{"signature":"sig","thinking":"readable","type":"thinking"}`,
						}}},
					}},
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "go"}}}},
				)
			},
			"missing signature": func(r *models.GenerateRequest) {
				r.Messages = append(r.Messages,
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{
						{Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{
							ProviderClass: constants.InferenceProviderAnthropic,
							ItemJson:      `{"signature":"","thinking":"","type":"thinking"}`,
						}}},
					}},
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "go"}}}},
				)
			},
			"unknown field": func(r *models.GenerateRequest) {
				r.Messages = append(r.Messages,
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{
						{Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{
							ProviderClass: constants.InferenceProviderAnthropic,
							ItemJson:      `{"extra":1,"signature":"sig","thinking":"","type":"thinking"}`,
						}}},
					}},
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "go"}}}},
				)
			},
			"wrong provider class": func(r *models.GenerateRequest) {
				r.Messages = append(r.Messages,
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{
						{Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{
							ProviderClass: constants.InferenceProviderOpenAI,
							ItemJson:      `{"signature":"sig","thinking":"","type":"thinking"}`,
						}}},
					}},
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "go"}}}},
				)
			},
			"gemini provider_signature on tool_call": func(r *models.GenerateRequest) {
				r.Messages = append(r.Messages,
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{
						{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c1", Name: "get_weather", ArgumentsJson: `{}`, ProviderSignature: "sig"}}},
					}},
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL, Parts: []*operatorv1.InferenceMessagePart{
						{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{CallId: "c1", Name: "get_weather", ResultJson: `{}`}}},
					}},
				)
			},
			"assistant prefill": func(r *models.GenerateRequest) {
				r.Messages = append(r.Messages,
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "prefill"}}}},
				)
			},
			"mid-history system turn": func(r *models.GenerateRequest) {
				r.Messages = append(r.Messages,
					&operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "late"}}}},
				)
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				fake := &anthropicFake{stream: anthropicTextStream("end_turn", "")}
				req := anthropicRequest(anthropicTestModel)
				mutate(&req)
				_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), req)
				require.ErrorIs(t, err, constants.ErrInferenceMessageInvalid)
				assert.Zero(t, fake.posts())
			})
		}
	})
}

func TestAnthropicBackend_DecodesStopReasonsUsageAndRefusal(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{
		"end_turn":                      "stop",
		"stop_sequence":                 "stop",
		"tool_use":                      "stop",
		"max_tokens":                    "length",
		"model_context_window_exceeded": "length",
		"pause_turn":                    "error",
		"refusal":                       "error",
	} {
		t.Run(reason, func(t *testing.T) {
			stopDetails := ""
			if reason == "refusal" {
				stopDetails = `{"type":"refusal","category":"cyber","explanation":"refusal-explanation-never-logged"}`
			}
			fake := &anthropicFake{stream: anthropicTextStream(reason, stopDetails)}
			backend := newFakeAnthropicBackend(t, fake)
			logger, sink := testutil.NewBufferLogger(t)
			backend.logger = logger

			resp, err := backend.Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
			require.NoError(t, err)
			assert.Equal(t, want, resp.FinishReason)
			require.Len(t, resp.Parts, 3)
			expectedReasoning := `{"signature":"sig-1","thinking":"","type":"thinking"}`
			assert.JSONEq(t, expectedReasoning, resp.Parts[0].GetProviderReasoning().GetItemJson())
			if resp.Parts[0].GetProviderReasoning().GetItemJson() != expectedReasoning {
				t.Fatalf("expected canonical reasoning JSON %s, got %s", expectedReasoning, resp.Parts[0].GetProviderReasoning().GetItemJson())
			}
			assert.Equal(t, "Hello", resp.Parts[1].GetText())
			assert.Equal(t, " there", resp.Parts[2].GetText())
			assert.Equal(t, int32(8), resp.PromptTokens)
			assert.Equal(t, int32(5), resp.CompletionTokens)
			require.NotNil(t, resp.ThinkingTokens)
			assert.Equal(t, int32(3), *resp.ThinkingTokens)
			require.NotNil(t, resp.CacheTokens)
			assert.Equal(t, int32(1), *resp.CacheTokens)
			assertHostedTiming(t, resp)
			assert.Equal(t, operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_UNAVAILABLE, models.ClassifyLoadState(resp.LoadDurationNS))
			if reason == "refusal" {
				assert.Contains(t, sink.String(), "stop_category=cyber")
				assert.Contains(t, sink.String(), "provider_attempt_id=attempt-1")
				assert.NotContains(t, sink.String(), "refusal-explanation-never-logged")
			}
		})
	}
}

func TestAnthropicBackend_StreamFailuresFailClosed(t *testing.T) {
	t.Parallel()
	msgStart := fmt.Sprintf("event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null}}\r\n\r\n", anthropicTestModel)
	for name, tc := range map[string]struct {
		stream string
		want   error
	}{
		"an overloaded error event": {
			"event: error\r\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\r\n\r\n",
			constants.ErrInferenceBackendTransient,
		},
		"an error event": {
			"event: error\r\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"bad\"}}\r\n\r\n",
			constants.ErrInferenceProviderStreamError,
		},
		"missing message_stop": {
			msgStart + "event: message_delta\r\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
		"missing stop_reason": {
			msgStart + "event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
		"missing model in message_start": {
			"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"\",\"content\":[]}}\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
		"changed model in message_delta": {
			msgStart + "event: message_delta\r\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"model\":\"claude-other\"}}\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
		"events after message_stop": {
			anthropicTextStream("end_turn", "") + "event: ping\r\ndata: {\"type\":\"ping\"}\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
		"contradictory stop_reason": {
			msgStart + "event: message_delta\r\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\r\n\r\n" +
				"event: message_delta\r\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
		"out of order block index": {
			msgStart + "event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
		"server tool block rejected": {
			msgStart + "event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srv_1\",\"name\":\"web_search\",\"input\":{}}}\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
		"malformed JSON": {
			"data: {not json\r\n\r\n",
			constants.ErrInferenceProviderResponseInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newFakeAnthropicBackend(t, &anthropicFake{stream: tc.stream}).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
			require.ErrorIs(t, err, tc.want)
		})
	}
	t.Run("parallel calls when the request forbids them", func(t *testing.T) {
		stream := msgStart +
			"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"a\",\"input\":{}}}\r\n\r\n" +
			"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":0}\r\n\r\n" +
			"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t2\",\"name\":\"b\",\"input\":{}}}\r\n\r\n" +
			"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":1}\r\n\r\n" +
			"event: message_delta\r\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\r\n\r\n" +
			"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n"
		req := anthropicRequest(anthropicTestModel)
		parallel := false
		req.ParallelToolCalls = &parallel
		_, err := newFakeAnthropicBackend(t, &anthropicFake{stream: stream}).Generate(testutil.Context(t), req)
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	})
	t.Run("duplicate tool call IDs fail closed", func(t *testing.T) {
		stream := msgStart +
			"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"dup\",\"name\":\"a\",\"input\":{}}}\r\n\r\n" +
			"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":0}\r\n\r\n" +
			"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"dup\",\"name\":\"b\",\"input\":{}}}\r\n\r\n"
		_, err := newFakeAnthropicBackend(t, &anthropicFake{stream: stream}).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	})
}

func TestAnthropicBackend_ProviderErrorsDoNotLogContent(t *testing.T) {
	t.Parallel()
	const sensitive = "private-prompt-and-api-key"
	for name, tc := range map[string]struct {
		fake *anthropicFake
		want error
	}{
		"HTTP error": {
			&anthropicFake{status: http.StatusBadRequest, errorBody: `{"type":"error","error":{"type":"` + sensitive + `","message":"` + sensitive + `"}}`},
			constants.ErrInferenceGenerateFailed,
		},
		"stream error": {
			&anthropicFake{stream: `event: error` + "\r\n" + `data: {"type":"error","error":{"type":"` + sensitive + `","message":"` + sensitive + `"}}` + "\r\n\r\n"},
			constants.ErrInferenceProviderStreamError,
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := newFakeAnthropicBackend(t, tc.fake)
			logger, sink := testutil.NewBufferLogger(t)
			backend.logger = logger
			_, err := backend.Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
			require.ErrorIs(t, err, tc.want)
			assert.NotContains(t, err.Error(), sensitive)
			assert.NotContains(t, sink.String(), sensitive)
			assert.Contains(t, sink.String(), "provider_attempt_id=attempt-1", "failures remain diagnosable by attempt ID")
		})
	}
}

func TestAnthropicBackend_ProgressFailuresDoNotLogContent(t *testing.T) {
	t.Parallel()
	const sensitive = "private-progress-content"
	backend := newFakeAnthropicBackend(t, &anthropicFake{stream: anthropicTextStream("end_turn", "")})
	logger, sink := testutil.NewBufferLogger(t)
	backend.logger = logger
	req := anthropicRequest(anthropicTestModel)
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

func TestAnthropicBackend_RejectsOverflowingTotalTokens(t *testing.T) {
	t.Parallel()
	stream := fmt.Sprintf(
		"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%d,\"output_tokens\":0}}}\r\n\r\n"+
			"event: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"hi\"}}\r\n\r\n"+
			"event: content_block_stop\r\ndata: {\"type\":\"content_block_stop\",\"index\":0}\r\n\r\n"+
			"event: message_delta\r\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\r\n\r\n"+
			"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n",
		anthropicTestModel, math.MaxInt32,
	)
	response, err := newFakeAnthropicBackend(t, &anthropicFake{stream: stream}).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
	require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	assert.Nil(t, response, "overflow must fail before a negative total can be signed")
}

func TestAnthropicBackend_StatusErrorsAreTyped(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"not_found_error":       {http.StatusNotFound, `{"type":"error","error":{"type":"not_found_error","message":"missing"}}`, constants.ErrInferenceModelNotFound},
		"timeout_error":         {http.StatusGatewayTimeout, `{"type":"error","error":{"type":"timeout_error","message":"timed out"}}`, constants.ErrInferenceBackendTimeout},
		"http 408":              {http.StatusRequestTimeout, ``, constants.ErrInferenceBackendTimeout},
		"request_too_large":     {http.StatusRequestEntityTooLarge, `{"type":"error","error":{"type":"request_too_large","message":"too large"}}`, constants.ErrInferenceContextOverflow},
		"prompt too long":       {http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1200000 tokens > 1000000"}}`, constants.ErrInferenceContextOverflow},
		"tools unsupported":     {http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"tool_use is not supported"}}`, constants.ErrInferenceToolsUnsupported},
		"unsupported schema":    {http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"output_config schema uses unsupported constraint"}}`, constants.ErrInferenceCapabilityUnsupported},
		"unsupported thinking":  {http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking mode cannot be disabled"}}`, constants.ErrInferenceCapabilityUnsupported},
		"authentication_error":  {http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`, constants.ErrInferenceBackendUnavailable},
		"billing_error":         {http.StatusPaymentRequired, `{"type":"error","error":{"type":"billing_error","message":"billing"}}`, constants.ErrInferenceBackendUnavailable},
		"permission_error":      {http.StatusForbidden, `{"type":"error","error":{"type":"permission_error","message":"forbidden"}}`, constants.ErrInferenceBackendUnavailable},
		"rate_limit_error":      {http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, constants.ErrInferenceBackendTransient},
		"api_error":             {http.StatusInternalServerError, `{"type":"error","error":{"type":"api_error","message":"oops"}}`, constants.ErrInferenceBackendTransient},
		"overloaded_error":      {529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, constants.ErrInferenceBackendTransient},
		"untyped 503":           {http.StatusServiceUnavailable, ``, constants.ErrInferenceBackendTransient},
		"untyped 401":           {http.StatusUnauthorized, ``, constants.ErrInferenceBackendUnavailable},
		"conflict_error":        {http.StatusConflict, `{"type":"error","error":{"type":"conflict_error","message":"conflict"}}`, constants.ErrInferenceGenerateFailed},
		"other invalid_request": {http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, constants.ErrInferenceGenerateFailed},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newFakeAnthropicBackend(t, &anthropicFake{status: tc.status, errorBody: tc.body}).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
			require.ErrorIs(t, err, tc.want)
		})
	}
	t.Run("an unknown model fails at the metadata read", func(t *testing.T) {
		fake := &anthropicFake{metaCode: http.StatusNotFound}
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
		assert.Zero(t, fake.posts())
	})
	t.Run("an overloaded metadata read is transient", func(t *testing.T) {
		fake := &anthropicFake{metaCode: 529}
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceBackendTransient)
		assert.Zero(t, fake.posts())
	})
	t.Run("an unauthorized metadata read is unavailable", func(t *testing.T) {
		fake := &anthropicFake{metaCode: http.StatusUnauthorized}
		_, err := newFakeAnthropicBackend(t, fake).Generate(testutil.Context(t), anthropicRequest(anthropicTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
	})
}

func TestAnthropicBackend_HostedCaptureRecordsExactStreamAndExcludesErrors(t *testing.T) {
	t.Parallel()
	t.Run("records complete stream without retries", func(t *testing.T) {
		store := &hostedRawResponseRecorder{}
		stream := anthropicToolCallStream(anthropicTestModel)
		fake := &anthropicFake{stream: stream}
		resp, err := newFakeAnthropicBackend(t, fake).Generate(WithAttemptStore(testutil.Context(t), store), anthropicRequest(anthropicTestModel))
		require.NoError(t, err)
		require.Equal(t, 1, store.calls)
		assert.Equal(t, "attempt-1", store.id)
		assert.Equal(t, stream, string(store.raw))
		assert.Equal(t, uint64(len(stream)), resp.HostedEgress.GetRawResponseSizeBytes())
		assert.Equal(t, 1, fake.posts())
	})
	t.Run("records trailing bytes on decode error without retrying", func(t *testing.T) {
		store := &hostedRawResponseRecorder{}
		stream := "data: not-json\r\n\r\n" + anthropicToolCallStream(anthropicTestModel)
		fake := &anthropicFake{stream: stream}
		_, err := newFakeAnthropicBackend(t, fake).Generate(WithAttemptStore(testutil.Context(t), store), anthropicRequest(anthropicTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
		require.Equal(t, 1, store.calls)
		assert.Equal(t, stream, string(store.raw))
		assert.Equal(t, 1, fake.posts())
	})
	t.Run("excludes HTTP error bodies", func(t *testing.T) {
		store := &hostedRawResponseRecorder{}
		fake := &anthropicFake{status: http.StatusTooManyRequests, errorBody: `{"type":"error","error":{"type":"rate_limit_error","message":"secret"}}`}
		_, err := newFakeAnthropicBackend(t, fake).Generate(WithAttemptStore(testutil.Context(t), store), anthropicRequest(anthropicTestModel))
		require.ErrorIs(t, err, constants.ErrInferenceBackendTransient)
		assert.Zero(t, store.calls)
		assert.Equal(t, 1, fake.posts(), "a transient failure is never retried in the client")
	})
}

func TestAnthropicBackend_InventoryPaginatesAndReportsIdentityDigests(t *testing.T) {
	t.Parallel()
	page1 := fmt.Sprintf(
		`{"data":[%s],"has_more":true,"last_id":"claude-opus-5-5"}`,
		anthropicModelMetadata("claude-opus-5-5", anthropicTestCreatedAt, true, true, false, true),
	)
	page2 := fmt.Sprintf(
		`{"data":[%s],"has_more":false,"last_id":"claude-haiku-3-5"}`,
		anthropicModelMetadata("claude-haiku-3-5", "2024-10-22T00:00:00Z", false, false, false, false),
	)
	fake := &anthropicFake{listPages: []string{page1, page2}}
	entries, err := newFakeAnthropicBackend(t, fake).ListProviderModelInventory(testutil.Context(t))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, ProviderModelInventoryEntry{
		ProviderClass:          constants.InferenceProviderAnthropic,
		ServedModelTag:         "claude-opus-5-5",
		ModelDigest:            anthropicIdentityDigest(t, "claude-opus-5-5", anthropicTestCreatedAt),
		ModelFamily:            "claude",
		Format:                 constants.HostedModelFormat,
		ContextLimit:           1000000,
		AdvertisedCapabilities: []string{"completion", "thinking"},
	}, entries[0])
	assert.Equal(t, "claude", entries[1].ModelFamily)
	assert.Equal(t, anthropicIdentityDigest(t, "claude-haiku-3-5", "2024-10-22T00:00:00Z"), entries[1].ModelDigest)
	assert.Equal(t, []string{"completion"}, entries[1].AdvertisedCapabilities)
	require.Len(t, fake.requests, 2)
	assert.Equal(t, "limit=1000", fake.requests[0].URL.RawQuery)
	assert.Equal(t, "after_id=claude-opus-5-5&limit=1000", fake.requests[1].URL.RawQuery)

	t.Run("rejects mismatched pagination cursor", func(t *testing.T) {
		badPage := fmt.Sprintf(
			`{"data":[%s],"has_more":true,"last_id":"wrong-id"}`,
			anthropicModelMetadata("claude-opus-5-5", anthropicTestCreatedAt, true, true, false, true),
		)
		_, err := newFakeAnthropicBackend(t, &anthropicFake{listPages: []string{badPage}}).ListProviderModelInventory(testutil.Context(t))
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	})

	t.Run("rejects duplicate model across pages", func(t *testing.T) {
		dupPage1 := fmt.Sprintf(
			`{"data":[%s],"has_more":true,"last_id":"claude-opus-5-5"}`,
			anthropicModelMetadata("claude-opus-5-5", anthropicTestCreatedAt, true, true, false, true),
		)
		dupPage2 := fmt.Sprintf(
			`{"data":[%s],"has_more":false,"last_id":"claude-opus-5-5"}`,
			anthropicModelMetadata("claude-opus-5-5", anthropicTestCreatedAt, true, true, false, true),
		)
		_, err := newFakeAnthropicBackend(t, &anthropicFake{listPages: []string{dupPage1, dupPage2}}).ListProviderModelInventory(testutil.Context(t))
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	})
}

func TestNewHostedBackend_Anthropic(t *testing.T) {
	t.Run("missing key fails closed", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.AnthropicAPIKey), "")
		_, err := NewHostedBackend(constants.InferenceProviderAnthropic, testutil.NewTestLogger())
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
	})
	t.Run("key with newline fails closed", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.AnthropicAPIKey), anthropicTestKey+"\n")
		_, err := NewHostedBackend(constants.InferenceProviderAnthropic, testutil.NewTestLogger())
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)
	})
	t.Run("anthropic builds against its fixed origin", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.AnthropicAPIKey), anthropicTestKey)
		backend, err := NewHostedBackend(constants.InferenceProviderAnthropic, testutil.NewTestLogger())
		require.NoError(t, err)
		assert.Equal(t, constants.InferenceProviderAnthropic, backend.ProviderClass())
		assert.Equal(t, anthropicOrigin(t), backend.(*AnthropicBackend).origin)
	})
}
