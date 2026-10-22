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

type standaloneEventReceiptFixture struct {
	reader              *memoryArtifactReader
	trust               *assessedSignerStub
	eventReceipt        *operatorv1.EventReceipt
	eventReceiptBody    []byte
	eventReceiptBinding EventReceiptImportBinding
	persistenceBinding  PersistenceImportBinding
}

func newStandaloneEventReceiptFixture(t *testing.T) *standaloneEventReceiptFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signerKeyID := "assessed-signer"
	eventReceipt := &operatorv1.EventReceipt{TransactionId: "tx-standalone", TransactionHash: "tx-hash", SignerKeyId: signerKeyID, ExecutedAtUnixMs: 1_700_000_001_000}
	eventReceiptPayload, err := governance.CanonicalizeEventReceipt(eventReceipt)
	require.NoError(t, err)
	eventReceipt.Signature = hex.EncodeToString(ed25519.Sign(privateKey, eventReceiptPayload))
	attestation := &operatorv1.EventReceiptPersistenceAttestation{TransactionId: eventReceipt.TransactionId, EventReceiptSignatureDigest: governance.SignatureDigest([]string{eventReceipt.Signature}), PersistedAtUnixMs: 1_700_000_002_000, AuditRecordId: eventReceipt.TransactionId, SignerKeyId: signerKeyID}
	attestationPayload, err := governance.CanonicalizeEventReceiptPersistenceAttestation(attestation)
	require.NoError(t, err)
	attestation.Signature = hex.EncodeToString(ed25519.Sign(privateKey, attestationPayload))
	eventReceipt.FinalPersistenceAttestation = attestation
	eventReceiptBody, err := compliancev1.MarshalCanonical(eventReceipt)
	require.NoError(t, err)
	persistenceBody, err := compliancev1.MarshalCanonical(attestation)
	require.NoError(t, err)
	eventReceiptReference := ContentReferenceForBody(constants.EventReceiptReferencePrefix, eventReceiptBody)
	persistenceReference := ContentReferenceForBody(constants.EventReceiptPersistenceReferencePrefix, persistenceBody)
	eventReceiptPath := filepath.Join("event_receipts", digestHex(eventReceiptBody)+constants.FileExtJSON)
	persistencePath := filepath.Join("persistence", digestHex(persistenceBody)+constants.FileExtJSON)
	return &standaloneEventReceiptFixture{
		reader:              &memoryArtifactReader{files: map[string][]byte{eventReceiptPath: eventReceiptBody, persistencePath: persistenceBody}},
		trust:               &assessedSignerStub{keys: map[string]ed25519.PublicKey{signerKeyID: publicKey}},
		eventReceipt:        eventReceipt,
		eventReceiptBody:    eventReceiptBody,
		eventReceiptBinding: EventReceiptImportBinding{Reference: eventReceiptReference, Path: eventReceiptPath, ScopeID: "scope-1", RunID: "run-1", AttemptID: "attempt-1", ScenarioID: "scenario-1"},
		persistenceBinding:  PersistenceImportBinding{Reference: persistenceReference, Path: persistencePath, EventReceiptReference: eventReceiptReference, EventReceiptPath: eventReceiptPath, ScopeID: "scope-1", RunID: "run-1", AttemptID: "attempt-1", ScenarioID: "scenario-1"},
	}
}

