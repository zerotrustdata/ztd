// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// maxArtifactBytes is the default size limit for a single evidence artifact
// read by an importer. Importers may override this with a larger or smaller
// limit.
const maxArtifactBytes = 16 << 20

// ArtifactReader is the read-only runtime-file capability used by importers and verifiers.
type ArtifactReader interface {
	ReadFile(context.Context, string) ([]byte, error)
	ReadDir(context.Context, string) ([]os.DirEntry, error)
}

// ReadResult holds the bytes and digest of a read artifact.
type ReadResult struct {
	Bytes  []byte
	SHA256 string
}

// ReadAndDigest reads a file from the given reader, enforces the size limit,
// and computes the SHA-256 digest of the bytes.
func ReadAndDigest(reader ArtifactReader, ctx context.Context, path string, maxBytes int) (ReadResult, error) {
	if maxBytes <= 0 {
		maxBytes = maxArtifactBytes
	}
	body, err := reader.ReadFile(ctx, path)
	if err != nil {
		return ReadResult{}, err
	}
	if len(body) > maxBytes {
		return ReadResult{}, constants.ErrEvidenceArtifactTooLarge
	}
	digest := sha256.Sum256(body)
	return ReadResult{Bytes: body, SHA256: hex.EncodeToString(digest[:])}, nil
}

func digestHex(body []byte) string {
	d := sha256.Sum256(body)
	return hex.EncodeToString(d[:])
}

// MarshalCanonicalProto marshals a proto message into canonical JSON bytes.
func MarshalCanonicalProto(msg proto.Message) ([]byte, error) {
	return compliancev1.MarshalCanonical(msg)
}

func CanonicalProtoBodyEqual(body []byte, expected proto.Message) (bool, error) {
	canonical, err := compliancev1.MarshalCanonical(expected)
	if err != nil {
		return false, fmt.Errorf("canonicalize expected protocol message: %w", err)
	}
	return bytes.Equal(body, canonical), nil
}

func CanonicalProtosEqual(left, right proto.Message) (bool, error) {
	leftBody, err := compliancev1.MarshalCanonical(left)
	if err != nil {
		return false, fmt.Errorf("canonicalize left protocol message: %w", err)
	}
	rightBody, err := compliancev1.MarshalCanonical(right)
	if err != nil {
		return false, fmt.Errorf("canonicalize right protocol message: %w", err)
	}
	return bytes.Equal(leftBody, rightBody), nil
}

func NewVerificationCheckResult(checkID, verifierID, verifierVersion string, evidenceRefs []string, failures []*compliancev1.VerificationFailure) *compliancev1.VerificationCheckResult {
	refs := append([]string(nil), evidenceRefs...)
	for _, failure := range failures {
		refs = append(refs, failure.GetSubjectRef())
	}
	sort.Strings(refs)
	uniqueRefs := refs[:0]
	for _, reference := range refs {
		if reference != "" && (len(uniqueRefs) == 0 || uniqueRefs[len(uniqueRefs)-1] != reference) {
			uniqueRefs = append(uniqueRefs, reference)
		}
	}
	status := compliancev1.VerificationCheckStatus_VERIFICATION_CHECK_STATUS_PASSED
	if len(failures) > 0 {
		status = compliancev1.VerificationCheckStatus_VERIFICATION_CHECK_STATUS_FAILED
	}
	return &compliancev1.VerificationCheckResult{CheckId: checkID, Status: status, EvidenceRefs: uniqueRefs, VerifierId: verifierID, VerifierVersion: verifierVersion, Failures: append([]*compliancev1.VerificationFailure(nil), failures...)}
}

// VerifyDigest compares the SHA-256 of the given bytes to the expected
// hex-encoded digest.
func VerifyDigest(body []byte, expected string) bool {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]) == expected
}

// ContentReferenceForBody computes the content-addressed reference for the
// given prefix and body bytes: "<prefix>:sha256:<hex-digest>".
func ContentReferenceForBody(prefix string, body []byte) string {
	digest := sha256.Sum256(body)
	return prefix + ":sha256:" + hex.EncodeToString(digest[:])
}

