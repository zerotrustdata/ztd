// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type anthropicAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	OutputTokensDetails      *struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// merge accepts cumulative counters, retaining fields omitted by later events.
func (u *anthropicUsage) merge(next *anthropicUsage) error {
	if next == nil {
		return nil
	}
	for _, pair := range []struct {
		old  **int64
		next *int64
	}{
		{&u.InputTokens, next.InputTokens}, {&u.OutputTokens, next.OutputTokens},
		{&u.CacheReadInputTokens, next.CacheReadInputTokens}, {&u.CacheCreationInputTokens, next.CacheCreationInputTokens},
	} {
		if pair.next != nil {
			if *pair.next < 0 || *pair.next > math.MaxInt32 || (*pair.old != nil && *pair.next < **pair.old) {
				return constants.ErrInferenceProviderResponseInvalid
			}
			*pair.old = pair.next
		}
	}
	if next.OutputTokensDetails != nil && next.OutputTokensDetails.ThinkingTokens != nil {
		if *next.OutputTokensDetails.ThinkingTokens < 0 || *next.OutputTokensDetails.ThinkingTokens > math.MaxInt32 {
			return constants.ErrInferenceProviderResponseInvalid
		}
		if u.OutputTokensDetails != nil && u.OutputTokensDetails.ThinkingTokens != nil && *next.OutputTokensDetails.ThinkingTokens < *u.OutputTokensDetails.ThinkingTokens {
			return constants.ErrInferenceProviderResponseInvalid
		}
		u.OutputTokensDetails = next.OutputTokensDetails
	}
	return nil
}

type anthropicTokenCounts struct {
	reported           bool
	prompt, completion int32
	thinking, cached   *int32
}

func anthropicUsageCounts(usage anthropicUsage) (anthropicTokenCounts, error) {
	counts := anthropicTokenCounts{}
	var prompt int64
	for _, n := range []*int64{usage.InputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens} {
		if n != nil {
			if *n < 0 || *n > math.MaxInt32 {
				return counts, constants.ErrInferenceProviderResponseInvalid
			}
			prompt += *n
		}
	}
	var output int64
	if usage.OutputTokens != nil {
		output = *usage.OutputTokens
	}
	if output < 0 || output > math.MaxInt32 || prompt+output > math.MaxInt32 {
		return counts, constants.ErrInferenceProviderResponseInvalid
	}
	if usage.OutputTokensDetails != nil && usage.OutputTokensDetails.ThinkingTokens != nil {
		thinking := *usage.OutputTokensDetails.ThinkingTokens
		if thinking < 0 || thinking > output {
			return counts, constants.ErrInferenceProviderResponseInvalid
		}
		n := int32(thinking)
		counts.thinking = &n
	}
	if usage.CacheReadInputTokens != nil {
		n := int32(*usage.CacheReadInputTokens)
		counts.cached = &n
	}
	counts.reported = usage.InputTokens != nil && usage.OutputTokens != nil
	if counts.reported {
		counts.prompt, counts.completion = int32(prompt), int32(output)
	}
	return counts, nil
}

type anthropicStreamMessage struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	Role       string            `json:"role"`
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason *string           `json:"stop_reason"`
	Usage      *anthropicUsage   `json:"usage"`
}

type anthropicStreamEvent struct {
	Type         string                  `json:"type"`
	Message      *anthropicStreamMessage `json:"message"`
	Index        *int                    `json:"index"`
	ContentBlock json.RawMessage         `json:"content_block"`
	Delta        json.RawMessage         `json:"delta"`
	Usage        *anthropicUsage         `json:"usage"`
	Error        *anthropicAPIError      `json:"error"`
}

type anthropicBlockDelta struct {
	Type        string  `json:"type"`
	Text        *string `json:"text"`
	Thinking    *string `json:"thinking"`
	Signature   *string `json:"signature"`
	PartialJSON *string `json:"partial_json"`
}

type anthropicStopDetails struct {
	Type        string `json:"type"`
	Category    string `json:"category"`
	Explanation string `json:"explanation"`
}

type anthropicMessageDelta struct {
	StopReason  string                `json:"stop_reason"`
	StopDetails *anthropicStopDetails `json:"stop_details"`
	Model       string                `json:"model"`
}

