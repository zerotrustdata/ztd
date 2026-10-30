// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ProviderRequestTimeout is the single request deadline authority for calls
// to the remote inference provider. The gateway's inference dispatch deadline
// (dispatch.RequestDeadline) is derived from it so the dispatch wait always
// outlives an in-flight provider call; a dispatch that outlives its deadline
// reports an unknown remote outcome rather than a clean timeout.
const ProviderRequestTimeout = 5 * time.Minute

// ProviderStatusTimeout bounds an explicit read-only readiness check against
// the remote provider. It is deliberately shorter than
// ProviderRequestTimeout: a /api/tags round trip is cheap, and an
// status request should finish before the generation deadline. Provider
// availability does not gate Operator startup.
const ProviderStatusTimeout = 30 * time.Second

// providerUnloadRecheckInterval spaces /api/ps reads while a released model's
// runner is still stopping.
const providerUnloadRecheckInterval = 250 * time.Millisecond

// defaultMaxResponseBytes bounds every response body read from the remote
// provider. A streaming chat response contains the full generated output;
// the bound is generous enough for any configured num_predict while still
// failing closed on a misbehaving or hostile endpoint.
const defaultMaxResponseBytes int64 = 32 << 20

// maxErrorBodyBytes bounds the error-body drain on non-OK responses. The
// body is drained only to allow connection reuse; provider error text is
// never included in returned errors or logs because it can echo prompt
// material or carry provider internals.
const maxErrorBodyBytes int64 = 4 << 10

// OllamaBackend implements Backend as an HTTP client to Ollama's /api/chat
// endpoint. It constructs the Ollama chat request from GenerateRequest,
// sends it over HTTP to the configured remote Ollama endpoint, and parses
// the response. It uses context.Context for cancellation and timeout. No
// process management, no goroutine supervision, no health-check loop —
// Ollama is a remote daemon that the deployment owns and monitors.
type OllamaBackend struct {
	providerHTTP
	logger *slog.Logger
}

// NewOllamaBackend constructs an OllamaBackend for the given provider
// endpoint. The endpoint is parsed and validated once: it must be an
// absolute http or https URL with a host. A path prefix is preserved, so
// endpoints behind a reverse-proxied prefix such as
// http://host/ollama resolve to http://host/ollama/api/chat.
func NewOllamaBackend(endpoint string, logger *slog.Logger) (*OllamaBackend, error) {
	provider, err := newProviderHTTP("ollama_backend", endpoint)
	if err != nil {
		return nil, err
	}
	return &OllamaBackend{providerHTTP: provider, logger: logger}, nil
}

// ollamaChatRequest is the request body for Ollama's /api/chat endpoint.
type ollamaChatRequest struct {
	Model     string              `json:"model"`
	Messages  []ollamaChatMessage `json:"messages"`
	Tools     []ollamaTool        `json:"tools,omitempty"`
	Stream    bool                `json:"stream"`
	Think     json.RawMessage     `json:"think,omitempty"`
	Format    json.RawMessage     `json:"format,omitempty"`
	Options   ollamaChatOptions   `json:"options"`
	KeepAlive json.RawMessage     `json:"keep_alive,omitempty"`
}

func encodeOllamaKeepAlive(value string) (json.RawMessage, error) {
	switch value {
	case "":
		return nil, nil
	case "-1":
		return json.RawMessage("-1"), nil
	case "0":
		return json.RawMessage("0"), nil
	default:
		if _, err := time.ParseDuration(value); err != nil {
			return nil, fmt.Errorf("ollama_backend: keep_alive: %w", constants.ErrInferenceGenerationOptionsInvalid)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("ollama_backend: keep_alive: %w", err)
		}
		return json.RawMessage(encoded), nil
	}
}

type ollamaChatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Thinking   string           `json:"thinking,omitempty"`
	ToolCalls  []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName   string           `json:"tool_name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolCall struct {
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ollamaChatOptions struct {
	Temperature float32  `json:"temperature,omitempty"`
	NumPredict  int32    `json:"num_predict,omitempty"`
	TopP        *float32 `json:"top_p,omitempty"`
	TopK        *int32   `json:"top_k,omitempty"`
	Seed        *int32   `json:"seed,omitempty"`
	Stop        []string `json:"stop,omitempty"`
	NumCtx      *int32   `json:"num_ctx,omitempty"`
}

// ollamaChatResponse is one response event from Ollama's /api/chat endpoint.
type ollamaChatResponse struct {
	// Error is set on a stream event Ollama sends instead of a done event when
	// generation fails after the request was accepted, such as when its
	// tool-call parser rejects the model's output.
	Error                 string            `json:"error"`
	Model                 string            `json:"model"`
	Message               ollamaChatMessage `json:"message"`
	Done                  bool              `json:"done"`
	DoneReason            string            `json:"done_reason"`
	PromptEvalCount       *int32            `json:"prompt_eval_count"`
	PromptEvalCachedCount *int32            `json:"prompt_eval_cached_count"`
	EvalCount             *int32            `json:"eval_count"`
	LoadDuration          *int64            `json:"load_duration"`
	PromptEvalDuration    *int64            `json:"prompt_eval_duration"`
	EvalDuration          *int64            `json:"eval_duration"`
	TotalDuration         *int64            `json:"total_duration"`
}

// ollamaTagsResponse is the response body from Ollama's /api/tags endpoint.
type ollamaTagsResponse struct {
	Models []ollamaTagModel `json:"models"`
}

type ollamaTagModel struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   uint64 `json:"size"`
}

type ollamaModelReleaseRequest struct {
	Model     string          `json:"model"`
	KeepAlive json.RawMessage `json:"keep_alive"`
	Stream    bool            `json:"stream"`
}

type ollamaModelReleaseResponse struct {
	Model      string `json:"model"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
}

// ReleaseModels unloads each requested model and then verifies the complete
// requested set is absent from provider residency. Keeping this postcondition
// in the same governed operator command prevents consensus inference for a
// later release or verification command from reloading an earlier model.
func (b *OllamaBackend) ReleaseModels(ctx context.Context, models []string) error {
	if len(models) == 0 {
		return fmt.Errorf("ollama_backend: release models: %w: at least one model is required", constants.ErrInferenceModelTagInvalid)
	}
	for _, model := range models {
		if err := operatorcapability.ValidateServedModelTag(model); err != nil {
			return fmt.Errorf("ollama_backend: release models: %w", err)
		}
	}
	for _, model := range models {
		if err := b.releaseModel(ctx, model); err != nil {
			return err
		}
	}
	// Ollama acknowledges keep_alive 0 with done_reason unload before its
	// scheduler stops the runner, so /api/ps is the completion signal.
	return awaitModelsAbsent(ctx, b.component, b.ReadResidency, models)
}

func (b *OllamaBackend) releaseModel(ctx context.Context, model string) error {
	if model == "" {
		return fmt.Errorf("ollama_backend: release model: %w", constants.ErrInferenceModelTagInvalid)
	}
	body, err := json.Marshal(ollamaModelReleaseRequest{
		Model:     model,
		KeepAlive: json.RawMessage(constants.InferenceUnloadKeepAlive),
		Stream:    false,
	})
	if err != nil {
		return fmt.Errorf("ollama_backend: release model: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL("api", "generate"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama_backend: release model: build request: %w", err)
	}
	httpReq.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return transportError(ctx, "ollama_backend", "release model", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("ollama_backend: release model: %w: model %q", constants.ErrInferenceModelNotFound, model)
		}
		return fmt.Errorf("ollama_backend: release model: %w: status %d", constants.ErrInferenceModelReleaseFailed, resp.StatusCode)
	}
	var releaseResp ollamaModelReleaseResponse
	if err := b.decodeResponse("release model", resp.Body, &releaseResp); err != nil {
		return err
	}
	if releaseResp.Model != model || !releaseResp.Done || releaseResp.DoneReason != constants.OllamaDoneReasonUnload {
		return fmt.Errorf("ollama_backend: release model: %w", constants.ErrInferenceProviderResponseInvalid)
	}
	return nil
}

