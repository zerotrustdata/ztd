// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// tierRoots are the module directories the tier check indexes and checks.
var tierRoots = []string{"internal", "cmd", "test", "protocol"}

// Check source rather than opening a listener to test tier enforcement. This
// also checks platform-specific files that the current host does not compile.
func TestTestTiers_ListenersRequireIntegrationAndBoundaryTestsStaySerial(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test sources")
	}
	root := filepath.Join(filepath.Dir(filename), "..", "..")
	modulePath, src, err := loadTierModule(root)
	if err != nil {
		t.Fatal(err)
	}
	violations, err := tierListenerViolations(root, modulePath, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
	fset := token.NewFileSet()
	for _, name := range src.names() {
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		body := src.files[name]
		unit, err := tierUnitBuildable(body)
		if err != nil {
			t.Fatal(err)
		}
		if unit {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(name)), body, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "Parallel" {
				t.Errorf("%s: integration/E2E tests must not call Parallel (INV-TEST-RUN-04)", fset.Position(call.Pos()))
			}
			return true
		})
	}
}

// tierListenerViolations returns one message per call in a Tier 1 test file
// that opens a listener directly or through module functions (INV-TEST-RUN-02),
// sorted by position.
//
// Resolution uses go/types over every Tier 1 variant of every module package.
// External imports are empty stub packages, so only module receiver types
// resolve, and the seeds are matched by import path. Limits, which keep the
// check from over-reporting but mean it can miss a listener:
//   - calls through function values, interface methods, and closures held in
//     variables are unresolved and not followed;
//   - a call inside a function literal counts toward the enclosing declaration
//     only when the literal is invoked where it is written (tierRunCalls), so a
//     closure stored in a field or variable, or passed to another call, is not
//     followed;
//   - a call made through a command or handler after it is built is not followed;
//   - package-level variable initializers are not callees;
//   - type errors are tolerated (see tierLoader.check);
//   - integration-only files are not indexed, since Tier 1 code cannot compile
//     against them, and declarations that differ only by platform or build tag
//     are unioned by name, so a listener in any variant marks the function.
func tierListenerViolations(root, modulePath string, src moduleSource) ([]string, error) {
	l := &tierLoader{
		fset:    token.NewFileSet(),
		root:    root,
		modPath: modulePath,
		src:     src,
		units:   make(map[tierUnitKey]*types.Package),
		stubs:   make(map[string]*types.Package),
		calls:   make(map[tierFuncID][]tierFuncID),
	}
	dirs := make([]string, 0, len(src.dirs))
	for dir := range src.dirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		l.unit(dir, tierBase)
		l.unit(dir, tierInPkgTest)
		l.unit(dir, tierExternalTest)
	}
	if l.err != nil {
		return nil, l.err
	}
	return l.violations(), nil
}

// loadTierModule reads the module path from root/go.mod and every Go file under
// tierRoots, keyed by path relative to root.
func loadTierModule(root string) (string, moduleSource, error) {
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", moduleSource{}, fmt.Errorf("tier check: read go.mod: %w", err)
	}
	modulePath := ""
	for _, line := range strings.Split(string(goMod), "\n") {
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			modulePath = strings.TrimSpace(rest)
			break
		}
	}
	if modulePath == "" {
		return "", moduleSource{}, fmt.Errorf("tier check: go.mod has no module directive")
	}
	files := make(map[string][]byte)
	for _, dir := range tierRoots {
		err := filepath.WalkDir(filepath.Join(root, dir), func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(file, ".go") {
				return nil
			}
			body, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, file)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = body
			return nil
		})
		if err != nil {
			return "", moduleSource{}, fmt.Errorf("tier check: walk %s: %w", dir, err)
		}
	}
	return modulePath, newModuleSource(files), nil
}

// moduleSource is the Go source of a module: file contents keyed by
// slash-separated path relative to the module root, and the file names of each
// directory.
type moduleSource struct {
	files map[string][]byte
	dirs  map[string][]string
}

func newModuleSource(files map[string][]byte) moduleSource {
	src := moduleSource{files: files, dirs: make(map[string][]string)}
	for name := range files {
		dir := path.Dir(name)
		src.dirs[dir] = append(src.dirs[dir], name)
	}
	for _, names := range src.dirs {
		sort.Strings(names)
	}
	return src
}

