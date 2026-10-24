// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	execution "github.com/zerotrustdata/ztd/internal/services/execution"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type fileEditResultsPublisher struct {
	mockResultsPublisher
	results []proto.Message
}

func (m *fileEditResultsPublisher) PublishFileEditResult(_ context.Context, result proto.Message, _ *PubSubCommandMessage) error {
	m.results = append(m.results, result)
	return nil
}

func (m *fileEditResultsPublisher) PublishEvaluationWorkspaceSeedResult(_ context.Context, result *operatorv1.EvaluationWorkspaceSeedResult, _ *PubSubCommandMessage) error {
	m.results = append(m.results, result)
	return nil
}

func newFileEditTestService(t *testing.T, cfg *config.Config, logger *slog.Logger) *execution.FileEditService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(cfg.WorkDir, logger)
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	editSvc, err := execution.NewFileEditService(cfg, logger, fileSvc)
	require.NoError(t, err)
	return editSvc
}

// fileOpsTestDeps overrides the default collaborators newFileOpsServiceWithDepsForTest builds.
// A nil field takes the default: a real file ledger over an unlocked test vault, a
// mockResultsPublisher, and a real local-first events store. The scrubber is always an active one.
type fileOpsTestDeps struct {
	Ledger      *storage.GitLedgerService
	Results     ResultsPublisher
	EventsStore *storage.LocalFirstEventsStore
}

// newFileOpsServiceWithDepsForTest builds a FileOpsService with every required dependency real
// except the results publisher, which reports to a mock unless deps supplies one.
func newFileOpsServiceWithDepsForTest(t *testing.T, cfg *config.Config, logger *slog.Logger, fileEditSvc *execution.FileEditService, client PubSubClient, deps fileOpsTestDeps) *FileOpsService {
	t.Helper()
	if deps.Ledger == nil {
		deps.Ledger = storagetest.NewTestLedger(t)
	}
	if deps.Results == nil {
		deps.Results = &mockResultsPublisher{}
	}
	if deps.EventsStore == nil {
		deps.EventsStore = storagetest.NewTestEventsStore(t, testOperatorSessionID)
	}
	svc, err := NewFileOpsService(cfg, logger, fileEditSvc, client, deps.Ledger, deps.Results, mustNewScrubbingSvc(t, logger), deps.EventsStore)
	require.NoError(t, err)
	return svc
}

// newFileOpsServiceForTest builds a FileOpsService over the default real dependencies.
func newFileOpsServiceForTest(t *testing.T, cfg *config.Config, logger *slog.Logger, fileEditSvc *execution.FileEditService, client PubSubClient) *FileOpsService {
	t.Helper()
	return newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{})
}