func TestEventReceiptImporter_Import_VerifiesSignatureWithAssessedTrust(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	nodes, err := NewEventReceiptImporterAt(fixture.reader, fixture.trust, fixture.eventReceiptBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	node := nodes[0]
	assert.Equal(t, VerificationStatusVerified, node.VerificationStatus)
	assert.Equal(t, fixture.eventReceiptBinding.Reference, node.ArtifactID)
	assert.Equal(t, fixture.eventReceipt.TransactionId, node.TransactionID)
	assert.Equal(t, fixture.eventReceiptBinding.ScopeID, node.ScopeID)
	assert.Equal(t, fixture.eventReceiptBinding.RunID, node.RunID)
	assert.Equal(t, []string{fixture.persistenceBinding.Reference}, node.References)
}

func TestEventReceiptImporter_Import_PreservesUnverifiedStatusWithoutAssessedKey(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	fixture.trust.keys = map[string]ed25519.PublicKey{}
	nodes, err := NewEventReceiptImporterAt(fixture.reader, fixture.trust, fixture.eventReceiptBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, VerificationStatusUnverified, nodes[0].VerificationStatus)
	assert.Empty(t, nodes[0].VerifierID)
}

func TestEventReceiptImporter_Import_MarksInvalidSignatureFailed(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	fixture.trust.keys[fixture.eventReceipt.SignerKeyId] = ed25519.PublicKey(strings.Repeat("x", ed25519.PublicKeySize))
	nodes, err := NewEventReceiptImporterAt(fixture.reader, fixture.trust, fixture.eventReceiptBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, VerificationStatusFailed, nodes[0].VerificationStatus)
	assert.Equal(t, constants.EventReceiptEvidenceVerifierID, nodes[0].VerifierID)
}

func TestEventReceiptImporter_Import_RejectsInvalidConfiguration(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	tests := []struct {
		name     string
		importer *EventReceiptImporter
	}{
		{name: "nil importer", importer: nil},
		{name: "nil reader", importer: NewEventReceiptImporterAt(nil, fixture.trust, fixture.eventReceiptBinding, time.Now)},
		{name: "nil trust", importer: NewEventReceiptImporterAt(fixture.reader, nil, fixture.eventReceiptBinding, time.Now)},
		{name: "invalid reference", importer: NewEventReceiptImporterAt(fixture.reader, fixture.trust, EventReceiptImportBinding{Reference: "invalid", Path: fixture.eventReceiptBinding.Path, ScopeID: "scope-1", RunID: "run-1"}, time.Now)},
		{name: "unsafe path", importer: NewEventReceiptImporterAt(fixture.reader, fixture.trust, EventReceiptImportBinding{Reference: fixture.eventReceiptBinding.Reference, Path: filepath.Join("..", "event_receipts"), ScopeID: "scope-1", RunID: "run-1"}, time.Now)},
		{name: "empty scope", importer: NewEventReceiptImporterAt(fixture.reader, fixture.trust, EventReceiptImportBinding{Reference: fixture.eventReceiptBinding.Reference, Path: fixture.eventReceiptBinding.Path, RunID: "run-1"}, time.Now)},
		{name: "empty run", importer: NewEventReceiptImporterAt(fixture.reader, fixture.trust, EventReceiptImportBinding{Reference: fixture.eventReceiptBinding.Reference, Path: fixture.eventReceiptBinding.Path, ScopeID: "scope-1"}, time.Now)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.importer.Import(context.Background())
			require.Error(t, err)
			assert.True(t, errors.Is(err, constants.ErrInvalidEvidenceGraph))
		})
	}
}

func TestEventReceiptImporter_Import_RejectsDigestAndCanonicalMutations(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*standaloneEventReceiptFixture)
		targetErr error
	}{
		{name: "digest mismatch", mutate: func(f *standaloneEventReceiptFixture) {
			f.eventReceiptBinding.Reference = constants.EventReceiptReferencePrefix + ":sha256:" + strings.Repeat("0", 64)
		}, targetErr: constants.ErrChecksumMismatch},
		{name: "noncanonical protojson", mutate: func(f *standaloneEventReceiptFixture) {
			body := append(append([]byte{}, f.eventReceiptBody...), '\n')
			f.reader.files[f.eventReceiptBinding.Path] = body
			f.eventReceiptBinding.Reference = ContentReferenceForBody(constants.EventReceiptReferencePrefix, body)
		}, targetErr: constants.ErrEvidenceArtifactMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStandaloneEventReceiptFixture(t)
			test.mutate(fixture)
			_, err := NewEventReceiptImporterAt(fixture.reader, fixture.trust, fixture.eventReceiptBinding, time.Now).Import(context.Background())
			require.Error(t, err)
			assert.True(t, errors.Is(err, test.targetErr), "error: %v", err)
		})
	}
}

func TestEventReceiptImporter_Import_PropagatesTrustAssessmentFailure(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	trustErr := fmt.Errorf("assessment unavailable")
	fixture.trust.err = trustErr
	_, err := NewEventReceiptImporterAt(fixture.reader, fixture.trust, fixture.eventReceiptBinding, time.Now).Import(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrEvidenceTrustNotAssessed))
	assert.True(t, errors.Is(err, trustErr))
}

func TestPersistenceImporter_Import_VerifiesPersistenceIndependently(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	nodes, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	node := nodes[0]
	assert.Equal(t, VerificationStatusVerified, node.VerificationStatus)
	assert.Equal(t, ArtifactTypeEventReceiptPersistence, node.ArtifactType)
	assert.Equal(t, fixture.persistenceBinding.Reference, node.ArtifactID)
	assert.Equal(t, fixture.eventReceipt.TransactionId, node.TransactionID)
	assert.Empty(t, node.References)
}

func TestPersistenceImporter_Import_PreservesUnverifiedStatusWithoutAssessedKey(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	fixture.trust.keys = map[string]ed25519.PublicKey{}
	nodes, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, VerificationStatusUnverified, nodes[0].VerificationStatus)
}

