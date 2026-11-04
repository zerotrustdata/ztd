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
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// OllamaMaintenanceService owns typed governed provider inventory, residency,
// release, pull, and copy commands on the Inference Operator, served by the
// Operator's configured provider (Ollama or a llama.cpp router; pull and copy
// fail closed under llama.cpp).
type OllamaMaintenanceService struct {
	config      *config.Config
	logger      *slog.Logger
	client      PubSubClient
	eventsStore *storage.LocalFirstEventsStore
	scrubbing   *scrubbing.ScrubbingService
	provider    inference.Provider
}

// NewOllamaMaintenanceService creates a new OllamaMaintenanceService. Every dependency
// except provider is required. The scrubber is the egress choke point dependency: every
// result envelope this service publishes is scrubbed by it, so construction fails closed
// with constants.ErrEgressScrubberUnavailable when it is nil or disabled. eventsStore records
// observed-state content evidence; a nil store fails with constants.ErrLocalFirstEventsStoreRequired.
// provider is nil on an Operator without inference, where every command fails closed.
func NewOllamaMaintenanceService(
	cfg *config.Config,
	logger *slog.Logger,
	client PubSubClient,
	scrubbingSvc *scrubbing.ScrubbingService,
	eventsStore *storage.LocalFirstEventsStore,
	provider inference.Provider,
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
		provider:    provider,
	}, nil
}

// HandleInventoryRequest lists installed provider models and returns a typed
// OllamaModelInventoryResult without routing through EXECUTE_BASH stdout.
func (s *OllamaMaintenanceService) HandleInventoryRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	fail := func(executionID string, err error) (string, error) {
		return s.failModelOperation(ctx, msg, "list inventory", executionID, constants.Event.Operator.OllamaModelInventory.Failed, err)
	}

	var request operatorv1.OllamaModelInventoryRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		return fail(executionIDFromMessage(msg), fmt.Errorf("decode request payload: %w", err))
	}

	executionID := request.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}

	backend, err := s.inferenceProvider()
	if err != nil {
		return fail(executionID, err)
	}

	entries, err := backend.ListProviderModelInventory(ctx)
	if err != nil {
		return fail(executionID, err)
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
	fail := func(executionID string, err error) (string, error) {
		return s.failModelOperation(ctx, msg, "read residency", executionID, constants.Event.Operator.OllamaModelResidency.Failed, err)
	}

	var request operatorv1.OllamaModelResidencyRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		return fail(executionIDFromMessage(msg), fmt.Errorf("decode request payload: %w", err))
	}

	executionID := request.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}

	backend, err := s.inferenceProvider()
	if err != nil {
		return fail(executionID, err)
	}

	residency, err := backend.ReadResidency(ctx)
	if err != nil {
		return fail(executionID, err)
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
	fail := func(executionID string, err error) (string, error) {
		return s.failModelOperation(ctx, msg, "release models", executionID, constants.Event.Operator.OllamaModelRelease.Failed, err)
	}

	var request operatorv1.OllamaModelReleaseRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		return fail(executionIDFromMessage(msg), fmt.Errorf("decode request payload: %w", err))
	}

	executionID := request.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}

	if err := validateReleaseModelTags(request.GetModelTags()); err != nil {
		return fail(executionID, err)
	}

	backend, err := s.inferenceProvider()
	if err != nil {
		return fail(executionID, err)
	}

	if err := backend.ReleaseModels(ctx, request.GetModelTags()); err != nil {
		return fail(executionID, err)
	}

	payload := &operatorv1.OllamaModelReleaseResult{
		ExecutionId: executionID,
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}
	return "", publishLFEATypedResponseTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelRelease.Completed, payload, s.eventsStore, s.scrubbing)
}

// HandlePullRequest downloads one model onto the provider and returns a typed
// OllamaModelPullResult. A failed pull returns its error, so L5 signs a FAILED
// receipt. The request's timeout_seconds bounds the whole download.
func (s *OllamaMaintenanceService) HandlePullRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	fail := func(executionID string, err error) (string, error) {
		return s.failModelOperation(ctx, msg, "pull model", executionID, constants.Event.Operator.OllamaModelPull.Failed, err)
	}

	var request operatorv1.OllamaModelPullRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		return fail(executionIDFromMessage(msg), fmt.Errorf("decode request payload: %w", err))
	}
	executionID := request.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}

	backend, err := s.inferenceProvider()
	if err != nil {
		return fail(executionID, err)
	}

	timeout := constants.ModelPullDefaultTimeout
	if seconds := request.GetTimeoutSeconds(); seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	pullCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	if err := backend.PullModel(pullCtx, request.GetModelTag()); err != nil {
		return fail(executionID, err)
	}
	s.logger.Info("Ollama model pull completed", "execution_id", executionID, "elapsed", time.Since(start).Round(time.Second).String())

	return "", publishLFEATypedResponseTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelPull.Completed, &operatorv1.OllamaModelPullResult{
		ExecutionId: executionID,
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}, s.eventsStore, s.scrubbing)
}

