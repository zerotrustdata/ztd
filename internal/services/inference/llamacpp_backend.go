// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/llamacpp"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// LlamaCppBackend implements Provider as an HTTP client to a llama.cpp
// llama-server router started with --models-dir. A model is named by its
// router model id. llama-server reports no content digest, so the ztd model
// digest is computed by the Operator from the GGUF files under the same
// models dir, which the Operator host must be able to read.
type LlamaCppBackend struct {
	providerHTTP
	logger   *slog.Logger
	digester *llamaCppModelDigester
}

// NewLlamaCppBackend constructs a LlamaCppBackend for a llama-server router
// endpoint and the router's --models-dir.
func NewLlamaCppBackend(endpoint, modelRoot string, logger *slog.Logger) (*LlamaCppBackend, error) {
	provider, err := newProviderHTTP("llamacpp_backend", endpoint)
	if err != nil {
		return nil, err
	}
	if modelRoot == "" {
		return nil, fmt.Errorf("llamacpp_backend: %w", constants.ErrInferenceLlamaCppModelRootRequired)
	}
	return &LlamaCppBackend{providerHTTP: provider, logger: logger, digester: newLlamaCppModelDigester(modelRoot)}, nil
}

// llamaCppChatRequest is the request body for llama-server's
// OpenAI-compatible /v1/chat/completions endpoint. Tool declarations share
// Ollama's wire shape.
type llamaCppChatRequest struct {
	Model              string                      `json:"model"`
	Messages           []llamaCppChatMessage       `json:"messages"`
	Tools              []ollamaTool                `json:"tools,omitempty"`
	ToolChoice         string                      `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool                       `json:"parallel_tool_calls,omitempty"`
	Stream             bool                        `json:"stream"`
	StreamOptions      llamaCppStreamOptions       `json:"stream_options"`
	Temperature        float32                     `json:"temperature,omitempty"`
	MaxTokens          int32                       `json:"max_tokens,omitempty"`
	TopP               *float32                    `json:"top_p,omitempty"`
	TopK               *int32                      `json:"top_k,omitempty"`
	Seed               *int32                      `json:"seed,omitempty"`
	Stop               []string                    `json:"stop,omitempty"`
	ResponseFormat     *llamaCppResponseFormat     `json:"response_format,omitempty"`
	ChatTemplateKwargs *llamaCppChatTemplateKwargs `json:"chat_template_kwargs,omitempty"`
}

type llamaCppStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type llamaCppResponseFormat struct {
	Type       string             `json:"type"`
	JSONSchema llamaCppJSONSchema `json:"json_schema"`
}

type llamaCppJSONSchema struct {
	Schema json.RawMessage `json:"schema"`
}

type llamaCppChatTemplateKwargs struct {
	EnableThinking bool `json:"enable_thinking"`
}

type llamaCppChatMessage struct {
	Role       string             `json:"role"`
	Content    string             `json:"content"`
	ToolCalls  []llamaCppToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	Name       string             `json:"name,omitempty"`
}

type llamaCppToolCall struct {
	Index    *int                     `json:"index,omitempty"`
	ID       string                   `json:"id,omitempty"`
	Type     string                   `json:"type,omitempty"`
	Function llamaCppToolCallFunction `json:"function"`
}

// llamaCppToolCallFunction carries tool-call arguments as a JSON-encoded
// string, as the OpenAI wire format requires. In a stream delta the
// arguments are a fragment of that string.
type llamaCppToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// llamaCppChatChunk is one server-sent event of a streamed chat completion.
type llamaCppChatChunk struct {
	Model   string                `json:"model"`
	Choices []llamaCppChunkChoice `json:"choices"`
	Usage   *llamaCppUsage        `json:"usage"`
	Timings *llamaCppTimings      `json:"timings"`
	Error   *llamaCppError        `json:"error"`
}

type llamaCppChunkChoice struct {
	Index        int                `json:"index"`
	Delta        llamaCppChunkDelta `json:"delta"`
	FinishReason *string            `json:"finish_reason"`
}

type llamaCppChunkDelta struct {
	Content          string             `json:"content"`
	ReasoningContent string             `json:"reasoning_content"`
	ToolCalls        []llamaCppToolCall `json:"tool_calls"`
}

type llamaCppUsage struct {
	PromptTokens        *int32                      `json:"prompt_tokens"`
	CompletionTokens    *int32                      `json:"completion_tokens"`
	PromptTokensDetails *llamaCppPromptTokenDetails `json:"prompt_tokens_details"`
}

type llamaCppPromptTokenDetails struct {
	CachedTokens *int32 `json:"cached_tokens"`
}

// llamaCppTimings is llama-server's per-request timing report. Durations are
// milliseconds.
type llamaCppTimings struct {
	CacheN      *int32   `json:"cache_n"`
	PromptMS    *float64 `json:"prompt_ms"`
	PredictedMS *float64 `json:"predicted_ms"`
}

type llamaCppError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// llamaCppErrorTypeContextOverflow is llama-server's error type for a prompt
// that does not fit the loaded context window.
const llamaCppErrorTypeContextOverflow = "exceed_context_size_error"

// llamaCppModelsResponse is the router's GET /models response.
type llamaCppModelsResponse struct {
	Data []llamaCppRouterModel `json:"data"`
}

type llamaCppRouterModel struct {
	ID     string              `json:"id"`
	Status llamaCppModelStatus `json:"status"`
}

type llamaCppModelStatus struct {
	Value string `json:"value"`
}

// llamaCppResidentStatuses are the router model states that hold, or are
// acquiring, model weights in memory.
var llamaCppResidentStatuses = []string{"loaded", "loading", "sleeping"}

type llamaCppModelRequest struct {
	Model string `json:"model"`
}

type llamaCppModelActionResponse struct {
	Success bool `json:"success"`
}

// llamaCppUnloadAfterRequest maps a keep-alive to llama-server's residency
// control. The router keeps a model loaded until it is unloaded or evicted, so
// only "-1" (keep resident) and "0" (unload after the request) are
// expressible.
func llamaCppUnloadAfterRequest(keepAlive string) (bool, error) {
	switch keepAlive {
	case "", "-1":
		return false, nil
	case "0":
		return true, nil
	default:
		return false, fmt.Errorf("llamacpp_backend: keep_alive %q: %w", keepAlive, constants.ErrInferenceCapabilityUnsupported)
	}
}

// Generate streams a chat completion from llama-server's
// /v1/chat/completions endpoint and returns the generated parts, usage
// metadata, and finish reason.
func (b *LlamaCppBackend) Generate(ctx context.Context, req models.GenerateRequest) (*models.GenerateResponse, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("llamacpp_backend: generate: %w", constants.ErrInferenceModelRefInvalid)
	}
	unload, err := llamaCppUnloadAfterRequest(req.KeepAlive)
	if err != nil {
		return nil, err
	}
	if req.ModelDigest != "" {
		if err := b.verifyModelDigest(ctx, req.Model, req.ModelDigest); err != nil {
			return nil, err
		}
	}
	chatReq, err := llamaCppChatRequestFor(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("llamacpp_backend: marshal request: %w", err)
	}
	normalizedRequestHash := models.SHA256Hex(body)

	// Streaming scrubs at the source. Without an enabled scrubber the stream is refused before the
	// provider is called, so no live output can leave unscrubbed.
	var stream *streamSegmenter
	if req.Stream {
		if reporter := ProgressReporterFromContext(ctx); reporter != nil {
			stream, err = newStreamSegmenter(req.Scrubber, reporter, req.ProviderAttemptID)
			if err != nil {
				return nil, fmt.Errorf("llamacpp_backend: generate: %w", err)
			}
		}
	}

	requestStartedAt := time.Now()
	resp, err := b.postJSON(ctx, "generate", chatReq, "v1", "chat", "completions")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if unload {
		defer b.unloadAfterRequest(ctx, req.Model)
	}

	// Ingestion owns the bounded raw bytes independently of parsing and delivery.
	// A slow reporter or invalid frame cannot truncate the provider artifact.
	capture := newProviderResponseCapture(b.maxResponseBytes, requestStartedAt)
	go capture.receive(resp.Body)
	var result llamaCppStreamResult
	var parts []*operatorv1.InferenceResponsePart
	var timeToFirstToken *int64
	var decodeErr error
	if resp.StatusCode == http.StatusOK {
		allowParallelToolCalls := req.ParallelToolCalls == nil || *req.ParallelToolCalls
		result, parts, timeToFirstToken, decodeErr = b.decodeChatStream(ctx, capture, capture.arrivalTime, req.Model, allowParallelToolCalls, req.ProviderAttemptID, stream)
	}
	responseBody, readErr := capture.result()
	// Only a 200 stream is bound to the attempt record, and only by digest and
	// size. A non-200 body can echo prompt material and is never recorded.
	if store := attemptStoreFromContext(ctx); store != nil && resp.StatusCode == http.StatusOK {
		if err := store.RecordRawResponse(context.WithoutCancel(ctx), req.ProviderAttemptID, responseBody); err != nil {
			return nil, errors.Join(readErr, fmt.Errorf("llamacpp_backend: record raw response digest: %w", err))
		}
	}
	if readErr != nil {
		return nil, transportError(ctx, b.component, "generate", readErr)
	}
	if int64(len(responseBody)) > b.maxResponseBytes {
		return nil, fmt.Errorf("llamacpp_backend: generate: %w: response exceeds %d bytes", constants.ErrInferenceProviderResponseInvalid, b.maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, b.generateStatusError(resp.StatusCode, responseBody, req.Model)
	}
	if decodeErr != nil {
		return nil, decodeErr
	}

	finishReason := result.finishReason
	if finishReason == "tool_calls" {
		finishReason = "stop"
	}
	promptTokens, completionTokens, cacheTokens, usageReported, err := llamaCppUsageCounts(result.usage, result.timings)
	if err != nil {
		return nil, err
	}
	// llama-server fixes the context window when it loads a model, so a
	// requested limit is enforced on the reported usage instead of sent.
	if req.ContextLimit != nil {
		if !usageReported {
			return nil, fmt.Errorf("llamacpp_backend: generate: context limit requires token usage: %w", constants.ErrInferenceProviderResponseInvalid)
		}
		if promptTokens >= *req.ContextLimit || int64(promptTokens)+int64(completionTokens) > int64(*req.ContextLimit) {
			return nil, fmt.Errorf("llamacpp_backend: generate: %w: prompt_tokens=%d completion_tokens=%d context_limit=%d", constants.ErrInferenceContextOverflow, promptTokens, completionTokens, *req.ContextLimit)
		}
	}
	promptEvalDuration, generationDuration, err := llamaCppDurations(result.timings)
	if err != nil {
		return nil, err
	}
	timingSource := operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_UNSPECIFIED
	if promptEvalDuration != nil || generationDuration != nil || timeToFirstToken != nil {
		timingSource = operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER
	}

	outputHash, err := models.ComputeInferenceOutputHash(parts, finishReason)
	if err != nil {
		return nil, fmt.Errorf("llamacpp_backend: generate: %w", err)
	}
	servedModelDigest := ""
	if req.ModelDigest != "" {
		if err := b.verifyModelDigest(ctx, req.Model, req.ModelDigest); err != nil {
			return nil, err
		}
		servedModelDigest = req.ModelDigest
	}
	return &models.GenerateResponse{
		Parts:                 parts,
		PromptTokens:          promptTokens,
		CompletionTokens:      completionTokens,
		TotalTokens:           promptTokens + completionTokens,
		UsageReported:         usageReported,
		CacheTokens:           cacheTokens,
		PromptEvalDurationNS:  promptEvalDuration,
		GenerationDurationNS:  generationDuration,
		TimeToFirstTokenNS:    timeToFirstToken,
		TimingSource:          timingSource,
		FinishReason:          finishReason,
		Model:                 req.Model,
		ServedModelDigest:     servedModelDigest,
		NormalizedRequestHash: normalizedRequestHash,
		OutputHash:            outputHash,
	}, nil
}

// llamaCppChatRequestFor maps a generation request onto llama-server's chat
// completion request.
func llamaCppChatRequestFor(req models.GenerateRequest) (llamaCppChatRequest, error) {
	ollamaMessages, err := inferenceMessagesToOllama(req.Messages)
	if err != nil {
		return llamaCppChatRequest{}, fmt.Errorf("llamacpp_backend: generate: messages: %w", err)
	}
	tools, err := inferenceToolsToOllama(req.Tools, req.ToolChoice)
	if err != nil {
		return llamaCppChatRequest{}, fmt.Errorf("llamacpp_backend: generate: tools: %w", err)
	}
	chatReq := llamaCppChatRequest{
		Model:             req.Model,
		Messages:          llamaCppMessages(ollamaMessages),
		Tools:             tools,
		ParallelToolCalls: req.ParallelToolCalls,
		Stream:            true,
		StreamOptions:     llamaCppStreamOptions{IncludeUsage: true},
		Temperature:       req.Temperature,
		MaxTokens:         resolveOutputTokenLimit(req.MaxTokens),
		TopP:              req.TopP,
		TopK:              req.TopK,
		Seed:              req.Seed,
		Stop:              req.StopSequences,
	}
	if len(tools) > 0 {
		chatReq.ToolChoice = "auto"
	}
	if req.Thinking != nil {
		switch mode := req.Thinking.GetMode().(type) {
		case *operatorv1.InferenceThinkingControl_Enabled:
			chatReq.ChatTemplateKwargs = &llamaCppChatTemplateKwargs{EnableThinking: mode.Enabled}
		case *operatorv1.InferenceThinkingControl_Level:
			return llamaCppChatRequest{}, fmt.Errorf("llamacpp_backend: generate: thinking level %q: %w", mode.Level, constants.ErrInferenceCapabilityUnsupported)
		default:
			return llamaCppChatRequest{}, fmt.Errorf("llamacpp_backend: generate: thinking: %w", constants.ErrInferenceGenerationOptionsInvalid)
		}
	}
	if req.ResponseFormat != nil {
		if req.ResponseFormat.GetMediaType() != inferenceJSONMediaType {
			return llamaCppChatRequest{}, fmt.Errorf("llamacpp_backend: generate: response format: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		if err := validateInferenceToolSchema(req.ResponseFormat.GetJsonSchema()); err != nil {
			return llamaCppChatRequest{}, fmt.Errorf("llamacpp_backend: generate: response format: %w", err)
		}
		chatReq.ResponseFormat = &llamaCppResponseFormat{
			Type:       "json_schema",
			JSONSchema: llamaCppJSONSchema{Schema: json.RawMessage(req.ResponseFormat.GetJsonSchema())},
		}
	}
	return chatReq, nil
}

// llamaCppMessages re-encodes validated chat messages in the OpenAI wire
// format: tool-call arguments become JSON strings and a tool result names its
// tool in "name".
func llamaCppMessages(messages []ollamaChatMessage) []llamaCppChatMessage {
	result := make([]llamaCppChatMessage, 0, len(messages))
	for _, message := range messages {
		mapped := llamaCppChatMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			Name:       message.ToolName,
		}
		for _, call := range message.ToolCalls {
			mapped.ToolCalls = append(mapped.ToolCalls, llamaCppToolCall{
				ID:   call.ID,
				Type: "function",
				Function: llamaCppToolCallFunction{
					Name:      call.Function.Name,
					Arguments: string(call.Function.Arguments),
				},
			})
		}
		result = append(result, mapped)
	}
	return result
}

// generateStatusError classifies a non-200 chat completion response.
func (b *LlamaCppBackend) generateStatusError(statusCode int, body []byte, model string) error {
	var apiError struct {
		Error llamaCppError `json:"error"`
	}
	_ = json.Unmarshal(body, &apiError)
	if b.logger != nil {
		b.logger.Warn("llama.cpp generation failed",
			"status", statusCode,
			"model", model,
			"error", apiError.Error.Message,
		)
	}
	message := strings.ToLower(string(body))
	switch {
	case statusCode == http.StatusRequestTimeout:
		return fmt.Errorf("llamacpp_backend: generate: %w", constants.ErrInferenceBackendTimeout)
	case apiError.Error.Type == llamaCppErrorTypeContextOverflow:
		return fmt.Errorf("llamacpp_backend: generate: %w", constants.ErrInferenceContextOverflow)
	case statusCode == http.StatusNotFound,
		statusCode == http.StatusBadRequest && strings.Contains(message, "not found"):
		return fmt.Errorf("llamacpp_backend: generate: %w: model %q", constants.ErrInferenceModelNotFound, model)
	case statusCode == http.StatusBadRequest && (strings.Contains(message, "--jinja") || strings.Contains(message, "tools param")):
		return fmt.Errorf("llamacpp_backend: generate: %w", constants.ErrInferenceToolsUnsupported)
	default:
		return fmt.Errorf("llamacpp_backend: generate: %w: status %d", constants.ErrInferenceGenerateFailed, statusCode)
	}
}

// llamaCppStreamError classifies an error event delivered inside a 200
// stream.
func llamaCppStreamError(apiError llamaCppError) error {
	if apiError.Type == llamaCppErrorTypeContextOverflow {
		return fmt.Errorf("llamacpp_backend: generate: %w", constants.ErrInferenceContextOverflow)
	}
	return fmt.Errorf("llamacpp_backend: generate: stream error: %w", constants.ErrInferenceGenerateFailed)
}

// llamaCppStreamResult is the terminal state of a decoded completion stream.
type llamaCppStreamResult struct {
	finishReason string
	usage        *llamaCppUsage
	timings      *llamaCppTimings
}

// llamaCppToolCallBuilder accumulates one streamed tool call's deltas.
type llamaCppToolCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

func (b *LlamaCppBackend) decodeChatStream(
	ctx context.Context,
	body io.Reader,
	arrivalTime func(int64) *int64,
	requestedModel string,
	allowParallelToolCalls bool,
	providerAttemptID string,
	stream *streamSegmenter,
) (llamaCppStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
	reader := bufio.NewReader(body)
	var offset int64
	var result llamaCppStreamResult
	parts := make([]*operatorv1.InferenceResponsePart, 0)
	toolCalls := make(map[int]*llamaCppToolCallBuilder)
	var toolCallOrder []int
	var timeToFirstToken *int64
	done := false
	invalid := func(reason string) (llamaCppStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
		return llamaCppStreamResult{}, nil, nil, fmt.Errorf("llamacpp_backend: generate: %s: %w", reason, constants.ErrInferenceProviderResponseInvalid)
	}
	deliver := func(eventParts []*operatorv1.InferenceResponsePart) error {
		if stream != nil {
			released, err := stream.accept(ctx, eventParts)
			if err != nil {
				return fmt.Errorf("llamacpp_backend: generate: stream segments: %w", err)
			}
			eventParts = released
			if err := stream.publish(released, requestedModel, timeToFirstToken); err != nil {
				b.logger.Warn("Inference progress delivery failed", "provider_attempt_id", providerAttemptID, "error", err)
			}
		}
		parts = append(parts, eventParts...)
		return nil
	}
	for {
		line, readErr := reader.ReadString('\n')
		offset += int64(len(line))
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return llamaCppStreamResult{}, nil, nil, fmt.Errorf("llamacpp_backend: generate: %w: %w", constants.ErrInferenceProviderResponseInvalid, readErr)
		}
		event := strings.TrimRight(line, "\r\n")
		switch {
		case event == "" || strings.HasPrefix(event, ":"):
		case done:
			return invalid("event after [DONE]")
		case strings.HasPrefix(event, "error:"):
			var apiError llamaCppError
			if err := json.Unmarshal([]byte(strings.TrimPrefix(event, "error:")), &apiError); err != nil {
				return invalid("error event")
			}
			return llamaCppStreamResult{}, nil, nil, llamaCppStreamError(apiError)
		case strings.HasPrefix(event, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(event, "data:"))
			if data == "[DONE]" {
				done = true
				break
			}
			var chunk llamaCppChatChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return invalid("chunk")
			}
			if chunk.Error != nil {
				return llamaCppStreamResult{}, nil, nil, llamaCppStreamError(*chunk.Error)
			}
			if chunk.Model != "" && chunk.Model != requestedModel {
				return invalid("served model differs from requested model")
			}
			if chunk.Usage != nil {
				result.usage = chunk.Usage
			}
			if chunk.Timings != nil {
				result.timings = chunk.Timings
			}
			var eventParts []*operatorv1.InferenceResponsePart
			sawOutput := false
			for _, choice := range chunk.Choices {
				if choice.Index != 0 {
					return invalid("choice index")
				}
				delta := choice.Delta
				hasDelta := delta.ReasoningContent != "" || delta.Content != "" || len(delta.ToolCalls) > 0
				if result.finishReason != "" && hasDelta {
					return invalid("delta after finish")
				}
				sawOutput = sawOutput || hasDelta
				if delta.ReasoningContent != "" {
					eventParts = append(eventParts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: delta.ReasoningContent}})
				}
				if delta.Content != "" {
					eventParts = append(eventParts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: delta.Content}})
				}
				for _, call := range delta.ToolCalls {
					if call.Index == nil || *call.Index < 0 {
						return invalid("tool call index")
					}
					builder, ok := toolCalls[*call.Index]
					if !ok {
						builder = &llamaCppToolCallBuilder{}
						toolCalls[*call.Index] = builder
						toolCallOrder = append(toolCallOrder, *call.Index)
						if !allowParallelToolCalls && len(toolCallOrder) > 1 {
							return invalid("parallel tool calls")
						}
					}
					if call.ID != "" {
						builder.id = call.ID
					}
					builder.name += call.Function.Name
					builder.arguments.WriteString(call.Function.Arguments)
				}
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					result.finishReason = *choice.FinishReason
				}
			}
			if sawOutput && timeToFirstToken == nil {
				timeToFirstToken = arrivalTime(offset)
			}
			if err := deliver(eventParts); err != nil {
				return llamaCppStreamResult{}, nil, nil, err
			}
		default:
			return invalid("event field")
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	switch {
	case !done:
		return invalid("stream ended without [DONE]")
	case result.finishReason == "":
		return invalid("stream ended without a finish reason")
	}
	slices.Sort(toolCallOrder)
	toolParts := make([]*operatorv1.InferenceResponsePart, 0, len(toolCallOrder))
	for _, index := range toolCallOrder {
		call := toolCalls[index]
		if call.name == "" || call.arguments.Len() == 0 {
			return invalid(fmt.Sprintf("tool call %d", index))
		}
		arguments, err := canonicalizeJSON(call.arguments.String(), true, nil)
		if err != nil {
			return invalid(fmt.Sprintf("tool call %d arguments", index))
		}
		toolParts = append(toolParts, &operatorv1.InferenceResponsePart{
			Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
				CallId:        call.id,
				Name:          call.name,
				ArgumentsJson: arguments,
			}},
		})
	}
	if err := deliver(toolParts); err != nil {
		return llamaCppStreamResult{}, nil, nil, err
	}
	if stream != nil {
		// The buffered tail is released only once the stream has terminated.
		tail := stream.finish(ctx)
		if err := stream.publish(tail, requestedModel, timeToFirstToken); err != nil {
			b.logger.Warn("Inference progress delivery failed", "provider_attempt_id", providerAttemptID, "error", err)
		}
		parts = append(parts, tail...)
	}
	return result, parts, timeToFirstToken, nil
}

// llamaCppUsageCounts validates the reported token usage. Cached prompt
// tokens come from the usage details, else from the timing report.
func llamaCppUsageCounts(usage *llamaCppUsage, timings *llamaCppTimings) (int32, int32, *int32, bool, error) {
	invalid := fmt.Errorf("llamacpp_backend: generate: token usage: %w", constants.ErrInferenceProviderResponseInvalid)
	var cacheTokens *int32
	if timings != nil {
		cacheTokens = timings.CacheN
	}
	if usage == nil {
		return 0, 0, nil, false, nil
	}
	if usage.PromptTokens == nil || usage.CompletionTokens == nil || *usage.PromptTokens < 0 || *usage.CompletionTokens < 0 {
		return 0, 0, nil, false, invalid
	}
	if int64(*usage.PromptTokens)+int64(*usage.CompletionTokens) > math.MaxInt32 {
		return 0, 0, nil, false, invalid
	}
	if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens != nil {
		cacheTokens = usage.PromptTokensDetails.CachedTokens
	}
	if cacheTokens != nil && *cacheTokens < 0 {
		return 0, 0, nil, false, invalid
	}
	return *usage.PromptTokens, *usage.CompletionTokens, cacheTokens, true, nil
}

// llamaCppDurations converts llama-server's millisecond prompt and generation
// timings to nanoseconds.
func llamaCppDurations(timings *llamaCppTimings) (*int64, *int64, error) {
	if timings == nil {
		return nil, nil, nil
	}
	convert := func(ms *float64) (*int64, error) {
		if ms == nil {
			return nil, nil
		}
		ns := *ms * float64(time.Millisecond)
		if math.IsNaN(ns) || math.IsInf(ns, 0) || ns < 0 || ns >= math.MaxInt64 {
			return nil, fmt.Errorf("llamacpp_backend: generate: timing: %w", constants.ErrInferenceProviderResponseInvalid)
		}
		value := int64(math.Round(ns))
		return &value, nil
	}
	prompt, err := convert(timings.PromptMS)
	if err != nil {
		return nil, nil, err
	}
	generation, err := convert(timings.PredictedMS)
	if err != nil {
		return nil, nil, err
	}
	return prompt, generation, nil
}

// verifyModelDigest fails closed unless the model's files under the model
// root carry the expected ztd model digest.
func (b *LlamaCppBackend) verifyModelDigest(ctx context.Context, model, expected string) error {
	_, digest, err := b.digester.modelDigest(ctx, model)
	if err != nil {
		return err
	}
	if digest != expected {
		return fmt.Errorf("llamacpp_backend: model %q: %w", model, constants.ErrInferenceModelDigestMismatch)
	}
	return nil
}

// unloadAfterRequest honors keep-alive 0 once a request has finished. The
// request's own outcome stands; a failed unload is logged, and a later
// governed release still verifies residency.
func (b *LlamaCppBackend) unloadAfterRequest(ctx context.Context, model string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ProviderStatusTimeout)
	defer cancel()
	if err := b.unloadModel(ctx, model); err != nil && b.logger != nil {
		b.logger.Warn("llama.cpp keep-alive 0 unload failed", "model", model, "error", err)
	}
}

// Decide calls llama-server's System One endpoint, verifying the frozen model
// digest before egress.
func (b *LlamaCppBackend) Decide(ctx context.Context, req models.DecisionRequest) (*models.DecisionResponse, error) {
	if req.AllowHosted || req.Input.GetAllowHosted() {
		return nil, constants.ErrInferenceHostedNotAllowed
	}
	unload, err := llamaCppUnloadAfterRequest(req.KeepAlive)
	if err != nil {
		return nil, err
	}
	req.KeepAlive = "" // residency is controlled through the router, not the request
	body, err := decisionBody(req)
	if err != nil {
		return nil, err
	}
	if req.ModelDigest != "" {
		if err := b.verifyModelDigest(ctx, req.Model, req.ModelDigest); err != nil {
			return nil, err
		}
	}
	if unload {
		defer b.unloadAfterRequest(ctx, req.Model)
	}
	return callSystemOne(ctx, b.client, b.apiURL("v1", "systemone"), "", body, req, b.maxResponseBytes)
}

// Status queries the router's /models endpoint to verify llama-server is
// reachable and lists the models it serves.
func (b *LlamaCppBackend) Status(ctx context.Context) (*models.BackendStatus, error) {
	routerModels, err := b.listRouterModels(ctx, "status")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(routerModels))
	for _, model := range routerModels {
		names = append(names, model.ID)
	}
	return &models.BackendStatus{Available: true, Models: names}, nil
}

// listRouterModels reads the router's model list from /models.
func (b *LlamaCppBackend) listRouterModels(ctx context.Context, op string) ([]llamaCppRouterModel, error) {
	var listed llamaCppModelsResponse
	if err := b.getJSON(ctx, op, &listed, "models"); err != nil {
		return nil, err
	}
	for _, model := range listed.Data {
		if model.ID == "" {
			return nil, fmt.Errorf("llamacpp_backend: %s: %w: model id is required", op, constants.ErrInferenceProviderResponseInvalid)
		}
	}
	return listed.Data, nil
}

// ReadResidency lists the router models that are loaded, loading, or
// sleeping. It is bounded by ProviderStatusTimeout like every other
// read-only provider query.
func (b *LlamaCppBackend) ReadResidency(ctx context.Context) (ProviderResidency, error) {
	ctx, cancel := context.WithTimeout(ctx, ProviderStatusTimeout)
	defer cancel()
	routerModels, err := b.listRouterModels(ctx, "read residency")
	if err != nil {
		return ProviderResidency{}, err
	}
	residency := ProviderResidency{Models: make([]ProviderResidencyModel, 0)}
	for _, model := range routerModels {
		if slices.Contains(llamaCppResidentStatuses, model.Status.Value) {
			residency.Models = append(residency.Models, ProviderResidencyModel{Name: model.ID})
		}
	}
	return residency, nil
}

// ReleaseModels unloads each requested resident model through the router and
// then verifies the complete requested set is absent from residency.
func (b *LlamaCppBackend) ReleaseModels(ctx context.Context, release []string) error {
	if len(release) == 0 {
		return fmt.Errorf("llamacpp_backend: release models: %w: at least one model is required", constants.ErrInferenceModelTagInvalid)
	}
	for _, model := range release {
		if err := operatorcapability.ValidateServedModelTag(model); err != nil {
			return fmt.Errorf("llamacpp_backend: release models: %w", err)
		}
	}
	routerModels, err := b.listRouterModels(ctx, "release models")
	if err != nil {
		return err
	}
	for _, model := range release {
		index := slices.IndexFunc(routerModels, func(candidate llamaCppRouterModel) bool { return candidate.ID == model })
		if index < 0 {
			return fmt.Errorf("llamacpp_backend: release model: %w: model %q", constants.ErrInferenceModelNotFound, model)
		}
		if !slices.Contains(llamaCppResidentStatuses, routerModels[index].Status.Value) {
			continue
		}
		if err := b.unloadModel(ctx, model); err != nil {
			return err
		}
	}
	return awaitModelsAbsent(ctx, b.component, b.ReadResidency, release)
}

func (b *LlamaCppBackend) unloadModel(ctx context.Context, model string) error {
	resp, err := b.postJSON(ctx, "release model", llamaCppModelRequest{Model: model}, "models", "unload")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("llamacpp_backend: release model: %w: model %q", constants.ErrInferenceModelNotFound, model)
		}
		return fmt.Errorf("llamacpp_backend: release model: %w: status %d", constants.ErrInferenceModelReleaseFailed, resp.StatusCode)
	}
	var released llamaCppModelActionResponse
	if err := b.decodeResponse("release model", resp.Body, &released); err != nil {
		return err
	}
	if !released.Success {
		return fmt.Errorf("llamacpp_backend: release model: %w", constants.ErrInferenceProviderResponseInvalid)
	}
	return nil
}

// ListProviderModelInventory describes every router model that resolves to
// GGUF files under the model root. A router model served from elsewhere (a
// preset or cache entry) cannot be digested by the Operator and is skipped.
func (b *LlamaCppBackend) ListProviderModelInventory(ctx context.Context) ([]ProviderModelInventoryEntry, error) {
	routerModels, err := b.listRouterModels(ctx, "list provider model inventory")
	if err != nil {
		return nil, fmt.Errorf("inference: list provider model inventory: %w", err)
	}
	entries := make([]ProviderModelInventoryEntry, 0, len(routerModels))
	for _, routerModel := range routerModels {
		model, digest, err := b.digester.modelDigest(ctx, routerModel.ID)
		if errors.Is(err, constants.ErrInferenceModelNotFound) || errors.Is(err, constants.ErrLlamaCppModelLayoutInvalid) {
			if b.logger != nil {
				b.logger.Warn("llama.cpp router model does not resolve under the model root; skipped", "model", routerModel.ID, "error", err)
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inference: list provider model inventory: model %q: %w", routerModel.ID, err)
		}
		entry, err := describeLlamaCppModel(model, digest)
		if err != nil {
			return nil, fmt.Errorf("inference: list provider model inventory: model %q: %w", routerModel.ID, err)
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("inference: list provider model inventory: %w", constants.ErrInferenceModelNotFound)
	}
	return entries, nil
}

func describeLlamaCppModel(model llamacpp.Model, digest string) (ProviderModelInventoryEntry, error) {
	metadata, err := llamacpp.ReadMetadata(model.Shards)
	if err != nil {
		return ProviderModelInventoryEntry{}, err
	}
	var artifactSize uint64
	for _, shard := range model.Shards {
		info, err := os.Stat(shard)
		if err != nil {
			return ProviderModelInventoryEntry{}, fmt.Errorf("model file: %w", err)
		}
		artifactSize += uint64(info.Size())
	}
	countSource := operatorv1.ParameterCountSource_PARAMETER_COUNT_SOURCE_UNSPECIFIED
	if metadata.ParameterCount > 0 {
		countSource = operatorv1.ParameterCountSource_PARAMETER_COUNT_SOURCE_MODEL_METADATA
	}
	parameterSize := metadata.SizeLabel
	if parameterSize == "" {
		parameterSize = formatParameterCount(metadata.ParameterCount)
	}
	var contextLimit uint32
	if metadata.ContextLength <= math.MaxUint32 {
		contextLimit = uint32(metadata.ContextLength)
	}
	capabilities := []string{"completion"}
	if model.MMProj != "" {
		capabilities = append(capabilities, "vision")
	}
	return ProviderModelInventoryEntry{
		ProviderClass:          constants.InferenceProviderLlamaCpp,
		ServedModelTag:         model.ID,
		ModelDigest:            digest,
		ModelFamily:            metadata.Architecture,
		ParameterSize:          parameterSize,
		ParameterCount:         metadata.ParameterCount,
		ParameterCountSource:   countSource,
		Quantization:           metadata.Quantization,
		Format:                 constants.ModelWeightFormatGGUF,
		ArtifactSizeBytes:      artifactSize,
		ContextLimit:           contextLimit,
		AdvertisedCapabilities: capabilities,
	}, nil
}

// formatParameterCount renders an exact parameter count as a rounded size
// label such as "8.2B".
func formatParameterCount(count uint64) string {
	switch {
	case count == 0:
		return ""
	case count >= 1_000_000_000:
		return strconv.FormatFloat(float64(count)/1e9, 'f', 1, 64) + "B"
	case count >= 1_000_000:
		return strconv.FormatFloat(float64(count)/1e6, 'f', 1, 64) + "M"
	case count >= 1_000:
		return strconv.FormatFloat(float64(count)/1e3, 'f', 1, 64) + "K"
	default:
		return strconv.FormatUint(count, 10)
	}
}
