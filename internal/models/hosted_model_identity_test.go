// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	hostedTestOrigin  = "https://generativelanguage.googleapis.com"
	hostedTestModel   = "gemini-3.6-flash"
	hostedTestVersion = "3.6-flash-07-2026"
)

func TestComputeHostedModelIdentityDigest(t *testing.T) {
	t.Parallel()
	digest, err := ComputeHostedModelIdentityDigest(constants.InferenceProviderGemini, hostedTestOrigin, hostedTestModel, hostedTestVersion)
	require.NoError(t, err)
	assert.True(t, IsSHA256Hex(digest))
	want := SHA256Hex([]byte("ztd.hosted-model-identity.v1\n" + `{"provider_class":"gemini","provider_origin":"https://generativelanguage.googleapis.com","model":"gemini-3.6-flash","provider_model_version":"3.6-flash-07-2026"}`))
	assert.Equal(t, want, digest, "the digest is a fixed, domain-separated record")

	changed, err := ComputeHostedModelIdentityDigest(constants.InferenceProviderGemini, hostedTestOrigin, hostedTestModel, "3.6-flash-09-2026")
	require.NoError(t, err)
	assert.NotEqual(t, digest, changed, "a re-versioned model behind the same ID changes the digest")

	for name, args := range map[string][4]string{
		"local class":   {constants.InferenceProviderOllama, hostedTestOrigin, hostedTestModel, hostedTestVersion},
		"no origin":     {constants.InferenceProviderGemini, "", hostedTestModel, hostedTestVersion},
		"no model":      {constants.InferenceProviderGemini, hostedTestOrigin, "", hostedTestVersion},
		"no version":    {constants.InferenceProviderGemini, hostedTestOrigin, hostedTestModel, ""},
		"unknown class": {"acme", hostedTestOrigin, hostedTestModel, hostedTestVersion},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ComputeHostedModelIdentityDigest(args[0], args[1], args[2], args[3])
			require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		})
	}
}

func TestHostedDispatchProviderClass(t *testing.T) {
	t.Parallel()
	assert.Equal(t, constants.InferenceProviderGemini, HostedDispatchProviderClass(constants.InferenceProviderGemini))
	assert.Empty(t, HostedDispatchProviderClass(constants.InferenceProviderOllama))
	assert.Empty(t, HostedDispatchProviderClass(constants.InferenceProviderLlamaCpp))
	assert.Empty(t, HostedDispatchProviderClass(""))
}

func validHostedResult(t *testing.T, pinned bool) *operatorv1.InferenceResult {
	t.Helper()
	digest, err := ComputeHostedModelIdentityDigest(constants.InferenceProviderGemini, hostedTestOrigin, hostedTestModel, hostedTestVersion)
	require.NoError(t, err)
	requestHash := SHA256Hex([]byte("request"))
	result := &operatorv1.InferenceResult{
		Model:                 hostedTestModel,
		RequestedModel:        hostedTestModel,
		NormalizedRequestHash: requestHash,
		HostedEgress: &operatorv1.InferenceHostedEgressAttestation{
			ProviderOrigin:        hostedTestOrigin,
			ModelVersion:          hostedTestModel,
			Provenance:            constants.HostedModelProvenance,
			NormalizedRequestHash: requestHash,
			ExplicitConsent:       true,
			ProviderClass:         constants.InferenceProviderGemini,
			RequestedModel:        hostedTestModel,
			ProviderModelVersion:  hostedTestVersion,
			ModelIdentityDigest:   digest,
			ProviderResponseId:    "r1",
			RawResponseDigest:     SHA256Hex([]byte("raw")),
			RawResponseSizeBytes:  3,
		},
	}
	if pinned {
		result.ServedModelDigest = digest
		result.RequestedModelDigest = digest
	}
	return result
}

func TestValidateHostedEgress(t *testing.T) {
	t.Parallel()
	digest := validHostedResult(t, true).GetHostedEgress().GetModelIdentityDigest()
	require.NoError(t, ValidateHostedEgress(constants.InferenceProviderGemini, hostedTestModel, "", validHostedResult(t, false)))
	require.NoError(t, ValidateHostedEgress(constants.InferenceProviderGemini, hostedTestModel, digest, validHostedResult(t, true)))
	require.NoError(t, ValidateHostedEgress("", "qwen3:4b", "", &operatorv1.InferenceResult{Model: "qwen3:4b"}))

	for name, tc := range map[string]struct {
		class, model, pinned string
		mutate               func(*operatorv1.InferenceResult)
		want                 error
	}{
		"a local result carrying an attestation": {"", hostedTestModel, "", func(*operatorv1.InferenceResult) {}, constants.ErrInferenceIdentityMismatch},
		"an unknown class":                       {"acme", hostedTestModel, "", func(*operatorv1.InferenceResult) {}, constants.ErrInferenceHostedNotAllowed},
		"no attestation":                         {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress = nil }, constants.ErrInferenceIdentityMismatch},
		"no consent":                             {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.ExplicitConsent = false }, constants.ErrInferenceIdentityMismatch},
		"another class":                          {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.ProviderClass = constants.InferenceProviderOpenAI }, constants.ErrInferenceIdentityMismatch},
		"another origin":                         {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.ProviderOrigin = "https://proxy.example" }, constants.ErrInferenceIdentityMismatch},
		"another provenance label":               {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.Provenance = "hosted-unpinned" }, constants.ErrInferenceIdentityMismatch},
		"another requested model":                {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.RequestedModel = "gemini-2.5-flash" }, constants.ErrInferenceIdentityMismatch},
		"a result model that differs":            {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.Model = "gemini-2.5-flash" }, constants.ErrInferenceIdentityMismatch},
		"no served model":                        {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.ModelVersion = "" }, constants.ErrInferenceIdentityMismatch},
		"another request hash":                   {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.NormalizedRequestHash = SHA256Hex([]byte("x")) }, constants.ErrInferenceIdentityMismatch},
		"no raw response digest":                 {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.RawResponseDigest = "" }, constants.ErrInferenceIdentityMismatch},
		"an empty raw response":                  {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.RawResponseSizeBytes = 0 }, constants.ErrInferenceIdentityMismatch},
		"a digest that does not recompute":       {constants.InferenceProviderGemini, hostedTestModel, "", func(r *operatorv1.InferenceResult) { r.HostedEgress.ProviderModelVersion = "other" }, constants.ErrInferenceIdentityMismatch},
		"a pinned digest the identity misses":    {constants.InferenceProviderGemini, hostedTestModel, SHA256Hex([]byte("pinned")), func(*operatorv1.InferenceResult) {}, constants.ErrInferenceModelDigestMismatch},
		"a pinned call the provider rerouted":    {constants.InferenceProviderGemini, hostedTestModel, digest, func(r *operatorv1.InferenceResult) { r.HostedEgress.ModelVersion = "gemini-3.7-flash" }, constants.ErrInferenceModelDigestMismatch},
		"a pinned call with no served digest":    {constants.InferenceProviderGemini, hostedTestModel, digest, func(r *operatorv1.InferenceResult) { r.ServedModelDigest = "" }, constants.ErrInferenceModelDigestMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			result := proto.Clone(validHostedResult(t, tc.pinned != "")).(*operatorv1.InferenceResult)
			tc.mutate(result)
			require.ErrorIs(t, ValidateHostedEgress(tc.class, tc.model, tc.pinned, result), tc.want)
		})
	}
}
