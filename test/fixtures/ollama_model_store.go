// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration || e2e

package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/ollama"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// OllamaModelStore is an on-disk Ollama model storage root holding one served
// model in Ollama's layout: manifests/<host>/<namespace>/<model>/<tag> and
// content-addressed blobs/sha256-<hex>.
type OllamaModelStore struct {
	Root string
	// ModelDigest is the SHA-256 of the model's manifest. A campaign variant
	// that declares this digest is verified as a digest match by the Provenance
	// Operator.
	ModelDigest string
}

// NewOllamaModelStore writes a fully verifiable model store for servedModelTag
// under testutil.TempDir. Every blob holds exactly the content its digest names,
// and the manifest is named by its own digest, so the attestor reads and hashes
// the store without error.
func NewOllamaModelStore(t *testing.T, servedModelTag string) OllamaModelStore {
	t.Helper()
	name := ollama.ParseName(servedModelTag)
	require.True(t, name.IsFullyQualified(), "model store: served model tag %q", servedModelTag)

	root := filepath.Join(testutil.TempDir(t), "models")
	blobsDir := filepath.Join(root, "blobs")
	manifestDir := filepath.Join(root, "manifests", filepath.Dir(name.ManifestFilepath()))
	require.NoError(t, os.MkdirAll(blobsDir, constants.PermDirPrivate))
	require.NoError(t, os.MkdirAll(manifestDir, constants.PermDirPrivate))

	config := writeModelBlob(t, blobsDir, []byte(`{"model_format":"gguf","model_family":"qwen3"}`))
	weights := writeModelBlob(t, blobsDir, []byte("scripted weights for "+servedModelTag))

	manifest := ollamaModelManifest{SchemaVersion: 2, MediaType: "application/vnd.docker.distribution.manifest.v2+json"}
	manifest.Config = ollamaModelDescriptor{Digest: "sha256:" + config.hex, MediaType: "application/vnd.docker.container.image.v1+json", Size: config.size}
	manifest.Layers = []ollamaModelDescriptor{{Digest: "sha256:" + weights.hex, MediaType: "application/vnd.ollama.image.model", Size: weights.size}}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "manifests", name.ManifestFilepath()), manifestBytes, constants.PermFilePrivate))

	return OllamaModelStore{Root: root, ModelDigest: sha256Hex(manifestBytes)}
}

type ollamaModelManifest struct {
	SchemaVersion int                     `json:"schemaVersion"`
	MediaType     string                  `json:"mediaType"`
	Config        ollamaModelDescriptor   `json:"config"`
	Layers        []ollamaModelDescriptor `json:"layers"`
}

type ollamaModelDescriptor struct {
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
}

type modelBlob struct {
	hex  string
	size int64
}

// writeModelBlob stores content under its own SHA-256 name.
func writeModelBlob(t *testing.T, blobsDir string, content []byte) modelBlob {
	t.Helper()
	digest := sha256Hex(content)
	require.NoError(t, os.WriteFile(filepath.Join(blobsDir, "sha256-"+digest), content, constants.PermFilePrivate))
	return modelBlob{hex: digest, size: int64(len(content))}
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