// Every dependency is required: a FileOpsService missing one could mutate files without a
// pre-mutation snapshot, publish unscrubbed, or run unrecorded, so construction fails closed
// with the typed sentinel.
func TestNewFileOpsService_RequiresEveryDependency(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	ledger := storagetest.NewTestLedger(t)
	results := &mockResultsPublisher{}
	scrubber := mustNewScrubbingSvc(t, logger)
	eventsStore := storagetest.NewTestEventsStore(t)

	svc, err := NewFileOpsService(cfg, logger, fileEditSvc, client, ledger, results, scrubber, eventsStore)
	require.NoError(t, err)
	require.NotNil(t, svc)
	assert.Same(t, ledger, svc.ledger)
	assert.Same(t, scrubber, svc.scrubbing)
	assert.Same(t, eventsStore, svc.eventsStore)

	tests := []struct {
		name        string
		cfg         *config.Config
		fileEdit    *execution.FileEditService
		ledger      *storage.GitLedgerService
		results     ResultsPublisher
		scrubber    *scrubbing.ScrubbingService
		eventsStore *storage.LocalFirstEventsStore
		wantErr     error
	}{
		{name: "nil config", fileEdit: fileEditSvc, ledger: ledger, results: results, scrubber: scrubber, eventsStore: eventsStore, wantErr: constants.ErrMissingRequiredField},
		{name: "nil file edit service", cfg: cfg, ledger: ledger, results: results, scrubber: scrubber, eventsStore: eventsStore, wantErr: constants.ErrMissingRequiredField},
		{name: "nil ledger", cfg: cfg, fileEdit: fileEditSvc, results: results, scrubber: scrubber, eventsStore: eventsStore, wantErr: constants.ErrLedgerRequired},
		{name: "nil results publisher", cfg: cfg, fileEdit: fileEditSvc, ledger: ledger, scrubber: scrubber, eventsStore: eventsStore, wantErr: constants.ErrPubSubResultsPublisher},
		{name: "nil scrubber", cfg: cfg, fileEdit: fileEditSvc, ledger: ledger, results: results, eventsStore: eventsStore, wantErr: constants.ErrEgressScrubberUnavailable},
		{name: "disabled scrubber", cfg: cfg, fileEdit: fileEditSvc, ledger: ledger, results: results, scrubber: newEgressScrubber(t, false), eventsStore: eventsStore, wantErr: constants.ErrEgressScrubberUnavailable},
		{name: "nil local-first events store", cfg: cfg, fileEdit: fileEditSvc, ledger: ledger, results: results, scrubber: scrubber, wantErr: constants.ErrLocalFirstEventsStoreRequired},
	}
	for _, tt := range tests {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			t.Parallel()
			svc, err := NewFileOpsService(tt.cfg, logger, tt.fileEdit, client, tt.ledger, tt.results, tt.scrubber, tt.eventsStore)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, svc)
		})
	}
}

func TestPayloadToFileEditRequest(t *testing.T) {
	t.Run("converts valid payload", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FileEditRequested{
			FilePath:        filepath.Join(tmpDir, "test.txt"),
			Operation:       "write",
			ExecutionId:     "exec-1",
			Justification:   "test",
			CreateBackup:    true,
			CreateIfMissing: true,
			Content:         "test content",
			OldContent:      "old content",
			NewContent:      "new content",
			InsertContent:   "insert content",
			InsertPosition:  5,
			StartLine:       10,
			EndLine:         20,
		}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:              "msg-1",
			EventType:       constants.Event.Operator.FileEdit.Requested,
			CaseID:          "case-1",
			TaskID:          func() *string { s := "task-1"; return &s }(),
			InvestigationID: "investigation-1",
			Payload:         payload,
		}

		editReq, err := payloadToFileEditRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "exec-1", editReq.ExecutionID)
		assert.Equal(t, "case-1", editReq.CaseID)
		assert.Equal(t, "task-1", editReq.TaskID)
		assert.Equal(t, "investigation-1", editReq.InvestigationID)
		assert.Equal(t, constants.FileOperation("write"), editReq.Operation)
		assert.Equal(t, filepath.Join(tmpDir, "test.txt"), editReq.FilePath)
		assert.Equal(t, "test", editReq.Justification)
		assert.True(t, editReq.CreateBackup)
		assert.True(t, editReq.CreateIfMissing)
		assert.Equal(t, "test content", editReq.Content)
		assert.Equal(t, "old content", editReq.OldContent)
		assert.Equal(t, "new content", editReq.NewContent)
		assert.Equal(t, "insert content", editReq.InsertContent)
		assert.Equal(t, 5, editReq.InsertPosition)
		assert.Equal(t, 10, editReq.StartLine)
		assert.Equal(t, 20, editReq.EndLine)
	})

	t.Run("rejects invalid protobuf", func(t *testing.T) {
		t.Parallel()
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FileEdit.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		_, err := payloadToFileEditRequest(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode")
	})

	t.Run("rejects missing file_path", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.FileEditRequested{Operation: "write"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FileEdit.Requested,
			Payload:   payload,
		}

		_, err := payloadToFileEditRequest(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing file_path")
	})

	t.Run("rejects missing operation", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FileEditRequested{FilePath: filepath.Join(tmpDir, "test.txt")}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FileEdit.Requested,
			Payload:   payload,
		}

		_, err := payloadToFileEditRequest(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing operation")
	})

	t.Run("uses message ID when payload has no execution_id", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FileEditRequested{FilePath: filepath.Join(tmpDir, "test.txt"), Operation: "write"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FileEdit.Requested,
			Payload:   payload,
		}

		editReq, err := payloadToFileEditRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "msg-1", editReq.ExecutionID)
	})

	t.Run("uses default justification when not specified", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FileEditRequested{FilePath: filepath.Join(tmpDir, "test.txt"), Operation: "write"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FileEdit.Requested,
			Payload:   payload,
		}

		editReq, err := payloadToFileEditRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "pub/sub command request", editReq.Justification)
	})
}

