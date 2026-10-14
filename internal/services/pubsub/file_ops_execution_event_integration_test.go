// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package pubsub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

// TestHandleFsListRequest_RecordsExecutionEvent verifies that an fs list writes its
// execution event to the real audit store, keyed by execution_id, with the output.
func TestHandleFsListRequest_RecordsExecutionEvent(t *testing.T) {
	tmpDir := testutil.OperatorDir(t)

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	store := storagetest.NewTestAuditStore(t, "sess-fs-list")
	svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{AuditStore: store})

	req := &operatorv1.FsListRequested{Path: tmpDir, ExecutionId: "exec-fs-list-1"}
	payload, err := proto.Marshal(req)
	require.NoError(t, err)
	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsList.Requested,
		OperatorSessionID: "sess-fs-list",
		Payload:           payload,
	}

	svc.HandleFsListRequest(context.Background(), msg)

	event, err := store.GetEventByExecutionID("exec-fs-list-1")
	require.NoError(t, err)
	assert.Equal(t, constants.Event.Operator.FsList.Completed, event.Type)
	assert.Equal(t, "exec-fs-list-1", event.ExecutionID)
}

func TestHandleFsListRequest_PublishesEntryPath(t *testing.T) {
	tmpDir := testutil.OperatorDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("hello"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "sub"), 0o700))

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	results := &mockResultsPublisher{}
	store := storagetest.NewTestAuditStore(t, "sess-fs-entries")
	svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, newFileEditTestService(t, cfg, logger), client, fileOpsTestDeps{Results: results, AuditStore: store})

	payload, err := proto.Marshal(&operatorv1.FsListRequested{Path: tmpDir, MaxDepth: 1, MaxEntries: 10})
	require.NoError(t, err)
	svc.HandleFsListRequest(context.Background(), &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsList.Requested,
		OperatorSessionID: "sess-fs-entries",
		Payload:           payload,
	})

	require.Len(t, results.fsListResults, 1)
	listing, ok := results.fsListResults[0].(*operatorv1.FsListResult)
	require.True(t, ok, "published result is %T", results.fsListResults[0])
	require.Empty(t, listing.GetErrorMessage())
	// FS_LIST preserves the validated requested path, including symlink aliases.
	assert.Equal(t, tmpDir, listing.GetPath())
	entries := make(map[string]*operatorv1.FsEntry, len(listing.GetEntries()))
	for _, entry := range listing.GetEntries() {
		entries[entry.GetName()] = entry
	}
	require.Contains(t, entries, "a.txt")
	require.Contains(t, entries, "sub")
	assert.Equal(t, filepath.Join(tmpDir, "a.txt"), entries["a.txt"].GetPath())
	assert.False(t, entries["a.txt"].GetIsDir())
	assert.Equal(t, int64(5), entries["a.txt"].GetSize())
	assert.Equal(t, filepath.Join(tmpDir, "sub"), entries["sub"].GetPath())
	assert.True(t, entries["sub"].GetIsDir())
}

// TestHandleFsReadRequest_RecordsExecutionEvent verifies that FS_READ records its raw execution
// event, content included, for a completed read and for a rejected request.
func TestHandleFsReadRequest_RecordsExecutionEvent(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	store := storagetest.NewTestAuditStore(t, "sess-fs-read")
	svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{AuditStore: store})

	readPath := filepath.Join(cfg.WorkDir, "notes.txt")
	require.NoError(t, os.WriteFile(readPath, []byte("ordinary content"), 0o600))

	read := func(req *operatorv1.FsReadRequested) {
		payload, err := proto.Marshal(req)
		require.NoError(t, err)
		svc.HandleFsReadRequest(context.Background(), &PubSubCommandMessage{
			ID:                "msg-" + req.ExecutionId,
			EventType:         constants.Event.Operator.FsRead.Requested,
			OperatorSessionID: "sess-fs-read",
			Payload:           payload,
		})
	}

	read(&operatorv1.FsReadRequested{Path: readPath, ExecutionId: "exec-fs-read-ok"})
	event, err := store.GetEventByExecutionID("exec-fs-read-ok")
	require.NoError(t, err)
	assert.Equal(t, constants.Event.Operator.FsRead.Completed, event.Type)
	assert.Equal(t, "ordinary content", event.CommandStdout)
	assert.Equal(t, 0, event.CommandExitCode)

	read(&operatorv1.FsReadRequested{ExecutionId: "exec-fs-read-no-path"})
	event, err = store.GetEventByExecutionID("exec-fs-read-no-path")
	require.NoError(t, err)
	assert.Equal(t, constants.Event.Operator.FsRead.Failed, event.Type)
	assert.Equal(t, 1, event.CommandExitCode)
	assert.Equal(t, "missing path in request", event.CommandStderr)

	events, err := store.ListEvents("sess-fs-read", 10, 0)
	require.NoError(t, err)
	assert.Len(t, events, 2, "each read records exactly one event")
}

