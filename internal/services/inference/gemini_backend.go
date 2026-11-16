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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	geminiComponent       = "gemini_backend"
	geminiAPIVersion      = "v1beta"
	geminiModelsPath      = "models"
	geminiModelNamePrefix = "models/"
	geminiStreamMethod    = ":streamGenerateContent"
	geminiGenerateMethod  = "generateContent"
	// geminiMaxStopSequences is the provider's limit on stopSequences.
	geminiMaxStopSequences = 5
	// geminiMaxModelIDBytes bounds a model ID before it enters a URL path.
	geminiMaxModelIDBytes = 128
	// geminiModelListPageSize and geminiMaxModelListPages bound one inventory read.
	geminiModelListPageSize = 1000
	geminiMaxModelListPages = 32
)

// Gemini finish reasons the backend maps to the inference finish vocabulary.
// Every other provider reason (safety, recitation, malformed function call,
// and the like) ends the generation without a normal stop and maps to "error".
const (
	geminiFinishStop      = "STOP"
	geminiFinishMaxTokens = "MAX_TOKENS"
)

// GeminiBackend is the hosted client for the Gemini API. It streams
// models/{model}:streamGenerateContent over server-sent events and sends the
// API key only in the x-goog-api-key header. Before every generation it reads
// the model's metadata and binds the provider's model version into the hosted
// model identity digest, so a campaign's frozen digest fails closed when the
// provider changes the model behind the name.
type GeminiBackend struct {
	providerHTTP
	origin string
	logger *slog.Logger
}

var _ HostedBackend = (*GeminiBackend)(nil)

// NewGeminiBackend builds a Gemini client against origin, the API origin the
// attestation records. key is the API key; it is never logged or placed in a URL.
func NewGeminiBackend(origin, key string, logger *slog.Logger) (*GeminiBackend, error) {
	if key == "" || logger == nil {
		return nil, fmt.Errorf("%s: %w", geminiComponent, constants.ErrMissingRequiredField)
	}
	headers := http.Header{}
	headers.Set(constants.HeaderGoogAPIKey, key)
	provider, err := newHostedProviderHTTP(geminiComponent, origin, headers)
	if err != nil {
		return nil, err
	}
	return &GeminiBackend{providerHTTP: provider, origin: provider.base.String(), logger: logger}, nil
}

// ProviderClass implements HostedBackend.
func (b *GeminiBackend) ProviderClass() string { return constants.InferenceProviderGemini }

// geminiModel is one entry of models.get or models.list.
type geminiModel struct {
	Name                       string   `json:"name"`
	Version                    string   `json:"version"`
	InputTokenLimit            int64    `json:"inputTokenLimit"`
	OutputTokenLimit           int64    `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	Thinking                   bool     `json:"thinking"`
}

type geminiModelList struct {
	Models        []geminiModel `json:"models"`
	NextPageToken string        `json:"nextPageToken"`
}

func (m geminiModel) id() string { return strings.TrimPrefix(m.Name, geminiModelNamePrefix) }

func (m geminiModel) generates() bool {
	for _, method := range m.SupportedGenerationMethods {
		if method == geminiGenerateMethod {
			return true
		}
	}
	return false
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

// geminiPart is one content part. The backend sends and accepts text, thought,
// function call, and function response parts; a response part of any other
// kind (inline data, file data, executable code) is rejected.
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`

	InlineData          json.RawMessage `json:"inlineData,omitempty"`
	FileData            json.RawMessage `json:"fileData,omitempty"`
	ExecutableCode      json.RawMessage `json:"executableCode,omitempty"`
	CodeExecutionResult json.RawMessage `json:"codeExecutionResult,omitempty"`
}

type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig geminiFunctionCallingConfig `json:"functionCallingConfig"`
}

type geminiFunctionCallingConfig struct {
	Mode string `json:"mode"`
}

type geminiThinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int32 `json:"thinkingBudget,omitempty"`
}