func TestPayloadToFsListRequest(t *testing.T) {
	t.Run("converts valid payload", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FsListRequested{
			Path:        tmpDir,
			ExecutionId: "exec-1",
			MaxDepth:    5,
			MaxEntries:  200,
		}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:              "msg-1",
			EventType:       constants.Event.Operator.FsList.Requested,
			CaseID:          "case-1",
			TaskID:          func() *string { s := "task-1"; return &s }(),
			InvestigationID: "investigation-1",
			Payload:         payload,
		}

		listReq, err := payloadToFsListRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "exec-1", listReq.ExecutionID)
		assert.Equal(t, "case-1", listReq.CaseID)
		assert.Equal(t, "task-1", listReq.TaskID)
		assert.Equal(t, "investigation-1", listReq.InvestigationID)
		assert.Equal(t, tmpDir, listReq.Path)
		assert.Equal(t, 5, listReq.MaxDepth)
		assert.Equal(t, 200, listReq.MaxEntries)
	})

	t.Run("rejects invalid protobuf", func(t *testing.T) {
		t.Parallel()
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsList.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		_, err := payloadToFsListRequest(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode")
	})

	t.Run("uses default path when not specified", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.FsListRequested{}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsList.Requested,
			Payload:   payload,
		}

		listReq, err := payloadToFsListRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, ".", listReq.Path)
	})

	t.Run("uses message ID when payload has no execution_id", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FsListRequested{Path: tmpDir}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsList.Requested,
			Payload:   payload,
		}

		listReq, err := payloadToFsListRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "msg-1", listReq.ExecutionID)
	})

	t.Run("uses default max_entries when not specified", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FsListRequested{Path: tmpDir}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsList.Requested,
			Payload:   payload,
		}

		listReq, err := payloadToFsListRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, 100, listReq.MaxEntries)
	})
}

func TestPayloadToFsGrepRequest(t *testing.T) {
	t.Run("converts valid payload", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FsGrepRequested{
			Path:        tmpDir,
			Pattern:     "test",
			ExecutionId: "exec-1",
			Includes:    []string{"*.go", "*.py"},
			MaxMatches:  200,
		}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:              "msg-1",
			EventType:       constants.Event.Operator.FsGrep.Requested,
			CaseID:          "case-1",
			TaskID:          func() *string { s := "task-1"; return &s }(),
			InvestigationID: "investigation-1",
			Payload:         payload,
		}

		grepReq, err := payloadToFsGrepRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "exec-1", grepReq.ExecutionID)
		assert.Equal(t, "case-1", grepReq.CaseID)
		assert.Equal(t, "task-1", grepReq.TaskID)
		assert.Equal(t, "investigation-1", grepReq.InvestigationID)
		assert.Equal(t, tmpDir, grepReq.Path)
		assert.Equal(t, "test", grepReq.Pattern)
		assert.Equal(t, []string{"*.go", "*.py"}, grepReq.Includes)
		assert.Equal(t, 200, grepReq.MaxMatches)
	})

	t.Run("rejects invalid protobuf", func(t *testing.T) {
		t.Parallel()
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsGrep.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		_, err := payloadToFsGrepRequest(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode")
	})

	t.Run("uses default path when not specified", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.FsGrepRequested{Pattern: "test"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsGrep.Requested,
			Payload:   payload,
		}

		grepReq, err := payloadToFsGrepRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, ".", grepReq.Path)
	})

	t.Run("uses message ID when payload has no execution_id", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FsGrepRequested{Path: tmpDir, Pattern: "test"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsGrep.Requested,
			Payload:   payload,
		}

		grepReq, err := payloadToFsGrepRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "msg-1", grepReq.ExecutionID)
	})

	t.Run("uses default max_matches when not specified", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		req := &operatorv1.FsGrepRequested{Path: tmpDir, Pattern: "test"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsGrep.Requested,
			Payload:   payload,
		}

		grepReq, err := payloadToFsGrepRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, 100, grepReq.MaxMatches)
	})
}

