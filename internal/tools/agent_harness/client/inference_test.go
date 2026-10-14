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
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestInferenceDispatchFailure_MapsCodeToSentinel(t *testing.T) {
	body, err := protojson.Marshal(&operatorv1.InferenceDispatchFailure{
		Error: constants.ErrInferenceToolsUnsupported.Error(),
		Code:  operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_TOOLS_UNSUPPORTED,
	})
	require.NoError(t, err)

	err = inferenceDispatchFailure(http.StatusUnprocessableEntity, body)
	assert.ErrorIs(t, err, constants.ErrInferenceToolsUnsupported)
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
}

func TestInferenceDispatchFailure_UncodedBodyIsStatusErrorOnly(t *testing.T) {
	err := inferenceDispatchFailure(http.StatusForbidden, []byte(`{"error":"forbidden"}`))
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.NotErrorIs(t, err, constants.ErrInferenceGovernanceRejected)

	err = inferenceDispatchFailure(http.StatusBadGateway, []byte("not json"))
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
}