type geminiGenerationConfig struct {
	Temperature        *float32              `json:"temperature,omitempty"`
	TopP               *float32              `json:"topP,omitempty"`
	TopK               *int32                `json:"topK,omitempty"`
	Seed               *int32                `json:"seed,omitempty"`
	MaxOutputTokens    *int32                `json:"maxOutputTokens,omitempty"`
	StopSequences      []string              `json:"stopSequences,omitempty"`
	ResponseMimeType   string                `json:"responseMimeType,omitempty"`
	ResponseJSONSchema json.RawMessage       `json:"responseJsonSchema,omitempty"`
	ThinkingConfig     *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiGenerateRequest struct {
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	Contents          []geminiContent        `json:"contents"`
	Tools             []geminiTool           `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig      `json:"toolConfig,omitempty"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig"`
}

type geminiAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

type geminiCandidate struct {
	Content      *geminiContent `json:"content"`
	FinishReason string         `json:"finishReason"`
	Index        int            `json:"index"`
}

type geminiPromptFeedback struct {
	BlockReason string `json:"blockReason"`
}

type geminiUsage struct {
	PromptTokenCount        *int32 `json:"promptTokenCount"`
	CandidatesTokenCount    *int32 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      *int32 `json:"thoughtsTokenCount"`
	CachedContentTokenCount *int32 `json:"cachedContentTokenCount"`
}

// geminiStreamEvent is one server-sent event of streamGenerateContent.
type geminiStreamEvent struct {
	Candidates     []geminiCandidate     `json:"candidates"`
	PromptFeedback *geminiPromptFeedback `json:"promptFeedback"`
	UsageMetadata  *geminiUsage          `json:"usageMetadata"`
	ModelVersion   string                `json:"modelVersion"`
	ResponseID     string                `json:"responseId"`
	Error          *geminiAPIError       `json:"error"`
}

// geminiStreamResult is what the stream reports beyond its parts.
type geminiStreamResult struct {
	servedModel  string
	responseID   string
	finishReason string
	usage        *geminiUsage
}

// validateGeminiModelID admits a bare Gemini model ID such as gemini-3.6-flash.
func validateGeminiModelID(model string) error {
	if model == "" || len(model) > geminiMaxModelIDBytes {
		return constants.ErrInferenceModelRefInvalid
	}
	for i, character := range model {
		alphanumeric := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')
		if alphanumeric || (i > 0 && strings.ContainsRune("._-", character)) {
			continue
		}
		return constants.ErrInferenceModelRefInvalid
	}
	return nil
}

// readModel reads one model's metadata with models.get. An unknown model is
// ErrInferenceModelNotFound; the metadata must name the model and report a
// version and an input token limit.
func (b *GeminiBackend) readModel(ctx context.Context, model string) (geminiModel, error) {
	httpReq, err := b.newRequest(ctx, http.MethodGet, b.apiURL(geminiAPIVersion, geminiModelsPath, model), nil)
	if err != nil {
		return geminiModel{}, fmt.Errorf("%s: read model: build request: %w", geminiComponent, err)
	}
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return geminiModel{}, transportError(ctx, geminiComponent, "read model", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		if resp.StatusCode == http.StatusNotFound {
			return geminiModel{}, fmt.Errorf("%s: read model: %w: model %q", geminiComponent, constants.ErrInferenceModelNotFound, model)
		}
		return geminiModel{}, fmt.Errorf("%s: read model: %w: status %d", geminiComponent, constants.ErrInferenceBackendUnavailable, resp.StatusCode)
	}
	var metadata geminiModel
	if err := b.decodeResponse("read model", resp.Body, &metadata); err != nil {
		return geminiModel{}, err
	}
	if metadata.id() != model || metadata.Version == "" || metadata.InputTokenLimit <= 0 || metadata.InputTokenLimit > int64(^uint32(0)>>1) {
		return geminiModel{}, fmt.Errorf("%s: read model %q: %w", geminiComponent, model, constants.ErrInferenceProviderResponseInvalid)
	}
	if !metadata.generates() {
		return geminiModel{}, fmt.Errorf("%s: model %q does not generate content: %w", geminiComponent, model, constants.ErrInferenceCapabilityUnsupported)
	}
	return metadata, nil
}

// listModels reads every model the API key can see with models.list.
func (b *GeminiBackend) listModels(ctx context.Context, op string) ([]geminiModel, error) {
	var all []geminiModel
	pageToken := ""
	for page := 0; page < geminiMaxModelListPages; page++ {
		query := url.Values{}
		query.Set("pageSize", fmt.Sprint(geminiModelListPageSize))
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var list geminiModelList
		if err := b.getJSONQuery(ctx, op, &list, query, geminiAPIVersion, geminiModelsPath); err != nil {
			return nil, err
		}
		all = append(all, list.Models...)
		if list.NextPageToken == "" {
			return all, nil
		}
		pageToken = list.NextPageToken
	}
	return nil, fmt.Errorf("%s: %s: %w: model list exceeds %d pages", geminiComponent, op, constants.ErrInferenceProviderResponseInvalid, geminiMaxModelListPages)
}

// identityDigest is the hosted model identity digest of a model's metadata.
func (b *GeminiBackend) identityDigest(metadata geminiModel) (string, error) {
	return models.ComputeHostedModelIdentityDigest(constants.InferenceProviderGemini, b.origin, metadata.id(), metadata.Version)
}

// Status lists the generation models the API key can see.
func (b *GeminiBackend) Status(ctx context.Context) (*models.BackendStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, ProviderStatusTimeout)
	defer cancel()
	listed, err := b.listModels(ctx, "status")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(listed))
	for _, model := range listed {
		if model.generates() {
			names = append(names, model.id())
		}
	}
	return &models.BackendStatus{Available: true, Models: names}, nil
}

