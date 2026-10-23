// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evidence

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type assessedSignerStub struct {
	keys map[string]ed25519.PublicKey
	err  error
}

type failingArtifactReader struct {
	err error
}

func (r *failingArtifactReader) ReadFile(context.Context, string) ([]byte, error) {
	return nil, r.err
}

func (r *failingArtifactReader) ReadDir(context.Context, string) ([]os.DirEntry, error) {
	return nil, r.err
}

func (s *assessedSignerStub) GetTrustedSignerPublicKey(_ context.Context, keyID string) (ed25519.PublicKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	key, exists := s.keys[keyID]
	if !exists {
		return nil, constants.ErrTrustedSignerKeyNotFound
	}
	return key, nil
}

type standaloneReceiptFixture struct {
	reader             *memoryArtifactReader
	trust              *assessedSignerStub
	receipt            *operatorv1.ActionReceipt
	receiptBody        []byte
	receiptBinding     ReceiptImportBinding
	persistenceBinding PersistenceImportBinding
}

func newStandaloneReceiptFixture(t *testing.T) *standaloneReceiptFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signerKeyID := "assessed-signer"
	receipt := &operatorv1.ActionReceipt{TransactionId: "tx-standalone", TransactionHash: "tx-hash", SignerKeyId: signerKeyID, ExecutedAtUnixMs: 1_700_000_001_000}
	receiptPayload, err := governance.CanonicalizeActionReceipt(receipt)
	require.NoError(t, err)
	receipt.Signature = hex.EncodeToString(ed25519.Sign(privateKey, receiptPayload))
	attestation := &operatorv1.ReceiptPersistenceAttestation{TransactionId: receipt.TransactionId, ReceiptSignatureDigest: governance.SignatureDigest([]string{receipt.Signature}), PersistedAtUnixMs: 1_700_000_002_000, AuditRecordId: receipt.TransactionId, SignerKeyId: signerKeyID}
	attestationPayload, err := governance.CanonicalizeReceiptPersistenceAttestation(attestation)
	require.NoError(t, err)
	attestation.Signature = hex.EncodeToString(ed25519.Sign(privateKey, attestationPayload))
	receipt.FinalPersistenceAttestation = attestation
	receiptBody, err := compliancev1.MarshalCanonical(receipt)
	require.NoError(t, err)
	persistenceBody, err := compliancev1.MarshalCanonical(attestation)
	require.NoError(t, err)
	receiptReference := ContentReferenceForBody(constants.ActionReceiptReferencePrefix, receiptBody)
	persistenceReference := ContentReferenceForBody(constants.ReceiptPersistenceReferencePrefix, persistenceBody)
	receiptPath := filepath.Join("receipts", digestHex(receiptBody)+constants.FileExtJSON)
	persistencePath := filepath.Join("persistence", digestHex(persistenceBody)+constants.FileExtJSON)
	return &standaloneReceiptFixture{
		reader:             &memoryArtifactReader{files: map[string][]byte{receiptPath: receiptBody, persistencePath: persistenceBody}},
		trust:              &assessedSignerStub{keys: map[string]ed25519.PublicKey{signerKeyID: publicKey}},
		receipt:            receipt,
		receiptBody:        receiptBody,
		receiptBinding:     ReceiptImportBinding{Reference: receiptReference, Path: receiptPath, ScopeID: "scope-1", RunID: "run-1", AttemptID: "attempt-1", ScenarioID: "scenario-1"},
		persistenceBinding: PersistenceImportBinding{Reference: persistenceReference, Path: persistencePath, ReceiptReference: receiptReference, ReceiptPath: receiptPath, ScopeID: "scope-1", RunID: "run-1", AttemptID: "attempt-1", ScenarioID: "scenario-1"},
	}
}

