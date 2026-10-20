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

// NewOllamaModelStore writes a fully verifiable model store for every tag in
// servedModelTags under testutil.TempDir and returns its root, laid out as
// Ollama stores models: manifests/<host>/<namespace>/<model>/<tag> and
// content-addressed blobs/sha256-<hex>. Every blob holds exactly the content its
// digest names, so the attestor reads and hashes the store without error, and
// each tag's manifest digest is ScriptedModelDigest(tag).
func NewOllamaModelStore(t *testing.T, servedModelTags []string) string {
	t.Helper()
	require.NotEmpty(t, servedModelTags, "model store: no served model tags")
	root := filepath.Join(testutil.TempDir(t), "models")
	blobsDir := filepath.Join(root, "blobs")
	require.NoError(t, os.MkdirAll(blobsDir, constants.PermDirPrivate))
	for _, tag := range servedModelTags {
		name := ollama.ParseName(tag)
		require.True(t, name.IsFullyQualified(), "model store: served model tag %q", tag)
		model := newScriptedModel(t, tag)
		for _, blob := range model.blobs {
			require.NoError(t, os.WriteFile(filepath.Join(blobsDir, "sha256-"+sha256Hex(blob)), blob, constants.PermFilePrivate))
		}
		manifestPath := filepath.Join(root, "manifests", name.ManifestFilepath())
		require.NoError(t, os.MkdirAll(filepath.Dir(manifestPath), constants.PermDirPrivate))
		require.NoError(t, os.WriteFile(manifestPath, model.manifest, constants.PermFilePrivate))
	}
	return root
}

// ScriptedModelDigest is the manifest digest NewOllamaModelStore writes for tag.
// A campaign variant that declares it is verified as a digest match by the
// Provenance Operator.
func ScriptedModelDigest(t *testing.T, tag string) string {
	t.Helper()
	return sha256Hex(newScriptedModel(t, tag).manifest)
}

// scriptedModel is one tag's manifest and the blobs it names. Its content
// depends only on the tag, so its digest is known before the store is written.
type scriptedModel struct {
	manifest []byte
	blobs    [][]byte
}

func newScriptedModel(t *testing.T, tag string) scriptedModel {
	t.Helper()
	config := []byte(`{"model_format":"gguf","model_family":"qwen3"}`)
	weights := []byte("scripted weights for " + tag)
	manifest := ollamaModelManifest{SchemaVersion: 2, MediaType: "application/vnd.docker.distribution.manifest.v2+json"}
	manifest.Config = ollamaModelDescriptor{Digest: "sha256:" + sha256Hex(config), MediaType: "application/vnd.docker.container.image.v1+json", Size: int64(len(config))}
	manifest.Layers = []ollamaModelDescriptor{{Digest: "sha256:" + sha256Hex(weights), MediaType: "application/vnd.ollama.image.model", Size: int64(len(weights))}}
	manifestBytes, err := json.Marshal(manifest)
	require.NoError(t, err)
	return scriptedModel{manifest: manifestBytes, blobs: [][]byte{config, weights}}
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

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
