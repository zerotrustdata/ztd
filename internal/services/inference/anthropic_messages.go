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
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Pointers distinguish a required empty thinking field from a missing field.
// Only these fields may bypass scrubbing as opaque provider state.
type anthropicReasoningBlock struct {
	Type      string  `json:"type"`
	Thinking  *string `json:"thinking,omitempty"`
	Signature *string `json:"signature,omitempty"`
	Data      *string `json:"data,omitempty"`
}

type anthropicBlock struct {
	anthropicReasoningBlock
	Text      *string         `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   *string         `json:"content,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	Strict      bool            `json:"strict"`
}

type anthropicToolChoice struct {
	Type                   string `json:"type"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type anthropicThinkingConfig struct {
	Type    string `json:"type"`
	Display string `json:"display,omitempty"`
}

type anthropicOutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

type anthropicOutputConfig struct {
	Format anthropicOutputFormat `json:"format"`
}

type anthropicGenerateRequest struct {
	Model         string                   `json:"model"`
	MaxTokens     int32                    `json:"max_tokens"`
	Stream        bool                     `json:"stream"`
	System        []anthropicBlock         `json:"system,omitempty"`
	Messages      []anthropicMessage       `json:"messages"`
	Tools         []anthropicTool          `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoice     `json:"tool_choice,omitempty"`
	Thinking      *anthropicThinkingConfig `json:"thinking,omitempty"`
	StopSequences []string                 `json:"stop_sequences,omitempty"`
	OutputConfig  *anthropicOutputConfig   `json:"output_config,omitempty"`
}

func validateAnthropicOptions(req models.GenerateRequest) error {
	if req.Temperature != 0 || req.TopP != nil || req.TopK != nil || req.Seed != nil || req.ContextLimit != nil {
		return fmt.Errorf("%s: sampling or context override: %w", anthropicComponent, constants.ErrInferenceCapabilityUnsupported)
	}
	if req.MaxTokens < 0 {
		return fmt.Errorf("%s: max tokens: %w", anthropicComponent, constants.ErrInferenceGenerationOptionsInvalid)
	}
	return nil
}

