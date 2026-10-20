// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// runEvent is one event of a single run, read from the owner's CLI session
// stream. Exactly one field is set.
type runEvent struct {
	// Stop is an ai.eval.stop.requested event naming the run.
	Stop *models.EvalStopRequestedPayload
	// Status is an app.run.status.updated event for the run.
	Status *models.RunStatusUpdatedPayload
}

// runEventStream is an open subscription to one run's events on the CLI session
// stream. Its events are sent on the channel given to subscribeRunEvents.
type runEventStream struct {
	cancel context.CancelFunc
	done   chan struct{}
	// err is why the stream stopped on its own. It is set before done closes and
	// is nil when the owner closed the stream.
	err error
}

// subscribeRunEvents opens the CLI session's SSE stream for runID and returns
// once the Gateway has registered the listener. Stop and status events for runID
// are sent on events, which the caller must keep draining until the stream
// closes. onConnect runs on every successful connection, including each
// reconnect, before any event from that connection is sent. Stop requests are
// ephemeral and a stream can drop between two events, so the caller reconciles
// its durable run state in onConnect instead of trusting the stream alone.
//
// A first connection that fails is returned and not retried. Once connected, the
// SSE client reconnects with backoff. The stream stops only when ctx is
// cancelled, the Gateway refuses the CLI session, or a stop or status payload
// does not decode. httpClient must carry the CLI
// mTLS identity that owns cliSessionID.
func subscribeRunEvents(ctx context.Context, httpClient *http.Client, baseURL, cliSessionID, runID string, events chan<- runEvent, onConnect func()) (*runEventStream, error) {
	sseURL := baseURL + constants.APIPaths.SSEStream + "?since_id=0"
	sseClient := sse.NewClient(sseURL, httpClient)
	sseClient.SetHeader(constants.HeaderCLISessionID, cliSessionID)
	// A clean server close is not the end of a run's events: reconnect, and let
	// onConnect reconcile whatever the gap held.
	sseClient.SetReconnectOnClose(true)

	subCtx, cancel := context.WithCancel(ctx)
	stream := &runEventStream{cancel: cancel, done: make(chan struct{})}

	// ready carries exactly one result: nil once the first connection has
	// reconciled, or the reason it failed. The fields below are touched only by
	// the goroutine running sseClient.Run and read after it returns.
	ready := make(chan error, 1)
	var established bool
	var lastErr error
	sseClient.SetOnConnect(func() {
		onConnect()
		if !established {
			established = true
			ready <- nil
		}
	})
	sseClient.SetOnDisconnect(func(err error) {
		lastErr = err
		if !established {
			// A failed first connection is not retried: stop Run's backoff wait.
			cancel()
		}
	})
	// decodeErr is the first stop or status payload that did not decode. It ends
	// the stream, because the frame may have been this run's stop.
	var decodeErr error
	go func() {
		defer close(stream.done)
		sseClient.Run(subCtx, func(eventType, data string) {
			if decodeErr != nil {
				return
			}
			if err := deliverRunEvent(subCtx, events, runID, eventType, data); err != nil {
				decodeErr = err
				cancel()
			}
		})
		if !established {
			connectErr := lastErr
			if connectErr == nil {
				connectErr = ctx.Err()
			}
			if connectErr == nil {
				connectErr = context.Canceled
			}
			ready <- connectErr
			return
		}
		switch {
		case decodeErr != nil:
			stream.err = decodeErr
		case subCtx.Err() == nil:
			// Run stopped retrying, so the Gateway refused the CLI session.
			stream.err = lastErr
		}
	}()

	if err := <-ready; err != nil {
		stream.Close()
		return nil, fmt.Errorf("run events: connect: %w", err)
	}
	return stream, nil
}

// deliverRunEvent decodes one SSE frame and sends it on events when it is a stop
// or status event for runID. Any other frame is ignored. A stop or status payload
// that does not decode is returned as an error: it may be this run's stop, so it
// is never dropped as another run's event.
func deliverRunEvent(ctx context.Context, events chan<- runEvent, runID, eventType, data string) error {
	ev, ok := sse.DecodeEvent(eventType, data)
	if !ok {
		return nil
	}
	var out runEvent
	switch ev.Type {
	case string(constants.EventAiEvalStopRequested):
		var payload models.EvalStopRequestedPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			return fmt.Errorf("decode %s: %w", ev.Type, err)
		}
		if payload.RunID != runID {
			return nil
		}
		out.Stop = &payload
	case string(constants.EventAppRunStatusUpdated):
		var payload models.RunStatusUpdatedPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			return fmt.Errorf("decode %s: %w", ev.Type, err)
		}
		if payload.RunID != runID {
			return nil
		}
		out.Status = &payload
	default:
		return nil
	}
	select {
	case events <- out:
	case <-ctx.Done():
	}
	return nil
}

// Done is closed when the stream has stopped, whether its owner closed it or it
// stopped on its own.
func (s *runEventStream) Done() <-chan struct{} { return s.done }

// Err reports why the stream stopped on its own. It is valid once Done is closed
// and is nil when the owner closed the stream.
func (s *runEventStream) Err() error { return s.err }

// StoppedError is the error an owner returns once Done has closed on its own.
// The Gateway refused the session, or the stream failed. It is valid once Done
// is closed.
func (s *runEventStream) StoppedError() error {
	err := s.err
	if err == nil {
		err = constants.ErrSSEStreamClosed
	}
	return fmt.Errorf("run events: %w", err)
}

// Close ends the subscription and waits for its SSE stream to stop. The owner
// calls it once, normally with defer.
func (s *runEventStream) Close() {
	s.cancel()
	<-s.done
}
