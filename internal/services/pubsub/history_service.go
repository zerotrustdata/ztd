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
	"strconv"
	"time"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

// HistoryService owns log retrieval, execution history, file history, file restore,
type HistoryService struct {
	config         *config.Config
	logger         *slog.Logger
	client         PubSubClient
	historyHandler *storage.HistoryHandler
	auditStore     *storage.SQLAuditStore
	scrubbing      *scrubbing.ScrubbingService
}

// NewHistoryService creates a new HistoryService. Every dependency is required: the history
// handler serves every history, diff, restore and log query (nil fails with
// constants.ErrHistoryHandlerRequired), the scrubber is the egress choke point (nil or disabled
// fails with constants.ErrEgressScrubberUnavailable) and the audit store records observed-state
// content evidence (nil fails with constants.ErrAuditStoreRequired).
func NewHistoryService(
	cfg *config.Config,
	logger *slog.Logger,
	client PubSubClient,
	historyHandler *storage.HistoryHandler,
	scrubbingSvc *scrubbing.ScrubbingService,
	auditStore *storage.SQLAuditStore,
) (*HistoryService, error) {
	if cfg == nil || logger == nil || client == nil {
		return nil, fmt.Errorf("history service: %w", constants.ErrMissingRequiredField)
	}
	if historyHandler == nil {
		return nil, fmt.Errorf("history service: %w", constants.ErrHistoryHandlerRequired)
	}
	if err := requireActiveScrubber("history service", scrubbingSvc); err != nil {
		return nil, err
	}
	if err := requireAuditStore("history service", auditStore); err != nil {
		return nil, err
	}
	return &HistoryService{
		config:         cfg,
		logger:         logger,
		client:         client,
		historyHandler: historyHandler,
		auditStore:     auditStore,
		scrubbing:      scrubbingSvc,
	}, nil
}

// HandleFetchLogsRequest reads the execution event for an execution_id. The event is the
// local record; the publisher scrubs the result on egress.
func (hs *HistoryService) HandleFetchLogsRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var protoFetch operatorv1.FetchLogsRequested
	if err := proto.Unmarshal(msg.Payload, &protoFetch); err != nil {
		hs.logger.Error("Failed to decode fetch logs payload as protobuf FetchLogsRequested", string(constants.ConnectionStateError), err)
		return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchLogs.Failed, "invalid request payload", hs.auditStore, hs.scrubbing))
	}

	executionID := protoFetch.ExecutionId
	if executionID == "" {
		hs.logger.Warn("Fetch logs request without execution_id")
		err := constants.ErrMissingRequiredField
		return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchLogs.Failed, "missing execution_id in request", hs.auditStore, hs.scrubbing))
	}

	hs.logger.Info("Fetch logs requested (audit events by execution_id, via Protobuf)",
		"execution_id", executionID)

	result, err := hs.historyHandler.FetchExecutionLogs(executionID)
	if err != nil {
		hs.logger.Error("Failed to retrieve execution logs", "execution_id", executionID, string(constants.ConnectionStateError), err)
		return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchLogs.Failed, fmt.Sprintf("failed to retrieve execution: %v", err), hs.auditStore, hs.scrubbing))
	}

	if err := publishLFAATypedResponseTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchLogs.Completed, result, hs.auditStore, hs.scrubbing); err != nil {
		return "", err
	}
	hs.logger.Info("Fetch logs result transmitted",
		"execution_id", executionID,
		"stdout_size", result.StdoutSize,
		"stderr_size", result.StderrSize)
	return "", nil
}

// HandleFetchHistoryRequest processes a fetch history request.
func (hs *HistoryService) HandleFetchHistoryRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	hs.logger.Info("FETCH_HISTORY requested (LFAA)")

	payload, err := hs.historyHandler.HandleFetchHistory(msg.Payload)
	if err != nil {
		hs.logger.Error("History handler failed", string(constants.ConnectionStateError), err)
		return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchHistory.Failed,
			fmt.Sprintf("failed to fetch history: %v", err), hs.auditStore, hs.scrubbing))
	}

	return "", publishLFAATypedResponseTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchHistory.Completed, payload, hs.auditStore, hs.scrubbing)
}

