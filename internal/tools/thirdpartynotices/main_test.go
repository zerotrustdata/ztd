// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestParseModulesTxt(t *testing.T) {
	t.Parallel()
	input := `# github.com/z/beta v1.2.0
## explicit; go 1.21
github.com/z/beta
github.com/z/beta/sub
# dario.cat/mergo v1.0.2
## explicit; go 1.13
dario.cat/mergo
# example.com/old v0.1.0 => example.com/new v0.2.0
## explicit
example.com/old
# example.com/local => ../local
## explicit
example.com/local
`
	mods, err := parseModulesTxt(strings.NewReader(input))
	require.NoError(t, err)
	require.Len(t, mods, 4)

	// Sorted by import path.
	assert.Equal(t, "dario.cat/mergo", mods[0].Path)
	assert.Equal(t, "v1.0.2", mods[0].Version)
	assert.Empty(t, mods[0].Replacement)

	assert.Equal(t, "example.com/local", mods[1].Path)
	assert.Empty(t, mods[1].Version)
	assert.Equal(t, "../local", mods[1].Replacement)

	assert.Equal(t, "example.com/old", mods[2].Path)
	assert.Equal(t, "v0.1.0", mods[2].Version)
	assert.Equal(t, "example.com/new v0.2.0", mods[2].Replacement)

	assert.Equal(t, "github.com/z/beta", mods[3].Path)
	assert.Equal(t, "v1.2.0", mods[3].Version)
}

func TestParseModulesTxtRejectsMalformedModuleLine(t *testing.T) {
	t.Parallel()
	_, err := parseModulesTxt(strings.NewReader("# a b c\n"))
	assert.Error(t, err)
}

func TestCollectGoModulesReadsLicenseFiles(t *testing.T) {
	t.Parallel()
	repo := testutil.TempDir(t)
	vendor := filepath.Join(repo, "vendor")
	modRoot := filepath.Join(vendor, "example.com", "lib")
	require.NoError(t, os.MkdirAll(modRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vendor, "modules.txt"),
		[]byte("# example.com/lib v1.0.0\n## explicit\nexample.com/lib\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(modRoot, "LICENSE"), []byte("license text\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(modRoot, "NOTICE"), []byte("notice text\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(modRoot, "lib.go"), []byte("package lib\n"), 0o644))

	mods, err := collectGoModules(repo)
	require.NoError(t, err)
	require.Len(t, mods, 1)
	require.Len(t, mods[0].Files, 2)
	assert.Equal(t, "LICENSE", mods[0].Files[0].Name)
	assert.Equal(t, "NOTICE", mods[0].Files[1].Name)
	assert.Equal(t, "notice text\n", mods[0].Files[1].Text)
}

func TestNPMPackageName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "react", npmPackageName("node_modules/react"))
	assert.Equal(t, "@tanstack/react-table", npmPackageName("node_modules/@tanstack/react-table"))
	assert.Equal(t, "b", npmPackageName("node_modules/a/node_modules/b"))
	assert.Empty(t, npmPackageName(""))
}

func TestParseNPMLicense(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "MIT", parseNPMLicense(json.RawMessage(`"MIT"`)))
	assert.Equal(t, "BSD-3-Clause", parseNPMLicense(json.RawMessage(`{"type":"BSD-3-Clause"}`)))
	assert.Equal(t, "UNKNOWN", parseNPMLicense(nil))
}

func TestWriteLicenseBlockFenceSurvivesBackticks(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	writeLicenseBlock(&b, LicenseFile{Name: "NOTICE", Text: "a ```` b\n"})
	assert.Contains(t, b.String(), "`````text\n")
}