// ListProviderModelInventory reports every generation model the API key can
// see. Each entry's digest is its hosted model identity digest, so freezing it
// into a campaign registry pins the provider's model version.
func (b *GeminiBackend) ListProviderModelInventory(ctx context.Context) ([]ProviderModelInventoryEntry, error) {
	listed, err := b.listModels(ctx, "list inventory")
	if err != nil {
		return nil, err
	}
	entries := make([]ProviderModelInventoryEntry, 0, len(listed))
	for _, model := range listed {
		if !model.generates() || model.Version == "" || validateGeminiModelID(model.id()) != nil {
			continue
		}
		digest, err := b.identityDigest(model)
		if err != nil {
			return nil, fmt.Errorf("%s: list inventory: model %q: %w", geminiComponent, model.id(), err)
		}
		capabilities := []string{"completion"}
		if model.Thinking {
			capabilities = append(capabilities, "thinking")
		}
		family, _, _ := strings.Cut(model.id(), "-")
		entry := ProviderModelInventoryEntry{
			ProviderClass:          constants.InferenceProviderGemini,
			ServedModelTag:         model.id(),
			ModelDigest:            digest,
			ModelFamily:            family,
			Format:                 constants.HostedModelFormat,
			AdvertisedCapabilities: capabilities,
		}
		if model.InputTokenLimit > 0 && model.InputTokenLimit <= int64(^uint32(0)) {
			entry.ContextLimit = uint32(model.InputTokenLimit)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Generate streams one governed generation from the Gemini API.
func (b *GeminiBackend) Generate(ctx context.Context, req models.GenerateRequest) (*models.GenerateResponse, error) {
	if req.ProviderClass != constants.InferenceProviderGemini {
		return nil, fmt.Errorf("%s: generate: provider class %q: %w", geminiComponent, req.ProviderClass, constants.ErrInferenceHostedNotAllowed)
	}
	if err := validateGeminiModelID(req.Model); err != nil {
		return nil, fmt.Errorf("%s: generate: %w", geminiComponent, err)
	}
	if req.ContextLimit != nil {
		// The provider fixes the model's context window; a requested window
		// cannot be applied, so it is refused rather than ignored.
		return nil, fmt.Errorf("%s: generate: context limit: %w", geminiComponent, constants.ErrInferenceCapabilityUnsupported)
	}
	metadata, err := b.readModel(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	identityDigest, err := b.identityDigest(metadata)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: %w", geminiComponent, err)
	}
	if req.ModelDigest != "" && identityDigest != req.ModelDigest {
		return nil, fmt.Errorf("%s: generate: provider model version changed: %w", geminiComponent, constants.ErrInferenceModelDigestMismatch)
	}

	body, err := buildGeminiRequest(req, metadata)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: %w", geminiComponent, err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: marshal request: %w", geminiComponent, err)
	}
	normalizedRequestHash := models.SHA256Hex(encoded)

	var stream *streamSegmenter
	if req.Stream {
		if reporter := ProgressReporterFromContext(ctx); reporter != nil {
			stream, err = newStreamSegmenter(req.Scrubber, reporter, req.ProviderAttemptID)
			if err != nil {
				return nil, fmt.Errorf("%s: generate: %w", geminiComponent, err)
			}
		}
	}

	generationCtx, stopGeneration := context.WithCancel(ctx)
	defer stopGeneration()
	target := b.base.JoinPath(geminiAPIVersion, geminiModelsPath, req.Model+geminiStreamMethod)
	target.RawQuery = url.Values{"alt": []string{"sse"}}.Encode()
	httpReq, err := b.newRequest(generationCtx, http.MethodPost, target.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("%s: generate: build request: %w", geminiComponent, err)
	}
	httpReq.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)

	requestStartedAt := time.Now()
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, transportError(ctx, geminiComponent, "generate", err)
	}
	defer resp.Body.Close()

	capture := newProviderResponseCapture(b.maxResponseBytes, requestStartedAt)
	go capture.receive(resp.Body)
	var result geminiStreamResult
	var parts []*operatorv1.InferenceResponsePart
	var timeToFirstToken *int64
	var decodeErr error
	if resp.StatusCode == http.StatusOK {
		allowParallelToolCalls := req.ParallelToolCalls == nil || *req.ParallelToolCalls
		result, parts, timeToFirstToken, decodeErr = b.decodeStream(ctx, capture, capture.arrivalTime, allowParallelToolCalls, req.ProviderAttemptID, stream)
	}
	stoppedRepeating := decodeErr == nil && result.finishReason == constants.InferenceFinishReasonRepetition
	if stoppedRepeating {
		stopGeneration()
	}
	responseBody, readErr := capture.result()
	if stoppedRepeating && ctx.Err() == nil {
		readErr = nil
	}
	// Only a 200 stream is bound to the attempt record, and only by digest and
	// size. A non-200 body can echo prompt material and is never recorded.
	if store := attemptStoreFromContext(ctx); store != nil && resp.StatusCode == http.StatusOK {
		if err := store.RecordRawResponse(context.WithoutCancel(ctx), req.ProviderAttemptID, responseBody); err != nil {
			return nil, errors.Join(readErr, fmt.Errorf("%s: record raw response digest: %w", geminiComponent, err))
		}
	}
	if readErr != nil {
		return nil, transportError(ctx, geminiComponent, "generate", readErr)
	}
	if int64(len(responseBody)) > b.maxResponseBytes {
		return nil, fmt.Errorf("%s: generate: %w: response exceeds %d bytes", geminiComponent, constants.ErrInferenceProviderResponseInvalid, b.maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, b.statusError(resp.StatusCode, responseBody, req)
	}
	if decodeErr != nil {
		return nil, decodeErr
	}

	// The provider answers under a model name of its choosing. A campaign pins
	// the requested model by digest, so a different served model fails closed;
	// otherwise the served name is recorded in the attestation.
	if req.ModelDigest != "" && result.servedModel != req.Model {
		return nil, fmt.Errorf("%s: generate: provider served %q for %q: %w", geminiComponent, result.servedModel, req.Model, constants.ErrInferenceModelDigestMismatch)
	}
	usage, err := geminiUsageCounts(result.usage)
	if err != nil {
		return nil, err
	}
	outputHash, err := models.ComputeInferenceOutputHash(parts, result.finishReason)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: %w", geminiComponent, err)
	}
	timingSource := operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_UNSPECIFIED
	if timeToFirstToken != nil {
		timingSource = operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER
	}
	servedModelDigest := ""
	if req.ModelDigest != "" {
		servedModelDigest = identityDigest
	}
	return &models.GenerateResponse{
		HostedEgress: &operatorv1.InferenceHostedEgressAttestation{
			ProviderOrigin:        b.origin,
			ModelVersion:          result.servedModel,
			Provenance:            constants.HostedModelProvenance,
			NormalizedRequestHash: normalizedRequestHash,
			ExplicitConsent:       true,
			ProviderClass:         constants.InferenceProviderGemini,
			RequestedModel:        req.Model,
			ProviderModelVersion:  metadata.Version,
			ModelIdentityDigest:   identityDigest,
			ProviderResponseId:    result.responseID,
			RawResponseDigest:     models.SHA256Hex(responseBody),
			RawResponseSizeBytes:  uint64(len(responseBody)),
		},
		Parts:                 parts,
		PromptTokens:          usage.prompt,
		CompletionTokens:      usage.completion,
		TotalTokens:           usage.prompt + usage.completion,
		UsageReported:         usage.reported,
		ThinkingTokens:        usage.thinking,
		CacheTokens:           usage.cached,
		TimeToFirstTokenNS:    timeToFirstToken,
		TimingSource:          timingSource,
		FinishReason:          result.finishReason,
		ContextWindow:         int32(metadata.InputTokenLimit),
		Model:                 req.Model,
		ServedModelDigest:     servedModelDigest,
		NormalizedRequestHash: normalizedRequestHash,
		OutputHash:            outputHash,
	}, nil
}