// HandleFetchFileHistoryRequest processes a fetch file history request.
func (hs *HistoryService) HandleFetchFileHistoryRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var protoFetch operatorv1.FetchFileHistoryRequested
	if err := proto.Unmarshal(msg.Payload, &protoFetch); err != nil {
		hs.logger.Error("Failed to decode fetch file history payload as protobuf FetchFileHistoryRequested", string(constants.ConnectionStateError), err)
		return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileHistory.Failed, "invalid request payload", hs.auditStore, hs.scrubbing))
	}
	hs.logger.Info("FETCH_FILE_HISTORY requested (LFAA, via Protobuf)", "message_id", msg.ID)

	payload, err := hs.historyHandler.HandleFetchFileHistory(msg.Payload)
	if err != nil {
		hs.logger.Error("File history handler failed", "message_id", msg.ID, logging.ErrorKindKey, logging.ErrorKindValue(err))
		return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileHistory.Failed,
			fmt.Sprintf("failed to fetch file history: %v", err), hs.auditStore, hs.scrubbing))
	}

	return "", publishLFAATypedResponseTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileHistory.Completed, payload, hs.auditStore, hs.scrubbing)
}

// HandleRestoreFileRequest processes a file restore request. The restore is mirrored through the
// verified session's ledger and recorded, before the result is published, as a raw execution
// event plus a file mutation row, as for FILE_EDIT. A failed record marks the result failed, so
// an unrecorded mutation is never reported as completed.
func (hs *HistoryService) HandleRestoreFileRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	hs.logger.Info("RESTORE_FILE requested (LFAA, via Protobuf)", "message_id", msg.ID)
	start := time.Now()

	var request operatorv1.RestoreFileRequested
	if err := proto.Unmarshal(msg.Payload, &request); err != nil {
		hs.logger.Error("Failed to decode restore file payload as protobuf RestoreFileRequested", string(constants.ConnectionStateError), err)
	}
	result, ledgerResult, restoreErr := hs.historyHandler.HandleRestoreFile(ctx, msg.OperatorSessionID, msg.Payload)
	result.ExecutionId = executionIDFromMessage(msg)

	restoreCommand := fmt.Sprintf("restore_file: %s @ %s", request.GetFilePath(), request.GetCommitHash())
	eventType := constants.Event.Operator.RestoreFile.Completed
	exitCode := 0
	if !result.GetSuccess() {
		eventType = constants.Event.Operator.RestoreFile.Failed
		exitCode = 1
	}
	if err := hs.recordRestore(&storage.Event{
		OperatorSessionID:   msg.OperatorSessionID,
		Timestamp:           time.Now().UTC(),
		Type:                eventType,
		ContentText:         restoreCommand,
		CommandRaw:          restoreCommand,
		CommandExitCode:     exitCode,
		CommandStderr:       result.GetError(),
		ExecutionDurationMs: time.Since(start).Milliseconds(),
		ExecutionID:         result.ExecutionId,
	}, result, ledgerResult); err != nil {
		hs.logger.Error("Restore file could not be recorded; result marked failed", logging.ErrorKind(err))
		restoreErr = errors.Join(restoreErr, err)
		eventType = constants.Event.Operator.RestoreFile.Failed
		result.Success = false
		result.Error = err.Error()
	}
	publishErr := publishLFAATypedResponseTo(ctx, hs.client, hs.config, hs.logger, msg, eventType, result, hs.auditStore, hs.scrubbing)
	return "", errors.Join(restoreErr, publishErr)
}

