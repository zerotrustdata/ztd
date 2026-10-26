// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const envVarsJSONFilename = "env_vars.json"

// validEnvCategories are the only env categories (INV-ENV-04). There is no
// category for platform configuration: it is a typed default or an explicit flag,
// never an environment variable.
var validEnvCategories = map[string]bool{
	"secret":        true,
	"user_endpoint": true,
	"host":          true,
	"test":          true,
}

type envVarRegistryEntry struct {
	GoConst     string `json:"_go_const"`
	Value       string `json:"value"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

func loadEnvVarRegistry(t *testing.T) map[string]envVarRegistryEntry {
	t.Helper()
	data, err := os.ReadFile(protocolConstantsPath(envVarsJSONFilename))
	require.NoError(t, err)
	var doc struct {
		EnvVars map[string]envVarRegistryEntry `json:"env_vars"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	require.NotEmpty(t, doc.EnvVars)
	return doc.EnvVars
}

// TestEnvVarRegistry_MatchesGoConstants asserts the typed Go registry and the
// protocol JSON registry describe exactly the same keys, and every key carries a
// valid category.
func TestEnvVarRegistry_MatchesGoConstants(t *testing.T) {
	registry := loadEnvVarRegistry(t)
	byGoConst := make(map[string]envVarRegistryEntry, len(registry))
	for name, entry := range registry {
		assert.Truef(t, validEnvCategories[entry.Category], "%s: category %q is not one of secret, user_endpoint, host, test", name, entry.Category)
		assert.NotEmptyf(t, entry.Description, "%s: description is required", name)
		require.NotEmptyf(t, entry.GoConst, "%s: _go_const is required", name)
		byGoConst[entry.GoConst] = entry
	}

	goFields := reflect.TypeOf(EnvVar)
	goValues := reflect.ValueOf(EnvVar)
	for i := 0; i < goFields.NumField(); i++ {
		field := goFields.Field(i).Name
		value := string(goValues.Field(i).Interface().(EnvVarKey))
		entry, ok := byGoConst["EnvVar."+field]
		if !assert.Truef(t, ok, "EnvVar.%s has no entry in %s", field, envVarsJSONFilename) {
			continue
		}
		assert.Equalf(t, value, entry.Value, "EnvVar.%s value differs from %s", field, envVarsJSONFilename)
		delete(byGoConst, "EnvVar."+field)
	}
	for goConst := range byGoConst {
		assert.Failf(t, "stale registry entry", "%s lists %s, which is not a field of constants.EnvVar", envVarsJSONFilename, goConst)
	}
}

// TestSecretEnvVars_MatchRegistrySecrets asserts SecretEnvVars lists exactly the
// registry keys in category "secret".
func TestSecretEnvVars_MatchRegistrySecrets(t *testing.T) {
	var want []string
	for _, entry := range loadEnvVarRegistry(t) {
		if entry.Category == "secret" {
			want = append(want, entry.Value)
		}
	}
	got := make([]string, 0, len(SecretEnvVars))
	for _, key := range SecretEnvVars {
		got = append(got, string(key))
	}
	assert.ElementsMatch(t, want, got)
}

// TestUserEndpointEnvVars_MatchRegistryUserEndpoints asserts UserEndpointEnvVars
// lists exactly the registry keys in category "user_endpoint".
func TestUserEndpointEnvVars_MatchRegistryUserEndpoints(t *testing.T) {
	var want []string
	for _, entry := range loadEnvVarRegistry(t) {
		if entry.Category == "user_endpoint" {
			want = append(want, entry.Value)
		}
	}
	got := make([]string, 0, len(UserEndpointEnvVars))
	for _, key := range UserEndpointEnvVars {
		got = append(got, string(key))
	}
	assert.ElementsMatch(t, want, got)
}

// TestProductionGoEnvReads_UseRegistryKeys asserts no production Go code, and
// no E2E harness code under test/, reads the environment through a raw string
// literal or a locally declared string constant; every key goes through the
// typed constants.EnvVar registry (INV-TYPE-06, INV-ENV-04).
func TestProductionGoEnvReads_UseRegistryKeys(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, dir := range []string{"internal", "cmd"} {
		found, err := walkProductionGoFiles(filepath.Join(root, dir))
		require.NoError(t, err)
		files = append(files, found...)
	}
	harness, err := filepath.Glob(filepath.Join(root, "test", "*", "*.go"))
	require.NoError(t, err)
	files = append(files, harness...)

	dirConsts := map[string]map[string]string{}
	var offenders []string
	for _, path := range files {
		dir := filepath.Dir(path)
		if _, ok := dirConsts[dir]; !ok {
			dirConsts[dir] = stringConsts(t, dir)
		}
		offenders = append(offenders, rawEnvReads(t, path, root, dirConsts[dir])...)
	}
	sort.Strings(offenders)
	assert.Empty(t, offenders, "os.Getenv/os.LookupEnv must take string(constants.EnvVar.<Key>), not a string literal or local constant")
}

// stringConsts returns the string-literal constants declared in the Go files
// of dir, keyed by name.
func stringConsts(t *testing.T, dir string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	consts := map[string]string{}
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.GenDecl)
			if !ok || decl.Tok != token.CONST {
				return true
			}
			for _, spec := range decl.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if value, err := strconv.Unquote(lit.Value); err == nil {
							consts[name.Name] = value
						}
					}
				}
			}
			return true
		})
	}
	return consts
}

func rawEnvReads(t *testing.T, path, root string, consts map[string]string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
			return true
		}
		var key string
		switch arg := call.Args[0].(type) {
		case *ast.BasicLit:
			if arg.Kind != token.STRING {
				return true
			}
			if key, err = strconv.Unquote(arg.Value); err != nil {
				key = arg.Value
			}
		case *ast.Ident:
			value, isConst := consts[arg.Name]
			if !isConst {
				return true
			}
			key = value
		default:
			return true
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		found = append(found, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" os."+sel.Sel.Name+"("+strconv.Quote(key)+")")
		return true
	})
	return found
}