// HandleCopyRequest aliases an installed model on the provider and returns a
// typed OllamaModelCopyResult. A failed copy returns its error, so L5 signs a
// FAILED receipt.
func (s *OllamaMaintenanceService) HandleCopyRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	fail := func(executionID string, err error) (string, error) {
		return s.failModelOperation(ctx, msg, "copy model", executionID, constants.Event.Operator.OllamaModelCopy.Failed, err)
	}

	var request operatorv1.OllamaModelCopyRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		return fail(executionIDFromMessage(msg), fmt.Errorf("decode request payload: %w", err))
	}
	executionID := request.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}

	backend, err := s.inferenceProvider()
	if err != nil {
		return fail(executionID, err)
	}
	if err := backend.CopyModel(ctx, request.GetSource(), request.GetDestination()); err != nil {
		return fail(executionID, err)
	}

	return "", publishLFEATypedResponseTo(ctx, s.client, s.config, s.logger, msg, constants.Event.Operator.OllamaModelCopy.Completed, &operatorv1.OllamaModelCopyResult{
		ExecutionId: executionID,
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}, s.eventsStore, s.scrubbing)
}

// failModelOperation publishes a failed operation as its action's typed result
// and returns the error, so L5 signs a FAILED receipt. The log names the failure
// by its classification only. Provider error text can carry model tags
// (INV-STOR-LOG-01); it reaches the result through the egress scrubber.
func (s *OllamaMaintenanceService) failModelOperation(
	ctx context.Context,
	msg *PubSubCommandMessage,
	operation string,
	executionID string,
	failedEvent constants.EventType,
	cause error,
) (string, error) {
	kind := logging.ErrorKindValue(cause)
	s.logger.Warn("Ollama model maintenance failed", "operation", operation, "execution_id", executionID, logging.ErrorKindKey, kind)
	err := fmt.Errorf("ollama maintenance: %s: %w", operation, cause)
	result := ollamaModelFailedResult(failedEvent, executionID, err.Error(), kind)
	return "", errors.Join(err, publishLFEATypedResponseTo(ctx, s.client, s.config, s.logger, msg, failedEvent, result, s.eventsStore, s.scrubbing))
}

// ollamaModelFailedResult returns the typed result that eventType, a model
// maintenance failure event, carries. The dispatching caller decodes a result by
// the action it sent, so a CommandResult in its place loses the failure: the
// field numbers differ. It returns nil for any other event.
func ollamaModelFailedResult(eventType constants.EventType, executionID, message, kind string) proto.Message {
	status := operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	switch eventType {
	case constants.Event.Operator.OllamaModelInventory.Failed:
		return &operatorv1.OllamaModelInventoryResult{ExecutionId: executionID, Status: status, ErrorMessage: message, ErrorType: kind}
	case constants.Event.Operator.OllamaModelResidency.Failed:
		return &operatorv1.OllamaModelResidencyResult{ExecutionId: executionID, Status: status, ErrorMessage: message, ErrorType: kind}
	case constants.Event.Operator.OllamaModelRelease.Failed:
		return &operatorv1.OllamaModelReleaseResult{ExecutionId: executionID, Status: status, ErrorMessage: message, ErrorType: kind}
	case constants.Event.Operator.OllamaModelPull.Failed:
		return &operatorv1.OllamaModelPullResult{ExecutionId: executionID, Status: status, ErrorMessage: message, ErrorType: kind}
	case constants.Event.Operator.OllamaModelCopy.Failed:
		return &operatorv1.OllamaModelCopyResult{ExecutionId: executionID, Status: status, ErrorMessage: message, ErrorType: kind}
	default:
		return nil
	}
}

// validateReleaseModelTags fails closed on an empty release set and on any tag
// that the served-model-tag validator rejects.
func validateReleaseModelTags(modelTags []string) error {
	if len(modelTags) == 0 {
		return fmt.Errorf("%w: at least one model tag is required", constants.ErrInferenceModelTagInvalid)
	}
	for _, modelTag := range modelTags {
		if err := operatorcapability.ValidateServedModelTag(modelTag); err != nil {
			return err
		}
	}
	return nil
}

func (s *OllamaMaintenanceService) inferenceProvider() (inference.Provider, error) {
	if s.provider == nil {
		return nil, fmt.Errorf("ollama maintenance: %w: inference is not enabled on this Operator", constants.ErrInferenceBackendUnavailable)
	}
	return s.provider, nil
}
