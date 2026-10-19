// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func testModelProvenanceWindow(attemptID string) (*evalv1.ModelProvenanceAttestationWindow, []byte) {
	digest := strings.Repeat("a", 64)
	window := &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              model_provenance.SchemaVersion,
		ProviderAttemptId:          attemptID,
		ProvenanceOperatorId:       "test-provenance-operator",
		ServedModelTag:             "probe-model:7b",
		ExpectedModelDigest:        digest,
		ObservedModelDigest:        digest,
		ManifestDigest:             strings.Repeat("b", 64),
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED,
		AttestedAtUnixMs:           1_700_000_000_000,
		DigestMatch:                true,
	}
	attestationDigest, err := model_provenance.ComputeAttestationDigest(window)
	if err != nil {
		panic(err)
	}
	window.AttestationDigest = attestationDigest

	windowBody, err := evalv1.MarshalCanonical(window)
	if err != nil {
		panic(err)
	}
	respBody, err := json.Marshal(models.ModelProvenanceResponse{Window: windowBody})
	if err != nil {
		panic(err)
	}
	return window, respBody
}

func TestRemoteModelProvenanceClient_Load_RejectsMissingInputs(t *testing.T) {
	t.Parallel()
	_, err := (&remoteModelProvenanceClient{}).Load(context.Background(), "attempt-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotFound)

	client := &remoteModelProvenanceClient{client: &cmdtest.MockAPIClient{}}
	_, err = client.Load(context.Background(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestRemoteModelProvenanceClient_Load_ReturnsGatewayWindow(t *testing.T) {
	t.Parallel()
	window, respBody := testModelProvenanceWindow("attempt-1")
	apiClient := &cmdtest.MockAPIClient{GetResp: respBody}
	remote := &remoteModelProvenanceClient{client: apiClient}

	loaded, err := remote.Load(context.Background(), "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, window.GetProviderAttemptId(), loaded.GetProviderAttemptId())
	assert.Equal(t, []string{constants.APIPaths.InferenceModelProvenanceAttestations + "attempt-1"}, apiClient.GetCalls)

	cached, err := remote.Load(context.Background(), "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, loaded.GetAttestationDigest(), cached.GetAttestationDigest())
	assert.Len(t, apiClient.GetCalls, 1, "second load should use cache")
}

func TestRemoteModelProvenanceClient_Load_PropagatesGatewayErrors(t *testing.T) {
	t.Parallel()
	apiClient := &cmdtest.MockAPIClient{GetErr: fmt.Errorf("network down")}
	remote := &remoteModelProvenanceClient{client: apiClient}

	_, err := remote.Load(context.Background(), "attempt-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway read")
}

func TestRemoteModelProvenanceClient_Load_RejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	apiClient := &cmdtest.MockAPIClient{GetResp: []byte("not-json")}
	remote := &remoteModelProvenanceClient{client: apiClient}

	_, err := remote.Load(context.Background(), "attempt-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}

func TestNewModelProvenanceRemote_ReturnsClientWhenGatewayHealthy(t *testing.T) {
	WithGatewayHealthCheck(t, true)
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	remote, err := newModelProvenanceRemote(fileSvc, cfg)
	require.NoError(t, err)
	require.NotNil(t, remote)
}
