// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// GatewayRuntimeID names the Gateway (and its embedded Operator) as an executing runtime.
const GatewayRuntimeID = "gateway"

// actuatorAlgorithm is the only algorithm an actuator export may declare.
const actuatorAlgorithm = "ed25519"

// LoadAnchor reads one executing runtime's actuator public-key export through
// its own RuntimeFileService and pins it. The export must name key_id ==
// hex(public_key), algorithm ed25519, and a 32-byte key.
func LoadAnchor(ctx context.Context, files fs.RuntimeFileService, runtimeID string) (*evalv1.ConformanceAnchor, error) {
	if runtimeID == "" {
		return nil, fmt.Errorf("oracle: load anchor: empty runtime id: %w", constants.ErrConformanceAnchorInvalid)
	}
	relPath := filepath.Join(constants.PkiDirname, constants.ActuatorPubJSONFilename)
	raw, err := files.ReadFile(ctx, relPath)
	if err != nil {
		if errors.Is(err, constants.ErrNotFound) {
			return nil, fmt.Errorf("oracle: load anchor for %s: %w: %w", runtimeID, constants.ErrConformanceAnchorMissing, err)
		}
		return nil, fmt.Errorf("oracle: read actuator export for %s: %w", runtimeID, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var export models.ActuatorPublicKeyExport
	if err := decoder.Decode(&export); err != nil {
		return nil, fmt.Errorf("oracle: decode actuator export for %s: %w: %w", runtimeID, constants.ErrConformanceAnchorInvalid, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("oracle: actuator export for %s has trailing data: %w", runtimeID, constants.ErrConformanceAnchorInvalid)
	}
	if _, err := validateAnchorKey(export.Algorithm, export.KeyID, export.PublicKey); err != nil {
		return nil, fmt.Errorf("oracle: actuator export for %s: %w", runtimeID, err)
	}

	digest := sha256.Sum256(raw)
	return &evalv1.ConformanceAnchor{
		RuntimeId:    runtimeID,
		KeyId:        export.KeyID,
		PublicKeyHex: export.PublicKey,
		ExportSha256: hex.EncodeToString(digest[:]),
	}, nil
}

// BoundAnchor pins an outbound Operator's actuator key as bound at enrollment
// (OperatorDocument.actuator_public_key, T00/T01). Used for outbound executors
// whose runtime root is not the Gateway's (campaign Data Operator, T25/T28/T30,
// and the conformance outbound Operator, which goes through CheckedBoundAnchor).
func BoundAnchor(doc *operatorv1.OperatorDocument) (*evalv1.ConformanceAnchor, error) {
	if doc == nil || doc.GetId() == "" {
		return nil, fmt.Errorf("oracle: bind anchor: empty operator id: %w", constants.ErrConformanceAnchorInvalid)
	}
	if _, err := validateAnchorKey(actuatorAlgorithm, doc.GetActuatorKeyId(), doc.GetActuatorPublicKey()); err != nil {
		return nil, fmt.Errorf("oracle: bind anchor for %s: %w", doc.GetId(), err)
	}
	return &evalv1.ConformanceAnchor{
		RuntimeId:    doc.GetId(),
		KeyId:        doc.GetActuatorKeyId(),
		PublicKeyHex: doc.GetActuatorPublicKey(),
	}, nil
}

// CheckedBoundAnchor pins an outbound Operator's enrollment-bound key after
// cross-checking it against that Operator's own PKI export, read through its
// RuntimeFileService. The export must name the same key id and public key as
// bound, so an Operator that rewrites its export cannot choose the key its
// receipts verify against. A differing key fails with ErrConformanceAnchorInvalid
// and a missing export with ErrConformanceAnchorMissing. The result is the bound
// key with the export's SHA-256 as provenance.
func CheckedBoundAnchor(ctx context.Context, files fs.RuntimeFileService, runtimeID string, bound *evalv1.ConformanceAnchor) (*evalv1.ConformanceAnchor, error) {
	if bound == nil || bound.GetRuntimeId() != runtimeID {
		return nil, fmt.Errorf("oracle: checked bound anchor for %s: bound anchor is not for this runtime: %w", runtimeID, constants.ErrConformanceAnchorInvalid)
	}
	if _, err := validateAnchorKey(actuatorAlgorithm, bound.GetKeyId(), bound.GetPublicKeyHex()); err != nil {
		return nil, fmt.Errorf("oracle: checked bound anchor for %s: %w", runtimeID, err)
	}
	export, err := LoadAnchor(ctx, files, runtimeID)
	if err != nil {
		return nil, fmt.Errorf("oracle: checked bound anchor for %s: %w", runtimeID, err)
	}
	if export.GetKeyId() != bound.GetKeyId() || export.GetPublicKeyHex() != bound.GetPublicKeyHex() {
		return nil, fmt.Errorf("oracle: checked bound anchor for %s: export key id %s does not match enrollment-bound key id %s: %w", runtimeID, export.GetKeyId(), bound.GetKeyId(), constants.ErrConformanceAnchorInvalid)
	}
	return &evalv1.ConformanceAnchor{
		RuntimeId:    runtimeID,
		KeyId:        bound.GetKeyId(),
		PublicKeyHex: bound.GetPublicKeyHex(),
		ExportSha256: export.GetExportSha256(),
	}, nil
}

// AnchorSet resolves pinned anchors by key id. Built only from a run record.
type AnchorSet struct {
	byKeyID map[string]pinnedAnchor
}

type pinnedAnchor struct {
	anchor *evalv1.ConformanceAnchor
	key    ed25519.PublicKey
}

// NewAnchorSet validates every anchor's key fields and indexes them by key id.
// export_sha256 is provenance only and is not checked here.
func NewAnchorSet(anchors []*evalv1.ConformanceAnchor) (*AnchorSet, error) {
	set := &AnchorSet{byKeyID: make(map[string]pinnedAnchor, len(anchors))}
	runtimeIDs := make(map[string]struct{}, len(anchors))
	for _, anchor := range anchors {
		if anchor == nil || anchor.GetRuntimeId() == "" {
			return nil, fmt.Errorf("oracle: anchor set: empty runtime id: %w", constants.ErrConformanceAnchorInvalid)
		}
		key, err := validateAnchorKey(actuatorAlgorithm, anchor.GetKeyId(), anchor.GetPublicKeyHex())
		if err != nil {
			return nil, fmt.Errorf("oracle: anchor set for %s: %w", anchor.GetRuntimeId(), err)
		}
		if _, exists := set.byKeyID[anchor.GetKeyId()]; exists {
			return nil, fmt.Errorf("oracle: anchor set: duplicate key id %s: %w", anchor.GetKeyId(), constants.ErrConformanceAnchorInvalid)
		}
		if _, exists := runtimeIDs[anchor.GetRuntimeId()]; exists {
			return nil, fmt.Errorf("oracle: anchor set: duplicate runtime id %s: %w", anchor.GetRuntimeId(), constants.ErrConformanceAnchorInvalid)
		}
		runtimeIDs[anchor.GetRuntimeId()] = struct{}{}
		set.byKeyID[anchor.GetKeyId()] = pinnedAnchor{anchor: anchor, key: key}
	}
	return set, nil
}

// Key returns the pinned public key for keyID, and fails unless that anchor
// belongs to runtimeID.
func (s *AnchorSet) Key(keyID, runtimeID string) (ed25519.PublicKey, error) {
	if s == nil {
		return nil, fmt.Errorf("oracle: resolve key %s: %w", keyID, constants.ErrConformanceAnchorUnknown)
	}
	pinned, ok := s.byKeyID[keyID]
	if !ok || pinned.anchor.GetRuntimeId() != runtimeID {
		return nil, fmt.Errorf("oracle: resolve key %s for %s: %w", keyID, runtimeID, constants.ErrConformanceAnchorUnknown)
	}
	return pinned.key, nil
}

// validateAnchorKey checks an actuator key triple and returns the decoded key.
// The key must be canonical lowercase hex, 32 bytes, and key_id must equal it.
func validateAnchorKey(algorithm, keyID, publicKeyHex string) (ed25519.PublicKey, error) {
	if algorithm != actuatorAlgorithm {
		return nil, fmt.Errorf("algorithm %q is not %s: %w", algorithm, actuatorAlgorithm, constants.ErrConformanceAnchorInvalid)
	}
	decoded, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("public key is not hex: %w: %w", constants.ErrConformanceAnchorInvalid, err)
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d: %w", len(decoded), ed25519.PublicKeySize, constants.ErrConformanceAnchorInvalid)
	}
	canonical := hex.EncodeToString(decoded)
	if publicKeyHex != canonical || keyID != canonical {
		return nil, fmt.Errorf("key_id must equal lowercase hex(public_key): %w", constants.ErrConformanceAnchorInvalid)
	}
	return ed25519.PublicKey(decoded), nil
}