// TestHandleFsReadRequest_UnrecordedReadIsFailed verifies that a read whose execution event
// cannot be recorded is published FAILED, without the file content.
func TestHandleFsReadRequest_UnrecordedReadIsFailed(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	// The message session is never created, so the real store rejects the event.
	store := storagetest.NewTestAuditStore(t)
	svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{AuditStore: store})

	readPath := filepath.Join(cfg.WorkDir, "notes.txt")
	require.NoError(t, os.WriteFile(readPath, []byte("ordinary content"), 0o600))
	payload, err := proto.Marshal(&operatorv1.FsReadRequested{Path: readPath, ExecutionId: "exec-fs-read-unrecorded"})
	require.NoError(t, err)

	svc.HandleFsReadRequest(context.Background(), &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsRead.Requested,
		OperatorSessionID: "sess-unknown",
		Payload:           payload,
	})

	readResult := decodeFsReadResultForTest(t, client.LastPublished())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, readResult.Status)
	assert.Equal(t, "audit_record_error", readResult.ErrorType)
	assert.Empty(t, readResult.Content)
}

// TestHandleRestoreFileRequest_RecordsFileMutation verifies that a completed restore is mirrored
// through the verified session's ledger and recorded as an execution event plus a RESTORE file
// mutation whose diff resolves from that ledger.
func TestHandleRestoreFileRequest_RecordsFileMutation(t *testing.T) {
	ctx := context.Background()
	workDir := testutil.OperatorDir(t)
	hostFile := filepath.Join(workDir, "notes.txt")
	const sessionID = "sess-restore"

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	store := storagetest.NewTestAuditStore(t, sessionID)
	ledger := storagetest.NewTestLedger(t)
	fileSvc := storagetest.NewTestFileSvc(t, testutil.OperatorDir(t))
	handler, err := storage.NewHistoryHandler(store, ledger, logger, fileSvc, workDir)
	require.NoError(t, err)
	svc, err := NewHistoryService(cfg, logger, client, handler, mustNewScrubbingSvc(t, logger), store)
	require.NoError(t, err)

	target, err := fileSvc.OpenFileEditTarget(ctx, hostFile, workDir)
	require.NoError(t, err)
	create, err := ledger.MirrorFileCreate(sessionID, target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostFile, []byte("original\n"), 0o600))
	require.NoError(t, ledger.CompleteMirrorCreate(create, sessionID))
	require.NoError(t, target.Close())
	require.NoError(t, os.WriteFile(hostFile, []byte("edited\n"), 0o600))

	payload, err := proto.Marshal(&operatorv1.RestoreFileRequested{
		ExecutionId:       "exec-restore-1",
		FilePath:          hostFile,
		CommitHash:        create.LedgerHashAfter,
		OperatorSessionId: sessionID,
	})
	require.NoError(t, err)
	svc.HandleRestoreFileRequest(ctx, &PubSubCommandMessage{
		EventType:         constants.Event.Operator.RestoreFile.Requested,
		OperatorSessionID: sessionID,
		Payload:           payload,
	})

	got, err := os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Equal(t, "original\n", string(got))

	event, err := store.GetEventByExecutionID("exec-restore-1")
	require.NoError(t, err)
	assert.Equal(t, constants.Event.Operator.RestoreFile.Completed, event.Type)

	mutations, err := store.ListFileMutationsBySession(sessionID, 10)
	require.NoError(t, err)
	require.Len(t, mutations, 1)
	assert.Equal(t, storage.FileMutationRestore, mutations[0].Operation)
	assert.Equal(t, event.ID, mutations[0].EventID)

	diff, err := handler.FetchFileDiff(mutations[0].ID)
	require.NoError(t, err)
	assert.Contains(t, diff.DiffContent, "-edited")
	assert.Contains(t, diff.DiffContent, "+original")
}
