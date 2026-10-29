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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// modelFileToolsFixture runs the five model-facing file tools' typed actions
// through the real FILE_EDIT, FS_LIST, history and restore handlers over one
// shared ledger and events store, as an Operator does.
type modelFileToolsFixture struct {
	t       *testing.T
	session string
	fileOps *FileOpsService
	history *HistoryService
	tools   *mcp.ToolRegistry
}

func newModelFileToolsFixture(t *testing.T, session string) *modelFileToolsFixture {
	t.Helper()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	store := storagetest.NewTestEventsStore(t, session)
	ledger := storagetest.NewTestLedger(t)
	fileOps := newFileOpsServiceWithDepsForTest(t, cfg, logger, newFileEditTestService(t, cfg, logger), client, fileOpsTestDeps{Ledger: ledger, EventsStore: store})
	handler, err := storage.NewHistoryHandler(store, ledger, logger, storagetest.NewTestFileSvc(t, cfg.WorkDir), cfg.WorkDir)
	require.NoError(t, err)
	history, err := NewHistoryService(cfg, logger, client, handler, mustNewScrubbingSvc(t, logger), store)
	require.NoError(t, err)
	tools := mcp.NewToolRegistry()
	require.NoError(t, mcp.RegisterNativeTools(tools))
	return &modelFileToolsFixture{t: t, session: session, fileOps: fileOps, history: history, tools: tools}
}

// call builds the typed action a model's tool call becomes, bound to binding,
// and runs it through the action's own handler. It returns the handler
// summary the Actuator signs into the receipt.
func (f *modelFileToolsFixture) call(tool string, args any, binding mcp.GovernedActionBinding) (string, error) {
	f.t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(f.t, err)
	native, ok := f.tools.Get(tool)
	require.True(f.t, ok)
	request, err := native.(mcp.GovernedActionTool).GovernedAction(raw, binding)
	require.NoError(f.t, err)
	payload, err := request.MarshalPayload()
	require.NoError(f.t, err)
	msg := &PubSubCommandMessage{ID: "msg-" + binding.ExecutionID, EventType: request.EventType, OperatorSessionID: f.session, Payload: payload}
	handlers := map[constants.ActionType]func(context.Context, *PubSubCommandMessage) (string, error){
		constants.ActionTypeFileEdit:         f.fileOps.HandleFileEditRequest,
		constants.ActionTypeFsList:           f.fileOps.HandleFsListRequest,
		constants.ActionTypeFetchFileHistory: f.history.HandleFetchFileHistoryRequest,
		constants.ActionTypeFetchFileDiff:    f.history.HandleFetchFileDiffRequest,
		constants.ActionTypeRestoreFile:      f.history.HandleRestoreFileRequest,
	}
	return handlers[request.ActionType](context.Background(), msg)
}

func (f *modelFileToolsFixture) binding(executionID string) mcp.GovernedActionBinding {
	return mcp.GovernedActionBinding{OperatorSessionID: f.session, ExecutionID: executionID}
}

func decodeSummary[T proto.Message](t *testing.T, summary string, into T) T {
	t.Helper()
	require.NoError(t, protojson.Unmarshal([]byte(summary), into), "summary %q", summary)
	return into
}

