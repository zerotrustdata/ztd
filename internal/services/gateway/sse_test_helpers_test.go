// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

// seedCLISessionDoc inserts a CLI session document into the DocStore for test setup.
func seedCLISessionDoc(t *testing.T, h *HTTPHandler, cliSessionID, userID, operatorSessionID string) {
	t.Helper()
	cliSess := models.CLISession{
		ID:                cliSessionID,
		UserID:            userID,
		OperatorSessionID: operatorSessionID,
	}
	cliBytes, err := json.Marshal(cliSess)
	require.NoError(t, err)
	err = h.dataController.docStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, cliBytes)
	require.NoError(t, err)
}

// bindOperatorToWebSession sets the KV binding from operator session to web session ID.
func bindOperatorToWebSession(t *testing.T, h *HTTPHandler, operatorSessionID, webSessionID string) {
	t.Helper()
	err := h.sseController.kvStore.KVSet(t.Context(), sessionOperatorBindKey(operatorSessionID), webSessionID, 0)
	require.NoError(t, err)
}

// seedCLISessionCtx seeds a CLI session document and returns a pre-stamped context
// plus the generated IDs. This reduces the repeated seedCLISessionDoc + context
// stamping boilerplate (opSessID + userID + cliSessionID) in stream/authorize tests.
// The suffix is used to generate unique IDs per test to avoid cross-test collisions.
func seedCLISessionCtx(t *testing.T, h *HTTPHandler, suffix string) (ctx context.Context, userID, cliSessionID string) {
	t.Helper()
	opSessID := "opsess-" + suffix
	userID = "user-" + suffix
	cliSessionID = "cli-" + suffix
	seedCLISessionDoc(t, h, cliSessionID, userID, opSessID)
	ctx = context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, opSessID)
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	ctx = context.WithValue(ctx, constants.ContextKeyCLISessionID, cliSessionID)
	return ctx, userID, cliSessionID
}

// sseAwaitTimeout bounds how long a stream test waits for an event it expects.
// It is a failure deadline, not a pacing interval: waits return the moment the
// recorder changes.
const sseAwaitTimeout = 10 * time.Second

// streamRecorder is a goroutine-safe ResponseWriter and Flusher. The stream
// handler writes from its own goroutine while the test waits on the body, so
// httptest.ResponseRecorder (unsynchronised) cannot be read mid-stream. Every
// write or flush wakes waiters, so tests block on stream events, not time.
type streamRecorder struct {
	header http.Header

	mu      sync.Mutex
	code    int
	body    strings.Builder
	flushes int
	changed chan struct{}
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{header: make(http.Header), changed: make(chan struct{})}
}

// Header is owned by the handler until the stream ends; read it after stop.
func (r *streamRecorder) Header() http.Header { return r.header }

func (r *streamRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = code
	}
	r.notifyLocked()
}

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = http.StatusOK
	}
	n, err := r.body.Write(p)
	r.notifyLocked()
	return n, err
}

func (r *streamRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = http.StatusOK
	}
	r.flushes++
	r.notifyLocked()
}

func (r *streamRecorder) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *streamRecorder) snapshot() (body string, flushes int, changed <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String(), r.flushes, r.changed
}

func (r *streamRecorder) Body() string {
	body, _, _ := r.snapshot()
	return body
}

func (r *streamRecorder) Code() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

// sseStream is a running SSE handler plus the recorder it writes to.
type sseStream struct {
	t      *testing.T
	rec    *streamRecorder
	cancel context.CancelFunc
	done   chan struct{}
}

// startSSEStream serves req on handler in a goroutine under a cancellable
// context. The stream is always stopped at test end.
func startSSEStream(t *testing.T, handler http.Handler, req *http.Request) *sseStream {
	t.Helper()
	streamCtx, cancel := context.WithCancel(req.Context())
	s := &sseStream{t: t, rec: newStreamRecorder(), cancel: cancel, done: make(chan struct{})}
	req = req.WithContext(streamCtx)
	go func() {
		defer close(s.done)
		handler.ServeHTTP(s.rec, req)
	}()
	t.Cleanup(func() { s.stop() })
	return s
}

// startInternalSSEStream serves req on the internal SSE stream handler.
func startInternalSSEStream(t *testing.T, h *HTTPHandler, req *http.Request) *sseStream {
	t.Helper()
	return startSSEStream(t, http.HandlerFunc(h.sseController.handleInternalSSEStream), req)
}

// await blocks until ok holds for the recorder, failing the test if the
// handler exits or sseAwaitTimeout elapses first.
func (s *sseStream) await(what string, ok func(body string, flushes int) bool) {
	s.t.Helper()
	timeout := time.NewTimer(sseAwaitTimeout)
	defer timeout.Stop()
	for {
		body, flushes, changed := s.rec.snapshot()
		if ok(body, flushes) {
			return
		}
		select {
		case <-changed:
		case <-s.done:
			// A final write may land between the snapshot and handler exit.
			if body, flushes, _ := s.rec.snapshot(); ok(body, flushes) {
				return
			}
			s.t.Fatalf("stream ended before %s\nbody: %s", what, body)
		case <-timeout.C:
			s.t.Fatalf("timed out after %s waiting for %s\nbody: %s", sseAwaitTimeout, what, body)
		}
	}
}

// awaitSubscribed waits for the first flush. The handler registers its pub/sub
// listener before that flush, so publishing afterwards cannot be missed.
func (s *sseStream) awaitSubscribed() {
	s.t.Helper()
	s.await("the stream to flush its subscription", func(_ string, flushes int) bool { return flushes >= 1 })
}

// awaitReplayed waits for the replay flush that follows the subscription flush.
// Streams that skip replay (since_id=0) never produce it; use awaitSubscribed.
func (s *sseStream) awaitReplayed() {
	s.t.Helper()
	s.await("the stream to finish replay", func(_ string, flushes int) bool { return flushes >= 2 })
}

// awaitBody waits until the body contains every fragment.
func (s *sseStream) awaitBody(fragments ...string) {
	s.t.Helper()
	s.await(fmt.Sprintf("body to contain %q", fragments), func(body string, _ int) bool {
		for _, f := range fragments {
			if !strings.Contains(body, f) {
				return false
			}
		}
		return true
	})
}

// stop cancels the stream, waits for the handler to return and yields the body.
func (s *sseStream) stop() string {
	s.cancel()
	<-s.done
	return s.rec.Body()
}

// errorWriter implements http.ResponseWriter but always returns an error on Write.
// It also implements http.Flusher as a no-op so the SSE handler can proceed.
type errorWriter struct {
	header http.Header
}

func (w *errorWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *errorWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("broken pipe: simulated write error")
}

func (w *errorWriter) WriteHeader(int) {}

func (w *errorWriter) Flush() {}
