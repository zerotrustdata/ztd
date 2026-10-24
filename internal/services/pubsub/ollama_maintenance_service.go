// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// OllamaMaintenanceService owns typed governed Ollama provider inventory and
// residency queries on the Inference Operator.
type OllamaMaintenanceService struct {
	config      *config.Config
	logger      *slog.Logger
	client      PubSubClient
	eventsStore *storage.LocalFirstEventsStore
	scrubbing   *scrubbing.ScrubbingService
}

// NewOllamaMaintenanceService creates a new OllamaMaintenanceService. Every dependency is
// required. The scrubber is the egress choke point dependency: every result envelope this
// service publishes is scrubbed by it, so construction fails closed with
// constants.ErrEgressScrubberUnavailable when it is nil or disabled. eventsStore records
// observed-state content evidence; a nil store fails with constants.ErrLocalFirstEventsStoreRequired.
func NewOllamaMaintenanceService(
	cfg *config.Config,
	logger *slog.Logger,
	client PubSubClient,
	scrubbingSvc *scrubbing.ScrubbingService,
	eventsStore *storage.LocalFirstEventsStore,
) (*OllamaMaintenanceService, error) {
	if cfg == nil || logger == nil || client == nil {
		return nil, fmt.Errorf("ollama maintenance service: %w", constants.ErrMissingRequiredField)
	}
	if err := requireActiveScrubber("ollama maintenance service", scrubbingSvc); err != nil {
		return nil, err
	}
	if err := requireEventsStore("ollama maintenance service", eventsStore); err != nil {
		return nil, err
	}
	return &OllamaMaintenanceService{
		config:      cfg,
		logger:      logger,
		client:      client,
		eventsStore: eventsStore,
		scrubbing:   scrubbingSvc,
	}, nil
}

// HandleInventoryRequest lists installed provider models and returns a typed
// OllamaModelInventoryResult without routing through EXECUTE_BASH stdout.
func (s *OllamaMaintenanceService) HandleInventoryRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var request operatorv1.OllamaModelInventoryRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		s.logger.Error("Failed to decode Ollama inventory payload", string(constants.ConnectionStateError), err)
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelInventory.Failed, "invalid request payload", s.eventsStore, s.scrubbing))
	}

	executionID := request.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}

	backend, err := s.ollamaBackend()
	if err != nil {
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelInventory.Failed, err.Error(), s.eventsStore, s.scrubbing))
	}

	entries, err := backend.ListProviderModelInventory(ctx)
	if err != nil {
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelInventory.Failed, err.Error(), s.eventsStore, s.scrubbing))
	}

	payload := &operatorv1.OllamaModelInventoryResult{
		ExecutionId: executionID,
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		Entries:     inference.ProviderModelInventoryEntriesToProto(entries),
	}
	return "", publishLFEATypedResponseTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelInventory.Completed, payload, s.eventsStore, s.scrubbing)
}

// HandleResidencyRequest reads typed /api/ps residency and returns a typed
// OllamaModelResidencyResult without routing through EXECUTE_BASH stdout.
func (s *OllamaMaintenanceService) HandleResidencyRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var request operatorv1.OllamaModelResidencyRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		s.logger.Error("Failed to decode Ollama residency payload", string(constants.ConnectionStateError), err)
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelResidency.Failed, "invalid request payload", s.eventsStore, s.scrubbing))
	}

	executionID := request.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}

	backend, err := s.ollamaBackend()
	if err != nil {
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelResidency.Failed, err.Error(), s.eventsStore, s.scrubbing))
	}

	residency, err := backend.ReadResidency(ctx)
	if err != nil {
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelResidency.Failed, err.Error(), s.eventsStore, s.scrubbing))
	}

	payload := inference.ProviderResidencyToProto(residency)
	payload.ExecutionId = executionID
	payload.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	return "", publishLFEATypedResponseTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelResidency.Completed, payload, s.eventsStore, s.scrubbing)
}

// HandleReleaseRequest unloads the requested served model tags, verifies they
// have left provider residency, and returns a typed OllamaModelReleaseResult.
// The full tag set is validated before any unload, and an empty set fails closed.
func (s *OllamaMaintenanceService) HandleReleaseRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var request operatorv1.OllamaModelReleaseRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		s.logger.Error("Failed to decode Ollama release payload", string(constants.ConnectionStateError), err)
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelRelease.Failed, "invalid request payload", s.eventsStore, s.scrubbing))
	}

	executionID := request.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}

	if err := validateReleaseModelTags(request.GetModelTags()); err != nil {
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelRelease.Failed, err.Error(), s.eventsStore, s.scrubbing))
	}

	backend, err := s.ollamaBackend()
	if err != nil {
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelRelease.Failed, err.Error(), s.eventsStore, s.scrubbing))
	}

	if err := backend.ReleaseModels(ctx, request.GetModelTags()); err != nil {
		return "", errors.Join(err, publishLFEAErrorTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelRelease.Failed, err.Error(), s.eventsStore, s.scrubbing))
	}

	payload := &operatorv1.OllamaModelReleaseResult{
		ExecutionId: executionID,
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}
	return "", publishLFEATypedResponseTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelRelease.Completed, payload, s.eventsStore, s.scrubbing)
}

// validateReleaseModelTags fails closed on an empty release set and on any tag
// that the served-model-tag validator rejects.
func validateReleaseModelTags(modelTags []string) error {
	if len(modelTags) == 0 {
		return fmt.Errorf("ollama maintenance: release models: %w: at least one model tag is required", constants.ErrInferenceModelTagInvalid)
	}
	for _, modelTag := range modelTags {
		if err := operatorcapability.ValidateServedModelTag(modelTag); err != nil {
			return fmt.Errorf("ollama maintenance: release models: %w", err)
		}
	}
	return nil
}

func (s *OllamaMaintenanceService) ollamaBackend() (*inference.OllamaBackend, error) {
	return inference.NewOllamaBackend(s.config.Inference.OllamaEndpoint, s.logger)
}
