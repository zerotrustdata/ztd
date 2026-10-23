// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestInferenceHTTP_TimeoutCoversApprovalAndDispatchDeadline(t *testing.T) {
	client, err := New(config.Config{MTLSBaseURL: "http://inference.test"})
	require.NoError(t, err)

	hc := client.inferenceHTTP()

	assert.Equal(t, mcp.ApprovalRequestTTL+dispatch.RequestDeadline, hc.Timeout)
	assert.Greater(t, hc.Timeout, mcp.ApprovalRequestTTL+inference.ProviderRequestTimeout, "the timeout must outlast the approval wait plus the provider request")
	assert.Same(t, client.http.Transport, hc.Transport)
}

func TestInferenceDispatchFailure_MapsCodeToSentinel(t *testing.T) {
	body, err := protojson.Marshal(&operatorv1.InferenceDispatchFailure{
		Error: constants.ErrInferenceToolsUnsupported.Error(),
		Code:  operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_TOOLS_UNSUPPORTED,
	})
	require.NoError(t, err)

	err = inferenceDispatchFailure(http.StatusUnprocessableEntity, body)
	require.ErrorIs(t, err, constants.ErrInferenceToolsUnsupported)
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
}

func TestInferenceDispatchFailure_UncodedBodyIsStatusErrorOnly(t *testing.T) {
	err := inferenceDispatchFailure(http.StatusForbidden, []byte(`{"error":"forbidden"}`))
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
	require.NotErrorIs(t, err, constants.ErrInferenceGovernanceRejected)

	err = inferenceDispatchFailure(http.StatusBadGateway, []byte("not json"))
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
}
