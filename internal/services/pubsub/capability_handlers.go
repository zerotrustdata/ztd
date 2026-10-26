// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"fmt"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
)

// CapabilityHandlers holds the independently constructed handlers for every enabled Operator capability.
type CapabilityHandlers struct {
	Inference             *inference.InferenceExecutionHandler
	InferenceAttemptStore inference.AttemptStore
	// InferenceProvider is the provider the Inference handler calls, shared
	// with the Operator's provider maintenance commands. Nil when inference
	// is disabled.
	InferenceProvider        inference.Provider
	ProviderBoundaryObserver *provider_observer.Handler
	ModelProvenanceOperator  *model_provenance.Handler
}

// NewCapabilityHandlers builds every enabled capability for either embedded or outbound execution.
// A nil provider is built from cfg.Inference when inference is enabled.
func NewCapabilityHandlers(cfg *config.Config, logger *slog.Logger, fileSvc fs.RuntimeFileService, scrubbingService *scrubbing.ScrubbingService, results ResultsPublisher, provider inference.Provider) (*CapabilityHandlers, error) {
	if cfg == nil || logger == nil || fileSvc == nil || results == nil {
		return nil, fmt.Errorf("operator capability handlers: %w", constants.ErrMissingRequiredField)
	}
	if err := cfg.OperatorCapabilities.Validate(); err != nil {
		return nil, err
	}
	var err error
	if cfg.Inference.Enabled && provider == nil {
		provider, err = inference.NewProvider(cfg.Inference, logger)
		if err != nil {
			return nil, fmt.Errorf("operator capabilities: inference provider: %w", err)
		}
	}
	if !cfg.Inference.Enabled {
		provider = nil
	}
	var inferenceHandler *inference.InferenceExecutionHandler
	var inferenceAttemptStore inference.AttemptStore
	if cfg.Inference.Enabled {
		inferenceHandler, err = inference.NewInferenceExecutionHandler(provider, cfg, scrubbingService, logger)
		if err != nil {
			return nil, fmt.Errorf("operator capabilities: inference handler: %w", err)
		}
		inferenceAttemptStore, err = inference.NewAttemptStore(fileSvc)
		if err != nil {
			return nil, fmt.Errorf("operator capabilities: inference attempt store: %w", err)
		}
		logger.Info("Inference backend initialized",
			"provider", cfg.Inference.Provider,
			"endpoint", cfg.Inference.Endpoint())
	}

	var providerBoundaryObserver *provider_observer.Handler
	if cfg.ProviderBoundaryObserver.Enabled {
		tracker, err := provider_observer.NewTracker(provider_observer.TrackerConfig{
			ObserverID: cfg.ProviderBoundaryObserver.ObserverID,
			Collector:  provider_observer.DefaultCollector(),
		})
		if err != nil {
			return nil, fmt.Errorf("operator capabilities: provider boundary observer tracker: %w", err)
		}
		providerBoundaryObserver, err = provider_observer.NewHandler(tracker, results, logger)
		if err != nil {
			return nil, fmt.Errorf("operator capabilities: provider boundary observer handler: %w", err)
		}
		logger.Info("Provider-boundary observer enabled",
			"observer_id", cfg.ProviderBoundaryObserver.ObserverID)
	}

	var modelProvenanceOperator *model_provenance.Handler
	if cfg.ProvenanceOperator.Enabled {
		attestor, err := newModelProvenanceAttestor(cfg.ProvenanceOperator)
		if err != nil {
			return nil, fmt.Errorf("operator capabilities: model provenance attestor: %w", err)
		}
		tracker, err := model_provenance.NewTracker(model_provenance.TrackerConfig{
			OperatorID: cfg.ProvenanceOperator.OperatorID,
			Attestor:   attestor,
		})
		if err != nil {
			return nil, fmt.Errorf("operator capabilities: model provenance tracker: %w", err)
		}
		modelProvenanceOperator, err = model_provenance.NewHandler(tracker, results, logger)
		if err != nil {
			return nil, fmt.Errorf("operator capabilities: model provenance handler: %w", err)
		}
		logger.Info("Model provenance operator enabled",
			"operator_id", cfg.ProvenanceOperator.OperatorID,
			"model_storage_root", cfg.ProvenanceOperator.ModelStorageRoot,
			"model_storage_format", cfg.ProvenanceOperator.ModelStorageFormat)
	}

	return &CapabilityHandlers{Inference: inferenceHandler, InferenceAttemptStore: inferenceAttemptStore, InferenceProvider: provider, ProviderBoundaryObserver: providerBoundaryObserver, ModelProvenanceOperator: modelProvenanceOperator}, nil
}

// newModelProvenanceAttestor builds the storage attestor for the configured
// model store layout.
func newModelProvenanceAttestor(cfg config.ProvenanceOperatorConfig) (model_provenance.Attestor, error) {
	switch cfg.ModelStorageFormat {
	case constants.ModelStorageFormatOllama:
		return model_provenance.NewOllamaStorageAttestor(cfg.ModelStorageRoot, cfg.OperatorID)
	case constants.ModelStorageFormatLlamaCpp:
		return model_provenance.NewLlamaCppStorageAttestor(cfg.ModelStorageRoot, cfg.OperatorID)
	default:
		return nil, fmt.Errorf("model storage format %q: %w", cfg.ModelStorageFormat, constants.ErrProvenanceModelStorageFormatInvalid)
	}
}