// Generate sends a generation request to Ollama's /api/chat endpoint and
// returns the generated text, usage metadata, and finish reason.
func (b *OllamaBackend) Generate(ctx context.Context, req models.GenerateRequest) (*models.GenerateResponse, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("ollama_backend: generate: %w", constants.ErrInferenceModelRefInvalid)
	}
	if req.ModelDigest != "" {
		digest, err := b.modelDigest(ctx, req.Model)
		if err != nil {
			return nil, err
		}
		if digest != req.ModelDigest {
			return nil, fmt.Errorf("ollama_backend: generate: %w", constants.ErrInferenceModelDigestMismatch)
		}
	}

	messages, err := inferenceMessagesToOllama(req.Messages)
	if err != nil {
		return nil, fmt.Errorf("ollama_backend: generate: messages: %w", err)
	}
	tools, err := inferenceToolsToOllama(req.Tools, req.ToolChoice)
	if err != nil {
		return nil, fmt.Errorf("ollama_backend: generate: tools: %w", err)
	}
	var think json.RawMessage
	if req.Thinking != nil {
		switch mode := req.Thinking.GetMode().(type) {
		case *operatorv1.InferenceThinkingControl_Enabled:
			think = json.RawMessage("false")
			if mode.Enabled {
				think = json.RawMessage("true")
			}
		case *operatorv1.InferenceThinkingControl_Level:
			return nil, fmt.Errorf("ollama_backend: generate: thinking level %q: %w", mode.Level, constants.ErrInferenceCapabilityUnsupported)
		default:
			return nil, fmt.Errorf("ollama_backend: generate: thinking: %w", constants.ErrInferenceGenerationOptionsInvalid)
		}
	}
	var format json.RawMessage
	if req.ResponseFormat != nil {
		if req.ResponseFormat.GetMediaType() != inferenceJSONMediaType {
			return nil, fmt.Errorf("ollama_backend: generate: response format: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		if err := validateInferenceToolSchema(req.ResponseFormat.GetJsonSchema()); err != nil {
			return nil, fmt.Errorf("ollama_backend: generate: response format: %w", err)
		}
		format = json.RawMessage(req.ResponseFormat.GetJsonSchema())
	}
	keepAlive, err := encodeOllamaKeepAlive(req.KeepAlive)
	if err != nil {
		return nil, fmt.Errorf("ollama_backend: generate: %w", err)
	}
	chatReq := ollamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Tools:    tools,
		Stream:   true,
		Think:    think,
		Format:   format,
		Options: ollamaChatOptions{
			Temperature: req.Temperature,
			NumPredict:  resolveOutputTokenLimit(req.MaxTokens),
			TopP:        req.TopP,
			TopK:        req.TopK,
			Seed:        req.Seed,
			Stop:        req.StopSequences,
			NumCtx:      req.ContextLimit,
		},
		KeepAlive: keepAlive,
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("ollama_backend: marshal request: %w", err)
	}
	normalizedRequestHash := models.SHA256Hex(body)

	// Streaming scrubs at the source. Without an enabled scrubber the stream is refused before the
	// provider is called, so no live output can leave unscrubbed.
	var stream *streamSegmenter
	if req.Stream {
		if reporter := ProgressReporterFromContext(ctx); reporter != nil {
			stream, err = newStreamSegmenter(req.Scrubber, reporter, req.ProviderAttemptID)
			if err != nil {
				return nil, fmt.Errorf("ollama_backend: generate: %w", err)
			}
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL("api", "chat"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama_backend: build request: %w", err)
	}
	httpReq.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)

	requestStartedAt := time.Now()
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, transportError(ctx, "ollama_backend", "generate", err)
	}
	defer resp.Body.Close()

	// Ingestion owns the bounded raw bytes independently of parsing and delivery.
	// A slow reporter or invalid frame cannot truncate the provider artifact.
	capture := newProviderResponseCapture(b.maxResponseBytes, requestStartedAt)
	go capture.receive(resp.Body)
	var chatResp ollamaChatResponse
	var parts []*operatorv1.InferenceResponsePart
	var timeToFirstToken *int64
	var decodeErr error
	if resp.StatusCode == http.StatusOK {
		allowParallelToolCalls := req.ParallelToolCalls == nil || *req.ParallelToolCalls
		chatResp, parts, timeToFirstToken, decodeErr = b.decodeChatStream(ctx, capture, capture.arrivalTime, allowParallelToolCalls, req.ContextLimit, req.ProviderAttemptID, stream)
	}
	responseBody, readErr := capture.result()
	// Only a 200 stream is bound to the attempt record, and only by digest and
	// size. A non-200 body can echo prompt material and is never recorded.
	if store := attemptStoreFromContext(ctx); store != nil && resp.StatusCode == http.StatusOK {
		if err := store.RecordRawResponse(context.WithoutCancel(ctx), req.ProviderAttemptID, responseBody); err != nil {
			return nil, errors.Join(readErr, fmt.Errorf("ollama_backend: record raw response digest: %w", err))
		}
	}
	if readErr != nil {
		return nil, transportError(ctx, "ollama_backend", "generate", readErr)
	}
	if int64(len(responseBody)) > b.maxResponseBytes {
		return nil, fmt.Errorf("ollama_backend: generate: %w: response exceeds %d bytes", constants.ErrInferenceProviderResponseInvalid, b.maxResponseBytes)
	}

	if resp.StatusCode != http.StatusOK {
		errorBody := responseBody
		var apiError struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(errorBody, &apiError)
		if b.logger != nil {
			b.logger.Warn("Ollama generation failed",
				"status", resp.StatusCode,
				"model", req.Model,
				"error", apiError.Error,
			)
		}
		switch {
		case resp.StatusCode == http.StatusRequestTimeout:
			return nil, fmt.Errorf("ollama_backend: generate: %w", constants.ErrInferenceBackendTimeout)
		case resp.StatusCode == http.StatusNotFound:
			return nil, fmt.Errorf("ollama_backend: generate: %w: model %q", constants.ErrInferenceModelNotFound, req.Model)
		case ollamaErrorIndicatesCapabilityUnsupported(resp.StatusCode, errorBody):
			return nil, fmt.Errorf("ollama_backend: generate: %w", constants.ErrInferenceToolsUnsupported)
		case ollamaCapabilityRejection(resp.StatusCode, errorBody):
			return nil, fmt.Errorf("ollama_backend: generate: %w", constants.ErrInferenceCapabilityUnsupported)
		default:
			return nil, fmt.Errorf("ollama_backend: generate: %w: status %d", constants.ErrInferenceGenerateFailed, resp.StatusCode)
		}
	}

	if decodeErr != nil {
		return nil, decodeErr
	}

	finishReason := chatResp.DoneReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if (chatResp.PromptEvalCount == nil) != (chatResp.EvalCount == nil) {
		return nil, fmt.Errorf("ollama_backend: generate: token usage: %w", constants.ErrInferenceProviderResponseInvalid)
	}
	promptTokens, completionTokens := int32(0), int32(0)
	usageReported := chatResp.PromptEvalCount != nil
	if usageReported {
		promptTokens = *chatResp.PromptEvalCount
		completionTokens = *chatResp.EvalCount
		if promptTokens < 0 || completionTokens < 0 {
			return nil, fmt.Errorf("ollama_backend: generate: token usage: %w", constants.ErrInferenceProviderResponseInvalid)
		}
	}
	// Ollama truncates an over-long prompt to num_ctx and still answers 200, so
	// a prompt that fills the window is indistinguishable from a silently
	// truncated one. Fail typed rather than score or act on a degraded answer.
	// The raw response is already preserved above.
	if ollamaPromptFillsContext(chatResp.PromptEvalCount, req.ContextLimit) {
		return nil, ollamaContextOverflowError(promptTokens, *req.ContextLimit)
	}
	cacheTokens := chatResp.PromptEvalCachedCount
	if cacheTokens != nil && *cacheTokens < 0 {
		return nil, fmt.Errorf("ollama_backend: generate: token usage: %w", constants.ErrInferenceProviderResponseInvalid)
	}
	timings := []*int64{chatResp.LoadDuration, chatResp.PromptEvalDuration, chatResp.EvalDuration, chatResp.TotalDuration, timeToFirstToken}
	timingSource := operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_UNSPECIFIED
	for _, duration := range timings {
		if duration == nil {
			continue
		}
		if *duration < 0 {
			return nil, fmt.Errorf("ollama_backend: generate: timing: %w", constants.ErrInferenceProviderResponseInvalid)
		}
		timingSource = operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER
	}

	outputHash, err := models.ComputeInferenceOutputHash(parts, finishReason)
	if err != nil {
		return nil, fmt.Errorf("ollama_backend: generate: %w", err)
	}
	servedModelDigest := ""
	if req.ModelDigest != "" {
		servedModelDigest, err = b.modelDigest(ctx, chatResp.Model)
		if err != nil {
			return nil, err
		}
		if servedModelDigest != req.ModelDigest {
			return nil, fmt.Errorf("ollama_backend: generate: %w", constants.ErrInferenceModelDigestMismatch)
		}
	}
	return &models.GenerateResponse{
		Parts:                 parts,
		PromptTokens:          promptTokens,
		CompletionTokens:      completionTokens,
		TotalTokens:           promptTokens + completionTokens,
		UsageReported:         usageReported,
		CacheTokens:           cacheTokens,
		LoadDurationNS:        chatResp.LoadDuration,
		PromptEvalDurationNS:  chatResp.PromptEvalDuration,
		GenerationDurationNS:  chatResp.EvalDuration,
		TotalDurationNS:       chatResp.TotalDuration,
		TimeToFirstTokenNS:    timeToFirstToken,
		TimingSource:          timingSource,
		FinishReason:          finishReason,
		Model:                 chatResp.Model,
		ServedModelDigest:     servedModelDigest,
		NormalizedRequestHash: normalizedRequestHash,
		OutputHash:            outputHash,
	}, nil
}

