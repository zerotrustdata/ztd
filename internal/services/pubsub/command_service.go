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
	"github.com/zerotrustdata/ztd/internal/models"
	execution "github.com/zerotrustdata/ztd/internal/services/execution"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// StatusUpdateInterval is the interval between periodic status updates during long-running commands.
const StatusUpdateInterval = 10 * time.Second

// CommandService owns command execution and cancellation handling.
type CommandService struct {
	config     *config.Config
	logger     *slog.Logger
	execution  *execution.ExecutionService
	results    ResultsPublisher
	auditStore *storage.SQLAuditStore
}

// NewCommandService creates a new CommandService. Every dependency is required and a nil one
// fails closed at construction. The results publisher carries every result and egresses it
// through the scrubber (constants.ErrPubSubResultsPublisher). The audit store holds the raw
// execution event of every command (constants.ErrAuditStoreRequired).
func NewCommandService(
	cfg *config.Config,
	logger *slog.Logger,
	execSvc *execution.ExecutionService,
	results ResultsPublisher,
	auditStore *storage.SQLAuditStore,
) (*CommandService, error) {
	if cfg == nil || logger == nil || execSvc == nil {
		return nil, fmt.Errorf("command service: %w", constants.ErrMissingRequiredField)
	}
	if results == nil {
		return nil, fmt.Errorf("command service: %w", constants.ErrPubSubResultsPublisher)
	}
	if err := requireAuditStore("command service", auditStore); err != nil {
		return nil, err
	}
	return &CommandService{
		config:     cfg,
		logger:     logger,
		execution:  execSvc,
		results:    results,
		auditStore: auditStore,
	}, nil
}

// HandleExecutionRequest processes an inbound command execution request.
func (cs *CommandService) HandleExecutionRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	if len(msg.Payload) > 1024*1024 {
		cs.logger.Error("Command payload exceeds size limit", "size", len(msg.Payload), "limit", 1024*1024)
		return "", constants.ErrPayloadExceedsLimit
	}
	var protoCmd operatorv1.CommandRequested
	if err := proto.Unmarshal(msg.Payload, &protoCmd); err != nil {
		cs.logger.Error("Failed to decode command payload as protobuf CommandRequested", string(constants.ConnectionStateError), err)
		return "", err
	}
	if protoCmd.Command == "" {
		cs.logger.Error("Invalid CommandRequested: missing command field")
		return "", constants.ErrEmptyCommand
	}

	cs.logger.Info("Parsed command payload via Protobuf (CommandRequested)")

	command := protoCmd.Command
	if err := operatorcapability.ValidateWitnessCommand(cs.config.EffectiveOperatorCapabilities(), command); err != nil {
		// The validator's error embeds the command line, so only its sentinel is logged.
		cs.logger.Error("Witness command rejected", "message_id", msg.ID, string(constants.ConnectionStateError), constants.ErrWitnessCommandNotCapable)
		return "", err
	}
	justification := protoCmd.Justification
	if justification == "" {
		justification = "No justification provided"
	}

	execReq, err := payloadToExecutionRequest(msg)
	if err != nil {
		cs.logger.Error("Failed to create execution request", string(constants.ConnectionStateError), err)
		return "", err
	}

	cs.logger.Info("Command execution requested",
		"execution_id", execReq.ExecutionID,
		"message_id", msg.ID)

	done := make(chan struct{})
	var result *models.ExecutionResult
	var execErr error
	startTime := time.Now().UTC()

	go func() {
		defer close(done)
		result, execErr = cs.execution.ExecuteCommand(ctx, execReq)
	}()

	statusUpdateCount := cs.runStatusTicker(ctx, execReq, msg, command, startTime, done)

	// runStatusTicker may have returned via ctx.Done() before the execution goroutine
	// finished writing result and execErr. Drain done to guarantee the write is visible.
	<-done

	if execErr != nil {
		result = &models.ExecutionResult{
			ExecutionID: execReq.ExecutionID,
			CaseID:      execReq.CaseID,
			TaskID: func() string {
				if execReq.TaskID != nil {
					return *execReq.TaskID
				}
				return ""
			}(),
			InvestigationID: execReq.InvestigationID,
			Command:         execReq.Command,
			Args:            execReq.Args,
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			ErrorMessage:    execErr.Error(),
			ErrorType:       "execution_error",
		}
	} else if result == nil {
		result = &models.ExecutionResult{
			ExecutionID: execReq.ExecutionID,
			CaseID:      execReq.CaseID,
			TaskID: func() string {
				if execReq.TaskID != nil {
					return *execReq.TaskID
				}
				return ""
			}(),
			Status:       operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			ErrorMessage: "execution returned no result",
			ErrorType:    "execution_error",
		}
	} else {
		cs.logger.Info("Command execution completed",
			"execution_id", result.ExecutionID,
			"status", result.Status,
			"status_updates", statusUpdateCount,
			"total_elapsed", fmt.Sprintf("%.1fs", time.Since(startTime).Seconds()))
	}

	cs.logger.Info("About to publish execution result", "execution_id", result.ExecutionID)

	// The execution event is the local record. It holds the raw output, encrypted at
	// rest, and is written before any egress; the published result is scrubbed.
	if err := cs.recordCommandExecution(msg, result, command, justification); err != nil {
		cs.logger.Error("Command executed but its event could not be recorded; result marked FAILED",
			string(constants.ConnectionStateError), err, "execution_id", result.ExecutionID)
		execErr = errors.Join(execErr, err)
		result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		result.ErrorMessage = err.Error()
		result.ErrorType = "audit_record_error"
	}

	// Convert domain result to wire payload for proto publishing
	payload := models.PayloadFromDomainResult(result)
	protoResult := &operatorv1.CommandResult{
		ExecutionId:          payload.ExecutionID,
		Status:               payload.Status,
		Stdout:               payload.Stdout,
		Stderr:               payload.Stderr,
		ExecutionTimeSeconds: float32(payload.DurationSeconds),
	}
	if payload.ReturnCode != nil {
		protoResult.ReturnCode = int32(*payload.ReturnCode) //nolint:gosec // exit codes are 0-255
	}
	if payload.ErrorMessage != nil {
		protoResult.Error = *payload.ErrorMessage
	}
	if payload.StartTime != nil {
		protoResult.StartTimeUnixMs = payload.StartTime.UnixMilli()
	}
	if payload.EndTime != nil {
		protoResult.EndTimeUnixMs = payload.EndTime.UnixMilli()
	}

	// PublishExecutionResult egresses the payload through the scrubber and publishes a typed
	// failure when the content is withheld.
	publishErr := cs.results.PublishExecutionResult(ctx, protoResult, msg)
	if err := errors.Join(execErr, executionResultError(result.Status, result.ErrorMessage), publishErr); err != nil {
		cs.logger.Warn("Command execution failed", "execution_id", result.ExecutionID, logging.ErrorKindKey, logging.ErrorKindValue(err))
		return "", fmt.Errorf("command execution: %w", err)
	}
	return "", nil
}

