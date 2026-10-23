// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newRecordingAuditService(t *testing.T) (*AuditService, *storage.SQLAuditStore, string) {
	t.Helper()
	cfg := testutil.NewTestConfig(t)
	store := storagetest.NewTestAuditStore(t, cfg.OperatorSessionId)
	return NewAuditService(cfg, testutil.NewTestLogger(), store), store, cfg.OperatorSessionId
}

func listSessionEvents(t *testing.T, store *storage.SQLAuditStore, sessionID string) []*storage.Event {
	t.Helper()
	events, err := store.ListEvents(sessionID, 20, 0)
	require.NoError(t, err)
	return events
}

func TestHandleUserMsgRequest_RecordsContentAndRejectsBadPayload(t *testing.T) {
	t.Parallel()

	t.Run("invalid protobuf", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleUserMsgRequest(testutil.Context(t), &PubSubCommandMessage{Payload: []byte("not-protobuf")})
		require.ErrorIs(t, err, constants.ErrAuditUnmarshalUserMsg)
		assert.Empty(t, listSessionEvents(t, store, sessionID))
	})

	t.Run("empty content records nothing", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleUserMsgRequest(testutil.Context(t), &PubSubCommandMessage{
			Payload: mustMarshalProto(t, &operatorv1.AuditMsgRequested{Content: ""}),
		})
		require.NoError(t, err)
		assert.Empty(t, listSessionEvents(t, store, sessionID))
	})

	t.Run("records the message on the operator session", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleUserMsgRequest(testutil.Context(t), &PubSubCommandMessage{
			Payload: mustMarshalProto(t, &operatorv1.AuditMsgRequested{Content: "hello operator"}),
		})
		require.NoError(t, err)

		events := listSessionEvents(t, store, sessionID)
		require.Len(t, events, 1)
		assert.Equal(t, constants.Event.Operator.Audit.UserMsg, events[0].Type)
		assert.Equal(t, sessionID, events[0].OperatorSessionID)
		assert.Equal(t, "hello operator", events[0].ContentText)
	})
}

func TestHandleUserMsgRequest_UninitializedStoreFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		store *storage.SQLAuditStore
		want  error
	}{
		{name: "nil store", store: nil, want: constants.ErrAuditStoreDisabled},
		{name: "nil database", store: &storage.SQLAuditStore{}, want: constants.ErrAuditStoreDBNotInitialized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := testutil.NewTestConfig(t)
			svc := NewAuditService(cfg, testutil.NewTestLogger(), tt.store)
			err := svc.HandleUserMsgRequest(testutil.Context(t), &PubSubCommandMessage{
				Payload: mustMarshalProto(t, &operatorv1.AuditMsgRequested{Content: "hello"}),
			})
			require.ErrorIs(t, err, constants.ErrAuditRecordUserMsg)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestHandleAIMsgRequest_RecordsContentAndRejectsBadPayload(t *testing.T) {
	t.Parallel()

	t.Run("invalid protobuf", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleAIMsgRequest(testutil.Context(t), &PubSubCommandMessage{Payload: []byte("not-protobuf")})
		require.ErrorIs(t, err, constants.ErrAuditUnmarshalAIMsg)
		assert.Empty(t, listSessionEvents(t, store, sessionID))
	})

	t.Run("empty content records nothing", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleAIMsgRequest(testutil.Context(t), &PubSubCommandMessage{
			Payload: mustMarshalProto(t, &operatorv1.AuditMsgRequested{Content: ""}),
		})
		require.NoError(t, err)
		assert.Empty(t, listSessionEvents(t, store, sessionID))
	})

	t.Run("records the message on the operator session", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleAIMsgRequest(testutil.Context(t), &PubSubCommandMessage{
			Payload: mustMarshalProto(t, &operatorv1.AuditMsgRequested{Content: "model reply"}),
		})
		require.NoError(t, err)

		events := listSessionEvents(t, store, sessionID)
		require.Len(t, events, 1)
		assert.Equal(t, constants.Event.Operator.Audit.AIMsg, events[0].Type)
		assert.Equal(t, "model reply", events[0].ContentText)
	})
}

func TestHandleDirectCmdRequest_RecordsCommandAndRejectsBadPayload(t *testing.T) {
	t.Parallel()

	t.Run("invalid protobuf", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleDirectCmdRequest(testutil.Context(t), &PubSubCommandMessage{Payload: []byte("not-protobuf")})
		require.ErrorIs(t, err, constants.ErrAuditUnmarshalDirectCmd)
		assert.Empty(t, listSessionEvents(t, store, sessionID))
	})

	t.Run("empty command records nothing", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleDirectCmdRequest(testutil.Context(t), &PubSubCommandMessage{
			Payload: mustMarshalProto(t, &operatorv1.DirectCommandAuditRequested{Command: ""}),
		})
		require.NoError(t, err)
		assert.Empty(t, listSessionEvents(t, store, sessionID))
	})

	t.Run("records the command", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleDirectCmdRequest(testutil.Context(t), &PubSubCommandMessage{
			Payload: mustMarshalProto(t, &operatorv1.DirectCommandAuditRequested{
				Command:     "ls -la",
				ExecutionId: "exec-1",
			}),
		})
		require.NoError(t, err)

		events := listSessionEvents(t, store, sessionID)
		require.Len(t, events, 1)
		assert.Equal(t, constants.Event.Operator.Audit.Command, events[0].Type)
		assert.Equal(t, "ls -la", events[0].CommandRaw)
		assert.Equal(t, constants.ExitCodeNone, events[0].CommandExitCode)
	})
}

func TestHandleDirectCmdResultRequest_RecordsResultAndRejectsBadPayload(t *testing.T) {
	t.Parallel()

	t.Run("invalid protobuf", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleDirectCmdResultRequest(testutil.Context(t), &PubSubCommandMessage{Payload: []byte("not-protobuf")})
		require.ErrorIs(t, err, constants.ErrAuditUnmarshalDirectResult)
		assert.Empty(t, listSessionEvents(t, store, sessionID))
	})

	t.Run("empty command records nothing", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleDirectCmdResultRequest(testutil.Context(t), &PubSubCommandMessage{
			Payload: mustMarshalProto(t, &operatorv1.DirectCommandResultAuditRequested{Command: ""}),
		})
		require.NoError(t, err)
		assert.Empty(t, listSessionEvents(t, store, sessionID))
	})

	t.Run("records stdout exit code and duration", func(t *testing.T) {
		t.Parallel()
		svc, store, sessionID := newRecordingAuditService(t)
		err := svc.HandleDirectCmdResultRequest(testutil.Context(t), &PubSubCommandMessage{
			Payload: mustMarshalProto(t, &operatorv1.DirectCommandResultAuditRequested{
				Command:              "ls -la",
				ExecutionId:          "exec-1",
				ExitCode:             0,
				Output:               "file1\nfile2",
				Stderr:               "warn",
				ExecutionTimeSeconds: 0.5,
			}),
		})
		require.NoError(t, err)

		events := listSessionEvents(t, store, sessionID)
		require.Len(t, events, 1)
		assert.Equal(t, "ls -la", events[0].CommandRaw)
		assert.Equal(t, "file1\nfile2", events[0].CommandStdout)
		assert.Equal(t, "warn", events[0].CommandStderr)
		assert.Equal(t, 0, events[0].CommandExitCode)
		assert.Equal(t, int64(500), events[0].ExecutionDurationMs)
	})
}
