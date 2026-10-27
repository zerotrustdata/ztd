// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// PubSubResultsService handles publishing results back to the requesting agent via Operator pub/sub
type PubSubResultsService struct {
	client   PubSubClient
	config   *config.Config
	logger   *slog.Logger
	scrubber *scrubbing.ScrubbingService
}

// NewPubSubResultsService creates a new Operator pub/sub results service. Every result payload is
// egressed through scrubber; a nil or disabled scrubber withholds content-bearing results.
func NewPubSubResultsService(cfg *config.Config, logger *slog.Logger, client PubSubClient, scrubber *scrubbing.ScrubbingService) (*PubSubResultsService, error) {
	return &PubSubResultsService{
		client:   client,
		config:   cfg,
		logger:   logger,
		scrubber: scrubber,
	}, nil
}

// PublishExecutionResult publishes command execution result via Operator pub/sub
// Stdout/stderr have already been sentinel.Sentinel-scrubbed by pubsub_commands.go before this is called.
func (rr *PubSubResultsService) PublishExecutionResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	eventType := rr.determineEventStatus(result, constants.Event.Operator.Command.Completed, constants.Event.Operator.Command.Failed)

	caseID := originalMsg.CaseID
	taskID := originalMsg.TaskID
	investigationID := originalMsg.InvestigationID

	rr.logger.Info("Publishing execution result", "original_message_id", originalMsg.ID)
	if err := rr.publishResultEnvelopeUniversal(ctx, eventType, caseID, taskID, investigationID, originalMsg, result); err != nil {
		return fmt.Errorf("pubsub: publish execution result: %w", err)
	}

	rr.logger.Info("Execution result transmitted to ztd",
		"operator_session_id", rr.config.OperatorSessionId,
		"event_type", eventType)
	return nil
}

// PublishCancellationResult publishes command cancellation result via Operator pub/sub
func (rr *PubSubResultsService) PublishCancellationResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	eventType := constants.Event.Operator.Command.Cancelled

	if err := rr.publishResultEnvelopeUniversal(ctx, eventType, originalMsg.CaseID, originalMsg.TaskID, originalMsg.InvestigationID, originalMsg, result); err != nil {
		return fmt.Errorf("pubsub: publish cancellation result: %w", err)
	}

	rr.logger.Info("Cancellation result transmitted to ztd",
		"operator_session_id", rr.config.OperatorSessionId)
	return nil
}

func (rr *PubSubResultsService) PublishShutdownAcknowledgement(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	if err := rr.publishResultEnvelopeUniversal(ctx, constants.Event.Operator.ShutdownAcknowledged, originalMsg.CaseID, originalMsg.TaskID, originalMsg.InvestigationID, originalMsg, result); err != nil {
		return fmt.Errorf("pubsub: publish shutdown acknowledgement: %w", err)
	}
	return nil
}

// PublishFileEditResult publishes file edit result via Operator pub/sub.
func (rr *PubSubResultsService) PublishFileEditResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	eventType := rr.determineEventStatus(result, constants.Event.Operator.FileEdit.Completed, constants.Event.Operator.FileEdit.Failed)

	if err := rr.publishResultEnvelopeUniversal(ctx, eventType, originalMsg.CaseID, originalMsg.TaskID, originalMsg.InvestigationID, originalMsg, result); err != nil {
		return fmt.Errorf("pubsub: publish file edit result: %w", err)
	}

	rr.logger.Info("File operation result transmitted to ztd", "operator_session_id", rr.config.OperatorSessionId)
	return nil
}

// PublishEvaluationWorkspaceSeedResult publishes an evaluation workspace seed result via Operator pub/sub.
func (rr *PubSubResultsService) PublishEvaluationWorkspaceSeedResult(ctx context.Context, result *operatorv1.EvaluationWorkspaceSeedResult, originalMsg *PubSubCommandMessage) error {
	eventType := rr.determineEventStatus(result, constants.Event.Operator.Eval.WorkspaceSeed.Completed, constants.Event.Operator.Eval.WorkspaceSeed.Failed)

	if err := rr.publishResultEnvelopeUniversal(ctx, eventType, originalMsg.CaseID, originalMsg.TaskID, originalMsg.InvestigationID, originalMsg, result); err != nil {
		return fmt.Errorf("pubsub: publish evaluation workspace seed result: %w", err)
	}
	return nil
}

