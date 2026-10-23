// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestNewHistoryService(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	auditStore := storagetest.NewTestAuditStore(t)
	handler := newTestHistoryHandler(t, logger, auditStore)
	scrubber := mustNewScrubbingSvc(t, logger)

	t.Run("binds every dependency", func(t *testing.T) {
		t.Parallel()
		svc, err := NewHistoryService(cfg, logger, client, handler, scrubber, auditStore)
		require.NoError(t, err)
		require.NotNil(t, svc)
		assert.Equal(t, cfg, svc.config)
		assert.Equal(t, logger, svc.logger)
		assert.Equal(t, client, svc.client)
		assert.Same(t, handler, svc.historyHandler)
		assert.Same(t, scrubber, svc.scrubbing)
		assert.Same(t, auditStore, svc.auditStore)
	})

	tests := []struct {
		name       string
		cfg        *config.Config
		handler    *storage.HistoryHandler
		scrubber   *scrubbing.ScrubbingService
		auditStore *storage.SQLAuditStore
		wantErr    error
	}{
		{name: "nil config", handler: handler, scrubber: scrubber, auditStore: auditStore, wantErr: constants.ErrMissingRequiredField},
		{name: "nil history handler", cfg: cfg, scrubber: scrubber, auditStore: auditStore, wantErr: constants.ErrHistoryHandlerRequired},
		{name: "nil scrubber", cfg: cfg, handler: handler, auditStore: auditStore, wantErr: constants.ErrEgressScrubberUnavailable},
		{name: "disabled scrubber", cfg: cfg, handler: handler, scrubber: newEgressScrubber(t, false), auditStore: auditStore, wantErr: constants.ErrEgressScrubberUnavailable},
		{name: "nil audit store", cfg: cfg, handler: handler, scrubber: scrubber, wantErr: constants.ErrAuditStoreRequired},
	}
	for _, tt := range tests {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			t.Parallel()
			svc, err := NewHistoryService(tt.cfg, logger, client, tt.handler, tt.scrubber, tt.auditStore)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, svc)
		})
	}
}

func TestHistoryService_HandleFetchLogsRequest(t *testing.T) {
	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestHistoryService(t, cfg, logger, client)

		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.FetchLogs.Requested,
			Payload:   []byte("invalid protobuf"),
		}
		svc.HandleFetchLogsRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), "invalid request payload")
	})

	t.Run("rejects missing execution_id", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestHistoryService(t, cfg, logger, client)

		req := &operatorv1.FetchLogsRequested{ExecutionId: ""}
		payload, err := proto.Marshal(req)
		require.NoError(t, err)

		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.FetchLogs.Requested,
			Payload:   payload,
		}
		svc.HandleFetchLogsRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), "missing execution_id")
	})
}

func TestHistoryService_HandleFetchLogsRequest_ReadsExecutionEvent(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	svc := newTestHistoryService(t, cfg, logger, client)
	const sessionID = "session-fetch-logs"
	require.NoError(t, svc.auditStore.CreateSession(sessionID, constants.SessionTypeOperator, "Operator Session", "operator-1"))

	fetch := func(executionID string) {
		payload, err := proto.Marshal(&operatorv1.FetchLogsRequested{ExecutionId: executionID})
		require.NoError(t, err)
		svc.HandleFetchLogsRequest(context.Background(), &PubSubCommandMessage{
			EventType:         constants.Event.Operator.FetchLogs.Requested,
			OperatorSessionID: sessionID,
			Payload:           payload,
		})
	}

	fetch("exec-missing")
	missing := client.LastPublished()
	require.NotNil(t, missing)
	assert.Contains(t, string(missing.Data), "failed to retrieve execution")

	_, err := svc.auditStore.RecordEvent(&storage.Event{
		OperatorSessionID: sessionID,
		Timestamp:         time.Now().UTC(),
		Type:              constants.Event.Operator.Audit.Command,
		CommandRaw:        "echo hello",
		CommandStdout:     "hello",
		ExecutionID:       "exec-present",
	})
	require.NoError(t, err)

	fetch("exec-present")
	found := client.LastPublished()
	require.NotNil(t, found)
	assert.Contains(t, string(found.Data), "echo hello")
	assert.NotContains(t, string(found.Data), "failed to retrieve execution")
}

func TestHistoryService_HandleFetchHistoryRequest(t *testing.T) {
	t.Run("rejects a request without operator_session_id", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestHistoryService(t, cfg, logger, client)

		payload, err := proto.Marshal(&operatorv1.FetchHistoryRequested{})
		require.NoError(t, err)
		svc.HandleFetchHistoryRequest(context.Background(), &PubSubCommandMessage{
			EventType: constants.Event.Operator.FetchHistory.Requested,
			Payload:   payload,
		})

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), "operator_session_id is required")
	})
}

func TestHistoryService_HandleFetchFileHistoryRequest(t *testing.T) {
	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestHistoryService(t, cfg, logger, client)

		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.FetchFileHistory.Requested,
			Payload:   []byte("invalid protobuf"),
		}
		svc.HandleFetchFileHistoryRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), "invalid request payload")
	})
}

func TestHistoryService_HandleRestoreFileRequest(t *testing.T) {
	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestHistoryService(t, cfg, logger, client)

		msg := &PubSubCommandMessage{
			EventType:         constants.Event.Operator.RestoreFile.Requested,
			OperatorSessionID: testOperatorSessionID,
			Payload:           []byte("invalid protobuf"),
		}
		svc.HandleRestoreFileRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), "invalid request format")
	})
}

// A failed restore records RESTORE_FILE.Failed as its raw execution event and is never
// published as completed.
func TestHistoryService_HandleRestoreFileRequest_RecordsFailedRestore(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	svc := newTestHistoryService(t, cfg, logger, client)

	payload, err := proto.Marshal(&operatorv1.RestoreFileRequested{
		ExecutionId:       "exec-restore-no-commit",
		FilePath:          filepath.Join(cfg.WorkDir, "notes.txt"),
		OperatorSessionId: testOperatorSessionID,
	})
	require.NoError(t, err)
	svc.HandleRestoreFileRequest(context.Background(), &PubSubCommandMessage{
		EventType:         constants.Event.Operator.RestoreFile.Requested,
		OperatorSessionID: testOperatorSessionID,
		Payload:           payload,
	})

	event, err := svc.auditStore.GetEventByExecutionID("exec-restore-no-commit")
	require.NoError(t, err)
	assert.Equal(t, constants.Event.Operator.RestoreFile.Failed, event.Type)
	assert.Equal(t, 1, event.CommandExitCode)
	assert.Contains(t, event.CommandStderr, "commit_hash is required")

	published := client.LastPublished()
	require.NotNil(t, published)
	var env commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(published.Data, &env))
	var result operatorv1.RestoreFileResult
	require.NoError(t, proto.Unmarshal(env.Payload, &result))
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "commit_hash is required")
}

func TestHistoryService_HandleFetchFileDiffRequest(t *testing.T) {
	t.Run("rejects invalid protobuf payload when local store is available", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestHistoryService(t, cfg, logger, client)

		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.FetchFileDiff.Requested,
			Payload:   []byte("invalid protobuf"),
		}
		svc.HandleFetchFileDiffRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), "invalid request payload")
	})

	t.Run("rejects when neither diff_id nor operator_session_id provided", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestHistoryService(t, cfg, logger, client)

		req := &operatorv1.FetchFileDiffRequested{}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.FetchFileDiff.Requested,
			Payload:   payload,
		}
		svc.HandleFetchFileDiffRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.Contains(t, string(published.Data), "either diff_id or operator_session_id is required")
	})

}
