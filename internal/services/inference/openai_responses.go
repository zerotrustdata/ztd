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
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const openaiMinMaxOutputTokens = 16

var openaiIncludeFields = []string{"reasoning.encrypted_content"}

type openaiInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openaiInputMessage struct {
	Type    string               `json:"type"`
	Role    string               `json:"role"`
	Content []openaiInputContent `json:"content"`
}

type openaiFunctionCallItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiFunctionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// openaiFunctionTool is one function declaration. Strict is always sent and
// always false: OpenAI strict mode requires every property in required, and
// native tool schemas declare optional arguments. Tool call arguments are
// validated against the typed tool contract at the governance boundary, as
// they are for every other provider.
type openaiFunctionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type openaiTextFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict *bool           `json:"strict,omitempty"`
}

type openaiTextConfig struct {
	Format *openaiTextFormat `json:"format,omitempty"`
}

type openaiReasoningConfig struct {
	Effort string `json:"effort,omitempty"`
}

// openaiReasoningItem is the wire schema for an OpenAI Responses API encrypted
// reasoning item. Summary and Content must remain empty so no readable
// reasoning text can bypass egress or ingress scrubbing.
type openaiReasoningItem struct {
	Type             string            `json:"type"`
	ID               string            `json:"id"`
	EncryptedContent *string           `json:"encrypted_content,omitempty"`
	Summary          []json.RawMessage `json:"summary,omitempty"`
	Content          []json.RawMessage `json:"content,omitempty"`
	Status           string            `json:"status,omitempty"`
}

