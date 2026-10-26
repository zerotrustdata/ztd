// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package inference owns the backend abstraction, the local provider HTTP
// clients (Ollama and a llama.cpp llama-server router), and the governed
// execution handler for local LLM inference (ztdllama). The provider owns
// process lifecycle, VRAM management, model eviction, and multi-model
// residency; the Go backend uses the provider's HTTP API for scored chat and
// explicit campaign-boundary model release. Role routing remains a
// config-and-payload concern.
package inference

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// Backend is the inference backend abstraction. Implementations send
// generation requests to a local inference daemon (Ollama or llama-server)
// and return the generated text, usage metadata, and finish reason. The
// interface is intentionally minimal: the provider owns model loading,
// residency, and eviction. Campaign-boundary release is a Provider
// operation and is not part of the scored Backend contract.
type Backend interface {
	// Generate sends a generation request to the backend and returns the
	// generated text, usage metadata, and finish reason. The request
	// carries the served model name (which encodes the role), the scrubbed
	// prompt, and generation parameters. Uses context.Context for
	// cancellation and timeout.
	Generate(ctx context.Context, req models.GenerateRequest) (*models.GenerateResponse, error)

	// Status queries the backend's readiness and lists the models available
	// in its store. Used by the readiness check at operator startup.
	Status(ctx context.Context) (*models.BackendStatus, error)
}

// DecisionBackend serves bounded System One decisions without generating text.
type DecisionBackend interface {
	Decide(ctx context.Context, req models.DecisionRequest) (*models.DecisionResponse, error)
}

// Provider is the configured local inference provider of an Inference
// Operator: scored generation and decisions plus the model inventory,
// residency, and release operations the Operator's maintenance commands use.
type Provider interface {
	Backend
	DecisionBackend
	ListProviderModelInventory(ctx context.Context) ([]ProviderModelInventoryEntry, error)
	ReadResidency(ctx context.Context) (ProviderResidency, error)
	ReleaseModels(ctx context.Context, models []string) error
}

// NewProvider constructs the provider selected by cfg.Provider. One Provider
// is built per Operator and shared, so per-provider caches such as llama.cpp
// model digests outlive a single request.
func NewProvider(cfg config.InferenceConfig, logger *slog.Logger) (Provider, error) {
	switch cfg.Provider {
	case constants.InferenceProviderOllama:
		return NewOllamaBackend(cfg.OllamaEndpoint, logger)
	case constants.InferenceProviderLlamaCpp:
		return NewLlamaCppBackend(cfg.LlamaCppEndpoint, cfg.LlamaCppModelRoot, logger)
	default:
		return nil, fmt.Errorf("inference: provider %q: %w", cfg.Provider, constants.ErrInferenceProviderInvalid)
	}
}
