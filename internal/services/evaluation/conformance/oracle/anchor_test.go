// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func exportPath() string {
	return filepath.Join(constants.PkiDirname, constants.ActuatorPubJSONFilename)
}

func newRuntimeFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	require.NoError(t, files.MkdirAll(context.Background(), constants.PkiDirname, constants.PermDirPrivate))
	return files
}

func writeExport(t *testing.T, files fs.RuntimeFileService, body string) {
	t.Helper()
	require.NoError(t, files.WriteFile(context.Background(), exportPath(), []byte(body), constants.PermFilePrivate))
}

func exportJSON(algorithm, keyID, publicKeyHex string) string {
	return fmt.Sprintf(`{"key_id":%q,"public_key":%q,"algorithm":%q}`, keyID, publicKeyHex, algorithm)
}

func TestLoadAnchor(t *testing.T) {
	signer := oracletest.NewSigner(t)
	pubHex := hex.EncodeToString(signer.Pub)
	shortHex := hex.EncodeToString(signer.Pub[:31])
	otherKey := hex.EncodeToString(oracletest.NewSigner(t).Pub)

	tests := []struct {
		name    string
		body    string
		missing bool
		want    error
	}{
		{name: "valid export pins key and file digest", body: exportJSON("ed25519", signer.KeyID, pubHex)},
		{name: "missing file", missing: true, want: constants.ErrConformanceAnchorMissing},
		{name: "key id is not hex of public key", body: exportJSON("ed25519", otherKey, pubHex), want: constants.ErrConformanceAnchorInvalid},
		{name: "wrong algorithm", body: exportJSON("rsa", signer.KeyID, pubHex), want: constants.ErrConformanceAnchorInvalid},
		{name: "short key", body: exportJSON("ed25519", shortHex, shortHex), want: constants.ErrConformanceAnchorInvalid},
		{name: "uppercase key hex", body: exportJSON("ed25519", signer.KeyID, toUpperHex(signer.Pub)), want: constants.ErrConformanceAnchorInvalid},
		{name: "unknown JSON field", body: `{"key_id":"` + signer.KeyID + `","public_key":"` + pubHex + `","algorithm":"ed25519","extra":1}`, want: constants.ErrConformanceAnchorInvalid},
		{name: "trailing data after the export", body: exportJSON("ed25519", signer.KeyID, pubHex) + `{}`, want: constants.ErrConformanceAnchorInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := newRuntimeFiles(t)
			if !tt.missing {
				writeExport(t, files, tt.body)
			}
			anchor, err := LoadAnchor(context.Background(), files, "op-1")
			if tt.want != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.want)
				return
			}
			require.NoError(t, err)
			digest := sha256.Sum256([]byte(tt.body))
			assert.Equal(t, "op-1", anchor.GetRuntimeId())
			assert.Equal(t, signer.KeyID, anchor.GetKeyId())
			assert.Equal(t, pubHex, anchor.GetPublicKeyHex())
			assert.Equal(t, hex.EncodeToString(digest[:]), anchor.GetExportSha256())
		})
	}
}

func TestLoadAnchorRejectsEmptyRuntimeID(t *testing.T) {
	files := newRuntimeFiles(t)
	_, err := LoadAnchor(context.Background(), files, "")
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorInvalid)
}

func toUpperHex(key []byte) string {
	return fmt.Sprintf("%X", key)
}

func boundDocument(signer oracletest.Signer) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                "op-1",
		ActuatorPublicKey: hex.EncodeToString(signer.Pub),
		ActuatorKeyId:     signer.KeyID,
	}
}

func TestBoundAnchor(t *testing.T) {
	signer := oracletest.NewSigner(t)
	otherSigner := oracletest.NewSigner(t)

	valid := boundDocument(signer)
	emptyKey := boundDocument(signer)
	emptyKey.ActuatorPublicKey = ""
	mismatchedKeyID := boundDocument(signer)
	mismatchedKeyID.ActuatorKeyId = otherSigner.KeyID
	shortKey := boundDocument(signer)
	shortKey.ActuatorPublicKey = hex.EncodeToString(signer.Pub[:31])
	shortKey.ActuatorKeyId = hex.EncodeToString(signer.Pub[:31])
	emptyOperator := boundDocument(signer)
	emptyOperator.Id = ""

	tests := []struct {
		name string
		doc  *operatorv1.OperatorDocument
		want error
	}{
		{name: "valid document", doc: valid},
		{name: "empty key", doc: emptyKey, want: constants.ErrConformanceAnchorInvalid},
		{name: "key id is not hex of public key", doc: mismatchedKeyID, want: constants.ErrConformanceAnchorInvalid},
		{name: "short key", doc: shortKey, want: constants.ErrConformanceAnchorInvalid},
		{name: "empty operator id", doc: emptyOperator, want: constants.ErrConformanceAnchorInvalid},
		{name: "nil document", doc: nil, want: constants.ErrConformanceAnchorInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			anchor, err := BoundAnchor(tt.doc)
			if tt.want != nil {
				assert.ErrorIs(t, err, tt.want)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "op-1", anchor.GetRuntimeId())
			assert.Equal(t, signer.KeyID, anchor.GetKeyId())
			assert.Equal(t, hex.EncodeToString(signer.Pub), anchor.GetPublicKeyHex())
			assert.Empty(t, anchor.GetExportSha256())
		})
	}
}

