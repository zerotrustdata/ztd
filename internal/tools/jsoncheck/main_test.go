// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(good, []byte(`{"rules":[{"id":"x"}]}`), 0o644))
	require.NoError(t, os.WriteFile(bad, []byte(`{"rules":[}`), 0o644))

	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, run([]string{good}, &stdout, &stderr))
	assert.Contains(t, stdout.String(), "valid JSON: "+good)

	stdout.Reset()
	assert.Equal(t, 1, run([]string{good, bad, filepath.Join(dir, "missing.json")}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "parse "+bad)
	assert.Contains(t, stderr.String(), "missing.json")

	assert.Equal(t, 2, run(nil, &stdout, &stderr))
}