type anthropicStreamResult struct {
	servedModel, responseID, finishReason string
	stopReason, stopCategory              string
	usage                                 anthropicUsage
}

// Anthropic streams one indexed block at a time. Keeping lifecycle state
// separate from delivery prevents partial tools or reasoning from escaping.
type anthropicStreamState struct {
	result                      anthropicStreamResult
	started, finishing, stopped bool
	block                       *anthropicBlock
	blockIndex                  int
	arguments                   strings.Builder
	toolIDs                     map[string]struct{}
	parallel                    bool
}

func (s *anthropicStreamState) consume(event anthropicStreamEvent) ([]*operatorv1.InferenceResponsePart, error) {
	invalid := constants.ErrInferenceProviderResponseInvalid
	if s.stopped {
		return nil, invalid
	}
	if event.Type == "error" {
		return nil, constants.ErrInferenceProviderStreamError
	}
	if event.Type == "ping" {
		return nil, nil
	}
	if event.Type == "message_start" {
		m := event.Message
		if s.started || m == nil || m.Type != "message" || m.Role != "assistant" || m.ID == "" || validateHostedModelID(m.Model) != nil || len(m.Content) != 0 || m.StopReason != nil {
			return nil, invalid
		}
		s.started = true
		s.result.servedModel, s.result.responseID = m.Model, m.ID
		return nil, s.result.usage.merge(m.Usage)
	}
	if !s.started {
		return nil, invalid
	}
	switch event.Type {
	case "content_block_start":
		if s.finishing || s.block != nil || event.Index == nil || *event.Index != s.blockIndex {
			return nil, invalid
		}
		return s.startBlock(event.ContentBlock)
	case "content_block_delta":
		if s.block == nil || event.Index == nil || *event.Index != s.blockIndex {
			return nil, invalid
		}
		return s.blockDelta(event.Delta)
	case "content_block_stop":
		if s.block == nil || event.Index == nil || *event.Index != s.blockIndex {
			return nil, invalid
		}
		parts, err := s.finishBlock()
		if err != nil {
			return nil, err
		}
		s.block, s.blockIndex = nil, s.blockIndex+1
		return parts, nil
	case "message_delta":
		if s.block != nil {
			return nil, invalid
		}
		var delta anthropicMessageDelta
		if json.Unmarshal(event.Delta, &delta) != nil || (delta.Model != "" && delta.Model != s.result.servedModel) {
			return nil, invalid
		}
		if delta.StopReason != "" {
			if s.result.finishReason != "" {
				return nil, invalid
			}
			s.result.finishReason = anthropicFinishReason(delta.StopReason)
			s.result.stopReason = delta.StopReason
			if delta.StopDetails != nil {
				s.result.stopCategory = delta.StopDetails.Category
			}
		}
		s.finishing = true
		return nil, s.result.usage.merge(event.Usage)
	case "message_stop":
		if s.block != nil || s.result.finishReason == "" {
			return nil, invalid
		}
		s.stopped = true
		return nil, nil
	default:
		return nil, invalid
	}
}

func (s *anthropicStreamState) startBlock(raw json.RawMessage) ([]*operatorv1.InferenceResponsePart, error) {
	invalid := constants.ErrInferenceProviderResponseInvalid
	var block anthropicBlock
	if json.Unmarshal(raw, &block) != nil {
		return nil, invalid
	}
	s.arguments.Reset()
	switch block.Type {
	case "thinking", "redacted_thinking":
		reasoning, err := decodeAnthropicReasoning(raw, false)
		if err != nil {
			return nil, err
		}
		block = anthropicBlock{anthropicReasoningBlock: reasoning}
	case "text":
		if block.Text == nil || block.Thinking != nil || block.Signature != nil || block.Data != nil || block.ID != "" || block.Name != "" || len(block.Input) > 0 || block.Content != nil || block.ToolUseID != "" {
			return nil, invalid
		}
		s.block = &block
		return anthropicTextParts(*block.Text), nil
	case "tool_use":
		if block.ID == "" || !validAnthropicToolName(block.Name) || block.Text != nil || block.Thinking != nil || block.Signature != nil || block.Data != nil || block.Content != nil || block.ToolUseID != "" {
			return nil, invalid
		}
		input, err := canonicalizeJSON(string(block.Input), true, nil)
		if err != nil || input != "{}" {
			return nil, invalid
		}
		if _, duplicate := s.toolIDs[block.ID]; duplicate || (!s.parallel && len(s.toolIDs) > 0) {
			return nil, invalid
		}
		s.toolIDs[block.ID] = struct{}{}
	default:
		// Server tools, fallback, images, and other content are not admitted.
		return nil, invalid
	}
	s.block = &block
	return nil, nil
}

