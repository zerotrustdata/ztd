// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(raw)
}

func packageJSON(version string) string {
	return fmt.Sprintf("{\n  \"name\": \"x\",\n  \"version\": \"%s\",\n  \"dependencies\": {\n    \"dep\": \"^9.9.9\"\n  }\n}\n", version)
}

func packageLockJSON(version string) string {
	return fmt.Sprintf("{\n  \"name\": \"x\",\n  \"version\": \"%[1]s\",\n  \"lockfileVersion\": 3,\n  \"packages\": {\n    \"\": {\n      \"name\": \"x\",\n      \"version\": \"%[1]s\"\n    },\n    \"node_modules/dep\": {\n      \"version\": \"9.9.9\"\n    }\n  }\n}\n", version)
}

// newRepo lays out a synthetic repo with every declaration at the given version.
// website has no top-level version, mirroring the real repo.
func newRepo(t *testing.T, platform, declared string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "VERSION", "v"+platform+"\n")
	for _, doc := range protocolDocs {
		writeFile(t, root, "protocol/docs/"+doc, "# Doc\n\nVersion: v"+declared+"\n")
	}
	for _, pkg := range npmPackages {
		if pkg == "website" {
			writeFile(t, root, pkg+"/package.json", "{\n  \"name\": \"site\"\n}\n")
			continue
		}
		writeFile(t, root, pkg+"/package.json", packageJSON(declared))
		writeFile(t, root, pkg+"/package-lock.json", packageLockJSON(declared))
	}
	return root
}

func TestRun_CheckPassesWhenInSync(t *testing.T) {
	root := newRepo(t, "1.2.3", "1.2.3")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, run([]string{"-check"}, root, &stdout, &stderr), stderr.String())
	// 4 docs + 4 packages x (package.json, lock root, lock packages[""]).
	assert.Contains(t, stdout.String(), "Version sync OK: 1.2.3 (16 declarations)")
}

func TestRun_CheckReportsDriftWithoutWriting(t *testing.T) {
	root := newRepo(t, "1.2.4", "1.2.3")
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 1, run([]string{"-check"}, root, &stdout, &stderr))
	assert.Contains(t, stderr.String(), `console/package-lock.json packages[""]: "1.2.3" != 1.2.4`)
	assert.Contains(t, stderr.String(), "protocol/docs/spec.md Version header")
	assert.Contains(t, readFile(t, root, "console/package.json"), `"version": "1.2.3"`)
}

func TestRun_WriteSyncsOnlyVersionSpans(t *testing.T) {
	root := newRepo(t, "2.0.0", "1.2.3")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, run([]string{"-write"}, root, &stdout, &stderr), stderr.String())
	assert.Contains(t, stdout.String(), "synced console/package.json: 1.2.3 -> 2.0.0")

	assert.Equal(t, packageJSON("2.0.0"), readFile(t, root, "console/package.json"))
	lock := readFile(t, root, "protocol/node/package-lock.json")
	assert.Equal(t, packageLockJSON("2.0.0"), lock)
	assert.Contains(t, lock, `"version": "9.9.9"`)
	assert.Equal(t, "# Doc\n\nVersion: v2.0.0\n", readFile(t, root, "protocol/docs/mcp.md"))

	stdout.Reset()
	require.Equal(t, 0, run([]string{"-check"}, root, &stdout, &stderr))
}

// Windows checkouts with core.autocrlf=true see CRLF lockfiles.
func TestRun_CheckAcceptsCRLFLockfiles(t *testing.T) {
	root := newRepo(t, "1.2.3", "1.2.3")
	for _, pkg := range []string{"console", "protocol/node"} {
		writeFile(t, root, pkg+"/package-lock.json", strings.ReplaceAll(packageLockJSON("1.2.3"), "\n", "\r\n"))
	}
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, run([]string{"-check"}, root, &stdout, &stderr), stderr.String())
}

func TestRun_AmbiguousDeclarationFails(t *testing.T) {
	root := newRepo(t, "1.0.0", "1.0.0")
	writeFile(t, root, "protocol/docs/a2a.md", "Version: v1.0.0\nVersion: v1.0.0\n")
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 1, run([]string{"-write"}, root, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "expected exactly one version declaration")
}

func TestRun_RejectsBadModesAndVersion(t *testing.T) {
	root := newRepo(t, "1.0.0", "1.0.0")
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 2, run(nil, root, &stdout, &stderr))
	assert.Equal(t, 2, run([]string{"-check", "-write"}, root, &stdout, &stderr))

	writeFile(t, root, "VERSION", "1.0\n")
	assert.Equal(t, 1, run([]string{"-check"}, root, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "VERSION file is not semver")
}
