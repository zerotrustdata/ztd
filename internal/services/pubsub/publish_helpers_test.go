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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestExecutionIDFromMessage(t *testing.T) {
	t.Run("extracts execution_id from payload", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.CommandRequested{Command: "ls -la", ExecutionId: "exec-123"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   payload,
		}

		execID := executionIDFromMessage(msg)
		assert.Equal(t, "exec-123", execID)
	})

	t.Run("falls back to message ID when payload has no execution_id", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.CommandRequested{Command: "ls -la"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   payload,
		}

		execID := executionIDFromMessage(msg)
		assert.Equal(t, "msg-1", execID)
	})

	t.Run("falls back to message ID on unmarshal error", func(t *testing.T) {
		t.Parallel()
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Command.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		execID := executionIDFromMessage(msg)
		assert.Equal(t, "msg-1", execID)
	})
}

func TestSetExecutionIDOnPayload(t *testing.T) {
	t.Run("sets execution_id on payload with field", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.CommandRequested{Command: "ls -la"}
		setExecutionIDOnPayload(req, "exec-123")
		assert.Equal(t, "exec-123", req.ExecutionId)
	})

	t.Run("does nothing when execution_id is empty", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.CommandRequested{Command: "ls -la", ExecutionId: "original"}
		setExecutionIDOnPayload(req, "")
		assert.Equal(t, "original", req.ExecutionId)
	})

	t.Run("does nothing when payload has no execution_id field", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.HeartbeatRequested{}
		setExecutionIDOnPayload(req, "exec-123")
		// Should not panic
	})
}

func TestPublishLFEATypedResponseTo(t *testing.T) {
	t.Run("publishes typed response successfully", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()

		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.Command.Requested,
			CaseID:            "case-1",
			InvestigationID:   "investigation-1",
			OperatorSessionID: "session-1",
			WebSessionID:      "web-session-1",
			CLISessionID:      "cli-session-1",
			Payload: mustMarshalProto(t, &operatorv1.CommandRequested{
				Command:     "ls -la",
				ExecutionId: "exec-123",
			}),
		}

		payload := &operatorv1.CommandResult{
			ExecutionId: "exec-123",
			Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		}

		publishLFEATypedResponseTo(context.Background(), client, cfg, logger, msg, constants.Event.Operator.Command.Completed, payload, storagetest.NewTestEventsStore(t), mustNewScrubbingSvc(t, logger))

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.NotEmpty(t, published.Data)
	})

	t.Run("handles build envelope error gracefully", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()

		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.Command.Requested,
			CaseID:            "case-1",
			InvestigationID:   "investigation-1",
			OperatorSessionID: "session-1",
			WebSessionID:      "web-session-1",
			CLISessionID:      "cli-session-1",
			Payload:           []byte("invalid protobuf"),
		}

		payload := &operatorv1.CommandResult{
			ExecutionId: "exec-123",
			Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		}

		publishLFEATypedResponseTo(context.Background(), client, cfg, logger, msg, constants.Event.Operator.Command.Completed, payload, storagetest.NewTestEventsStore(t), mustNewScrubbingSvc(t, logger))
		// Should log error and not panic
	})

	t.Run("handles publish error gracefully", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		client.SetPublishError(true)

		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.Command.Requested,
			CaseID:            "case-1",
			InvestigationID:   "investigation-1",
			OperatorSessionID: "session-1",
			WebSessionID:      "web-session-1",
			CLISessionID:      "cli-session-1",
			Payload: mustMarshalProto(t, &operatorv1.CommandRequested{
				Command:     "ls -la",
				ExecutionId: "exec-123",
			}),
		}

		payload := &operatorv1.CommandResult{
			ExecutionId: "exec-123",
			Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		}

		err := publishLFEATypedResponseTo(context.Background(), client, cfg, logger, msg, constants.Event.Operator.Command.Completed, payload, storagetest.NewTestEventsStore(t), mustNewScrubbingSvc(t, logger))
		require.ErrorIs(t, err, constants.ErrClientClosed)
	})
}

