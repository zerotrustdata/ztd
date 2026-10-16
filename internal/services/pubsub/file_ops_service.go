// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	execution "github.com/zerotrustdata/ztd/internal/services/execution"
	fsvc "github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	// maxInt32Value is the maximum value for int32, used for bounds checking
	maxInt32Value = math.MaxInt32

	// defaultMaxReadSize is the default maximum size for file reads in bytes
	defaultMaxReadSize = 102400

	// defaultMaxEntries is the default maximum number of entries for fs list
	defaultMaxEntries = 100

	// defaultMaxMatches is the default maximum number of matches for fs grep
	defaultMaxMatches = 100
)

// FileOpsService owns file edit, fs list, and fs read handling.
type FileOpsService struct {
	config     *config.Config
	logger     *slog.Logger
	fileEdit   *execution.FileEditService
	fsList     *execution.FsListService
	fsGrep     *execution.FsGrepService
	results    ResultsPublisher
	scrubbing  *scrubbing.ScrubbingService
	auditStore *storage.SQLAuditStore
	ledger     *storage.GitLedgerService
	client     PubSubClient
}

// NewFileOpsService creates a new FileOpsService. Every dependency is required and a nil one
// fails closed at construction. The file ledger is required because a runtime that can mutate
// files must snapshot them first (constants.ErrLedgerRequired). The audit store records the
// execution event and the file mutation of every operation (constants.ErrAuditStoreRequired).
// The results publisher carries every result (constants.ErrPubSubResultsPublisher). The scrubber
// is the egress choke point for observed-state responses (constants.ErrEgressScrubberUnavailable
// when nil or disabled).
func NewFileOpsService(
	cfg *config.Config,
	logger *slog.Logger,
	fileEditSvc *execution.FileEditService,
	client PubSubClient,
	ledger *storage.GitLedgerService,
	results ResultsPublisher,
	scrubbingSvc *scrubbing.ScrubbingService,
	auditStore *storage.SQLAuditStore,
) (*FileOpsService, error) {
	if cfg == nil || logger == nil || fileEditSvc == nil || client == nil {
		return nil, fmt.Errorf("file ops service: %w", constants.ErrMissingRequiredField)
	}
	if ledger == nil {
		return nil, fmt.Errorf("file ops service: %w", constants.ErrLedgerRequired)
	}
	if results == nil {
		return nil, fmt.Errorf("file ops service: %w", constants.ErrPubSubResultsPublisher)
	}
	if err := requireActiveScrubber("file ops service", scrubbingSvc); err != nil {
		return nil, err
	}
	if err := requireAuditStore("file ops service", auditStore); err != nil {
		return nil, err
	}
	return &FileOpsService{
		config:     cfg,
		logger:     logger,
		fileEdit:   fileEditSvc,
		fsList:     execution.NewFsListService(cfg.WorkDir, logger),
		fsGrep:     execution.NewFsGrepService(cfg.WorkDir, logger),
		results:    results,
		scrubbing:  scrubbingSvc,
		auditStore: auditStore,
		ledger:     ledger,
		client:     client,
	}, nil
}

