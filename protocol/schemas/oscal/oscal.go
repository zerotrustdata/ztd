// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version  2.0.

// Package oscal embeds the unmodified official NIST OSCAL 1.1.2
// assessment-results JSON Schema into the Go protocol library so that
// downstream validation is fully offline and available in air-gapped builds.
//
// The schema bytes are authenticated by a pinned SHA-256 digest. Any caller
// that retrieves the embedded bytes through [SchemaBytes] receives a copy
// whose digest is verified against the pinned value. A digest mismatch fails
// closed: [SchemaBytes] returns a wrapped [constants.ErrOSCALSchemaDigestMismatch]
// and no bytes, so no production path can validate against a tampered or
// replaced schema.
//
// The schema is Draft-07 JSON Schema and is consumed by the repository-owned
// pure-Go validator in internal/services/compliance/oscal/validator. No
// third-party JSON Schema engine, subprocess, or network service is used.
//
// Provenance metadata (source URL, release tag, license, retrieval method,
// byte length, SHA-256) is embedded alongside the schema in
// oscal/v1.1.2/provenance.json and checked against the schema bytes by the
// package tests.
package oscal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	_ "embed"

	"github.com/zerotrustdata/ztd/internal/constants"
)

//go:embed v1.1.2/oscal_assessment-results_schema.json
var embeddedSchemaJSON []byte

// PinnedSHA256 is the authenticated SHA-256 digest of the official NIST OSCAL
// 1.1.2 assessment-results JSON Schema. It is the single source of truth for
// schema integrity. Any schema bytes whose digest differs from this value are
// rejected.
const PinnedSHA256 = "d033da70154cf6625ae46a746199e88e58f2928b1387dfac051d381b92f41b0d"

// PinnedByteLength is the authenticated byte length of the embedded schema.
const PinnedByteLength = 133015

// SchemaVersion is the OSCAL schema version (1.1.2).
const SchemaVersion = "1.1.2"

// SchemaID is the $id of the embedded schema.
const SchemaID = "http://csrc.nist.gov/ns/oscal/1.1.2/oscal-ar-schema.json"

// SchemaJSONDraft is the JSON Schema draft used by the embedded schema.
const SchemaJSONDraft = "draft-07"

var (
	initOnce      sync.Once
	initErr       error
	verifiedBytes []byte
)

// init verifies the embedded schema digest exactly once. Subsequent calls to
// SchemaBytes return the verified copy without re-hashing.
func verifyOnce() {
	initOnce.Do(func() {
		if len(embeddedSchemaJSON) != PinnedByteLength {
			initErr = fmt.Errorf("%w: expected %d bytes, got %d", constants.ErrOSCALSchemaDigestMismatch, PinnedByteLength, len(embeddedSchemaJSON))
			return
		}
		sum := sha256.Sum256(embeddedSchemaJSON)
		got := hex.EncodeToString(sum[:])
		if got != PinnedSHA256 {
			initErr = fmt.Errorf("%w: expected %s, got %s", constants.ErrOSCALSchemaDigestMismatch, PinnedSHA256, got)
			return
		}
		verifiedBytes = bytes.Clone(embeddedSchemaJSON)
	})
}

// SchemaBytes returns a copy of the authenticated NIST OSCAL 1.1.2
// assessment-results JSON Schema bytes. The digest is verified against
// PinnedSHA256 on the first call; a mismatch returns a wrapped
// constants.ErrOSCALSchemaDigestMismatch and no bytes.
func SchemaBytes() ([]byte, error) {
	verifyOnce()
	if initErr != nil {
		return nil, initErr
	}
	return bytes.Clone(verifiedBytes), nil
}
