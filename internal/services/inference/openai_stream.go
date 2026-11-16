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
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type openaiAPIError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Param   string `json:"param"`
	Message string `json:"message"`
}

type openaiUsage struct {
	InputTokens        *int64 `json:"input_tokens"`
	OutputTokens       *int64 `json:"output_tokens"`
	TotalTokens        *int64 `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type openaiTokenCounts struct {
	reported                  bool
	prompt, completion, total int32
	thinking, cached          *int32
}

func openaiUsageCounts(usage *openaiUsage) (openaiTokenCounts, error) {
	if usage == nil || usage.InputTokens == nil || usage.OutputTokens == nil {
		return openaiTokenCounts{}, nil
	}
	invalid := fmt.Errorf("%s: generate: token usage: %w", openaiComponent, constants.ErrInferenceProviderResponseInvalid)
	input := *usage.InputTokens
	output := *usage.OutputTokens
	if input < 0 || input > math.MaxInt32 || output < 0 || output > math.MaxInt32 || input+output > math.MaxInt32 {
		return openaiTokenCounts{}, invalid
	}
	total := input + output
	if usage.TotalTokens != nil {
		if *usage.TotalTokens < 0 || *usage.TotalTokens > math.MaxInt32 {
			return openaiTokenCounts{}, invalid
		}
		total = *usage.TotalTokens
	}
	counts := openaiTokenCounts{
		reported:   true,
		prompt:     int32(input),
		completion: int32(output),
		total:      int32(total),
	}
	if usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens != nil {
		cached := *usage.InputTokensDetails.CachedTokens
		if cached < 0 || cached > input {
			return openaiTokenCounts{}, invalid
		}
		n := int32(cached)
		counts.cached = &n
	}
	if usage.OutputTokensDetails != nil && usage.OutputTokensDetails.ReasoningTokens != nil {
		reasoning := *usage.OutputTokensDetails.ReasoningTokens
		if reasoning < 0 || reasoning > output {
			return openaiTokenCounts{}, invalid
		}
		n := int32(reasoning)
		counts.thinking = &n
	}
	return counts, nil
}

type openaiIncompleteDetails struct {
	Reason string `json:"reason"`
}

type openaiStreamResponse struct {
	ID                string                   `json:"id"`
	Status            string                   `json:"status"`
	Model             string                   `json:"model"`
	IncompleteDetails *openaiIncompleteDetails `json:"incomplete_details"`
	Error             *openaiAPIError          `json:"error"`
	Usage             *openaiUsage             `json:"usage"`
}

type openaiStreamItem struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Role      string            `json:"role"`
	CallID    string            `json:"call_id"`
	Name      string            `json:"name"`
	Arguments string            `json:"arguments"`
	Summary   []json.RawMessage `json:"summary"`
	Content   []json.RawMessage `json:"content"`
}

type openaiStreamContentPart struct {
	Type string `json:"type"`
}

type openaiStreamEvent struct {
	Type           string                   `json:"type"`
	SequenceNumber *int64                   `json:"sequence_number"`
	Response       *openaiStreamResponse    `json:"response"`
	OutputIndex    *int                     `json:"output_index"`
	Item           json.RawMessage          `json:"item"`
	Part           *openaiStreamContentPart `json:"part"`
	Delta          *string                  `json:"delta"`
	Text           *string                  `json:"text"`
	Arguments      *string                  `json:"arguments"`
	Code           string                   `json:"code"`
	Param          string                   `json:"param"`
	Message        string                   `json:"message"`
	Error          *openaiAPIError          `json:"error"`
}

type openaiStreamResult struct {
	servedModel, responseID, finishReason string
	usage                                 *openaiUsage
}

type openaiActiveItem struct {
	kind        string
	id          string
	callID      string
	name        string
	done        bool
	sawArgDelta bool
	text        strings.Builder
	arguments   strings.Builder
}

type openaiStreamState struct {
	result          openaiStreamResult
	started         bool
	terminal        bool
	hasSeq          bool
	lastSeq         int64
	nextOutputIndex int
	items           map[int]*openaiActiveItem
	toolIDs         map[string]struct{}
	parallel        bool
}

func (s *openaiStreamState) consume(event openaiStreamEvent) ([]*operatorv1.InferenceResponsePart, error) {
	invalid := constants.ErrInferenceProviderResponseInvalid
	if s.terminal {
		return nil, invalid
	}
	if event.Type == "error" {
		if event.SequenceNumber != nil {
			if s.hasSeq && *event.SequenceNumber <= s.lastSeq {
				return nil, invalid
			}
			s.hasSeq = true
			s.lastSeq = *event.SequenceNumber
		}
		return nil, constants.ErrInferenceProviderStreamError
	}
	if event.SequenceNumber == nil {
		return nil, invalid
	}
	if s.hasSeq && *event.SequenceNumber <= s.lastSeq {
		return nil, invalid
	}
	s.hasSeq = true
	s.lastSeq = *event.SequenceNumber

	if event.Type == "response.created" {
		r := event.Response
		if s.started || r == nil || r.ID == "" || validateHostedModelID(r.Model) != nil {
			return nil, invalid
		}
		s.started = true
		s.result.responseID = r.ID
		s.result.servedModel = r.Model
		return nil, nil
	}
	if !s.started {
		return nil, invalid
	}

	switch event.Type {
	case "response.in_progress":
		if r := event.Response; r != nil {
			if (r.ID != "" && r.ID != s.result.responseID) || (r.Model != "" && r.Model != s.result.servedModel) {
				return nil, invalid
			}
		}
		return nil, nil

	case "response.output_item.added":
		return nil, s.addItem(event)

	case "response.content_part.added", "response.content_part.done":
		item, err := s.openItem(event.OutputIndex, "message")
		if err != nil || event.Part == nil || event.Part.Type != "output_text" {
			return nil, invalid
		}
		_ = item
		return nil, nil

	case "response.output_text.delta":
		item, err := s.openItem(event.OutputIndex, "message")
		if err != nil || event.Delta == nil {
			return nil, invalid
		}
		if item.text.Len()+len(*event.Delta) > maxInferenceResponsePartTextBytes {
			return nil, invalid
		}
		item.text.WriteString(*event.Delta)
		return openaiTextParts(*event.Delta), nil

	case "response.output_text.done":
		item, err := s.openItem(event.OutputIndex, "message")
		if err != nil || event.Text == nil || item.text.String() != *event.Text {
			return nil, invalid
		}
		return nil, nil

	case "response.function_call_arguments.delta":
		item, err := s.openItem(event.OutputIndex, "function_call")
		if err != nil || event.Delta == nil {
			return nil, invalid
		}
		if item.arguments.Len()+len(*event.Delta) > maxInferenceToolJSONBytes {
			return nil, invalid
		}
		item.arguments.WriteString(*event.Delta)
		item.sawArgDelta = true
		return nil, nil

	case "response.function_call_arguments.done":
		item, err := s.openItem(event.OutputIndex, "function_call")
		if err != nil || event.Arguments == nil {
			return nil, invalid
		}
		if item.sawArgDelta {
			if item.arguments.String() != *event.Arguments {
				return nil, invalid
			}
		} else {
			if len(*event.Arguments) > maxInferenceToolJSONBytes {
				return nil, invalid
			}
			item.arguments.WriteString(*event.Arguments)
		}
		return nil, nil

	case "response.output_item.done":
		return s.finishItem(event)

	case "response.completed", "response.incomplete":
		return nil, s.finishResponse(event)

	case "response.failed":
		r := event.Response
		if r == nil || r.ID != s.result.responseID || (r.Model != "" && r.Model != s.result.servedModel) {
			return nil, invalid
		}
		return nil, constants.ErrInferenceProviderStreamError

	default:
		return nil, invalid
	}
}

func (s *openaiStreamState) openItem(outputIndex *int, wantKind string) (*openaiActiveItem, error) {
	if outputIndex == nil {
		return nil, constants.ErrInferenceProviderResponseInvalid
	}
	item, ok := s.items[*outputIndex]
	if !ok || item.done || item.kind != wantKind {
		return nil, constants.ErrInferenceProviderResponseInvalid
	}
	return item, nil
}

func (s *openaiStreamState) addItem(event openaiStreamEvent) error {
	invalid := constants.ErrInferenceProviderResponseInvalid
	if event.OutputIndex == nil || *event.OutputIndex != s.nextOutputIndex || len(event.Item) == 0 {
		return invalid
	}
	var item openaiStreamItem
	if json.Unmarshal(event.Item, &item) != nil {
		return invalid
	}
	active := &openaiActiveItem{kind: item.Type, id: item.ID}
	switch item.Type {
	case "reasoning":
		if item.ID == "" || len(item.Summary) > 0 || len(item.Content) > 0 {
			return invalid
		}
	case "message":
		if item.Role != "" && item.Role != "assistant" {
			return invalid
		}
	case "function_call":
		if item.CallID == "" || !validOpenAIToolName(item.Name) {
			return invalid
		}
		if _, duplicate := s.toolIDs[item.CallID]; duplicate || (!s.parallel && len(s.toolIDs) > 0) {
			return invalid
		}
		s.toolIDs[item.CallID] = struct{}{}
		active.callID = item.CallID
		active.name = item.Name
	default:
		return invalid
	}
	s.items[*event.OutputIndex] = active
	s.nextOutputIndex++
	return nil
}

func (s *openaiStreamState) finishItem(event openaiStreamEvent) ([]*operatorv1.InferenceResponsePart, error) {
	invalid := constants.ErrInferenceProviderResponseInvalid
	if event.OutputIndex == nil || len(event.Item) == 0 {
		return nil, invalid
	}
	active, ok := s.items[*event.OutputIndex]
	if !ok || active.done {
		return nil, invalid
	}
	var item openaiStreamItem
	if json.Unmarshal(event.Item, &item) != nil || item.Type != active.kind {
		return nil, invalid
	}
	active.done = true
	switch active.kind {
	case "reasoning":
		if item.ID != active.id {
			return nil, invalid
		}
		part, err := openaiReasoningPart(event.Item)
		if err != nil {
			return nil, err
		}
		return []*operatorv1.InferenceResponsePart{part}, nil

	case "function_call":
		if item.CallID != active.callID || item.Name != active.name {
			return nil, invalid
		}
		args := active.arguments.String()
		if args != "" && item.Arguments != "" && args != item.Arguments {
			return nil, invalid
		}
		if args == "" {
			args = item.Arguments
		}
		if args == "" {
			args = "{}"
		}
		canonical, err := canonicalizeJSON(args, true, nil)
		if err != nil {
			return nil, invalid
		}
		return []*operatorv1.InferenceResponsePart{{
			Part: &operatorv1.InferenceResponsePart_ToolCall{
				ToolCall: &operatorv1.InferenceToolCall{
					CallId:        active.callID,
					Name:          active.name,
					ArgumentsJson: canonical,
				},
			},
		}}, nil

	case "message":
		if item.Role != "" && item.Role != "assistant" {
			return nil, invalid
		}
		return nil, nil

	default:
		return nil, invalid
	}
}

func (s *openaiStreamState) finishResponse(event openaiStreamEvent) error {
	invalid := constants.ErrInferenceProviderResponseInvalid
	r := event.Response
	if r == nil || r.ID != s.result.responseID || validateHostedModelID(r.Model) != nil || r.Model != s.result.servedModel {
		return invalid
	}
	for _, active := range s.items {
		if !active.done {
			return invalid
		}
	}
	switch event.Type {
	case "response.completed":
		if r.Status != "completed" {
			return invalid
		}
		s.result.finishReason = "stop"
	case "response.incomplete":
		if r.Status != "incomplete" {
			return invalid
		}
		reason := ""
		if r.IncompleteDetails != nil {
			reason = r.IncompleteDetails.Reason
		}
		s.result.finishReason = openaiFinishReason(r.Status, reason)
	default:
		return invalid
	}
	s.result.usage = r.Usage
	s.terminal = true
	return nil
}

func openaiTextParts(text string) []*operatorv1.InferenceResponsePart {
	if text == "" {
		return nil
	}
	return []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: text}}}
}

func openaiFinishReason(status, incompleteReason string) string {
	switch status {
	case "completed":
		return "stop"
	case "incomplete":
		if incompleteReason == "max_output_tokens" {
			return "length"
		}
		return "error"
	default:
		return "error"
	}
}

func (b *OpenAIBackend) decodeStream(
	ctx context.Context,
	body io.Reader,
	arrivalTime func(int64) *int64,
	parallel bool,
	attemptID string,
	stream *streamSegmenter,
) (openaiStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
	fail := func(err error) (openaiStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
		return openaiStreamResult{}, nil, nil, fmt.Errorf("%s: decode stream: %w", openaiComponent, err)
	}
	reader := bufio.NewReaderSize(body, 64<<10)
	state := openaiStreamState{
		parallel: parallel,
		items:    make(map[int]*openaiActiveItem),
		toolIDs:  make(map[string]struct{}),
	}
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
		var event openaiStreamEvent
		if json.Unmarshal(data, &event) != nil {
			return fail(constants.ErrInferenceProviderResponseInvalid)
		}
		eventParts, err := state.consume(event)
		if err != nil {
			if errors.Is(err, constants.ErrInferenceProviderStreamError) {
				b.logger.Warn("OpenAI ended the response stream with an error",
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
	if !state.terminal && state.result.finishReason != constants.InferenceFinishReasonRepetition {
		return fail(constants.ErrInferenceProviderResponseInvalid)
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

// statusError classifies a non-200 response by HTTP status first, then
// error.code / error.param / error.type (and bounded message hints on 400).
// Provider messages and bodies can echo prompts or keys and are never logged,
// returned, or signed (INV-STOR-LOG-01).
func (b *OpenAIBackend) statusError(status int, body []byte, model, attemptID string) error {
	var envelope struct {
		Error openaiAPIError `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	b.logger.Warn("OpenAI request failed",
		"status", status,
		"provider_attempt_id", attemptID,
	)

	apiErr := envelope.Error
	message := strings.ToLower(apiErr.Message)
	kind := constants.ErrInferenceGenerateFailed

	switch {
	case status == http.StatusNotFound || apiErr.Code == "model_not_found":
		return fmt.Errorf("%s: provider status %d: %w: model %q", openaiComponent, status, constants.ErrInferenceModelNotFound, model)
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout || apiErr.Code == "timeout":
		kind = constants.ErrInferenceBackendTimeout
	case status == http.StatusRequestEntityTooLarge || apiErr.Code == "context_length_exceeded":
		kind = constants.ErrInferenceContextOverflow
	case status >= http.StatusMultipleChoices && status < http.StatusBadRequest:
		kind = constants.ErrInferenceBackendUnavailable
	case status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError ||
		apiErr.Code == "rate_limit_exceeded" || apiErr.Code == "insufficient_quota" || apiErr.Type == "rate_limit_error" || apiErr.Type == "server_error":
		kind = constants.ErrInferenceBackendUnavailable
	case status == http.StatusBadRequest:
		switch {
		case strings.Contains(message, "context length") || strings.Contains(message, "context window"):
			kind = constants.ErrInferenceContextOverflow
		case apiErr.Param == "temperature" || apiErr.Param == "top_p" || apiErr.Param == "reasoning" ||
			apiErr.Param == "text" || apiErr.Param == "text.format" || apiErr.Param == "seed" || apiErr.Param == "stop" || apiErr.Param == "top_k" ||
			apiErr.Code == "unsupported_parameter" || apiErr.Code == "invalid_json_schema" ||
			strings.Contains(message, "temperature") || strings.Contains(message, "top_p") ||
			strings.Contains(message, "reasoning") || strings.Contains(message, "schema"):
			kind = constants.ErrInferenceCapabilityUnsupported
		case apiErr.Param == "tools" || apiErr.Param == "tool_choice" || apiErr.Param == "parallel_tool_calls" ||
			strings.Contains(message, "tool"):
			kind = constants.ErrInferenceToolsUnsupported
		case apiErr.Param == "max_output_tokens":
			kind = constants.ErrInferenceGenerationOptionsInvalid
		}
	}
	return fmt.Errorf("%s: provider status %d: %w", openaiComponent, status, kind)
}