// HandleFileEditRequest processes an inbound file edit request.
func (fs *FileOpsService) HandleFileEditRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var protoEdit operatorv1.FileEditRequested
	if err := proto.Unmarshal(msg.Payload, &protoEdit); err != nil {
		fs.logger.Error("Failed to decode file edit payload as protobuf FileEditRequested", "error", err)
		return "", fmt.Errorf("file edit: decode request: %w", err)
	}

	operation := protoEdit.Operation

	fs.logger.Info("File edit requested (via Protobuf)",
		"message_id", msg.ID,
		"operation", operation)

	editReq, err := payloadToFileEditRequest(msg)
	if err != nil {
		fs.logger.Error("Failed to create file edit request", slog.String("message_id", msg.ID), logging.ErrorKind(err))
		return "", err
	}
	target, err := fs.fileEdit.OpenFileEditTarget(ctx, editReq.FilePath)
	if err != nil {
		fs.logger.Error("File edit path rejected", slog.String("message_id", msg.ID), logging.ErrorKind(err))
		protoResult := &operatorv1.FileEditResult{
			ExecutionId:  editReq.ExecutionID,
			Status:       operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FilePath:     editReq.FilePath,
			Operation:    string(editReq.Operation),
			ErrorMessage: err.Error(),
			ErrorType:    "validation_error",
		}
		if publishErr := fs.results.PublishFileEditResult(ctx, protoResult, msg); publishErr != nil {
			fs.logger.Error("Failed to publish rejected file edit result", logging.ErrorKind(publishErr))
			err = errors.Join(err, publishErr)
		}
		return "", err
	}
	defer func() { _ = target.Close() }()
	editReq.FilePath = target.Path()
	filePath := target.Path()

	// Begin ledger two-phase commit (pre-mutation snapshot). The ledger reads
	// through the same rooted target the path policy admitted. Evidence is keyed to
	// the verified command session, not the runtime config: the Gateway embedded
	// Operator serves whichever session claimed it.
	var ledgerResult *storage.LedgerResult
	if operation != "read" {
		var beginErr error
		ledgerResult, beginErr = fs.beginLedgerTwoPhaseCommit(msg.OperatorSessionID, target, operation)
		if beginErr != nil {
			// Fail closed: without a pre-mutation snapshot the edit is never executed.
			fs.logger.Error("File edit refused: ledger snapshot unavailable", slog.String("message_id", msg.ID), logging.ErrorKind(beginErr))
			publishErr := fs.publishLFAAError(ctx, msg, constants.Event.Operator.FileEdit.Failed, beginErr.Error())
			return "", errors.Join(beginErr, publishErr)
		}
	}

	result, err := fs.fileEdit.ExecuteFileEdit(ctx, editReq)
	if err != nil {
		fs.logger.Error("File edit execution failed", slog.String("message_id", msg.ID), logging.ErrorKind(err))
		return "", err
	}
	if result == nil {
		fs.logger.Error("File edit returned no result")
		return "", fmt.Errorf("file edit: %w", constants.ErrInternal)
	}

	execErr := fs.finishLedgerTwoPhaseCommit(msg.OperatorSessionID, ledgerResult, result)

	commandStr := fmt.Sprintf("file_%s: %s", operation, filePath)

	exitCode := 0
	eventType := constants.Event.Operator.FileEdit.Completed
	if result.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		exitCode = 1
		eventType = constants.Event.Operator.FileEdit.Failed
	}

	// The execution event is the local record of this edit: raw output, encrypted at
	// rest, written before the result is published. A failed record marks the result
	// FAILED so an unrecorded mutation is never reported as completed.
	eventID, err := fs.recordFileOpEvent(&storage.Event{
		OperatorSessionID:   msg.OperatorSessionID,
		Timestamp:           time.Now().UTC(),
		Type:                eventType,
		ContentText:         fmt.Sprintf("File %s: %s", operation, filePath),
		CommandRaw:          commandStr,
		CommandExitCode:     exitCode,
		CommandStdout:       result.Content,
		CommandStderr:       result.ErrorMessage,
		ExecutionDurationMs: int64(result.DurationSeconds * 1000),
		ExecutionID:         result.ExecutionID,
	})
	if err != nil {
		fs.logger.Error("File edit event could not be recorded; result marked FAILED", logging.ErrorKind(err))
		execErr = errors.Join(execErr, err)
		result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		result.ErrorMessage = err.Error()
		result.ErrorType = "audit_record_error"
	} else if result.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED && ledgerResult != nil {
		mutation := &storage.FileMutationLog{
			EventID:          eventID,
			Filepath:         filePath,
			Operation:        ledgerResult.Operation,
			LedgerHashBefore: ledgerResult.LedgerHashBefore,
			LedgerHashAfter:  ledgerResult.LedgerHashAfter,
			DiffStat:         ledgerResult.DiffStat,
		}
		if err := fs.auditStore.RecordFileMutation(mutation); err != nil {
			fs.logger.Error("File mutation could not be recorded; result marked FAILED", logging.ErrorKind(err))
			mutationErr := fmt.Errorf("%w: %w", constants.ErrAuditStoreRecordFileMutationFailed, err)
			execErr = errors.Join(execErr, mutationErr)
			result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
			result.ErrorMessage = mutationErr.Error()
			result.ErrorType = "audit_record_error"
		}
	}

	protoResult := &operatorv1.FileEditResult{
		ExecutionId:     result.ExecutionID,
		Status:          result.Status,
		FilePath:        result.FilePath,
		Operation:       string(result.Operation),
		DurationSeconds: float32(result.DurationSeconds),
	}
	if result.ErrorMessage != "" {
		protoResult.ErrorMessage = result.ErrorMessage
	}
	if result.ErrorType != "" {
		protoResult.ErrorType = result.ErrorType
	}
	if result.BytesWritten != 0 {
		protoResult.BytesWritten = result.BytesWritten
	}
	if result.LinesChanged != 0 {
		linesChanged := result.LinesChanged
		if linesChanged > maxInt32Value {
			linesChanged = maxInt32Value
		}
		protoResult.LinesChanged = int32(linesChanged) //nolint:gosec // bounds checked above
	}
	if result.BackupPath != "" {
		protoResult.BackupPath = result.BackupPath
	}
	if result.Content != "" {
		protoResult.Content = result.Content
		protoResult.StdoutSize = int32(len(result.Content)) //nolint:gosec // bounded by pub/sub payload limits
	}
	if result.ErrorMessage != "" && result.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
		protoResult.StderrSize = int32(len(result.ErrorMessage)) //nolint:gosec // bounded by pub/sub payload limits
	}

	// The results publisher egresses the payload and publishes a typed failure when it is withheld.
	publishErr := fs.results.PublishFileEditResult(ctx, protoResult, msg)
	if execErr == nil {
		execErr = executionResultError(result.Status, result.ErrorMessage)
	}
	if err := errors.Join(execErr, publishErr); err != nil {
		return "", fmt.Errorf("file edit: %w", err)
	}
	return commandStr, nil
}

