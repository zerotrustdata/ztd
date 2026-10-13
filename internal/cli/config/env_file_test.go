// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// unsetEnvForTest clears key for the test and restores it afterwards.
func unsetEnvForTest(t *testing.T, key constants.EnvVarKey) {
	t.Helper()
	t.Setenv(string(key), "")
	require.NoError(t, os.Unsetenv(string(key)))
}

func writeEnvFile(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(testutil.TempDir(t), constants.EnvFilename)
	require.NoError(t, os.WriteFile(path, []byte(content), perm))
	require.NoError(t, os.Chmod(path, perm))
	return path
}

func TestLoadEnvFile(t *testing.T) {
	token := constants.EnvVar.CloudflareAPIToken
	ollama := constants.EnvVar.OllamaEndpoint

	t.Run("loads registry keys and skips comments", func(t *testing.T) {
		unsetEnvForTest(t, token)
		unsetEnvForTest(t, ollama)
		path := writeEnvFile(t, "# comment\n\nexport "+string(token)+"=\"tok en\"\n"+string(ollama)+"='http://10.0.0.2:11434'\n", constants.PermFilePrivate)

		require.NoError(t, LoadEnvFile(path))
		assert.Equal(t, "tok en", os.Getenv(string(token)))
		assert.Equal(t, "http://10.0.0.2:11434", os.Getenv(string(ollama)))
	})

	t.Run("exported variable wins", func(t *testing.T) {
		t.Setenv(string(token), "from-shell")
		path := writeEnvFile(t, string(token)+"=from-file\n", constants.PermFilePrivate)

		require.NoError(t, LoadEnvFile(path))
		assert.Equal(t, "from-shell", os.Getenv(string(token)))
	})

	t.Run("missing file loads nothing", func(t *testing.T) {
		require.NoError(t, LoadEnvFile(filepath.Join(testutil.TempDir(t), constants.EnvFilename)))
	})

	t.Run("unknown key is rejected", func(t *testing.T) {
		path := writeEnvFile(t, "ZTD_NOT_REGISTERED=x\n", constants.PermFilePrivate)
		assert.ErrorIs(t, LoadEnvFile(path), constants.ErrEnvFileUnknownKey)
	})

	t.Run("host key is rejected", func(t *testing.T) {
		path := writeEnvFile(t, string(constants.EnvVar.Home)+"=/tmp\n", constants.PermFilePrivate)
		assert.ErrorIs(t, LoadEnvFile(path), constants.ErrEnvFileUnknownKey)
	})

	t.Run("malformed line is rejected", func(t *testing.T) {
		path := writeEnvFile(t, "not an assignment\n", constants.PermFilePrivate)
		assert.ErrorIs(t, LoadEnvFile(path), constants.ErrEnvFileMalformed)
	})

	t.Run("group or world readable file is rejected", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("permission check is enforced on Linux and macOS")
		}
		unsetEnvForTest(t, token)
		path := writeEnvFile(t, string(token)+"=x\n", 0o644)
		assert.ErrorIs(t, LoadEnvFile(path), constants.ErrEnvFileInvalid)
		_, set := os.LookupEnv(string(token))
		assert.False(t, set)
	})

	t.Run("symlink is rejected", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("O_NOFOLLOW is enforced on Linux and macOS")
		}
		target := writeEnvFile(t, string(token)+"=x\n", constants.PermFilePrivate)
		link := filepath.Join(testutil.TempDir(t), constants.EnvFilename)
		require.NoError(t, os.Symlink(target, link))
		assert.Error(t, LoadEnvFile(link))
	})
}

func TestInferenceOllamaEndpoint(t *testing.T) {
	key := string(constants.EnvVar.OllamaEndpoint)

	t.Setenv(key, " http://10.0.0.2:11434 ")
	assert.Equal(t, "http://10.0.0.3:11434", InferenceOllamaEndpoint(" http://10.0.0.3:11434 "), "flag wins")
	assert.Equal(t, "http://10.0.0.2:11434", InferenceOllamaEndpoint("  "), "env fills a blank flag")

	t.Setenv(key, "")
	assert.Empty(t, InferenceOllamaEndpoint(""))
}

func TestWithoutUserEnv(t *testing.T) {
	environ := []string{
		"HOME=/home/u",
		string(constants.EnvVar.CloudflareAPIToken) + "=token",
		string(constants.EnvVar.OllamaEndpoint) + "=http://10.0.0.2:11434",
		"PATH=/usr/bin",
	}
	assert.Equal(t, []string{"HOME=/home/u", "PATH=/usr/bin"}, WithoutUserEnv(environ))
}
