// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package jitllm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jitllm/jitllm/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jitllm/jitllmtest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestListResolveAndDescribeContainer(t *testing.T) {
	root := testutil.TempDir(t)
	path := jitllmtest.WriteChatContainer(t, testutil.TempDir(t), root, "stories")
	_, err := server.New(server.Config{ModelDir: root}).SetModelOptions("stories", []string{"tiny"}, nil, nil)
	require.NoError(t, err)

	listed, err := List(root)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "stories", listed[0].ID)
	assert.Equal(t, path, listed[0].Path)
	assert.Equal(t, []string{"tiny"}, listed[0].Aliases)

	for _, name := range []string{"stories", "stories.jlm", "tiny"} {
		resolved, err := Resolve(root, name)
		require.NoError(t, err, name)
		assert.Equal(t, path, resolved.Path, name)
	}
	_, err = Resolve(root, "missing")
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)

	metadata, err := ReadMetadata(path)
	require.NoError(t, err)
	assert.Equal(t, "llama", metadata.Architecture)
	assert.Equal(t, "F32", metadata.Quantization, "stories260K is all F32")
	assert.Greater(t, metadata.ParameterCount, uint64(200_000))
	assert.Less(t, metadata.ParameterCount, uint64(400_000))
	assert.False(t, metadata.Vision)
}

func TestReadMetadataRejectsAForeignFile(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "notes.jlm")
	require.NoError(t, os.WriteFile(path, make([]byte, 4096), constants.PermFilePrivate))
	_, err := ReadMetadata(path)
	require.ErrorIs(t, err, constants.ErrJitLLMContainerInvalid)
}