// beginLedgerTwoPhaseCommit starts the pre-mutation snapshot phase of the git ledger
// two-phase commit. It determines whether the operation creates a new file or
// modifies an existing one and calls the appropriate ledger begin method.
// It returns (nil, nil) when the operation needs no snapshot (the edit fails on its own),
// and an error wrapping the ledger error when the snapshot cannot be taken. The caller must
// refuse the edit in that case.
func (fs *FileOpsService) beginLedgerTwoPhaseCommit(operatorSessionID string, target *fsvc.FileEditTarget, operation string) (*storage.LedgerResult, error) {
	info, statErr := target.Stat()
	fileExists := statErr == nil && !info.IsDir()

	var ledgerResult *storage.LedgerResult
	var ledgerErr error

	switch {
	case !fileExists && (operation == "write" || operation == "create"):
		ledgerResult, ledgerErr = fs.ledger.MirrorFileCreate(operatorSessionID, target)
	case fileExists:
		ledgerResult, ledgerErr = fs.ledger.LedgerFileWrite(operatorSessionID, target)
	default:
		// File does not exist and operation is not a creation (e.g., replace/insert/delete
		// on a non-existent file). ExecuteFileEdit will fail; skip the ledger snapshot.
		return nil, nil
	}

	if ledgerErr != nil {
		return nil, fmt.Errorf("file edit: begin ledger two-phase commit: %w", ledgerErr)
	}

	return ledgerResult, nil
}

// finishLedgerTwoPhaseCommit completes the post-mutation snapshot after a successful edit. A failed
// completion fails the result: the edit happened but its snapshot is not recorded, so the result
// is published as a FileEdit failure and the audit trail records it as failed.
func (fs *FileOpsService) finishLedgerTwoPhaseCommit(operatorSessionID string, ledgerResult *storage.LedgerResult, result *models.FileEditResult) error {
	if ledgerResult == nil || result.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return nil
	}
	if err := fs.completeLedgerTwoPhaseCommit(operatorSessionID, ledgerResult); err != nil {
		fs.logger.Error("Failed to complete ledger two-phase commit", slog.String("operator_session_id", operatorSessionID), logging.ErrorKind(err))
		wrapped := fmt.Errorf("file edit: complete ledger two-phase commit: %w", err)
		result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		result.ErrorMessage = wrapped.Error()
		return wrapped
	}
	return nil
}

