// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package model_provenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/ollama"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Attestor independently attests model weight blobs at the storage site.
type Attestor interface {
	Attest(ctx context.Context, servedModelTag, expectedModelDigest string, attestedAt time.Time) (*evalv1.ModelProvenanceAttestationWindow, error)
}

// OllamaStorageAttestor hashes Ollama manifest and content-addressed blobs
// beneath modelStorageRoot and compares the manifest digest to the expected
// campaign model digest.
type OllamaStorageAttestor struct {
	modelStorageRoot string
	operatorID       string
	blobs            *blobVerifier
}

// NewOllamaStorageAttestor constructs a storage-side attestor for one model
// root directory (for example ~/.ollama/models).
func NewOllamaStorageAttestor(modelStorageRoot, operatorID string) (*OllamaStorageAttestor, error) {
	root := strings.TrimSpace(modelStorageRoot)
	if root == "" {
		return nil, fmt.Errorf("model provenance attestor: %w", constants.ErrMissingRequiredField)
	}
	if operatorID == "" {
		operatorID = "ztd-model-provenance-operator"
	}
	return &OllamaStorageAttestor{modelStorageRoot: root, operatorID: operatorID, blobs: newBlobVerifier(filepath.Join(root, "blobs"), time.Now)}, nil
}

// ollamaRunnerManifestMediaType marks a wrapper layer that is itself one
// runner's manifest in a multi-runner tag.
const ollamaRunnerManifestMediaType = "application/vnd.docker.distribution.manifest.v2+json"

type ollamaManifest struct {
	Config struct {
		Digest    string `json:"digest"`
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Digest    string `json:"digest"`
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
	} `json:"layers"`
}

// Attest reads the Ollama manifest for servedModelTag, verifies referenced
// blobs, and returns a provenance window with digest_match set.
func (a *OllamaStorageAttestor) Attest(ctx context.Context, servedModelTag, expectedModelDigest string, attestedAt time.Time) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if servedModelTag == "" || expectedModelDigest == "" {
		return nil, fmt.Errorf("model provenance attestor: %w", constants.ErrMissingRequiredField)
	}
	if !models.IsSHA256Hex(expectedModelDigest) {
		return nil, fmt.Errorf("model provenance attestor: invalid expected digest")
	}

	manifestPath, err := resolveOllamaManifestPath(a.modelStorageRoot, servedModelTag)
	if err != nil {
		return nil, err
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("model provenance attestor: read manifest: %w", err)
	}

	manifestDigest := models.SHA256Hex(manifestBytes)

	var manifest ollamaManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("model provenance attestor: parse manifest: %w", err)
	}
	expected := normalizeOllamaDigest(expectedModelDigest)
	if manifestDigest != expected {
		runnerDigest, runnerBytes, found, err := a.readRunnerManifest(manifest, expected)
		if err != nil {
			return nil, err
		}
		if found {
			manifestDigest, manifest = runnerDigest, ollamaManifest{}
			if err := json.Unmarshal(runnerBytes, &manifest); err != nil {
				return nil, fmt.Errorf("model provenance attestor: parse runner manifest: %w", err)
			}
		}
	}
	observedDigest := normalizeOllamaDigest(manifestDigest)

	weightAttestations := make([]*evalv1.ModelWeightAttestation, 0, len(manifest.Layers)+1)
	if manifest.Config.Digest != "" {
		attestation, err := a.attestBlob(ctx, manifest.Config.Digest, manifest.Config.MediaType, manifest.Config.Size)
		if err != nil {
			return nil, err
		}
		weightAttestations = append(weightAttestations, attestation)
	}
	for _, layer := range manifest.Layers {
		attestation, err := a.attestBlob(ctx, layer.Digest, layer.MediaType, layer.Size)
		if err != nil {
			return nil, err
		}
		weightAttestations = append(weightAttestations, attestation)
	}

	digestMatch := observedDigest == expected
	return &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              SchemaVersion,
		ProvenanceOperatorId:       a.operatorID,
		ServedModelTag:             servedModelTag,
		ExpectedModelDigest:        expectedModelDigest,
		ObservedModelDigest:        observedDigest,
		ManifestDigest:             manifestDigest,
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED,
		WeightAttestations:         weightAttestations,
		AttestedAtUnixMs:           attestedAt.UTC().UnixMilli(),
		DigestMatch:                digestMatch,
	}, nil
}