func TestReceiptImporter_Import_VerifiesSignatureWithAssessedTrust(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	nodes, err := NewReceiptImporterAt(fixture.reader, fixture.trust, fixture.receiptBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	node := nodes[0]
	assert.Equal(t, VerificationStatusVerified, node.VerificationStatus)
	assert.Equal(t, fixture.receiptBinding.Reference, node.ArtifactID)
	assert.Equal(t, fixture.receipt.TransactionId, node.TransactionID)
	assert.Equal(t, fixture.receiptBinding.ScopeID, node.ScopeID)
	assert.Equal(t, fixture.receiptBinding.RunID, node.RunID)
	assert.Equal(t, []string{fixture.persistenceBinding.Reference}, node.References)
}

func TestReceiptImporter_Import_PreservesUnverifiedStatusWithoutAssessedKey(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	fixture.trust.keys = map[string]ed25519.PublicKey{}
	nodes, err := NewReceiptImporterAt(fixture.reader, fixture.trust, fixture.receiptBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, VerificationStatusUnverified, nodes[0].VerificationStatus)
	assert.Empty(t, nodes[0].VerifierID)
}

func TestReceiptImporter_Import_MarksInvalidSignatureFailed(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	fixture.trust.keys[fixture.receipt.SignerKeyId] = ed25519.PublicKey(strings.Repeat("x", ed25519.PublicKeySize))
	nodes, err := NewReceiptImporterAt(fixture.reader, fixture.trust, fixture.receiptBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, VerificationStatusFailed, nodes[0].VerificationStatus)
	assert.Equal(t, constants.ReceiptEvidenceVerifierID, nodes[0].VerifierID)
}

func TestReceiptImporter_Import_RejectsInvalidConfiguration(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	tests := []struct {
		name     string
		importer *ReceiptImporter
	}{
		{name: "nil importer", importer: nil},
		{name: "nil reader", importer: NewReceiptImporterAt(nil, fixture.trust, fixture.receiptBinding, time.Now)},
		{name: "nil trust", importer: NewReceiptImporterAt(fixture.reader, nil, fixture.receiptBinding, time.Now)},
		{name: "invalid reference", importer: NewReceiptImporterAt(fixture.reader, fixture.trust, ReceiptImportBinding{Reference: "invalid", Path: fixture.receiptBinding.Path, ScopeID: "scope-1", RunID: "run-1"}, time.Now)},
		{name: "unsafe path", importer: NewReceiptImporterAt(fixture.reader, fixture.trust, ReceiptImportBinding{Reference: fixture.receiptBinding.Reference, Path: filepath.Join("..", "receipts"), ScopeID: "scope-1", RunID: "run-1"}, time.Now)},
		{name: "empty scope", importer: NewReceiptImporterAt(fixture.reader, fixture.trust, ReceiptImportBinding{Reference: fixture.receiptBinding.Reference, Path: fixture.receiptBinding.Path, RunID: "run-1"}, time.Now)},
		{name: "empty run", importer: NewReceiptImporterAt(fixture.reader, fixture.trust, ReceiptImportBinding{Reference: fixture.receiptBinding.Reference, Path: fixture.receiptBinding.Path, ScopeID: "scope-1"}, time.Now)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.importer.Import(context.Background())
			require.Error(t, err)
			assert.True(t, errors.Is(err, constants.ErrInvalidEvidenceGraph))
		})
	}
}

func TestReceiptImporter_Import_RejectsDigestAndCanonicalMutations(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*standaloneReceiptFixture)
		targetErr error
	}{
		{name: "digest mismatch", mutate: func(f *standaloneReceiptFixture) {
			f.receiptBinding.Reference = constants.ActionReceiptReferencePrefix + ":sha256:" + strings.Repeat("0", 64)
		}, targetErr: constants.ErrChecksumMismatch},
		{name: "noncanonical protojson", mutate: func(f *standaloneReceiptFixture) {
			body := append(append([]byte{}, f.receiptBody...), '\n')
			f.reader.files[f.receiptBinding.Path] = body
			f.receiptBinding.Reference = ContentReferenceForBody(constants.ActionReceiptReferencePrefix, body)
		}, targetErr: constants.ErrEvidenceArtifactMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStandaloneReceiptFixture(t)
			test.mutate(fixture)
			_, err := NewReceiptImporterAt(fixture.reader, fixture.trust, fixture.receiptBinding, time.Now).Import(context.Background())
			require.Error(t, err)
			assert.True(t, errors.Is(err, test.targetErr), "error: %v", err)
		})
	}
}

func TestReceiptImporter_Import_PropagatesTrustAssessmentFailure(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	trustErr := fmt.Errorf("assessment unavailable")
	fixture.trust.err = trustErr
	_, err := NewReceiptImporterAt(fixture.reader, fixture.trust, fixture.receiptBinding, time.Now).Import(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrEvidenceTrustNotAssessed))
	assert.True(t, errors.Is(err, trustErr))
}

func TestPersistenceImporter_Import_VerifiesPersistenceIndependently(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	nodes, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	node := nodes[0]
	assert.Equal(t, VerificationStatusVerified, node.VerificationStatus)
	assert.Equal(t, ArtifactTypeReceiptPersistence, node.ArtifactType)
	assert.Equal(t, fixture.persistenceBinding.Reference, node.ArtifactID)
	assert.Equal(t, fixture.receipt.TransactionId, node.TransactionID)
	assert.Empty(t, node.References)
}

