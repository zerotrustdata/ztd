// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestInProcessPubSubClient_CancellationDuringSelectedCallbacks(t *testing.T) {
	t.Parallel()
	broker := &mockBroker{}
	client := NewInProcessPubSubClient(broker, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := client.Subscribe(ctx, "results")
	require.NoError(t, err)
	// Publish can select a callback before cancellation unregisters it.
	handler := broker.handlers["results"]
	var senders sync.WaitGroup
	for range 16 {
		senders.Go(func() {
			for range 128 {
				handler("results", []byte("result"))
			}
		})
	}
	<-ch
	cancel()
	for range ch {
	}
	senders.Wait()
	// Even a callback held across teardown must be safe after channel close.
	require.NotPanics(t, func() { handler("results", []byte("late result")) })
	client.Close()
}

type mockBroker struct {
	handlers map[string]func(string, []byte)
}

func (m *mockBroker) Publish(channel string, data []byte) int {
	if handler, ok := m.handlers[channel]; ok {
		handler(channel, data)
	}
	return 1
}

func (m *mockBroker) RegisterHandler(channel string, handler func(string, []byte)) func() {
	if m.handlers == nil {
		m.handlers = make(map[string]func(string, []byte))
	}
	m.handlers[channel] = handler
	return func() {
		delete(m.handlers, channel)
	}
}

func newTestInProcessPubSubClient(broker Broker) *InProcessPubSubClient {
	return NewInProcessPubSubClient(broker, testutil.NewTestLogger())
}

func TestNewInProcessPubSubClient(t *testing.T) {
	t.Parallel()
	t.Run("creates client successfully", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		logger := testutil.NewTestLogger()
		client := NewInProcessPubSubClient(broker, logger)
		require.NotNil(t, client)
		assert.Equal(t, broker, client.broker)
		assert.Same(t, logger, client.logger)
	})
}

func TestInProcessPubSubClient_Subscribe(t *testing.T) {
	t.Parallel()
	t.Run("subscribes to channel successfully", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		client := newTestInProcessPubSubClient(broker)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch, err := client.Subscribe(ctx, "test-channel")
		require.NoError(t, err)
		require.NotNil(t, ch)
	})

	t.Run("rejects subscription when closed", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		client := newTestInProcessPubSubClient(broker)
		client.Close()

		ctx := context.Background()
		_, err := client.Subscribe(ctx, "test-channel")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "client is closed")
	})

	t.Run("rejects duplicate subscription", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		client := newTestInProcessPubSubClient(broker)

		ctx1, cancel1 := context.WithCancel(context.Background())
		defer cancel1()

		_, err := client.Subscribe(ctx1, "test-channel")
		require.NoError(t, err)

		ctx2 := context.Background()
		_, err = client.Subscribe(ctx2, "test-channel")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already subscribed")
	})

	t.Run("receives published messages", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		client := newTestInProcessPubSubClient(broker)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch, err := client.Subscribe(ctx, "test-channel")
		require.NoError(t, err)

		// Publish a message
		go func() {
			time.Sleep(10 * time.Millisecond)
			broker.Publish("test-channel", []byte("test message"))
		}()

		select {
		case msg := <-ch:
			assert.Equal(t, []byte("test message"), msg)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("did not receive message")
		}
	})
}

func TestInProcessPubSubClient_Publish(t *testing.T) {
	t.Parallel()
	t.Run("publishes successfully", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		client := newTestInProcessPubSubClient(broker)

		ctx := context.Background()
		err := client.Publish(ctx, "test-channel", []byte("test message"))
		require.NoError(t, err)
	})

	t.Run("rejects publish when closed", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		client := newTestInProcessPubSubClient(broker)
		client.Close()

		ctx := context.Background()
		err := client.Publish(ctx, "test-channel", []byte("test message"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "client is closed")
	})
}

func TestInProcessPubSubClient_Close(t *testing.T) {
	t.Parallel()
	t.Run("closes successfully", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		client := newTestInProcessPubSubClient(broker)

		client.Close()
		// Should not panic
		client.Close()
	})

	t.Run("prevents operations after close", func(t *testing.T) {
		t.Parallel()
		broker := &mockBroker{}
		client := newTestInProcessPubSubClient(broker)

		client.Close()

		ctx := context.Background()
		err := client.Publish(ctx, "test-channel", []byte("test"))
		require.Error(t, err)
	})
}