// ParseContentReference splits a content-addressed reference into its prefix,
// digest, and validity flag. The format is "<prefix>:sha256:<64-hex-chars>".
func ParseContentReference(reference string) (string, string, bool) {
	parts := strings.Split(reference, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "sha256" || len(parts[2]) != sha256.Size*2 || strings.ToLower(parts[2]) != parts[2] {
		return "", "", false
	}
	decoded, err := hex.DecodeString(parts[2])
	return parts[0], parts[2], err == nil && len(decoded) == sha256.Size
}

// ParseExpectedContentReference parses a content reference and verifies the
// prefix matches the expected value.
func ParseExpectedContentReference(reference, expectedPrefix string) (string, string, bool) {
	prefix, digest, ok := ParseContentReference(reference)
	return prefix, digest, ok && prefix == expectedPrefix
}

type CanonicalArtifact struct {
	Reference      *compliancev1.ComplianceEvidenceReference
	RelativePath   string
	CanonicalBytes []byte
}

// ValidateArtifactWrite reports the first unmet precondition of an evidence
// artifact write, each under constants.ErrEvidenceArtifactPersistFailed: a file
// service and a body (constants.ErrMissingRequiredField), and a supported
// artifact type (constants.ErrEvidenceArtifactTypeInvalid).
func ValidateArtifactWrite(fileSvc fs.RuntimeFileService, artifactType ArtifactType, hasBody bool) error {
	switch {
	case fileSvc == nil:
		return fmt.Errorf("%w: file service: %w", constants.ErrEvidenceArtifactPersistFailed, constants.ErrMissingRequiredField)
	case !hasBody:
		return fmt.Errorf("%w: artifact body: %w", constants.ErrEvidenceArtifactPersistFailed, constants.ErrMissingRequiredField)
	case !ContainsArtifactType(SupportedArtifactTypes(), artifactType):
		return fmt.Errorf("%w: %w: %q", constants.ErrEvidenceArtifactPersistFailed, constants.ErrEvidenceArtifactTypeInvalid, artifactType)
	}
	return nil
}

func PersistCanonicalProtoArtifact(ctx context.Context, fileSvc fs.RuntimeFileService, directory string, artifactType ArtifactType, message proto.Message) (*CanonicalArtifact, error) {
	if err := ValidateArtifactWrite(fileSvc, artifactType, message != nil); err != nil {
		return nil, err
	}
	if err := ValidateRelativePath(directory); err != nil {
		return nil, fmt.Errorf("%w: artifact directory: %w", constants.ErrEvidenceArtifactPersistFailed, err)
	}
	body, err := MarshalCanonicalProto(message)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize %s: %w", constants.ErrEvidenceArtifactPersistFailed, artifactType, err)
	}
	artifactID := ContentAddress(artifactType, body)
	_, digest, ok := ParseContentAddress(artifactID)
	if !ok {
		return nil, fmt.Errorf("%w: generated content address is invalid", constants.ErrEvidenceArtifactPersistFailed)
	}
	if err := fileSvc.MkdirAll(ctx, directory, constants.PermDirStandard); err != nil {
		return nil, fmt.Errorf("%w: create artifact directory: %w", constants.ErrEvidenceArtifactPersistFailed, err)
	}
	path := filepath.Join(directory, digest+constants.FileExtJSON)
	if err := fileSvc.WriteFile(ctx, path, body, constants.PermFileReadOnly); err != nil {
		return nil, fmt.Errorf("%w: write %s: %w", constants.ErrEvidenceArtifactPersistFailed, artifactID, err)
	}
	return &CanonicalArtifact{
		Reference: &compliancev1.ComplianceEvidenceReference{
			ArtifactId:   artifactID,
			ArtifactType: string(artifactType),
			Sha256:       digest,
			MediaType:    constants.MediaTypeJSON,
		},
		RelativePath:   path,
		CanonicalBytes: body,
	}, nil
}

func ContainsArtifactType(values []ArtifactType, target ArtifactType) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// ValidateCanonicalJSON verifies that the bytes are valid JSON with no
// trailing data and that the encoding is canonical compact (no extra
// whitespace).
func ValidateCanonicalJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON contains trailing data: %w", constants.ErrEvidenceArtifactMalformed)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return err
	}
	if !bytes.Equal(body, compact.Bytes()) {
		return fmt.Errorf("JSON is not canonical compact encoding: %w", constants.ErrEvidenceArtifactMalformed)
	}
	return nil
}

// unsafePathChars are rejected in every evidence path: a colon names a drive
// or an alternate data stream on Windows, and NUL truncates the path.
const unsafePathChars = ":\x00"

// ValidatePathElement reports constants.ErrEvidencePathElementInvalid unless
// the value is a single safe path component: not empty, no separators, no
// traversal, and none of the characters ValidateRelativePath rejects, so every
// identifier it admits also forms a valid evidence directory.
func ValidatePathElement(value string) error {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value || filepath.IsAbs(value) || strings.ContainsAny(value, unsafePathChars) {
		return fmt.Errorf("%w: %q", constants.ErrEvidencePathElementInvalid, value)
	}
	return nil
}

