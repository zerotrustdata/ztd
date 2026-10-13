// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	execution "github.com/zerotrustdata/ztd/internal/services/execution"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

func TestPayloadToExecutionRequest(t *testing.T) {
	t.Run("converts valid payload", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.CommandRequested{
			Command:        "ls -la",
			ExecutionId:    "exec-1",
			Justification:  "test",
			Intent:         "list files",
			TimeoutSeconds: 30,
			Environment: map[string]string{
				"ZTD_OLLAMA_ENDPOINT": "http://inference.example:11434",
			},
			WorkingDirectory: "/var/lib/ztd",
		}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:              "msg-1",
			EventType:       constants.Event.Operator.Command.Requested,
			CaseID:          "case-1",
			TaskID:          func() *string { s := "task-1"; return &s }(),
			InvestigationID: "investigation-1",
			Payload:         payload,
		}

		execReq, err := payloadToExecutionRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "exec-1", execReq.ExecutionID)
		assert.Equal(t, "case-1", execReq.CaseID)
		assert.Equal(t, "task-1", *execReq.TaskID)
		assert.Equal(t, "investigation-1", execReq.InvestigationID)
		assert.Equal(t, "ls -la", execReq.Command)
		assert.Equal(t, "test", execReq.Justification)
		assert.Equal(t, "list files", execReq.Intent)
		assert.Equal(t, 30, execReq.TimeoutSeconds)
		assert.Equal(t, map[string]string{"ZTD_OLLAMA_ENDPOINT": "http://inference.example:11434"}, execReq.Environment)
		require.NotNil(t, execReq.WorkingDirectory)
		assert.Equal(t, "/var/lib/ztd", *execReq.WorkingDirectory)
	})

	t.Run("rejects invalid protobuf", func(t *testing.T) {
		t.Parallel()
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		_, err := payloadToExecutionRequest(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode command payload")
	})

	t.Run("rejects missing command", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.CommandRequested{Command: ""}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   payload,
		}

		_, err := payloadToExecutionRequest(msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing command")
	})

	t.Run("uses message ID when payload has no execution_id", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.CommandRequested{Command: "ls -la"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   payload,
		}

		execReq, err := payloadToExecutionRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, "msg-1", execReq.ExecutionID)
	})

	t.Run("uses default timeout when not specified", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.CommandRequested{Command: "ls -la"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   payload,
		}

		execReq, err := payloadToExecutionRequest(msg)
		require.NoError(t, err)
		assert.Equal(t, 300, execReq.TimeoutSeconds)
	})
}

func TestCommandService_HandleExecutionRequest(t *testing.T) {
	t.Run("rejects oversized payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		largePayload := make([]byte, 1024*1024+1)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   largePayload,
		}

		svc.HandleExecutionRequest(context.Background(), msg)
		// Should log error and return without panic
	})

	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		svc.HandleExecutionRequest(context.Background(), msg)
		// Should log error and return without panic
	})

	t.Run("rejects missing command field", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		req := &operatorv1.CommandRequested{Command: ""}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   payload,
		}

		svc.HandleExecutionRequest(context.Background(), msg)
		// Should log error and return without panic
	})

	t.Run("rejects ollama service command without opt in", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		cfg.ProviderBoundaryObserver.Enabled = true
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		req := &operatorv1.CommandRequested{Command: "ollama stop", ExecutionId: "exec-1"}
		payload, err := proto.Marshal(req)
		require.NoError(t, err)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   payload,
		}

		svc.HandleExecutionRequest(context.Background(), msg)
	})
}

func TestNewCommandService(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	execSvc := execution.NewExecutionService(cfg, logger)
	results := &mockResultsPublisher{}
	auditStore := storagetest.NewTestAuditStore(t)

	t.Run("binds every dependency", func(t *testing.T) {
		t.Parallel()
		svc, err := NewCommandService(cfg, logger, execSvc, results, auditStore)
		require.NoError(t, err)
		require.NotNil(t, svc)
		assert.Same(t, execSvc, svc.execution)
		assert.Same(t, results, svc.results)
		assert.Same(t, auditStore, svc.auditStore)
	})

	tests := []struct {
		name       string
		cfg        *config.Config
		execSvc    *execution.ExecutionService
		results    ResultsPublisher
		auditStore *storage.SQLAuditStore
		wantErr    error
	}{
		{name: "nil config", execSvc: execSvc, results: results, auditStore: auditStore, wantErr: constants.ErrMissingRequiredField},
		{name: "nil execution service", cfg: cfg, results: results, auditStore: auditStore, wantErr: constants.ErrMissingRequiredField},
		{name: "nil results publisher", cfg: cfg, execSvc: execSvc, auditStore: auditStore, wantErr: constants.ErrPubSubResultsPublisher},
		{name: "nil audit store", cfg: cfg, execSvc: execSvc, results: results, wantErr: constants.ErrAuditStoreRequired},
	}
	for _, tt := range tests {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			t.Parallel()
			svc, err := NewCommandService(tt.cfg, logger, tt.execSvc, tt.results, tt.auditStore)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, svc)
		})
	}
}

func TestCommandService_HandleCancelRequest(t *testing.T) {
	t.Run("rejects oversized payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		largePayload := make([]byte, 64*1024+1)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.CancelRequested,
			Payload:   largePayload,
		}

		svc.HandleCancelRequest(context.Background(), msg)
		// Should log error and return without panic
	})

	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.CancelRequested,
			Payload:   []byte("invalid protobuf"),
		}

		svc.HandleCancelRequest(context.Background(), msg)
		// Should log error and return without panic
	})

	t.Run("rejects missing execution_id", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		req := &operatorv1.CommandCancelRequested{ExecutionId: ""}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.CancelRequested,
			Payload:   payload,
		}

		svc.HandleCancelRequest(context.Background(), msg)
		// Should log error and return without panic
	})
}

func TestCommandService_runStatusTicker(t *testing.T) {
	t.Run("returns zero when done channel closes immediately", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		ctx := context.Background()
		done := make(chan struct{})
		close(done)

		execReq := &models.ExecutionRequestPayload{ExecutionID: "exec-1"}
		msg := &PubSubCommandMessage{ID: "msg-1"}
		startTime := time.Now()

		count := svc.runStatusTicker(ctx, execReq, msg, "test", startTime, done)
		assert.Equal(t, 0, count)
	})

	t.Run("returns count when context cancelled", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := newTestCommandService(t, cfg, logger, &mockResultsPublisher{})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		execReq := &models.ExecutionRequestPayload{ExecutionID: "exec-1"}
		msg := &PubSubCommandMessage{ID: "msg-1"}
		startTime := time.Now()

		// Cancel context after a short delay
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		count := svc.runStatusTicker(ctx, execReq, msg, "test", startTime, done)
		assert.GreaterOrEqual(t, count, 0)
	})
}

func TestIsValidFileOperation(t *testing.T) {
	t.Parallel()
	validOps := []constants.FileOperation{
		constants.FileOperationCreate,
		constants.FileOperationDelete,
		constants.FileOperationInsert,
		constants.FileOperationPatch,
		constants.FileOperationRead,
		constants.FileOperationReplace,
		constants.FileOperationUpdate,
		constants.FileOperationWrite,
	}
	for _, op := range validOps {
		if !isValidFileOperation(op) {
			t.Errorf("expected %s to be valid", op)
		}
	}

	if isValidFileOperation("invalid") {
		t.Error("expected 'invalid' to be invalid")
	}
	if isValidFileOperation("") {
		t.Error("expected empty string to be invalid")
	}
}
