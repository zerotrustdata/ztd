// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// A Tribunal member retry is dispatched through the same path as any governed
// inference: the retry ordinal rides to the dispatch service, whose result check
// requires the signed result to echo it (validateInferenceResult).
func TestNewDispatchInferenceRequest_CarriesTheMemberRetryCount(t *testing.T) {
	for _, retry := range []uint32{0, 1, 3} {
		req, err := newDispatchInferenceRequest(&operatorv1.InferenceDispatchRequest{
			Role:              operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
			Model:             "model:tag",
			ProviderAttemptId: "tribunal-run-2",
			RetryCount:        retry,
			Messages: []*operatorv1.InferenceMessage{{
				Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
				Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hello"}}},
			}},
		}, "app", "user")
		require.NoError(t, err)
		assert.Equal(t, retry, req.RetryCount)
		assert.Equal(t, "tribunal-run-2", req.ProviderAttemptID)
	}
}