type geminiTokenCounts struct {
	reported           bool
	prompt, completion int32
	thinking, cached   *int32
}

// geminiUsageCounts maps usageMetadata to inference usage. Completion tokens
// are candidate plus thought tokens, so total = prompt + completion holds as
// it does for a local provider's generated thinking; thought tokens are also
// reported as thinking tokens.
func geminiUsageCounts(usage *geminiUsage) (geminiTokenCounts, error) {
	if usage == nil || usage.PromptTokenCount == nil {
		return geminiTokenCounts{}, nil
	}
	invalid := fmt.Errorf("%s: generate: token usage: %w", geminiComponent, constants.ErrInferenceProviderResponseInvalid)
	value := func(count *int32) int32 {
		if count == nil {
			return 0
		}
		return *count
	}
	prompt, candidates, thoughts, cached := value(usage.PromptTokenCount), value(usage.CandidatesTokenCount), value(usage.ThoughtsTokenCount), usage.CachedContentTokenCount
	if prompt < 0 || candidates < 0 || thoughts < 0 || (cached != nil && *cached < 0) || int64(candidates)+int64(thoughts) > int64(^uint32(0)>>1) {
		return geminiTokenCounts{}, invalid
	}
	counts := geminiTokenCounts{reported: true, prompt: prompt, completion: candidates + thoughts, cached: cached}
	if usage.ThoughtsTokenCount != nil {
		counts.thinking = &thoughts
	}
	return counts, nil
}