func (s *anthropicStreamState) blockDelta(raw json.RawMessage) ([]*operatorv1.InferenceResponsePart, error) {
	invalid := constants.ErrInferenceProviderResponseInvalid
	var delta anthropicBlockDelta
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&delta) != nil {
		return nil, invalid
	}
	fields := 0
	for _, field := range []*string{delta.Text, delta.Thinking, delta.Signature, delta.PartialJSON} {
		if field != nil {
			fields++
		}
	}
	if fields != 1 {
		return nil, invalid
	}
	switch {
	case delta.Type == "text_delta" && s.block.Type == "text" && delta.Text != nil:
		return anthropicTextParts(*delta.Text), nil
	case delta.Type == "thinking_delta" && s.block.Type == "thinking" && delta.Thinking != nil:
		if *delta.Thinking != "" {
			return nil, invalid
		}
	case delta.Type == "signature_delta" && s.block.Type == "thinking" && delta.Signature != nil:
		if len(*s.block.Signature)+len(*delta.Signature) > maxInferenceProviderReasoningBytes {
			return nil, invalid
		}
		*s.block.Signature += *delta.Signature
	case delta.Type == "input_json_delta" && s.block.Type == "tool_use" && delta.PartialJSON != nil:
		if s.arguments.Len()+len(*delta.PartialJSON) > maxInferenceToolJSONBytes {
			return nil, invalid
		}
		s.arguments.WriteString(*delta.PartialJSON)
	default:
		return nil, invalid
	}
	return nil, nil
}

func (s *anthropicStreamState) finishBlock() ([]*operatorv1.InferenceResponsePart, error) {
	switch s.block.Type {
	case "thinking", "redacted_thinking":
		part, err := anthropicReasoningPart(s.block.anthropicReasoningBlock)
		if err != nil {
			return nil, err
		}
		return []*operatorv1.InferenceResponsePart{part}, nil
	case "tool_use":
		args := s.arguments.String()
		if args == "" {
			args = "{}"
		}
		canonical, err := canonicalizeJSON(args, true, nil)
		if err != nil {
			return nil, constants.ErrInferenceProviderResponseInvalid
		}
		return []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId: s.block.ID, Name: s.block.Name, ArgumentsJson: canonical,
		}}}}, nil
	default:
		return nil, nil
	}
}

func anthropicTextParts(text string) []*operatorv1.InferenceResponsePart {
	if text == "" {
		return nil
	}
	return []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: text}}}
}

func anthropicFinishReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence", "tool_use":
		return "stop"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	default:
		return "error"
	}
}