// ollamaPromptFillsContext reports whether the evaluated prompt reached the
// requested num_ctx. It is false when either value is unreported, because the
// provider's own default window is then unknown.
func ollamaPromptFillsContext(promptEvalCount, contextLimit *int32) bool {
	return promptEvalCount != nil && contextLimit != nil && *promptEvalCount >= *contextLimit
}

func ollamaContextOverflowError(promptTokens, contextLimit int32) error {
	return fmt.Errorf("ollama_backend: generate: %w: prompt_tokens=%d context_limit=%d", constants.ErrInferenceContextOverflow, promptTokens, contextLimit)
}

func inferenceMessagesToOllama(messages []*operatorv1.InferenceMessage) ([]ollamaChatMessage, error) {
	result := make([]ollamaChatMessage, 0, len(messages))
	for messageIndex, message := range messages {
		if message == nil || len(message.GetParts()) == 0 {
			return nil, fmt.Errorf("%w: message %d", constants.ErrInferenceMessageInvalid, messageIndex)
		}
		role, err := inferenceMessageRoleToOllama(message.GetRole())
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", messageIndex, err)
		}
		mapped := ollamaChatMessage{Role: role}
		for partIndex, part := range message.GetParts() {
			if part == nil || part.GetPart() == nil {
				return nil, fmt.Errorf("%w: message %d part %d", constants.ErrInferenceMessageInvalid, messageIndex, partIndex)
			}
			switch value := part.GetPart().(type) {
			case *operatorv1.InferenceMessagePart_Text:
				if message.GetRole() == operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL {
					return nil, fmt.Errorf("%w: message %d text part", constants.ErrInferenceMessageInvalid, messageIndex)
				}
				mapped.Content += value.Text
			case *operatorv1.InferenceMessagePart_ToolCall:
				if message.GetRole() != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT || value.ToolCall == nil || value.ToolCall.GetName() == "" {
					return nil, fmt.Errorf("%w: message %d tool call", constants.ErrInferenceMessageInvalid, messageIndex)
				}
				arguments, err := canonicalizeJSON(value.ToolCall.GetArgumentsJson(), true, nil)
				if err != nil {
					return nil, fmt.Errorf("%w: message %d tool call arguments", constants.ErrInferenceMessageInvalid, messageIndex)
				}
				mapped.ToolCalls = append(mapped.ToolCalls, ollamaToolCall{
					ID:   value.ToolCall.GetCallId(),
					Type: "function",
					Function: ollamaToolFunction{
						Name:      value.ToolCall.GetName(),
						Arguments: json.RawMessage(arguments),
					},
				})
			case *operatorv1.InferenceMessagePart_ToolResult:
				if message.GetRole() != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL || value.ToolResult == nil || value.ToolResult.GetName() == "" {
					return nil, fmt.Errorf("%w: message %d tool result", constants.ErrInferenceMessageInvalid, messageIndex)
				}
				resultJSON, err := canonicalizeJSON(value.ToolResult.GetResultJson(), false, nil)
				if err != nil {
					return nil, fmt.Errorf("%w: message %d tool result JSON", constants.ErrInferenceMessageInvalid, messageIndex)
				}
				result = append(result, ollamaChatMessage{
					Role:       "tool",
					Content:    resultJSON,
					ToolName:   value.ToolResult.GetName(),
					ToolCallID: value.ToolResult.GetCallId(),
				})
			default:
				return nil, fmt.Errorf("%w: message %d part %d", constants.ErrInferenceMessageInvalid, messageIndex, partIndex)
			}
		}
		if message.GetRole() != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL {
			result = append(result, mapped)
		}
	}
	return result, nil
}

