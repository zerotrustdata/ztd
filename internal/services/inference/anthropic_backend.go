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
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	anthropicComponent         = "anthropic_backend"
	anthropicAPIVersion        = "2026-10-21"
	anthropicModelListPageSize = 1000
	anthropicMaxModelListPages = 32
)

// AnthropicBackend serves the Messages API through the governed Inference
// Operator. Model identity binds the Models API's created_at, not weight hashes.
type AnthropicBackend struct {
	providerHTTP
	origin string
	logger *slog.Logger
}

var _ HostedBackend = (*AnthropicBackend)(nil)

// NewAnthropicBackend accepts only the class's fixed origin. Credentials travel
// in a header; redirects and client retries are disabled.
func NewAnthropicBackend(origin, key string, logger *slog.Logger) (*AnthropicBackend, error) {
	if logger == nil {
		return nil, fmt.Errorf("%s: %w", anthropicComponent, constants.ErrMissingRequiredField)
	}
	expected, _ := constants.HostedInferenceProviderOrigin(constants.InferenceProviderAnthropic)
	if origin != expected {
		return nil, fmt.Errorf("%s: hosted origin: %w", anthropicComponent, constants.ErrInferenceEndpointInvalid)
	}
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, fmt.Errorf("%s: %w", anthropicComponent, constants.ErrInferenceHostedProviderKeyMissing)
	}
	headers := http.Header{}
	headers.Set(constants.HeaderAnthropicAPIKey, strings.TrimSpace(key))
	headers.Set(constants.HeaderAnthropicVersion, anthropicAPIVersion)
	p, err := newHostedProviderHTTP(anthropicComponent, origin, headers)
	if err != nil {
		return nil, err
	}
	return &AnthropicBackend{providerHTTP: p, origin: p.base.String(), logger: logger}, nil
}

// ProviderClass implements HostedBackend.
func (b *AnthropicBackend) ProviderClass() string { return constants.InferenceProviderAnthropic }

type anthropicCapability struct {
	Supported bool `json:"supported"`
}

type anthropicThinkingCapabilities struct {
	Supported bool `json:"supported"`
	Types     struct {
		Adaptive anthropicCapability `json:"adaptive"`
		Disabled anthropicCapability `json:"disabled"`
	} `json:"types"`
}

type anthropicModel struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	CreatedAt      string `json:"created_at"`
	MaxInputTokens int64  `json:"max_input_tokens"`
	MaxTokens      int64  `json:"max_tokens"`
	Capabilities   struct {
		Thinking          anthropicThinkingCapabilities `json:"thinking"`
		StructuredOutputs anthropicCapability           `json:"structured_outputs"`
	} `json:"capabilities"`
}

func (m anthropicModel) validate() error {
	_, dateErr := time.Parse(time.RFC3339Nano, m.CreatedAt)
	if m.Type != "model" || validateHostedModelID(m.ID) != nil || dateErr != nil ||
		m.MaxInputTokens <= 0 || m.MaxInputTokens > math.MaxInt32 || m.MaxTokens <= 0 || m.MaxTokens > math.MaxInt32 {
		return fmt.Errorf("%s: model metadata: %w", anthropicComponent, constants.ErrInferenceProviderResponseInvalid)
	}
	return nil
}

func (b *AnthropicBackend) readModel(ctx context.Context, model string) (anthropicModel, error) {
	httpReq, err := b.newRequest(ctx, http.MethodGet, b.apiURL("v1", "models", model), nil)
	if err != nil {
		return anthropicModel{}, fmt.Errorf("%s: read model: build request: %w", anthropicComponent, err)
	}
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return anthropicModel{}, transportError(ctx, anthropicComponent, "read model", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		switch resp.StatusCode {
		case http.StatusNotFound:
			return anthropicModel{}, fmt.Errorf("%s: read model: %w: model %q", anthropicComponent, constants.ErrInferenceModelNotFound, model)
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return anthropicModel{}, fmt.Errorf("%s: read model: %w: status %d", anthropicComponent, constants.ErrInferenceBackendTimeout, resp.StatusCode)
		default:
			return anthropicModel{}, fmt.Errorf("%s: read model: %w: status %d", anthropicComponent, constants.ErrInferenceBackendUnavailable, resp.StatusCode)
		}
	}
	var metadata anthropicModel
	if err := b.decodeResponse("read model", resp.Body, &metadata); err != nil {
		return anthropicModel{}, err
	}
	if err := metadata.validate(); err != nil {
		return anthropicModel{}, err
	}
	// An alias resolved by Models API is not the requested identity.
	if metadata.ID != model {
		return anthropicModel{}, fmt.Errorf("%s: read model: identity changed: %w", anthropicComponent, constants.ErrInferenceModelDigestMismatch)
	}
	return metadata, nil
}