// readRunnerManifest resolves a multi-runner tag. Ollama stores such a tag's
// manifest as a wrapper whose layers include one manifest per runner, and its
// API reports the served runner manifest's digest, not the wrapper's. When the
// wrapper references a runner manifest with exactly the expected digest, that
// content-addressed blob is read, its hash verified, and returned, so the
// window attests only the weights the served digest names. found is false when
// no referenced runner manifest has that digest.
func (a *OllamaStorageAttestor) readRunnerManifest(wrapper ollamaManifest, expectedDigest string) (digest string, body []byte, found bool, err error) {
	for _, layer := range wrapper.Layers {
		if layer.MediaType != ollamaRunnerManifestMediaType || normalizeOllamaDigest(layer.Digest) != expectedDigest {
			continue
		}
		digestHex, err := digestRefToHex(layer.Digest)
		if err != nil {
			return "", nil, false, err
		}
		body, err := os.ReadFile(filepath.Join(a.modelStorageRoot, "blobs", "sha256-"+digestHex))
		if err != nil {
			return "", nil, false, fmt.Errorf("model provenance attestor: read runner manifest %s: %w", digestHex, err)
		}
		if models.SHA256Hex(body) != digestHex {
			return "", nil, false, fmt.Errorf("model provenance attestor: runner manifest digest mismatch for %s", digestHex)
		}
		return digestHex, body, true, nil
	}
	return "", nil, false, nil
}

func (a *OllamaStorageAttestor) attestBlob(ctx context.Context, digestRef, mediaType string, declaredSize int64) (*evalv1.ModelWeightAttestation, error) {
	digestHex, err := digestRefToHex(digestRef)
	if err != nil {
		return nil, err
	}
	verification, err := a.blobs.verify(ctx, digestHex)
	if err != nil {
		return nil, err
	}
	if declaredSize > 0 && verification.sizeBytes != declaredSize {
		return nil, fmt.Errorf("model provenance attestor: blob size mismatch for %s", digestHex)
	}
	return &evalv1.ModelWeightAttestation{
		BlobDigest:                digestHex,
		SizeBytes:                 uint64(verification.sizeBytes),
		MediaType:                 mediaType,
		VerificationMode:          verification.mode,
		FullHashCompletedAtUnixMs: verification.hashCompletedAt.UTC().UnixMilli(),
	}, nil
}

func resolveOllamaManifestPath(storageRoot, servedModelTag string) (string, error) {
	name := ollama.ParseName(servedModelTag)
	if !name.IsFullyQualified() {
		return "", fmt.Errorf("model provenance attestor: invalid served model tag %q", servedModelTag)
	}
	manifestPath := filepath.Join(storageRoot, "manifests", name.ManifestFilepath())
	if _, err := os.Stat(manifestPath); err != nil {
		return "", fmt.Errorf("model provenance attestor: manifest not found for %q: %w", servedModelTag, err)
	}
	return manifestPath, nil
}

func digestRefToHex(digestRef string) (string, error) {
	digestRef = strings.TrimSpace(digestRef)
	digestRef = strings.TrimPrefix(digestRef, "sha256:")
	if !models.IsSHA256Hex(digestRef) {
		return "", fmt.Errorf("model provenance attestor: invalid digest ref %q", digestRef)
	}
	return digestRef, nil
}

func normalizeOllamaDigest(digest string) string {
	return strings.TrimPrefix(strings.TrimSpace(digest), "sha256:")
}