// completeLedgerTwoPhaseCommit completes the post-mutation snapshot phase of the git
// ledger two-phase commit by dispatching to the appropriate CompleteMirror* method
// based on the LedgerResult.Operation. Returns nil if result is nil.
func (fs *FileOpsService) completeLedgerTwoPhaseCommit(operatorSessionID string, result *storage.LedgerResult) error {
	if result == nil {
		return nil
	}
	switch result.Operation {
	case storage.FileMutationCreate:
		return fs.ledger.CompleteMirrorCreate(result, operatorSessionID)
	default:
		return fs.ledger.CompleteMirrorWrite(result, operatorSessionID)
	}
}

// HandleFsListRequest processes an inbound filesystem list request.
func (fs *FileOpsService) HandleFsListRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var protoList operatorv1.FsListRequested
	if err := proto.Unmarshal(msg.Payload, &protoList); err != nil {
		fs.logger.Error("Failed to decode fs list payload as protobuf FsListRequested", "error", err)
		return "", err
	}

	path := protoList.Path
	if path == "" {
		path = "."
	}

	fs.logger.Info("File system list requested (via Protobuf)", "message_id", msg.ID)

	fsListReq, err := payloadToFsListRequest(msg)
	if err != nil {
		fs.logger.Error("Failed to create fs list request", slog.String("message_id", msg.ID), logging.ErrorKind(err))
		return "", err
	}

	result, err := fs.fsList.ExecuteFsList(ctx, fsListReq)
	execErr := err
	if err != nil {
		result = &models.FsListResult{
			ExecutionID:     fsListReq.ExecutionID,
			CaseID:          fsListReq.CaseID,
			TaskID:          fsListReq.TaskID,
			InvestigationID: fsListReq.InvestigationID,
			Path:            fsListReq.Path,
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			ErrorMessage:    err.Error(),
			ErrorType:       "execution_error",
		}
	}

	listCommand := fmt.Sprintf("fs_list: %s", path)
	listEventType := constants.Event.Operator.FsList.Completed
	listExitCode := 0
	if result.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		listEventType = constants.Event.Operator.FsList.Failed
		listExitCode = 1
	}
	var listStdout string
	if result.Entries != nil {
		if entriesJSON, jsonErr := json.Marshal(result.Entries); jsonErr == nil {
			listStdout = string(entriesJSON)
		}
	}
	if _, err := fs.recordFileOpEvent(&storage.Event{
		OperatorSessionID:   msg.OperatorSessionID,
		Timestamp:           time.Now().UTC(),
		Type:                listEventType,
		ContentText:         listCommand,
		CommandRaw:          listCommand,
		CommandExitCode:     listExitCode,
		CommandStdout:       listStdout,
		CommandStderr:       result.ErrorMessage,
		ExecutionDurationMs: int64(result.DurationSeconds * 1000),
		ExecutionID:         result.ExecutionID,
	}); err != nil {
		fs.logger.Error("FS list event could not be recorded; result marked FAILED", logging.ErrorKind(err))
		execErr = errors.Join(execErr, err)
		result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		result.ErrorMessage = err.Error()
		result.ErrorType = "audit_record_error"
	} else {
		fs.logger.Info("FS list event recorded",
			"execution_id", result.ExecutionID,
			"entries", result.TotalCount)
	}

	totalCount := result.TotalCount
	if totalCount > maxInt32Value {
		totalCount = maxInt32Value
	}
	protoResult := &operatorv1.FsListResult{
		ExecutionId:     result.ExecutionID,
		Status:          result.Status,
		Path:            result.Path,
		Truncated:       result.Truncated,
		TotalCount:      int32(totalCount), //nolint:gosec // bounds checked above
		DurationSeconds: float32(result.DurationSeconds),
	}
	if result.ErrorMessage != "" {
		protoResult.ErrorMessage = result.ErrorMessage
	}
	if result.ErrorType != "" {
		protoResult.ErrorType = result.ErrorType
	}
	if result.Entries != nil {
		protoResult.Entries = make([]*operatorv1.FsEntry, len(result.Entries))
		for i, entry := range result.Entries {
			protoEntry := &operatorv1.FsEntry{
				Name:    entry.Name,
				Path:    entry.Path,
				IsDir:   entry.IsDir,
				Size:    entry.Size,
				ModTime: entry.ModTime,
			}
			// Map Mode from string to int32 if available
			if entry.Mode != "" {
				// Parse the mode string (e.g., "-rw-r--r--" or "0755")
				// For now, set to 0 if parsing fails
				protoEntry.Mode = 0
			}
			protoResult.Entries[i] = protoEntry
		}
	}

	// The results publisher egresses the payload and publishes a typed failure when it is withheld.
	publishErr := fs.results.PublishFsListResult(ctx, protoResult, msg)
	if err := errors.Join(execErr, executionResultError(result.Status, result.ErrorMessage), publishErr); err != nil {
		return "", fmt.Errorf("FsList: %w", err)
	}
	return "", nil
}

