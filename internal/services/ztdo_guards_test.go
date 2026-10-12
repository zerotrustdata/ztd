// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// capturingLogger returns a logger whose JSON records can be inspected, so
// tests assert on what the service actually reported.
func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func decodeLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var record map[string]any
		require.NoError(t, dec.Decode(&record))
		records = append(records, record)
	}
	return records
}

func TestZtdoService_Start_RejectsSecondStartWithoutDisturbingRunningService(t *testing.T) {
	service, err := NewZtdoService(testutil.NewTestConfig(t), testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestPubSubClientFactory())
	require.NoError(t, err)
	service.running = true

	err = service.Start(t.Context())

	require.ErrorIs(t, err, constants.ErrServiceUnavailable)
	assert.True(t, service.running, "a rejected second start must leave the service running")
	assert.Nil(t, service.cancel, "a rejected start must not create a new service context")
	assert.Nil(t, service.pubSubClient, "a rejected start must not open a second pub/sub client")
}

func TestZtdoService_Start_RequiresRuntimeFileService(t *testing.T) {
	service, err := NewZtdoService(testutil.NewTestConfig(t), testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), nil, newTestPubSubClientFactory())
	require.NoError(t, err)

	err = service.Start(t.Context())

	require.ErrorIs(t, err, constants.ErrInternal)
	assert.Contains(t, err.Error(), "fileSvc must be provided")
	assert.False(t, service.running)
	assert.Nil(t, service.pubSubClient, "nothing may be connected before the file service is validated")
	if service.cancel != nil {
		service.cancel()
	}
}

func TestZtdoService_Stop_NeverStartedServiceIsANoOp(t *testing.T) {
	logger, buf := capturingLogger()
	service, err := NewZtdoService(testutil.NewTestConfig(t), logger, newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	require.NoError(t, service.Stop(t.Context()))

	assert.False(t, service.running)
	for _, record := range decodeLogRecords(t, buf) {
		assert.NotEqual(t, "ztd Operator shutting down...", record["msg"], "stopping a service that never started must not run the shutdown sequence")
	}
}

func TestZtdoService_Stop_CancelsServiceContextAndMarksStopped(t *testing.T) {
	service, err := NewZtdoService(testutil.NewTestConfig(t), testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestPubSubClientFactory())
	require.NoError(t, err)
	service.ctx, service.cancel = context.WithCancel(t.Context())
	service.running = true

	require.NoError(t, service.Stop(t.Context()))

	assert.False(t, service.running)
	assert.Error(t, service.ctx.Err(), "stop must cancel the service context so background work exits")
	require.NoError(t, service.Stop(t.Context()), "a second stop is a no-op")
}

// outboundOperatorSourceGlobs locate the production sources of the outbound
// Operator (PEP), relative to this package directory (internal/services).
// The outbound Operator exposes no inbound listeners and must never depend on
// the Gateway's MCP service (see docs/architecture/operator.md).
var outboundOperatorSourceGlobs = []string{
	"ztdo*.go",
	"../cli/serve/operator*.go",
}

const mcpPackageSuffix = "internal/services/mcp"

// inboundListenerFuncs maps an import path to package-level functions that
// open an inbound listener or serve on one.
var inboundListenerFuncs = map[string]map[string]bool{
	"net":        {"Listen": true, "ListenTCP": true, "ListenPacket": true},
	"crypto/tls": {"Listen": true},
	"net/http":   {"ListenAndServe": true, "ListenAndServeTLS": true, "Serve": true},
}

// inboundListenerMethods are method names that open or serve an inbound
// listener when called on a value whose type cannot be resolved without
// go/types (for example (*http.Server).Serve or (*net.ListenConfig).Listen).
var inboundListenerMethods = map[string]bool{
	"Listen":            true,
	"ListenAndServe":    true,
	"ListenAndServeTLS": true,
	"Serve":             true,
}

// findOutboundOperatorViolations parses src as Go and reports every use of an
// inbound listener or the MCP gateway service. Imports are resolved by their
// local name, so aliased imports are still checked. Violations are formatted
// as "file:line:col: message". Comments and string literals are not reported.
func findOutboundOperatorViolations(filename string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	var violations []string
	report := func(pos token.Pos, format string, args ...any) {
		violations = append(violations, fmt.Sprintf("%s: %s", fset.Position(pos), fmt.Sprintf(format, args...)))
	}

	imports := make(map[string]string) // local package name -> import path
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("unquote import in %s: %w", filename, err)
		}
		isMCP := strings.HasSuffix(path, mcpPackageSuffix)
		_, isListenerPkg := inboundListenerFuncs[path]

		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			// A dot import would bring forbidden identifiers into scope unqualified.
			if isMCP || isListenerPkg {
				report(imp.Pos(), "dot-imports %s, hiding forbidden calls from the scanner", path)
			}
			continue
		}
		if name == "_" {
			continue
		}
		imports[name] = path
		if isMCP {
			report(imp.Pos(), "imports %s; the outbound Operator must not depend on the MCP gateway", path)
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		if ident, ok := sel.X.(*ast.Ident); ok {
			if path, imported := imports[ident.Name]; imported {
				if strings.HasSuffix(path, mcpPackageSuffix) && (name == "GatewayService" || name == "NewGatewayService") {
					report(sel.Pos(), "references mcp.%s; the outbound Operator must not construct the MCP gateway", name)
				}
				if inboundListenerFuncs[path][name] {
					report(sel.Pos(), "%s.%s opens an inbound listener; the outbound Operator must expose no inbound listeners", ident.Name, name)
				}
				return true
			}
		}
		// Receiver type is not an import: a method that serves or listens on an
		// inbound socket, such as (*http.Server).Serve or srv.ListenAndServe.
		if inboundListenerMethods[name] {
			report(sel.Pos(), "calls method %s, which may open an inbound listener; the outbound Operator must expose no inbound listeners", name)
		}
		return true
	})

	return violations, nil
}