// ValidPathElement returns true if ValidatePathElement accepts the value.
func ValidPathElement(value string) bool {
	return ValidatePathElement(value) == nil
}

// ValidateRelativePath reports constants.ErrEvidenceRelativePathInvalid unless
// the value is a safe relative path that does not escape the root via
// traversal.
func ValidateRelativePath(value string) error {
	if strings.HasPrefix(value, "/") || strings.ContainsAny(value, unsafePathChars) {
		return fmt.Errorf("%w: %q", constants.ErrEvidenceRelativePathInvalid, value)
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%w: %q", constants.ErrEvidenceRelativePathInvalid, value)
	}
	return nil
}

// ValidRelativePath returns true if ValidateRelativePath accepts the value.
func ValidRelativePath(value string) bool {
	return ValidateRelativePath(value) == nil
}

// ClassifyReadError maps a read error to the appropriate evidence error
// constant.
func ClassifyReadError(err error) error {
	if errors.Is(err, constants.ErrNotFound) {
		return constants.ErrUnresolvedReference
	}
	if errors.Is(err, constants.ErrEvidenceArtifactTooLarge) {
		return constants.ErrEvidenceArtifactTooLarge
	}
	return constants.ErrInvalidEvidenceGraph
}

// Contains returns true if the target string appears in the values slice.
func Contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type ReceiptBinding struct {
	RunID                   string
	ScenarioID              string
	AttemptID               string
	ExecutionID             string
	InvestigationID         string
	TransactionID           string
	TargetOperatorID        string
	TargetOperatorSessionID string
	ActionType              string
}

type ReceiptProjection struct {
	ExecutionID       string
	TransactionID     string
	TransactionHash   string
	InvestigationID   string
	OperatorID        string
	OperatorSessionID string
	ActionType        string
	SignerKeyID       string
	Signature         string
}

type VerifiedReceiptEvidence struct {
	Binding                ReceiptBinding
	ReceiptReference       *compliancev1.ComplianceEvidenceReference
	PersistenceReference   *compliancev1.ComplianceEvidenceReference
	ProtocolChainReference string
	Receipt                *operatorv1.EventReceipt
}