// HandleFsGrepRequest processes an inbound filesystem grep request.
func (fs *FileOpsService) HandleFsGrepRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var protoGrep operatorv1.FsGrepRequested
	if err := proto.Unmarshal(msg.Payload, &protoGrep); err != nil {
		fs.logger.Error("Failed to decode fs grep payload as protobuf FsGrepRequested", "error", err)
		return "", err
	}

	path := protoGrep.Path
	if path == "" {
		path = "."
	}

	fs.logger.Info("File system grep requested (via Protobuf)", "message_id", msg.ID)

	fsGrepReq, err := payloadToFsGrepRequest(msg)
	if err != nil {
		fs.logger.Error("Failed to create fs grep request", slog.String("message_id", msg.ID), logging.ErrorKind(err))
		return "", err
	}

	result, err := fs.fsGrep.ExecuteFsGrep(ctx, fsGrepReq)
	execErr := err
	if err != nil {
		result = &models.FsGrepResult{
			ExecutionID:     fsGrepReq.ExecutionID,
			CaseID:          fsGrepReq.CaseID,
			TaskID:          fsGrepReq.TaskID,
			InvestigationID: fsGrepReq.InvestigationID,
			Path:            fsGrepReq.Path,
			Pattern:         fsGrepReq.Pattern,
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			ErrorMessage:    err.Error(),
			ErrorType:       "execution_error",
		}
	}

	grepCommand := fmt.Sprintf("fs_grep: %s (pattern: %s)", path, protoGrep.Pattern)
	grepEventType := constants.Event.Operator.FsGrep.Completed
	grepExitCode := 0
	if result.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		grepEventType = constants.Event.Operator.FsGrep.Failed
		grepExitCode = 1
	}
	var grepStdout string
	if result.Matches != nil {
		if matchesJSON, jsonErr := json.Marshal(result.Matches); jsonErr == nil {
			grepStdout = string(matchesJSON)
		}
	}
	if _, err := fs.recordFileOpEvent(&storage.Event{
		OperatorSessionID:   msg.OperatorSessionID,
		Timestamp:           time.Now().UTC(),
		Type:                grepEventType,
		ContentText:         grepCommand,
		CommandRaw:          grepCommand,
		CommandExitCode:     grepExitCode,
		CommandStdout:       grepStdout,
		CommandStderr:       result.ErrorMessage,
		ExecutionDurationMs: int64(result.DurationSeconds * 1000),
		ExecutionID:         result.ExecutionID,
	}); err != nil {
		fs.logger.Error("FS grep event could not be recorded; result marked FAILED", logging.ErrorKind(err))
		execErr = errors.Join(execErr, err)
		result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		result.ErrorMessage = err.Error()
		result.ErrorType = "audit_record_error"
	} else {
		fs.logger.Info("FS grep event recorded",
			"execution_id", result.ExecutionID,
			"matches", result.TotalMatches)
	}

	totalMatches := result.TotalMatches
	if totalMatches > maxInt32Value {
		totalMatches = maxInt32Value
	}
	protoResult := &operatorv1.FsGrepResult{
		ExecutionId:     result.ExecutionID,
		Status:          result.Status,
		Path:            result.Path,
		TotalMatches:    int32(totalMatches), //nolint:gosec // bounds checked above
		Truncated:       result.Truncated,
		DurationSeconds: float32(result.DurationSeconds),
	}
	if result.ErrorMessage != "" {
		protoResult.ErrorMessage = result.ErrorMessage
	}
	if result.ErrorType != "" {
		protoResult.ErrorType = result.ErrorType
	}
	if result.Matches != nil {
		protoResult.Matches = make([]*operatorv1.FsGrepMatch, len(result.Matches))
		for i, match := range result.Matches {
			protoResult.Matches[i] = &operatorv1.FsGrepMatch{
				Path:       match.Path,
				LineNumber: int32(match.LineNumber), //nolint:gosec // line numbers bounded by file size
				Content:    match.Content,
				Before:     match.Before,
				After:      match.After,
			}
		}
	}

	// The results publisher egresses the payload and publishes a typed failure when it is withheld.
	publishErr := fs.results.PublishFsGrepResult(ctx, protoResult, msg)
	if err := errors.Join(execErr, executionResultError(result.Status, result.ErrorMessage), publishErr); err != nil {
		return "", fmt.Errorf("FsGrep: %w", err)
	}
	return "", nil
}

