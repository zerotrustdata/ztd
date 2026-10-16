// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
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

func TestClient_DispatchInference_FailedReceiptPreserved(t *testing.T) {
	expectedReceipt := &operatorv1.ActionReceipt{
		TransactionId: "tx-inference-failed-002",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		Signature:     "sig-failed-002",
		ResultSummary: "provider timeout",
	}
	body, err := protojson.Marshal(&operatorv1.InferenceDispatchFailure{
		Error:   constants.ErrInferenceBackendTimeout.Error(),
		Code:    operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_TIMEOUT,
		Receipt: expectedReceipt,
	})
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, constants.APIPaths.InferenceDispatch, r.URL.Path)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)

	req := &operatorv1.InferenceDispatchRequest{
		Role:     operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}}}},
	}
	_, _, dispatchErr := client.DispatchInference(context.Background(), req)
	require.Error(t, dispatchErr)
	assert.ErrorIs(t, dispatchErr, constants.ErrInferenceBackendTimeout)

	failedReceipt := governance.FailedReceipt(dispatchErr)
	require.NotNil(t, failedReceipt)
	assert.True(t, proto.Equal(expectedReceipt, failedReceipt))
}

func TestClient_DispatchInference_NoReceiptYieldsNilFailedReceipt(t *testing.T) {
	body, err := protojson.Marshal(&operatorv1.InferenceDispatchFailure{
		Error: constants.ErrInferenceGovernanceRejected.Error(),
		Code:  operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED,
	})
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)

	req := &operatorv1.InferenceDispatchRequest{
		Role:     operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "hi"}}}}},
	}
	_, _, dispatchErr := client.DispatchInference(context.Background(), req)
	require.Error(t, dispatchErr)
	assert.ErrorIs(t, dispatchErr, constants.ErrInferenceGovernanceRejected)

	assert.Nil(t, governance.FailedReceipt(dispatchErr))
}
