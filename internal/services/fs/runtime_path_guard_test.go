// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRootFromFSPackage is the repository root relative to this package
// directory (internal/services/fs).
const repoRootFromFSPackage = "../../../"

// runtimeStateOwnerDir is the only package allowed to perform raw runtime I/O:
// it is the owner of .ztd/ state (INV-FS-01).
const runtimeStateOwnerDir = "internal/services/fs"

// rawOSFuncs are the os package functions that touch the filesystem by path.
var rawOSFuncs = map[string]bool{
	"ReadFile":  true,
	"WriteFile": true,
	"Open":      true,
	"OpenFile":  true,
	"Create":    true,
	"Mkdir":     true,
	"MkdirAll":  true,
	"Remove":    true,
	"RemoveAll": true,
	"Rename":    true,
	"Stat":      true,
	"Lstat":     true,
	"Chmod":     true,
	"ReadDir":   true,
	"Symlink":   true,
	"Readlink":  true,
	"Truncate":  true,
	"OpenRoot":  true,
	"DirFS":     true,
}

// rawFilepathFuncs are the path/filepath functions that walk or match the
// filesystem by path. The first argument is the checked path.
var rawFilepathFuncs = map[string]bool{
	"Walk":    true,
	"WalkDir": true,
	"Glob":    true,
}

// runtimeStateConstants are the internal/constants names that denote .ztd/
// runtime directories and database files. Sourced from internal/constants/paths.go:
// RuntimeDirname, DataDirname, VaultDirname, SecretsDirname, PkiDirname, and
// every database filename (*DbFilename, *DBFilename).
var runtimeStateConstants = map[string]bool{
	"RuntimeDirname":  true,
	"DataDirname":     true,
	"VaultDirname":    true,
	"SecretsDirname":  true,
	"PkiDirname":      true,
	"DbFilename":      true,
	"ChaosDBFilename": true,
}

// rawOSRuntimeAccessAllowlist maps a repository-relative source file to the
// reason its raw os access to runtime paths is legitimate. A stale entry (no
// remaining hit) fails the guard.
var rawOSRuntimeAccessAllowlist = map[string]string{
	"internal/cli/cmd/gw/workload_identities.go": "stats the trust bundle of another runtime's workload directory (absolute, from the workload registry); RuntimeFileService is rooted at the running runtime and cannot address it",
	"internal/cli/identityreset/reset.go":        "Lstat and os.OpenRoot confine the destructive reset to the runtime directory and refuse symlinks; intentionally stronger than RuntimeFileService.RemoveAll, which is not root-confined",
}

// rawOSHit is one raw os or sql.Open call whose arguments reference runtime state.
type rawOSHit struct {
	line int
	call string // "os.ReadFile", "sql.Open", ...
}

// rawOSCallee reports the package and function name for os.F(...) and sql.Open(...).
func rawOSCallee(call *ast.CallExpr) (pkg, name string, ok bool) {
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	ident, isIdent := sel.X.(*ast.Ident)
	if !isIdent {
		return "", "", false
	}
	switch {
	case ident.Name == "os" && rawOSFuncs[sel.Sel.Name]:
		return "os", sel.Sel.Name, true
	case ident.Name == "filepath" && rawFilepathFuncs[sel.Sel.Name]:
		return "filepath", sel.Sel.Name, true
	case ident.Name == "sql" && sel.Sel.Name == "Open":
		return "sql", "Open", true
	}
	return "", "", false
}

// Import paths whose local aliases the rule resolves, so an aliased import
// such as `runtimepaths "github.com/.../internal/constants"` is still checked.
const (
	constantsImportPath = "github.com/zerotrustdata/ztd/internal/constants"
	pathsImportPath     = "github.com/zerotrustdata/ztd/internal/paths"
)

// runtimeStateRefs holds the local identifiers that refer to the constants and
// paths packages in one file.
type runtimeStateRefs struct {
	constantsNames map[string]bool
	pathsNames     map[string]bool
}

