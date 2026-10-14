// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package ssh

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestBuildAuthMethods(t *testing.T) {
	t.Run("empty host config", func(t *testing.T) {
		r := HostConfig{}
		methods, err := BuildAuthMethods(r, "", "")
		require.NoError(t, err)
		assert.Empty(t, methods)
	})

	t.Run("non-existent key file", func(t *testing.T) {
		r := HostConfig{KeyFiles: []string{"/nonexistent/key"}}
		_, err := BuildAuthMethods(r, "", "")
		require.Error(t, err)
	})

	t.Run("invalid key file content", func(t *testing.T) {
		dir := testutil.TempDir(t)
		keyPath := filepath.Join(dir, "invalid_key")
		require.NoError(t, os.WriteFile(keyPath, []byte("not a valid key"), 0600))

		r := HostConfig{KeyFiles: []string{keyPath}}
		_, err := BuildAuthMethods(r, "", "")
		require.Error(t, err)
	})

	t.Run("invalid SSH agent socket", func(t *testing.T) {
		r := HostConfig{}
		_, err := BuildAuthMethods(r, "/nonexistent/agent.sock", "")
		require.Error(t, err)
	})

	t.Run("empty key file", func(t *testing.T) {
		dir := testutil.TempDir(t)
		keyPath := filepath.Join(dir, "empty_key")
		require.NoError(t, os.WriteFile(keyPath, []byte(""), 0600))

		r := HostConfig{KeyFiles: []string{keyPath}}
		_, err := BuildAuthMethods(r, "", "")
		require.Error(t, err)
	})
}