// recordCommandExecution writes the execution's audit event with the raw stdout and
// stderr. A failed write is an error: the command already ran, so the caller marks
// the result FAILED rather than publishing it unrecorded.
func (cs *CommandService) recordCommandExecution(msg *PubSubCommandMessage, result *models.ExecutionResult, command, justification string) error {
	event := &storage.Event{
		OperatorSessionID:   msg.OperatorSessionID,
		Timestamp:           time.Now().UTC(),
		Type:                constants.Event.Operator.Audit.Command,
		ContentText:         justification,
		CommandRaw:          command,
		CommandExitCode:     result.ReturnCode,
		CommandStdout:       result.Stdout,
		CommandStderr:       result.Stderr,
		ExecutionDurationMs: int64(result.DurationSeconds * 1000),
		ExecutionID:         result.ExecutionID,
	}
	if _, err := cs.auditStore.RecordEvent(event); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrExecutionRecordFailed, err)
	}
	cs.logger.Info("Command execution event recorded in audit store (LFAA)",
		"execution_id", result.ExecutionID,
		"operator_session_id", msg.OperatorSessionID)
	return nil
}

// runStatusTicker drives the periodic status update loop while a command executes.
// It returns the number of status updates sent, which is used in completion logging.
func (cs *CommandService) runStatusTicker(
	ctx context.Context,
	execReq *models.ExecutionRequestPayload,
	msg *PubSubCommandMessage,
	command string,
	startTime time.Time,
	done <-chan struct{},
) int {
	ticker := time.NewTicker(StatusUpdateInterval)
	defer ticker.Stop()

	updateCount := 0

	for {
		select {
		case <-done:
			return updateCount

		case <-ticker.C:
			updateCount++
			elapsed := time.Since(startTime).Seconds()

			statusUpdate := &operatorv1.ExecutionStatusUpdate{
				ExecutionId:    execReq.ExecutionID,
				Command:        command,
				Status:         operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
				ProcessAlive:   true,
				ElapsedSeconds: float32(elapsed),
				Message:        fmt.Sprintf("Command still executing (%.0fs elapsed)", elapsed),
			}
			// Progress telemetry is dropped when its content is withheld at egress; the final result still egresses scrubbed.
			if err := cs.results.PublishExecutionStatus(ctx, statusUpdate, msg); err != nil {
				cs.logger.Warn("Failed to publish status update", string(constants.ConnectionStateError), err)
			} else {
				cs.logger.Info("Execution status update sent",
					"execution_id", execReq.ExecutionID,
					"elapsed", fmt.Sprintf("%.0fs", elapsed),
					"update_count", updateCount)
			}

		case <-ctx.Done():
			cs.logger.Warn("Command execution context cancelled")
			return updateCount
		}
	}
}

