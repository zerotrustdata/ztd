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
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const providerClassOllama = "ollama"

// ProviderModelInventoryEntry is one discovered provider model identity with
// normalized metadata used to materialize eval ModelVariant records.
type ProviderModelInventoryEntry struct {
	ProviderClass          string                          `json:"provider_class,omitempty"`
	ServedModelTag         string                          `json:"served_model_tag,omitempty"`
	ModelDigest            string                          `json:"model_digest,omitempty"`
	ModelFamily            string                          `json:"model_family,omitempty"`
	ParameterSize          string                          `json:"parameter_size,omitempty"`
	ParameterCount         uint64                          `json:"parameter_count,omitempty"`
	ParameterCountSource   operatorv1.ParameterCountSource `json:"parameter_count_source,omitempty"`
	Quantization           string                          `json:"quantization,omitempty"`
	Format                 string                          `json:"format,omitempty"`
	ArtifactSizeBytes      uint64                          `json:"artifact_size_bytes,omitempty"`
	ContextLimit           uint32                          `json:"context_limit,omitempty"`
	AdvertisedCapabilities []string                        `json:"advertised_capabilities,omitempty"`
}

type ollamaShowRequest struct {
	Name string `json:"name"`
}

type ollamaShowResponse struct {
	Parameters   string                     `json:"parameters"`
	Details      ollamaModelDetails         `json:"details"`
	Capabilities []string                   `json:"capabilities"`
	ModelInfo    map[string]json.RawMessage `json:"model_info"`
}