func TestPersistenceImporter_Import_MarksInvalidAttestationSignatureFailed(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	fixture.eventReceipt.FinalPersistenceAttestation.Signature = strings.Repeat("0", ed25519.SignatureSize*2)
	eventReceiptBody, err := compliancev1.MarshalCanonical(fixture.eventReceipt)
	require.NoError(t, err)
	persistenceBody, err := compliancev1.MarshalCanonical(fixture.eventReceipt.FinalPersistenceAttestation)
	require.NoError(t, err)
	fixture.eventReceiptBinding.Reference = ContentReferenceForBody(constants.EventReceiptReferencePrefix, eventReceiptBody)
	fixture.persistenceBinding.EventReceiptReference = fixture.eventReceiptBinding.Reference
	fixture.persistenceBinding.Reference = ContentReferenceForBody(constants.EventReceiptPersistenceReferencePrefix, persistenceBody)
	fixture.reader.files[fixture.eventReceiptBinding.Path] = eventReceiptBody
	fixture.reader.files[fixture.persistenceBinding.Path] = persistenceBody
	nodes, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, VerificationStatusFailed, nodes[0].VerificationStatus)
}

func TestPersistenceImporter_Import_RejectsAttestationNotEmbeddedInEventReceipt(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	attestation := &operatorv1.EventReceiptPersistenceAttestation{TransactionId: fixture.eventReceipt.TransactionId, PersistedAtUnixMs: 1_700_000_003_000, SignerKeyId: fixture.eventReceipt.SignerKeyId}
	body, err := compliancev1.MarshalCanonical(attestation)
	require.NoError(t, err)
	fixture.persistenceBinding.Reference = ContentReferenceForBody(constants.EventReceiptPersistenceReferencePrefix, body)
	fixture.reader.files[fixture.persistenceBinding.Path] = body
	_, err = NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrEvidenceScopeMismatch))
}

