// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPubSubPublish_HandlerTeardownDuringEvidenceNotification(t *testing.T) {
	broker := NewGatewayWebSocketHandler(slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	notifications := make(chan []byte, 1)
	stopNotifications := broker.RegisterHandler("evidence", func(_ string, data []byte) {
		notifications <- data
	})
	defer stopNotifications()

	entered := make(chan struct{})
	unregistered := make(chan struct{})
	stopResults := broker.RegisterHandler("results", func(_ string, data []byte) {
		close(entered)
		// An attestation wakes its waiter before announcing the persisted
		// window. The waiter removes its handler while this callback runs.
		select {
		case <-unregistered:
			broker.Publish("evidence", data)
		case <-ctx.Done():
		}
	})
	finished := make(chan int, 1)
	go func() { finished <- broker.Publish("results", []byte("attempt-1")) }()
	<-entered
	go func() {
		stopResults()
		close(unregistered)
	}()

	// On the broken implementation cancellation releases the callback's
	// read lock, so both goroutines are still joined before the test fails.
	count := <-finished
	<-unregistered
	require.NoError(t, ctx.Err(), "handler teardown blocked the evidence notification")
	require.Equal(t, 1, count)
	require.Equal(t, []byte("attempt-1"), <-notifications)
	require.Zero(t, broker.Publish("results", []byte("attempt-2")))
}