func TestLFEAResponsesUseExecutingOperatorIdentity(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion", true: "failure"}[failure], func(t *testing.T) {
			cfg := testutil.NewTestConfig(t)
			cfg.OperatorID = "gateway-config-identity"
			cfg.OperatorSessionId = "gateway-config-session"
			operatorID := string(constants.DocIDEmbeddedOperator)
			msg := &PubSubCommandMessage{
				ID: "inventory-command", EventType: constants.Event.Operator.OllamaModelInventory.Requested,
				OperatorID: &operatorID, OperatorSessionID: "embedded-session",
				Payload: mustMarshalProto(t, &operatorv1.OllamaModelInventoryRequested{ExecutionId: "inventory-execution"}),
			}
			client := pubsubtest.NewMockOperatorPubSubClient()
			if failure {
				publishLFEAErrorTo(t.Context(), client, cfg, testutil.NewTestLogger(), msg, constants.Event.Operator.OllamaModelInventory.Failed, "inventory failed", storagetest.NewTestEventsStore(t), mustNewScrubbingSvc(t, testutil.NewTestLogger()))
			} else {
				publishLFEATypedResponseTo(t.Context(), client, cfg, testutil.NewTestLogger(), msg, constants.Event.Operator.OllamaModelInventory.Completed, &operatorv1.OllamaModelInventoryResult{}, storagetest.NewTestEventsStore(t), mustNewScrubbingSvc(t, testutil.NewTestLogger()))
			}
			published := client.LastPublished()
			require.NotNil(t, published)
			assert.Equal(t, ResultsChannel(operatorID, msg.OperatorSessionID), published.Channel)
			envelope := &commonv1.GovernanceEnvelope{}
			require.NoError(t, protojson.Unmarshal(published.Data, envelope))
			assert.Equal(t, operatorID, envelope.GetOperatorId())
			assert.Equal(t, msg.OperatorSessionID, envelope.GetOperatorSessionId())
			assert.Equal(t, msg.ID, envelope.GetId())
		})
	}
}

func TestPublishLFEAErrorTo(t *testing.T) {
	t.Run("publishes error response successfully", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()

		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.Command.Requested,
			CaseID:            "case-1",
			InvestigationID:   "investigation-1",
			OperatorSessionID: "session-1",
			WebSessionID:      "web-session-1",
			CLISessionID:      "cli-session-1",
			Payload:           []byte("invalid protobuf"),
		}

		publishLFEAErrorTo(context.Background(), client, cfg, logger, msg, constants.Event.Operator.Command.Failed, "test error", storagetest.NewTestEventsStore(t), mustNewScrubbingSvc(t, logger))

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.NotEmpty(t, published.Data)
	})

	t.Run("handles publish error gracefully", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		client.SetPublishError(true)

		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.Command.Requested,
			CaseID:            "case-1",
			InvestigationID:   "investigation-1",
			OperatorSessionID: "session-1",
			WebSessionID:      "web-session-1",
			CLISessionID:      "cli-session-1",
			Payload:           []byte("invalid protobuf"),
		}

		err := publishLFEAErrorTo(context.Background(), client, cfg, logger, msg, constants.Event.Operator.Command.Failed, "test error", storagetest.NewTestEventsStore(t), mustNewScrubbingSvc(t, logger))
		require.ErrorIs(t, err, constants.ErrClientClosed)
	})
}