func (s moduleSource) names() []string {
	names := make([]string, 0, len(s.files))
	for name := range s.files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// tierVariant selects which files of a directory form one checked package.
type tierVariant int

const (
	tierBase         tierVariant = iota // non-test files
	tierInPkgTest                       // non-test files plus in-package _test.go files
	tierExternalTest                    // the external _test package, importing tierInPkgTest
)

// tierFuncID names a function or method: import path, receiver base type name
// (empty for functions), and name. External test packages use the import path
// of the package under test with a _test suffix.
type tierFuncID struct {
	pkg, recv, name string
}

// tierCallSite is a resolved call in a Tier 1 test file.
type tierCallSite struct {
	pos    token.Pos
	callee tierFuncID
	expr   string
}

type tierUnitKey struct {
	dir     string
	variant tierVariant
}

// tierFile is one parsed, Tier 1 buildable source file of a directory.
type tierFile struct {
	ast  *ast.File
	test bool // _test.go
	ext  bool // package clause ends in _test
}

// tierLoader type-checks the module one package variant at a time and records
// the call index and Tier 1 call sites while each variant is checked.
type tierLoader struct {
	fset    *token.FileSet
	root    string
	modPath string
	src     moduleSource
	units   map[tierUnitKey]*types.Package
	stubs   map[string]*types.Package
	calls   map[tierFuncID][]tierFuncID
	sites   []tierCallSite
	err     error
}

// unit type-checks one variant of the package in dir, once. It indexes the
// declarations and call sites of the files the variant owns.
func (l *tierLoader) unit(dir string, variant tierVariant) *types.Package {
	key := tierUnitKey{dir: dir, variant: variant}
	if pkg, ok := l.units[key]; ok {
		return pkg
	}
	if variant == tierInPkgTest && !l.hasTestNames(dir) {
		pkg := l.unit(dir, tierBase)
		l.units[key] = pkg
		return pkg
	}
	files, err := l.parse(dir, variant)
	if err != nil {
		l.fail(err)
	}
	importPath := l.importPath(dir)
	pkgPath := importPath
	imp := tierImporter{loader: l}
	if variant == tierExternalTest {
		pkgPath += "_test"
		imp.self = importPath
		imp.selfPkg = l.unit(dir, tierInPkgTest)
	}
	pkg, info := l.check(pkgPath, files, imp)
	for _, f := range files {
		if f.test == (variant == tierBase) {
			continue // owned by another variant of this directory
		}
		l.index(f, info, pkgPath)
	}
	l.units[key] = pkg
	return pkg
}

// parse returns the Tier 1 buildable files of dir that belong to variant.
func (l *tierLoader) parse(dir string, variant tierVariant) ([]tierFile, error) {
	var files []tierFile
	for _, name := range l.src.dirs[dir] {
		body := l.src.files[name]
		unit, err := tierUnitBuildable(body)
		if err != nil {
			return nil, fmt.Errorf("tier check: %s: %w", name, err)
		}
		if !unit {
			continue
		}
		parsed, err := parser.ParseFile(l.fset, filepath.Join(l.root, filepath.FromSlash(name)), body, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("tier check: parse %s: %w", name, err)
		}
		test := strings.HasSuffix(name, "_test.go")
		ext := test && strings.HasSuffix(parsed.Name.Name, "_test")
		switch {
		case variant == tierBase && test,
			variant == tierInPkgTest && ext,
			variant == tierExternalTest && !ext:
			continue
		}
		files = append(files, tierFile{ast: parsed, test: test, ext: ext})
	}
	return files, nil
}

// check type-checks files as one package. Type errors are tolerated: external
// imports are empty stubs, so their selectors do not resolve, and only module
// receiver types are needed. Duplicate declarations from platform variants are
// also reported as errors, and their bodies are still checked.
func (l *tierLoader) check(pkgPath string, files []tierFile, imp types.Importer) (*types.Package, *types.Info) {
	info := &types.Info{
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	if len(files) == 0 {
		return newStubPackage(pkgPath), info
	}
	asts := make([]*ast.File, 0, len(files))
	for _, f := range files {
		asts = append(asts, f.ast)
	}
	conf := types.Config{
		Importer:    imp,
		FakeImportC: true,
		Error:       func(error) {},
	}
	pkg, _ := conf.Check(pkgPath, l.fset, asts, info)
	return pkg, info
}

// index records the module-function calls in f's declarations and, for test
// files, every resolved call as a Tier 1 call site.
func (l *tierLoader) index(f tierFile, info *types.Info, pkgPath string) {
	imports := tierImports(f.ast)
	for _, decl := range f.ast.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		id := tierFuncID{pkg: pkgPath, recv: tierRecvName(fd), name: fd.Name.Name}
		tierRunCalls(fd.Body, func(call *ast.CallExpr) {
			if callee, ok := tierCallee(info, imports, call); ok {
				l.calls[id] = append(l.calls[id], callee)
			}
		})
	}
	if !f.test {
		return
	}
	ast.Inspect(f.ast, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if callee, ok := tierCallee(info, imports, call); ok {
			l.sites = append(l.sites, tierCallSite{pos: call.Pos(), callee: callee, expr: types.ExprString(call.Fun)})
		}
		return true
	})
}