func TestFileOpsService_HandleFileEditRequest(t *testing.T) {
	t.Run("publishes failed result for protected runtime path", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		results := &fileEditResultsPublisher{}
		svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Results: results})

		filePath := filepath.Join(cfg.WorkDir, constants.RuntimeDirname, constants.TmpDirname, "release-smoke", "worker", "start.log")
		payload, err := proto.Marshal(&operatorv1.FileEditRequested{
			FilePath:    filePath,
			Operation:   string(constants.FileOperationWrite),
			ExecutionId: "exec-protected-file-edit",
			Content:     "must not be written",
		})
		require.NoError(t, err)
		msg := &PubSubCommandMessage{
			ID:                "msg-protected-file-edit",
			EventType:         constants.Event.Operator.FileEdit.Requested,
			OperatorSessionID: testOperatorSessionID,
			Payload:           payload,
		}

		_, err = svc.HandleFileEditRequest(context.Background(), msg)
		require.ErrorIs(t, err, constants.ErrFileEditProtectedRuntimePath)
		require.Len(t, results.results, 1)
		result, ok := results.results[0].(*operatorv1.FileEditResult)
		require.True(t, ok, "published result is %T", results.results[0])
		assert.Equal(t, "exec-protected-file-edit", result.GetExecutionId())
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.GetStatus())
		assert.Equal(t, filePath, result.GetFilePath())
		assert.Equal(t, string(constants.FileOperationWrite), result.GetOperation())
		assert.Equal(t, "validation_error", result.GetErrorType())
		assert.Contains(t, result.GetErrorMessage(), constants.ErrFileEditProtectedRuntimePath.Error())
		_, statErr := os.Stat(filePath)
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	})

	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FileEdit.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		svc.HandleFileEditRequest(context.Background(), msg)
		// Should log error and return without panic
	})

	t.Run("rejects missing file_path", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		req := &operatorv1.FileEditRequested{Operation: "write"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FileEdit.Requested,
			Payload:   payload,
		}

		svc.HandleFileEditRequest(context.Background(), msg)
		// Should log error and return without panic
	})

	t.Run("rejects missing operation", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		req := &operatorv1.FileEditRequested{FilePath: filepath.Join(tmpDir, "test.txt")}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FileEdit.Requested,
			Payload:   payload,
		}

		svc.HandleFileEditRequest(context.Background(), msg)
		// Should log error and return without panic
	})
}

