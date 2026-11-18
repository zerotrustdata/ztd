// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func hostedInferencePayload(t *testing.T, providerClass string) []byte {
	t.Helper()
	return mustMarshalInferenceRequested(t, &operatorv1.InferenceRequested{
		RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
		Role:                 operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		ProviderClass:        providerClass,
		Model:                geminiTestModel,
		Messages:             textInferenceMessages("What is the weather in Paris?"),
		Tools:                []*operatorv1.InferenceToolDeclaration{weatherTool()},
	})
}

func newHostedTestHandler(t *testing.T, local *stubBackend, hosted map[string]HostedBackend) *InferenceExecutionHandler {
	t.Helper()
	cfg := &config.Config{Inference: config.InferenceConfig{Enabled: true}}
	handler := mustNewHandler(t, local, cfg, mustNewScrubbingSvc(t), testutil.NewTestLogger())
	handler.ConfigureHostedBackends(hosted)
	return handler
}

func TestInferenceHandler_HostedProviderClassRoutesToItsClient(t *testing.T) {
	t.Parallel()
	local := &stubBackend{}
	fake := &geminiFake{stream: geminiToolCallStream(geminiTestModel)}
	handler := newHostedTestHandler(t, local, map[string]HostedBackend{constants.InferenceProviderGemini: newFakeGeminiBackend(t, fake)})

	resp, err := handler.ExecuteInference(testutil.Context(t), &testCommandMessage{payload: hostedInferencePayload(t, constants.InferenceProviderGemini)})
	require.NoError(t, err)

	assert.Zero(t, local.calls, "a hosted request never reaches the local provider")
	assert.Equal(t, 1, fake.posts())
	require.NotNil(t, resp.HostedEgress)
	assert.Equal(t, geminiTestModel, resp.RequestedModel)
	call := resp.Parts[0].GetToolCall()
	require.NotNil(t, call)
	assert.Equal(t, geminiCapturedSignature, call.GetProviderSignature(), "scrubbing keeps the opaque signature")
	result := resp.ToProtoInferenceResult()
	require.NoError(t, models.ValidateHostedEgress(constants.InferenceProviderGemini, geminiTestModel, "", result))
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	assert.True(t, models.IsSHA256Hex(digest), "the signed result digest covers the hosted attestation")
}

func TestInferenceHandler_HostedRequestsFailClosed(t *testing.T) {
	t.Parallel()
	t.Run("a class the Operator was not started with", func(t *testing.T) {
		local := &stubBackend{}
		handler := newHostedTestHandler(t, local, nil)
		_, err := handler.ExecuteInference(testutil.Context(t), &testCommandMessage{payload: hostedInferencePayload(t, constants.InferenceProviderGemini)})
		require.ErrorIs(t, err, constants.ErrInferenceHostedNotAllowed)
		assert.Zero(t, local.calls)
	})
	t.Run("an unknown class", func(t *testing.T) {
		handler := newHostedTestHandler(t, &stubBackend{}, map[string]HostedBackend{constants.InferenceProviderGemini: newFakeGeminiBackend(t, &geminiFake{})})
		_, err := handler.ExecuteInference(testutil.Context(t), &testCommandMessage{payload: hostedInferencePayload(t, "acme")})
		require.ErrorIs(t, err, constants.ErrInferenceHostedNotAllowed)
	})
	t.Run("a decision request", func(t *testing.T) {
		handler := newHostedTestHandler(t, &stubBackend{}, nil)
		payload := mustMarshalInferenceRequested(t, &operatorv1.InferenceRequested{
			RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
			Role:                 operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
			ProviderClass:        constants.InferenceProviderGemini,
			Model:                geminiTestModel,
			Decision:             &operatorv1.InferenceDecisionRequest{},
		})
		_, err := handler.ExecuteInference(testutil.Context(t), &testCommandMessage{payload: payload})
		require.ErrorIs(t, err, constants.ErrInferenceDecisionUnsupported)
	})
	t.Run("a local provider that returns a hosted attestation", func(t *testing.T) {
		local := &stubBackend{generateResp: &models.GenerateResponse{
			Parts:        textInferenceResponseParts("hi"),
			FinishReason: "stop",
			Model:        geminiTestModel,
			HostedEgress: &operatorv1.InferenceHostedEgressAttestation{ProviderClass: constants.InferenceProviderGemini},
		}}
		handler := newHostedTestHandler(t, local, nil)
		_, err := handler.ExecuteInference(testutil.Context(t), &testCommandMessage{payload: hostedInferencePayload(t, "")})
		require.ErrorIs(t, err, constants.ErrInferenceIdentityMismatch)
	})
	t.Run("a hosted client that returns no attestation", func(t *testing.T) {
		handler := newHostedTestHandler(t, &stubBackend{}, map[string]HostedBackend{constants.InferenceProviderGemini: unattestedHostedBackend{&stubBackend{}}})
		_, err := handler.ExecuteInference(testutil.Context(t), &testCommandMessage{payload: hostedInferencePayload(t, constants.InferenceProviderGemini)})
		require.ErrorIs(t, err, constants.ErrInferenceIdentityMismatch)
	})
}

// unattestedHostedBackend answers as a hosted client that omits its attestation.
type unattestedHostedBackend struct{ *stubBackend }

func (unattestedHostedBackend) ProviderClass() string { return constants.InferenceProviderGemini }

func (unattestedHostedBackend) ListProviderModelInventory(_ context.Context) ([]ProviderModelInventoryEntry, error) {
	return nil, nil
}