func inferenceMessageRoleToOllama(role operatorv1.InferenceMessageRole) (string, error) {
	switch role {
	case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM:
		return "system", nil
	case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER:
		return "user", nil
	case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT:
		return "assistant", nil
	case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL:
		return "tool", nil
	default:
		return "", constants.ErrInferenceMessageInvalid
	}
}

func inferenceToolsToOllama(tools []*operatorv1.InferenceToolDeclaration, choice *operatorv1.InferenceToolChoice) ([]ollamaTool, error) {
	allowed := make(map[string]struct{})
	if choice != nil {
		if choice.GetMode() != operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO {
			return nil, constants.ErrInferenceCapabilityUnsupported
		}
		for _, name := range choice.GetAllowedToolNames() {
			allowed[name] = struct{}{}
		}
	}
	result := make([]ollamaTool, 0, len(tools))
	for toolIndex, tool := range tools {
		if tool == nil || tool.GetName() == "" {
			return nil, fmt.Errorf("%w: tool %d", constants.ErrInferenceToolSchemaInvalid, toolIndex)
		}
		if err := validateInferenceToolSchema(tool.GetJsonSchema()); err != nil {
			return nil, fmt.Errorf("%w: tool %d: %w", constants.ErrInferenceToolSchemaInvalid, toolIndex, err)
		}
		if len(allowed) > 0 {
			if _, ok := allowed[tool.GetName()]; !ok {
				continue
			}
		}
		result = append(result, ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        tool.GetName(),
				Description: tool.GetDescription(),
				Parameters:  json.RawMessage(tool.GetJsonSchema()),
			},
		})
	}
	return result, nil
}