func TestPersistenceImporter_Import_PreservesUnverifiedStatusWithoutAssessedKey(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	fixture.trust.keys = map[string]ed25519.PublicKey{}
	nodes, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, VerificationStatusUnverified, nodes[0].VerificationStatus)
}

func TestPersistenceImporter_Import_MarksInvalidAttestationSignatureFailed(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	fixture.receipt.FinalPersistenceAttestation.Signature = strings.Repeat("0", ed25519.SignatureSize*2)
	receiptBody, err := compliancev1.MarshalCanonical(fixture.receipt)
	require.NoError(t, err)
	persistenceBody, err := compliancev1.MarshalCanonical(fixture.receipt.FinalPersistenceAttestation)
	require.NoError(t, err)
	fixture.receiptBinding.Reference = ContentReferenceForBody(constants.ActionReceiptReferencePrefix, receiptBody)
	fixture.persistenceBinding.ReceiptReference = fixture.receiptBinding.Reference
	fixture.persistenceBinding.Reference = ContentReferenceForBody(constants.ReceiptPersistenceReferencePrefix, persistenceBody)
	fixture.reader.files[fixture.receiptBinding.Path] = receiptBody
	fixture.reader.files[fixture.persistenceBinding.Path] = persistenceBody
	nodes, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, VerificationStatusFailed, nodes[0].VerificationStatus)
}

func TestPersistenceImporter_Import_RejectsAttestationNotEmbeddedInReceipt(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	attestation := &operatorv1.ReceiptPersistenceAttestation{TransactionId: fixture.receipt.TransactionId, PersistedAtUnixMs: 1_700_000_003_000, SignerKeyId: fixture.receipt.SignerKeyId}
	body, err := compliancev1.MarshalCanonical(attestation)
	require.NoError(t, err)
	fixture.persistenceBinding.Reference = ContentReferenceForBody(constants.ReceiptPersistenceReferencePrefix, body)
	fixture.reader.files[fixture.persistenceBinding.Path] = body
	_, err = NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrEvidenceScopeMismatch))
}

func TestReceiptAndPersistenceImporters_Import_RespectCancellation(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewReceiptImporterAt(fixture.reader, fixture.trust, fixture.receiptBinding, time.Now).Import(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestReceiptAndPersistenceImporters_Import_ProduceValidGraph(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	receiptNodes, err := NewReceiptImporterAt(fixture.reader, fixture.trust, fixture.receiptBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	persistenceNodes, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.NoError(t, err)

	graph := NewEvidenceGraph(maxArtifactBytes, []string{constants.MediaTypeJSON})
	for _, node := range append(receiptNodes, persistenceNodes...) {
		require.NoError(t, graph.AddNode(node))
	}
	graph.ValidateAll(time.Unix(1_600_000_000, 0).UTC(), time.Unix(1_800_000_000, 0).UTC())
	assert.True(t, graph.Valid(), "graph failures: %v", graph.Failures())
	assert.Equal(t, 2, graph.NodeCount())
}

func TestReceiptAndPersistenceImporters_Import_RecordDeterministicVerifierMetadata(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	verifiedAt := time.Unix(1_700_000_003, 0).UTC()
	now := func() time.Time { return verifiedAt }
	receiptImporter := NewReceiptImporterAt(fixture.reader, fixture.trust, fixture.receiptBinding, now)
	persistenceImporter := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, now)

	for _, importer := range []func(context.Context) ([]EvidenceNode, error){receiptImporter.Import, persistenceImporter.Import} {
		nodes, err := importer(context.Background())
		require.NoError(t, err)
		require.Len(t, nodes, 1)
		assert.Equal(t, constants.ReceiptEvidenceVerifierID, nodes[0].VerifierID)
		assert.Equal(t, constants.ReceiptEvidenceVerifierVersion, nodes[0].VerifierVersion)
		assert.Equal(t, verifiedAt, nodes[0].VerifiedAt)
	}
}

func TestPersistenceImporter_Import_RejectsInvalidConfiguration(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	tests := []struct {
		name     string
		importer *PersistenceImporter
	}{
		{name: "nil importer", importer: nil},
		{name: "nil reader", importer: NewPersistenceImporterAt(nil, fixture.trust, fixture.persistenceBinding, time.Now)},
		{name: "nil trust", importer: NewPersistenceImporterAt(fixture.reader, nil, fixture.persistenceBinding, time.Now)},
		{name: "invalid persistence reference", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: "invalid", Path: fixture.persistenceBinding.Path, ReceiptReference: fixture.persistenceBinding.ReceiptReference, ReceiptPath: fixture.persistenceBinding.ReceiptPath, ScopeID: fixture.persistenceBinding.ScopeID, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "unsafe persistence path", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: filepath.Join("..", "persistence"), ReceiptReference: fixture.persistenceBinding.ReceiptReference, ReceiptPath: fixture.persistenceBinding.ReceiptPath, ScopeID: fixture.persistenceBinding.ScopeID, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "invalid receipt reference", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: fixture.persistenceBinding.Path, ReceiptReference: "invalid", ReceiptPath: fixture.persistenceBinding.ReceiptPath, ScopeID: fixture.persistenceBinding.ScopeID, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "unsafe receipt path", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: fixture.persistenceBinding.Path, ReceiptReference: fixture.persistenceBinding.ReceiptReference, ReceiptPath: filepath.Join("..", "receipts"), ScopeID: fixture.persistenceBinding.ScopeID, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "empty scope", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: fixture.persistenceBinding.Path, ReceiptReference: fixture.persistenceBinding.ReceiptReference, ReceiptPath: fixture.persistenceBinding.ReceiptPath, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "empty run", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: fixture.persistenceBinding.Path, ReceiptReference: fixture.persistenceBinding.ReceiptReference, ReceiptPath: fixture.persistenceBinding.ReceiptPath, ScopeID: fixture.persistenceBinding.ScopeID}, time.Now)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.importer.Import(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
		})
	}
}

