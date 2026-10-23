// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package model_provenance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func testAttestationWindow(t *testing.T, providerAttemptID string) *evalv1.ModelProvenanceAttestationWindow {
	t.Helper()
	digest := strings.Repeat("a", 64)
	window := &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              SchemaVersion,
		ProviderAttemptId:          providerAttemptID,
		ProvenanceOperatorId:       "test-provenance-operator",
		ServedModelTag:             "probe-model:7b",
		ExpectedModelDigest:        digest,
		ObservedModelDigest:        digest,
		ManifestDigest:             strings.Repeat("b", 64),
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED,
		WeightAttestations: []*evalv1.ModelWeightAttestation{{
			BlobDigest:                strings.Repeat("c", 64),
			SizeBytes:                 7,
			MediaType:                 "application/vnd.ollama.image.model",
			VerificationMode:          evalv1.ModelWeightVerificationMode_MODEL_WEIGHT_VERIFICATION_MODE_FILE_IDENTITY_UNCHANGED,
			FullHashCompletedAtUnixMs: 1_699_999_999_000,
		}},
		AttestedAtUnixMs: 1_700_000_000_000,
		DigestMatch:      true,
	}
	attestationDigest, err := ComputeAttestationDigest(window)
	require.NoError(t, err)
	window.AttestationDigest = attestationDigest
	return window
}
