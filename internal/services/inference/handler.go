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
	"fmt"
	"log/slog"
	"math"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jsonschema"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

const inferenceJSONMediaType = "application/json"

// InferenceExecutionHandler implements governance.ExecutionHandler. It decodes and validates the governed typed conversation, recursively scrubs data-bearing values, calls Backend.Generate, and returns the digest the L5 actuator records in the receipt.
type InferenceExecutionHandler struct {
	backend   Backend
	cfg       *config.Config
	scrubbing *scrubbing.ScrubbingService
	logger    *slog.Logger
}

// NewInferenceExecutionHandler constructs an InferenceExecutionHandler. Every
// dependency is required: a handler without a scrubbing service would send
// unscrubbed data across the provider boundary, so construction fails closed
// with constants.ErrMissingRequiredField instead.
func NewInferenceExecutionHandler(backend Backend, cfg *config.Config, scrubbingSvc *scrubbing.ScrubbingService, logger *slog.Logger) (*InferenceExecutionHandler, error) {
	if backend == nil || cfg == nil || scrubbingSvc == nil || logger == nil {
		return nil, fmt.Errorf("inference handler: %w", constants.ErrMissingRequiredField)
	}
	return &InferenceExecutionHandler{
		backend:   backend,
		cfg:       cfg,
		scrubbing: scrubbingSvc,
		logger:    logger,
	}, nil
}

// ExecuteVerifiedTransaction implements governance.ExecutionHandler. It is
// called by the L5 actuator after L1–L4 verification passes. It delegates to
// ExecuteInference and returns the canonical result digest as the receipt
// summary so the signed ActionReceipt binds the complete InferenceResult
// (see operator.proto InferenceResult.result_digest).
func (h *InferenceExecutionHandler) ExecuteVerifiedTransaction(ctx context.Context, eventType constants.EventType, cmdMsg governance.CommandMessage) (string, error) {
	resp, err := h.ExecuteInference(ctx, cmdMsg)
	if err != nil {
		return "", err
	}
	digest, err := models.ComputeInferenceResultDigest(resp.ToProtoInferenceResult())
	if err != nil {
		return "", err
	}
	return digest, nil
}

