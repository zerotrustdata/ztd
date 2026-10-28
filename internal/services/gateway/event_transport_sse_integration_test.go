// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestObserveProducer_EmitSSEEvent_RejectsNonSSETransportEvents(t *testing.T) {
	producer, sseStore, _ := newObserveProducerTestEnv(t)
	route := producerRoute("user-test-1")
	payload := map[string]string{"key": "value"}

	t.Run("rejects local event type", func(t *testing.T) {
		err := producer.emitSSEEvent(route, string(constants.EventPlatformAuditChainCheckpointed), payload)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)

		// Assert no row was stored
		rows, qErr := sseStore.SSEEventsListSince(route, 0, 100)
		require.NoError(t, qErr)
		assert.Empty(t, rows)
	})

	t.Run("rejects local consensus vote recorded event", func(t *testing.T) {
		err := producer.emitSSEEvent(route, string(constants.EventConsensusVoteRecorded), payload)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)

		rows, qErr := sseStore.SSEEventsListSince(route, 0, 100)
		require.NoError(t, qErr)
		assert.Empty(t, rows)
	})

	t.Run("rejects pubsub-only operator shutdown acknowledged event", func(t *testing.T) {
		err := producer.emitSSEEvent(route, string(constants.Event.Operator.ShutdownAcknowledged), payload)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)

		rows, qErr := sseStore.SSEEventsListSince(route, 0, 100)
		require.NoError(t, qErr)
		assert.Empty(t, rows)
	})

	t.Run("rejects unknown event type", func(t *testing.T) {
		err := producer.emitSSEEvent(route, "ztd.v1.nonexistent.event", payload)
		require.ErrorIs(t, err, constants.ErrTxUnknownEventType)

		rows, qErr := sseStore.SSEEventsListSince(route, 0, 100)
		require.NoError(t, qErr)
		assert.Empty(t, rows)
	})
}

func TestSSEEventPublisher_RejectsNonSSETransportEvents(t *testing.T) {
	_, sseStore, _ := newObserveProducerTestEnv(t)
	pubsub := NewGatewayWebSocketHandler(nil)
	publisher := NewSSEEventPublisher(sseStore, pubsub)
	route := producerRoute("user-test-2")
	payload := map[string]string{"message": "hello"}

	t.Run("Publish rejects local event", func(t *testing.T) {
		err := publisher.Publish(route, string(constants.EventPlatformAuditChainCheckpointed), payload, "producer-test")
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
	})

	t.Run("Publish rejects pubsub-only event", func(t *testing.T) {
		err := publisher.Publish(route, string(constants.Event.Operator.Command.Cancelled), payload, "producer-test")
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
	})

	t.Run("PublishEphemeral rejects local event", func(t *testing.T) {
		err := publisher.PublishEphemeral(route, string(constants.EventPlatformAuditChainCheckpointed), payload)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
	})

	t.Run("PublishEphemeral rejects pubsub-only event", func(t *testing.T) {
		err := publisher.PublishEphemeral(route, string(constants.Event.Operator.Command.Cancelled), payload)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
	})

	t.Run("Publish accepts registered sse event", func(t *testing.T) {
		err := publisher.Publish(route, string(constants.EventAppAgentStatusUpdated), payload, "producer-test")
		require.NoError(t, err)
	})
}