// runtimeStateRefsFor resolves the local names of the constants and paths
// imports. A file that imports neither falls back to the conventional names.
func runtimeStateRefsFor(file *ast.File) (runtimeStateRefs, error) {
	refs := runtimeStateRefs{constantsNames: map[string]bool{}, pathsNames: map[string]bool{}}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return refs, fmt.Errorf("unquote import: %w", err)
		}
		var target map[string]bool
		var defaultName string
		switch path {
		case constantsImportPath:
			target, defaultName = refs.constantsNames, "constants"
		case pathsImportPath:
			target, defaultName = refs.pathsNames, "paths"
		default:
			continue
		}
		name := defaultName
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name != "_" && name != "." {
			target[name] = true
		}
	}
	if len(refs.constantsNames) == 0 {
		refs.constantsNames["constants"] = true
	}
	if len(refs.pathsNames) == 0 {
		refs.pathsNames["paths"] = true
	}
	return refs, nil
}

// isRuntimeStateSelector reports whether sel is paths.Infra.<field> or
// constants.<runtime name>, using the file's resolved local names.
func (refs runtimeStateRefs) isRuntimeStateSelector(sel *ast.SelectorExpr) bool {
	switch x := sel.X.(type) {
	case *ast.Ident:
		return refs.constantsNames[x.Name] && runtimeStateConstants[sel.Sel.Name]
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		return ok && refs.pathsNames[pkg.Name] && x.Sel.Name == "Infra"
	}
	return false
}

// exprReferencesRuntimeState reports whether the expression subtree contains a
// runtime-state selector.
func (refs runtimeStateRefs) exprReferencesRuntimeState(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && refs.isRuntimeStateSelector(sel) {
			found = true
			return false
		}
		return true
	})
	return found
}

// exprReferencesNames reports whether the expression subtree mentions any of the
// names as a variable. The field of a selector (x.name) is not a reference.
func exprReferencesNames(expr ast.Expr, names map[string]bool) bool {
	if len(names) == 0 {
		return false
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if exprReferencesNames(v.X, names) {
				found = true
			}
			return false
		case *ast.Ident:
			if names[v.Name] {
				found = true
			}
		}
		return true
	})
	return found
}

// correspondingRHS returns the right-hand expression that feeds position i. A
// single multi-value right-hand side (x, err := f(...)) feeds every position.
func correspondingRHS(rhs []ast.Expr, i int) ast.Expr {
	if len(rhs) == 1 {
		return rhs[0]
	}
	if i < len(rhs) {
		return rhs[i]
	}
	return nil
}

// runtimeTaintedNames returns the identifiers assigned from a runtime-state
// expression anywhere in body (one level: x := paths.Infra.X taints x, but
// y := x does not taint y).
func (refs runtimeStateRefs) runtimeTaintedNames(body ast.Node) map[string]bool {
	tainted := make(map[string]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if rhs := correspondingRHS(s.Rhs, i); rhs != nil && refs.exprReferencesRuntimeState(rhs) {
					tainted[ident.Name] = true
				}
			}
		case *ast.ValueSpec:
			for i, name := range s.Names {
				if rhs := correspondingRHS(s.Values, i); rhs != nil && refs.exprReferencesRuntimeState(rhs) {
					tainted[name.Name] = true
				}
			}
		}
		return true
	})
	return tainted
}

// findRawOSRuntimeAccess parses src as Go and reports every os.F(...) or
// sql.Open(...) call whose arguments reference .ztd/ runtime state, either
// directly or through one level of local assignment. Generated files (first
// comment contains "Code generated") are not reported.
func findRawOSRuntimeAccess(filename string, src []byte) ([]rawOSHit, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}
	if len(file.Comments) > 0 && strings.Contains(file.Comments[0].Text(), "Code generated") {
		return nil, nil
	}
	refs, err := runtimeStateRefsFor(file)
	if err != nil {
		return nil, fmt.Errorf("resolve imports in %s: %w", filename, err)
	}

	var hits []rawOSHit
	scan := func(scope ast.Node, tainted map[string]bool) {
		ast.Inspect(scope, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			pkg, name, ok := rawOSCallee(call)
			if !ok {
				return true
			}
			for _, arg := range call.Args {
				if refs.exprReferencesRuntimeState(arg) || exprReferencesNames(arg, tainted) {
					hits = append(hits, rawOSHit{line: fset.Position(call.Pos()).Line, call: pkg + "." + name})
					break
				}
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Body != nil {
				scan(fn.Body, refs.runtimeTaintedNames(fn.Body))
			}
			continue
		}
		// Package-level declarations have no local flow; check them with no taint.
		scan(decl, nil)
	}
	return hits, nil
}