// EVAL_WORKSPACE_SEED writes only attempt workspace files and sentinels under
// the Operator working directory, through the same ledgered write path as
// FILE_EDIT; every other destination is rejected before anything is written.
func TestFileOpsService_HandleEvaluationWorkspaceSeedRequest(t *testing.T) {
	workspace := constants.EvaluationWorkspacePrefix + "0123456789abcdef"
	seedIn := func(t *testing.T, cfg *config.Config, filePath string) (*fileEditResultsPublisher, *storage.LocalFirstEventsStore, error) {
		t.Helper()
		logger := testutil.NewTestLogger()
		results := &fileEditResultsPublisher{}
		eventsStore := storagetest.NewTestEventsStore(t, testOperatorSessionID)
		svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, newFileEditTestService(t, cfg, logger), pubsubtest.NewMockOperatorPubSubClient(), fileOpsTestDeps{Results: results, EventsStore: eventsStore})
		payload, err := proto.Marshal(&operatorv1.EvaluationWorkspaceSeedRequested{ExecutionId: "attempt-1", FilePath: filePath, Content: "fixture"})
		require.NoError(t, err)
		_, err = svc.HandleEvaluationWorkspaceSeedRequest(context.Background(), &PubSubCommandMessage{
			ID:                "msg-seed",
			EventType:         constants.Event.Operator.Eval.WorkspaceSeed.Requested,
			OperatorSessionID: testOperatorSessionID,
			Payload:           payload,
		})
		return results, eventsStore, err
	}

	t.Run("writes a workspace fixture and records it", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		filePath := filepath.Join(cfg.WorkDir, constants.EvaluationWorkspaceDirname, workspace, "logs", "auth.log")
		results, eventsStore, err := seedIn(t, cfg, filePath)
		require.NoError(t, err)
		require.Len(t, results.results, 1)
		result, ok := results.results[0].(*operatorv1.EvaluationWorkspaceSeedResult)
		require.True(t, ok, "published result is %T", results.results[0])
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.GetStatus())
		assert.Equal(t, "attempt-1", result.GetExecutionId())
		assert.Equal(t, filePath, result.GetFilePath())
		content, err := os.ReadFile(filePath)
		require.NoError(t, err)
		assert.Equal(t, "fixture", string(content))
		events, err := eventsStore.ListEvents(testOperatorSessionID, 10, 0)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, constants.Event.Operator.Eval.WorkspaceSeed.Completed, events[0].Type)
	})

	t.Run("overwrites the sentinel", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		sentinel := filepath.Join(cfg.WorkDir, constants.EvaluationWorkspaceDirname, workspace+constants.EvaluationWorkspaceSentinelSuffix)
		for range 2 {
			_, _, err := seedIn(t, cfg, sentinel)
			require.NoError(t, err)
		}
		content, err := os.ReadFile(sentinel)
		require.NoError(t, err)
		assert.Equal(t, "fixture", string(content))
	})

	for _, tc := range []struct {
		name string
		path func(workDir string) string
	}{
		{name: "working directory file", path: func(workDir string) string { return filepath.Join(workDir, "notes.txt") }},
		{name: "workspaces directory file", path: func(workDir string) string {
			return filepath.Join(workDir, constants.EvaluationWorkspaceDirname, "notes.txt")
		}},
		{name: "malformed workspace", path: func(workDir string) string {
			return filepath.Join(workDir, constants.EvaluationWorkspaceDirname, "ws-nothex", "notes.txt")
		}},
		{name: "outside working directory", path: func(workDir string) string {
			return filepath.Join(filepath.Dir(workDir), constants.EvaluationWorkspaceDirname, workspace, "notes.txt")
		}},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			cfg := testutil.NewTestConfig(t)
			filePath := tc.path(cfg.WorkDir)
			results, _, err := seedIn(t, cfg, filePath)
			require.ErrorIs(t, err, constants.ErrEvaluationWorkspaceSeedPathRejected)
			require.Len(t, results.results, 1)
			result, ok := results.results[0].(*operatorv1.EvaluationWorkspaceSeedResult)
			require.True(t, ok, "published result is %T", results.results[0])
			assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.GetStatus())
			assert.Equal(t, "validation_error", result.GetErrorType())
			_, statErr := os.Stat(filePath)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}

	t.Run("rejects a workspace symlinked outside the workspaces directory", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		outside := filepath.Join(cfg.WorkDir, "outside")
		require.NoError(t, os.MkdirAll(outside, constants.PermDirStandard))
		workspaces := filepath.Join(cfg.WorkDir, constants.EvaluationWorkspaceDirname)
		require.NoError(t, os.MkdirAll(workspaces, constants.PermDirStandard))
		if err := os.Symlink(outside, filepath.Join(workspaces, workspace)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		results, _, err := seedIn(t, cfg, filepath.Join(workspaces, workspace, "notes.txt"))
		require.ErrorIs(t, err, constants.ErrEvaluationWorkspaceSeedPathRejected)
		require.Len(t, results.results, 1)
		_, statErr := os.Stat(filepath.Join(outside, "notes.txt"))
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	})
}

