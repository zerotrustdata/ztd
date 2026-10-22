// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

func decisionInput() *operatorv1.InferenceDecisionRequest {
	return &operatorv1.InferenceDecisionRequest{State: "Read the workspace inventory", Questions: []*operatorv1.InferenceDecisionQuestion{{Name: "safe", Instructions: "Is this read-only?", Kind: &operatorv1.InferenceDecisionQuestion_Noul{Noul: &operatorv1.InferenceNoulQuestion{}}}}}
}

type decisionTransport func(*http.Request) (*http.Response, error)

func (f decisionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type decisionVault struct {
	calls int
	key   string
	err   error
}

func (v *decisionVault) DecryptSecret(name string) (string, error) {
	v.calls++
	if name != TypeSafeCredentialName {
		panic("wrong credential name")
	}
	return v.key, v.err
}
func TestTypeSafeDecisionOptInPinningVaultAndAttestation(t *testing.T) {
	v := &decisionVault{key: "test-vault-only-key"}
	backend, err := NewTypeSafeBackend(v)
	require.NoError(t, err)
	calls := 0
	backend.client.Transport = decisionTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, TypeSafeSystemOneEndpoint, r.URL.String())
		require.Equal(t, "Bearer test-vault-only-key", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), "keep_alive")
		require.NotContains(t, string(body), v.key)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"model":"jev-1.14.0","answers":{"safe":{"type":"noul","noul":0.99}},"usage":{"input_tokens":12,"output_tokens":1}}`)), Header: make(http.Header)}, nil
	})
	req := models.DecisionRequest{Input: decisionInput(), Model: "jev-1.14.0", KeepAlive: "-1"}
	_, err = backend.Decide(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceHostedNotAllowed)
	require.Zero(t, v.calls)
	require.Zero(t, calls)
	req.AllowHosted = true
	req.Input.AllowHosted = true
	req.Model = "jev-latest"
	_, err = backend.Decide(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceModelRefInvalid)
	require.Zero(t, v.calls)
	req.Model = "jev-1.14.0"
	resp, err := backend.Decide(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, v.calls)
	require.Empty(t, resp.ServedModelDigest)
	require.True(t, resp.HostedEgress.ExplicitConsent)
	require.Equal(t, "hosted-unpinned", resp.HostedEgress.Provenance)
	require.NoError(t, models.ValidateDecisionCompletion(req.Input, resp.ToProtoInferenceResult()))
	v.err = constants.ErrVaultLocked
	_, err = backend.Decide(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrVaultLocked)
	require.Equal(t, 1, calls)
	require.ErrorIs(t, backend.client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}
func TestDecisionInferenceRejectsChatControlsAndOversize(t *testing.T) {
	req := &operatorv1.InferenceRequested{Model: "tev1:0.8b", Decision: decisionInput(), RequestSchemaVersion: constants.InferenceRequestSchemaVersion}
	require.NoError(t, ValidateDecisionInference(req))
	for _, edit := range []func(*operatorv1.InferenceRequested){
		func(r *operatorv1.InferenceRequested) { r.Messages = []*operatorv1.InferenceMessage{{}} },
		func(r *operatorv1.InferenceRequested) { r.Stream = true },
		func(r *operatorv1.InferenceRequested) { r.MaxTokens = 1 },
		func(r *operatorv1.InferenceRequested) { r.Thinking = &operatorv1.InferenceThinkingControl{} },
	} {
		copy := proto.Clone(req).(*operatorv1.InferenceRequested)
		edit(copy)
		require.ErrorIs(t, ValidateDecisionInference(copy), constants.ErrInferenceDecisionRequestInvalid)
	}
	req.Decision.State = strings.Repeat("x", maxDecisionBodyBytes)
	require.ErrorIs(t, ValidateDecisionInference(req), constants.ErrInferenceRequestTooLarge)
}

type decisionStub struct {
	stubBackend
	decisions int
}

func (s *decisionStub) Decide(_ context.Context, r models.DecisionRequest) (*models.DecisionResponse, error) {
	s.decisions++
	result := &operatorv1.InferenceDecisionResult{Answers: []*operatorv1.InferenceDecisionAnswer{{Name: "safe", Kind: &operatorv1.InferenceDecisionAnswer_Noul{Noul: .99}}}}
	hash, err := models.ComputeDecisionOutputHash(result)
	if err != nil {
		return nil, err
	}
	return &models.DecisionResponse{Decision: result, Model: r.Model, ServedModelDigest: r.ModelDigest, NormalizedRequestHash: models.SHA256Hex([]byte("decision request")), OutputHash: hash}, nil
}
func TestDecisionHandlerUsesTypedBackendAndSignedResultDigest(t *testing.T) {
	backend := &decisionStub{}
	cfg := &config.Config{Inference: config.InferenceConfig{Enabled: true, DecisionModels: []string{"tev1:0.8b"}}}
	handler := mustNewHandler(t, backend, cfg, mustNewScrubbingSvc(t), testutil.NewTestLogger())
	req := &operatorv1.InferenceRequested{Role: operatorv1.ModelRole_MODEL_ROLE_PRIMARY, Model: "tev1:0.8b", Decision: decisionInput(), ProviderAttemptId: "decision-attempt", RequestSchemaVersion: constants.InferenceRequestSchemaVersion}
	data, err := proto.Marshal(req)
	require.NoError(t, err)
	response, err := handler.ExecuteInference(t.Context(), &testCommandMessage{payload: data})
	require.NoError(t, err)
	require.Equal(t, 1, backend.decisions)
	require.Zero(t, backend.calls)
	require.Empty(t, response.Parts)
	require.NotNil(t, response.Decision)
	digest, err := models.ComputeInferenceResultDigest(response.ToProtoInferenceResult())
	require.NoError(t, err)
	require.Len(t, digest, 64)
	req.Messages = []*operatorv1.InferenceMessage{{}}
	data, err = proto.Marshal(req)
	require.NoError(t, err)
	_, err = handler.ExecuteInference(t.Context(), &testCommandMessage{payload: data})
	require.ErrorIs(t, err, constants.ErrInferenceDecisionRequestInvalid)
	require.Equal(t, 1, backend.decisions)
}