// PublishFsListResult publishes file system list result via Operator pub/sub.
func (rr *PubSubResultsService) PublishFsListResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	eventType := rr.determineEventStatus(result, constants.Event.Operator.FsList.Completed, constants.Event.Operator.FsList.Failed)

	if err := rr.publishResultEnvelopeUniversal(ctx, eventType, originalMsg.CaseID, originalMsg.TaskID, originalMsg.InvestigationID, originalMsg, result); err != nil {
		return fmt.Errorf("pubsub: publish fs list result: %w", err)
	}

	rr.logger.Info("FS list result transmitted to ztd", "operator_session_id", rr.config.OperatorSessionId)
	return nil
}

// PublishFsGrepResult publishes file system grep result via Operator pub/sub.
func (rr *PubSubResultsService) PublishFsGrepResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	eventType := rr.determineEventStatus(result, constants.Event.Operator.FsGrep.Completed, constants.Event.Operator.FsGrep.Failed)

	if err := rr.publishResultEnvelopeUniversal(ctx, eventType, originalMsg.CaseID, originalMsg.TaskID, originalMsg.InvestigationID, originalMsg, result); err != nil {
		return fmt.Errorf("pubsub: publish fs grep result: %w", err)
	}

	rr.logger.Info("FS grep result transmitted to ztd", "operator_session_id", rr.config.OperatorSessionId)
	return nil
}

// PublishInferenceProgress publishes bounded inference progress telemetry to
// the results channel while a governed provider stream is active. Progress
// events are delivery telemetry only; the signed InferenceCompletion remains
// the sole authoritative terminal outcome.
func (rr *PubSubResultsService) PublishInferenceProgress(ctx context.Context, originalMsg *PubSubCommandMessage, progress *operatorv1.InferenceProgressEvent) error {
	if originalMsg == nil || progress == nil {
		return fmt.Errorf("pubsub: publish inference progress: %w", constants.ErrMissingRequiredField)
	}

	originatingAction, err := constants.Registry.ActionFor(originalMsg.EventType)
	if err != nil {
		return fmt.Errorf("pubsub: publish inference progress: %w", err)
	}
	resultEnv, err := BuildUniversalResultEnvelope(
		ctx,
		rr.config,
		originalMsg.EventType,
		constants.Event.Operator.Inference.ProgressUpdated,
		originatingAction,
		progress,
		rr.scrubber,
		originalMsg.ID,
		rr.config.OperatorID,
		originalMsg.CaseID,
		originalMsg.InvestigationID,
		originalMsg.TaskID,
		originalMsg.WebSessionID,
		originalMsg.CLISessionID,
	)
	if err != nil {
		return fmt.Errorf("pubsub: build inference progress envelope: %w", err)
	}
	resultEnv.OperatorSessionId = originalMsg.OperatorSessionID
	operatorID := rr.config.OperatorID
	if originalMsg.OperatorID != nil && *originalMsg.OperatorID != "" {
		operatorID = *originalMsg.OperatorID
	}

	if err := rr.publishUniversal(ctx, resultEnv, operatorID, originalMsg.OperatorSessionID); err != nil {
		return fmt.Errorf("pubsub: publish inference progress: %w", err)
	}
	return nil
}