// HandleFsReadRequest processes an inbound filesystem read request. Every outcome,
// including a rejected request, is recorded as a raw execution event before the result is
// published, and a failed record marks the result FAILED without its content.
func (fs *FileOpsService) HandleFsReadRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	fs.logger.Info("File system read requested (via Protobuf)", "message_id", msg.ID)
	start := time.Now()
	result, readErr := fs.executeFsRead(ctx, msg)
	result.ExecutionId = executionIDFromMessage(msg)
	result.DurationSeconds = float32(time.Since(start).Seconds())

	readCommand := fmt.Sprintf("fs_read: %s", result.Path)
	eventType := constants.Event.Operator.FsRead.Completed
	exitCode := 0
	if result.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		eventType = constants.Event.Operator.FsRead.Failed
		exitCode = 1
	}
	if _, err := fs.recordFileOpEvent(&storage.Event{
		OperatorSessionID:   msg.OperatorSessionID,
		Timestamp:           time.Now().UTC(),
		Type:                eventType,
		ContentText:         readCommand,
		CommandRaw:          readCommand,
		CommandExitCode:     exitCode,
		CommandStdout:       result.Content,
		CommandStderr:       result.ErrorMessage,
		ExecutionDurationMs: int64(result.DurationSeconds * 1000),
		ExecutionID:         result.ExecutionId,
	}); err != nil {
		fs.logger.Error("FS read event could not be recorded; result marked FAILED", logging.ErrorKind(err))
		readErr = errors.Join(readErr, err)
		eventType = constants.Event.Operator.FsRead.Failed
		result.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		result.Content = ""
		result.SizeBytes = 0
		result.Truncated = false
		result.ErrorMessage = err.Error()
		result.ErrorType = "audit_record_error"
	}
	publishErr := fs.publishLFAATypedResponse(ctx, msg, eventType, result)
	if err := errors.Join(readErr, publishErr); err != nil {
		fs.logger.Warn("File system read failed", "message_id", msg.ID, "execution_id", result.ExecutionId, "error_type", result.ErrorType, logging.ErrorKind(err))
		return "", fmt.Errorf("fs read: %w", err)
	}
	return readCommand, nil
}