type anthropicModelList struct {
	Data    []anthropicModel `json:"data"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}

func (b *AnthropicBackend) listModels(ctx context.Context, op string) ([]anthropicModel, error) {
	var all []anthropicModel
	after := ""
	seen := make(map[string]struct{})
	for page := 0; page < anthropicMaxModelListPages; page++ {
		query := url.Values{"limit": {strconv.Itoa(anthropicModelListPageSize)}}
		if after != "" {
			query.Set("after_id", after)
		}
		var list anthropicModelList
		if err := b.getJSONQuery(ctx, op, &list, query, "v1", "models"); err != nil {
			return nil, err
		}
		for _, model := range list.Data {
			if err := model.validate(); err != nil {
				return nil, err
			}
			if _, duplicate := seen[model.ID]; duplicate {
				return nil, fmt.Errorf("%s: %s: duplicate model: %w", anthropicComponent, op, constants.ErrInferenceProviderResponseInvalid)
			}
			seen[model.ID] = struct{}{}
		}
		all = append(all, list.Data...)
		if !list.HasMore {
			return all, nil
		}
		if len(list.Data) == 0 || list.LastID != list.Data[len(list.Data)-1].ID {
			return nil, fmt.Errorf("%s: %s: pagination cursor: %w", anthropicComponent, op, constants.ErrInferenceProviderResponseInvalid)
		}
		after = list.LastID
	}
	return nil, fmt.Errorf("%s: %s: model list exceeds %d pages: %w", anthropicComponent, op, anthropicMaxModelListPages, constants.ErrInferenceProviderResponseInvalid)
}

func (b *AnthropicBackend) identityDigest(metadata anthropicModel) (string, error) {
	return models.ComputeHostedModelIdentityDigest(b.ProviderClass(), b.origin, metadata.ID, metadata.CreatedAt)
}

// Status reports the models visible to this API key.
func (b *AnthropicBackend) Status(ctx context.Context) (*models.BackendStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, ProviderStatusTimeout)
	defer cancel()
	listed, err := b.listModels(ctx, "status")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(listed))
	for _, model := range listed {
		names = append(names, model.ID)
	}
	return &models.BackendStatus{Available: true, Models: names}, nil
}

// ListProviderModelInventory exposes provider-reported identities and limits.
func (b *AnthropicBackend) ListProviderModelInventory(ctx context.Context) ([]ProviderModelInventoryEntry, error) {
	listed, err := b.listModels(ctx, "list inventory")
	if err != nil {
		return nil, err
	}
	entries := make([]ProviderModelInventoryEntry, 0, len(listed))
	for _, model := range listed {
		digest, err := b.identityDigest(model)
		if err != nil {
			return nil, fmt.Errorf("%s: list inventory: %w", anthropicComponent, err)
		}
		capabilities := []string{"completion"}
		if model.Capabilities.Thinking.Types.Adaptive.Supported {
			capabilities = append(capabilities, "thinking")
		}
		family, _, _ := strings.Cut(model.ID, "-")
		entries = append(entries, ProviderModelInventoryEntry{
			ProviderClass: b.ProviderClass(), ServedModelTag: model.ID, ModelDigest: digest,
			ModelFamily: family, Format: constants.HostedModelFormat,
			ContextLimit: uint32(model.MaxInputTokens), AdvertisedCapabilities: capabilities,
		})
	}
	return entries, nil
}

// Generate re-reads the identity before streaming one Messages API response.
func (b *AnthropicBackend) Generate(ctx context.Context, req models.GenerateRequest) (*models.GenerateResponse, error) {
	if req.ProviderClass != b.ProviderClass() {
		return nil, fmt.Errorf("%s: generate: %w", anthropicComponent, constants.ErrInferenceHostedNotAllowed)
	}
	if err := validateHostedModelID(req.Model); err != nil {
		return nil, fmt.Errorf("%s: generate: %w", anthropicComponent, err)
	}
	if err := validateAnthropicOptions(req); err != nil {
		return nil, err
	}
	metadata, err := b.readModel(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	digest, err := b.identityDigest(metadata)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: identity: %w", anthropicComponent, err)
	}
	if req.ModelDigest != "" && req.ModelDigest != digest {
		return nil, fmt.Errorf("%s: generate: model version changed: %w", anthropicComponent, constants.ErrInferenceModelDigestMismatch)
	}
	body, err := buildAnthropicRequest(req, metadata)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: %w", anthropicComponent, err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: marshal request: %w", anthropicComponent, err)
	}
	requestHash := models.SHA256Hex(encoded)
	var stream *streamSegmenter
	if req.Stream {
		if reporter := ProgressReporterFromContext(ctx); reporter != nil {
			stream, err = newStreamSegmenter(req.Scrubber, reporter, req.ProviderAttemptID)
			if err != nil {
				return nil, fmt.Errorf("%s: generate: %w", anthropicComponent, err)
			}
		}
	}
	generationCtx, stopGeneration := context.WithCancel(ctx)
	defer stopGeneration()
	httpReq, err := b.newRequest(generationCtx, http.MethodPost, b.apiURL("v1", "messages"), bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("%s: generate: build request: %w", anthropicComponent, err)
	}
	httpReq.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)
	startedAt := time.Now()
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, transportError(ctx, anthropicComponent, "generate", err)
	}
	defer resp.Body.Close()
	var result anthropicStreamResult
	var parts []*operatorv1.InferenceResponsePart
	var ttft *int64
	response, err := b.consumeHostedResponse(ctx, resp, req.ProviderAttemptID, startedAt, stopGeneration, func(capture *providerResponseCapture) (bool, error) {
		var decodeErr error
		parallel := req.ParallelToolCalls == nil || *req.ParallelToolCalls
		result, parts, ttft, decodeErr = b.decodeStream(ctx, capture, capture.arrivalTime, parallel, req.ProviderAttemptID, stream)
		return decodeErr == nil && result.finishReason == constants.InferenceFinishReasonRepetition, decodeErr
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, b.statusError(resp.StatusCode, response.body, req.ProviderAttemptID)
	}
	attestation, servedDigest, err := hostedEgressAttestation(anthropicComponent, b.origin, req, hostedResponseEvidence{
		servedModel: result.servedModel, providerModelVersion: metadata.CreatedAt, identityDigest: digest,
		responseID: result.responseID, normalizedRequestHash: requestHash, rawResponse: response.body,
	})
	if err != nil {
		return nil, err
	}
	usage, err := anthropicUsageCounts(result.usage)
	if err != nil {
		return nil, err
	}
	outputHash, err := models.ComputeInferenceOutputHash(parts, result.finishReason)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: %w", anthropicComponent, err)
	}
	timing := newHostedTiming(ttft, response.completeNS)
	return &models.GenerateResponse{
		HostedEgress: attestation, Parts: parts, Model: req.Model, ContextWindow: int32(metadata.MaxInputTokens),
		PromptTokens: usage.prompt, CompletionTokens: usage.completion, TotalTokens: usage.prompt + usage.completion,
		UsageReported: usage.reported, CacheTokens: usage.cached, ThinkingTokens: usage.thinking,
		GenerationDurationNS: timing.generation, TotalDurationNS: timing.total,
		TimeToFirstTokenNS: timing.firstToken, TimingSource: timing.source, FinishReason: result.finishReason,
		ServedModelDigest: servedDigest, NormalizedRequestHash: requestHash, OutputHash: outputHash,
	}, nil
}