// PublishInferenceCompletion publishes the protocol-owned InferenceCompletion
// — the final signed EventReceipt plus, on success, the complete
// InferenceResult — via Operator pub/sub. The completion envelope is
// correlated with the original command by transaction ID (the command
// envelope's Id) on the results channel. A completion whose receipt status
// is not EXECUTION_STATUS_COMPLETED is published under the inference.failed
// event type so the waiting Gateway dispatch terminates immediately with a
// typed failure.
func (rr *PubSubResultsService) PublishInferenceCompletion(ctx context.Context, env *commonv1.GovernanceEnvelope, completion *operatorv1.InferenceCompletion) error {
	if env == nil || completion == nil || completion.Receipt == nil {
		return fmt.Errorf("pubsub: publish inference completion: %w", constants.ErrMissingRequiredField)
	}

	eventType := constants.Event.Operator.Inference.Completed
	if completion.Receipt.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		eventType = constants.Event.Operator.Inference.Failed
	}

	resultEnv, err := BuildUniversalResultEnvelope(ctx, rr.config, constants.EventType(env.EventType), eventType, constants.ActionType(env.ActionType), completion, rr.scrubber, env.Id, env.OperatorId, env.CaseId, env.InvestigationId, &env.TaskId, env.WebSessionId, env.CliSessionId)
	if err != nil {
		return fmt.Errorf("pubsub: build inference completion envelope: %w", err)
	}
	// Route and identify by the command envelope's operator session, not the
	// service config, so the completion returns on the channel the waiting
	// dispatcher is subscribed to.
	resultEnv.OperatorSessionId = env.OperatorSessionId

	if err := rr.publishUniversal(ctx, resultEnv, env.OperatorId, env.OperatorSessionId); err != nil {
		return fmt.Errorf("pubsub: publish inference completion: %w", err)
	}

	rr.logger.Info("Inference completion transmitted to ztd",
		"operator_session_id", env.OperatorSessionId,
		"event_type", eventType,
		"transaction_id", completion.Receipt.TransactionId)
	return nil
}

// PublishProviderBoundaryObservationCompleted publishes a completed
// provider-boundary observation window on the observer operator results
// channel. The completion is correlated with the originating command by
// transaction ID.
func (rr *PubSubResultsService) PublishProviderBoundaryObservationCompleted(ctx context.Context, origin *commonv1.GovernanceEnvelope, completion *evalv1.ProviderBoundaryObservationCompleted) error {
	if origin == nil || origin.GetId() == "" || completion == nil || completion.GetWindow() == nil {
		return fmt.Errorf("pubsub: publish provider boundary observation completion: %w", constants.ErrMissingRequiredField)
	}

	originatingAction, err := constants.Registry.ActionFor(constants.Event.Operator.ProviderBoundaryObservation.Requested)
	if err != nil {
		return fmt.Errorf("pubsub: publish provider boundary observation completion: %w", err)
	}
	resultEnv, err := BuildUniversalResultEnvelope(
		ctx,
		rr.config,
		constants.Event.Operator.ProviderBoundaryObservation.Requested,
		constants.Event.Operator.ProviderBoundaryObservation.Completed,
		originatingAction,
		completion,
		rr.scrubber,
		origin.Id,
		origin.OperatorId,
		origin.CaseId,
		origin.InvestigationId,
		&origin.TaskId,
		origin.WebSessionId,
		origin.CliSessionId,
	)
	if err != nil {
		return fmt.Errorf("pubsub: build provider boundary observation completion envelope: %w", err)
	}

	resultEnv.OperatorSessionId = origin.OperatorSessionId
	if err := rr.publishUniversal(ctx, resultEnv, origin.OperatorId, origin.OperatorSessionId); err != nil {
		return fmt.Errorf("pubsub: publish provider boundary observation completion: %w", err)
	}

	rr.logger.Info("Provider-boundary observation completion transmitted",
		"operator_session_id", origin.OperatorSessionId,
		"provider_attempt_id", completion.GetWindow().GetProviderAttemptId(),
		"transaction_id", origin.Id)
	return nil
}

// PublishModelProvenanceObservationCompleted publishes a completed model
// provenance attestation window on the provenance operator results channel.
func (rr *PubSubResultsService) PublishModelProvenanceObservationCompleted(ctx context.Context, origin *commonv1.GovernanceEnvelope, completion *evalv1.ModelProvenanceObservationCompleted) error {
	if origin == nil || origin.GetId() == "" || completion == nil || completion.GetWindow() == nil {
		return fmt.Errorf("pubsub: publish model provenance observation completion: %w", constants.ErrMissingRequiredField)
	}

	originatingAction, err := constants.Registry.ActionFor(constants.Event.Operator.ModelProvenanceObservation.Requested)
	if err != nil {
		return fmt.Errorf("pubsub: publish model provenance observation completion: %w", err)
	}
	resultEnv, err := BuildUniversalResultEnvelope(
		ctx,
		rr.config,
		constants.Event.Operator.ModelProvenanceObservation.Requested,
		constants.Event.Operator.ModelProvenanceObservation.Completed,
		originatingAction,
		completion,
		rr.scrubber,
		origin.Id,
		origin.OperatorId,
		origin.CaseId,
		origin.InvestigationId,
		&origin.TaskId,
		origin.WebSessionId,
		origin.CliSessionId,
	)
	if err != nil {
		return fmt.Errorf("pubsub: build model provenance observation completion envelope: %w", err)
	}

	resultEnv.OperatorSessionId = origin.OperatorSessionId
	if err := rr.publishUniversal(ctx, resultEnv, origin.OperatorId, origin.OperatorSessionId); err != nil {
		return fmt.Errorf("pubsub: publish model provenance observation completion: %w", err)
	}

	rr.logger.Info("Model provenance observation completion transmitted",
		"operator_session_id", origin.OperatorSessionId,
		"provider_attempt_id", completion.GetWindow().GetProviderAttemptId(),
		"transaction_id", origin.Id)
	return nil
}