func buildAnthropicRequest(req models.GenerateRequest, metadata anthropicModel) (anthropicGenerateRequest, error) {
	if err := validateAnthropicOptions(req); err != nil {
		return anthropicGenerateRequest{}, err
	}
	if err := metadata.validate(); err != nil {
		return anthropicGenerateRequest{}, err
	}
	system, messages, err := inferenceMessagesToAnthropic(req.Messages)
	if err != nil {
		return anthropicGenerateRequest{}, err
	}
	tools, err := inferenceToolsToAnthropic(req.Tools, req.ToolChoice)
	if err != nil {
		return anthropicGenerateRequest{}, err
	}
	thinking, err := anthropicThinking(req.Thinking, metadata.Capabilities.Thinking)
	if err != nil {
		return anthropicGenerateRequest{}, err
	}
	body := anthropicGenerateRequest{
		Model: req.Model, MaxTokens: req.MaxTokens, Stream: true, System: system, Messages: messages,
		Tools: tools, Thinking: thinking, StopSequences: req.StopSequences,
	}
	// Messages requires max_tokens. Its default is the provider-reported output
	// capacity, never a local generation cap or a checked-in model table.
	if body.MaxTokens == 0 {
		body.MaxTokens = int32(metadata.MaxTokens)
	}
	if len(tools) > 0 {
		body.ToolChoice = &anthropicToolChoice{Type: "auto", DisableParallelToolUse: req.ParallelToolCalls != nil && !*req.ParallelToolCalls}
	}
	if req.ResponseFormat != nil {
		if req.ResponseFormat.GetMediaType() != inferenceJSONMediaType || !metadata.Capabilities.StructuredOutputs.Supported {
			return anthropicGenerateRequest{}, fmt.Errorf("response format: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		if err := validateInferenceToolSchema(req.ResponseFormat.GetJsonSchema()); err != nil {
			return anthropicGenerateRequest{}, fmt.Errorf("response format: %w", err)
		}
		body.OutputConfig = &anthropicOutputConfig{Format: anthropicOutputFormat{
			Type: "json_schema", Schema: json.RawMessage(req.ResponseFormat.GetJsonSchema()),
		}}
	}
	return body, nil
}

func anthropicThinking(control *operatorv1.InferenceThinkingControl, capabilities anthropicThinkingCapabilities) (*anthropicThinkingConfig, error) {
	if control != nil {
		mode, ok := control.GetMode().(*operatorv1.InferenceThinkingControl_Enabled)
		if !ok || control.GetIncludeThoughts() {
			return nil, fmt.Errorf("thinking control: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		if !mode.Enabled {
			if !capabilities.Supported || capabilities.Types.Disabled.Supported {
				return &anthropicThinkingConfig{Type: "disabled"}, nil
			}
			// between_tools does not disable thinking and cannot omit its
			// readable updates. It cannot satisfy this control or opaque replay.
			return nil, fmt.Errorf("disabled thinking: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		if !capabilities.Types.Adaptive.Supported {
			return nil, fmt.Errorf("adaptive thinking: %w", constants.ErrInferenceCapabilityUnsupported)
		}
	}
	if capabilities.Types.Adaptive.Supported {
		return &anthropicThinkingConfig{Type: "adaptive", Display: "omitted"}, nil
	}
	if capabilities.Supported && !capabilities.Types.Disabled.Supported {
		return nil, fmt.Errorf("thinking mode: %w", constants.ErrInferenceCapabilityUnsupported)
	}
	// Older models default to no thinking; explicitly keep it disabled. Manual
	// extended thinking would require inventing a budget absent from the intent.
	return &anthropicThinkingConfig{Type: "disabled"}, nil
}

func inferenceMessagesToAnthropic(messages []*operatorv1.InferenceMessage) ([]anthropicBlock, []anthropicMessage, error) {
	var system []anthropicBlock
	var contents []anthropicMessage
	toolTurnOpen := false
	for i, message := range messages {
		if message == nil || len(message.GetParts()) == 0 {
			return nil, nil, fmt.Errorf("message %d: %w", i, constants.ErrInferenceMessageInvalid)
		}
		var blocks []anthropicBlock
		for j, part := range message.GetParts() {
			block, err := inferenceMessagePartToAnthropic(message.GetRole(), part)
			if err != nil {
				return nil, nil, fmt.Errorf("message %d part %d: %w", i, j, err)
			}
			blocks = append(blocks, block)
		}
		switch message.GetRole() {
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM:
			// Moving a mid-history system turn would edit signed thinking history.
			if len(contents) > 0 {
				return nil, nil, fmt.Errorf("system message order: %w", constants.ErrInferenceMessageInvalid)
			}
			system = append(system, blocks...)
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER:
			contents = append(contents, anthropicMessage{Role: "user", Content: blocks})
			toolTurnOpen = false
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT:
			contents = append(contents, anthropicMessage{Role: "assistant", Content: blocks})
			toolTurnOpen = false
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL:
			if toolTurnOpen {
				last := &contents[len(contents)-1]
				last.Content = append(last.Content, blocks...)
			} else {
				contents = append(contents, anthropicMessage{Role: "user", Content: blocks})
			}
			toolTurnOpen = true
		default:
			return nil, nil, fmt.Errorf("message role: %w", constants.ErrInferenceMessageInvalid)
		}
	}
	if len(contents) == 0 || contents[0].Role != "user" || contents[len(contents)-1].Role == "assistant" {
		return nil, nil, fmt.Errorf("conversation requires user start and no assistant prefill: %w", constants.ErrInferenceMessageInvalid)
	}
	return system, contents, nil
}

func inferenceMessagePartToAnthropic(role operatorv1.InferenceMessageRole, part *operatorv1.InferenceMessagePart) (anthropicBlock, error) {
	invalid := constants.ErrInferenceMessageInvalid
	if part == nil {
		return anthropicBlock{}, invalid
	}
	switch value := part.GetPart().(type) {
	case *operatorv1.InferenceMessagePart_Text:
		if role == operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL {
			return anthropicBlock{}, invalid
		}
		text := value.Text
		return anthropicBlock{anthropicReasoningBlock: anthropicReasoningBlock{Type: "text"}, Text: &text}, nil
	case *operatorv1.InferenceMessagePart_ToolCall:
		call := value.ToolCall
		if role != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT || call.GetCallId() == "" || !validAnthropicToolName(call.GetName()) || call.GetProviderSignature() != "" {
			return anthropicBlock{}, invalid
		}
		args, err := canonicalizeJSON(call.GetArgumentsJson(), true, nil)
		if err != nil {
			return anthropicBlock{}, invalid
		}
		return anthropicBlock{anthropicReasoningBlock: anthropicReasoningBlock{Type: "tool_use"}, ID: call.GetCallId(), Name: call.GetName(), Input: json.RawMessage(args)}, nil
	case *operatorv1.InferenceMessagePart_ToolResult:
		result := value.ToolResult
		if role != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL || result.GetCallId() == "" {
			return anthropicBlock{}, invalid
		}
		content, err := canonicalizeJSON(result.GetResultJson(), false, nil)
		if err != nil {
			return anthropicBlock{}, invalid
		}
		return anthropicBlock{anthropicReasoningBlock: anthropicReasoningBlock{Type: "tool_result"}, ToolUseID: result.GetCallId(), Content: &content}, nil
	case *operatorv1.InferenceMessagePart_ProviderReasoning:
		item := value.ProviderReasoning
		if role != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT || item.GetProviderClass() != constants.InferenceProviderAnthropic {
			return anthropicBlock{}, invalid
		}
		if len(item.GetItemJson()) > maxInferenceProviderReasoningBytes {
			return anthropicBlock{}, constants.ErrInferenceRequestTooLarge
		}
		if _, err := normalizeCanonicalJSON(item.GetItemJson(), true, nil); err != nil {
			return anthropicBlock{}, fmt.Errorf("opaque reasoning: %w", err)
		}
		block, err := decodeAnthropicReasoning([]byte(item.GetItemJson()), true)
		if err != nil {
			return anthropicBlock{}, invalid
		}
		return anthropicBlock{anthropicReasoningBlock: block}, nil
	default:
		return anthropicBlock{}, invalid
	}
}

func validAnthropicToolName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func inferenceToolsToAnthropic(tools []*operatorv1.InferenceToolDeclaration, choice *operatorv1.InferenceToolChoice) ([]anthropicTool, error) {
	if choice != nil && choice.GetMode() != operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO {
		return nil, constants.ErrInferenceCapabilityUnsupported
	}
	if err := validateInferenceToolChoice(&models.InferenceRequestPayload{ToolChoice: choice, Tools: tools}); err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{})
	for _, name := range choice.GetAllowedToolNames() {
		allowed[name] = struct{}{}
	}
	declarations := make([]anthropicTool, 0, len(tools))
	for i, tool := range tools {
		if !validAnthropicToolName(tool.GetName()) {
			return nil, fmt.Errorf("tool %d name: %w", i, constants.ErrInferenceToolSchemaInvalid)
		}
		if err := validateInferenceToolSchema(tool.GetJsonSchema()); err != nil {
			return nil, fmt.Errorf("tool %d schema: %w", i, constants.ErrInferenceToolSchemaInvalid)
		}
		if len(allowed) > 0 {
			if _, ok := allowed[tool.GetName()]; !ok {
				continue
			}
		}
		declarations = append(declarations, anthropicTool{Name: tool.GetName(), Description: tool.GetDescription(), InputSchema: json.RawMessage(tool.GetJsonSchema()), Strict: true})
	}
	return declarations, nil
}

// decodeAnthropicReasoning rejects unknown fields, mixed block shapes and every
// readable thinking byte. Neither decoder errors nor provider text may escape.
func decodeAnthropicReasoning(raw []byte, complete bool) (anthropicReasoningBlock, error) {
	var block anthropicReasoningBlock
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&block); err != nil || len(raw) > maxInferenceProviderReasoningBytes {
		return anthropicReasoningBlock{}, constants.ErrInferenceProviderResponseInvalid
	}
	valid := false
	switch block.Type {
	case "thinking":
		valid = block.Thinking != nil && *block.Thinking == "" && block.Signature != nil && block.Data == nil && (!complete || *block.Signature != "")
	case "redacted_thinking":
		valid = block.Data != nil && *block.Data != "" && block.Thinking == nil && block.Signature == nil
	}
	if !valid {
		return anthropicReasoningBlock{}, constants.ErrInferenceProviderResponseInvalid
	}
	return block, nil
}

func anthropicReasoningPart(block anthropicReasoningBlock) (*operatorv1.InferenceResponsePart, error) {
	raw, err := json.Marshal(block)
	if err != nil {
		return nil, fmt.Errorf("reasoning block: %w", constants.ErrInferenceProviderResponseInvalid)
	}
	if _, err := decodeAnthropicReasoning(raw, true); err != nil {
		return nil, err
	}
	canonical, err := canonicalizeJSON(string(raw), true, nil)
	if err != nil {
		return nil, constants.ErrInferenceProviderResponseInvalid
	}
	return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{
		ProviderClass: constants.InferenceProviderAnthropic, ItemJson: canonical,
	}}}, nil
}