// statusError classifies a non-200 generation response. The provider's
// message is logged, never returned or signed: it can echo prompt material.
func (b *GeminiBackend) statusError(status int, body []byte, req models.GenerateRequest) error {
	var envelope struct {
		Error geminiAPIError `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	b.logger.Warn("Gemini generation failed",
		"status", status,
		"provider_status", envelope.Error.Status,
		"model", req.Model,
		"provider_attempt_id", req.ProviderAttemptID,
		"error", envelope.Error.Message,
	)
	message := strings.ToLower(envelope.Error.Message)
	switch {
	case status == http.StatusNotFound:
		return fmt.Errorf("%s: generate: %w: model %q", geminiComponent, constants.ErrInferenceModelNotFound, req.Model)
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return fmt.Errorf("%s: generate: %w", geminiComponent, constants.ErrInferenceBackendTimeout)
	case status == http.StatusBadRequest && strings.Contains(message, "exceeds the maximum number of tokens"):
		return fmt.Errorf("%s: generate: %w", geminiComponent, constants.ErrInferenceContextOverflow)
	case status == http.StatusBadRequest && strings.Contains(message, "function calling is not enabled"):
		return fmt.Errorf("%s: generate: %w", geminiComponent, constants.ErrInferenceToolsUnsupported)
	case status == http.StatusBadRequest && strings.Contains(message, "thinking") && (strings.Contains(message, "not supported") || strings.Contains(message, "only works")):
		return fmt.Errorf("%s: generate: %w", geminiComponent, constants.ErrInferenceCapabilityUnsupported)
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusTooManyRequests, status >= http.StatusInternalServerError:
		return fmt.Errorf("%s: generate: %w: status %d", geminiComponent, constants.ErrInferenceBackendUnavailable, status)
	default:
		return fmt.Errorf("%s: generate: %w: status %d", geminiComponent, constants.ErrInferenceGenerateFailed, status)
	}
}

// buildGeminiRequest maps a governed generation onto a generateContent body.
func buildGeminiRequest(req models.GenerateRequest, metadata geminiModel) (geminiGenerateRequest, error) {
	system, contents, err := inferenceMessagesToGemini(req.Messages)
	if err != nil {
		return geminiGenerateRequest{}, fmt.Errorf("messages: %w", err)
	}
	body := geminiGenerateRequest{SystemInstruction: system, Contents: contents}
	declarations, err := inferenceToolsToGemini(req.Tools, req.ToolChoice)
	if err != nil {
		return geminiGenerateRequest{}, fmt.Errorf("tools: %w", err)
	}
	if len(declarations) > 0 {
		body.Tools = []geminiTool{{FunctionDeclarations: declarations}}
		body.ToolConfig = &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{Mode: "AUTO"}}
	}
	config := geminiGenerationConfig{
		TopP:          req.TopP,
		TopK:          req.TopK,
		Seed:          req.Seed,
		StopSequences: req.StopSequences,
	}
	// A zero temperature or output limit is unset, as on the local providers.
	if req.Temperature != 0 {
		temperature := req.Temperature
		config.Temperature = &temperature
	}
	if req.MaxTokens > 0 {
		maxTokens := req.MaxTokens
		config.MaxOutputTokens = &maxTokens
	}
	if len(req.StopSequences) > geminiMaxStopSequences {
		return geminiGenerateRequest{}, fmt.Errorf("stop sequences: %w", constants.ErrInferenceGenerationOptionsInvalid)
	}
	if req.ResponseFormat != nil {
		if req.ResponseFormat.GetMediaType() != inferenceJSONMediaType {
			return geminiGenerateRequest{}, fmt.Errorf("response format: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		if err := validateInferenceToolSchema(req.ResponseFormat.GetJsonSchema()); err != nil {
			return geminiGenerateRequest{}, fmt.Errorf("response format: %w", err)
		}
		config.ResponseMimeType = inferenceJSONMediaType
		config.ResponseJSONSchema = json.RawMessage(req.ResponseFormat.GetJsonSchema())
	}
	thinking, err := geminiThinking(req.Thinking, metadata.Thinking)
	if err != nil {
		return geminiGenerateRequest{}, err
	}
	config.ThinkingConfig = thinking
	body.GenerationConfig = config
	return body, nil
}

// geminiThinking maps the thinking control. A thinking model returns its
// thoughts unless thinking is disabled, as a local thinking model does.
func geminiThinking(control *operatorv1.InferenceThinkingControl, modelThinks bool) (*geminiThinkingConfig, error) {
	enabled := modelThinks
	if control != nil {
		mode, ok := control.GetMode().(*operatorv1.InferenceThinkingControl_Enabled)
		if !ok {
			return nil, fmt.Errorf("thinking: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		if mode.Enabled && !modelThinks {
			return nil, fmt.Errorf("thinking: %w", constants.ErrInferenceCapabilityUnsupported)
		}
		enabled = mode.Enabled
	}
	switch {
	case !modelThinks:
		return nil, nil
	case enabled:
		return &geminiThinkingConfig{IncludeThoughts: true}, nil
	default:
		var none int32
		return &geminiThinkingConfig{ThinkingBudget: &none}, nil
	}
}

// inferenceMessagesToGemini maps the typed conversation. System text becomes
// the system instruction; assistant turns are role "model"; consecutive tool
// results share one user turn, as the API expects for parallel calls.
func inferenceMessagesToGemini(messages []*operatorv1.InferenceMessage) (*geminiContent, []geminiContent, error) {
	var system *geminiContent
	contents := make([]geminiContent, 0, len(messages))
	toolTurnOpen := false
	for messageIndex, message := range messages {
		if message == nil || len(message.GetParts()) == 0 {
			return nil, nil, fmt.Errorf("%w: message %d", constants.ErrInferenceMessageInvalid, messageIndex)
		}
		role := message.GetRole()
		var parts []geminiPart
		for partIndex, part := range message.GetParts() {
			mapped, err := inferenceMessagePartToGemini(role, part)
			if err != nil {
				return nil, nil, fmt.Errorf("message %d part %d: %w", messageIndex, partIndex, err)
			}
			parts = append(parts, mapped)
		}
		switch role {
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM:
			if system == nil {
				system = &geminiContent{}
			}
			system.Parts = append(system.Parts, parts...)
			toolTurnOpen = false
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER:
			contents = append(contents, geminiContent{Role: "user", Parts: parts})
			toolTurnOpen = false
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT:
			contents = append(contents, geminiContent{Role: "model", Parts: parts})
			toolTurnOpen = false
		case operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL:
			if toolTurnOpen {
				last := &contents[len(contents)-1]
				last.Parts = append(last.Parts, parts...)
				continue
			}
			contents = append(contents, geminiContent{Role: "user", Parts: parts})
			toolTurnOpen = true
		default:
			return nil, nil, fmt.Errorf("%w: message %d role", constants.ErrInferenceMessageInvalid, messageIndex)
		}
	}
	return system, contents, nil
}

func inferenceMessagePartToGemini(role operatorv1.InferenceMessageRole, part *operatorv1.InferenceMessagePart) (geminiPart, error) {
	if part == nil || part.GetPart() == nil {
		return geminiPart{}, constants.ErrInferenceMessageInvalid
	}
	switch value := part.GetPart().(type) {
	case *operatorv1.InferenceMessagePart_Text:
		if role == operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL {
			return geminiPart{}, fmt.Errorf("%w: text part in tool message", constants.ErrInferenceMessageInvalid)
		}
		return geminiPart{Text: value.Text}, nil
	case *operatorv1.InferenceMessagePart_ToolCall:
		if role != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT || value.ToolCall == nil || value.ToolCall.GetName() == "" {
			return geminiPart{}, fmt.Errorf("%w: tool call", constants.ErrInferenceMessageInvalid)
		}
		arguments, err := canonicalizeJSON(value.ToolCall.GetArgumentsJson(), true, nil)
		if err != nil {
			return geminiPart{}, fmt.Errorf("%w: tool call arguments", constants.ErrInferenceMessageInvalid)
		}
		return geminiPart{
			FunctionCall: &geminiFunctionCall{
				ID:   value.ToolCall.GetCallId(),
				Name: value.ToolCall.GetName(),
				Args: json.RawMessage(arguments),
			},
			// The provider requires the signature it issued with a function
			// call back on that call; it is returned verbatim.
			ThoughtSignature: value.ToolCall.GetProviderSignature(),
		}, nil
	case *operatorv1.InferenceMessagePart_ToolResult:
		if role != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL || value.ToolResult == nil || value.ToolResult.GetName() == "" {
			return geminiPart{}, fmt.Errorf("%w: tool result", constants.ErrInferenceMessageInvalid)
		}
		resultJSON, err := canonicalizeJSON(value.ToolResult.GetResultJson(), false, nil)
		if err != nil {
			return geminiPart{}, fmt.Errorf("%w: tool result JSON", constants.ErrInferenceMessageInvalid)
		}
		// The API takes a function response as an object; the tool's result
		// is its "output" member.
		response, err := json.Marshal(struct {
			Output json.RawMessage `json:"output"`
		}{Output: json.RawMessage(resultJSON)})
		if err != nil {
			return geminiPart{}, fmt.Errorf("%w: tool result JSON", constants.ErrInferenceMessageInvalid)
		}
		return geminiPart{FunctionResponse: &geminiFunctionResponse{
			ID:       value.ToolResult.GetCallId(),
			Name:     value.ToolResult.GetName(),
			Response: response,
		}}, nil
	default:
		return geminiPart{}, constants.ErrInferenceMessageInvalid
	}
}

// inferenceToolsToGemini maps tool declarations. An allowed-name list narrows
// the declarations sent, because the API accepts allowed function names only
// with a forced calling mode, and the governed tool choice is AUTO.
func inferenceToolsToGemini(tools []*operatorv1.InferenceToolDeclaration, choice *operatorv1.InferenceToolChoice) ([]geminiFunctionDeclaration, error) {
	allowed := make(map[string]struct{})
	if choice != nil {
		if choice.GetMode() != operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO {
			return nil, constants.ErrInferenceCapabilityUnsupported
		}
		for _, name := range choice.GetAllowedToolNames() {
			allowed[name] = struct{}{}
		}
	}
	declarations := make([]geminiFunctionDeclaration, 0, len(tools))
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
		declarations = append(declarations, geminiFunctionDeclaration{
			Name:                 tool.GetName(),
			Description:          tool.GetDescription(),
			ParametersJSONSchema: json.RawMessage(tool.GetJsonSchema()),
		})
	}
	return declarations, nil
}

// geminiResponseParts maps one event's candidate parts.
func geminiResponseParts(content *geminiContent) ([]*operatorv1.InferenceResponsePart, error) {
	if content == nil {
		return nil, nil
	}
	parts := make([]*operatorv1.InferenceResponsePart, 0, len(content.Parts))
	for partIndex, part := range content.Parts {
		if len(part.InlineData) > 0 || len(part.FileData) > 0 || len(part.ExecutableCode) > 0 || len(part.CodeExecutionResult) > 0 || part.FunctionResponse != nil {
			return nil, fmt.Errorf("%w: part %d kind", constants.ErrInferenceProviderResponseInvalid, partIndex)
		}
		switch {
		case part.FunctionCall != nil:
			if part.FunctionCall.Name == "" || part.Text != "" || part.Thought {
				return nil, fmt.Errorf("%w: function call part %d", constants.ErrInferenceProviderResponseInvalid, partIndex)
			}
			args := string(part.FunctionCall.Args)
			if len(part.FunctionCall.Args) == 0 || string(part.FunctionCall.Args) == "null" {
				args = "{}"
			}
			arguments, err := canonicalizeJSON(args, true, nil)
			if err != nil {
				return nil, fmt.Errorf("%w: function call part %d arguments", constants.ErrInferenceProviderResponseInvalid, partIndex)
			}
			parts = append(parts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
				CallId:            part.FunctionCall.ID,
				Name:              part.FunctionCall.Name,
				ArgumentsJson:     arguments,
				ProviderSignature: part.ThoughtSignature,
			}}})
		case part.Text == "":
			// The final event carries an empty text part, sometimes only a signature.
		case part.Thought:
			parts = append(parts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: part.Text}})
		default:
			parts = append(parts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: part.Text}})
		}
	}
	return parts, nil
}

// geminiFinishReason maps a provider finish reason onto the inference vocabulary.
func geminiFinishReason(reason string) string {
	switch reason {
	case geminiFinishStop:
		return "stop"
	case geminiFinishMaxTokens:
		return "length"
	default:
		return "error"
	}
}

// decodeStream reads the server-sent events of a 200 response. Every event
// must name the same served model and response; the event carrying a finish
// reason ends generation, and only usage may follow it.
func (b *GeminiBackend) decodeStream(
	ctx context.Context,
	body io.Reader,
	arrivalTime func(int64) *int64,
	allowParallelToolCalls bool,
	providerAttemptID string,
	stream *streamSegmenter,
) (geminiStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
	fail := func(format string, args ...any) (geminiStreamResult, []*operatorv1.InferenceResponsePart, *int64, error) {
		return geminiStreamResult{}, nil, nil, fmt.Errorf("%s: generate: "+format, append([]any{geminiComponent}, args...)...)
	}
	reader := bufio.NewReaderSize(body, 64<<10)
	var offset int64
	var result geminiStreamResult
	parts := make([]*operatorv1.InferenceResponsePart, 0)
	var timeToFirstToken *int64
	toolCallCount := 0
	finished := false
	repetition := newRepetitionDetector()
	for result.finishReason != constants.InferenceFinishReasonRepetition {
		data, consumed, err := nextSSEData(reader)
		offset += consumed
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail("%w: %w", constants.ErrInferenceProviderResponseInvalid, err)
		}
		var event geminiStreamEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fail("%w: %w", constants.ErrInferenceProviderResponseInvalid, err)
		}
		if event.Error != nil {
			b.logger.Warn("Gemini ended the response stream with an error",
				"provider_attempt_id", providerAttemptID,
				"model", result.servedModel,
				"provider_status", event.Error.Status,
				"error", event.Error.Message,
			)
			return fail("%w", constants.ErrInferenceProviderStreamError)
		}
		if event.PromptFeedback != nil && event.PromptFeedback.BlockReason != "" {
			b.logger.Warn("Gemini blocked the prompt",
				"provider_attempt_id", providerAttemptID,
				"block_reason", event.PromptFeedback.BlockReason,
			)
			return fail("prompt blocked by provider: %w", constants.ErrInferenceGenerateFailed)
		}
		if event.ModelVersion != "" {
			if result.servedModel != "" && event.ModelVersion != result.servedModel {
				return fail("served model changed: %w", constants.ErrInferenceProviderResponseInvalid)
			}
			result.servedModel = event.ModelVersion
		}
		if event.ResponseID != "" {
			if result.responseID != "" && event.ResponseID != result.responseID {
				return fail("response id changed: %w", constants.ErrInferenceProviderResponseInvalid)
			}
			result.responseID = event.ResponseID
		}
		if event.UsageMetadata != nil {
			result.usage = event.UsageMetadata
		}
		if len(event.Candidates) > 1 || (len(event.Candidates) == 1 && event.Candidates[0].Index != 0) {
			return fail("candidates: %w", constants.ErrInferenceProviderResponseInvalid)
		}
		var eventParts []*operatorv1.InferenceResponsePart
		finishReason := ""
		if len(event.Candidates) == 1 {
			candidate := event.Candidates[0]
			eventParts, err = geminiResponseParts(candidate.Content)
			if err != nil {
				return fail("response parts: %w", err)
			}
			finishReason = candidate.FinishReason
		}
		if finished && len(eventParts) > 0 {
			return fail("parts after finish: %w", constants.ErrInferenceProviderResponseInvalid)
		}
		for _, part := range eventParts {
			if part.GetToolCall() != nil {
				toolCallCount++
			}
		}
		if !allowParallelToolCalls && toolCallCount > 1 {
			return fail("response parts: %w", constants.ErrInferenceProviderResponseInvalid)
		}
		if len(eventParts) > 0 && timeToFirstToken == nil {
			timeToFirstToken = arrivalTime(offset)
		}
		if finishReason != "" && !finished {
			finished = true
			result.finishReason = geminiFinishReason(finishReason)
		}
		if !finished && repetition.observe(eventParts) {
			finished = true
			result.finishReason = constants.InferenceFinishReasonRepetition
		}
		if stream != nil {
			released, err := stream.accept(ctx, eventParts)
			if err != nil {
				return fail("stream segments: %w", err)
			}
			eventParts = released
			if err := stream.publish(released, result.servedModel, timeToFirstToken); err != nil {
				b.logger.Warn("Inference progress delivery failed", "provider_attempt_id", providerAttemptID, "error", err)
			}
		}
		parts = append(parts, eventParts...)
	}
	if !finished {
		return fail("stream ended without a finish reason: %w", constants.ErrInferenceProviderResponseInvalid)
	}
	if result.servedModel == "" {
		return fail("stream carries no model version: %w", constants.ErrInferenceProviderResponseInvalid)
	}
	if stream != nil {
		tail := stream.finish(ctx)
		if err := stream.publish(tail, result.servedModel, timeToFirstToken); err != nil {
			b.logger.Warn("Inference progress delivery failed", "provider_attempt_id", providerAttemptID, "error", err)
		}
		parts = append(parts, tail...)
	}
	return result, parts, timeToFirstToken, nil
}

// nextSSEData returns the data of the next server-sent event and the bytes
// consumed reading it. Multiple data lines join with a newline; comments and
// other fields are skipped. It returns io.EOF once no event remains.
func nextSSEData(reader *bufio.Reader) ([]byte, int64, error) {
	var data []byte
	var consumed int64
	have := false
	for {
		line, err := reader.ReadBytes('\n')
		consumed += int64(len(line))
		trimmed := bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
		case len(trimmed) == 0:
			if have {
				return data, consumed, nil
			}
		case trimmed[0] == ':':
		default:
			field, value, _ := bytes.Cut(trimmed, []byte(":"))
			if string(field) == "data" {
				if have {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(value, []byte(" "))...)
				have = true
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && have {
				return data, consumed, nil
			}
			return nil, consumed, err
		}
	}
}