// PublishExecutionStatus publishes periodic status updates during command execution.
func (rr *PubSubResultsService) PublishExecutionStatus(ctx context.Context, status proto.Message, originalMsg *PubSubCommandMessage) error {
	reflectMsg := status.ProtoReflect()

	// Extract execution status and execution ID via reflection (payload-specific)
	var executionStatus protoreflect.EnumNumber
	var executionID string

	if fd := reflectMsg.Descriptor().Fields().ByName("status"); fd != nil {
		executionStatus = reflectMsg.Get(fd).Enum()
	}
	if fd := reflectMsg.Descriptor().Fields().ByName("execution_id"); fd != nil {
		executionID = reflectMsg.Get(fd).String()
	}

	eventType := constants.Event.Operator.Command.StatusUpdated.Running
	switch executionStatus {
	case protoreflect.EnumNumber(operatorv1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED):
		eventType = constants.Event.Operator.Command.StatusUpdated.Queued
	case protoreflect.EnumNumber(operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED):
		eventType = constants.Event.Operator.Command.StatusUpdated.Completed
	case protoreflect.EnumNumber(operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED), protoreflect.EnumNumber(operatorv1.ExecutionStatus_EXECUTION_STATUS_TIMEOUT):
		eventType = constants.Event.Operator.Command.StatusUpdated.Failed
	case protoreflect.EnumNumber(operatorv1.ExecutionStatus_EXECUTION_STATUS_CANCELLED):
		eventType = constants.Event.Operator.Command.StatusUpdated.Cancelled
	}

	// Use original message ID for correlation and context from originalMsg
	operatorID := rr.config.OperatorID
	if originalMsg.OperatorID != nil && *originalMsg.OperatorID != "" {
		operatorID = *originalMsg.OperatorID
	}
	originatingAction, err := constants.Registry.ActionFor(originalMsg.EventType)
	if err != nil {
		return fmt.Errorf("pubsub: publish status update: %w", err)
	}
	env, err := BuildUniversalResultEnvelope(ctx, rr.config, originalMsg.EventType, eventType, originatingAction, status, rr.scrubber, originalMsg.ID, operatorID, originalMsg.CaseID, originalMsg.InvestigationID, originalMsg.TaskID, originalMsg.WebSessionID, originalMsg.CLISessionID)
	if err != nil {
		return fmt.Errorf("pubsub: build status envelope: %w", err)
	}

	if err := rr.publishUniversal(ctx, env, operatorID, originalMsg.OperatorSessionID); err != nil {
		return fmt.Errorf("pubsub: publish status update: %w", err)
	}

	rr.logger.Info("Execution status update transmitted", "event_type", eventType, "execution_id", executionID)
	return nil
}

// PublishHeartbeat publishes heartbeat to dedicated Operator pub/sub heartbeat channel.
// It wraps the heartbeat in a GovernanceEnvelope for consistency with other results.
func (rr *PubSubResultsService) PublishHeartbeat(ctx context.Context, heartbeat proto.Message) error {
	rr.logger.Info("Publishing heartbeat to Operator pub/sub")

	// Build the GovernanceEnvelope
	operatorSessionID := rr.config.OperatorSessionId

	env, err := BuildUniversalResultEnvelope(ctx, rr.config, "", constants.Event.Operator.Heartbeat, constants.ActionTypeHeartbeat, heartbeat, rr.scrubber, "", rr.config.OperatorID, "", "", nil, "", "")
	if err != nil {
		return fmt.Errorf("pubsub: build heartbeat envelope: %w", err)
	}

	data, err := protojson.Marshal(env)
	if err != nil {
		return fmt.Errorf("pubsub: marshal heartbeat envelope: %w", err)
	}

	channelName := HeartbeatChannel(rr.config.OperatorID, operatorSessionID)
	if err := rr.client.Publish(ctx, channelName, data); err != nil {
		return fmt.Errorf("pubsub: publish heartbeat: %w", err)
	}
	return nil
}

