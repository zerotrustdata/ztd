// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package logging

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRootFromLoggingPackage is the repository root relative to this package
// directory (internal/services/logging).
const repoRootFromLoggingPackage = "../../../"

// logContentGuardDirs are the execution-path packages whose logger calls the
// guard scans. They handle command lines, output, file paths, tool arguments,
// envelopes and inference prompts, none of which may reach the plaintext log
// (INV-STOR-ENC-01: execution content is encrypted at rest, or not written).
var logContentGuardDirs = []string{
	"internal/services/execution",
	"internal/services/pubsub",
	"internal/services/governance",
	"internal/services/inference",
	"internal/services/mcp",
	"internal/services/storage",
}

// forbiddenLogKeys are attribute keys that carry execution content. Log an
// identifier (execution_id, transaction_id, operator_session_id), a size or
// count, or logging.ErrorKind(err) instead.
var forbiddenLogKeys = map[string]bool{
	"command":         true,
	"cmd":             true,
	"args":            true,
	"arguments":       true,
	"tool_arguments":  true,
	"stdout":          true,
	"stderr":          true,
	"stdout_preview":  true,
	"stderr_preview":  true,
	"output":          true,
	"content":         true,
	"prompt":          true,
	"line":            true,
	"file":            true,
	"file_path":       true,
	"filepath":        true,
	"path":            true,
	"working_dir":     true,
	"target":          true,
	"target_resource": true,
	"pattern":         true,
	"diff":            true,
	"diff_stat":       true,
	"summary":         true,
	"violations":      true,
	"error_message":   true,
}

// logContentAllowlist maps a repository-relative source file to the forbidden
// keys it may log, with the reason. The keys here describe runtime
// infrastructure, not execution content. A stale entry (no remaining hit) fails.
var logContentAllowlist = map[string]map[string]string{
	"internal/services/governance/actuator_pub_export.go": {"path": "location of the exported Actuator public key under the runtime root"},
	"internal/services/storage/audit_store.go":            {"path": "runtime data directory write probe"},
	"internal/services/mcp/gateway.go":                    {"target": "configured downstream MCP target of the circuit breaker"},
}

// loggerMethods are the leveled logging methods. The context forms take a
// context.Context before the message.
var loggerMethods = map[string]int{
	"Debug": 0, "Info": 0, "Warn": 0, "Error": 0,
	"DebugContext": 1, "InfoContext": 1, "WarnContext": 1, "ErrorContext": 1,
}

// logContentHit is one violation: a forbidden key, or a non-literal message.
type logContentHit struct {
	line int
	what string
}

// loadStringConstants maps every string constant declared under dir to its
// value, so string(constants.X) attribute keys resolve to their text.
func loadStringConstants(dir string) (map[string]string, error) {
	values := map[string]string{}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read constants dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse constants %s: %w", entry.Name(), err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							values[name.Name] = v
						}
					}
				}
			}
		}
	}
	return values, nil
}

// resolveLogKey returns the text of an attribute-key expression: a string
// literal, string(constants.X), or constants.X.
func resolveLogKey(expr ast.Expr, constants map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			v, err := strconv.Unquote(e.Value)
			return v, err == nil
		}
	case *ast.CallExpr:
		if ident, ok := e.Fun.(*ast.Ident); ok && ident.Name == "string" && len(e.Args) == 1 {
			return resolveLogKey(e.Args[0], constants)
		}
	case *ast.SelectorExpr:
		v, ok := constants[e.Sel.Name]
		return v, ok
	}
	return "", false
}

// isLiteralMessage reports whether the message is a string literal or a
// concatenation of literals. A computed message (a variable, fmt.Sprintf) can
// carry content, such as a raw output line.
func isLiteralMessage(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.BinaryExpr:
		return e.Op == token.ADD && isLiteralMessage(e.X) && isLiteralMessage(e.Y)
	case *ast.ParenExpr:
		return isLiteralMessage(e.X)
	}
	return false
}

// attrCallKey returns the key of a typed attribute call (slog.String("key", v),
// slog.Any, ...). ok is false when expr is not such a call.
func attrCallKey(expr ast.Expr, constants map[string]string) (key string, isAttr bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	switch {
	case pkg.Name == "slog" && len(call.Args) >= 1:
		k, _ := resolveLogKey(call.Args[0], constants)
		return k, true
	case pkg.Name == "logging" && sel.Sel.Name == "ErrorKind":
		return ErrorKindKey, true
	}
	return "", false
}

// findLogContentHits parses src and reports every leveled logger call whose
// attribute keys are forbidden or whose message is not a literal. A logger call
// is a Debug/Info/Warn/Error method on a receiver whose name contains "log".
func findLogContentHits(filename string, src []byte, constants map[string]string) ([]logContentHit, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	var hits []logContentHit
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		msgIdx, isLevel := loggerMethods[sel.Sel.Name]
		if !isLevel || !receiverLooksLikeLogger(sel.X) || len(call.Args) <= msgIdx {
			return true
		}
		line := fset.Position(call.Pos()).Line
		if !isLiteralMessage(call.Args[msgIdx]) {
			hits = append(hits, logContentHit{line: line, what: "non-literal message"})
		}
		args := call.Args[msgIdx+1:]
		for i := 0; i < len(args); {
			if key, isAttr := attrCallKey(args[i], constants); isAttr {
				if forbiddenLogKeys[key] {
					hits = append(hits, logContentHit{line: line, what: key})
				}
				i++
				continue
			}
			if key, ok := resolveLogKey(args[i], constants); ok && forbiddenLogKeys[key] {
				hits = append(hits, logContentHit{line: line, what: key})
			}
			i += 2
		}
		return true
	})
	return hits, nil
}

