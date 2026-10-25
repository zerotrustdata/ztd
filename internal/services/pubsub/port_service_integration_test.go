//go:build integration

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
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

func TestPortService_HandlePortCheckRequest(t *testing.T) {
	t.Run("rejects invalid protobuf payload", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestPortService(t, cfg, logger, client)

		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.PortCheck.Requested,
			Payload:   []byte("invalid protobuf"),
		}

		svc.HandlePortCheckRequest(context.Background(), msg)
		// Should log error and publish error response
	})

	t.Run("rejects invalid port (zero)", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestPortService(t, cfg, logger, client)

		req := &operatorv1.CheckPortRequested{Port: 0}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.PortCheck.Requested,
			Payload:   payload,
		}

		svc.HandlePortCheckRequest(context.Background(), msg)
		// Should log warning and publish error response
	})

	t.Run("rejects invalid port (negative)", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestPortService(t, cfg, logger, client)

		req := &operatorv1.CheckPortRequested{Port: -1}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.PortCheck.Requested,
			Payload:   payload,
		}

		svc.HandlePortCheckRequest(context.Background(), msg)
		// Should log warning and publish error response
	})

	t.Run("rejects invalid port (too large)", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestPortService(t, cfg, logger, client)

		req := &operatorv1.CheckPortRequested{Port: 70000}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.PortCheck.Requested,
			Payload:   payload,
		}

		svc.HandlePortCheckRequest(context.Background(), msg)
		// Should log warning and publish error response
	})

	t.Run("handles valid port check with defaults", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestPortService(t, cfg, logger, client)

		req := &operatorv1.CheckPortRequested{Port: 8080}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.PortCheck.Requested,
			OperatorSessionID: "session-1",
			Payload:           payload,
		}

		svc.HandlePortCheckRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.NotEmpty(t, published.Data)
	})

	t.Run("handles valid port check with custom host", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestPortService(t, cfg, logger, client)

		req := &operatorv1.CheckPortRequested{Port: 8080, Host: "127.0.0.1"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.PortCheck.Requested,
			OperatorSessionID: "session-1",
			Payload:           payload,
		}

		svc.HandlePortCheckRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.NotEmpty(t, published.Data)
	})

	t.Run("handles valid port check with custom protocol", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()
		svc := newTestPortService(t, cfg, logger, client)

		req := &operatorv1.CheckPortRequested{Port: 8080, Protocol: "tcp"}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:                "msg-1",
			EventType:         constants.Event.Operator.PortCheck.Requested,
			OperatorSessionID: "session-1",
			Payload:           payload,
		}

		svc.HandlePortCheckRequest(context.Background(), msg)

		published := client.LastPublished()
		require.NotNil(t, published)
		assert.NotEmpty(t, published.Data)
	})
}

func TestPortService_RecordsObservedStateEvidence(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	svc := newTestPortService(t, cfg, logger, client)
	const sessionID = "session-port-evidence"
	require.NoError(t, svc.eventsStore.CreateSession(sessionID, constants.SessionTypeOperator, "Operator Session", "operator-1"))

	payload, err := proto.Marshal(&operatorv1.CheckPortRequested{Port: 0})
	require.NoError(t, err)
	svc.HandlePortCheckRequest(context.Background(), &PubSubCommandMessage{
		ID:                "msg-port-evidence",
		EventType:         constants.Event.Operator.PortCheck.Requested,
		OperatorSessionID: sessionID,
		Payload:           payload,
	})

	events, err := svc.eventsStore.ListEvents(sessionID, 10, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, constants.Event.Operator.PortCheck.Failed, events[0].Type)
	assert.Contains(t, events[0].ContentText, "port must be between 1 and 65535")
}