func TestFileOpsService_HandleFsGrepRequest(t *testing.T) {
	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsGrep.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		_, err := svc.HandleFsGrepRequest(testutil.Context(t), msg)
		require.Error(t, err)
		assert.Zero(t, client.PublishedCount())
	})
}

func TestFileOpsService_HandleFsReadRequest(t *testing.T) {
	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.FsRead.Requested,
			OperatorSessionID: testOperatorSessionID,
			Payload:           []byte("invalid protobuf"),
		}

		_, err := svc.HandleFsReadRequest(testutil.Context(t), msg)
		require.Error(t, err)
		result := decodeFsReadResultForTest(t, client.LastPublished())
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
		assert.Equal(t, "invalid_request", result.ErrorType)
	})

	t.Run("rejects missing path", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.FsRead.Requested,
			OperatorSessionID: testOperatorSessionID,
			Payload:           mustMarshalProto(t, &operatorv1.FsReadRequested{Path: ""}),
		}

		_, err := svc.HandleFsReadRequest(testutil.Context(t), msg)
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		result := decodeFsReadResultForTest(t, client.LastPublished())
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
		assert.Equal(t, "invalid_request", result.ErrorType)
	})
}

func decodeFsReadResultForTest(t *testing.T, published *pubsubtest.PublishedMessage) *operatorv1.FsReadResult {
	t.Helper()
	require.NotNil(t, published)
	var env commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(published.Data, &env))
	var readResult operatorv1.FsReadResult
	require.NoError(t, proto.Unmarshal(env.Payload, &readResult))
	return &readResult
}

func TestFileOpsService_HandleFsReadRequest_RuntimePathPolicy(t *testing.T) {
	t.Run("refuses protected runtime data and publishes FAILED without content", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		dataDir := testutil.NewTestPaths(cfg.WorkDir).DataDir
		require.NoError(t, os.MkdirAll(dataDir, 0o700))
		protectedPath := filepath.Join(dataDir, "key-material.txt")
		require.NoError(t, os.WriteFile(protectedPath, []byte("protected-runtime-state"), 0o600))

		req := &operatorv1.FsReadRequested{Path: protectedPath}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.FsRead.Requested,
			OperatorSessionID: testOperatorSessionID,
			Payload:           payload,
		}

		svc.HandleFsReadRequest(context.Background(), msg)

		require.Equal(t, 1, client.PublishedCount())
		readResult := decodeFsReadResultForTest(t, client.LastPublished())
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, readResult.Status)
		assert.Equal(t, "read_error", readResult.ErrorType)
		assert.NotEmpty(t, readResult.ErrorMessage)
		assert.Empty(t, readResult.Content, "protected runtime state must not be returned")
	})

	t.Run("reads an ordinary file inside the workdir", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		ordinaryPath := filepath.Join(cfg.WorkDir, "notes.txt")
		require.NoError(t, os.WriteFile(ordinaryPath, []byte("ordinary content"), 0o600))

		req := &operatorv1.FsReadRequested{Path: ordinaryPath}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.FsRead.Requested,
			OperatorSessionID: testOperatorSessionID,
			Payload:           payload,
		}

		svc.HandleFsReadRequest(context.Background(), msg)

		require.Equal(t, 1, client.PublishedCount())
		readResult := decodeFsReadResultForTest(t, client.LastPublished())
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, readResult.Status)
		assert.Equal(t, "ordinary content", readResult.Content)
		assert.Equal(t, int64(len("ordinary content")), readResult.SizeBytes)
	})
}

func TestFileOpsService_HandleFsListRequest(t *testing.T) {
	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.FsList.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		_, err := svc.HandleFsListRequest(testutil.Context(t), msg)
		require.Error(t, err)
		assert.Zero(t, client.PublishedCount())
	})

	t.Run("empty path lists the operator work directory", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		require.NoError(t, os.WriteFile(filepath.Join(cfg.WorkDir, "from-default.txt"), []byte("x"), constants.PermFilePrivate))
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		results := &recordingResultsPublisher{}
		svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Results: results})

		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.FsList.Requested,
			OperatorSessionID: testOperatorSessionID,
			Payload:           mustMarshalProto(t, &operatorv1.FsListRequested{}),
		}

		_, err := svc.HandleFsListRequest(testutil.Context(t), msg)
		require.NoError(t, err)
		result := results.lastList(t)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
		assert.Equal(t, cfg.WorkDir, result.Path)
	})
}