func TestPublishObservedStateEvidence(t *testing.T) {
	const sessionID = "session-1"
	newMsg := func() *PubSubCommandMessage {
		return &PubSubCommandMessage{ID: "msg-1", OperatorSessionID: sessionID}
	}

	t.Run("persists observed-state content for FsListResult", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		eventsStore := storagetest.NewTestEventsStore(t, sessionID)

		payload := &operatorv1.FsListResult{
			Entries: []*operatorv1.FsEntry{
				{Name: "file1.txt", Size: 100},
				{Name: "file2.txt", Size: 200},
			},
		}

		publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.FsList.Completed, payload, eventsStore, mustNewScrubbingSvc(t, logger))

		events, err := eventsStore.ListEvents(sessionID, 10, 0)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, constants.Event.Operator.FsList.Completed, events[0].Type)
		assert.NotEmpty(t, events[0].ContentText)
		assert.Contains(t, events[0].ContentText, "file1.txt")
	})

	t.Run("does not persist for handlers that record their own raw event", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		eventsStore := storagetest.NewTestEventsStore(t, sessionID)

		publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.FsRead.Completed, &operatorv1.FsReadResult{Content: "file content here"}, eventsStore, mustNewScrubbingSvc(t, logger))
		publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.FsRead.Failed, &operatorv1.FsReadResult{ErrorMessage: "read failed"}, eventsStore, mustNewScrubbingSvc(t, logger))
		publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.RestoreFile.Failed, &operatorv1.RestoreFileResult{Error: "restore failed"}, eventsStore, mustNewScrubbingSvc(t, logger))

		events, err := eventsStore.ListEvents(sessionID, 10, 0)
		require.NoError(t, err)
		assert.Empty(t, events, "FS_READ and RESTORE_FILE record their raw event in the handler")
	})

	t.Run("persists observed-state content for PortCheckResult", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		eventsStore := storagetest.NewTestEventsStore(t, sessionID)

		payload := &operatorv1.PortCheckResult{
			Results: []*operatorv1.PortCheckEntry{
				{Host: "localhost", Port: 8080, Open: true},
			},
		}

		publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.PortCheck.Completed, payload, eventsStore, mustNewScrubbingSvc(t, logger))

		events, err := eventsStore.ListEvents(sessionID, 10, 0)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, constants.Event.Operator.PortCheck.Completed, events[0].Type)
		assert.NotEmpty(t, events[0].ContentText)
	})

	t.Run("scrubs sensitive content before persisting", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		eventsStore := storagetest.NewTestEventsStore(t, sessionID)

		payload := &operatorv1.FsListResult{
			Entries: []*operatorv1.FsEntry{{Name: "password=secret123 api_key=ghp_test_token"}},
		}

		publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.FsList.Completed, payload, eventsStore, mustNewScrubbingSvc(t, logger))

		events, err := eventsStore.ListEvents(sessionID, 10, 0)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, constants.Event.Operator.FsList.Completed, events[0].Type)
		// Content should be scrubbed
		assert.NotContains(t, events[0].ContentText, "secret123")
		assert.NotContains(t, events[0].ContentText, "ghp_test_token")
	})

	t.Run("does not persist for command events (double-recording guard)", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		eventsStore := storagetest.NewTestEventsStore(t, sessionID)

		payload := &operatorv1.CommandResult{
			ExecutionId: "exec-1",
			Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		}

		// Command events should not be recorded via publishObservedStateEvidence
		publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.Command.Completed, payload, eventsStore, mustNewScrubbingSvc(t, logger))

		events, err := eventsStore.ListEvents(sessionID, 10, 0)
		require.NoError(t, err)
		assert.Empty(t, events, "command events should not be recorded via observed-state path")
	})

	t.Run("non-fatal: a failing store write does not panic or record", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		// The session is never created, so the real store rejects the event.
		eventsStore := storagetest.NewTestEventsStore(t)

		payload := &operatorv1.FsListResult{
			Entries: []*operatorv1.FsEntry{{Name: "file1.txt"}},
		}

		assert.NotPanics(t, func() {
			publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.FsList.Completed, payload, eventsStore, mustNewScrubbingSvc(t, logger))
		})
		events, err := eventsStore.ListEvents(sessionID, 10, 0)
		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("handles empty content gracefully", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		eventsStore := storagetest.NewTestEventsStore(t, sessionID)

		payload := &operatorv1.FsListResult{
			Entries: []*operatorv1.FsEntry{}, // Empty list
		}

		publishObservedStateEvidence(context.Background(), logger, newMsg(), constants.Event.Operator.FsList.Completed, payload, eventsStore, mustNewScrubbingSvc(t, logger))

		events, err := eventsStore.ListEvents(sessionID, 10, 0)
		require.NoError(t, err)
		assert.Empty(t, events, "empty content should not be recorded")
	})
}

