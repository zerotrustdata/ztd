// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package profiling serves Go runtime profiles (CPU, heap, allocs, goroutine,
// mutex, block, execution trace) on an opt-in, loopback-only listener.
package profiling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

const (
	// mutexProfileFraction samples 1 in N mutex contention events.
	mutexProfileFraction = 10
	// blockProfileRateNanos samples about one blocking event per this many
	// nanoseconds spent blocked, so short channel waits stay cheap.
	blockProfileRateNanos = int(time.Millisecond)
	// writeTimeout must exceed the longest requested CPU profile or trace;
	// pprof rejects ?seconds= values at or above the server WriteTimeout.
	writeTimeout      = 5 * time.Minute
	readHeaderTimeout = 5 * time.Second
)

// Server is a running loopback pprof listener.
type Server struct {
	srv    *http.Server
	ln     net.Listener
	logger *slog.Logger
	done   chan struct{}
}

// ValidateListenAddress accepts host:port where host is localhost or a
// loopback IP. Unspecified hosts (0.0.0.0, ::) are rejected: profiles expose
// command lines, heap contents, and goroutine stacks.
func ValidateListenAddress(addr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("%w: %q: %w", constants.ErrProfilingListenAddress, addr, err)
	}
	if strings.EqualFold(host, constants.LocalhostHostname) {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: %q", constants.ErrProfilingListenAddress, addr)
}

// Start binds addr, enables mutex and block sampling, and serves
// /debug/pprof/ until Stop.
func Start(addr string, logger *slog.Logger) (*Server, error) {
	if err := ValidateListenAddress(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", strings.TrimSpace(addr))
	if err != nil {
		return nil, fmt.Errorf("profiling: listen %s: %w", addr, err)
	}

	runtime.SetMutexProfileFraction(mutexProfileFraction)
	runtime.SetBlockProfileRate(blockProfileRateNanos)

	s := &Server{
		srv: &http.Server{
			Handler:           newMux(),
			ReadHeaderTimeout: readHeaderTimeout,
			WriteTimeout:      writeTimeout,
		},
		ln:     ln,
		logger: logger,
		done:   make(chan struct{}),
	}
	go s.serve()
	logger.Info("pprof profiling listener started", "addr", s.Addr())
	return s, nil
}

// newMux registers pprof handlers on a private mux. The net/http/pprof import
// also registers on http.DefaultServeMux, which no ztd listener serves.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func (s *Server) serve() {
	defer close(s.done)
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.logger.Error("pprof profiling listener failed", "error", err)
	}
}

// Addr returns the bound listener address.
func (s *Server) Addr() string {
	return s.ln.Addr().String()
}

// Stop shuts the listener down and disables mutex and block sampling.
func (s *Server) Stop(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	<-s.done
	runtime.SetMutexProfileFraction(0)
	runtime.SetBlockProfileRate(0)
	if err != nil {
		return fmt.Errorf("profiling: shutdown: %w", err)
	}
	return nil
}