// tierRunCalls visits each call that running body can make. A function literal
// runs only where it is invoked: the literal in func(){...}() is followed, while
// a literal stored in a field or variable, or passed to another call, is not.
// A builder that returns a command or handler whose closure listens therefore
// does not count as listening.
func tierRunCalls(body ast.Node, visit func(*ast.CallExpr)) {
	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			visit(n)
			if lit, ok := ast.Unparen(n.Fun).(*ast.FuncLit); ok {
				tierRunCalls(lit.Body, visit)
			}
		}
		return true
	})
}

// listenerVia maps each module function that reaches a seed to the next callee
// on a shortest chain toward that seed. The search runs breadth-first from the
// seeds over sorted keys, so chains are minimal and deterministic.
func (l *tierLoader) listenerVia() map[tierFuncID]tierFuncID {
	ids := make([]tierFuncID, 0, len(l.calls))
	for id := range l.calls {
		ids = append(ids, id)
	}
	sortTierIDs(ids)
	callers := make(map[tierFuncID][]tierFuncID)
	for _, id := range ids {
		for _, callee := range l.calls[id] {
			callers[callee] = append(callers[callee], id)
		}
	}
	targets := make([]tierFuncID, 0, len(callers))
	for callee := range callers {
		targets = append(targets, callee)
	}
	sortTierIDs(targets)
	var queue []tierFuncID
	for _, target := range targets {
		if tierIsSeed(target) {
			queue = append(queue, target)
		}
	}
	via := make(map[tierFuncID]tierFuncID)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, caller := range callers[cur] {
			if _, seen := via[caller]; seen {
				continue
			}
			via[caller] = cur
			queue = append(queue, caller)
		}
	}
	return via
}

type tierViolation struct {
	where   token.Position
	message string
}

// violations renders one message per Tier 1 call site that reaches a listener,
// in source order. A direct seed call keeps the original message; a call that
// reaches one shows the chain, starting with the callee as written.
func (l *tierLoader) violations() []string {
	via := l.listenerVia()
	var found []tierViolation
	for _, site := range l.sites {
		where := l.fset.Position(site.pos)
		if tierIsSeed(site.callee) {
			found = append(found, tierViolation{where, "socket tests require integration or e2e build tags (INV-TEST-RUN-02)"})
			continue
		}
		next, ok := via[site.callee]
		if !ok {
			continue
		}
		chain := []string{site.expr}
		for cur := next; ; cur = via[cur] {
			chain = append(chain, tierDisplay(cur))
			if tierIsSeed(cur) {
				break
			}
		}
		found = append(found, tierViolation{where, strings.Join(chain, " → ") + " (INV-TEST-RUN-02)"})
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i].where, found[j].where
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	out := make([]string, 0, len(found))
	for _, v := range found {
		out = append(out, fmt.Sprintf("%s: %s", v.where, v.message))
	}
	return out
}

// importer resolves module imports to their checked base variant and every
// other import to a stub. External test packages resolve their own package
// under test to the in-package variant.
type tierImporter struct {
	loader  *tierLoader
	self    string
	selfPkg *types.Package
}

func (i tierImporter) Import(importPath string) (*types.Package, error) {
	if i.selfPkg != nil && importPath == i.self {
		return i.selfPkg, nil
	}
	return i.loader.importPackage(importPath), nil
}

func (l *tierLoader) importPackage(importPath string) *types.Package {
	if dir, ok := l.localDir(importPath); ok && len(l.src.dirs[dir]) > 0 {
		return l.unit(dir, tierBase)
	}
	if pkg, ok := l.stubs[importPath]; ok {
		return pkg
	}
	pkg := newStubPackage(importPath)
	l.stubs[importPath] = pkg
	return pkg
}

func (l *tierLoader) localDir(importPath string) (string, bool) {
	if importPath == l.modPath {
		return ".", true
	}
	return strings.CutPrefix(importPath, l.modPath+"/")
}

func (l *tierLoader) importPath(dir string) string {
	if dir == "." {
		return l.modPath
	}
	return l.modPath + "/" + dir
}