// executeFsRead reads the requested file and returns the result without an execution ID or
// duration; HandleFsReadRequest stamps both and records the event.
func (fs *FileOpsService) executeFsRead(ctx context.Context, msg *PubSubCommandMessage) (*operatorv1.FsReadResult, error) {
	failed := func(path, errorType string, err error) (*operatorv1.FsReadResult, error) {
		return &operatorv1.FsReadResult{
			Path:         path,
			Status:       operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			ErrorMessage: err.Error(),
			ErrorType:    errorType,
		}, err
	}

	var protoRead operatorv1.FsReadRequested
	if err := proto.Unmarshal(msg.Payload, &protoRead); err != nil {
		fs.logger.Error("Failed to decode fs read payload as protobuf FsReadRequested", "error", err)
		return failed("", "invalid_request", fmt.Errorf("decode FsReadRequested: %w", err))
	}
	if protoRead.Path == "" {
		fs.logger.Warn("Fs read request without path")
		return failed("", "invalid_request", fmt.Errorf("missing path in request: %w", constants.ErrMissingRequiredField))
	}

	maxSize := protoRead.MaxSize
	if maxSize <= 0 {
		maxSize = defaultMaxReadSize
	}

	// SECURITY: The path must pass the same runtime path policy as FILE_EDIT
	// (protected .ztd state is refused) and is opened through a rooted target
	// so a symlink swap cannot escape the admitted directory.
	target, err := fs.fileEdit.OpenFileEditTarget(ctx, protoRead.Path)
	if err != nil {
		return failed(protoRead.Path, "read_error", err)
	}
	defer func() { _ = target.Close() }()

	file, err := target.Open()
	if err != nil {
		return failed(protoRead.Path, "read_error", err)
	}
	defer func() { _ = file.Close() }()

	fileInfo, err := file.Stat()
	if err != nil {
		return failed(protoRead.Path, "read_error", err)
	}

	// SECURITY: io.LimitReader bounds memory when reading a large file.
	readLimit := int64(maxSize)
	data, err := io.ReadAll(io.LimitReader(file, readLimit))
	if err != nil {
		return failed(protoRead.Path, "read_error", err)
	}

	return &operatorv1.FsReadResult{
		Path:      protoRead.Path,
		Status:    operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		Content:   string(data),
		SizeBytes: int64(len(data)),
		Truncated: fileInfo.Size() > readLimit,
	}, nil
}

func (fs *FileOpsService) publishLFAATypedResponse(ctx context.Context, msg *PubSubCommandMessage, eventType constants.EventType, payload proto.Message) error {
	return publishLFAATypedResponseTo(ctx, fs.client, fs.config, fs.logger, msg, eventType, payload, fs.auditStore, fs.scrubbing)
}

// recordFileOpEvent writes a file operation's execution event to the audit store.
func (fs *FileOpsService) recordFileOpEvent(event *storage.Event) (int64, error) {
	id, err := fs.auditStore.RecordEvent(event)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", constants.ErrExecutionRecordFailed, err)
	}
	return id, nil
}

func (fs *FileOpsService) publishLFAAError(ctx context.Context, msg *PubSubCommandMessage, eventType constants.EventType, errorMsg string) error {
	return publishLFAAErrorTo(ctx, fs.client, fs.config, fs.logger, msg, eventType, errorMsg, fs.auditStore, fs.scrubbing)
}