func TestExtractContentText(t *testing.T) {
	t.Run("nil payload returns empty", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, extractContentText(nil))
	})

	t.Run("FsListResult with empty entries returns empty", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FsListResult{Entries: []*operatorv1.FsEntry{}}
		assert.Empty(t, extractContentText(payload))
	})

	t.Run("FsListResult with entries returns JSON", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FsListResult{
			Entries: []*operatorv1.FsEntry{{Name: "file.txt", Size: 100}},
		}
		result := extractContentText(payload)
		assert.Contains(t, result, "file.txt")
	})

	t.Run("FsReadResult returns content", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FsReadResult{Content: "hello world"}
		assert.Equal(t, "hello world", extractContentText(payload))
	})

	t.Run("FsReadResult empty content returns empty", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FsReadResult{Content: ""}
		assert.Empty(t, extractContentText(payload))
	})

	t.Run("FsGrepResult with empty matches returns empty", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FsGrepResult{Matches: []*operatorv1.FsGrepMatch{}}
		assert.Empty(t, extractContentText(payload))
	})

	t.Run("FsGrepResult with matches returns JSON", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FsGrepResult{
			Matches: []*operatorv1.FsGrepMatch{{Path: "test.go", LineNumber: 42}},
		}
		result := extractContentText(payload)
		assert.Contains(t, result, "test.go")
	})

	t.Run("PortCheckResult with empty results returns empty", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.PortCheckResult{Results: []*operatorv1.PortCheckEntry{}}
		assert.Empty(t, extractContentText(payload))
	})

	t.Run("FetchLogsResult returns formatted string", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FetchLogsResult{
			Command:    "journalctl",
			ReturnCode: 0,
			StdoutSize: 1024,
			StderrSize: 0,
		}
		result := extractContentText(payload)
		assert.Contains(t, result, "journalctl")
		assert.Contains(t, result, "exit_code: 0")
	})

	t.Run("FetchHistoryResult returns formatted string", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FetchHistoryResult{Total: 42}
		result := extractContentText(payload)
		assert.Contains(t, result, "42 events")
	})

	t.Run("FetchFileHistoryResult returns formatted string", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FetchFileHistoryResult{
			FilePath: "/etc/hosts",
			History:  []*operatorv1.FileHistoryEntry{{}},
		}
		result := extractContentText(payload)
		assert.Contains(t, result, "/etc/hosts")
		assert.Contains(t, result, "1 entries")
	})

	t.Run("FetchFileDiffResult with diff returns formatted string", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FetchFileDiffResult{
			Diff: &operatorv1.FileDiffEntry{FilePath: "/etc/hosts", Operation: "modify"},
		}
		result := extractContentText(payload)
		assert.Contains(t, result, "/etc/hosts")
		assert.Contains(t, result, "modify")
	})

	t.Run("FetchFileDiffResult without diff returns count", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.FetchFileDiffResult{Total: 5}
		result := extractContentText(payload)
		assert.Contains(t, result, "5 diffs")
	})

	t.Run("RestoreFileResult returns formatted string", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.RestoreFileResult{
			FilePath:   "/etc/hosts",
			CommitHash: "abc123",
		}
		result := extractContentText(payload)
		assert.Contains(t, result, "/etc/hosts")
		assert.Contains(t, result, "abc123")
	})

	t.Run("HeartbeatResult returns formatted string", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.HeartbeatResult{
			OperatorId: "op-1",
			Status:     "healthy",
		}
		result := extractContentText(payload)
		assert.Contains(t, result, "op-1")
		assert.Contains(t, result, "healthy")
	})

	t.Run("EvalAnswerRequested returns formatted string", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.EvalAnswerRequested{
			Benchmark: "bench-1",
			PromptId:  "prompt-1",
		}
		result := extractContentText(payload)
		assert.Contains(t, result, "bench-1")
		assert.Contains(t, result, "prompt-1")
	})

	t.Run("unknown payload type falls back to JSON marshal", func(t *testing.T) {
		t.Parallel()
		payload := &operatorv1.CommandRequested{Command: "ls -la"}
		result := extractContentText(payload)
		assert.Contains(t, result, "ls -la")
	})
}