func ollamaResponseParts(message ollamaChatMessage, allowParallelToolCalls bool) ([]*operatorv1.InferenceResponsePart, error) {
	if !allowParallelToolCalls && len(message.ToolCalls) > 1 {
		return nil, constants.ErrInferenceProviderResponseInvalid
	}
	parts := make([]*operatorv1.InferenceResponsePart, 0, len(message.ToolCalls)+2)
	if message.Thinking != "" {
		parts = append(parts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: message.Thinking}})
	}
	if message.Content != "" {
		parts = append(parts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: message.Content}})
	}
	for callIndex, call := range message.ToolCalls {
		if call.Function.Name == "" || len(call.Function.Arguments) == 0 {
			return nil, fmt.Errorf("%w: tool call %d", constants.ErrInferenceProviderResponseInvalid, callIndex)
		}
		arguments, err := canonicalizeJSON(string(call.Function.Arguments), true, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: tool call %d arguments", constants.ErrInferenceProviderResponseInvalid, callIndex)
		}
		parts = append(parts, &operatorv1.InferenceResponsePart{
			Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
				CallId:        call.ID,
				Name:          call.Function.Name,
				ArgumentsJson: arguments,
			}},
		})
	}
	return parts, nil
}

const (
	inlineThinkOpenTag  = "<think>"
	inlineThinkCloseTag = "</think>"
)

type inlineThinkPhase uint8

const (
	// inlineThinkLead holds content until it shows whether it leads with <think>.
	inlineThinkLead inlineThinkPhase = iota
	// inlineThinkInside routes content to thinking until </think>.
	inlineThinkInside
	// inlineThinkTrim drops whitespace that follows </think>.
	inlineThinkTrim
	// inlineThinkPassthrough passes content through unchanged. It is terminal.
	inlineThinkPassthrough
)

// inlineThinkSplitter moves a leading <think>...</think> block that some Ollama
// models emit inline in message.content into thinking, so graders never read
// reasoning as the answer. It is per stream and decides only at the start.
type inlineThinkSplitter struct {
	phase inlineThinkPhase
	// buf is undecided leading content, or a possible prefix of a close tag
	// while inside the block. It is the only content held back from output.
	buf string
}

// rewrite splits message.Content into Thinking and Content in place. A provider
// Thinking field disables splitting, so reasoning is never split twice. done
// marks the terminal event, after which any buffered content is flushed.
func (s *inlineThinkSplitter) rewrite(message *ollamaChatMessage, done bool) {
	if message.Thinking != "" {
		message.Content = s.disable() + message.Content
		return
	}
	thinking, text := s.push(message.Content)
	if done {
		flushedThinking, flushedText := s.flush()
		thinking += flushedThinking
		text += flushedText
	}
	message.Thinking = thinking
	message.Content = text
}