func TestPersistenceImporter_Import_RejectsDigestCanonicalAndBindingMutations(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*testing.T, *standaloneReceiptFixture)
		targetErr error
	}{
		{name: "persistence digest mismatch", mutate: func(_ *testing.T, fixture *standaloneReceiptFixture) {
			fixture.persistenceBinding.Reference = constants.ReceiptPersistenceReferencePrefix + ":sha256:" + strings.Repeat("0", 64)
		}, targetErr: constants.ErrChecksumMismatch},
		{name: "receipt digest mismatch", mutate: func(_ *testing.T, fixture *standaloneReceiptFixture) {
			fixture.persistenceBinding.ReceiptReference = constants.ActionReceiptReferencePrefix + ":sha256:" + strings.Repeat("0", 64)
		}, targetErr: constants.ErrChecksumMismatch},
		{name: "noncanonical persistence protojson", mutate: func(_ *testing.T, fixture *standaloneReceiptFixture) {
			body := append(append([]byte{}, fixture.reader.files[fixture.persistenceBinding.Path]...), '\n')
			fixture.reader.files[fixture.persistenceBinding.Path] = body
			fixture.persistenceBinding.Reference = ContentReferenceForBody(constants.ReceiptPersistenceReferencePrefix, body)
		}, targetErr: constants.ErrEvidenceArtifactMalformed},
		{name: "persistence differs from embedded attestation", mutate: func(t *testing.T, fixture *standaloneReceiptFixture) {
			t.Helper()
			attestation := proto.Clone(fixture.receipt.FinalPersistenceAttestation).(*operatorv1.ReceiptPersistenceAttestation)
			attestation.AuditRecordId = "other-audit-record"
			body, err := compliancev1.MarshalCanonical(attestation)
			require.NoError(t, err)
			fixture.reader.files[fixture.persistenceBinding.Path] = body
			fixture.persistenceBinding.Reference = ContentReferenceForBody(constants.ReceiptPersistenceReferencePrefix, body)
		}, targetErr: constants.ErrEvidenceScopeMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStandaloneReceiptFixture(t)
			test.mutate(t, fixture)
			_, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, test.targetErr)
		})
	}
}

func TestReceiptAndPersistenceImporters_Import_WrapReadFailures(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	readErr := fmt.Errorf("artifact source unavailable")
	reader := &failingArtifactReader{err: readErr}
	for _, importer := range []func(context.Context) ([]EvidenceNode, error){
		NewReceiptImporterAt(reader, fixture.trust, fixture.receiptBinding, time.Now).Import,
		NewPersistenceImporterAt(reader, fixture.trust, fixture.persistenceBinding, time.Now).Import,
	} {
		_, err := importer(context.Background())
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrEvidenceImporterFailed)
		assert.ErrorIs(t, err, readErr)
	}
}

func TestPersistenceImporter_Import_PropagatesTrustAssessmentFailure(t *testing.T) {
	fixture := newStandaloneReceiptFixture(t)
	trustErr := fmt.Errorf("assessment unavailable")
	fixture.trust.err = trustErr
	_, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvidenceTrustNotAssessed)
	assert.ErrorIs(t, err, trustErr)
}
