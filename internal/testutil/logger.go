// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
)

// LogSink is a concurrency-safe text buffer for slog output captured in tests.
type LogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p to the sink.
func (s *LogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// String returns the text recorded so far.
func (s *LogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// NewBufferLogger returns a debug logger and the sink it writes. Assertions
// read the sink; discarded loggers cannot prove a log line was emitted.
func NewBufferLogger(tb testing.TB) (*slog.Logger, *LogSink) {
	tb.Helper()
	sink := &LogSink{}
	logger := slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, sink
}
