// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package buildinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMakefile_FrontendSourceDiscoveryDoesNotDependOnPlatformFind(t *testing.T) {
	t.Parallel()
	root, err := RepositoryRoot(".")
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)

	makefile := string(body)
	assert.Contains(t, makefile, `$(shell go build -o $(SOURCE_FILES_TOOL) ./internal/tools/sourcefiles 2>&1 || echo "go build exited $$?")`)
	assert.Contains(t, makefile, "source_files = $(shell $(SOURCE_FILES_TOOL) -base . $(1))")
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, "EXPLORER_SOURCES :=") || strings.HasPrefix(line, "CONSOLE_SOURCES :=") {
			assert.Contains(t, line, "$(call source_files,")
			assert.NotContains(t, line, "$(shell find ")
			assert.NotContains(t, line, "/usr/bin/find")
		}
	}
}

// Runtime and website goals must parse without a Go toolchain on PATH:
// fresh-host onboarding runs `make down` from a plain shell, and the website CI
// job has only Node.
func TestMakefile_RuntimeGoalsSkipSourceLister(t *testing.T) {
	t.Parallel()
	root, err := RepositoryRoot(".")
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)

	makefile := string(body)
	assert.Contains(t, makefile, "TOOLCHAIN_FREE_GOALS := help status down stop website-test website-build website-bundle-check")
	assert.Contains(t, makefile, "ifneq ($(filter-out $(TOOLCHAIN_FREE_GOALS),$(or $(MAKECMDGOALS),$(.DEFAULT_GOAL))),)")
}