// PublishEventReceipt publishes a signed EventReceipt to the gateway's
// receipts:<operator_id>:<operator_session_id> channel. The receipt is wrapped
// as the payload of a GovernanceEnvelope that copies the identity fields
// (operator_id, operator_session_id, requestor_user_id, acting_app_id,
// action_type, target_resource, case_id, investigation_id, task_id) from the
// original command envelope env, so the gateway can build a complete
// EventReceiptRecord without the EventReceipt proto carrying those fields.
// The gateway intercepts the publish, verifies the receipt signature against
// the operator's actuator public key, records the receipt in its
// LocalFirstEventsStore, and fans out the envelope to subscribers.
func (rr *PubSubResultsService) PublishEventReceipt(ctx context.Context, env *commonv1.GovernanceEnvelope, receipt *operatorv1.EventReceipt) error {
	if env == nil || receipt == nil {
		return fmt.Errorf("pubsub: publish event receipt: %w", constants.ErrMissingRequiredField)
	}
	receiptEventType := constants.Event.Operator.Receipt.Recorded
	receiptEntry, ok := constants.Registry.Lookup(receiptEventType)
	if !ok {
		return fmt.Errorf("pubsub: publish event receipt: %w: %q", constants.ErrTxUnknownEventType, receiptEventType)
	}
	if !receiptEntry.HasTransport("pubsub") {
		return fmt.Errorf("pubsub: publish event receipt: %w: %q", constants.ErrEventTransportViolation, receiptEventType)
	}

	receiptBytes, err := proto.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("pubsub: publish event receipt: marshal receipt: %w", err)
	}

	receiptEnv := &commonv1.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		Timestamp:         env.Timestamp,
		SourceComponent:   commonv1.Component_COMPONENT_ZTDO,
		OperatorId:        env.OperatorId,
		OperatorSessionId: env.OperatorSessionId,
		ActionType:        env.ActionType,
		TargetResource:    env.TargetResource,
		EventType:         string(constants.Event.Operator.Receipt.Recorded),
		Payload:           receiptBytes,
		RequestorUserId:   env.RequestorUserId,
		ActingAppId:       env.ActingAppId,
		CaseId:            env.CaseId,
		InvestigationId:   env.InvestigationId,
		TaskId:            env.TaskId,
		WebSessionId:      env.WebSessionId,
		CliSessionId:      env.CliSessionId,
	}

	wire, err := protojson.Marshal(receiptEnv)
	if err != nil {
		return fmt.Errorf("pubsub: publish event receipt: marshal envelope: %w", err)
	}

	channel := ReceiptsChannel(env.OperatorId, env.OperatorSessionId)
	rr.logger.Info("Publishing event receipt to gateway receipts channel",
		"channel", channel,
		"transaction_id", receipt.TransactionId,
		"event_type", receiptEnv.EventType)
	if err := rr.client.Publish(ctx, channel, wire); err != nil {
		return fmt.Errorf("pubsub: publish event receipt: %w", err)
	}
	return nil
}

// publishUniversal marshals a GovernanceEnvelope as protojson and publishes it to the results channel.
// operatorID overrides rr.config.OperatorID for channel routing (e.g. gateway mode where config has no operator ID).
func (rr *PubSubResultsService) publishUniversal(ctx context.Context, env *commonv1.GovernanceEnvelope, operatorID, operatorSessionID string) error {
	if env != nil && env.EventType != "" {
		entry, ok := constants.Registry.Lookup(constants.EventType(env.EventType))
		if !ok {
			return fmt.Errorf("pubsub: publish universal: %w: %q", constants.ErrTxUnknownEventType, env.EventType)
		}
		if !entry.HasTransport("pubsub") {
			return fmt.Errorf("pubsub: publish universal: %w: %q", constants.ErrEventTransportViolation, env.EventType)
		}
	}
	data, err := protojson.Marshal(env)
	if err != nil {
		return fmt.Errorf("pubsub: marshal envelope: %w", err)
	}
	if operatorID == "" {
		operatorID = rr.config.OperatorID
	}
	channel := ResultsChannel(operatorID, operatorSessionID)
	rr.logger.Info("Publishing result",
		"channel", channel,
		"event_type", env.EventType,
		"id", env.Id)
	return rr.client.Publish(ctx, channel, data)
}