func TestCheckedBoundAnchor(t *testing.T) {
	signer := oracletest.NewSigner(t)
	other := oracletest.NewSigner(t)
	pubHex := hex.EncodeToString(signer.Pub)
	otherHex := hex.EncodeToString(other.Pub)
	shortBound := signer.Anchor("op-1")
	shortBound.PublicKeyHex = hex.EncodeToString(signer.Pub[:31])

	tests := []struct {
		name      string
		runtimeID string
		bound     *evalv1.ConformanceAnchor
		body      string
		missing   bool
		want      error
		// names are the key ids a mismatch error must name, so an operator can tell which key the export carries.
		names []string
	}{
		{name: "export equals bound pins the bound key with the export digest", runtimeID: "op-1", bound: signer.Anchor("op-1"), body: exportJSON("ed25519", signer.KeyID, pubHex)},
		{name: "export names a different key", runtimeID: "op-1", bound: signer.Anchor("op-1"), body: exportJSON("ed25519", other.KeyID, otherHex), want: constants.ErrConformanceAnchorInvalid, names: []string{other.KeyID, signer.KeyID}},
		{name: "export key id is not hex of its public key", runtimeID: "op-1", bound: signer.Anchor("op-1"), body: exportJSON("ed25519", other.KeyID, pubHex), want: constants.ErrConformanceAnchorInvalid},
		{name: "export missing", runtimeID: "op-1", bound: signer.Anchor("op-1"), missing: true, want: constants.ErrConformanceAnchorMissing},
		{name: "bound anchor names another runtime", runtimeID: "op-1", bound: signer.Anchor("op-2"), body: exportJSON("ed25519", signer.KeyID, pubHex), want: constants.ErrConformanceAnchorInvalid},
		{name: "bound anchor absent", runtimeID: "op-1", bound: nil, body: exportJSON("ed25519", signer.KeyID, pubHex), want: constants.ErrConformanceAnchorInvalid},
		{name: "bound key is not a full ed25519 key", runtimeID: "op-1", bound: shortBound, body: exportJSON("ed25519", signer.KeyID, pubHex), want: constants.ErrConformanceAnchorInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := newRuntimeFiles(t)
			if !tt.missing {
				writeExport(t, files, tt.body)
			}
			anchor, err := CheckedBoundAnchor(context.Background(), files, tt.runtimeID, tt.bound)
			if tt.want != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.want)
				for _, name := range tt.names {
					require.ErrorContains(t, err, name)
				}
				assert.Nil(t, anchor)
				return
			}
			require.NoError(t, err)
			digest := sha256.Sum256([]byte(tt.body))
			assert.Equal(t, "op-1", anchor.GetRuntimeId())
			assert.Equal(t, signer.KeyID, anchor.GetKeyId())
			assert.Equal(t, pubHex, anchor.GetPublicKeyHex())
			assert.Equal(t, hex.EncodeToString(digest[:]), anchor.GetExportSha256())
		})
	}
}

func TestNewAnchorSet(t *testing.T) {
	signer := oracletest.NewSigner(t)
	other := oracletest.NewSigner(t)

	tampered := signer.Anchor("op-1")
	tampered.PublicKeyHex = hex.EncodeToString(other.Pub)

	tests := []struct {
		name    string
		anchors []*evalv1.ConformanceAnchor
		want    error
	}{
		{name: "valid anchors", anchors: []*evalv1.ConformanceAnchor{signer.Anchor("gateway"), other.Anchor("op-1")}},
		{name: "duplicate key id", anchors: []*evalv1.ConformanceAnchor{signer.Anchor("gateway"), signer.Anchor("op-1")}, want: constants.ErrConformanceAnchorInvalid},
		{name: "duplicate runtime id", anchors: []*evalv1.ConformanceAnchor{signer.Anchor("op-1"), other.Anchor("op-1")}, want: constants.ErrConformanceAnchorInvalid},
		{name: "tampered public key", anchors: []*evalv1.ConformanceAnchor{tampered}, want: constants.ErrConformanceAnchorInvalid},
		{name: "empty runtime id", anchors: []*evalv1.ConformanceAnchor{signer.Anchor("")}, want: constants.ErrConformanceAnchorInvalid},
		{name: "nil anchor", anchors: []*evalv1.ConformanceAnchor{nil}, want: constants.ErrConformanceAnchorInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := NewAnchorSet(tt.anchors)
			if tt.want != nil {
				assert.ErrorIs(t, err, tt.want)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, set)
		})
	}
}

func TestAnchorSetKey(t *testing.T) {
	signer := oracletest.NewSigner(t)
	unpinned := oracletest.NewSigner(t)
	set, err := NewAnchorSet([]*evalv1.ConformanceAnchor{signer.Anchor("op-1")})
	require.NoError(t, err)

	key, err := set.Key(signer.KeyID, "op-1")
	require.NoError(t, err)
	assert.Equal(t, signer.Pub, key)

	_, err = set.Key(signer.KeyID, "op-2")
	require.ErrorIs(t, err, constants.ErrConformanceAnchorUnknown)

	_, err = set.Key(unpinned.KeyID, "op-1")
	require.ErrorIs(t, err, constants.ErrConformanceAnchorUnknown)

	var nilSet *AnchorSet
	_, err = nilSet.Key(signer.KeyID, "op-1")
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorUnknown)
}
