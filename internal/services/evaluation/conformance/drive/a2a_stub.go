// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// a2aStubResult is the success body the stub answers with. It is the shape
// mcp.DispatchToA2ADownstream decodes.
type a2aStubResult struct {
	Result string `json:"result"`
}

// A2AStub is the harness-owned A2A downstream the Gateway calls. It answers
// every call with a fixed success and records each call, so the stub is the
// effect observer for A2A cases.
type A2AStub struct {
	listener net.Listener
	server   *http.Server
	done     chan error

	mu sync.Mutex
	// current is the attempt whose calls are being recorded, or "" when none is open.
	current string
	// attempts holds the calls recorded under each attempt ID.
	attempts map[string][]*evalv1.ConformanceA2ACall
	// stray holds calls that arrived with no attempt open. The next End returns them,
	// so a stray call shows up in the next case's evidence (where the verifier fails it).
	stray []*evalv1.ConformanceA2ACall
}

// StartA2AStub listens on addr (e.g. "127.0.0.1:0") and serves the A2A
// downstream contract the Gateway calls. URL() is what the Gateway's
// --a2a-downstream-url must be set to.
func StartA2AStub(addr string) (*A2AStub, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("a2a stub: listen %s: %w", addr, err)
	}
	s := &A2AStub{
		listener: listener,
		done:     make(chan error, 1),
		attempts: make(map[string][]*evalv1.ConformanceA2ACall),
	}
	s.server = &http.Server{Handler: http.HandlerFunc(s.serve)}
	go func() { s.done <- s.server.Serve(listener) }()
	return s, nil
}

// URL returns the base URL the Gateway's --a2a-downstream-url must be set to.
func (s *A2AStub) URL() string {
	return "http://" + s.listener.Addr().String()
}

// Close stops the listener and waits for the server goroutine to return. It is
// called once, by the owner of the stub.
func (s *A2AStub) Close(ctx context.Context) error {
	shutdownErr := s.server.Shutdown(ctx)
	serveErr := <-s.done
	if shutdownErr != nil {
		return fmt.Errorf("a2a stub: shutdown: %w", shutdownErr)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("a2a stub: serve: %w", serveErr)
	}
	return nil
}

// Begin opens attemptID. Calls recorded until End are attributed to it.
func (s *A2AStub) Begin(attemptID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = attemptID
}

// End closes attemptID and returns the calls recorded for it, plus any stray
// calls that arrived while no attempt was open.
func (s *A2AStub) End(attemptID string) []*evalv1.ConformanceA2ACall {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := append(s.stray, s.attempts[attemptID]...)
	s.stray = nil
	delete(s.attempts, attemptID)
	if s.current == attemptID {
		s.current = ""
	}
	return calls
}

// serve answers one downstream call. A body that does not decode strictly into
// the downstream request is a 400 and is not recorded.
func (s *A2AStub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req mcp.A2ADownstreamRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "malformed A2A downstream request", http.StatusBadRequest)
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		http.Error(w, "malformed A2A downstream request: trailing data", http.StatusBadRequest)
		return
	}
	s.record(&evalv1.ConformanceA2ACall{SkillName: req.SkillName, PayloadJson: string(req.Payload)})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(a2aStubResult{Result: "ok"})
}

// record stores one call under the open attempt, or as a stray call.
func (s *A2AStub) record(call *evalv1.ConformanceA2ACall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == "" {
		s.stray = append(s.stray, call)
		return
	}
	s.attempts[s.current] = append(s.attempts[s.current], call)
}