// scrubResponseParts returns a scrubbed copy of the provider response parts. Every content-bearing
// string (text, thinking, tool-call name and arguments) passes through ScrubText exactly once. Call
// IDs are provider identifiers, not content, and are kept. The caller's parts are never mutated.
// It fails closed with constants.ErrEgressScrubberUnavailable when the response carries content and
// the scrubber is nil or disabled; raw output is never returned.
func scrubResponseParts(ctx context.Context, parts []*operatorv1.InferenceResponsePart, s *scrubbing.ScrubbingService) ([]*operatorv1.InferenceResponsePart, error) {
	out := make([]*operatorv1.InferenceResponsePart, 0, len(parts))
	var fields []*string
	for _, part := range parts {
		if part == nil {
			continue
		}
		clone, ok := proto.Clone(part).(*operatorv1.InferenceResponsePart)
		if !ok {
			return nil, fmt.Errorf("inference response part: %w", constants.ErrInferenceResultDigest)
		}
		switch v := clone.GetPart().(type) {
		case *operatorv1.InferenceResponsePart_Text:
			fields = append(fields, &v.Text)
		case *operatorv1.InferenceResponsePart_Thinking:
			fields = append(fields, &v.Thinking)
		case *operatorv1.InferenceResponsePart_ToolCall:
			if v.ToolCall != nil {
				fields = append(fields, &v.ToolCall.Name, &v.ToolCall.ArgumentsJson)
			}
		}
		out = append(out, clone)
	}

	hasContent := false
	for _, field := range fields {
		if *field != "" {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return out, nil
	}
	if s == nil || !s.IsEnabled() {
		return nil, fmt.Errorf("inference response: %w", constants.ErrEgressScrubberUnavailable)
	}
	for _, field := range fields {
		if *field != "" {
			*field = s.ScrubText(ctx, *field)
		}
	}
	return out, nil
}

// ExecuteInference decodes the protobuf InferenceRequested payload, validates and scrubs its typed conversation, authorizes its model against the active standard or campaign authority, and calls Backend.Generate.
func (h *InferenceExecutionHandler) ExecuteInference(ctx context.Context, cmdMsg governance.CommandMessage) (*models.GenerateResponse, error) {
	payloadBytes := cmdMsg.GetPayload()
	if len(payloadBytes) == 0 {
		return nil, fmt.Errorf("inference handler: empty payload: %w", constants.ErrPubSubEmptyPayload)
	}
	if len(payloadBytes) > maxInferenceGovernedPayloadBytes {
		return nil, fmt.Errorf("inference handler: %w", constants.ErrInferenceRequestTooLarge)
	}

	req := &operatorv1.InferenceRequested{}
	if err := proto.Unmarshal(payloadBytes, req); err != nil {
		return nil, fmt.Errorf("inference handler: unmarshal payload: %w", err)
	}

	infReq := models.FromProtoInferenceRequested(req)
	if err := validateInferenceRequestBounds(req); err != nil {
		return nil, fmt.Errorf("inference handler: %w", err)
	}
	if err := h.normalizeInferenceInput(ctx, &infReq); err != nil {
		return nil, fmt.Errorf("inference handler: normalize request: %w", err)
	}

	if infReq.Role == models.InferenceModelRoleUnspecified {
		return nil, fmt.Errorf("inference handler: %w", constants.ErrInferenceRoleInvalid)
	}
	approved, err := h.authorizeInferenceModel(infReq)
	if err != nil {
		return nil, fmt.Errorf("inference handler: authorize model: %w", err)
	}
	genReq := infReq.ToGenerateRequest(approved)
	// The backend scrubs streamed output at the source with this scrubber (see stream_segmenter.go).
	genReq.Scrubber = h.scrubbing
	// Apply config default keep-alive when the request does not override it.
	if genReq.KeepAlive == "" {
		genReq.KeepAlive = h.cfg.Inference.KeepAlive
	}

	h.logger.Info("Dispatching governed inference request",
		"role", infReq.Role,
		"model", genReq.Model,
		"message_count", len(genReq.Messages),
		"tool_count", len(genReq.Tools))

	resp, err := h.backend.Generate(ctx, genReq)
	if err != nil {
		return nil, fmt.Errorf("inference handler: %w", err)
	}
	if resp == nil || resp.Model != genReq.Model {
		return nil, fmt.Errorf("inference handler: %w", constants.ErrInferenceIdentityMismatch)
	}
	if !models.IsSHA256Hex(resp.NormalizedRequestHash) || !models.IsSHA256Hex(resp.OutputHash) {
		return nil, fmt.Errorf("inference handler: %w", constants.ErrInferenceEvidenceHashInvalid)
	}
	// The output is digest-bound (InferenceResult.result_digest, signed into the L5 receipt), so it is
	// scrubbed here, before ExecuteVerifiedTransaction computes that digest. OutputHash is recomputed over
	// the scrubbed parts so every hash the Gateway checks describes the text that leaves the Operator.
	// Streamed output was already scrubbed segment by segment at the source (stream_segmenter.go); ScrubText is
	// idempotent on its own output, so this pass leaves those parts unchanged and the progress hash still matches.
	scrubbedParts, err := scrubResponseParts(ctx, resp.Parts, h.scrubbing)
	if err != nil {
		return nil, fmt.Errorf("inference handler: scrub response: %w", err)
	}
	scrubbedOutputHash, err := models.ComputeInferenceOutputHash(scrubbedParts, resp.FinishReason)
	if err != nil {
		return nil, fmt.Errorf("inference handler: output hash: %w", err)
	}
	resp.Parts = scrubbedParts
	resp.OutputHash = scrubbedOutputHash
	if genReq.ModelDigest != "" && resp.ServedModelDigest != genReq.ModelDigest {
		return nil, fmt.Errorf("inference handler: %w", constants.ErrInferenceModelDigestMismatch)
	}
	resp.ProviderAttemptID = genReq.ProviderAttemptID
	resp.RequestedModel = genReq.Model
	resp.RequestedModelDigest = genReq.ModelDigest
	resp.CampaignID = genReq.CampaignID
	resp.RunID = genReq.RunID
	resp.AssignmentID = genReq.AssignmentID
	resp.EvaluationAttemptID = genReq.EvaluationAttemptID
	resp.ScenarioID = genReq.ScenarioID
	resp.ModelRegistryDigest = genReq.ModelRegistryDigest
	resp.RetryCount = infReq.RetryCount
	resp.RetryClassification = models.ClassifyRetry(infReq.RetryCount)
	resp.LoadState = models.ClassifyLoadState(resp.LoadDurationNS)

	return resp, nil
}

func (h *InferenceExecutionHandler) authorizeInferenceModel(req models.InferenceRequestPayload) (string, error) {
	if HasCampaignAuthority(req.CampaignID, req.ModelRegistryDigest, req.ModelRegistry) {
		return authorizeGovernedCampaignModel(req)
	}
	// The Operator is a worker and holds no model configuration: the model is
	// the user's choice, carried on the governed request. An absent model fails
	// closed; an unknown one is rejected by the provider.
	if req.Model == "" {
		return "", constants.ErrInferenceModelRefInvalid
	}
	return req.Model, nil
}

func authorizeGovernedCampaignModel(req models.InferenceRequestPayload) (string, error) {
	if req.CampaignID == "" || !models.IsSHA256Hex(req.ModelRegistryDigest) ||
		req.RunID == "" || req.AssignmentID == "" || req.EvaluationAttemptID == "" || req.ScenarioID == "" {
		return "", constants.ErrInferenceCampaignBindingInvalid
	}
	if req.Model == "" || !models.IsSHA256Hex(req.ModelDigest) || len(req.ModelRegistry) == 0 {
		return "", constants.ErrInferenceModelRegistryInvalid
	}
	if err := VerifyModelRegistryBinding(req.CampaignID, req.ModelRegistryDigest, req.Model, req.ModelDigest, req.ModelRegistry); err != nil {
		return "", err
	}
	return req.Model, nil
}

func (h *InferenceExecutionHandler) normalizeInferenceInput(ctx context.Context, req *models.InferenceRequestPayload) error {
	if req.ProviderAttemptID == "" {
		return constants.ErrInferenceProviderAttemptRequired
	}
	if req.ModelDigest != "" && !models.IsSHA256Hex(req.ModelDigest) {
		return constants.ErrInferenceEvidenceHashInvalid
	}
	if err := validateInferenceGenerationOptions(req); err != nil {
		return err
	}
	if len(req.Messages) == 0 {
		return constants.ErrInferenceMessagesRequired
	}
	// Input that carries content must be scrubbed before it reaches the provider. A disabled scrubber
	// is refused, never passed through. Content-free input has nothing to scrub, so the identity is safe.
	scrubText := func(value string) string { return value }
	if h.scrubbing.IsEnabled() {
		scrubText = func(value string) string { return h.scrubbing.ScrubText(ctx, value) }
	} else if inferenceMessagesHaveContent(req.Messages) {
		return fmt.Errorf("inference input: %w", constants.ErrEgressScrubberUnavailable)
	}
	if err := normalizeInferenceMessages(req.Messages, scrubText); err != nil {
		return err
	}
	return validateInferenceToolDefinitions(req.Tools)
}

func validateInferenceGenerationOptions(req *models.InferenceRequestPayload) error {
	if req.RequestSchemaVersion != constants.InferenceRequestSchemaVersion {
		return fmt.Errorf("%w: request schema version", constants.ErrInferenceGenerationOptionsInvalid)
	}
	if math.IsNaN(float64(req.Temperature)) || math.IsInf(float64(req.Temperature), 0) || req.Temperature < 0 || req.Temperature > 2 {
		return fmt.Errorf("%w: temperature", constants.ErrInferenceGenerationOptionsInvalid)
	}
	if req.MaxTokens < 0 || req.MaxTokens > constants.InferenceMaxOutputTokens {
		return fmt.Errorf("%w: max tokens", constants.ErrInferenceGenerationOptionsInvalid)
	}
	if req.TopP != nil && (math.IsNaN(float64(*req.TopP)) || math.IsInf(float64(*req.TopP), 0) || *req.TopP < 0 || *req.TopP > 1) {
		return fmt.Errorf("%w: top_p", constants.ErrInferenceGenerationOptionsInvalid)
	}
	if req.TopK != nil && (*req.TopK <= 0 || *req.TopK > maxInferenceTopK) {
		return fmt.Errorf("%w: top_k", constants.ErrInferenceGenerationOptionsInvalid)
	}
	if req.Seed != nil && *req.Seed < 0 {
		return fmt.Errorf("%w: seed", constants.ErrInferenceGenerationOptionsInvalid)
	}
	if len(req.StopSequences) > maxInferenceStopSequences {
		return fmt.Errorf("%w: stop sequences", constants.ErrInferenceGenerationOptionsInvalid)
	}
	for i, stop := range req.StopSequences {
		if stop == "" || len(stop) > maxInferenceStopSequenceBytes {
			return fmt.Errorf("%w: stop sequence %d", constants.ErrInferenceGenerationOptionsInvalid, i)
		}
	}
	if req.ResponseFormat != nil {
		if req.ResponseFormat.GetMediaType() != inferenceJSONMediaType {
			return fmt.Errorf("%w: response media type", constants.ErrInferenceCapabilityUnsupported)
		}
		if err := validateInferenceToolSchema(req.ResponseFormat.GetJsonSchema()); err != nil {
			return fmt.Errorf("%w: response schema: %w", constants.ErrInferenceGenerationOptionsInvalid, err)
		}
	}
	if err := validateInferenceToolChoice(req); err != nil {
		return err
	}
	if err := validateInferenceThinking(req.Thinking); err != nil {
		return err
	}
	if req.ContextLimit != nil && (*req.ContextLimit <= 0 || *req.ContextLimit > maxInferenceContextLimit) {
		return fmt.Errorf("%w: context limit", constants.ErrInferenceGenerationOptionsInvalid)
	}
	return validateKeepAlive(req.KeepAlive)
}

func validateInferenceToolChoice(req *models.InferenceRequestPayload) error {
	if req.ToolChoice == nil {
		return nil
	}
	if req.ToolChoice.GetMode() != operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO {
		return fmt.Errorf("%w: tool choice mode", constants.ErrInferenceCapabilityUnsupported)
	}
	declared := make(map[string]struct{}, len(req.Tools))
	for _, tool := range req.Tools {
		if tool != nil {
			declared[tool.GetName()] = struct{}{}
		}
	}
	allowed := make(map[string]struct{}, len(req.ToolChoice.GetAllowedToolNames()))
	for _, name := range req.ToolChoice.GetAllowedToolNames() {
		if name == "" {
			return fmt.Errorf("%w: empty allowed tool name", constants.ErrInferenceGenerationOptionsInvalid)
		}
		if _, exists := declared[name]; !exists {
			return fmt.Errorf("%w: undeclared allowed tool", constants.ErrInferenceGenerationOptionsInvalid)
		}
		if _, exists := allowed[name]; exists {
			return fmt.Errorf("%w: duplicate allowed tool", constants.ErrInferenceGenerationOptionsInvalid)
		}
		allowed[name] = struct{}{}
	}
	return nil
}

func validateInferenceThinking(thinking *operatorv1.InferenceThinkingControl) error {
	if thinking == nil {
		return nil
	}
	switch mode := thinking.GetMode().(type) {
	case *operatorv1.InferenceThinkingControl_Enabled:
		if !mode.Enabled && thinking.GetIncludeThoughts() {
			return fmt.Errorf("%w: disabled thinking includes thoughts", constants.ErrInferenceGenerationOptionsInvalid)
		}
	case *operatorv1.InferenceThinkingControl_Level:
		return fmt.Errorf("%w: thinking level %q", constants.ErrInferenceCapabilityUnsupported, mode.Level)
	default:
		return fmt.Errorf("%w: thinking mode", constants.ErrInferenceGenerationOptionsInvalid)
	}
	return nil
}

// inferenceMessagesHaveContent reports whether any message part carries a non-empty value that a
// scrubber would have to inspect.
func inferenceMessagesHaveContent(messages []*operatorv1.InferenceMessage) bool {
	for _, message := range messages {
		for _, part := range message.GetParts() {
			switch v := part.GetPart().(type) {
			case *operatorv1.InferenceMessagePart_Text:
				if v.Text != "" {
					return true
				}
			case *operatorv1.InferenceMessagePart_ToolCall:
				if v.ToolCall.GetName() != "" || v.ToolCall.GetArgumentsJson() != "" {
					return true
				}
			case *operatorv1.InferenceMessagePart_ToolResult:
				if v.ToolResult.GetName() != "" || v.ToolResult.GetResultJson() != "" {
					return true
				}
			}
		}
	}
	return false
}

// normalizeInferenceMessages validates each typed message and applies scrubText to every content leaf.
// Callers must pass an enabled scrubber's ScrubText whenever the input carries content.
func normalizeInferenceMessages(messages []*operatorv1.InferenceMessage, scrubText func(string) string) error {
	for messageIndex, message := range messages {
		if message == nil || !validInferenceMessageRole(message.GetRole()) || len(message.GetParts()) == 0 {
			return fmt.Errorf("%w: message %d", constants.ErrInferenceMessageInvalid, messageIndex)
		}
		for partIndex, part := range message.GetParts() {
			if part == nil || part.GetPart() == nil {
				return fmt.Errorf("%w: message %d part %d", constants.ErrInferenceMessageInvalid, messageIndex, partIndex)
			}
			if err := normalizeInferenceMessagePart(message, part, messageIndex, partIndex, scrubText); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeInferenceMessagePart(message *operatorv1.InferenceMessage, part *operatorv1.InferenceMessagePart, messageIndex, partIndex int, scrubText func(string) string) error {
	switch value := part.GetPart().(type) {
	case *operatorv1.InferenceMessagePart_Text:
		if message.GetRole() == operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL {
			return fmt.Errorf("%w: message %d text part", constants.ErrInferenceMessageInvalid, messageIndex)
		}
		value.Text = scrubText(value.Text)
	case *operatorv1.InferenceMessagePart_ToolCall:
		if message.GetRole() != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT || value.ToolCall == nil || value.ToolCall.GetName() == "" {
			return fmt.Errorf("%w: message %d tool call", constants.ErrInferenceMessageInvalid, messageIndex)
		}
		normalized, err := normalizeCanonicalJSON(value.ToolCall.GetArgumentsJson(), true, scrubText)
		if err != nil {
			return fmt.Errorf("message %d tool call arguments: %w", messageIndex, err)
		}
		value.ToolCall.ArgumentsJson = normalized
	case *operatorv1.InferenceMessagePart_ToolResult:
		if message.GetRole() != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL || value.ToolResult == nil || value.ToolResult.GetName() == "" {
			return fmt.Errorf("%w: message %d tool result", constants.ErrInferenceMessageInvalid, messageIndex)
		}
		normalized, err := normalizeCanonicalJSON(value.ToolResult.GetResultJson(), false, scrubText)
		if err != nil {
			return fmt.Errorf("message %d tool result: %w", messageIndex, err)
		}
		value.ToolResult.ResultJson = normalized
	default:
		return fmt.Errorf("%w: message %d part %d", constants.ErrInferenceMessageInvalid, messageIndex, partIndex)
	}
	return nil
}

func validateInferenceToolDefinitions(tools []*operatorv1.InferenceToolDeclaration) error {
	for toolIndex, tool := range tools {
		if tool == nil || tool.GetName() == "" {
			return fmt.Errorf("%w: tool %d", constants.ErrInferenceToolSchemaInvalid, toolIndex)
		}
		if err := validateInferenceToolSchema(tool.GetJsonSchema()); err != nil {
			return fmt.Errorf("%w: tool %d: %w", constants.ErrInferenceToolSchemaInvalid, toolIndex, err)
		}
	}
	return nil
}

func validateInferenceToolSchema(value string) error {
	if _, err := normalizeCanonicalJSON(value, true, nil); err != nil {
		return err
	}
	if _, err := jsonschema.NewCompiler().Compile([]byte(value)); err != nil {
		return err
	}
	return nil
}

func validInferenceMessageRole(role operatorv1.InferenceMessageRole) bool {
	switch role {
	case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM,
		operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
		operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT,
		operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL:
		return true
	default:
		return false
	}
}

func normalizeCanonicalJSON(value string, requireObject bool, scrubText func(string) string) (string, error) {
	canonical, err := canonicalizeJSON(value, requireObject, nil)
	if err != nil {
		return "", err
	}
	if canonical != value {
		return "", constants.ErrInferenceJSONNonCanonical
	}
	if scrubText == nil {
		return canonical, nil
	}
	return canonicalizeJSON(value, requireObject, scrubText)
}

func canonicalizeJSON(value string, requireObject bool, scrubText func(string) string) (string, error) {
	if value == "" {
		return "", constants.ErrInferenceJSONInvalid
	}
	acceptAll := true
	if failures := jsonschema.NewValidator().Validate(&jsonschema.Schema{Boolean: &acceptAll}, []byte(value)); len(failures) > 0 {
		return "", fmt.Errorf("%w: %s", constants.ErrInferenceJSONInvalid, failures.Error())
	}
	canonical, err := normalizeJSONValue(json.RawMessage(value), scrubText)
	if err != nil {
		return "", err
	}
	if requireObject && (len(canonical) == 0 || canonical[0] != '{') {
		return "", constants.ErrInferenceJSONInvalid
	}
	return string(canonical), nil
}

func normalizeJSONValue(raw json.RawMessage, scrubText func(string) string) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, constants.ErrInferenceJSONInvalid
	}
	switch trimmed[0] {
	case '{':
		values := map[string]json.RawMessage{}
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrInferenceJSONInvalid, err)
		}
		for key, value := range values {
			normalized, err := normalizeJSONValue(value, scrubText)
			if err != nil {
				return nil, err
			}
			values[key] = normalized
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, fmt.Errorf("normalize JSON object: %w", err)
		}
		return encoded, nil
	case '[':
		var values []json.RawMessage
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrInferenceJSONInvalid, err)
		}
		for i, value := range values {
			normalized, err := normalizeJSONValue(value, scrubText)
			if err != nil {
				return nil, err
			}
			values[i] = normalized
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, fmt.Errorf("normalize JSON array: %w", err)
		}
		return encoded, nil
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrInferenceJSONInvalid, err)
		}
		if scrubText != nil {
			value = scrubText(value)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("normalize JSON string: %w", err)
		}
		return encoded, nil
	default:
		if !json.Valid(trimmed) {
			return nil, constants.ErrInferenceJSONInvalid
		}
		return append(json.RawMessage(nil), trimmed...), nil
	}
}