// push consumes one content chunk and returns the thinking and text it resolves.
func (s *inlineThinkSplitter) push(content string) (string, string) {
	var thinking, text strings.Builder
	in := s.buf + content
	s.buf = ""
	for {
		switch s.phase {
		case inlineThinkLead:
			trimmed := strings.TrimLeftFunc(in, unicode.IsSpace)
			switch {
			case strings.HasPrefix(trimmed, inlineThinkOpenTag):
				s.phase = inlineThinkInside
				in = trimmed[len(inlineThinkOpenTag):]
			case trimmed == "" || strings.HasPrefix(inlineThinkOpenTag, trimmed):
				// Undecided: keep everything, including whitespace, until it resolves.
				s.buf = in
				return thinking.String(), text.String()
			default:
				s.phase = inlineThinkPassthrough
				text.WriteString(in)
				return thinking.String(), text.String()
			}
		case inlineThinkInside:
			idx := strings.Index(in, inlineThinkCloseTag)
			if idx < 0 {
				held := partialTagSuffixLen(in, inlineThinkCloseTag)
				thinking.WriteString(in[:len(in)-held])
				s.buf = in[len(in)-held:]
				return thinking.String(), text.String()
			}
			thinking.WriteString(in[:idx])
			s.phase = inlineThinkTrim
			in = in[idx+len(inlineThinkCloseTag):]
		case inlineThinkTrim:
			in = strings.TrimLeftFunc(in, unicode.IsSpace)
			if in == "" {
				return thinking.String(), text.String()
			}
			s.phase = inlineThinkPassthrough
			text.WriteString(in)
			return thinking.String(), text.String()
		default:
			text.WriteString(in)
			return thinking.String(), text.String()
		}
	}
}

// flush releases buffered content at stream end. An unterminated block is all
// thinking, and undecided content is text.
func (s *inlineThinkSplitter) flush() (string, string) {
	buf := s.buf
	phase := s.phase
	s.buf = ""
	s.phase = inlineThinkPassthrough
	switch phase {
	case inlineThinkInside:
		return buf, ""
	case inlineThinkLead:
		return "", buf
	default:
		return "", ""
	}
}

// disable ends splitting and returns buffered content, which then passes through as text.
func (s *inlineThinkSplitter) disable() string {
	buf := s.buf
	s.buf = ""
	s.phase = inlineThinkPassthrough
	return buf
}

// partialTagSuffixLen returns the length of the longest proper prefix of tag
// that s ends with, so that a tag split across chunks can be held back.
func partialTagSuffixLen(s, tag string) int {
	for n := min(len(tag)-1, len(s)); n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}

// responseRead records arrival times without interpreting provider payloads.
// Decoder offsets later locate the delivery time of the first content event.
type responseRead struct {
	endOffset int64
	elapsedNS int64
}

func (b *OllamaBackend) decodeChatStream(
	ctx context.Context,
	body io.Reader,
	arrivalTime func(int64) *int64,
	allowParallelToolCalls bool,
	contextLimit *int32,
	providerAttemptID string,
	stream *streamSegmenter,
) (ollamaChatResponse, []*operatorv1.InferenceResponsePart, *int64, error) {
	decoder := json.NewDecoder(body)
	parts := make([]*operatorv1.InferenceResponsePart, 0)
	var terminal *ollamaChatResponse
	var servedModel string
	var timeToFirstToken *int64
	toolCallCount := 0
	inlineThink := &inlineThinkSplitter{}
	for {
		var event ollamaChatResponse
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: %w: %w", constants.ErrInferenceProviderResponseInvalid, err)
		}
		if event.Error != "" {
			// The provider's message stays in this log. It can echo model output,
			// so it is never signed or returned.
			b.logger.Warn("Ollama ended the response stream with an error",
				"provider_attempt_id", providerAttemptID,
				"model", servedModel,
				"error", event.Error,
			)
			return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: %w", constants.ErrInferenceProviderStreamError)
		}
		if terminal != nil {
			return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: terminal event: %w", constants.ErrInferenceProviderResponseInvalid)
		}
		if event.Model != "" {
			if servedModel != "" && event.Model != servedModel {
				return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: served model changed: %w", constants.ErrInferenceProviderResponseInvalid)
			}
			servedModel = event.Model
		}
		toolCallCount += len(event.Message.ToolCalls)
		if !allowParallelToolCalls && toolCallCount > 1 {
			return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: response parts: %w", constants.ErrInferenceProviderResponseInvalid)
		}
		// The terminal event also flushes buffered content, so it is emitted with the done event's parts.
		inlineThink.rewrite(&event.Message, event.Done)
		eventParts, err := ollamaResponseParts(event.Message, allowParallelToolCalls)
		if err != nil {
			return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: response parts: %w", err)
		}
		if len(eventParts) > 0 && timeToFirstToken == nil {
			timeToFirstToken = arrivalTime(decoder.InputOffset())
		}
		if stream != nil {
			released, err := stream.accept(ctx, eventParts)
			if err != nil {
				return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: stream segments: %w", err)
			}
			eventParts = released
			if err := stream.publish(released, servedModel, timeToFirstToken); err != nil {
				b.logger.Warn("Inference progress delivery failed", "provider_attempt_id", providerAttemptID, "error", err)
			}
		}
		parts = append(parts, eventParts...)
		if event.Done {
			terminal = &event
		}
	}
	if terminal != nil && stream != nil {
		// The buffered tail is released only once the terminal event has arrived.
		tail := stream.finish(ctx)
		if err := stream.publish(tail, servedModel, timeToFirstToken); err != nil {
			b.logger.Warn("Inference progress delivery failed", "provider_attempt_id", providerAttemptID, "error", err)
		}
		parts = append(parts, tail...)
	}
	switch {
	case terminal == nil:
		return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: terminal event: stream ended without a done event: %w", constants.ErrInferenceProviderResponseInvalid)
	case terminal.Model == "":
		return ollamaChatResponse{}, nil, nil, fmt.Errorf("ollama_backend: generate: terminal event: done event carries no model: %w", constants.ErrInferenceProviderResponseInvalid)
	case len(parts) == 0 && ollamaPromptFillsContext(terminal.PromptEvalCount, contextLimit):
		// An over-long prompt that Ollama truncated surfaces as an empty 200;
		// classify it as overflow, not as the model's empty answer.
		return ollamaChatResponse{}, nil, nil, ollamaContextOverflowError(*terminal.PromptEvalCount, *contextLimit)
	}
	// Otherwise an empty completion is the served model's answer (often a
	// malformed tool call the provider parser dropped). It returns as an empty
	// result, so the caller grades it rather than blaming the provider.
	return *terminal, parts, timeToFirstToken, nil
}