func BuildVerifiedReceiptEvidence(binding ReceiptBinding, projection ReceiptProjection, receipt *operatorv1.EventReceipt) (*VerifiedReceiptEvidence, error) {
	if binding.RunID == "" || binding.ScenarioID == "" || binding.AttemptID == "" || binding.ExecutionID == "" || binding.InvestigationID == "" || binding.TransactionID == "" || binding.TargetOperatorID == "" || binding.TargetOperatorSessionID == "" || binding.ActionType == "" {
		return nil, fmt.Errorf("%w: receipt binding is incomplete", constants.ErrInvalidEvidenceGraph)
	}
	if projection.ExecutionID != binding.ExecutionID || projection.InvestigationID != binding.InvestigationID || projection.TransactionID != binding.TransactionID || projection.OperatorID != binding.TargetOperatorID || projection.OperatorSessionID != binding.TargetOperatorSessionID || projection.ActionType != binding.ActionType || projection.TransactionHash == "" || projection.SignerKeyID == "" || projection.Signature == "" {
		return nil, fmt.Errorf("%w: receipt projection does not match the declared binding", constants.ErrInvalidEvidenceGraph)
	}
	if receipt == nil || receipt.GetTransactionId() != projection.TransactionID || receipt.GetTransactionHash() != projection.TransactionHash || receipt.GetSignerKeyId() != projection.SignerKeyID || receipt.GetSignature() != projection.Signature {
		return nil, fmt.Errorf("%w: canonical receipt does not match the governed response projection", constants.ErrInvalidEvidenceGraph)
	}
	attestation := receipt.GetFinalPersistenceAttestation()
	if attestation == nil || attestation.GetTransactionId() != binding.TransactionID || attestation.GetAuditRecordId() != binding.TransactionID || attestation.GetSignerKeyId() != projection.SignerKeyID || attestation.GetReceiptSignatureDigest() == "" || attestation.GetPersistedAtUnixMs() <= 0 || attestation.GetSignature() == "" {
		return nil, fmt.Errorf("%w: canonical receipt lacks a bound final-persistence attestation", constants.ErrInvalidEvidenceGraph)
	}
	chain, err := governance.ValidateDeterministicProtocolChain(receipt)
	if err != nil {
		return nil, fmt.Errorf("%w: validate deterministic protocol chain: %w", constants.ErrInvalidEvidenceGraph, err)
	}
	bound := false
	for _, stage := range receipt.GetDeterministicStageEvidence() {
		if stage.GetTransactionId() == binding.TransactionID && stage.GetTransactionHash() == projection.TransactionHash && stage.GetOperatorId() == binding.TargetOperatorID && stage.GetOperatorSessionId() == binding.TargetOperatorSessionID && stage.GetActionType() == binding.ActionType && stage.GetCaseId() == binding.RunID && stage.GetInvestigationId() == binding.InvestigationID && stage.GetTaskId() == binding.AttemptID {
			bound = true
			break
		}
	}
	if !bound {
		return nil, fmt.Errorf("%w: canonical receipt lacks the declared run, scenario, attempt, transaction, action, and target binding", constants.ErrEvidenceScopeMismatch)
	}
	receiptBody, err := MarshalCanonicalProto(receipt)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize event receipt: %w", constants.ErrInvalidEvidenceGraph, err)
	}
	persistenceBody, err := MarshalCanonicalProto(attestation)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize receipt persistence attestation: %w", constants.ErrInvalidEvidenceGraph, err)
	}
	receiptID := ContentAddress(ArtifactTypeEventReceipt, receiptBody)
	_, receiptDigest, _ := ParseContentAddress(receiptID)
	persistenceID := ContentAddress(ArtifactTypeReceiptPersistence, persistenceBody)
	_, persistenceDigest, _ := ParseContentAddress(persistenceID)
	return &VerifiedReceiptEvidence{
		Binding: binding,
		ReceiptReference: &compliancev1.ComplianceEvidenceReference{
			ArtifactId: receiptID, ArtifactType: string(ArtifactTypeEventReceipt), Sha256: receiptDigest, MediaType: constants.MediaTypeJSON,
			RunId: binding.RunID, AttemptId: binding.AttemptID, ScenarioId: binding.ScenarioID, TransactionId: binding.TransactionID,
		},
		PersistenceReference: &compliancev1.ComplianceEvidenceReference{
			ArtifactId: persistenceID, ArtifactType: string(ArtifactTypeReceiptPersistence), Sha256: persistenceDigest, MediaType: constants.MediaTypeJSON,
			RunId: binding.RunID, AttemptId: binding.AttemptID, ScenarioId: binding.ScenarioID, TransactionId: binding.TransactionID,
		},
		ProtocolChainReference: chain.ContentReference,
		Receipt:                receipt,
	}, nil
}

func ValidateVerificationReport(body []byte, reportID, verifierID, verifierVersion string, notAfter time.Time) (*compliancev1.ComplianceVerificationReport, error) {
	report := &compliancev1.ComplianceVerificationReport{}
	if err := compliancev1.UnmarshalCanonical(body, report); err != nil {
		return nil, fmt.Errorf("%w: decode canonical verification report: %w", constants.ErrEvidenceArtifactMalformed, err)
	}
	if reportID == "" || verifierID == "" || verifierVersion == "" || notAfter.IsZero() || report.GetReportId() != reportID || report.GetVerifierId() != verifierID || report.GetVerifierVersion() != verifierVersion || !report.GetValid() || len(report.GetFailures()) != 0 || report.GetVerifiedAt() == nil || report.GetVerifiedAt().CheckValid() != nil || report.GetVerifiedAt().AsTime().After(notAfter) {
		return nil, fmt.Errorf("%w: verification report is invalid or does not match its declared binding", constants.ErrReportVerificationFailed)
	}
	var expectedCheckID string
	switch {
	case verifierID == constants.ConformanceVerifierID && verifierVersion == constants.ConformanceVerifierVersion:
		expectedCheckID = constants.ConformanceVerificationCheck
	default:
		return nil, fmt.Errorf("%w: protected source verifier is unsupported", constants.ErrReportVerificationFailed)
	}
	if len(report.GetChecks()) != 1 {
		return nil, fmt.Errorf("%w: protected source verification report requires exactly one check", constants.ErrReportVerificationFailed)
	}
	check := report.GetChecks()[0]
	if check == nil || check.GetCheckId() != expectedCheckID || check.GetStatus() != compliancev1.VerificationCheckStatus_VERIFICATION_CHECK_STATUS_PASSED || check.GetVerifierId() != verifierID || check.GetVerifierVersion() != verifierVersion || len(check.GetEvidenceRefs()) != 1 || check.GetEvidenceRefs()[0] != reportID || len(check.GetFailures()) != 0 {
		return nil, fmt.Errorf("%w: protected source verification check is invalid or does not match its declared binding", constants.ErrReportVerificationFailed)
	}
	return report, nil
}
