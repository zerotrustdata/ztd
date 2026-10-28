// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package model_provenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func writeLlamaCppModelFile(t *testing.T, root, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), constants.PermDirPrivate))
	require.NoError(t, os.WriteFile(path, body, constants.PermFilePrivate))
	return models.SHA256Hex(body)
}

func TestLlamaCppStorageAttestor_AttestsSingleFileModel(t *testing.T) {
	root := testutil.TempDir(t)
	body := []byte("synthetic gguf payload")
	digest := writeLlamaCppModelFile(t, root, "qwen3.gguf", body)
	attestor, err := NewLlamaCppStorageAttestor(root, "test-provenance-operator")
	require.NoError(t, err)

	window, err := attestor.Attest(testutil.Context(t), "qwen3", digest, time.UnixMilli(1_700_000_000_000))
	require.NoError(t, err)
	assert.True(t, window.GetDigestMatch())
	assert.Equal(t, digest, window.GetObservedModelDigest(), "a single-file model digest is its GGUF checksum")
	assert.Empty(t, window.GetManifestDigest())
	assert.Equal(t, evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNAVAILABLE, window.GetManifestVerificationStatus())
	require.Len(t, window.GetWeightAttestations(), 1)
	weight := window.GetWeightAttestations()[0]
	assert.Equal(t, digest, weight.GetBlobDigest())
	assert.Equal(t, uint64(len(body)), weight.GetSizeBytes())
	assert.Equal(t, constants.ModelWeightMediaTypeGGUF, weight.GetMediaType())
	assert.Equal(t, fullHash, weight.GetVerificationMode())
	assert.Positive(t, weight.GetFullHashCompletedAtUnixMs())
}

func TestLlamaCppStorageAttestor_ComposesSplitModelAndAttestsSidecars(t *testing.T) {
	root := testutil.TempDir(t)
	first := writeLlamaCppModelFile(t, root, filepath.Join("big", "big-00001-of-00002.gguf"), []byte("shard one"))
	second := writeLlamaCppModelFile(t, root, filepath.Join("big", "big-00002-of-00002.gguf"), []byte("shard two"))
	projector := writeLlamaCppModelFile(t, root, filepath.Join("big", "mmproj-big.gguf"), []byte("projector"))
	composed := models.SHA256Hex([]byte(first + "\n" + second))
	attestor, err := NewLlamaCppStorageAttestor(root, "test-provenance-operator")
	require.NoError(t, err)

	window, err := attestor.Attest(testutil.Context(t), "big", composed, time.UnixMilli(1_700_000_000_000))
	require.NoError(t, err)
	assert.True(t, window.GetDigestMatch())
	assert.Equal(t, composed, window.GetObservedModelDigest())
	var attested []string
	for _, weight := range window.GetWeightAttestations() {
		attested = append(attested, weight.GetBlobDigest())
	}
	assert.Equal(t, []string{first, second, projector}, attested, "the projector is attested but not part of the model digest")
}

func TestLlamaCppStorageAttestor_ReportsAMismatchedModel(t *testing.T) {
	root := testutil.TempDir(t)
	digest := writeLlamaCppModelFile(t, root, "qwen3.gguf", []byte("served weights"))
	attestor, err := NewLlamaCppStorageAttestor(root, "")
	require.NoError(t, err)

	expected := models.SHA256Hex([]byte("campaign weights"))
	window, err := attestor.Attest(testutil.Context(t), "qwen3", expected, time.UnixMilli(1_700_000_000_000))
	require.NoError(t, err)
	assert.False(t, window.GetDigestMatch())
	assert.Equal(t, digest, window.GetObservedModelDigest())
	assert.Equal(t, expected, window.GetExpectedModelDigest())
}

func TestLlamaCppStorageAttestor_FailsClosed(t *testing.T) {
	root := testutil.TempDir(t)
	digest := writeLlamaCppModelFile(t, root, "qwen3.gguf", []byte("weights"))
	attestor, err := NewLlamaCppStorageAttestor(root, "")
	require.NoError(t, err)

	_, err = NewLlamaCppStorageAttestor(" ", "")
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	_, err = attestor.Attest(testutil.Context(t), "", digest, time.Now())
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	_, err = attestor.Attest(testutil.Context(t), "qwen3", "not-a-digest", time.Now())
	require.ErrorIs(t, err, constants.ErrInferenceEvidenceHashInvalid)
	_, err = attestor.Attest(testutil.Context(t), "absent", digest, time.Now())
	require.ErrorIs(t, err, constants.ErrLlamaCppModelNotFound)
	_, err = attestor.Attest(testutil.Context(t), "../qwen3", digest, time.Now())
	require.ErrorIs(t, err, constants.ErrLlamaCppModelNotFound)
}