func (b *OllamaBackend) modelDigest(ctx context.Context, model string) (string, error) {
	tags, err := b.listTags(ctx, "model digest")
	if err != nil {
		return "", err
	}
	for _, candidate := range tags {
		if candidate.Name != model {
			continue
		}
		digest, err := models.NormalizeProviderModelDigest(candidate.Digest)
		if err != nil {
			return "", fmt.Errorf("ollama_backend: model digest: %w", err)
		}
		return digest, nil
	}
	return "", fmt.Errorf("ollama_backend: model digest: %w", constants.ErrInferenceModelNotFound)
}

// Status queries Ollama's /api/tags endpoint to verify the daemon is
// reachable and lists the models available in its store.
func (b *OllamaBackend) Status(ctx context.Context) (*models.BackendStatus, error) {
	tags, err := b.listTags(ctx, "status")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	return &models.BackendStatus{Available: true, Models: names}, nil
}

// ListModelVariants queries Ollama's /api/tags endpoint and returns every
// installed model as a typed registry variant with normalized digests.
func (b *OllamaBackend) ListModelVariants(ctx context.Context) ([]*operatorv1.InferenceModelVariant, error) {
	tags, err := b.listTags(ctx, "list model variants")
	if err != nil {
		return nil, err
	}
	variants := make([]*operatorv1.InferenceModelVariant, 0, len(tags))
	for _, candidate := range tags {
		if candidate.Name == "" {
			continue
		}
		digest, err := models.NormalizeProviderModelDigest(candidate.Digest)
		if err != nil {
			return nil, fmt.Errorf("ollama_backend: list model variants: model %q: %w", candidate.Name, err)
		}
		variants = append(variants, &operatorv1.InferenceModelVariant{Model: candidate.Name, Digest: digest})
	}
	if len(variants) == 0 {
		return nil, fmt.Errorf("ollama_backend: list model variants: %w", constants.ErrInferenceModelNotFound)
	}
	return variants, nil
}

// listTags reads the provider's installed-model list from /api/tags.
func (b *OllamaBackend) listTags(ctx context.Context, op string) ([]ollamaTagModel, error) {
	var tagsResp ollamaTagsResponse
	if err := b.getJSON(ctx, op, &tagsResp, "api", "tags"); err != nil {
		return nil, err
	}
	return tagsResp.Models, nil
}

func ollamaErrorIndicatesCapabilityUnsupported(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest || len(body) == 0 {
		return false
	}
	message := strings.ToLower(string(body))
	return strings.Contains(message, "does not support tools") ||
		strings.Contains(message, "tools not supported") ||
		strings.Contains(message, "tool use not supported") ||
		strings.Contains(message, "function calling not supported")
}

// ollamaCapabilityRejection reports a 400 rejecting a non-tool capability the
// request asked for, such as thinking on a model without a thinking template.
func ollamaCapabilityRejection(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest || len(body) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(string(body)), "does not support thinking")
}
