// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestHandleFsListRequest_UnknownSessionMarksTheResultFailed(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	results := &recordingResultsPublisher{}
	svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Results: results})

	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsList.Requested,
		OperatorSessionID: "missing-session",
		Payload:           mustMarshalProto(t, &operatorv1.FsListRequested{Path: cfg.WorkDir, MaxDepth: 1, MaxEntries: 10}),
	}

	_, err := svc.HandleFsListRequest(testutil.Context(t), msg)
	require.Error(t, err)
	result := results.lastList(t)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
	assert.Equal(t, "audit_record_error", result.ErrorType)
	assert.NotEmpty(t, result.ErrorMessage)
}

func TestHandleFsListRequest_ValidPath(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.WorkDir, "listed.txt"), []byte("x"), constants.PermFilePrivate))

	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	results := &recordingResultsPublisher{}
	svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Results: results})

	// Depth 0: recursing into the runtime tree's .ztd/ would fill MaxEntries before
	// listed.txt whenever Readdir (on-disk hash order) returns .ztd first.
	req := &operatorv1.FsListRequested{Path: cfg.WorkDir, MaxDepth: 0, MaxEntries: 10}
	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsList.Requested,
		OperatorSessionID: testOperatorSessionID,
		Payload:           mustMarshalProto(t, req),
	}

	_, err := svc.HandleFsListRequest(testutil.Context(t), msg)
	require.NoError(t, err)
	result := results.lastList(t)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
	assert.Equal(t, cfg.WorkDir, result.Path)
	assert.GreaterOrEqual(t, result.TotalCount, int32(1))
	var found bool
	for _, entry := range result.Entries {
		if entry.Name == "listed.txt" {
			found = true
		}
	}
	assert.True(t, found, "listing must include the file written in the work directory")
}

// TestHandleFsListRequest_PublishesEntryPath pins the wire shape clients decode:
// every published entry carries its own path, because a listing without it
// cannot be attributed to a file.

func TestHandleFsGrepRequest_ValidPath(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.WorkDir, "test.txt"), []byte("hello world\ntest line\n"), constants.PermFilePrivate))

	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	results := &recordingResultsPublisher{}
	svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Results: results})

	req := &operatorv1.FsGrepRequested{Path: cfg.WorkDir, Pattern: "hello", MaxMatches: 10}
	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsGrep.Requested,
		OperatorSessionID: testOperatorSessionID,
		Payload:           mustMarshalProto(t, req),
	}

	_, err := svc.HandleFsGrepRequest(testutil.Context(t), msg)
	require.NoError(t, err)
	result := results.lastGrep(t)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, result.Status)
	require.NotEmpty(t, result.Matches)
	assert.Contains(t, result.Matches[0].Content, "hello")
}

func TestHandleFsReadRequest_ValidPath(t *testing.T) {
	t.Parallel()
	tmpDir := testutil.OperatorDir(t)
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("file content"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

	req := &operatorv1.FsReadRequested{Path: testFile}
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
}

func TestHandleFsReadRequest_NonExistentPath(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

	req := &operatorv1.FsReadRequested{Path: filepath.Join(cfg.WorkDir, "missing.txt")}
	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsRead.Requested,
		OperatorSessionID: testOperatorSessionID,
		Payload:           mustMarshalProto(t, req),
	}

	_, err := svc.HandleFsReadRequest(testutil.Context(t), msg)
	require.Error(t, err)
	result := decodeFsReadResultForTest(t, client.LastPublished())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
	assert.NotEmpty(t, result.ErrorMessage)
}

func TestHandleFileEditRequest_InvalidOperation(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

	req := &operatorv1.FileEditRequested{
		FilePath:  filepath.Join(cfg.WorkDir, "test.txt"),
		Operation: "invalid_op",
	}
	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FileEdit.Requested,
		OperatorSessionID: testOperatorSessionID,
		Payload:           mustMarshalProto(t, req),
	}

	_, err := svc.HandleFileEditRequest(testutil.Context(t), msg)
	require.Error(t, err)
	assert.Zero(t, client.PublishedCount())
}

func TestHandleFsListRequest_InvalidPath(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	results := &recordingResultsPublisher{}
	svc := newFileOpsServiceWithDepsForTest(t, cfg, logger, fileEditSvc, client, fileOpsTestDeps{Results: results})

	req := &operatorv1.FsListRequested{Path: filepath.Join(cfg.WorkDir, "does-not-exist")}
	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsList.Requested,
		OperatorSessionID: testOperatorSessionID,
		Payload:           mustMarshalProto(t, req),
	}

	_, err := svc.HandleFsListRequest(testutil.Context(t), msg)
	require.Error(t, err)
	result := results.lastList(t)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, result.Status)
	assert.NotEmpty(t, result.ErrorMessage)
}

