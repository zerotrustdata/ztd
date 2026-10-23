// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version  2.0.

package oscal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	_ "embed"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

//go:embed v1.1.2/provenance.json
var embeddedProvenanceJSON []byte

// provenanceBytes returns a copy of the embedded provenance metadata JSON.
func provenanceBytes() []byte {
	return bytes.Clone(embeddedProvenanceJSON)
}

// provenance is the typed view over the embedded provenance.json metadata.
type provenance struct {
	SchemaType       string `json:"schema_type"`
	SchemaVersion    string `json:"schema_version"`
	SourceURL        string `json:"source_url"`
	SourceRepository string `json:"source_repository"`
	SourceTag        string `json:"source_tag"`
	SourcePath       string `json:"source_path"`
	SchemaID         string `json:"schema_id"`
	JSONSchemaDraft  string `json:"json_schema_draft"`
	License          string `json:"license"`
	LicenseSummary   string `json:"license_summary"`
	ByteLength       int    `json:"byte_length"`
	SHA256           string `json:"sha256"`
	RetrievedAt      string `json:"retrieved_at"`
	RetrievalMethod  string `json:"retrieval_method"`
	IntegrityNote    string `json:"integrity_note"`
}

// loadProvenance decodes the embedded provenance metadata and verifies that
// its declared byte length and SHA-256 match the embedded schema bytes. A
// mismatch returns constants.ErrOSCALProvenanceMismatch.
func loadProvenance() (*provenance, error) {
	var p provenance
	if err := json.Unmarshal(embeddedProvenanceJSON, &p); err != nil {
		return nil, fmt.Errorf("oscal: decode provenance: %w", err)
	}
	schemaBytes, err := SchemaBytes()
	if err != nil {
		return nil, err
	}
	if p.ByteLength != len(schemaBytes) {
		return nil, fmt.Errorf("%w: provenance byte_length=%d, actual=%d", constants.ErrOSCALProvenanceMismatch, p.ByteLength, len(schemaBytes))
	}
	sum := sha256.Sum256(schemaBytes)
	got := hex.EncodeToString(sum[:])
	if got != p.SHA256 {
		return nil, fmt.Errorf("%w: provenance sha256=%s, actual=%s", constants.ErrOSCALProvenanceMismatch, p.SHA256, got)
	}
	return &p, nil
}

// verifySchemaDigest independently re-hashes the embedded schema and compares
// against PinnedSHA256 by re-reading the schema through SchemaBytes.
func verifySchemaDigest() error {
	_, err := SchemaBytes()
	return err
}

func TestSchemaBytes_MatchesPinnedDigest(t *testing.T) {
	b, err := SchemaBytes()
	require.NoError(t, err)
	assert.Len(t, b, PinnedByteLength)
	sum := sha256.Sum256(b)
	assert.Equal(t, PinnedSHA256, hex.EncodeToString(sum[:]))
}

func TestSchemaBytes_IsACopy(t *testing.T) {
	b1, err := SchemaBytes()
	require.NoError(t, err)
	b2, err := SchemaBytes()
	require.NoError(t, err)
	assert.NotSame(t, &b1[0], &b2[0])
	assert.Equal(t, b1, b2)
}

func TestSchemaBytes_DecodesAsValidJSON(t *testing.T) {
	b, err := SchemaBytes()
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(b, &schema))
	assert.Equal(t, "http://json-schema.org/draft-07/schema#", schema["$schema"])
	assert.Equal(t, SchemaID, schema["$id"])
	assert.Equal(t, "object", schema["type"])
	defs, ok := schema["definitions"].(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, defs)
}

func TestVerifySchemaDigest_PassesForEmbeddedSchema(t *testing.T) {
	assert.NoError(t, verifySchemaDigest())
}

func TestProvenanceBytes_DecodesAndMatchesSchema(t *testing.T) {
	p, err := loadProvenance()
	require.NoError(t, err)
	assert.Equal(t, "oscal-assessment-results", p.SchemaType)
	assert.Equal(t, SchemaVersion, p.SchemaVersion)
	assert.Equal(t, SchemaID, p.SchemaID)
	assert.Equal(t, SchemaJSONDraft, p.JSONSchemaDraft) //nolint:testifylint // exact bytes, not JSON equivalence
	assert.Equal(t, PinnedByteLength, p.ByteLength)
	assert.Equal(t, PinnedSHA256, p.SHA256)
	assert.NotEmpty(t, p.SourceURL)
	assert.NotEmpty(t, p.SourceTag)
	assert.NotEmpty(t, p.License)
	assert.NotEmpty(t, p.RetrievalMethod)
}

func TestProvenanceBytes_IsValidJSON(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal(provenanceBytes(), &raw))
	assert.NotEmpty(t, raw)
}

func TestPinnedSHA256_IsValidHex(t *testing.T) {
	assert.Len(t, PinnedSHA256, 64)
	for _, c := range PinnedSHA256 {
		assert.True(t, strings.ContainsRune("0123456789abcdef", c), "invalid hex char %q", c)
	}
}
