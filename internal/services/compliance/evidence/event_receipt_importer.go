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
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type AssessedSignerSource interface {
	GetTrustedSignerPublicKey(context.Context, string) (ed25519.PublicKey, error)
}

type EventReceiptImportBinding struct {
	Reference  string
	Path       string
	ScopeID    string
	RunID      string
	AttemptID  string
	ScenarioID string
}

type EventReceiptImporter struct {
	reader  ArtifactReader
	trust   AssessedSignerSource
	binding EventReceiptImportBinding
	nowFunc func() time.Time
}

func NewEventReceiptImporterAt(reader ArtifactReader, trust AssessedSignerSource, binding EventReceiptImportBinding, nowFunc func() time.Time) *EventReceiptImporter {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &EventReceiptImporter{reader: reader, trust: trust, binding: binding, nowFunc: nowFunc}
}

func (i *EventReceiptImporter) Import(ctx context.Context) ([]EvidenceNode, error) {
	if i == nil || i.reader == nil || i.trust == nil || !validEventReceiptBinding(i.binding, constants.EventReceiptReferencePrefix) {
		return nil, fmt.Errorf("%w: reader, assessed trust, content reference, path, scope, and run are required", constants.ErrInvalidEvidenceGraph)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	eventReceipt, result, err := readCanonicalEventReceipt(ctx, i.reader, i.binding)
	if err != nil {
		return nil, err
	}
	status, verifierID, verifierVersion, verifiedAt, err := verifyAssessedEventReceiptSignature(ctx, i.trust, eventReceipt, i.nowFunc)
	if err != nil {
		return nil, err
	}
	references := []string{}
	if eventReceipt.GetFinalPersistenceAttestation() != nil {
		body, err := compliancev1.MarshalCanonical(eventReceipt.GetFinalPersistenceAttestation())
		if err != nil {
			return nil, fmt.Errorf("%w: marshal persistence attestation: %w", constants.ErrEvidenceArtifactMalformed, err)
		}
		references = append(references, ContentReferenceForBody(constants.EventReceiptPersistenceReferencePrefix, body))
	}
	_, digest, _ := ParseExpectedContentReference(i.binding.Reference, constants.EventReceiptReferencePrefix)
	return []EvidenceNode{{ArtifactID: i.binding.Reference, ArtifactType: ArtifactTypeEventReceipt, SHA256: digest, MediaType: constants.MediaTypeJSON, SchemaRef: "ztd.operator.v1.EventReceipt", ProducerIdentity: eventReceipt.GetSignerKeyId(), ProducedAt: time.UnixMilli(eventReceipt.GetExecutedAtUnixMs()), ScopeID: i.binding.ScopeID, RunID: i.binding.RunID, AttemptID: i.binding.AttemptID, ScenarioID: i.binding.ScenarioID, TransactionID: eventReceipt.GetTransactionId(), VerificationStatus: status, VerifierID: verifierID, VerifierVersion: verifierVersion, VerifiedAt: verifiedAt, BundlePath: i.binding.Path, CanonicalBytes: result.Bytes, References: references}}, nil
}

type PersistenceImportBinding struct {
	Reference             string
	Path                  string
	EventReceiptReference string
	EventReceiptPath      string
	ScopeID               string
	RunID                 string
	AttemptID             string
	ScenarioID            string
}

type PersistenceImporter struct {
	reader  ArtifactReader
	trust   AssessedSignerSource
	binding PersistenceImportBinding
	nowFunc func() time.Time
}

func NewPersistenceImporterAt(reader ArtifactReader, trust AssessedSignerSource, binding PersistenceImportBinding, nowFunc func() time.Time) *PersistenceImporter {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &PersistenceImporter{reader: reader, trust: trust, binding: binding, nowFunc: nowFunc}
}

func (i *PersistenceImporter) Import(ctx context.Context) ([]EvidenceNode, error) {
	if i == nil || i.reader == nil || i.trust == nil || !validPersistenceBinding(i.binding) {
		return nil, fmt.Errorf("%w: reader, assessed trust, content references, paths, scope, and run are required", constants.ErrInvalidEvidenceGraph)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	eventReceiptBinding := EventReceiptImportBinding{Reference: i.binding.EventReceiptReference, Path: i.binding.EventReceiptPath, ScopeID: i.binding.ScopeID, RunID: i.binding.RunID, AttemptID: i.binding.AttemptID, ScenarioID: i.binding.ScenarioID}
	eventReceipt, _, err := readCanonicalEventReceipt(ctx, i.reader, eventReceiptBinding)
	if err != nil {
		return nil, err
	}
	attestation, result, err := readCanonicalPersistence(ctx, i.reader, i.binding)
	if err != nil {
		return nil, err
	}
	if eventReceipt.GetFinalPersistenceAttestation() == nil || !proto.Equal(eventReceipt.GetFinalPersistenceAttestation(), attestation) || eventReceipt.GetTransactionId() != attestation.GetTransactionId() || eventReceipt.GetSignerKeyId() != attestation.GetSignerKeyId() {
		return nil, fmt.Errorf("%w: persistence attestation does not match event receipt", constants.ErrEvidenceScopeMismatch)
	}
	status, verifierID, verifierVersion, verifiedAt, err := verifyAssessedPersistence(ctx, i.trust, eventReceipt, i.nowFunc)
	if err != nil {
		return nil, err
	}
	_, digest, _ := ParseExpectedContentReference(i.binding.Reference, constants.EventReceiptPersistenceReferencePrefix)
	return []EvidenceNode{{ArtifactID: i.binding.Reference, ArtifactType: ArtifactTypeEventReceiptPersistence, SHA256: digest, MediaType: constants.MediaTypeJSON, SchemaRef: "ztd.operator.v1.EventReceiptPersistenceAttestation", ProducerIdentity: attestation.GetSignerKeyId(), ProducedAt: time.UnixMilli(attestation.GetPersistedAtUnixMs()), ScopeID: i.binding.ScopeID, RunID: i.binding.RunID, AttemptID: i.binding.AttemptID, ScenarioID: i.binding.ScenarioID, TransactionID: attestation.GetTransactionId(), VerificationStatus: status, VerifierID: verifierID, VerifierVersion: verifierVersion, VerifiedAt: verifiedAt, BundlePath: i.binding.Path, CanonicalBytes: result.Bytes, References: []string{}}}, nil
}

func readCanonicalEventReceipt(ctx context.Context, reader ArtifactReader, binding EventReceiptImportBinding) (*operatorv1.EventReceipt, ReadResult, error) {
	_, digest, _ := ParseExpectedContentReference(binding.Reference, constants.EventReceiptReferencePrefix)
	result, err := ReadAndDigest(reader, ctx, binding.Path, maxArtifactBytes)
	if err != nil {
		return nil, ReadResult{}, fmt.Errorf("%w: %s: %w", constants.ErrEvidenceImporterFailed, binding.Path, err)
	}
	if !VerifyDigest(result.Bytes, digest) {
		return nil, ReadResult{}, fmt.Errorf("%w: %s", constants.ErrChecksumMismatch, binding.Reference)
	}
	eventReceipt := &operatorv1.EventReceipt{}
	if err := compliancev1.UnmarshalCanonical(result.Bytes, eventReceipt); err != nil {
		return nil, ReadResult{}, fmt.Errorf("%w: %s: %w", constants.ErrEvidenceArtifactMalformed, binding.Path, err)
	}
	if eventReceipt.GetTransactionId() == "" || eventReceipt.GetSignerKeyId() == "" || eventReceipt.GetExecutedAtUnixMs() <= 0 {
		return nil, ReadResult{}, fmt.Errorf("%w: event receipt binding is incomplete", constants.ErrEvidenceArtifactMalformed)
	}
	return eventReceipt, result, nil
}

func readCanonicalPersistence(ctx context.Context, reader ArtifactReader, binding PersistenceImportBinding) (*operatorv1.EventReceiptPersistenceAttestation, ReadResult, error) {
	_, digest, _ := ParseExpectedContentReference(binding.Reference, constants.EventReceiptPersistenceReferencePrefix)
	result, err := ReadAndDigest(reader, ctx, binding.Path, maxArtifactBytes)
	if err != nil {
		return nil, ReadResult{}, fmt.Errorf("%w: %s: %w", constants.ErrEvidenceImporterFailed, binding.Path, err)
	}
	if !VerifyDigest(result.Bytes, digest) {
		return nil, ReadResult{}, fmt.Errorf("%w: %s", constants.ErrChecksumMismatch, binding.Reference)
	}
	attestation := &operatorv1.EventReceiptPersistenceAttestation{}
	if err := compliancev1.UnmarshalCanonical(result.Bytes, attestation); err != nil {
		return nil, ReadResult{}, fmt.Errorf("%w: %s: %w", constants.ErrEvidenceArtifactMalformed, binding.Path, err)
	}
	if attestation.GetTransactionId() == "" || attestation.GetSignerKeyId() == "" || attestation.GetPersistedAtUnixMs() <= 0 {
		return nil, ReadResult{}, fmt.Errorf("%w: persistence binding is incomplete", constants.ErrEvidenceArtifactMalformed)
	}
	return attestation, result, nil
}

func verifyAssessedEventReceiptSignature(ctx context.Context, trust AssessedSignerSource, eventReceipt *operatorv1.EventReceipt, nowFunc func() time.Time) (VerificationStatus, string, string, time.Time, error) {
	publicKey, err := trust.GetTrustedSignerPublicKey(ctx, eventReceipt.GetSignerKeyId())
	if errors.Is(err, constants.ErrTrustedSignerKeyNotFound) {
		return VerificationStatusUnverified, "", "", time.Time{}, nil
	}
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("%w: resolve signer %s: %w", constants.ErrEvidenceTrustNotAssessed, eventReceipt.GetSignerKeyId(), err)
	}
	status := VerificationStatusVerified
	if err := governance.VerifyEventReceiptSignature(eventReceipt, publicKey); err != nil {
		status = VerificationStatusFailed
	}
	return status, constants.EventReceiptEvidenceVerifierID, constants.EventReceiptEvidenceVerifierVersion, nowFunc(), nil
}

func verifyAssessedPersistence(ctx context.Context, trust AssessedSignerSource, eventReceipt *operatorv1.EventReceipt, nowFunc func() time.Time) (VerificationStatus, string, string, time.Time, error) {
	publicKey, err := trust.GetTrustedSignerPublicKey(ctx, eventReceipt.GetSignerKeyId())
	if errors.Is(err, constants.ErrTrustedSignerKeyNotFound) {
		return VerificationStatusUnverified, "", "", time.Time{}, nil
	}
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("%w: resolve signer %s: %w", constants.ErrEvidenceTrustNotAssessed, eventReceipt.GetSignerKeyId(), err)
	}
	status := VerificationStatusVerified
	if governance.VerifyEventReceipt(eventReceipt, publicKey) != nil {
		status = VerificationStatusFailed
	}
	return status, constants.EventReceiptEvidenceVerifierID, constants.EventReceiptEvidenceVerifierVersion, nowFunc(), nil
}

func validEventReceiptBinding(binding EventReceiptImportBinding, prefix string) bool {
	_, _, referenceValid := ParseExpectedContentReference(binding.Reference, prefix)
	return referenceValid && ValidRelativePath(binding.Path) && binding.ScopeID != "" && binding.RunID != ""
}

func validPersistenceBinding(binding PersistenceImportBinding) bool {
	return validEventReceiptBinding(EventReceiptImportBinding{Reference: binding.Reference, Path: binding.Path, ScopeID: binding.ScopeID, RunID: binding.RunID}, constants.EventReceiptPersistenceReferencePrefix) && validEventReceiptBinding(EventReceiptImportBinding{Reference: binding.EventReceiptReference, Path: binding.EventReceiptPath, ScopeID: binding.ScopeID, RunID: binding.RunID}, constants.EventReceiptReferencePrefix)
}