func (l *tierLoader) hasTestNames(dir string) bool {
	for _, name := range l.src.dirs[dir] {
		if strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}

func (l *tierLoader) fail(err error) {
	if l.err == nil {
		l.err = err
	}
}

// newStubPackage returns a complete package standing in for an import the
// check does not read from source. It is empty except for the seed receiver
// types of tierSeedMethods, so a method call on one of them resolves.
func newStubPackage(importPath string) *types.Package {
	name := path.Base(importPath)
	if i := strings.IndexByte(name, '.'); i > 0 {
		name = name[:i] // gopkg.in/yaml.v3 -> yaml
	}
	pkg := types.NewPackage(importPath, name)
	for recv, methods := range tierSeedMethods[importPath] {
		obj := types.NewTypeName(token.NoPos, pkg, recv, nil)
		named := types.NewNamed(obj, types.NewStruct(nil, nil), nil)
		pkg.Scope().Insert(obj)
		for _, method := range methods {
			// Arguments and results are not modeled; the type errors at the
			// call are tolerated and the selection still resolves.
			sig := types.NewSignatureType(types.NewVar(token.NoPos, pkg, "", types.NewPointer(named)), nil, nil, nil, nil, false)
			named.AddMethod(types.NewFunc(token.NoPos, pkg, method, sig))
		}
	}
	pkg.MarkComplete()
	return pkg
}

// tierCallee resolves the function a call invokes. It returns false for calls
// it cannot resolve: function values, interface methods, conversions, builtins.
func tierCallee(info *types.Info, imports map[string]string, call *ast.CallExpr) (tierFuncID, bool) {
	fun := ast.Unparen(call.Fun)
	switch instantiated := fun.(type) {
	case *ast.IndexExpr:
		fun = ast.Unparen(instantiated.X)
	case *ast.IndexListExpr:
		fun = ast.Unparen(instantiated.X)
	}
	switch fn := fun.(type) {
	case *ast.Ident:
		if f, ok := info.Uses[fn].(*types.Func); ok {
			return tierFuncOf(f)
		}
	case *ast.SelectorExpr:
		if qual, ok := fn.X.(*ast.Ident); ok {
			switch obj := info.Uses[qual].(type) {
			case *types.PkgName:
				return tierFuncID{pkg: obj.Imported().Path(), name: fn.Sel.Name}, true
			case nil:
				// go/types did not bind the qualifier, so fall back to the file's import name.
				if importPath, ok := imports[qual.Name]; ok {
					return tierFuncID{pkg: importPath, name: fn.Sel.Name}, true
				}
			}
		}
		if sel, ok := info.Selections[fn]; ok {
			if f, ok := sel.Obj().(*types.Func); ok {
				return tierFuncOf(f)
			}
			return tierFuncID{}, false
		}
		if f, ok := info.Uses[fn.Sel].(*types.Func); ok {
			return tierFuncOf(f)
		}
	}
	return tierFuncID{}, false
}

// tierFuncOf names a function object. Methods are named by their receiver's
// base type, so a method on another type with the same name does not match.
func tierFuncOf(fn *types.Func) (tierFuncID, bool) {
	fn = fn.Origin()
	if fn.Pkg() == nil {
		return tierFuncID{}, false
	}
	id := tierFuncID{pkg: fn.Pkg().Path(), name: fn.Name()}
	if recv := fn.Signature().Recv(); recv != nil {
		typ := recv.Type()
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = ptr.Elem()
		}
		named, ok := typ.(*types.Named)
		if !ok {
			return tierFuncID{}, false
		}
		id.recv = named.Obj().Name()
	}
	return id, true
}

