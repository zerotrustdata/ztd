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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jitllm/jitllmtest"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestJitLLMStorageAttestor_AttestsContainerDigest(t *testing.T) {
	root := testutil.TempDir(t)
	path := jitllmtest.WriteChatContainer(t, testutil.TempDir(t), root, "stories")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	digest := models.SHA256Hex(body)
	attestor, err := NewJitLLMStorageAttestor(root, "provenance-under-test")
	require.NoError(t, err)

	window, err := attestor.Attest(testutil.Context(t), "stories", digest, time.Unix(1_700_000_000, 0).UTC())
	require.NoError(t, err)
	assert.True(t, window.DigestMatch)
	assert.Equal(t, digest, window.ObservedModelDigest)
	assert.Equal(t, evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNAVAILABLE, window.ManifestVerificationStatus)
	require.Len(t, window.WeightAttestations, 1)
	assert.Equal(t, constants.ModelWeightMediaTypeJLM, window.WeightAttestations[0].MediaType)
	assert.Equal(t, digest, window.WeightAttestations[0].BlobDigest)
	assert.Equal(t, uint64(len(body)), window.WeightAttestations[0].SizeBytes)

	window, err = attestor.Attest(testutil.Context(t), "stories", models.SHA256Hex([]byte("other")), time.Unix(1_700_000_000, 0).UTC())
	require.NoError(t, err)
	assert.False(t, window.DigestMatch)

	_, err = attestor.Attest(testutil.Context(t), "absent", digest, time.Unix(1_700_000_000, 0).UTC())
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
}