// TestOutboundOperatorSource_OpensNoInboundListenersAndNoMCPGateway is a
// source-scan guard for the outbound Operator invariant: its production code
// opens no inbound listeners and never imports, constructs, or calls the MCP
// gateway service.
func TestOutboundOperatorSource_OpensNoInboundListenersAndNoMCPGateway(t *testing.T) {
	for _, pattern := range outboundOperatorSourceGlobs {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err)
		require.NotEmptyf(t, matches, "guard pattern %q matched no files; the outbound Operator sources moved", pattern)

		for _, file := range matches {
			if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
				continue
			}
			src, err := os.ReadFile(file)
			require.NoError(t, err)

			violations, err := findOutboundOperatorViolations(file, src)
			require.NoError(t, err)
			for _, v := range violations {
				t.Error(v)
			}
		}
	}
}

// TestOutboundOperatorSourceScanner_DetectsForbiddenPatterns proves the scanner
// used by the source guard detects each forbidden pattern, so a clean result
// from the guard means the production sources are clean.
func TestOutboundOperatorSourceScanner_DetectsForbiddenPatterns(t *testing.T) {
	const mcpImport = `"github.com/zerotrustdata/ztd/internal/services/mcp"`

	tests := []struct {
		name       string
		imports    string
		body       string
		want       int
		wantSubstr string
	}{
		{name: "net.Listen", imports: `"net"`, body: `ln, _ := net.Listen("tcp", ":0"); _ = ln`, want: 1, wantSubstr: "net.Listen"},
		{name: "net.ListenTCP", imports: `"net"`, body: `_, _ = net.ListenTCP("tcp", nil)`, want: 1, wantSubstr: "net.ListenTCP"},
		{name: "net.ListenPacket", imports: `"net"`, body: `_, _ = net.ListenPacket("udp", ":0")`, want: 1, wantSubstr: "net.ListenPacket"},
		{name: "aliased net import", imports: `n "net"`, body: `_, _ = n.Listen("tcp", ":0")`, want: 1, wantSubstr: "n.Listen"},
		{name: "tls.Listen", imports: `"crypto/tls"`, body: `_, _ = tls.Listen("tcp", ":0", nil)`, want: 1, wantSubstr: "tls.Listen"},
		{name: "http.ListenAndServe", imports: `"net/http"`, body: `_ = http.ListenAndServe(":0", nil)`, want: 1, wantSubstr: "http.ListenAndServe"},
		{name: "http.ListenAndServeTLS", imports: `"net/http"`, body: `_ = http.ListenAndServeTLS(":0", "c", "k", nil)`, want: 1, wantSubstr: "http.ListenAndServeTLS"},
		{name: "http.Serve", imports: `"net/http"`, body: `_ = http.Serve(nil, nil)`, want: 1, wantSubstr: "http.Serve"},
		{name: "(*http.Server).Serve method expression", imports: `"net/http"`, body: `_ = (*http.Server).Serve`, want: 1, wantSubstr: "Serve"},
		{name: "server value Serve call", imports: `"net/http"`, body: `srv := &http.Server{}; _ = srv.Serve(nil)`, want: 1, wantSubstr: "Serve"},
		{name: "ListenConfig Listen call", imports: `"context"` + "\n\t" + `"net"`, body: `var lc net.ListenConfig; _, _ = lc.Listen(context.Background(), "tcp", ":0")`, want: 1, wantSubstr: "Listen"},
		{name: "dot-import of net", imports: `. "net"`, body: `_, _ = Listen("tcp", ":0")`, want: 1, wantSubstr: "dot-imports"},
		{name: "mcp import", imports: mcpImport, body: ``, want: 1, wantSubstr: "imports"},
		{name: "mcp.GatewayService construction", imports: mcpImport, body: `_ = mcp.GatewayService{}`, want: 2, wantSubstr: "mcp.GatewayService"},
		{name: "mcp.NewGatewayService call", imports: mcpImport, body: `_ = mcp.NewGatewayService(nil)`, want: 2, wantSubstr: "mcp.NewGatewayService"},
		{name: "listener mentioned only in comment and string", imports: `"net"`, body: "// net.Listen is forbidden here\n\t_ = \"net.Listen\"", want: 0},
		{name: "clean outbound code", imports: `"context"`, body: `_ = context.Background()`, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package serve\n\nimport (\n\t" + tt.imports + "\n)\n\nfunc run() {\n\t" + tt.body + "\n}\n"

			violations, err := findOutboundOperatorViolations("fixture.go", []byte(src))
			require.NoError(t, err)

			assert.Len(t, violations, tt.want, "violations: %v", violations)
			if tt.wantSubstr != "" {
				var matched bool
				for _, v := range violations {
					if strings.Contains(v, tt.wantSubstr) {
						matched = true
					}
				}
				assert.True(t, matched, "expected a violation mentioning %q, got %v", tt.wantSubstr, violations)
			}
		})
	}
}

func TestOutboundOperatorSourceScanner_RejectsUnparseableSource(t *testing.T) {
	_, err := findOutboundOperatorViolations("broken.go", []byte("package"))
	require.Error(t, err, "an unparseable source must fail the guard, not pass silently")
}