// tierRecvName returns the receiver's base type name, stripping pointers,
// parentheses, and type parameters.
func tierRecvName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	expr := fd.Recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// tierImports maps each import name in file to its import path.
func tierImports(file *ast.File) map[string]string {
	imports := make(map[string]string, len(file.Imports))
	for _, spec := range file.Imports {
		importPath := strings.Trim(spec.Path.Value, "\"`")
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	return imports
}

// tierSeedMethods are the standard-library methods that open a listener or
// dial, by import path and receiver type name.
var tierSeedMethods = map[string]map[string][]string{
	"net": {
		"Dialer":       {"Dial", "DialContext"},
		"ListenConfig": {"Listen", "ListenPacket"},
	},
	"crypto/tls": {
		"Dialer": {"Dial", "DialContext"},
	},
	"net/http": {
		"Server": {"ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS"},
	},
	"net/http/httptest": {
		"Server": {"Start", "StartTLS"},
	},
}

// tierIsSeed reports whether id is a direct listener or dialer in the standard
// library: a package-level function, or a method in tierSeedMethods.
func tierIsSeed(id tierFuncID) bool {
	if id.recv != "" {
		return slices.Contains(tierSeedMethods[id.pkg][id.recv], id.name)
	}
	switch id.pkg {
	case "net":
		return strings.HasPrefix(id.name, "Listen") || strings.HasPrefix(id.name, "Dial")
	case "crypto/tls":
		return id.name == "Listen" || id.name == "Dial" || id.name == "DialWithDialer"
	case "net/http":
		return id.name == "ListenAndServe" || id.name == "ListenAndServeTLS" || id.name == "Serve" || id.name == "ServeTLS"
	case "net/http/httptest":
		return id.name == "NewServer" || id.name == "NewTLSServer" || id.name == "NewUnstartedServer"
	}
	return false
}

// tierDisplay renders a function as pkg.Name or pkg.Recv.Name, using the last
// element of the import path.
func tierDisplay(id tierFuncID) string {
	if id.recv != "" {
		return path.Base(id.pkg) + "." + id.recv + "." + id.name
	}
	return path.Base(id.pkg) + "." + id.name
}

func tierIDLess(a, b tierFuncID) bool {
	if a.pkg != b.pkg {
		return a.pkg < b.pkg
	}
	if a.recv != b.recv {
		return a.recv < b.recv
	}
	return a.name < b.name
}

func sortTierIDs(ids []tierFuncID) {
	sort.Slice(ids, func(i, j int) bool { return tierIDLess(ids[i], ids[j]) })
}

// tierUnitBuildable reports whether a Tier 1 build can compile a file with this
// content. Integration and E2E files are excluded, and so are ignore-tagged
// files that no build compiles.
func tierUnitBuildable(body []byte) (bool, error) {
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "//go:build ") {
			expr, err := constraint.Parse(line)
			if err != nil {
				return false, err
			}
			return buildAllowsWithout(expr, "integration", "e2e", "ignore"), nil
		}
	}
	return true, nil
}

// A platform constraint alone (linux, !windows, etc.) does not exclude Tier 1.
// Evaluate all other tag combinations with the two boundary tiers disabled.
func testBuildAllowsUnit(expr constraint.Expr) bool {
	return buildAllowsWithout(expr, "integration", "e2e")
}

// buildAllowsWithout reports whether some assignment of the tags in expr
// satisfies it while every tag in off is false.
func buildAllowsWithout(expr constraint.Expr, off ...string) bool {
	tags := make(map[string]int)
	var collect func(constraint.Expr)
	collect = func(expr constraint.Expr) {
		switch expr := expr.(type) {
		case *constraint.TagExpr:
			if !slices.Contains(off, expr.Tag) {
				if _, exists := tags[expr.Tag]; !exists {
					tags[expr.Tag] = len(tags)
				}
			}
		case *constraint.NotExpr:
			collect(expr.X)
		case *constraint.AndExpr:
			collect(expr.X)
			collect(expr.Y)
		case *constraint.OrExpr:
			collect(expr.X)
			collect(expr.Y)
		}
	}
	collect(expr)
	for bits := 0; bits < 1<<len(tags); bits++ {
		if expr.Eval(func(tag string) bool {
			index, exists := tags[tag]
			return exists && bits&(1<<index) != 0
		}) {
			return true
		}
	}
	return false
}

func TestTestBuildAllowsUnit_PlatformConstraintsDoNotReplaceTierTags(t *testing.T) {
	for _, tc := range []struct {
		expr string
		unit bool
	}{
		{"integration", false},
		{"integration || e2e", false},
		{"linux && integration", false},
		{"!windows && integration", false},
		{"linux && !windows", true},
		{"integration || linux", true},
		{"!integration", true},
		{"e2e || !windows", true},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			expr, err := constraint.Parse("//go:build " + tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			if got := testBuildAllowsUnit(expr); got != tc.unit {
				t.Errorf("unit availability = %v, want %v", got, tc.unit)
			}
		})
	}
}

