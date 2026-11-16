// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package model_provenance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func hostedTestAttempt(t *testing.T, servedModel string) (HostedAttempt, string) {
	t.Helper()
	origin, _ := constants.HostedInferenceProviderOrigin(constants.InferenceProviderGemini)
	digest, err := models.ComputeHostedModelIdentityDigest(constants.InferenceProviderGemini, origin, "gemini-3.6-flash", "3.6-flash-07-2026")
	require.NoError(t, err)
	return HostedAttempt{
		InferenceOperatorID:    "embedded-operator",
		InferenceTransactionID: "tx-1",
		ProviderAttemptID:      "attempt-1",
		ExpectedModelDigest:    digest,
		StartedAtUnixMs:        1000,
		CompletedAtUnixMs:      2000,
		Result: &operatorv1.InferenceResult{
			ResultDigest: models.SHA256Hex([]byte("result")),
			HostedEgress: &operatorv1.InferenceHostedEgressAttestation{
				ProviderOrigin:       origin,
				ModelVersion:         servedModel,
				ProviderClass:        constants.InferenceProviderGemini,
				RequestedModel:       "gemini-3.6-flash",
				ProviderModelVersion: "3.6-flash-07-2026",
				ModelIdentityDigest:  digest,
				ProviderResponseId:   "r1",
				RawResponseDigest:    models.SHA256Hex([]byte("raw")),
			},
		},
	}, digest
}

func TestHostedAttestationWindow_RecordsTheSignedHostedIdentity(t *testing.T) {
	t.Parallel()
	attempt, digest := hostedTestAttempt(t, "gemini-3.6-flash")
	window, err := HostedAttestationWindow(attempt, 3000)
	require.NoError(t, err)

	assert.Equal(t, evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_HOSTED_PROVIDER_REPORTED, window.GetManifestVerificationStatus())
	assert.Equal(t, "embedded-operator", window.GetProvenanceOperatorId(), "the Inference Operator that signed the attestation")
	assert.Equal(t, "gemini-3.6-flash", window.GetServedModelTag())
	assert.Equal(t, digest, window.GetExpectedModelDigest())
	assert.Equal(t, digest, window.GetObservedModelDigest())
	assert.True(t, window.GetDigestMatch())
	assert.Empty(t, window.GetWeightAttestations())
	assert.Equal(t, "tx-1", window.GetHosted().GetInferenceTransactionId())
	assert.Equal(t, attempt.Result.GetResultDigest(), window.GetHosted().GetResultDigest())
	assert.Equal(t, "3.6-flash-07-2026", window.GetHosted().GetProviderModelVersion())
	require.NoError(t, ValidateAttestationWindow(window))
}

func TestHostedAttestationWindow_ARerouteDoesNotMatch(t *testing.T) {
	t.Parallel()
	attempt, _ := hostedTestAttempt(t, "gemini-3.7-flash")
	window, err := HostedAttestationWindow(attempt, 3000)
	require.NoError(t, err)
	assert.False(t, window.GetDigestMatch(), "a served model other than the requested one never matches")
}

func TestValidateAttestationWindow_RejectsTamperedHostedWindows(t *testing.T) {
	t.Parallel()
	attempt, _ := hostedTestAttempt(t, "gemini-3.6-flash")
	valid, err := HostedAttestationWindow(attempt, 3000)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		mutate func(*evalv1.ModelProvenanceAttestationWindow)
		want   error
	}{
		"no hosted record": {func(w *evalv1.ModelProvenanceAttestationWindow) { w.Hosted = nil }, constants.ErrEvidenceArtifactMalformed},
		"a hosted record on storage": {func(w *evalv1.ModelProvenanceAttestationWindow) {
			w.ManifestVerificationStatus = evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_VERIFIED
		}, constants.ErrEvidenceArtifactMalformed},
		"weight attestations": {func(w *evalv1.ModelProvenanceAttestationWindow) {
			w.WeightAttestations = []*evalv1.ModelWeightAttestation{{}}
		}, constants.ErrEvidenceArtifactMalformed},
		"another origin":             {func(w *evalv1.ModelProvenanceAttestationWindow) { w.Hosted.ProviderOrigin = "https://proxy.example" }, constants.ErrEvidenceArtifactMalformed},
		"a rewritten version":        {func(w *evalv1.ModelProvenanceAttestationWindow) { w.Hosted.ProviderModelVersion = "3.6-flash-09-2026" }, constants.ErrModelProvenanceDigestMismatch},
		"a forged match":             {func(w *evalv1.ModelProvenanceAttestationWindow) { w.Hosted.ServedModel = "gemini-3.7-flash" }, constants.ErrModelProvenanceDigestMismatch},
		"a requested model mismatch": {func(w *evalv1.ModelProvenanceAttestationWindow) { w.ServedModelTag = "gemini-2.5-flash" }, constants.ErrEvidenceArtifactMalformed},
		"no inference transaction":   {func(w *evalv1.ModelProvenanceAttestationWindow) { w.Hosted.InferenceTransactionId = "" }, constants.ErrEvidenceArtifactMalformed},
		"an unknown provider class":  {func(w *evalv1.ModelProvenanceAttestationWindow) { w.Hosted.ProviderClass = "acme" }, constants.ErrEvidenceArtifactMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			window := proto.Clone(valid).(*evalv1.ModelProvenanceAttestationWindow)
			tc.mutate(window)
			// The attestation digest is recomputed, as a forger would.
			digest, err := ComputeAttestationDigest(window)
			require.NoError(t, err)
			window.AttestationDigest = digest
			require.ErrorIs(t, ValidateAttestationWindow(window), tc.want)
		})
	}
}