func (b *AnthropicBackend) decodeStream(ctx context.Context, body io.Reader, arrivalTime func(int64) *int64, parallel bool, attemptID string, stream *streamSegmenter) (anthropicStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
	fail := func(err error) (anthropicStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
		return anthropicStreamResult{}, nil, nil, fmt.Errorf("%s: decode stream: %w", anthropicComponent, err)
	}
	reader := bufio.NewReaderSize(body, 64<<10)
	state := anthropicStreamState{parallel: parallel, toolIDs: make(map[string]struct{})}
	var offset int64
	var ttft *int64
	var parts []*operatorv1.InferenceResponsePart
	repetition := newRepetitionDetector()
	for {
		data, consumed, err := nextSSEData(reader)
		offset += consumed
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(constants.ErrInferenceProviderResponseInvalid)
		}
		var event anthropicStreamEvent
		if json.Unmarshal(data, &event) != nil {
			return fail(constants.ErrInferenceProviderResponseInvalid)
		}
		eventParts, err := state.consume(event)
		if err != nil {
			if errors.Is(err, constants.ErrInferenceProviderStreamError) {
				b.logger.Warn("Anthropic ended the response stream with an error",
					slog.String("provider_attempt_id", attemptID),
				)
			}
			return fail(err)
		}
		if len(eventParts) > 0 && ttft == nil {
			ttft = arrivalTime(offset)
		}
		repeating := repetition.observe(eventParts)
		if stream != nil {
			eventParts, err = stream.accept(ctx, eventParts)
			if err != nil {
				return fail(err)
			}
			if err := stream.publish(eventParts, state.result.servedModel, ttft); err != nil {
				b.logger.Warn("Inference progress delivery failed", slog.String("provider_attempt_id", attemptID), logging.ErrorKind(err))
			}
		}
		parts = append(parts, eventParts...)
		if repeating {
			state.result.finishReason = constants.InferenceFinishReasonRepetition
			break
		}
	}
	if !state.stopped && state.result.finishReason != constants.InferenceFinishReasonRepetition {
		return fail(constants.ErrInferenceProviderResponseInvalid)
	}
	if state.result.finishReason == "error" {
		attrs := []any{
			slog.String("provider_attempt_id", attemptID),
			slog.String("stop_reason", state.result.stopReason),
		}
		if state.result.stopCategory != "" {
			attrs = append(attrs, slog.String("stop_category", state.result.stopCategory))
		}
		b.logger.Warn("Anthropic generation ended without normal stop", attrs...)
	}
	if stream != nil {
		tail := stream.finish(ctx)
		if err := stream.publish(tail, state.result.servedModel, ttft); err != nil {
			b.logger.Warn("Inference progress delivery failed", slog.String("provider_attempt_id", attemptID), logging.ErrorKind(err))
		}
		parts = append(parts, tail...)
	}
	return state.result, parts, ttft, nil
}

// Provider messages can echo prompts or keys. Classification uses the typed
// error first, then bounded message hints for invalid_request_error only.
// Neither the message nor the raw body is logged, returned, or signed.
func (b *AnthropicBackend) statusError(status int, body []byte, attemptID string) error {
	var envelope struct {
		Error anthropicAPIError `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	b.logger.Warn("Anthropic generation failed",
		"status", status,
		"provider_attempt_id", attemptID,
	)
	kind := constants.ErrInferenceGenerateFailed
	switch envelope.Error.Type {
	case "not_found_error":
		kind = constants.ErrInferenceModelNotFound
	case "timeout_error":
		kind = constants.ErrInferenceBackendTimeout
	case "request_too_large":
		kind = constants.ErrInferenceContextOverflow
	case "authentication_error", "billing_error", "permission_error", "rate_limit_error", "api_error", "overloaded_error":
		kind = constants.ErrInferenceBackendUnavailable
	case "invalid_request_error":
		message := strings.ToLower(envelope.Error.Message)
		switch {
		case strings.Contains(message, "prompt is too long"), strings.Contains(message, "context length"), strings.Contains(message, "context window"):
			kind = constants.ErrInferenceContextOverflow
		case strings.Contains(message, "schema"), strings.Contains(message, "thinking"), strings.Contains(message, "effort"), strings.Contains(message, "temperature"), strings.Contains(message, "top_p"), strings.Contains(message, "top_k"), strings.Contains(message, "output_config"):
			kind = constants.ErrInferenceCapabilityUnsupported
		case strings.Contains(message, "tool"):
			kind = constants.ErrInferenceToolsUnsupported
		}
	default:
		switch {
		case status == http.StatusNotFound:
			kind = constants.ErrInferenceModelNotFound
		case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
			kind = constants.ErrInferenceBackendTimeout
		case status == http.StatusRequestEntityTooLarge:
			kind = constants.ErrInferenceContextOverflow
		case (status >= http.StatusMultipleChoices && status < http.StatusBadRequest) || status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
			kind = constants.ErrInferenceBackendUnavailable
		}
	}
	return fmt.Errorf("%s: provider status %d: %w", anthropicComponent, status, kind)
}