// HandleCancelRequest processes an inbound command cancellation request.
func (cs *CommandService) HandleCancelRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	if len(msg.Payload) > 64*1024 {
		cs.logger.Error("Cancel payload exceeds size limit", "size", len(msg.Payload), "limit", 64*1024)
		return "", constants.ErrPayloadExceedsLimit
	}
	var protoCancel operatorv1.CommandCancelRequested
	if err := proto.Unmarshal(msg.Payload, &protoCancel); err != nil {
		cs.logger.Error("Failed to decode cancel payload as protobuf CommandCancelRequested", string(constants.ConnectionStateError), err)
		return "", err
	}
	if protoCancel.ExecutionId == "" {
		cs.logger.Error("Invalid CommandCancelRequested: missing execution_id field")
		return "", constants.ErrMissingRequiredField
	}

	cs.logger.Info("Parsed cancel payload via Protobuf (CommandCancelRequested)")
	executionID := protoCancel.ExecutionId

	cs.logger.Info("Command cancellation requested by user", "execution_id", executionID)

	err := cs.execution.CancelExecution(executionID)

	now := time.Now().UTC()
	var result *models.ExecutionResult

	if err != nil {
		cs.logger.Warn("Failed to cancel execution (may have already completed)", string(constants.ConnectionStateError), err)
		result = &models.ExecutionResult{
			ExecutionID:  executionID,
			CaseID:       msg.CaseID,
			Status:       operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			StartTime:    now,
			ErrorMessage: fmt.Sprintf("Cancel failed: %v", err),
			ErrorType:    "cancel_failed",
		}
	} else {
		cs.logger.Info("Command cancelled successfully", "execution_id", executionID)
		result = &models.ExecutionResult{
			ExecutionID:  executionID,
			CaseID:       msg.CaseID,
			Status:       operatorv1.ExecutionStatus_EXECUTION_STATUS_CANCELLED,
			StartTime:    now,
			ErrorMessage: "Command cancelled by user",
			ErrorType:    "user_cancelled",
		}
	}

	// Convert domain result to wire payload for proto publishing
	payload := models.PayloadFromDomainResult(result)
	protoResult := &operatorv1.CommandResult{
		ExecutionId: executionID,
		Status:      payload.Status,
	}
	if payload.ErrorMessage != nil {
		protoResult.Error = *payload.ErrorMessage
	}
	if payload.StartTime != nil {
		protoResult.StartTimeUnixMs = payload.StartTime.UnixMilli()
	}

	publishErr := cs.results.PublishCancellationResult(ctx, protoResult, msg)
	return "", errors.Join(err, publishErr)
}

// payloadToExecutionRequest is a package-level helper shared by CommandService and tests.
func payloadToExecutionRequest(msg *PubSubCommandMessage) (*models.ExecutionRequestPayload, error) {
	var protoCmd operatorv1.CommandRequested
	if err := proto.Unmarshal(msg.Payload, &protoCmd); err != nil {
		return nil, fmt.Errorf("pubsub: decode command payload: %w", err)
	}
	if protoCmd.Command == "" {
		return nil, fmt.Errorf("pubsub: missing command in protobuf CommandRequested")
	}

	executionID := executionIDFromMessage(msg)
	if protoCmd.ExecutionId != "" {
		executionID = protoCmd.ExecutionId
	}

	timeoutSeconds := 300
	if protoCmd.TimeoutSeconds > 0 {
		timeoutSeconds = int(protoCmd.TimeoutSeconds)
	}

	request := &models.ExecutionRequestPayload{
		ExecutionID:     executionID,
		CaseID:          msg.CaseID,
		TaskID:          msg.TaskID,
		InvestigationID: msg.InvestigationID,
		Command:         protoCmd.Command,
		TimeoutSeconds:  timeoutSeconds,
		RequestedBy:     "ztd-system",
		Environment:     protoCmd.GetEnvironment(),
		Justification:   protoCmd.Justification,
		Intent:          protoCmd.Intent,
	}
	if protoCmd.GetWorkingDirectory() != "" {
		workingDirectory := protoCmd.GetWorkingDirectory()
		request.WorkingDirectory = &workingDirectory
	}
	return request, nil
}
