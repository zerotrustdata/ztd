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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	openaiComponent  = "openai_backend"
	openaiAPIVersion = "v1"
)

// OpenAIBackend is the hosted client for the OpenAI Responses API. It streams
// POST /v1/responses over server-sent events with store disabled and encrypted
// reasoning enabled, and sends the API key only in the Authorization Bearer
// header. Before every generation it reads the model's metadata and binds the
// provider's created timestamp into the hosted model identity digest, so a
// campaign's frozen digest fails closed when the provider changes or reroutes
// the model behind the name.
type OpenAIBackend struct {
	providerHTTP
	origin string
	logger *slog.Logger
}

var _ HostedBackend = (*OpenAIBackend)(nil)

// NewOpenAIBackend builds an OpenAI client only against the class's fixed API
// origin. Credentials travel only in the Authorization Bearer header; redirects
// and client retries are disabled.
func NewOpenAIBackend(origin, key string, logger *slog.Logger) (*OpenAIBackend, error) {
	if logger == nil {
		return nil, fmt.Errorf("%s: %w", openaiComponent, constants.ErrMissingRequiredField)
	}
	expected, _ := constants.HostedInferenceProviderOrigin(constants.InferenceProviderOpenAI)
	if origin != expected {
		return nil, fmt.Errorf("%s: hosted origin: %w", openaiComponent, constants.ErrInferenceEndpointInvalid)
	}
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, fmt.Errorf("%s: %w", openaiComponent, constants.ErrInferenceHostedProviderKeyMissing)
	}
	headers := http.Header{}
	headers.Set(constants.HeaderAuthorization, constants.BearerScheme+strings.TrimSpace(key))
	provider, err := newHostedProviderHTTP(openaiComponent, origin, headers)
	if err != nil {
		return nil, err
	}
	return &OpenAIBackend{providerHTTP: provider, origin: provider.base.String(), logger: logger}, nil
}

// ProviderClass implements HostedBackend.
func (b *OpenAIBackend) ProviderClass() string { return constants.InferenceProviderOpenAI }

// openaiModel is one entry of GET /v1/models or GET /v1/models/{model}.
type openaiModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (m openaiModel) validate() error {
	if m.Object != "model" || m.ID == "" || m.Created <= 0 {
		return fmt.Errorf("%s: model metadata: %w", openaiComponent, constants.ErrInferenceProviderResponseInvalid)
	}
	return nil
}

func (m openaiModel) version() string {
	return strconv.FormatInt(m.Created, 10)
}

type openaiModelList struct {
	Object string        `json:"object"`
	Data   []openaiModel `json:"data"`
}

// readModel reads one model's metadata via GET /v1/models/{model}. An unknown
// model fails with ErrInferenceModelNotFound; a returned ID differing from the
// requested model fails closed with ErrInferenceModelDigestMismatch.
func (b *OpenAIBackend) readModel(ctx context.Context, model string) (openaiModel, error) {
	httpReq, err := b.newRequest(ctx, http.MethodGet, b.apiURL(openaiAPIVersion, "models", model), nil)
	if err != nil {
		return openaiModel{}, fmt.Errorf("%s: read model: build request: %w", openaiComponent, err)
	}
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return openaiModel{}, transportError(ctx, openaiComponent, "read model", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return openaiModel{}, b.statusError(resp.StatusCode, body, model, "")
	}
	var metadata openaiModel
	if err := b.decodeResponse("read model", resp.Body, &metadata); err != nil {
		return openaiModel{}, err
	}
	if err := metadata.validate(); err != nil || validateHostedModelID(metadata.ID) != nil {
		return openaiModel{}, fmt.Errorf("%s: read model %q: %w", openaiComponent, model, constants.ErrInferenceProviderResponseInvalid)
	}
	// An alias resolved by Models API is not the requested identity.
	if metadata.ID != model {
		return openaiModel{}, fmt.Errorf("%s: read model: identity changed: %w", openaiComponent, constants.ErrInferenceModelDigestMismatch)
	}
	return metadata, nil
}

func (b *OpenAIBackend) listModels(ctx context.Context, op string) ([]openaiModel, error) {
	var list openaiModelList
	if err := b.getJSON(ctx, op, &list, openaiAPIVersion, "models"); err != nil {
		return nil, err
	}
	if list.Object != "list" {
		return nil, fmt.Errorf("%s: %s: %w", openaiComponent, op, constants.ErrInferenceProviderResponseInvalid)
	}
	seen := make(map[string]struct{}, len(list.Data))
	for _, model := range list.Data {
		if err := model.validate(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[model.ID]; duplicate {
			return nil, fmt.Errorf("%s: %s: duplicate model: %w", openaiComponent, op, constants.ErrInferenceProviderResponseInvalid)
		}
		seen[model.ID] = struct{}{}
	}
	return list.Data, nil
}

func (b *OpenAIBackend) identityDigest(metadata openaiModel) (string, error) {
	return models.ComputeHostedModelIdentityDigest(b.ProviderClass(), b.origin, metadata.ID, metadata.version())
}

// Status reports the models visible to this API key.
func (b *OpenAIBackend) Status(ctx context.Context) (*models.BackendStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, ProviderStatusTimeout)
	defer cancel()
	listed, err := b.listModels(ctx, "status")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(listed))
	for _, model := range listed {
		if validateHostedModelID(model.ID) == nil {
			names = append(names, model.ID)
		}
	}
	return &models.BackendStatus{Available: true, Models: names}, nil
}