func TestFileOpsService_BeginLedgerTwoPhaseCommit(t *testing.T) {
	t.Run("snapshots an existing file", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		cfg := testutil.NewTestConfig(t)
		cfg.WorkDir = tmpDir
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		testFile := filepath.Join(tmpDir, "test.txt")
		require.NoError(t, os.WriteFile(testFile, []byte("content"), constants.PermFilePrivate))

		target, err := fileEditSvc.OpenFileEditTarget(context.Background(), testFile)
		require.NoError(t, err)
		t.Cleanup(func() { _ = target.Close() })

		result, err := svc.beginLedgerTwoPhaseCommit("session-ledger-test", target, "write")
		require.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("returns nil for path traversal attempt", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		cfg := testutil.NewTestConfig(t)
		cfg.WorkDir = tmpDir
		logger := testutil.NewTestLogger()
		fileEditSvc := newFileEditTestService(t, cfg, logger)

		target, err := fileEditSvc.OpenFileEditTarget(context.Background(), filepath.Join("..", "escape.txt"))
		require.Error(t, err)
		assert.Nil(t, target)
	})

	t.Run("returns nil when file does not exist and operation is not create/write", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		cfg := testutil.NewTestConfig(t)
		cfg.WorkDir = tmpDir
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		nonExistentFile := filepath.Join(tmpDir, "missing.txt")
		target, err := fileEditSvc.OpenFileEditTarget(context.Background(), nonExistentFile)
		require.NoError(t, err)
		t.Cleanup(func() { _ = target.Close() })

		result, err := svc.beginLedgerTwoPhaseCommit("session-ledger-test", target, "replace")
		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestFileOpsService_CompleteLedgerTwoPhaseCommit(t *testing.T) {
	t.Run("returns nil for nil ledger result", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

		err := svc.completeLedgerTwoPhaseCommit("session-ledger-test", nil)
		assert.NoError(t, err)
	})
}

// newFileOpsLedgerForTest builds a real git ledger over a fresh runtime root. The vault is unlocked
// when unlocked is true; otherwise it stays locked, so every snapshot fails closed.
func newFileOpsLedgerForTest(t *testing.T, tmpDir string, unlocked bool) (*storage.GitLedgerService, *vault.Vault) {
	t.Helper()
	logger := testutil.NewTestLogger()
	fileSvc := storagetest.NewTestFileSvc(t, tmpDir)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	header, _, err := vault.NewVaultHeader(key)
	require.NoError(t, err)
	require.NoError(t, header.Save(fileSvc))
	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: logger})
	require.NoError(t, err)
	if unlocked {
		require.NoError(t, v.Unlock(key))
	}
	t.Cleanup(func() { _ = v.Close() })
	ledger, err := storage.NewGitLedgerService(&storage.LedgerConfig{EncryptionVault: v}, logger, fileSvc)
	require.NoError(t, err)
	return ledger, v
}