func TestPayloadToFileEditRequest_InvalidOperation(t *testing.T) {
	t.Parallel()
	tmpDir := testutil.OperatorDir(t)
	req := &operatorv1.FileEditRequested{
		FilePath:  filepath.Join(tmpDir, "test.txt"),
		Operation: "invalid",
	}
	payload, _ := proto.Marshal(req)
	msg := &PubSubCommandMessage{
		ID:        "msg-1",
		EventType: constants.Event.Operator.FileEdit.Requested,
		Payload:   payload,
	}

	_, err := payloadToFileEditRequest(msg)
	assert.Error(t, err)
}

func TestPayloadToFileEditRequest_WithTaskID(t *testing.T) {
	t.Parallel()
	tmpDir := testutil.OperatorDir(t)
	req := &operatorv1.FileEditRequested{
		FilePath:  filepath.Join(tmpDir, "test.txt"),
		Operation: "write",
		Content:   "test",
	}
	payload, _ := proto.Marshal(req)
	taskID := "task-123"
	msg := &PubSubCommandMessage{
		ID:        "msg-1",
		EventType: constants.Event.Operator.FileEdit.Requested,
		TaskID:    &taskID,
		Payload:   payload,
	}

	editReq, err := payloadToFileEditRequest(msg)
	require.NoError(t, err)
	assert.Equal(t, "task-123", editReq.TaskID)
}

func TestHandleFsReadRequest_ScrubbingRedactsSecrets(t *testing.T) {
	t.Parallel()
	tmpDir := testutil.OperatorDir(t)
	testFile := filepath.Join(tmpDir, "secret.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("password=secret123 api_key=ghp_test_token"), 0644))

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

	req := &operatorv1.FsReadRequested{Path: testFile}
	payload, _ := proto.Marshal(req)
	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsRead.Requested,
		OperatorSessionID: testOperatorSessionID,
		Payload:           payload,
	}

	svc.HandleFsReadRequest(context.Background(), msg)

	published := client.LastPublished()
	require.NotNil(t, published)

	var env commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(published.Data, &env))

	var readResult operatorv1.FsReadResult
	require.NoError(t, proto.Unmarshal(env.Payload, &readResult))
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, readResult.Status)
	assert.NotContains(t, readResult.Content, "secret123", "password value must be redacted")
	assert.NotContains(t, readResult.Content, "ghp_test_token", "api key must be redacted")
}

func TestHandleFsReadRequest_TruncatesLargeFile(t *testing.T) {
	t.Parallel()
	tmpDir := testutil.OperatorDir(t)
	testFile := filepath.Join(tmpDir, "large.txt")
	largeContent := strings.Repeat("A", 2048)
	require.NoError(t, os.WriteFile(testFile, []byte(largeContent), 0644))

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	fileEditSvc := newFileEditTestService(t, cfg, logger)
	svc := newFileOpsServiceForTest(t, cfg, logger, fileEditSvc, client)

	req := &operatorv1.FsReadRequested{Path: testFile, MaxSize: 100}
	payload, _ := proto.Marshal(req)
	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.FsRead.Requested,
		OperatorSessionID: testOperatorSessionID,
		Payload:           payload,
	}

	svc.HandleFsReadRequest(context.Background(), msg)

	published := client.LastPublished()
	require.NotNil(t, published)

	var env commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(published.Data, &env))

	var readResult operatorv1.FsReadResult
	require.NoError(t, proto.Unmarshal(env.Payload, &readResult))
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, readResult.Status)
	assert.True(t, readResult.Truncated, "file larger than max_size must be truncated")
	assert.Equal(t, int64(100), readResult.SizeBytes)
}

// recordingResultsPublisher keeps the list and grep results the handler publishes.
// The default mock drops grep results, so these tests would otherwise have nothing to assert.
type recordingResultsPublisher struct {
	mockResultsPublisher
	lists []*operatorv1.FsListResult
	greps []*operatorv1.FsGrepResult
}

func (r *recordingResultsPublisher) PublishFsListResult(_ context.Context, result proto.Message, _ *PubSubCommandMessage) error {
	r.lists = append(r.lists, result.(*operatorv1.FsListResult))
	return nil
}

func (r *recordingResultsPublisher) PublishFsGrepResult(_ context.Context, result proto.Message, _ *PubSubCommandMessage) error {
	r.greps = append(r.greps, result.(*operatorv1.FsGrepResult))
	return nil
}

func (r *recordingResultsPublisher) lastList(t *testing.T) *operatorv1.FsListResult {
	t.Helper()
	require.NotEmpty(t, r.lists)
	return r.lists[len(r.lists)-1]
}

func (r *recordingResultsPublisher) lastGrep(t *testing.T) *operatorv1.FsGrepResult {
	t.Helper()
	require.NotEmpty(t, r.greps)
	return r.greps[len(r.greps)-1]
}