// TestNoRawOSAccessToRuntimePaths is a source-scan guard for INV-FS-01: no
// production code outside internal/services/fs performs raw os or sql.Open
// calls on .ztd/ runtime state. Such state must go through fs.RuntimeFileService.
func TestNoRawOSAccessToRuntimePaths(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(repoRootFromFSPackage)
	require.NoError(t, err)

	hitsByFile := make(map[string][]rawOSHit)
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == "vendor" || rel == runtimeStateOwnerDir {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hits, err := findRawOSRuntimeAccess(rel, src)
			if err != nil {
				return err
			}
			if len(hits) > 0 {
				hitsByFile[rel] = append(hitsByFile[rel], hits...)
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
		if _, allowed := rawOSRuntimeAccessAllowlist[file]; allowed {
			continue
		}
		for _, hit := range hitsByFile[file] {
			failures = append(failures, fmt.Sprintf("%s:%d %s", file, hit.line, hit.call))
		}
	}

	allowlisted := make([]string, 0, len(rawOSRuntimeAccessAllowlist))
	for file := range rawOSRuntimeAccessAllowlist {
		allowlisted = append(allowlisted, file)
	}
	sort.Strings(allowlisted)
	for _, file := range allowlisted {
		if strings.TrimSpace(rawOSRuntimeAccessAllowlist[file]) == "" {
			failures = append(failures, fmt.Sprintf("%s: allowlist entry needs a reason", file))
		}
		if len(hitsByFile[file]) == 0 {
			failures = append(failures, fmt.Sprintf("%s: stale allowlist entry, no raw os runtime access remains; remove it", file))
		}
	}

	if len(failures) > 0 {
		t.Errorf("raw os access to .ztd/ runtime state outside internal/services/fs (INV-FS-01); route it through fs.RuntimeFileService:\n%s",
			strings.Join(failures, "\n"))
	}
}

// TestRawOSRuntimeAccessScanner_DetectsForbiddenPatterns proves the scanner
// flags each forbidden pattern, so a clean guard result means the sources are clean.
func TestRawOSRuntimeAccessScanner_DetectsForbiddenPatterns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		want     int
		wantCall string
	}{
		{name: "os.ReadFile on paths.Infra", body: `_, _ = os.ReadFile(paths.Infra.DbPath)`, want: 1, wantCall: "os.ReadFile"},
		{name: "os.MkdirAll on constant", body: `_ = os.MkdirAll(filepath.Join(constants.RuntimeDirname, "x"), 0o700)`, want: 1, wantCall: "os.MkdirAll"},
		{name: "os.OpenRoot on paths.Infra", body: `_, _ = os.OpenRoot(paths.Infra.DataDir)`, want: 1, wantCall: "os.OpenRoot"},
		{name: "sql.Open on DSN", body: `_, _ = sql.Open("sqlite", paths.Infra.DbPath)`, want: 1, wantCall: "sql.Open"},
		{name: "filepath.WalkDir on paths.Infra", body: `_ = filepath.WalkDir(paths.Infra.DataDir, func(string, os.DirEntry, error) error { return nil })`, want: 1, wantCall: "filepath.WalkDir"},
		{name: "os.DirFS on constant", body: `_ = os.DirFS(filepath.Join(constants.RuntimeDirname, "x"))`, want: 1, wantCall: "os.DirFS"},
		{name: "direct assignment then os call", body: "p := paths.Infra.DbPath\n\t_ = os.Remove(p)", want: 1, wantCall: "os.Remove"},
		{name: "var assignment then os call", body: "var p = filepath.Join(paths.Infra.SecretsDir, \"k\")\n\t_, _ = os.ReadFile(p)", want: 1, wantCall: "os.ReadFile"},
		{name: "multi-value assignment then os call", body: "p, err := f(constants.SecretsDirname)\n\t_ = err\n\t_, _ = os.Stat(p)", want: 1, wantCall: "os.Stat"},
		{name: "runtime call inside func literal", body: "func() { _, _ = os.ReadFile(paths.Infra.DbPath) }()", want: 1, wantCall: "os.ReadFile"},
		{name: "two-level flow is not followed", body: "a := paths.Infra.DataDir\n\tb := a\n\t_, _ = os.ReadFile(b)", want: 0},
		{name: "non-runtime constant", body: `_, _ = os.ReadFile(constants.PathEtcHosts)`, want: 0},
		{name: "repository-relative path", body: `_, _ = os.ReadFile("eval/base-model-inventory.json")`, want: 0},
		{name: "selector field is not a variable reference", body: "var x struct{ p string }\n\t_, _ = os.ReadFile(x.p)\n\tp := paths.Infra.DbPath\n\t_ = p", want: 0},
		{name: "runtime reference not passed to os", body: `_ = paths.Infra.DbPath`, want: 0},
		{name: "runtime path only in comment and string", body: "// os.ReadFile(paths.Infra.DbPath)\n\t_ = \"os.ReadFile(paths.Infra.DbPath)\"", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := "package x\n\nfunc f() {\n\t" + tt.body + "\n}\n"

			hits, err := findRawOSRuntimeAccess("fixture.go", []byte(src))
			require.NoError(t, err)

			assert.Len(t, hits, tt.want, "hits: %v", hits)
			if tt.wantCall != "" && len(hits) > 0 {
				assert.Equal(t, tt.wantCall, hits[0].call)
			}
		})
	}
}

