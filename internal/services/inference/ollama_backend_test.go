// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestTransportError_ClassifiesNetworkTimeoutAndPreservesCause(t *testing.T) {
	cause := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	err := transportError(t.Context(), "generate", cause)
	require.ErrorIs(t, err, constants.ErrInferenceBackendTimeout)
	require.ErrorIs(t, err, cause)
	require.NotErrorIs(t, err, constants.ErrInferenceBackendUnavailable)

	deadlineErr := fmt.Errorf("provider request: %w", context.DeadlineExceeded)
	err = transportError(t.Context(), "generate", deadlineErr)
	require.ErrorIs(t, err, constants.ErrInferenceBackendTimeout)
	assert.ErrorIs(t, err, deadlineErr)
}

func TestEncodeOllamaKeepAlive(t *testing.T) {
	t.Parallel()
	encoded, err := encodeOllamaKeepAlive("-1")
	require.NoError(t, err)
	assert.Equal(t, json.RawMessage("-1"), encoded) //nolint:testifylint // exact bytes, not JSON equivalence
	encoded, err = encodeOllamaKeepAlive("5m")
	require.NoError(t, err)
	assert.Equal(t, json.RawMessage(`"5m"`), encoded) //nolint:testifylint // exact bytes, not JSON equivalence
}

func TestOllamaBackend_GenerateRejectsUnsupportedToolChoiceAndThinkingLevel(t *testing.T) {
	t.Parallel()
	backend, err := NewOllamaBackend("http://127.0.0.1:1", testutil.NewTestLogger())
	require.NoError(t, err)
	tests := []struct {
		name string
		req  models.GenerateRequest
	}{
		{
			name: "required tool choice",
			req: models.GenerateRequest{Model: "test-model", ToolChoice: &operatorv1.InferenceToolChoice{
				Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_REQUIRED,
			}},
		},
		{
			name: "thinking level",
			req: models.GenerateRequest{Model: "test-model", Thinking: &operatorv1.InferenceThinkingControl{
				Mode: &operatorv1.InferenceThinkingControl_Level{Level: "high"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := backend.Generate(context.Background(), tt.req)

			require.Error(t, err)
			assert.Nil(t, response)
			assert.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)
		})
	}
}

func TestOllamaBackend_GenerateEmptyModelReturnsErrInferenceModelRefInvalid(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	backend, err := NewOllamaBackend("http://127.0.0.1:11434", logger)
	require.NoError(t, err)

	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, constants.ErrInferenceModelRefInvalid)
}

func TestOllamaBackend_GenerateUnavailableReturnsErrInferenceBackendUnavailable(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	// Use a port that's almost certainly not listening
	backend, err := NewOllamaBackend("http://127.0.0.1:1", logger)
	require.NoError(t, err)

	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
}

func TestOllamaBackend_StatusUnavailableReturnsErrInferenceBackendUnavailable(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	backend, err := NewOllamaBackend("http://127.0.0.1:1", logger)
	require.NoError(t, err)

	status, err := backend.Status(context.Background())

	require.Error(t, err)
	assert.Nil(t, status)
	assert.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
}

func TestOllamaBackend_NewRejectsInvalidEndpoint(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()

	cases := []struct {
		name     string
		endpoint string
	}{
		{name: "empty", endpoint: ""},
		{name: "unparseable", endpoint: "://missing-scheme"},
		{name: "unsupported scheme", endpoint: "ftp://192.168.1.2:11434"},
		{name: "missing host", endpoint: "http://"},
		{name: "scheme-relative", endpoint: "//192.168.1.2:11434"},
		{name: "bare host", endpoint: "192.168.1.2:11434"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, err := NewOllamaBackend(tc.endpoint, logger)
			require.Error(t, err)
			assert.Nil(t, backend)
			assert.ErrorIs(t, err, constants.ErrInferenceEndpointInvalid)
		})
	}
}

func TestOllamaBackend_GeneratePreservesUnderlyingTransportCause(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()

	backend, err := NewOllamaBackend("http://127.0.0.1:1", logger)
	require.NoError(t, err)
	resp, err := backend.Generate(context.Background(), models.GenerateRequest{
		Role:  models.InferenceModelRolePrimary,
		Model: "test-model",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
	var urlErr *url.Error
	assert.ErrorAs(t, err, &urlErr, "the underlying transport error must stay in the chain")
}