// determineEventStatus determines the event type based on the status field of a proto message via reflection.
// Returns failedEventType if the status is FAILED or TIMEOUT, otherwise returns completedEventType.
func (rr *PubSubResultsService) determineEventStatus(result proto.Message, completedEventType, failedEventType constants.EventType) constants.EventType {
	reflectMsg := result.ProtoReflect()
	statusFd := reflectMsg.Descriptor().Fields().ByName("status")
	if statusFd != nil {
		status := reflectMsg.Get(statusFd).Enum()
		if status == protoreflect.EnumNumber(operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED) || status == protoreflect.EnumNumber(operatorv1.ExecutionStatus_EXECUTION_STATUS_TIMEOUT) {
			return failedEventType
		}
	}
	return completedEventType
}

// publishResultEnvelopeUniversal builds a GovernanceEnvelope for result publishing.
func (rr *PubSubResultsService) publishResultEnvelopeUniversal(
	ctx context.Context,
	eventType constants.EventType,
	caseID string,
	taskID *string,
	investigationID string,
	originalMsg *PubSubCommandMessage,
	payload proto.Message,
) error {
	// Use original message ID for correlation
	originalMessageID := originalMsg.ID
	senderID := rr.config.OperatorID
	if originalMsg.OperatorID != nil {
		senderID = *originalMsg.OperatorID
	}

	originatingAction, err := constants.Registry.ActionFor(originalMsg.EventType)
	if err != nil {
		return fmt.Errorf("pubsub: build result envelope: %w", err)
	}
	env, err := BuildUniversalResultEnvelope(ctx, rr.config, originalMsg.EventType, eventType, originatingAction, payload, rr.scrubber, originalMessageID, senderID, caseID, investigationID, taskID, originalMsg.WebSessionID, originalMsg.CLISessionID)
	if err != nil {
		if isEgressWithheld(err) {
			return rr.publishWithheldFailure(ctx, eventType, originalMsg, payload, senderID, caseID, investigationID, taskID, err)
		}
		return fmt.Errorf("pubsub: build result envelope: %w", err)
	}

	return rr.publishUniversal(ctx, env, senderID, originalMsg.OperatorSessionID)
}

// publishWithheldFailure replaces a result whose content was withheld at egress with the typed
// failure event for its payload type. The failure carries only fixed text, never the payload.
// A payload type without a typed failure event is not published; the withholding error is returned.
func (rr *PubSubResultsService) publishWithheldFailure(
	ctx context.Context,
	outcome constants.EventType,
	originalMsg *PubSubCommandMessage,
	payload proto.Message,
	senderID string,
	caseID string,
	investigationID string,
	taskID *string,
	withheld error,
) error {
	failure := egressFailureEvent(outcome, payload)
	if failure == "" {
		return fmt.Errorf("pubsub: build result envelope: %w", withheld)
	}
	originatingAction, err := constants.Registry.ActionFor(originalMsg.EventType)
	if err != nil {
		return fmt.Errorf("pubsub: build withheld failure envelope: %w", err)
	}
	failurePayload := &operatorv1.CommandResult{
		ExecutionId: executionIDFromMessage(originalMsg),
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		Error:       egressWithheldText(withheld),
	}
	env, err := buildResultEnvelope(rr.config, originalMsg.EventType, failure, originatingAction, failurePayload, originalMsg.ID, senderID, caseID, investigationID, taskID, originalMsg.WebSessionID, originalMsg.CLISessionID)
	if err != nil {
		return fmt.Errorf("pubsub: build withheld failure envelope: %w", err)
	}
	if err := rr.publishUniversal(ctx, env, senderID, originalMsg.OperatorSessionID); err != nil {
		return fmt.Errorf("pubsub: publish withheld failure: %w", err)
	}
	return fmt.Errorf("pubsub: result withheld: %w", withheld)
}