// receiverLooksLikeLogger reports whether the call receiver is a logger by name
// (logger, Logger, s.logger, slog, ...), matching how the repository names them.
func receiverLooksLikeLogger(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return strings.Contains(strings.ToLower(e.Name), "log")
	case *ast.SelectorExpr:
		return strings.Contains(strings.ToLower(e.Sel.Name), "log") || receiverLooksLikeLogger(e.X)
	}
	return false
}

// TestLoggerCallsCarryNoExecutionContent is a source-scan guard for
// INV-STOR-LOG-01: logger calls in the execution-path packages never use
// content attribute keys and never build the message from runtime values.
func TestLoggerCallsCarryNoExecutionContent(t *testing.T) {
	root, err := filepath.Abs(repoRootFromLoggingPackage)
	require.NoError(t, err)
	constants, err := loadStringConstants(filepath.Join(root, "internal", "constants"))
	require.NoError(t, err)

	hitsByFile := map[string][]logContentHit{}
	for _, dir := range logContentGuardDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hits, err := findLogContentHits(rel, src, constants)
			if err != nil {
				return err
			}
			if len(hits) > 0 {
				hitsByFile[rel] = hits
			}
			return nil
		})
		require.NoError(t, err)
	}

	files := make([]string, 0, len(hitsByFile))
	for file := range hitsByFile {
		files = append(files, file)
	}
	sort.Strings(files)

	var failures []string
	for _, file := range files {
		allowed := logContentAllowlist[file]
		for _, hit := range hitsByFile[file] {
			if _, ok := allowed[hit.what]; ok {
				continue
			}
			failures = append(failures, fmt.Sprintf("%s:%d %s", file, hit.line, hit.what))
		}
	}

	allowlisted := make([]string, 0, len(logContentAllowlist))
	for file := range logContentAllowlist {
		allowlisted = append(allowlisted, file)
	}
	sort.Strings(allowlisted)
	for _, file := range allowlisted {
		seen := map[string]bool{}
		for _, hit := range hitsByFile[file] {
			seen[hit.what] = true
		}
		keys := make([]string, 0, len(logContentAllowlist[file]))
		for key := range logContentAllowlist[file] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if strings.TrimSpace(logContentAllowlist[file][key]) == "" {
				failures = append(failures, fmt.Sprintf("%s: allowlist entry %q needs a reason", file, key))
			}
			if !seen[key] {
				failures = append(failures, fmt.Sprintf("%s: stale allowlist entry %q, no hit remains; remove it", file, key))
			}
		}
	}

	if len(failures) > 0 {
		t.Errorf("logger calls carry execution content (INV-STOR-LOG-01); log an identifier, a size or count, or logging.ErrorKind(err):\n%s",
			strings.Join(failures, "\n"))
	}
}

// TestLogContentScanner_DetectsForbiddenPatterns proves the scanner flags each
// forbidden pattern, so a clean guard result means the sources are clean.
func TestLogContentScanner_DetectsForbiddenPatterns(t *testing.T) {
	constants := map[string]string{"ApprovalTypeCommand": "command", "ConnectionStateError": "error"}
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"literal command key", `l.logger.Info("run", "command", cmd)`, []string{"command"}},
		{"stdout key", `s.logger.Debug("out", "execution_id", id, "stdout", out)`, []string{"stdout"}},
		{"args key", `logger.Warn("x", "args", a)`, []string{"args"}},
		{"tool arguments key", `logger.Warn("x", "tool_arguments", a)`, []string{"tool_arguments"}},
		{"prompt key", `logger.Error("x", "prompt", p)`, []string{"prompt"}},
		{"content key", `logger.Info("x", "content", c)`, []string{"content"}},
		{"file path key", `logger.Info("x", "file_path", p)`, []string{"file_path"}},
		{"constant key resolves", `logger.Info("x", string(constants.ApprovalTypeCommand), cmd)`, []string{"command"}},
		{"slog attr key", `logger.Info("x", slog.String("stderr", e))`, []string{"stderr"}},
		{"context form", `logger.InfoContext(ctx, "x", "command", c)`, []string{"command"}},
		{"computed message", `logger.Info(line, "execution_id", id)`, []string{"non-literal message"}},
		{"sprintf message", `logger.Info(fmt.Sprintf("ran %s", cmd))`, []string{"non-literal message"}},
		{"value named like a key is fine", `logger.Info("x", "execution_id", command)`, nil},
		{"identifier keys are fine", `logger.Info("x", "execution_id", id, "stdout_size", n, string(constants.ConnectionStateError), err, logging.ErrorKind(err))`, nil},
		{"non logger receiver is ignored", `t.Info("x", "command", c)`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\nfunc f() {\n" + tt.body + "\n}\n"
			hits, err := findLogContentHits("p.go", []byte(src), constants)
			require.NoError(t, err)
			var got []string
			for _, hit := range hits {
				got = append(got, hit.what)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "none"},
		{"path error carries no path", &fs.PathError{Op: "open", Path: "/home/user/secret.txt", Err: fs.ErrNotExist}, "not_found"},
		{"permission", fmt.Errorf("read /etc/shadow: %w", fs.ErrPermission), "permission"},
		{"exists", fs.ErrExist, "exists"},
		{"other", errors.New("exec: \"curl https://example.invalid/secret\": not found"), "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := ErrorKind(tt.err)
			assert.Equal(t, ErrorKindKey, attr.Key)
			assert.Equal(t, tt.want, attr.Value.String())
		})
	}
}