// payloadToFileEditRequest is a package-level helper shared by FileOpsService and tests.
func payloadToFileEditRequest(msg *PubSubCommandMessage) (*models.FileEditRequest, error) {
	var p operatorv1.FileEditRequested
	if err := proto.Unmarshal(msg.Payload, &p); err != nil {
		return nil, fmt.Errorf("failed to decode file edit payload as protobuf FileEditRequested: %w", err)
	}
	if p.FilePath == "" {
		return nil, fmt.Errorf("missing file_path in payload")
	}
	if p.Operation == "" {
		return nil, fmt.Errorf("missing operation in payload")
	}

	requestID := executionIDFromMessage(msg)
	if p.ExecutionId != "" {
		requestID = p.ExecutionId
	}

	justification := p.Justification
	if justification == "" {
		justification = "pub/sub command request"
	}

	operation := constants.FileOperation(p.Operation)
	if !isValidFileOperation(operation) {
		return nil, fmt.Errorf("invalid file operation: %s", p.Operation)
	}

	req := &models.FileEditRequest{
		ExecutionID: requestID,
		CaseID:      msg.CaseID,
		TaskID: func() string {
			if msg.TaskID != nil {
				return *msg.TaskID
			}
			return ""
		}(),
		InvestigationID: msg.InvestigationID,
		Operation:       operation,
		FilePath:        p.FilePath,
		RequestedBy:     "ztd-system",
		Justification:   justification,
		CreateBackup:    p.CreateBackup,
		CreateIfMissing: p.CreateIfMissing,
	}

	if p.Content != "" {
		req.Content = p.Content
	}
	if p.OldContent != "" {
		req.OldContent = p.OldContent
	}
	if p.NewContent != "" {
		req.NewContent = p.NewContent
	}
	if p.InsertContent != "" {
		req.InsertContent = p.InsertContent
	}
	if p.InsertPosition != 0 {
		req.InsertPosition = int(p.InsertPosition)
	}
	if p.StartLine != 0 {
		req.StartLine = int(p.StartLine)
	}
	if p.EndLine != 0 {
		req.EndLine = int(p.EndLine)
	}
	if p.PatchContent != "" {
		req.PatchContent = p.PatchContent
	}

	return req, nil
}

// payloadToFsListRequest is a package-level helper shared by FileOpsService and tests.
func payloadToFsListRequest(msg *PubSubCommandMessage) (*models.FsListRequest, error) {
	var p operatorv1.FsListRequested
	if err := proto.Unmarshal(msg.Payload, &p); err != nil {
		return nil, fmt.Errorf("failed to decode fs list payload as protobuf FsListRequested: %w", err)
	}

	path := p.Path
	if path == "" {
		path = "."
	}

	requestID := executionIDFromMessage(msg)
	if p.ExecutionId != "" {
		requestID = p.ExecutionId
	}

	maxEntries := p.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}

	return &models.FsListRequest{
		ExecutionID: requestID,
		CaseID:      msg.CaseID,
		TaskID: func() string {
			if msg.TaskID != nil {
				return *msg.TaskID
			}
			return ""
		}(),
		InvestigationID: msg.InvestigationID,
		Path:            path,
		MaxDepth:        int(p.MaxDepth),
		MaxEntries:      int(maxEntries),
		RequestedBy:     "ztd-system",
	}, nil
}

// payloadToFsGrepRequest is a package-level helper shared by FileOpsService and tests.
func payloadToFsGrepRequest(msg *PubSubCommandMessage) (*models.FsGrepRequest, error) {
	var p operatorv1.FsGrepRequested
	if err := proto.Unmarshal(msg.Payload, &p); err != nil {
		return nil, fmt.Errorf("failed to decode fs grep payload as protobuf FsGrepRequested: %w", err)
	}

	path := p.Path
	if path == "" {
		path = "."
	}

	requestID := executionIDFromMessage(msg)
	if p.ExecutionId != "" {
		requestID = p.ExecutionId
	}

	maxMatches := p.MaxMatches
	if maxMatches <= 0 {
		maxMatches = defaultMaxMatches
	}

	return &models.FsGrepRequest{
		ExecutionID: requestID,
		CaseID:      msg.CaseID,
		TaskID: func() string {
			if msg.TaskID != nil {
				return *msg.TaskID
			}
			return ""
		}(),
		InvestigationID: msg.InvestigationID,
		Path:            path,
		Pattern:         p.Pattern,
		Includes:        p.Includes,
		MaxMatches:      int(maxMatches),
	}, nil
}

// isValidFileOperation checks if the operation is a valid typed constant.
func isValidFileOperation(op constants.FileOperation) bool {
	switch op {
	case constants.FileOperationCreate,
		constants.FileOperationDelete,
		constants.FileOperationInsert,
		constants.FileOperationPatch,
		constants.FileOperationRead,
		constants.FileOperationReplace,
		constants.FileOperationUpdate,
		constants.FileOperationWrite:
		return true
	default:
		return false
	}
}
