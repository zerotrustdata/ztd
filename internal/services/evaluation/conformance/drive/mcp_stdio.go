// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"

	"github.com/zerotrustdata/ztd/internal/cli/mcptransport"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// mcpStdioProtocolVersion is the MCP protocol version the driver asks for in
// initialize, the version the Gateway serves by default.
const mcpStdioProtocolVersion = "2025-06-18"

// mcpStdioMaxLine bounds one response line read from the bridge.
const mcpStdioMaxLine = 16 * 1024 * 1024

// MCPStdio submits attempts as tools/call through a `ztd mcp stdio` child, the
// bridge an agent spawns. The bridge proxies each call to the Gateway's MCP route
// over mTLS and, when a call suspends for L3, waits for the human decision itself
// before it answers. The executing Operator is the embedded one.
type MCPStdio struct {
	mu     sync.Mutex
	stdin  io.WriteCloser
	lines  <-chan stdioLine
	done   chan struct{}
	wait   func() error
	nextID int64
	once   sync.Once
	closed error
}

// stdioLine is one line the bridge wrote to stdout, or the read error that ended
// the stream.
type stdioLine struct {
	raw []byte
	err error
}

// StartMCPStdio starts cmd, which must run `ztd mcp stdio`, and runs the MCP
// handshake: initialize, notifications/initialized, then tools/list, which must
// serve the native tools the driver calls. The driver owns cmd's stdin and
// stdout; the caller sets its stderr. The child is stopped on any failure.
func StartMCPStdio(ctx context.Context, cmd *exec.Cmd) (*MCPStdio, error) {
	if cmd == nil {
		return nil, fmt.Errorf("mcp stdio: command: %w", constants.ErrMissingRequiredField)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp stdio: start %s: %w", cmd.Path, err)
	}
	m, err := ConnectMCPStdio(ctx, stdin, stdout, cmd.Wait)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return m, nil
}

// ConnectMCPStdio speaks newline-delimited JSON-RPC to a running bridge over its
// stdin and stdout and runs the handshake. wait reaps the bridge once stdin is
// closed. A failed handshake closes stdin; the caller stops the bridge.
func ConnectMCPStdio(ctx context.Context, stdin io.WriteCloser, stdout io.Reader, wait func() error) (*MCPStdio, error) {
	if stdin == nil || stdout == nil || wait == nil {
		return nil, fmt.Errorf("mcp stdio: bridge pipes: %w", constants.ErrMissingRequiredField)
	}
	lines := make(chan stdioLine)
	done := make(chan struct{})
	go readStdioLines(stdout, lines, done)
	m := &MCPStdio{stdin: stdin, lines: lines, done: done, wait: wait}
	if err := m.handshake(ctx); err != nil {
		close(m.done)
		_ = m.stdin.Close()
		return nil, err
	}
	return m, nil
}

// readStdioLines sends each stdout line until the stream ends or the driver closes.
func readStdioLines(r io.Reader, lines chan<- stdioLine, done <-chan struct{}) {
	defer close(lines)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), mcpStdioMaxLine)
	for scanner.Scan() {
		select {
		case lines <- stdioLine{raw: bytes.Clone(scanner.Bytes())}:
		case <-done:
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	select {
	case lines <- stdioLine{err: err}:
	case <-done:
	}
}

// handshake initializes the session and checks the served tools.
func (m *MCPStdio) handshake(ctx context.Context) error {
	init := map[string]any{
		"protocolVersion": mcpStdioProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ztd-conformance", "version": cases.SuiteVersion},
	}
	if _, err := m.result(ctx, "initialize", init); err != nil {
		return err
	}
	if err := m.notify("notifications/initialized"); err != nil {
		return err
	}
	result, err := m.result(ctx, "tools/list", struct{}{})
	if err != nil {
		return err
	}
	// Only names are read, as on the HTTP MCP route.
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &list); err != nil {
		return fmt.Errorf("mcp stdio: decode tools/list result: %w: %w", constants.ErrInvalidJSONResponse, err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, t := range list.Tools {
		names = append(names, t.Name)
	}
	return checkNativeTools(names)
}

// Drive calls one tool for the attempt and returns one observation.
func (m *MCPStdio) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	call, err := mcpCall(a)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("mcp stdio %s: %w", a.Case.Fault, err)
	}
	raw, err := m.call(ctx, "tools/call", call)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("mcp stdio %s: tools/call: %w", a.Case.Fault, err)
	}
	return cases.Driven{Observations: []*evalv1.ConformanceObservation{Observe(client.MCPStdioOutcome(raw))}}, nil
}

// Close closes the bridge's stdin, which ends its serve loop, and reaps it.
func (m *MCPStdio) Close() error {
	m.once.Do(func() {
		close(m.done)
		if err := m.stdin.Close(); err != nil {
			m.closed = fmt.Errorf("mcp stdio: close stdin: %w", err)
			return
		}
		var exitErr *exec.ExitError
		if err := m.wait(); err != nil && !errors.As(err, &exitErr) {
			m.closed = fmt.Errorf("mcp stdio: wait: %w", err)
		}
	})
	return m.closed
}

// result sends one request and returns its result, failing on a JSON-RPC error.
func (m *MCPStdio) result(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := m.call(ctx, method, params)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: %s: %w", method, err)
	}
	var resp struct {
		Result json.RawMessage            `json:"result"`
		Error  *mcptransport.JSONRPCError `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("mcp stdio: decode %s: %w: %w", method, constants.ErrInvalidJSONResponse, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp stdio: %s: %w: %s", method, constants.ErrJSONRPCErrorResponse, resp.Error.Message)
	}
	return resp.Result, nil
}

// call sends one request and returns the raw response line that carries its id.
// One request is in flight at a time. A line with another id answers a call whose
// context ended before its answer arrived, and is skipped.
func (m *MCPStdio) call(ctx context.Context, method string, params any) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := m.nextID
	if err := m.send(id, method, params); err != nil {
		return nil, err
	}
	want := strconv.FormatInt(id, 10)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case line, ok := <-m.lines:
			if !ok {
				return nil, fmt.Errorf("mcp stdio: bridge closed: %w", io.EOF)
			}
			if line.err != nil {
				return nil, fmt.Errorf("mcp stdio: read: %w", line.err)
			}
			var head struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(line.raw, &head); err != nil {
				return nil, fmt.Errorf("mcp stdio: decode response: %w: %w", constants.ErrInvalidJSONResponse, err)
			}
			if string(head.ID) == want {
				return line.raw, nil
			}
		}
	}
}

// notify sends a notification, which has no id and gets no answer.
func (m *MCPStdio) notify(method string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.send(0, method, nil)
}

// send writes one request line. id 0 sends a notification.
func (m *MCPStdio) send(id int64, method string, params any) error {
	req := mcptransport.JSONRPCRequest{JSONRPC: constants.JSONRPCVersion, Method: method}
	if id != 0 {
		req.ID = id
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("mcp stdio: marshal %s params: %w", method, err)
		}
		req.Params = raw
	}
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("mcp stdio: marshal %s: %w", method, err)
	}
	if _, err := m.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("mcp stdio: write %s: %w", method, err)
	}
	return nil
}