type ollamaModelDetails struct {
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

var ollamaNumCtxPattern = regexp.MustCompile(`(?m)^num_ctx\s+(\d+)\s*$`)

// ListProviderModelInventory queries every installed Ollama model tag and
// preserves each served tag as a separate inventory entry even when digests match.
func (b *OllamaBackend) ListProviderModelInventory(ctx context.Context) ([]ProviderModelInventoryEntry, error) {
	tags, err := b.listTags(ctx, "list provider model inventory")
	if err != nil {
		return nil, fmt.Errorf("inference: list provider model inventory: %w", err)
	}
	entries := make([]ProviderModelInventoryEntry, 0, len(tags))
	for _, tag := range tags {
		if tag.Name == "" {
			continue
		}
		digest, err := models.NormalizeProviderModelDigest(tag.Digest)
		if err != nil {
			return nil, fmt.Errorf("inference: list provider model inventory: model %q digest: %w", tag.Name, err)
		}
		entry, err := b.describeProviderModel(ctx, tag.Name, digest)
		if err != nil {
			return nil, fmt.Errorf("inference: list provider model inventory: model %q: %w", tag.Name, err)
		}
		entry.ArtifactSizeBytes = tag.Size
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("inference: list provider model inventory: %w", constants.ErrInferenceModelNotFound)
	}
	return entries, nil
}

func (b *OllamaBackend) describeProviderModel(ctx context.Context, servedTag, digest string) (ProviderModelInventoryEntry, error) {
	show, err := b.showModel(ctx, servedTag)
	if err != nil {
		return ProviderModelInventoryEntry{}, err
	}
	family := strings.TrimSpace(show.Details.Family)
	if family == "" && len(show.Details.Families) > 0 {
		family = strings.TrimSpace(show.Details.Families[0])
	}
	parameterSize := strings.TrimSpace(show.Details.ParameterSize)
	parameterCount, err := parseProviderParameterCount(parameterSize, show.ModelInfo)
	if err != nil {
		return ProviderModelInventoryEntry{}, fmt.Errorf("parse parameter size %q: %w", parameterSize, err)
	}
	contextLimit := parseProviderContextLimit(show.Parameters, show.ModelInfo)
	countSource := operatorv1.ParameterCountSource_PARAMETER_COUNT_SOURCE_UNSPECIFIED
	if parameterCount > 0 {
		countSource = operatorv1.ParameterCountSource_PARAMETER_COUNT_SOURCE_ROUNDED_LABEL
		if _, ok := show.ModelInfo["general.parameter_count"]; ok {
			countSource = operatorv1.ParameterCountSource_PARAMETER_COUNT_SOURCE_MODEL_METADATA
		}
	}
	capabilities := append([]string(nil), show.Capabilities...)
	return ProviderModelInventoryEntry{
		ProviderClass:          providerClassOllama,
		ServedModelTag:         servedTag,
		ModelDigest:            digest,
		ModelFamily:            family,
		ParameterSize:          parameterSize,
		ParameterCount:         parameterCount,
		ParameterCountSource:   countSource,
		Quantization:           strings.TrimSpace(show.Details.QuantizationLevel),
		Format:                 strings.TrimSpace(show.Details.Format),
		ContextLimit:           contextLimit,
		AdvertisedCapabilities: capabilities,
	}, nil
}

func (b *OllamaBackend) showModel(ctx context.Context, model string) (*ollamaShowResponse, error) {
	body, err := json.Marshal(ollamaShowRequest{Name: model})
	if err != nil {
		return nil, fmt.Errorf("ollama_backend: show model: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL("api", "show"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama_backend: show model: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, transportError(ctx, "show model", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("ollama_backend: show model: %w: status %d", constants.ErrInferenceBackendUnavailable, resp.StatusCode)
	}
	var show ollamaShowResponse
	if err := b.decodeResponse("show model", resp.Body, &show); err != nil {
		return nil, err
	}
	return &show, nil
}

func parseProviderParameterCount(raw string, modelInfo map[string]json.RawMessage) (uint64, error) {
	// Use the integer metadata when present. The human-readable parameter_size
	// label is rounded, and cannot describe an exact deployed parameter count.
	if encoded, ok := modelInfo["general.parameter_count"]; ok {
		var count uint64
		if err := json.Unmarshal(encoded, &count); err != nil {
			return 0, fmt.Errorf("%w: decode general.parameter_count: %w", constants.ErrInferenceProviderResponseInvalid, err)
		}
		if count == 0 {
			return 0, fmt.Errorf("%w: general.parameter_count must be positive", constants.ErrInferenceProviderResponseInvalid)
		}
		return count, nil
	}
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return 0, nil
	}
	var multiplier uint64
	switch {
	case strings.HasSuffix(value, "b"):
		multiplier = 1_000_000_000
		value = strings.TrimSuffix(value, "b")
	case strings.HasSuffix(value, "m"):
		multiplier = 1_000_000
		value = strings.TrimSuffix(value, "m")
	case strings.HasSuffix(value, "k"):
		multiplier = 1_000
		value = strings.TrimSuffix(value, "k")
	default:
		return 0, fmt.Errorf("%w: unsupported parameter size suffix", constants.ErrInferenceProviderResponseInvalid)
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed*float64(multiplier) >= math.Exp2(64) {
		return 0, fmt.Errorf("%w: parameter size must be finite and within uint64 range", constants.ErrInferenceProviderResponseInvalid)
	}
	return uint64(parsed * float64(multiplier)), nil
}

func parseProviderContextLimit(parameters string, modelInfo map[string]json.RawMessage) uint32 {
	if match := ollamaNumCtxPattern.FindStringSubmatch(parameters); len(match) == 2 {
		if parsed, err := strconv.ParseUint(match[1], 10, 32); err == nil {
			return uint32(parsed)
		}
	}
	// Ollama prefixes model_info keys with the model architecture, for example
	// "gemma3.context_length". Keys are visited in sorted order so the result
	// is deterministic.
	keys := make([]string, 0, len(modelInfo))
	for key := range modelInfo {
		if strings.HasSuffix(key, ".context_length") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		var parsed uint64
		if err := json.Unmarshal(modelInfo[key], &parsed); err == nil && parsed > 0 && parsed <= uint64(^uint32(0)) {
			return uint32(parsed)
		}
	}
	return 0
}

// NormalizeProviderModelVariantID derives a stable variant ID from the served tag.
func NormalizeProviderModelVariantID(servedTag string) string {
	slug := strings.ToLower(servedTag)
	slug = strings.NewReplacer("/", "-", ":", "-", ".", "-", "_", "-").Replace(slug)
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	return strings.Trim(slug, "-")
}
