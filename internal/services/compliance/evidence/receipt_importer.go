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

type ReceiptImportBinding struct {
	Reference  string
	Path       string
	ScopeID    string
	RunID      string
	AttemptID  string
	ScenarioID string
}

type ReceiptImporter struct {
	reader  ArtifactReader
	trust   AssessedSignerSource
	binding ReceiptImportBinding
	nowFunc func() time.Time
}

func NewReceiptImporterAt(reader ArtifactReader, trust AssessedSignerSource, binding ReceiptImportBinding, nowFunc func() time.Time) *ReceiptImporter {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &ReceiptImporter{reader: reader, trust: trust, binding: binding, nowFunc: nowFunc}
}

func (i *ReceiptImporter) Import(ctx context.Context) ([]EvidenceNode, error) {
	if i == nil || i.reader == nil || i.trust == nil {
		return nil, fmt.Errorf("%w: reader and assessed trust are required", constants.ErrInvalidEvidenceGraph)
	}
	if err := validReceiptBinding(i.binding, constants.EventReceiptReferencePrefix); err != nil {
		return nil, fmt.Errorf("%w: event receipt binding: %w", constants.ErrInvalidEvidenceGraph, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	receipt, result, err := readCanonicalReceipt(ctx, i.reader, i.binding)
	if err != nil {
		return nil, err
	}
	status, verifierID, verifierVersion, verifiedAt, err := verifyAssessedReceiptSignature(ctx, i.trust, receipt, i.nowFunc)
	if err != nil {
		return nil, err
	}
	references := []string{}
	if receipt.GetFinalPersistenceAttestation() != nil {
		body, err := compliancev1.MarshalCanonical(receipt.GetFinalPersistenceAttestation())
		if err != nil {
			return nil, fmt.Errorf("%w: marshal persistence attestation: %w", constants.ErrEvidenceArtifactMalformed, err)
		}
		references = append(references, ContentReferenceForBody(constants.ReceiptPersistenceReferencePrefix, body))
	}
	_, digest, _ := ParseExpectedContentReference(i.binding.Reference, constants.EventReceiptReferencePrefix)
	return []EvidenceNode{{ArtifactID: i.binding.Reference, ArtifactType: ArtifactTypeEventReceipt, SHA256: digest, MediaType: constants.MediaTypeJSON, SchemaRef: "ztd.operator.v1.EventReceipt", ProducerIdentity: receipt.GetSignerKeyId(), ProducedAt: time.UnixMilli(receipt.GetExecutedAtUnixMs()), ScopeID: i.binding.ScopeID, RunID: i.binding.RunID, AttemptID: i.binding.AttemptID, ScenarioID: i.binding.ScenarioID, TransactionID: receipt.GetTransactionId(), VerificationStatus: status, VerifierID: verifierID, VerifierVersion: verifierVersion, VerifiedAt: verifiedAt, BundlePath: i.binding.Path, CanonicalBytes: result.Bytes, References: references}}, nil
}

type PersistenceImportBinding struct {
	Reference        string
	Path             string
	ReceiptReference string
	ReceiptPath      string
	ScopeID          string
	RunID            string
	AttemptID        string
	ScenarioID       string
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
	if i == nil || i.reader == nil || i.trust == nil {
		return nil, fmt.Errorf("%w: reader and assessed trust are required", constants.ErrInvalidEvidenceGraph)
	}
	if err := validPersistenceBinding(i.binding); err != nil {
		return nil, fmt.Errorf("%w: receipt persistence binding: %w", constants.ErrInvalidEvidenceGraph, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	receiptBinding := ReceiptImportBinding{Reference: i.binding.ReceiptReference, Path: i.binding.ReceiptPath, ScopeID: i.binding.ScopeID, RunID: i.binding.RunID, AttemptID: i.binding.AttemptID, ScenarioID: i.binding.ScenarioID}
	receipt, _, err := readCanonicalReceipt(ctx, i.reader, receiptBinding)
	if err != nil {
		return nil, err
	}
	attestation, result, err := readCanonicalPersistence(ctx, i.reader, i.binding)
	if err != nil {
		return nil, err
	}
	if receipt.GetFinalPersistenceAttestation() == nil || !proto.Equal(receipt.GetFinalPersistenceAttestation(), attestation) || receipt.GetTransactionId() != attestation.GetTransactionId() || receipt.GetSignerKeyId() != attestation.GetSignerKeyId() {
		return nil, fmt.Errorf("%w: persistence attestation does not match receipt", constants.ErrEvidenceScopeMismatch)
	}
	status, verifierID, verifierVersion, verifiedAt, err := verifyAssessedPersistence(ctx, i.trust, receipt, i.nowFunc)
	if err != nil {
		return nil, err
	}
	_, digest, _ := ParseExpectedContentReference(i.binding.Reference, constants.ReceiptPersistenceReferencePrefix)
	return []EvidenceNode{{ArtifactID: i.binding.Reference, ArtifactType: ArtifactTypeReceiptPersistence, SHA256: digest, MediaType: constants.MediaTypeJSON, SchemaRef: "ztd.operator.v1.ReceiptPersistenceAttestation", ProducerIdentity: attestation.GetSignerKeyId(), ProducedAt: time.UnixMilli(attestation.GetPersistedAtUnixMs()), ScopeID: i.binding.ScopeID, RunID: i.binding.RunID, AttemptID: i.binding.AttemptID, ScenarioID: i.binding.ScenarioID, TransactionID: attestation.GetTransactionId(), VerificationStatus: status, VerifierID: verifierID, VerifierVersion: verifierVersion, VerifiedAt: verifiedAt, BundlePath: i.binding.Path, CanonicalBytes: result.Bytes, References: []string{}}}, nil
}

func readCanonicalReceipt(ctx context.Context, reader ArtifactReader, binding ReceiptImportBinding) (*operatorv1.EventReceipt, ReadResult, error) {
	_, digest, _ := ParseExpectedContentReference(binding.Reference, constants.EventReceiptReferencePrefix)
	result, err := ReadAndDigest(reader, ctx, binding.Path, maxArtifactBytes)
	if err != nil {
		return nil, ReadResult{}, fmt.Errorf("%w: %s: %w", constants.ErrEvidenceImporterFailed, binding.Path, err)
	}
	if !VerifyDigest(result.Bytes, digest) {
		return nil, ReadResult{}, fmt.Errorf("%w: %s", constants.ErrChecksumMismatch, binding.Reference)
	}
	receipt := &operatorv1.EventReceipt{}
	if err := compliancev1.UnmarshalCanonical(result.Bytes, receipt); err != nil {
		return nil, ReadResult{}, fmt.Errorf("%w: %s: %w", constants.ErrEvidenceArtifactMalformed, binding.Path, err)
	}
	if receipt.GetTransactionId() == "" || receipt.GetSignerKeyId() == "" || receipt.GetExecutedAtUnixMs() <= 0 {
		return nil, ReadResult{}, fmt.Errorf("%w: receipt binding is incomplete", constants.ErrEvidenceArtifactMalformed)
	}
	return receipt, result, nil
}

func readCanonicalPersistence(ctx context.Context, reader ArtifactReader, binding PersistenceImportBinding) (*operatorv1.ReceiptPersistenceAttestation, ReadResult, error) {
	_, digest, _ := ParseExpectedContentReference(binding.Reference, constants.ReceiptPersistenceReferencePrefix)
	result, err := ReadAndDigest(reader, ctx, binding.Path, maxArtifactBytes)
	if err != nil {
		return nil, ReadResult{}, fmt.Errorf("%w: %s: %w", constants.ErrEvidenceImporterFailed, binding.Path, err)
	}
	if !VerifyDigest(result.Bytes, digest) {
		return nil, ReadResult{}, fmt.Errorf("%w: %s", constants.ErrChecksumMismatch, binding.Reference)
	}
	attestation := &operatorv1.ReceiptPersistenceAttestation{}
	if err := compliancev1.UnmarshalCanonical(result.Bytes, attestation); err != nil {
		return nil, ReadResult{}, fmt.Errorf("%w: %s: %w", constants.ErrEvidenceArtifactMalformed, binding.Path, err)
	}
	if attestation.GetTransactionId() == "" || attestation.GetSignerKeyId() == "" || attestation.GetPersistedAtUnixMs() <= 0 {
		return nil, ReadResult{}, fmt.Errorf("%w: persistence binding is incomplete", constants.ErrEvidenceArtifactMalformed)
	}
	return attestation, result, nil
}

func verifyAssessedReceiptSignature(ctx context.Context, trust AssessedSignerSource, receipt *operatorv1.EventReceipt, nowFunc func() time.Time) (VerificationStatus, string, string, time.Time, error) {
	publicKey, err := trust.GetTrustedSignerPublicKey(ctx, receipt.GetSignerKeyId())
	if errors.Is(err, constants.ErrTrustedSignerKeyNotFound) {
		return VerificationStatusUnverified, "", "", time.Time{}, nil
	}
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("%w: resolve signer %s: %w", constants.ErrEvidenceTrustNotAssessed, receipt.GetSignerKeyId(), err)
	}
	status := VerificationStatusVerified
	if err := governance.VerifyEventReceiptSignature(receipt, publicKey); err != nil {
		status = VerificationStatusFailed
	}
	return status, constants.ReceiptEvidenceVerifierID, constants.ReceiptEvidenceVerifierVersion, nowFunc(), nil
}

func verifyAssessedPersistence(ctx context.Context, trust AssessedSignerSource, receipt *operatorv1.EventReceipt, nowFunc func() time.Time) (VerificationStatus, string, string, time.Time, error) {
	publicKey, err := trust.GetTrustedSignerPublicKey(ctx, receipt.GetSignerKeyId())
	if errors.Is(err, constants.ErrTrustedSignerKeyNotFound) {
		return VerificationStatusUnverified, "", "", time.Time{}, nil
	}
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("%w: resolve signer %s: %w", constants.ErrEvidenceTrustNotAssessed, receipt.GetSignerKeyId(), err)
	}
	status := VerificationStatusVerified
	if governance.VerifyEventReceipt(receipt, publicKey) != nil {
		status = VerificationStatusFailed
	}
	return status, constants.ReceiptEvidenceVerifierID, constants.ReceiptEvidenceVerifierVersion, nowFunc(), nil
}

func validReceiptBinding(binding ReceiptImportBinding, prefix string) error {
	if _, _, referenceValid := ParseExpectedContentReference(binding.Reference, prefix); !referenceValid {
		return errors.New("content reference is invalid")
	}
	if err := ValidateRelativePath(binding.Path); err != nil {
		return err
	}
	if binding.ScopeID == "" || binding.RunID == "" {
		return constants.ErrMissingRequiredField
	}
	return nil
}

func validPersistenceBinding(binding PersistenceImportBinding) error {
	if err := validReceiptBinding(ReceiptImportBinding{Reference: binding.Reference, Path: binding.Path, ScopeID: binding.ScopeID, RunID: binding.RunID}, constants.ReceiptPersistenceReferencePrefix); err != nil {
		return fmt.Errorf("persistence: %w", err)
	}
	return validReceiptBinding(ReceiptImportBinding{Reference: binding.ReceiptReference, Path: binding.ReceiptPath, ScopeID: binding.ScopeID, RunID: binding.RunID}, constants.EventReceiptReferencePrefix)
}
