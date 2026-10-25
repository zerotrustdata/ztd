// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"context"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/timesvc"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// eventsStoreInterface defines the methods HistoryHandler needs from the events store.
// This allows for dependency injection and unit testing with mocks.
type eventsStoreInterface interface {
	GetOperatorSession(sessionID string) (*OperatorSession, error)
	GetEvents(scope models.EventsScope, limit, offset int) ([]*Event, error)
	GetFileMutations(eventID int64) ([]*FileMutationLog, error)
	GetEventByExecutionID(executionID string) (*Event, error)
	GetFileMutationByID(id int64) (*FileMutationLog, error)
	ListFileMutationsBySession(operatorSessionID string, limit int) ([]*FileMutationLog, error)
}

// ledgerInterface defines the methods HistoryHandler needs from the ledger service.
// This allows for dependency injection and unit testing with mocks.
// It also includes two-phase commit methods needed for integration test setup.
type ledgerInterface interface {
	GetFileHistory(filePath string, limit int, sessionID string) ([]FileHistoryEntry, error)
	RestoreFileFromCommit(target *fs.FileEditTarget, commitHash, sourceSessionID, operatorSessionID string) (*LedgerResult, error)
	GetFileAtCommit(filePath, commitHash, sessionID string) (string, error)
	GetDiffContent(hashBefore, hashAfter, filePath, sessionID string) (string, error)
	MirrorFileCreate(operatorSessionID string, target *fs.FileEditTarget) (*LedgerResult, error)
	CompleteMirrorCreate(result *LedgerResult, operatorSessionID string) error
	LedgerFileWrite(operatorSessionID string, target *fs.FileEditTarget) (*LedgerResult, error)
	CompleteMirrorWrite(result *LedgerResult, operatorSessionID string) error
}

// loggerInterface defines the logging methods used by HistoryHandler.
type loggerInterface interface {
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
}

type HistoryHandler struct {
	eventsStore eventsStoreInterface
	ledger      ledgerInterface
	logger      loggerInterface
	fileSvc     fs.RuntimeFileService
	workDir     string
}

// NewHistoryHandler wires the history handler. fileSvc and workDir apply the
// governed FILE_EDIT path policy to RESTORE_FILE destinations. The ledger is
// required: file history, diff, and restore are served only from it.
func NewHistoryHandler(eventsStore eventsStoreInterface, ledger ledgerInterface, logger loggerInterface, fileSvc fs.RuntimeFileService, workDir string) (*HistoryHandler, error) {
	if ledger == nil {
		return nil, fmt.Errorf("history handler: %w", constants.ErrLedgerRequired)
	}
	return &HistoryHandler{
		eventsStore: eventsStore,
		ledger:      ledger,
		logger:      logger,
		fileSvc:     fileSvc,
		workDir:     workDir,
	}, nil
}

func (hh *HistoryHandler) HandleFetchHistory(requestJSON []byte) (*operatorv1.FetchHistoryResult, error) {
	var request operatorv1.FetchHistoryRequested
	if err := proto.Unmarshal(requestJSON, &request); err != nil {
		return hh.fetchHistoryError(fmt.Errorf("invalid request format as protobuf FetchHistoryRequested: %w", err))
	}

	if request.OperatorSessionId == "" {
		return hh.fetchHistoryError(fmt.Errorf("operator_session_id is required: %w", constants.ErrMissingRequiredField))
	}

	limit := int(request.Limit)
	if limit <= 0 {
		limit = 50
	}

	offset := int(request.Offset)

	hh.logger.Info("Handling FETCH_HISTORY request (via Protobuf)",
		"operator_session_id", request.OperatorSessionId,
		"limit", limit,
		"offset", offset)

	session, err := hh.eventsStore.GetOperatorSession(request.OperatorSessionId)
	if err != nil {
		return hh.fetchHistoryError(fmt.Errorf("failed to get session: %w", err))
	}

	events, err := hh.eventsStore.GetEvents(models.EventsScope{OperatorSessionID: request.OperatorSessionId}, limit, offset)
	if err != nil {
		return hh.fetchHistoryError(fmt.Errorf("failed to get events: %w", err))
	}

	result := &operatorv1.FetchHistoryResult{
		Success:           true,
		OperatorSessionId: request.OperatorSessionId,
		Events:            make([]*operatorv1.AuditEvent, 0, len(events)),
		Limit:             int32(limit),  //nolint:gosec // bounded by query parameters
		Offset:            int32(offset), //nolint:gosec // bounded by query parameters
	}

	if session != nil {
		result.WebSession = &operatorv1.AuditWebSession{
			Id:           session.ID,
			Title:        session.Title,
			CreatedAt:    timesvc.FormatTimestamp(session.CreatedAt),
			UserIdentity: session.UserIdentity,
		}
	}

	for _, event := range events {
		auditEvent := &operatorv1.AuditEvent{
			Id:                  event.ID,
			OperatorSessionId:   event.OperatorSessionID,
			Timestamp:           timesvc.FormatTimestamp(event.Timestamp),
			Type:                string(event.Type),
			ContentText:         event.ContentText,
			CommandRaw:          event.CommandRaw,
			CommandExitCode:     0, // Will be set below if not nil
			CommandStdout:       event.CommandStdout,
			CommandStderr:       event.CommandStderr,
			ExecutionDurationMs: event.ExecutionDurationMs,
			StoredLocally:       event.StoredLocally,
			FileMutations:       []*operatorv1.AuditFileMutation{},
		}
		if event.CommandExitCode != constants.ExitCodeNone {
			auditEvent.CommandExitCode = int32(event.CommandExitCode) //nolint:gosec // exit codes are 0-255
		}

		if event.Type == constants.Event.Operator.FileEdit.Completed {
			mutations, err := hh.eventsStore.GetFileMutations(event.ID)
			if err != nil {
				hh.logger.Warn("Failed to get file mutations", "event_id", event.ID, string(constants.ConnectionStateError), err)
			} else {
				for _, m := range mutations {
					auditEvent.FileMutations = append(auditEvent.FileMutations, &operatorv1.AuditFileMutation{
						Id:               m.ID,
						Filepath:         m.Filepath,
						Operation:        string(m.Operation),
						LedgerHashBefore: m.LedgerHashBefore,
						LedgerHashAfter:  m.LedgerHashAfter,
						DiffStat:         m.DiffStat,
					})
				}
			}
		}

		result.Events = append(result.Events, auditEvent)
	}

	result.Total = int32(len(result.Events)) //nolint:gosec // bounded by query results
	return result, nil
}