func TestEventReceiptAndPersistenceImporters_Import_RespectCancellation(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewEventReceiptImporterAt(fixture.reader, fixture.trust, fixture.eventReceiptBinding, time.Now).Import(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestEventReceiptAndPersistenceImporters_Import_ProduceValidGraph(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	eventReceiptNodes, err := NewEventReceiptImporterAt(fixture.reader, fixture.trust, fixture.eventReceiptBinding, time.Now).Import(context.Background())
	require.NoError(t, err)
	persistenceNodes, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.NoError(t, err)

	graph := NewEvidenceGraph(maxArtifactBytes, []string{constants.MediaTypeJSON})
	for _, node := range append(eventReceiptNodes, persistenceNodes...) {
		require.NoError(t, graph.AddNode(node))
	}
	graph.ValidateAll(time.Unix(1_600_000_000, 0).UTC(), time.Unix(1_800_000_000, 0).UTC())
	assert.True(t, graph.Valid(), "graph failures: %v", graph.Failures())
	assert.Equal(t, 2, graph.NodeCount())
}

func TestEventReceiptAndPersistenceImporters_Import_RecordDeterministicVerifierMetadata(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	verifiedAt := time.Unix(1_700_000_003, 0).UTC()
	now := func() time.Time { return verifiedAt }
	eventReceiptImporter := NewEventReceiptImporterAt(fixture.reader, fixture.trust, fixture.eventReceiptBinding, now)
	persistenceImporter := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, now)

	for _, importer := range []func(context.Context) ([]EvidenceNode, error){eventReceiptImporter.Import, persistenceImporter.Import} {
		nodes, err := importer(context.Background())
		require.NoError(t, err)
		require.Len(t, nodes, 1)
		assert.Equal(t, constants.EventReceiptEvidenceVerifierID, nodes[0].VerifierID)
		assert.Equal(t, constants.EventReceiptEvidenceVerifierVersion, nodes[0].VerifierVersion)
		assert.Equal(t, verifiedAt, nodes[0].VerifiedAt)
	}
}

func TestPersistenceImporter_Import_RejectsInvalidConfiguration(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	tests := []struct {
		name     string
		importer *PersistenceImporter
	}{
		{name: "nil importer", importer: nil},
		{name: "nil reader", importer: NewPersistenceImporterAt(nil, fixture.trust, fixture.persistenceBinding, time.Now)},
		{name: "nil trust", importer: NewPersistenceImporterAt(fixture.reader, nil, fixture.persistenceBinding, time.Now)},
		{name: "invalid persistence reference", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: "invalid", Path: fixture.persistenceBinding.Path, EventReceiptReference: fixture.persistenceBinding.EventReceiptReference, EventReceiptPath: fixture.persistenceBinding.EventReceiptPath, ScopeID: fixture.persistenceBinding.ScopeID, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "unsafe persistence path", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: filepath.Join("..", "persistence"), EventReceiptReference: fixture.persistenceBinding.EventReceiptReference, EventReceiptPath: fixture.persistenceBinding.EventReceiptPath, ScopeID: fixture.persistenceBinding.ScopeID, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "invalid event receipt reference", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: fixture.persistenceBinding.Path, EventReceiptReference: "invalid", EventReceiptPath: fixture.persistenceBinding.EventReceiptPath, ScopeID: fixture.persistenceBinding.ScopeID, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "unsafe event receipt path", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: fixture.persistenceBinding.Path, EventReceiptReference: fixture.persistenceBinding.EventReceiptReference, EventReceiptPath: filepath.Join("..", "event_receipts"), ScopeID: fixture.persistenceBinding.ScopeID, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "empty scope", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: fixture.persistenceBinding.Path, EventReceiptReference: fixture.persistenceBinding.EventReceiptReference, EventReceiptPath: fixture.persistenceBinding.EventReceiptPath, RunID: fixture.persistenceBinding.RunID}, time.Now)},
		{name: "empty run", importer: NewPersistenceImporterAt(fixture.reader, fixture.trust, PersistenceImportBinding{Reference: fixture.persistenceBinding.Reference, Path: fixture.persistenceBinding.Path, EventReceiptReference: fixture.persistenceBinding.EventReceiptReference, EventReceiptPath: fixture.persistenceBinding.EventReceiptPath, ScopeID: fixture.persistenceBinding.ScopeID}, time.Now)},
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
		mutate    func(*testing.T, *standaloneEventReceiptFixture)
		targetErr error
	}{
		{name: "persistence digest mismatch", mutate: func(_ *testing.T, fixture *standaloneEventReceiptFixture) {
			fixture.persistenceBinding.Reference = constants.EventReceiptPersistenceReferencePrefix + ":sha256:" + strings.Repeat("0", 64)
		}, targetErr: constants.ErrChecksumMismatch},
		{name: "event receipt digest mismatch", mutate: func(_ *testing.T, fixture *standaloneEventReceiptFixture) {
			fixture.persistenceBinding.EventReceiptReference = constants.EventReceiptReferencePrefix + ":sha256:" + strings.Repeat("0", 64)
		}, targetErr: constants.ErrChecksumMismatch},
		{name: "noncanonical persistence protojson", mutate: func(_ *testing.T, fixture *standaloneEventReceiptFixture) {
			body := append(append([]byte{}, fixture.reader.files[fixture.persistenceBinding.Path]...), '\n')
			fixture.reader.files[fixture.persistenceBinding.Path] = body
			fixture.persistenceBinding.Reference = ContentReferenceForBody(constants.EventReceiptPersistenceReferencePrefix, body)
		}, targetErr: constants.ErrEvidenceArtifactMalformed},
		{name: "persistence differs from embedded attestation", mutate: func(t *testing.T, fixture *standaloneEventReceiptFixture) {
			attestation := proto.Clone(fixture.eventReceipt.FinalPersistenceAttestation).(*operatorv1.EventReceiptPersistenceAttestation)
			attestation.AuditRecordId = "other-audit-record"
			body, err := compliancev1.MarshalCanonical(attestation)
			require.NoError(t, err)
			fixture.reader.files[fixture.persistenceBinding.Path] = body
			fixture.persistenceBinding.Reference = ContentReferenceForBody(constants.EventReceiptPersistenceReferencePrefix, body)
		}, targetErr: constants.ErrEvidenceScopeMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStandaloneEventReceiptFixture(t)
			test.mutate(t, fixture)
			_, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, test.targetErr)
		})
	}
}

func TestEventReceiptAndPersistenceImporters_Import_WrapReadFailures(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	readErr := fmt.Errorf("artifact source unavailable")
	reader := &failingArtifactReader{err: readErr}
	for _, importer := range []func(context.Context) ([]EvidenceNode, error){
		NewEventReceiptImporterAt(reader, fixture.trust, fixture.eventReceiptBinding, time.Now).Import,
		NewPersistenceImporterAt(reader, fixture.trust, fixture.persistenceBinding, time.Now).Import,
	} {
		_, err := importer(context.Background())
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrEvidenceImporterFailed)
		assert.ErrorIs(t, err, readErr)
	}
}

func TestPersistenceImporter_Import_PropagatesTrustAssessmentFailure(t *testing.T) {
	fixture := newStandaloneEventReceiptFixture(t)
	trustErr := fmt.Errorf("assessment unavailable")
	fixture.trust.err = trustErr
	_, err := NewPersistenceImporterAt(fixture.reader, fixture.trust, fixture.persistenceBinding, time.Now).Import(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvidenceTrustNotAssessed)
	assert.ErrorIs(t, err, trustErr)
}
