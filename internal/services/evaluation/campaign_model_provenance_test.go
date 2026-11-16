// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func testModelProvenanceWindow(t *testing.T) *evalv1.ModelProvenanceAttestationWindow {
	t.Helper()
	digest := strings.Repeat("a", 64)
	window := &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              model_provenance.SchemaVersion,
		ProviderAttemptId:          "attempt-1",
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
	require.NoError(t, err)
	window.AttestationDigest = attestationDigest
	return window
}

func TestVerifyModelProvenanceWindow_InterimPolicy_AllowsMissingWindow(t *testing.T) {
	t.Parallel()
	err := VerifyModelProvenanceWindow(nil, strings.Repeat("a", 64), ModelProvenancePolicyInterim)
	assert.NoError(t, err)
}

func TestVerifyModelProvenanceWindow_StrictPolicy_RequiresWindow(t *testing.T) {
	t.Parallel()
	err := VerifyModelProvenanceWindow(nil, strings.Repeat("a", 64), ModelProvenancePolicyStrict)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
}

func TestVerifyModelProvenanceWindow_StrictPolicy_RequiresDigestMatch(t *testing.T) {
	t.Parallel()
	window := testModelProvenanceWindow(t)
	window.DigestMatch = false
	attestationDigest, err := model_provenance.ComputeAttestationDigest(window)
	require.NoError(t, err)
	window.AttestationDigest = attestationDigest

	err = VerifyModelProvenanceWindow(window, window.GetExpectedModelDigest(), ModelProvenancePolicyStrict)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrModelProvenanceDigestMismatch)
}

func TestVerifyModelProvenanceWindow_ExpectedDigestMismatch(t *testing.T) {
	t.Parallel()
	window := testModelProvenanceWindow(t)
	err := VerifyModelProvenanceWindow(window, strings.Repeat("c", 64), ModelProvenancePolicyInterim)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrModelProvenanceDigestMismatch)
}

func TestStore_ModelProvenanceWindowRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	window := testModelProvenanceWindow(t)
	require.NoError(t, store.SaveModelProvenanceWindow(ctx, "run-1", window))

	loaded, err := store.LoadModelProvenanceWindow(ctx, "run-1", "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, window.GetAttestationDigest(), loaded.GetAttestationDigest())
	_, err = store.LoadModelProvenanceWindow(ctx, "run-1", "attempt-absent")
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestStore_SaveModelProvenanceWindowRejectsInvalidWindow(t *testing.T) {
	t.Parallel()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	window := testModelProvenanceWindow(t)
	window.AttestationDigest = "invalid"

	err := store.SaveModelProvenanceWindow(t.Context(), "run-1", window)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationReportPersistFailed)
}

func TestStore_LoadModelProvenanceWindowRejectsMissingProviderAttemptID(t *testing.T) {
	t.Parallel()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	_, err := store.LoadModelProvenanceWindow(t.Context(), "run-1", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

func TestCampaignModelProvenanceReader_VerifyAssignmentModelProvenance_StrictMissingWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	reader, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
		RunId: "run-1",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inf-1",
			ProviderAttemptId: "attempt-missing",
			ModelVariant:      &evalv1.ModelVariant{ModelDigest: strings.Repeat("a", 64)},
		}},
	}
	failures, unavailable := reader.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	require.NotEmpty(t, failures)
	require.NotEmpty(t, unavailable)
}

// A governance-rejected inference has no provider attempt, so strict
// provenance expects no attestation window for it.
func TestCampaignModelProvenanceReader_VerifyAssignmentModelProvenance_StrictSkipsGovernanceRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	reader, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
		RunId: "run-1",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inf-denied",
			ModelVariant:      &evalv1.ModelVariant{ModelDigest: strings.Repeat("a", 64)},
		}},
	}
	failures, unavailable := reader.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
}

// A hosted attempt that returned no result carries no signed attestation, so
// strict provenance expects no window for it; a hosted attempt with a result
// and every local attempt still need one.
func TestModelProvenanceRequired(t *testing.T) {
	t.Parallel()
	hosted := &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderGemini}
	local := &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderOllama}
	digest := strings.Repeat("c", 64)
	tests := []struct {
		name   string
		record *evalv1.ModelInferenceRecord
		want   bool
	}{
		{"governance rejected", &evalv1.ModelInferenceRecord{ModelVariant: local, ResultDigest: digest}, false},
		{"local completed", &evalv1.ModelInferenceRecord{ProviderAttemptId: "a", ModelVariant: local, ResultDigest: digest}, true},
		{"local failed", &evalv1.ModelInferenceRecord{ProviderAttemptId: "a", ModelVariant: local}, true},
		{"hosted completed", &evalv1.ModelInferenceRecord{ProviderAttemptId: "a", ModelVariant: hosted, ResultDigest: digest}, true},
		{"hosted failed", &evalv1.ModelInferenceRecord{ProviderAttemptId: "a", ModelVariant: hosted}, false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ModelProvenanceRequired(tt.record), tt.name)
	}
}

func TestCampaignModelProvenanceReader_VerifyAssignmentModelProvenance_StrictHostedFailedAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	reader, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	hosted := &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderGemini, ModelDigest: strings.Repeat("a", 64)}
	failed := &evalv1.EvaluationAssignmentResult{
		RunId: "run-1",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inf-throttled",
			ProviderAttemptId: "attempt-throttled",
			ModelVariant:      hosted,
		}},
	}
	failures, unavailable := reader.VerifyAssignmentModelProvenance(ctx, failed, ModelProvenancePolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)

	completed := &evalv1.EvaluationAssignmentResult{
		RunId: "run-1",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inf-completed",
			ProviderAttemptId: "attempt-completed",
			ModelVariant:      hosted,
			ResultDigest:      strings.Repeat("c", 64),
		}},
	}
	failures, _ = reader.VerifyAssignmentModelProvenance(ctx, completed, ModelProvenancePolicyStrict)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0], "inf-completed")
}

func TestCampaignModelProvenanceReader_VerifyAssignmentModelProvenance_StrictDigestMatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	window := testModelProvenanceWindow(t)
	require.NoError(t, store.SaveModelProvenanceWindow(ctx, "run-1", window))
	reader, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
		RunId: "run-1",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inf-1",
			ProviderAttemptId: "attempt-1",
			ModelVariant:      &evalv1.ModelVariant{ModelDigest: window.GetExpectedModelDigest()},
		}},
	}
	failures, unavailable := reader.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
}