func (hh *HistoryHandler) HandleFetchFileHistory(requestJSON []byte) (*operatorv1.FetchFileHistoryResult, error) {
	var request operatorv1.FetchFileHistoryRequested
	if err := proto.Unmarshal(requestJSON, &request); err != nil {
		return hh.fetchFileHistoryError(fmt.Errorf("invalid request format as protobuf FetchFileHistoryRequested: %w", err))
	}

	if request.FilePath == "" {
		return hh.fetchFileHistoryError(fmt.Errorf("file_path is required: %w", constants.ErrMissingRequiredField))
	}

	limit := int(request.Limit)
	if limit <= 0 {
		limit = 50
	}

	hh.logger.Info("Handling FETCH_FILE_HISTORY request (via Protobuf)",
		"limit", limit,
		"operator_session_id", request.OperatorSessionId)

	history, err := hh.ledger.GetFileHistory(request.FilePath, limit, request.OperatorSessionId)
	if err != nil {
		return hh.fetchFileHistoryError(fmt.Errorf("failed to get file history: %w", err))
	}

	result := &operatorv1.FetchFileHistoryResult{
		Success:  true,
		FilePath: request.FilePath,
		History:  make([]*operatorv1.FileHistoryEntry, 0, len(history)),
	}

	for _, entry := range history {
		result.History = append(result.History, &operatorv1.FileHistoryEntry{
			CommitHash: entry.CommitHash,
			Timestamp:  timesvc.FormatTimestamp(entry.Timestamp),
			Message:    entry.Message,
		})
	}

	return result, nil
}

// HandleRestoreFile restores a file to its content at a commit of the request's
// operator_session_id ledger. The restore is a file write by operatorSessionID, the verified
// executing session, and is mirrored through that session's ledger. A successful restore returns
// the ledger result for the file mutation record; failures return both the typed result and the underlying error.
func (hh *HistoryHandler) HandleRestoreFile(ctx context.Context, operatorSessionID string, requestJSON []byte) (*operatorv1.RestoreFileResult, *LedgerResult, error) {
	var request operatorv1.RestoreFileRequested
	if err := proto.Unmarshal(requestJSON, &request); err != nil {
		return hh.restoreFileError(fmt.Errorf("invalid request format as protobuf RestoreFileRequested: %w", err))
	}

	if request.FilePath == "" {
		return hh.restoreFileError(fmt.Errorf("file_path is required: %w", constants.ErrMissingRequiredField))
	}
	if request.CommitHash == "" {
		return hh.restoreFileError(fmt.Errorf("commit_hash is required: %w", constants.ErrMissingRequiredField))
	}
	if request.OperatorSessionId == "" {
		return hh.restoreFileError(fmt.Errorf("operator_session_id is required: %w", constants.ErrMissingRequiredField))
	}
	if operatorSessionID == "" {
		return hh.restoreFileError(fmt.Errorf("failed to restore file: executing session: %w", constants.ErrMissingRequiredField))
	}

	hh.logger.Info("Handling RESTORE_FILE request (via Protobuf)",
		"commit_hash", request.CommitHash)

	if hh.fileSvc == nil {
		return hh.restoreFileError(fmt.Errorf("failed to restore file: file service: %w", constants.ErrMissingRequiredField))
	}
	// A restore is a governed file mutation: the destination passes the same
	// runtime path policy and rooted handle as FILE_EDIT.
	target, err := hh.fileSvc.OpenFileEditTarget(ctx, request.FilePath, hh.workDir)
	if err != nil {
		return hh.restoreFileError(fmt.Errorf("failed to restore file: %w", err))
	}
	defer func() { _ = target.Close() }()

	ledgerResult, err := hh.ledger.RestoreFileFromCommit(target, request.CommitHash, request.OperatorSessionId, operatorSessionID)
	if err != nil {
		return hh.restoreFileError(fmt.Errorf("failed to restore file: %w", err))
	}

	return &operatorv1.RestoreFileResult{
		Success:    true,
		FilePath:   target.Path(),
		CommitHash: request.CommitHash,
	}, ledgerResult, nil
}