// A model can make a governed edit, find the change that broke a file, read
// its diff, and restore the file to the commit before it, using only what the
// signed tool results show. Every step is the typed action the console and
// CLI use, so each edit and the restore are ledgered file mutations.
func TestModelFileTools_EditFindDiffAndRestoreABadChange(t *testing.T) {
	f := newModelFileToolsFixture(t, "sess-model-tools")
	dir := testutil.OperatorDir(t)
	file := filepath.Join(dir, "config.yaml")
	const good = "tls_verify: true\nretries: 3\n"

	summary, err := f.call(constants.MCPToolFileEdit, map[string]any{"file_path": file, "operation": "write", "content": good, "justification": "baseline"}, f.binding("exec-write"))
	require.NoError(t, err)
	written := decodeSummary(t, summary, &operatorv1.FileEditResult{})
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, written.GetStatus())

	_, err = f.call(constants.MCPToolFileEdit, map[string]any{"file_path": file, "operation": "replace", "old_content": "tls_verify: true", "new_content": "tls_verify: false", "justification": "unblock deploy"}, f.binding("exec-bad"))
	require.NoError(t, err)
	got, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Contains(t, string(got), "tls_verify: false")

	summary, err = f.call(constants.MCPToolListDirectory, map[string]any{"path": dir}, f.binding("exec-list"))
	require.NoError(t, err)
	listing := decodeSummary(t, summary, &operatorv1.FsListResult{})
	require.Len(t, listing.GetEntries(), 1)
	assert.Equal(t, "config.yaml", listing.GetEntries()[0].GetName())

	summary, err = f.call(constants.MCPToolFileDiff, map[string]any{"file_path": file}, f.binding("exec-changes"))
	require.NoError(t, err)
	changes := decodeSummary(t, summary, &operatorv1.FetchFileDiffResult{})
	require.Len(t, changes.GetDiffs(), 2)
	var bad *operatorv1.FileDiffEntry
	for _, change := range changes.GetDiffs() {
		if change.GetOperation() == string(storage.FileMutationWrite) {
			bad = change
		}
	}
	require.NotNil(t, bad, "changes: %v", changes.GetDiffs())
	require.NotEmpty(t, bad.GetLedgerHashBefore())

	summary, err = f.call(constants.MCPToolFileDiff, map[string]any{"diff_id": bad.GetId()}, f.binding("exec-diff"))
	require.NoError(t, err)
	diff := decodeSummary(t, summary, &operatorv1.FetchFileDiffResult{}).GetDiff()
	assert.Contains(t, diff.GetDiffContent(), "-tls_verify: true")
	assert.Contains(t, diff.GetDiffContent(), "+tls_verify: false")

	summary, err = f.call(constants.MCPToolFileHistory, map[string]any{"file_path": file}, f.binding("exec-history"))
	require.NoError(t, err)
	commits := decodeSummary(t, summary, &operatorv1.FetchFileHistoryResult{}).GetHistory()
	hashes := make([]string, 0, len(commits))
	for _, commit := range commits {
		hashes = append(hashes, commit.GetCommitHash())
	}
	assert.Contains(t, hashes, bad.GetLedgerHashBefore())

	summary, err = f.call(constants.MCPToolRestoreFile, map[string]any{"file_path": file, "commit_hash": bad.GetLedgerHashBefore()}, f.binding("exec-restore"))
	require.NoError(t, err)
	assert.True(t, decodeSummary(t, summary, &operatorv1.RestoreFileResult{}).GetSuccess())
	got, err = os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, good, string(got))
}

// A diff_id names a file mutation row on the Operator. A request scoped to one
// session never returns another session's diff.
func TestModelFileTools_DiffIDIsScopedToTheVerifiedSession(t *testing.T) {
	f := newModelFileToolsFixture(t, "sess-owner")
	file := filepath.Join(testutil.OperatorDir(t), "notes.txt")
	_, err := f.call(constants.MCPToolFileEdit, map[string]any{"file_path": file, "operation": "write", "content": "private\n", "justification": "seed"}, f.binding("exec-seed"))
	require.NoError(t, err)
	summary, err := f.call(constants.MCPToolFileDiff, map[string]any{"file_path": file}, f.binding("exec-list"))
	require.NoError(t, err)
	changes := decodeSummary(t, summary, &operatorv1.FetchFileDiffResult{}).GetDiffs()
	require.Len(t, changes, 1)

	_, err = f.call(constants.MCPToolFileDiff, map[string]any{"diff_id": changes[0].GetId()}, mcp.GovernedActionBinding{OperatorSessionID: "sess-intruder", ExecutionID: "exec-cross"})
	require.ErrorIs(t, err, constants.ErrFileDiffOutsideSession)
}