// TestTestTiersResolverFollowsHelpersToListeners pins the transitive resolver on
// small in-memory modules: the three F7 shapes, every chain length, and the
// shapes that must not be reported.
func TestTestTiersResolverFollowsHelpersToListeners(t *testing.T) {
	const modulePath = "example.com/m"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "direct listener call in a unit test file is reported",
			files: map[string]string{
				"internal/drive/a_test.go": "package drive\n\nimport (\n\t\"net/http/httptest\"\n\t\"testing\"\n)\n\nfunc TestDirect(t *testing.T) {\n\thttptest.NewServer(nil)\n}\n",
			},
			want: []string{"socket tests require integration or e2e build tags"},
		},
		{
			name: "same-package helper in a non-test file is reported through a bare call",
			files: map[string]string{
				"internal/drive/listen.go": "package drive\n\nimport \"net\"\n\nfunc openSocket() {\n\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n}\n",
				"internal/drive/a_test.go": "package drive\n\nimport \"testing\"\n\nfunc TestOpen(t *testing.T) {\n\topenSocket()\n}\n",
			},
			want: []string{"openSocket → net.Listen (INV-TEST-RUN-02)"},
		},
		{
			name: "F7 sentinel shape: helper in another test file opens a server",
			files: map[string]string{
				"internal/drive/dispatch_test.go": "package drive\n\nimport (\n\t\"net/http/httptest\"\n\t\"testing\"\n)\n\nfunc newFakeDispatch(t *testing.T) {\n\tsrv := httptest.NewServer(nil)\n\tt.Cleanup(srv.Close)\n}\n",
				"internal/drive/sentinel_test.go": "package drive\n\nimport \"testing\"\n\nfunc TestSentinel(t *testing.T) {\n\tnewFakeDispatch(t)\n}\n",
			},
			want: []string{
				"socket tests require integration or e2e build tags",
				"newFakeDispatch → httptest.NewServer (INV-TEST-RUN-02)",
			},
		},
		{
			name: "F7 attended shape: helper in a second test file opens a listener",
			files: map[string]string{
				"internal/drive/a2a_test.go":      "package drive\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc newFakeA2A(t *testing.T) {\n\tln, _ := net.Listen(\"tcp\", \"127.0.0.1:0\")\n\tt.Cleanup(func() { ln.Close() })\n}\n",
				"internal/drive/attended_test.go": "package drive\n\nimport \"testing\"\n\nfunc TestAttended(t *testing.T) {\n\tnewFakeA2A(t)\n}\n",
			},
			want: []string{
				"socket tests require integration or e2e build tags",
				"newFakeA2A → net.Listen (INV-TEST-RUN-02)",
			},
		},
		{
			name: "F7 a2a stub shape: helper calls a production function that listens",
			files: map[string]string{
				"internal/drive/a2a_stub.go":      "package drive\n\nimport \"net\"\n\ntype A2AStub struct{ ln net.Listener }\n\nfunc StartA2AStub(addr string) (*A2AStub, error) {\n\tln, err := net.Listen(\"tcp\", addr)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn &A2AStub{ln: ln}, nil\n}\n",
				"internal/drive/a2a_stub_test.go": "package drive\n\nimport \"testing\"\n\nfunc startStub(t *testing.T) *A2AStub {\n\ts, err := StartA2AStub(\"127.0.0.1:0\")\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n\treturn s\n}\n\nfunc TestA2AStubServes(t *testing.T) {\n\tstartStub(t)\n}\n",
			},
			want: []string{
				"StartA2AStub → net.Listen (INV-TEST-RUN-02)",
				"startStub → drive.StartA2AStub → net.Listen (INV-TEST-RUN-02)",
			},
		},
		{
			name: "two-hop chain through production helpers is reported at each test call site",
			files: map[string]string{
				"internal/prod/prod.go":         "package prod\n\nimport \"net\"\n\nfunc Start() {\n\topenSocket()\n}\n\nfunc openSocket() {\n\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n}\n",
				"internal/drive/helper_test.go": "package drive\n\nimport \"example.com/m/internal/prod\"\n\nfunc startStub() {\n\tprod.Start()\n}\n",
				"internal/drive/a_test.go":      "package drive\n\nimport \"testing\"\n\nfunc TestChain(t *testing.T) {\n\tstartStub()\n}\n",
			},
			want: []string{
				"startStub → prod.Start → prod.openSocket → net.Listen (INV-TEST-RUN-02)",
				"prod.Start → prod.openSocket → net.Listen (INV-TEST-RUN-02)",
			},
		},
		{
			name: "production function in another package is reported through an import",
			files: map[string]string{
				"internal/prod/prod.go":    "package prod\n\nimport \"net\"\n\nfunc Start() {\n\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n}\n",
				"internal/drive/a_test.go": "package drive\n\nimport (\n\t\"example.com/m/internal/prod\"\n\t\"testing\"\n)\n\nfunc TestStart(t *testing.T) {\n\tprod.Start()\n}\n",
			},
			want: []string{"prod.Start → net.Listen (INV-TEST-RUN-02)"},
		},
		{
			name: "external test package reaches a listener in the package under test",
			files: map[string]string{
				"internal/prod/prod.go":      "package prod\n\nimport \"net\"\n\nfunc Start() {\n\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n}\n",
				"internal/prod/prod_test.go": "package prod_test\n\nimport (\n\t\"example.com/m/internal/prod\"\n\t\"testing\"\n)\n\nfunc TestStart(t *testing.T) {\n\tprod.Start()\n}\n",
			},
			want: []string{"prod.Start → net.Listen (INV-TEST-RUN-02)"},
		},
		{
			name: "method reached through a constructor is reported with its receiver",
			files: map[string]string{
				"internal/drive/server.go": "package drive\n\nimport \"net\"\n\ntype server struct{}\n\nfunc newServer() *server {\n\treturn &server{}\n}\n\nfunc (s *server) Start() {\n\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n}\n",
				"internal/drive/a_test.go": "package drive\n\nimport \"testing\"\n\nfunc TestStart(t *testing.T) {\n\ts := newServer()\n\ts.Start()\n}\n",
			},
			want: []string{"s.Start → net.Listen (INV-TEST-RUN-02)"},
		},
		{
			name: "method with the same name on a different receiver is not reported",
			files: map[string]string{
				"internal/drive/server.go": "package drive\n\nimport \"net\"\n\ntype listening struct{}\n\nfunc (listening) Start() {\n\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n}\n\ntype quiet struct{}\n\nfunc (quiet) Start() {}\n",
				"internal/drive/a_test.go": "package drive\n\nimport \"testing\"\n\nfunc TestQuiet(t *testing.T) {\n\tvar q quiet\n\tq.Start()\n}\n",
			},
		},
		{
			name: "helper that only builds an http.Handler is not reported",
			files: map[string]string{
				"internal/drive/handler_test.go": "package drive\n\nimport (\n\t\"net/http\"\n\t\"testing\"\n)\n\nfunc newHandler() http.Handler {\n\tmux := http.NewServeMux()\n\tmux.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {})\n\treturn mux\n}\n",
				"internal/drive/a_test.go":       "package drive\n\nimport \"testing\"\n\nfunc TestHandler(t *testing.T) {\n\t_ = newHandler()\n}\n",
			},
		},
		{
			name: "builder whose stored closure listens is not reported until the closure runs",
			files: map[string]string{
				"internal/drive/command.go": "package drive\n\nimport \"net\"\n\ntype command struct{ run func() }\n\nfunc newCommand() *command {\n\treturn &command{run: func() {\n\t\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n\t}}\n}\n",
				"internal/drive/a_test.go":  "package drive\n\nimport \"testing\"\n\nfunc TestBuild(t *testing.T) {\n\t_ = newCommand()\n}\n",
			},
		},
		{
			name: "closure invoked where it is written is reported",
			files: map[string]string{
				"internal/drive/open.go":   "package drive\n\nimport \"net\"\n\nfunc openNow() {\n\tfunc() {\n\t\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n\t}()\n}\n",
				"internal/drive/a_test.go": "package drive\n\nimport \"testing\"\n\nfunc TestOpenNow(t *testing.T) {\n\topenNow()\n}\n",
			},
			want: []string{"openNow → net.Listen (INV-TEST-RUN-02)"},
		},
		{
			name: "integration-tagged file with a direct listener call is not checked",
			files: map[string]string{
				"internal/drive/live_integration_test.go": "//go:build integration\n\npackage drive\n\nimport (\n\t\"net/http/httptest\"\n\t\"testing\"\n)\n\nfunc TestLive(t *testing.T) {\n\thttptest.NewServer(nil)\n}\n",
			},
		},
		{
			name: "integration-only helper does not taint a unit helper with the same name",
			files: map[string]string{
				"internal/drive/open_test.go":             "//go:build !integration\n\npackage drive\n\nfunc openPort() {}\n",
				"internal/drive/open_integration_test.go": "//go:build integration\n\npackage drive\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc openPort() {\n\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n}\n",
				"internal/drive/a_test.go":                "package drive\n\nimport \"testing\"\n\nfunc TestOpen(t *testing.T) {\n\topenPort()\n}\n",
			},
		},
		{
			name: "listener in a platform variant is reported on every platform",
			files: map[string]string{
				"internal/prod/port_windows.go": "//go:build windows\n\npackage prod\n\nimport \"net\"\n\nfunc OpenPort() {\n\tnet.Listen(\"tcp\", \"127.0.0.1:0\")\n}\n",
				"internal/prod/port_other.go":   "//go:build !windows\n\npackage prod\n\nfunc OpenPort() {}\n",
				"internal/drive/a_test.go":      "package drive\n\nimport (\n\t\"example.com/m/internal/prod\"\n\t\"testing\"\n)\n\nfunc TestPort(t *testing.T) {\n\tprod.OpenPort()\n}\n",
			},
			want: []string{"prod.OpenPort → net.Listen (INV-TEST-RUN-02)"},
		},
		{
			name: "tls and http package-level listeners and dialers are seeds",
			files: map[string]string{
				"internal/drive/a_test.go": "package drive\n\nimport (\n\t\"crypto/tls\"\n\t\"net/http\"\n\t\"testing\"\n)\n\nfunc TestPackageSeeds(t *testing.T) {\n\ttls.Listen(\"tcp\", \"127.0.0.1:0\", nil)\n\ttls.Dial(\"tcp\", \"127.0.0.1:1\", nil)\n\ttls.DialWithDialer(nil, \"tcp\", \"127.0.0.1:1\", nil)\n\thttp.Serve(nil, nil)\n\thttp.ServeTLS(nil, nil, \"\", \"\")\n}\n",
			},
			want: []string{
				"socket tests require integration or e2e build tags",
				"socket tests require integration or e2e build tags",
				"socket tests require integration or e2e build tags",
				"socket tests require integration or e2e build tags",
				"socket tests require integration or e2e build tags",
			},
		},
		{
			name: "listener and dialer methods are seeds",
			files: map[string]string{
				"internal/drive/a_test.go": "package drive\n\nimport (\n\t\"context\"\n\t\"crypto/tls\"\n\t\"net\"\n\t\"net/http\"\n\t\"net/http/httptest\"\n\t\"testing\"\n)\n\nfunc TestMethodSeeds(t *testing.T) {\n\tsrv := &http.Server{Addr: \"127.0.0.1:0\"}\n\tgo srv.ListenAndServe()\n\tvar d net.Dialer\n\td.DialContext(context.Background(), \"tcp\", \"127.0.0.1:1\")\n\tvar lc net.ListenConfig\n\tlc.Listen(context.Background(), \"tcp\", \"127.0.0.1:0\")\n\ttd := &tls.Dialer{}\n\ttd.Dial(\"tcp\", \"127.0.0.1:1\")\n\tts := &httptest.Server{}\n\tts.StartTLS()\n}\n",
			},
			want: []string{
				"socket tests require integration or e2e build tags",
				"socket tests require integration or e2e build tags",
				"socket tests require integration or e2e build tags",
				"socket tests require integration or e2e build tags",
				"socket tests require integration or e2e build tags",
			},
		},
		{
			name: "server method reached through a module helper is reported",
			files: map[string]string{
				"internal/prod/prod.go":    "package prod\n\nimport \"net/http\"\n\ntype Front struct{ *http.Server }\n\nfunc (f *Front) Run() {\n\tf.Serve(nil)\n}\n",
				"internal/drive/a_test.go": "package drive\n\nimport (\n\t\"example.com/m/internal/prod\"\n\t\"testing\"\n)\n\nfunc TestRun(t *testing.T) {\n\tf := &prod.Front{}\n\tf.Run()\n}\n",
			},
			want: []string{"f.Run → http.Server.Serve (INV-TEST-RUN-02)"},
		},
		{
			name: "http.Server Shutdown is not a seed",
			files: map[string]string{
				"internal/drive/a_test.go": "package drive\n\nimport (\n\t\"net/http\"\n\t\"testing\"\n)\n\nfunc TestShutdown(t *testing.T) {\n\tsrv := &http.Server{}\n\tsrv.Close()\n}\n",
			},
		},
		{
			name: "call to an undefined helper is not reported",
			files: map[string]string{
				"internal/drive/a_test.go": "package drive\n\nimport \"testing\"\n\nfunc TestMissing(t *testing.T) {\n\tundefinedHelper()\n}\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := make(map[string][]byte, len(tc.files))
			for name, body := range tc.files {
				files[name] = []byte(body)
			}
			got, err := tierListenerViolations("", modulePath, newModuleSource(files))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d violations, want %d:\n%s", len(got), len(tc.want), strings.Join(got, "\n"))
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("violation %d = %q, want it to contain %q", i, got[i], want)
				}
			}
		})
	}
}