func (hh *HistoryHandler) GetFileAtCommit(filePath, commitHash, operatorSessionID string) (string, error) {
	return hh.ledger.GetFileAtCommit(filePath, commitHash, operatorSessionID)
}

// FetchExecutionLogs returns the execution event recorded for an execution ID, raw and
// decrypted. events is the sole execution record. The results publisher scrubs it on egress.
func (hh *HistoryHandler) FetchExecutionLogs(executionID string) (*operatorv1.FetchLogsResult, error) {
	event, err := hh.eventsStore.GetEventByExecutionID(executionID)
	if err != nil {
		return nil, fmt.Errorf("fetch logs: %w", err)
	}
	var returnCode int32
	if event.CommandExitCode != constants.ExitCodeNone {
		returnCode = int32(event.CommandExitCode) //nolint:gosec // exit codes are 0-255
	}
	return &operatorv1.FetchLogsResult{
		ExecutionId: executionID,
		Command:     event.CommandRaw,
		ReturnCode:  returnCode,
		DurationMs:  event.ExecutionDurationMs,
		Stdout:      event.CommandStdout,
		Stderr:      event.CommandStderr,
		StdoutSize:  int32(len(event.CommandStdout)), //nolint:gosec // bounded by execution output size
		StderrSize:  int32(len(event.CommandStderr)), //nolint:gosec // bounded by execution output size
		Timestamp:   timesvc.FormatTimestamp(event.Timestamp),
	}, nil
}

// FetchFileDiff resolves one file mutation row and computes its diff from the session
// ledger, in memory. The diff is returned raw; the results publisher scrubs it on egress.
func (hh *HistoryHandler) FetchFileDiff(mutationID int64) (*operatorv1.FileDiffEntry, error) {
	mutation, err := hh.eventsStore.GetFileMutationByID(mutationID)
	if err != nil {
		return nil, fmt.Errorf("fetch file diff: %w", err)
	}
	return hh.fileDiffEntry(mutation, true)
}

// ListFileDiffs returns the newest file mutations of an operator session, optionally
// filtered to one file path. The entries carry the diff size but not the diff content.
func (hh *HistoryHandler) ListFileDiffs(operatorSessionID, filePath string, limit int) ([]*operatorv1.FileDiffEntry, error) {
	mutations, err := hh.eventsStore.ListFileMutationsBySession(operatorSessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list file diffs: %w", err)
	}
	entries := make([]*operatorv1.FileDiffEntry, 0, len(mutations))
	for _, mutation := range mutations {
		if filePath != "" && mutation.Filepath != filePath {
			continue
		}
		entry, err := hh.fileDiffEntry(mutation, false)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// fileDiffEntry builds the wire entry for one mutation. It fails closed: an
// unreadable diff is an error, never an empty diff.
func (hh *HistoryHandler) fileDiffEntry(mutation *FileMutationLog, withContent bool) (*operatorv1.FileDiffEntry, error) {
	content, err := hh.ledger.GetDiffContent(mutation.LedgerHashBefore, mutation.LedgerHashAfter, mutation.Filepath, mutation.OperatorSessionID)
	if err != nil {
		return nil, fmt.Errorf("fetch file diff: %w", err)
	}
	entry := &operatorv1.FileDiffEntry{
		Id:                strconv.FormatInt(mutation.ID, 10),
		Timestamp:         timesvc.FormatTimestamp(mutation.Timestamp),
		FilePath:          mutation.Filepath,
		Operation:         string(mutation.Operation),
		LedgerHashBefore:  mutation.LedgerHashBefore,
		LedgerHashAfter:   mutation.LedgerHashAfter,
		DiffStat:          mutation.DiffStat,
		DiffSize:          int32(len(content)), //nolint:gosec // bounded by file size
		OperatorSessionId: mutation.OperatorSessionID,
	}
	if withContent {
		entry.DiffContent = content
	}
	return entry, nil
}

func (hh *HistoryHandler) fetchHistoryError(err error) (*operatorv1.FetchHistoryResult, error) {
	return &operatorv1.FetchHistoryResult{Success: false, Error: err.Error()}, err
}

func (hh *HistoryHandler) fetchFileHistoryError(err error) (*operatorv1.FetchFileHistoryResult, error) {
	return &operatorv1.FetchFileHistoryResult{Success: false, Error: err.Error()}, err
}

func (hh *HistoryHandler) restoreFileError(err error) (*operatorv1.RestoreFileResult, *LedgerResult, error) {
	return &operatorv1.RestoreFileResult{Success: false, Error: err.Error()}, nil, err
}
