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
	"github.com/zerotrustdata/ztd/internal/llamacpp"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// LlamaCppStorageAttestor hashes the GGUF files a llama-server router loads
// for a model from its --models-dir and compares the composed model digest
// to the expected campaign model digest. A models-dir has no manifest, so
// the window carries no manifest digest.
type LlamaCppStorageAttestor struct {
	modelsDir  string
	operatorID string
	blobs      *blobVerifier
}

// NewLlamaCppStorageAttestor constructs a storage-side attestor for one
// llama-server --models-dir.
func NewLlamaCppStorageAttestor(modelsDir, operatorID string) (*LlamaCppStorageAttestor, error) {
	root := strings.TrimSpace(modelsDir)
	if root == "" {
		return nil, fmt.Errorf("model provenance attestor: %w", constants.ErrMissingRequiredField)
	}
	if operatorID == "" {
		operatorID = "ztd-model-provenance-operator"
	}
	return &LlamaCppStorageAttestor{modelsDir: root, operatorID: operatorID, blobs: newBlobVerifier(time.Now)}, nil
}

// Attest resolves servedModelTag with llama.cpp's models-dir rules, hashes
// every file the router loads for it, and returns a provenance window whose
// observed digest is composed from the model's shards. Projector and draft
// sidecars are attested as weights but are not part of the model digest.
func (a *LlamaCppStorageAttestor) Attest(ctx context.Context, servedModelTag, expectedModelDigest string, attestedAt time.Time) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if servedModelTag == "" || expectedModelDigest == "" {
		return nil, fmt.Errorf("model provenance attestor: %w", constants.ErrMissingRequiredField)
	}
	if !models.IsSHA256Hex(expectedModelDigest) {
		return nil, fmt.Errorf("model provenance attestor: invalid expected digest")
	}
	model, err := llamacpp.ResolveModel(a.modelsDir, servedModelTag)
	if err != nil {
		return nil, fmt.Errorf("model provenance attestor: %w", err)
	}

	files := model.Files()
	weightAttestations := make([]*evalv1.ModelWeightAttestation, 0, len(files))
	shardDigests := make([]string, 0, len(model.Shards))
	for index, path := range files {
		verification, err := a.blobs.verify(ctx, path, "")
		if err != nil {
			return nil, err
		}
		if index < len(model.Shards) {
			shardDigests = append(shardDigests, verification.digestHex)
		}
		weightAttestations = append(weightAttestations, &evalv1.ModelWeightAttestation{
			BlobDigest:                verification.digestHex,
			SizeBytes:                 uint64(verification.sizeBytes),
			MediaType:                 constants.ModelWeightMediaTypeGGUF,
			VerificationMode:          verification.mode,
			FullHashCompletedAtUnixMs: verification.hashCompletedAt.UTC().UnixMilli(),
		})
	}
	observedDigest, err := llamacpp.ComposeModelDigest(shardDigests)
	if err != nil {
		return nil, fmt.Errorf("model provenance attestor: %w", err)
	}

	return &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              SchemaVersion,
		ProvenanceOperatorId:       a.operatorID,
		ServedModelTag:             servedModelTag,
		ExpectedModelDigest:        expectedModelDigest,
		ObservedModelDigest:        observedDigest,
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNAVAILABLE,
		WeightAttestations:         weightAttestations,
		AttestedAtUnixMs:           attestedAt.UTC().UnixMilli(),
		DigestMatch:                observedDigest == expectedModelDigest,
	}, nil
}