// ListProviderModelInventory reports every model the API key can see whose ID
// passes hosted model ID validation. Each entry's digest is its hosted model
// identity digest. OpenAI /v1/models does not expose context limits, so
// ContextLimit is left unset (0).
func (b *OpenAIBackend) ListProviderModelInventory(ctx context.Context) ([]ProviderModelInventoryEntry, error) {
	listed, err := b.listModels(ctx, "list inventory")
	if err != nil {
		return nil, err
	}
	entries := make([]ProviderModelInventoryEntry, 0, len(listed))
	for _, model := range listed {
		if validateHostedModelID(model.ID) != nil {
			continue
		}
		digest, err := b.identityDigest(model)
		if err != nil {
			return nil, fmt.Errorf("%s: list inventory: %w", openaiComponent, err)
		}
		family, _, _ := strings.Cut(model.ID, "-")
		entries = append(entries, ProviderModelInventoryEntry{
			ProviderClass:          b.ProviderClass(),
			ServedModelTag:         model.ID,
			ModelDigest:            digest,
			ModelFamily:            family,
			Format:                 constants.HostedModelFormat,
			AdvertisedCapabilities: []string{"completion"},
		})
	}
	return entries, nil
}

// Generate re-reads the model identity before streaming one Responses API call.
func (b *OpenAIBackend) Generate(ctx context.Context, req models.GenerateRequest) (*models.GenerateResponse, error) {
	if req.ProviderClass != b.ProviderClass() {
		return nil, fmt.Errorf("%s: generate: provider class %q: %w", openaiComponent, req.ProviderClass, constants.ErrInferenceHostedNotAllowed)
	}
	if err := validateHostedModelID(req.Model); err != nil {
		return nil, fmt.Errorf("%s: generate: %w", openaiComponent, err)
	}
	if err := validateOpenAIOptions(req); err != nil {
		return nil, err
	}
	metadata, err := b.readModel(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	identityDigest, err := b.identityDigest(metadata)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: identity: %w", openaiComponent, err)
	}
	if req.ModelDigest != "" && req.ModelDigest != identityDigest {
		return nil, fmt.Errorf("%s: generate: provider model version changed: %w", openaiComponent, constants.ErrInferenceModelDigestMismatch)
	}

	body, err := buildOpenAIRequest(req)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: %w", openaiComponent, err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: marshal request: %w", openaiComponent, err)
	}
	normalizedRequestHash := models.SHA256Hex(encoded)

	var stream *streamSegmenter
	if req.Stream {
		if reporter := ProgressReporterFromContext(ctx); reporter != nil {
			stream, err = newStreamSegmenter(req.Scrubber, reporter, req.ProviderAttemptID)
			if err != nil {
				return nil, fmt.Errorf("%s: generate: %w", openaiComponent, err)
			}
		}
	}

	generationCtx, stopGeneration := context.WithCancel(ctx)
	defer stopGeneration()
	httpReq, err := b.newRequest(generationCtx, http.MethodPost, b.apiURL(openaiAPIVersion, "responses"), bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("%s: generate: build request: %w", openaiComponent, err)
	}
	httpReq.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)

	requestStartedAt := time.Now()
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, transportError(ctx, openaiComponent, "generate", err)
	}
	defer resp.Body.Close()

	var result openaiStreamResult
	var parts []*operatorv1.InferenceResponsePart
	var timeToFirstToken *int64
	response, err := b.consumeHostedResponse(ctx, resp, req.ProviderAttemptID, requestStartedAt, stopGeneration, func(capture *providerResponseCapture) (bool, error) {
		var decodeErr error
		allowParallelToolCalls := req.ParallelToolCalls == nil || *req.ParallelToolCalls
		result, parts, timeToFirstToken, decodeErr = b.decodeStream(ctx, capture, capture.arrivalTime, allowParallelToolCalls, req.ProviderAttemptID, stream)
		return decodeErr == nil && result.finishReason == constants.InferenceFinishReasonRepetition, decodeErr
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, b.statusError(resp.StatusCode, response.body, req.Model, req.ProviderAttemptID)
	}

	attestation, servedModelDigest, err := hostedEgressAttestation(openaiComponent, b.origin, req, hostedResponseEvidence{
		servedModel:           result.servedModel,
		providerModelVersion:  metadata.version(),
		identityDigest:        identityDigest,
		responseID:            result.responseID,
		normalizedRequestHash: normalizedRequestHash,
		rawResponse:           response.body,
	})
	if err != nil {
		return nil, err
	}
	usage, err := openaiUsageCounts(result.usage)
	if err != nil {
		return nil, err
	}
	outputHash, err := models.ComputeInferenceOutputHash(parts, result.finishReason)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: %w", openaiComponent, err)
	}
	timing := newHostedTiming(timeToFirstToken, response.completeNS)
	return &models.GenerateResponse{
		HostedEgress:          attestation,
		Parts:                 parts,
		PromptTokens:          usage.prompt,
		CompletionTokens:      usage.completion,
		TotalTokens:           usage.total,
		UsageReported:         usage.reported,
		ThinkingTokens:        usage.thinking,
		CacheTokens:           usage.cached,
		GenerationDurationNS:  timing.generation,
		TotalDurationNS:       timing.total,
		TimeToFirstTokenNS:    timing.firstToken,
		TimingSource:          timing.source,
		FinishReason:          result.finishReason,
		Model:                 req.Model,
		ServedModelDigest:     servedModelDigest,
		NormalizedRequestHash: normalizedRequestHash,
		OutputHash:            outputHash,
	}, nil
}