type openaiGenerateRequest struct {
	Model             string                 `json:"model"`
	Input             []json.RawMessage      `json:"input"`
	Instructions      string                 `json:"instructions,omitempty"`
	Tools             []openaiFunctionTool   `json:"tools,omitempty"`
	ToolChoice        string                 `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                  `json:"parallel_tool_calls,omitempty"`
	Temperature       *float32               `json:"temperature,omitempty"`
	TopP              *float32               `json:"top_p,omitempty"`
	MaxOutputTokens   *int32                 `json:"max_output_tokens,omitempty"`
	Text              *openaiTextConfig      `json:"text,omitempty"`
	Reasoning         *openaiReasoningConfig `json:"reasoning,omitempty"`
	Include           []string               `json:"include"`
	Store             bool                   `json:"store"`
	Stream            bool                   `json:"stream"`
}

func validateOpenAIOptions(req models.GenerateRequest) error {
	if req.Seed != nil || len(req.StopSequences) > 0 || req.TopK != nil || req.ContextLimit != nil {
		return fmt.Errorf("%s: unsupported parameter or context override: %w", openaiComponent, constants.ErrInferenceCapabilityUnsupported)
	}
	if req.MaxTokens < 0 || (req.MaxTokens > 0 && req.MaxTokens < openaiMinMaxOutputTokens) {
		return fmt.Errorf("%s: max_output_tokens: %w", openaiComponent, constants.ErrInferenceGenerationOptionsInvalid)
	}
	return nil
}

func buildOpenAIRequest(req models.GenerateRequest) (openaiGenerateRequest, error) {
	if err := validateOpenAIOptions(req); err != nil {
		return openaiGenerateRequest{}, err
	}
	instructions, input, err := inferenceMessagesToOpenAI(req.Messages)
	if err != nil {
		return openaiGenerateRequest{}, fmt.Errorf("messages: %w", err)
	}
	tools, err := inferenceToolsToOpenAI(req.Tools, req.ToolChoice)
	if err != nil {
		return openaiGenerateRequest{}, fmt.Errorf("tools: %w", err)
	}
	reasoning, err := openaiReasoning(req.Thinking)
	if err != nil {
		return openaiGenerateRequest{}, err
	}
	body := openaiGenerateRequest{
		Model:        req.Model,
		Input:        input,
		Instructions: instructions,
		Tools:        tools,
		TopP:         req.TopP,
		Reasoning:    reasoning,
		Include:      openaiIncludeFields,
		Store:        false,
		Stream:       true,
	}
	if req.Temperature != 0 {
		temperature := req.Temperature
		body.Temperature = &temperature
	}
	if req.MaxTokens > 0 {
		maxTokens := req.MaxTokens
		body.MaxOutputTokens = &maxTokens
	}
	if len(tools) > 0 {
		body.ToolChoice = "auto"
		if req.ParallelToolCalls != nil {
			body.ParallelToolCalls = req.ParallelToolCalls
		}
	}
	if req.ResponseFormat != nil {
		if req.ResponseFormat.GetMediaType() != inferenceJSONMediaType {
			return openaiGenerateRequest{}, fmt.Errorf("response format: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		if err := validateInferenceToolSchema(req.ResponseFormat.GetJsonSchema()); err != nil {
			return openaiGenerateRequest{}, fmt.Errorf("response format: %w", err)
		}
		strict := true
		body.Text = &openaiTextConfig{
			Format: &openaiTextFormat{
				Type:   "json_schema",
				Name:   "response",
				Schema: json.RawMessage(req.ResponseFormat.GetJsonSchema()),
				Strict: &strict,
			},
		}
	}
	return body, nil
}

func openaiReasoning(control *operatorv1.InferenceThinkingControl) (*openaiReasoningConfig, error) {
	if control == nil {
		return nil, nil
	}
	mode, ok := control.GetMode().(*operatorv1.InferenceThinkingControl_Enabled)
	if !ok || control.GetIncludeThoughts() || !mode.Enabled {
		return nil, fmt.Errorf("thinking control: %w", constants.ErrInferenceCapabilityUnsupported)
	}
	// Never request reasoning summary; encrypted content is requested via Include.
	return &openaiReasoningConfig{Effort: "medium"}, nil
}

func marshalOpenAIItem(item any) (json.RawMessage, error) {
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("marshal input item: %w", constants.ErrInferenceMessageInvalid)
	}
	return raw, nil
}

func inferenceMessagesToOpenAI(messages []*operatorv1.InferenceMessage) (string, []json.RawMessage, error) {
	var instructionParts []string
	input := make([]json.RawMessage, 0, len(messages))

	for messageIndex, message := range messages {
		if message == nil || len(message.GetParts()) == 0 {
			return "", nil, fmt.Errorf("%w: message %d", constants.ErrInferenceMessageInvalid, messageIndex)
		}
		role := message.GetRole()
		switch role {
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM:
			var sysTexts []string
			for partIndex, part := range message.GetParts() {
				if part == nil || part.GetPart() == nil {
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
				textPart, ok := part.GetPart().(*operatorv1.InferenceMessagePart_Text)
				if !ok {
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
				sysTexts = append(sysTexts, textPart.Text)
			}
			if len(input) == 0 {
				instructionParts = append(instructionParts, sysTexts...)
			} else {
				content := make([]openaiInputContent, 0, len(sysTexts))
				for _, t := range sysTexts {
					content = append(content, openaiInputContent{Type: "input_text", Text: t})
				}
				raw, err := marshalOpenAIItem(openaiInputMessage{Type: "message", Role: "developer", Content: content})
				if err != nil {
					return "", nil, err
				}
				input = append(input, raw)
			}

		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER:
			content := make([]openaiInputContent, 0, len(message.GetParts()))
			for partIndex, part := range message.GetParts() {
				if part == nil || part.GetPart() == nil {
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
				textPart, ok := part.GetPart().(*operatorv1.InferenceMessagePart_Text)
				if !ok {
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
				content = append(content, openaiInputContent{Type: "input_text", Text: textPart.Text})
			}
			raw, err := marshalOpenAIItem(openaiInputMessage{Type: "message", Role: "user", Content: content})
			if err != nil {
				return "", nil, err
			}
			input = append(input, raw)

		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT:
			var pendingText []openaiInputContent
			flushText := func() error {
				if len(pendingText) == 0 {
					return nil
				}
				raw, err := marshalOpenAIItem(openaiInputMessage{Type: "message", Role: "assistant", Content: pendingText})
				if err != nil {
					return err
				}
				input = append(input, raw)
				pendingText = nil
				return nil
			}
			hasNonText := false
			for _, part := range message.GetParts() {
				if part != nil {
					if _, isText := part.GetPart().(*operatorv1.InferenceMessagePart_Text); !isText {
						hasNonText = true
						break
					}
				}
			}
			for partIndex, part := range message.GetParts() {
				if part == nil || part.GetPart() == nil {
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
				switch value := part.GetPart().(type) {
				case *operatorv1.InferenceMessagePart_Text:
					if value.Text == "" && hasNonText {
						continue
					}
					pendingText = append(pendingText, openaiInputContent{Type: "output_text", Text: value.Text})
				case *operatorv1.InferenceMessagePart_ToolCall:
					if err := flushText(); err != nil {
						return "", nil, err
					}
					call := value.ToolCall
					if call == nil || call.GetCallId() == "" || !validOpenAIToolName(call.GetName()) || call.GetProviderSignature() != "" {
						return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
					}
					args, err := canonicalizeJSON(call.GetArgumentsJson(), true, nil)
					if err != nil {
						return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
					}
					raw, err := marshalOpenAIItem(openaiFunctionCallItem{
						Type:      "function_call",
						CallID:    call.GetCallId(),
						Name:      call.GetName(),
						Arguments: args,
					})
					if err != nil {
						return "", nil, err
					}
					input = append(input, raw)
				case *operatorv1.InferenceMessagePart_ProviderReasoning:
					if err := flushText(); err != nil {
						return "", nil, err
					}
					item := value.ProviderReasoning
					if item == nil || item.GetProviderClass() != constants.InferenceProviderOpenAI {
						return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
					}
					if len(item.GetItemJson()) > maxInferenceProviderReasoningBytes {
						return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceRequestTooLarge)
					}
					if _, err := normalizeCanonicalJSON(item.GetItemJson(), true, nil); err != nil {
						return "", nil, fmt.Errorf("message %d part %d: opaque reasoning: %w", messageIndex, partIndex, err)
					}
					if err := validateOpenAIReasoning([]byte(item.GetItemJson())); err != nil {
						return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
					}
					input = append(input, json.RawMessage(item.GetItemJson()))
				default:
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
			}
			if err := flushText(); err != nil {
				return "", nil, err
			}

		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL:
			for partIndex, part := range message.GetParts() {
				if part == nil || part.GetPart() == nil {
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
				tr, ok := part.GetPart().(*operatorv1.InferenceMessagePart_ToolResult)
				if !ok || tr.ToolResult == nil || tr.ToolResult.GetCallId() == "" || tr.ToolResult.GetName() == "" {
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
				output, err := canonicalizeJSON(tr.ToolResult.GetResultJson(), false, nil)
				if err != nil {
					return "", nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, constants.ErrInferenceMessageInvalid)
				}
				raw, err := marshalOpenAIItem(openaiFunctionCallOutputItem{
					Type:   "function_call_output",
					CallID: tr.ToolResult.GetCallId(),
					Output: output,
				})
				if err != nil {
					return "", nil, err
				}
				input = append(input, raw)
			}

		default:
			return "", nil, fmt.Errorf("%w: message %d role", constants.ErrInferenceMessageInvalid, messageIndex)
		}
	}

	if len(input) == 0 {
		return "", nil, fmt.Errorf("conversation requires non-system input: %w", constants.ErrInferenceMessageInvalid)
	}
	return strings.Join(instructionParts, "\n"), input, nil
}

func validOpenAIToolName(name string) bool {
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

func inferenceToolsToOpenAI(tools []*operatorv1.InferenceToolDeclaration, choice *operatorv1.InferenceToolChoice) ([]openaiFunctionTool, error) {
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
	declarations := make([]openaiFunctionTool, 0, len(tools))
	for i, tool := range tools {
		if tool == nil || !validOpenAIToolName(tool.GetName()) {
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
		declarations = append(declarations, openaiFunctionTool{
			Type:        "function",
			Name:        tool.GetName(),
			Description: tool.GetDescription(),
			Parameters:  json.RawMessage(tool.GetJsonSchema()),
			Strict:      false,
		})
	}
	return declarations, nil
}

// validateOpenAIReasoning checks an OpenAI reasoning item against the typed
// schema and fails closed if any readable summary or content is present or if
// id or encrypted_content is missing.
func validateOpenAIReasoning(raw []byte) error {
	if len(raw) == 0 || len(raw) > maxInferenceProviderReasoningBytes {
		return constants.ErrInferenceProviderResponseInvalid
	}
	var item openaiReasoningItem
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&item); err != nil {
		return constants.ErrInferenceProviderResponseInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return constants.ErrInferenceProviderResponseInvalid
	}
	if item.Type != "reasoning" || item.ID == "" || item.EncryptedContent == nil || *item.EncryptedContent == "" {
		return constants.ErrInferenceProviderResponseInvalid
	}
	if len(item.Summary) > 0 || len(item.Content) > 0 {
		return constants.ErrInferenceProviderResponseInvalid
	}
	if item.Status != "" && item.Status != "completed" && item.Status != "incomplete" {
		return constants.ErrInferenceProviderResponseInvalid
	}
	return nil
}

func openaiReasoningPart(raw []byte) (*operatorv1.InferenceResponsePart, error) {
	if err := validateOpenAIReasoning(raw); err != nil {
		return nil, err
	}
	canonical, err := canonicalizeJSON(string(raw), true, nil)
	if err != nil {
		return nil, constants.ErrInferenceProviderResponseInvalid
	}
	if len(canonical) > maxInferenceProviderReasoningBytes {
		return nil, constants.ErrInferenceProviderResponseInvalid
	}
	return &operatorv1.InferenceResponsePart{
		Part: &operatorv1.InferenceResponsePart_ProviderReasoning{
			ProviderReasoning: &operatorv1.InferenceProviderReasoning{
				ProviderClass: constants.InferenceProviderOpenAI,
				ItemJson:      canonical,
			},
		},
	}, nil
}