func TestRawOSRuntimeAccessScanner_SkipsGeneratedFiles(t *testing.T) {
	t.Parallel()
	src := "// Code generated by a tool. DO NOT EDIT.\n\npackage x\n\nimport \"os\"\n\nfunc f() { _, _ = os.ReadFile(paths.Infra.DbPath) }\n"

	hits, err := findRawOSRuntimeAccess("generated.go", []byte(src))
	require.NoError(t, err)
	assert.Empty(t, hits, "generated sources are owned by their generator")
}

func TestRawOSRuntimeAccessScanner_RejectsUnparseableSource(t *testing.T) {
	t.Parallel()
	_, err := findRawOSRuntimeAccess("broken.go", []byte("package"))
	require.Error(t, err, "an unparseable source must fail the guard, not pass silently")
}

func TestRawOSRuntimeAccessScanner_ResolvesImportAliases(t *testing.T) {
	t.Parallel()
	src := "package x\n\nimport (\n\truntimepaths \"github.com/zerotrustdata/ztd/internal/constants\"\n\tnotpaths \"github.com/zerotrustdata/ztd/internal/paths\"\n)\n\nfunc f() {\n\t_, _ = os.ReadFile(runtimepaths.RuntimeDirname)\n\t_, _ = os.ReadFile(paths.Infra.DbPath)\n\t_, _ = os.ReadFile(notpaths.Infra.DbPath)\n}\n"

	hits, err := findRawOSRuntimeAccess("aliased.go", []byte(src))
	require.NoError(t, err)
	// Line 9 (runtimepaths) and line 11 (notpaths, an alias of internal/paths) match.
	// Line 10 (paths) is not imported in this file, so it must not match.
	require.Len(t, hits, 2, "only the resolved aliases may match: %v", hits)
	assert.Equal(t, 9, hits[0].line)
	assert.Equal(t, 11, hits[1].line)
}