func TestFileOpsService_LedgerFailureFailsClosed(t *testing.T) {
	t.Run("begin failure refuses the edit and publishes FileEdit.Failed", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		cfg := testutil.NewTestConfig(t)
		cfg.WorkDir = tmpDir
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		ledger, _ := newFileOpsLedgerForTest(t, tmpDir, false)
		svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Ledger: ledger})

		testFile := filepath.Join(tmpDir, "target.txt")
		require.NoError(t, os.WriteFile(testFile, []byte("original"), constants.PermFilePrivate))

		req := &operatorv1.FileEditRequested{FilePath: testFile, Operation: "write", Content: "replaced"}
		payload, err := proto.Marshal(req)
		require.NoError(t, err)
		msg := &PubSubCommandMessage{
			ID:                "msg-ledger-locked",
			EventType:         constants.Event.Operator.FileEdit.Requested,
			OperatorSessionID: "session-ledger-locked",
			Payload:           payload,
		}

		_, err = svc.HandleFileEditRequest(context.Background(), msg)
		require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)

		got, err := os.ReadFile(testFile)
		require.NoError(t, err)
		assert.Equal(t, "original", string(got), "the edit must not run without a snapshot")

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), string(constants.Event.Operator.FileEdit.Failed))
		assert.NotContains(t, string(published.Data), string(constants.Event.Operator.FileEdit.Completed))
	})

	t.Run("completion failure fails the result and publishes FileEdit.Failed", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		cfg := testutil.NewTestConfig(t)
		cfg.WorkDir = tmpDir
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		ledger, v := newFileOpsLedgerForTest(t, tmpDir, true)
		svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Ledger: ledger})

		testFile := filepath.Join(tmpDir, "target.txt")
		require.NoError(t, os.WriteFile(testFile, []byte("original"), constants.PermFilePrivate))
		target, err := fileEditSvc.OpenFileEditTarget(context.Background(), testFile)
		require.NoError(t, err)
		t.Cleanup(func() { _ = target.Close() })

		session := "session-ledger-complete"
		ledgerResult, err := svc.beginLedgerTwoPhaseCommit(session, target, "write")
		require.NoError(t, err)
		require.NotNil(t, ledgerResult)

		// The vault locks between the snapshot and its completion.
		require.NoError(t, v.Close())

		result := &models.FileEditResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, FilePath: testFile}
		finishErr := svc.finishLedgerTwoPhaseCommit(session, ledgerResult, result)
		require.ErrorIs(t, finishErr, constants.ErrLedgerVaultRequired)

		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
		assert.Contains(t, result.ErrorMessage, "complete ledger two-phase commit")

		rr, err := NewPubSubResultsService(cfg, logger, client, newEgressScrubber(t, true))
		require.NoError(t, err)
		msg := &PubSubCommandMessage{ID: "msg-ledger-complete", EventType: constants.Event.Operator.FileEdit.Requested, OperatorSessionID: session}
		require.NoError(t, rr.PublishFileEditResult(context.Background(), &operatorv1.FileEditResult{
			ExecutionId:  result.ExecutionID,
			Status:       result.Status,
			FilePath:     result.FilePath,
			ErrorMessage: result.ErrorMessage,
		}, msg))

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), string(constants.Event.Operator.FileEdit.Failed))
		assert.NotContains(t, string(published.Data), string(constants.Event.Operator.FileEdit.Completed))
	})

	t.Run("audit record failure fails the result and returns ErrExecutionRecordFailed", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.OperatorDir(t)
		cfg := testutil.NewTestConfig(t)
		cfg.WorkDir = tmpDir
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		fileEditSvc := newFileEditTestService(t, cfg, logger)
		results := &fileEditResultsPublisher{}
		// local-first events store has no session row for "unregistered-session", so RecordEvent fails FK check.
		svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Results: results})

		testFile := filepath.Join(tmpDir, "unrecorded.txt")
		req := &operatorv1.FileEditRequested{
			FilePath:        testFile,
			Operation:       "write",
			Content:         "new content",
			CreateIfMissing: true,
		}
		payload, err := proto.Marshal(req)
		require.NoError(t, err)
		msg := &PubSubCommandMessage{
			ID:                "msg-unrecorded-edit",
			EventType:         constants.Event.Operator.FileEdit.Requested,
			OperatorSessionID: "unregistered-session",
			Payload:           payload,
		}

		_, err = svc.HandleFileEditRequest(context.Background(), msg)
		require.ErrorIs(t, err, constants.ErrExecutionRecordFailed)
		require.Len(t, results.results, 1)
		published, ok := results.results[0].(*operatorv1.FileEditResult)
		require.True(t, ok)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, published.GetStatus())
		assert.Equal(t, "audit_record_error", published.GetErrorType())
	})
}