// recordRestore writes the restore's execution event and, for a completed restore, its file
// mutation row keyed to that event.
func (hs *HistoryService) recordRestore(event *storage.Event, result *operatorv1.RestoreFileResult, ledgerResult *storage.LedgerResult) error {
	eventID, err := hs.auditStore.RecordEvent(event)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrExecutionRecordFailed, err)
	}
	if !result.GetSuccess() || ledgerResult == nil {
		return nil
	}
	if err := hs.auditStore.RecordFileMutation(&storage.FileMutationLog{
		EventID:          eventID,
		Filepath:         result.GetFilePath(),
		Operation:        ledgerResult.Operation,
		LedgerHashBefore: ledgerResult.LedgerHashBefore,
		LedgerHashAfter:  ledgerResult.LedgerHashAfter,
		DiffStat:         ledgerResult.DiffStat,
	}); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreRecordFileMutationFailed, err)
	}
	return nil
}

// HandleFetchFileDiffRequest processes a fetch file diff request. A diff_id names a
// file_mutation_log row; the diff is computed from the session ledger. An
// operator_session_id lists that session's mutations without diff content.
func (hs *HistoryService) HandleFetchFileDiffRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	hs.logger.Info("FETCH_FILE_DIFF requested (LFAA, via Protobuf)")

	var protoDiff operatorv1.FetchFileDiffRequested
	if err := proto.Unmarshal(msg.Payload, &protoDiff); err != nil {
		hs.logger.Error("Failed to decode fetch file diff payload as protobuf FetchFileDiffRequested", string(constants.ConnectionStateError), err)
		return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileDiff.Failed, "invalid request payload", hs.auditStore, hs.scrubbing))
	}
	diffID := protoDiff.DiffId
	operatorSessionID := protoDiff.OperatorSessionId
	filePath := protoDiff.FilePath
	limit := protoDiff.Limit
	if limit <= 0 {
		limit = 50
	}

	if diffID != "" {
		mutationID, err := strconv.ParseInt(diffID, 10, 64)
		if err != nil {
			return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileDiff.Failed,
				fmt.Sprintf("invalid diff_id: %s", diffID), hs.auditStore, hs.scrubbing))
		}
		entry, err := hs.historyHandler.FetchFileDiff(mutationID)
		if err != nil {
			hs.logger.Error("Failed to fetch file diff", "diff_id", diffID, logging.ErrorKindKey, logging.ErrorKindValue(err))
			return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileDiff.Failed,
				fmt.Sprintf("failed to fetch file diff: %v", err), hs.auditStore, hs.scrubbing))
		}
		return "", publishLFAATypedResponseTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileDiff.Completed,
			&operatorv1.FetchFileDiffResult{
				Success: true,
				Diff:    entry,
			}, hs.auditStore, hs.scrubbing)
	}

	if operatorSessionID != "" {
		diffs, err := hs.historyHandler.ListFileDiffs(operatorSessionID, filePath, int(limit))
		if err != nil {
			hs.logger.Error("Failed to fetch file diffs by session", "operator_session_id", operatorSessionID, logging.ErrorKindKey, logging.ErrorKindValue(err))
			return "", errors.Join(err, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileDiff.Failed,
				fmt.Sprintf("failed to fetch file diffs: %v", err), hs.auditStore, hs.scrubbing))
		}

		total := int32(len(diffs)) //nolint:gosec // bounded by query limits
		return "", publishLFAATypedResponseTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileDiff.Completed,
			&operatorv1.FetchFileDiffResult{
				Success:           true,
				Diffs:             diffs,
				Total:             total,
				OperatorSessionId: operatorSessionID,
			}, hs.auditStore, hs.scrubbing)
	}

	return "", errors.Join(constants.ErrMissingRequiredField, publishLFAAErrorTo(ctx, hs.client, hs.config, hs.logger, msg, constants.Event.Operator.FetchFileDiff.Failed,
		"either diff_id or operator_session_id is required", hs.auditStore, hs.scrubbing))
}
