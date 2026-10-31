// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package model_provenance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jitllm"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// JitLLMStorageAttestor hashes the .jlm container a jitllm models folder
// holds for a model, the file an Inference Operator's embedded engine loads,
// and compares that SHA-256 to the expected campaign digest. A models folder
// has no manifest, so the window carries none.
type JitLLMStorageAttestor struct {
	modelsDir  string
	operatorID string
	blobs      *blobVerifier
}

// NewJitLLMStorageAttestor constructs a storage-side attestor for one jitllm
// models folder.
func NewJitLLMStorageAttestor(modelsDir, operatorID string) (*JitLLMStorageAttestor, error) {
	root := strings.TrimSpace(modelsDir)
	if root == "" {
		return nil, fmt.Errorf("model provenance attestor: %w", constants.ErrMissingRequiredField)
	}
	if operatorID == "" {
		operatorID = "ztd-model-provenance-operator"
	}
	return &JitLLMStorageAttestor{modelsDir: root, operatorID: operatorID, blobs: newBlobVerifier(time.Now)}, nil
}

// Attest resolves servedModelTag to one container, hashes that file, and
// returns a provenance window whose observed digest is the file's SHA-256.
func (a *JitLLMStorageAttestor) Attest(ctx context.Context, servedModelTag, expectedModelDigest string, attestedAt time.Time) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if servedModelTag == "" || expectedModelDigest == "" {
		return nil, fmt.Errorf("model provenance attestor: %w", constants.ErrMissingRequiredField)
	}
	if !models.IsSHA256Hex(expectedModelDigest) {
		return nil, fmt.Errorf("model provenance attestor: invalid expected digest: %w", constants.ErrInferenceEvidenceHashInvalid)
	}
	container, err := jitllm.Resolve(a.modelsDir, servedModelTag)
	if err != nil {
		return nil, fmt.Errorf("model provenance attestor: %w", err)
	}
	verification, err := a.blobs.verify(ctx, container.Path, "")
	if err != nil {
		return nil, err
	}
	return &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              SchemaVersion,
		ProvenanceOperatorId:       a.operatorID,
		ServedModelTag:             servedModelTag,
		ExpectedModelDigest:        expectedModelDigest,
		ObservedModelDigest:        verification.digestHex,
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNAVAILABLE,
		WeightAttestations: []*evalv1.ModelWeightAttestation{{
			BlobDigest:                verification.digestHex,
			SizeBytes:                 uint64(verification.sizeBytes),
			MediaType:                 constants.ModelWeightMediaTypeJLM,
			VerificationMode:          verification.mode,
			FullHashCompletedAtUnixMs: verification.hashCompletedAt.UTC().UnixMilli(),
		}},
		AttestedAtUnixMs: attestedAt.UTC().UnixMilli(),
		DigestMatch:      verification.digestHex == expectedModelDigest,
	}, nil
}
