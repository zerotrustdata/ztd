// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

func TestCanonicalProtoBodyEqual_ComparesAgainstCanonicalProtocolBytes(t *testing.T) {
	message := &compliancev1.ComplianceVerificationReport{ReportId: "report-1", Valid: true}
	body, err := compliancev1.MarshalCanonical(message)
	require.NoError(t, err)

	equal, err := CanonicalProtoBodyEqual(body, message)
	require.NoError(t, err)
	assert.True(t, equal)
	equal, err = CanonicalProtoBodyEqual(append(body, ' '), message)
	require.NoError(t, err)
	assert.False(t, equal)
}

func TestCanonicalProtosEqual_PreservesRepeatedFieldOrder(t *testing.T) {
	left := &compliancev1.ComplianceVerificationReport{ReportId: "report-1", Failures: []*compliancev1.VerificationFailure{{Code: "a"}, {Code: "b"}}}
	right := proto.Clone(left).(*compliancev1.ComplianceVerificationReport)

	equal, err := CanonicalProtosEqual(left, right)
	require.NoError(t, err)
	assert.True(t, equal)
	right.Failures[0], right.Failures[1] = right.Failures[1], right.Failures[0]
	equal, err = CanonicalProtosEqual(left, right)
	require.NoError(t, err)
	assert.False(t, equal)
}

func TestNewVerificationCheckResult_EmitsTypedPassedAndFailedChecks(t *testing.T) {
	passed := NewVerificationCheckResult("check-1", "verifier-1", "1.0.0", []string{"b", "a", "a"}, nil)
	assert.Equal(t, compliancev1.VerificationCheckStatus_VERIFICATION_CHECK_STATUS_PASSED, passed.GetStatus())
	assert.Equal(t, []string{"a", "b"}, passed.GetEvidenceRefs())
	failure := &compliancev1.VerificationFailure{Code: "code", SubjectRef: "subject", Reason: "reason"}
	failed := NewVerificationCheckResult("check-1", "verifier-1", "1.0.0", []string{"evidence"}, []*compliancev1.VerificationFailure{failure})
	assert.Equal(t, compliancev1.VerificationCheckStatus_VERIFICATION_CHECK_STATUS_FAILED, failed.GetStatus())
	assert.Equal(t, []string{"evidence", "subject"}, failed.GetEvidenceRefs())
	assert.Equal(t, []*compliancev1.VerificationFailure{failure}, failed.GetFailures())
}

func TestReadAndDigest_ReturnsBytesAndDigest(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	reader := &memoryArtifactReader{files: map[string][]byte{"path": body}}
	result, err := ReadAndDigest(reader, context.Background(), "path", 0)
	require.NoError(t, err)
	assert.Equal(t, body, result.Bytes)
	digest := sha256.Sum256(body)
	assert.Equal(t, hex.EncodeToString(digest[:]), result.SHA256)
}

func TestReadAndDigest_PropagatesReadError(t *testing.T) {
	reader := &memoryArtifactReader{files: map[string][]byte{}}
	_, err := ReadAndDigest(reader, context.Background(), "missing", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestReadAndDigest_EnforcesSizeLimit(t *testing.T) {
	body := []byte("this is more than 5 bytes")
	reader := &memoryArtifactReader{files: map[string][]byte{"path": body}}
	_, err := ReadAndDigest(reader, context.Background(), "path", 5)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactTooLarge)
}

func TestReadAndDigest_DefaultMaxBytesWhenZero(t *testing.T) {
	body := make([]byte, 100)
	reader := &memoryArtifactReader{files: map[string][]byte{"path": body}}
	result, err := ReadAndDigest(reader, context.Background(), "path", 0)
	require.NoError(t, err)
	assert.Equal(t, body, result.Bytes)
}

func TestVerifyDigest_MatchesCorrectDigest(t *testing.T) {
	body := []byte(`{"verified":true}`)
	digest := sha256.Sum256(body)
	assert.True(t, VerifyDigest(body, hex.EncodeToString(digest[:])))
}

func TestVerifyDigest_RejectsIncorrectDigest(t *testing.T) {
	assert.False(t, VerifyDigest([]byte(`{}`), "wrong"))
}

func TestContentReferenceForBody_FormatAndDeterminism(t *testing.T) {
	body := []byte(`{"ref":"test"}`)
	ref := ContentReferenceForBody("event-receipt", body)
	digest := sha256.Sum256(body)
	expected := "event-receipt:sha256:" + hex.EncodeToString(digest[:])
	assert.Equal(t, expected, ref)
}

func TestParseContentReference_ValidReference(t *testing.T) {
	body := []byte(`{}`)
	ref := ContentReferenceForBody("demo-manifest", body)
	prefix, digest, ok := ParseContentReference(ref)
	require.True(t, ok)
	assert.Equal(t, "demo-manifest", prefix)
	assert.Len(t, digest, 64)
}

func TestParseContentReference_InvalidReferences(t *testing.T) {
	emptyDigest := sha256.Sum256(nil)
	tests := []struct {
		name      string
		reference string
	}{
		{"empty", ""},
		{"missing prefix", ":sha256:" + hex.EncodeToString(emptyDigest[:])},
		{"wrong algorithm", "demo:md5:abc"},
		{"uppercase digest", "demo:sha256:" + strings.ToUpper(hex.EncodeToString(emptyDigest[:]))},
		{"too short", "demo:sha256:abc"},
		{"too many parts", "demo:sha256:abc:extra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := ParseContentReference(tt.reference)
			assert.False(t, ok)
		})
	}
}

func TestParseExpectedContentReference_MatchesPrefix(t *testing.T) {
	body := []byte(`{}`)
	ref := ContentReferenceForBody("event-receipt", body)
	prefix, digest, ok := ParseExpectedContentReference(ref, "event-receipt")
	require.True(t, ok)
	assert.Equal(t, "event-receipt", prefix)
	assert.Len(t, digest, 64)
}

func TestParseExpectedContentReference_RejectsWrongPrefix(t *testing.T) {
	body := []byte(`{}`)
	ref := ContentReferenceForBody("event-receipt", body)
	_, _, ok := ParseExpectedContentReference(ref, "demo-manifest")
	assert.False(t, ok)
}

func TestValidateCanonicalJSON_AcceptsCanonicalCompact(t *testing.T) {
	body := []byte(`{"key":"value","num":42}`)
	require.NoError(t, ValidateCanonicalJSON(body))
}

func TestValidateCanonicalJSON_RejectsNonCanonicalWhitespace(t *testing.T) {
	body := []byte(`{"key": "value"}`)
	err := ValidateCanonicalJSON(body)
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	assert.Contains(t, err.Error(), "canonical compact")
}

func TestValidateCanonicalJSON_RejectsTrailingData(t *testing.T) {
	body := []byte(`{"key":"value"}extra`)
	err := ValidateCanonicalJSON(body)
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	assert.Contains(t, err.Error(), "trailing data")
}

func TestValidateCanonicalJSON_RejectsInvalidJSON(t *testing.T) {
	body := []byte(`{invalid}`)
	err := ValidateCanonicalJSON(body)
	require.Error(t, err)
}

func TestValidPathElement_AcceptsSafeElement(t *testing.T) {
	assert.True(t, ValidPathElement("manifest.json"))
	assert.True(t, ValidPathElement("abc123"))
}

func TestValidatePathElement_ReturnsTypedErrorForUnsafeElements(t *testing.T) {
	for _, value := range []string{"", ".", "..", "a/b", "/absolute", "eval-1022-13:30", "run\x00id"} {
		require.ErrorIs(t, ValidatePathElement(value), constants.ErrEvidencePathElementInvalid, value)
	}
	require.NoError(t, ValidatePathElement("eval-1022-1330"))
}

func TestValidPathElement_RejectsUnsafeElements(t *testing.T) {
	assert.False(t, ValidPathElement(""))
	assert.False(t, ValidPathElement("."))
	assert.False(t, ValidPathElement(".."))
	assert.False(t, ValidPathElement("a/b"))
	assert.False(t, ValidPathElement("/absolute"))
	assert.False(t, ValidPathElement("eval-1022-13:30"))
	assert.False(t, ValidPathElement("a\x00b"))
}

func TestValidatePathElement_AdmitsOnlyValidRelativePaths(t *testing.T) {
	for _, value := range []string{"", "..", "a/b", "eval-1022-13:30", "a\x00b"} {
		require.ErrorIs(t, ValidatePathElement(value), constants.ErrEvidencePathElementInvalid, "%q", value)
	}
	for _, value := range []string{"eval-1022-1330", "run-1", "abc123"} {
		require.NoError(t, ValidatePathElement(value))
		require.NoError(t, ValidateRelativePath("runs/"+value+"/evidence"), "an admitted element must form a valid evidence directory")
	}
}

func TestValidateRelativePath_ReportsTypedError(t *testing.T) {
	for _, value := range []string{"", "..", "../escape", "/absolute", "runs/a:b/evidence", "a\x00b"} {
		assert.ErrorIs(t, ValidateRelativePath(value), constants.ErrEvidenceRelativePathInvalid, "%q", value)
	}
}

// unusedFileService panics on any file operation, proving a rejected write
// fails before touching storage.
type unusedFileService struct{ fs.RuntimeFileService }

func TestPersistCanonicalProtoArtifact_ReportsEachUnmetPrecondition(t *testing.T) {
	message := &compliancev1.ComplianceVerificationReport{ReportId: "report-1", Valid: true}
	files := unusedFileService{}
	tests := []struct {
		name         string
		files        fs.RuntimeFileService
		directory    string
		artifactType ArtifactType
		message      proto.Message
		want         error
	}{
		{name: "file service", directory: "evidence", artifactType: ArtifactTypeEvalManifest, message: message, want: constants.ErrMissingRequiredField},
		{name: "message", files: files, directory: "evidence", artifactType: ArtifactTypeEvalManifest, want: constants.ErrMissingRequiredField},
		{name: "artifact type", files: files, directory: "evidence", artifactType: ArtifactType("unknown"), message: message, want: constants.ErrEvidenceArtifactTypeInvalid},
		{name: "directory", files: files, directory: "runs/eval-1022-13:30/evidence", artifactType: ArtifactTypeEvalManifest, message: message, want: constants.ErrEvidenceRelativePathInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PersistCanonicalProtoArtifact(context.Background(), tt.files, tt.directory, tt.artifactType, tt.message)
			require.ErrorIs(t, err, constants.ErrEvidenceArtifactPersistFailed)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestValidRelativePath_AcceptsSafeRelativePath(t *testing.T) {
	assert.True(t, ValidRelativePath("receipts/abc.json"))
	assert.True(t, ValidRelativePath("a/b/c.json"))
}

func TestValidRelativePath_RejectsUnsafePaths(t *testing.T) {
	assert.False(t, ValidRelativePath(""))
	assert.False(t, ValidRelativePath("."))
	assert.False(t, ValidRelativePath(".."))
	assert.False(t, ValidRelativePath("../escape"))
	assert.False(t, ValidRelativePath("/absolute"))
}

func TestClassifyReadError_MapsNotFoundToUnresolvedReference(t *testing.T) {
	assert.ErrorIs(t, ClassifyReadError(constants.ErrNotFound), constants.ErrUnresolvedReference)
}

func TestClassifyReadError_PreservesTooLarge(t *testing.T) {
	assert.ErrorIs(t, ClassifyReadError(constants.ErrEvidenceArtifactTooLarge), constants.ErrEvidenceArtifactTooLarge)
}

func TestClassifyReadError_MapsUnknownToInvalidGraph(t *testing.T) {
	assert.ErrorIs(t, ClassifyReadError(errors.New("unknown")), constants.ErrInvalidEvidenceGraph)
}

func TestValidateVerificationReport_EnforcesCanonicalBoundSuccessfulReport(t *testing.T) {
	verifiedAt := time.Unix(1_700_000_000, 0).UTC()
	valid := &compliancev1.ComplianceVerificationReport{
		ReportId:        "run-1",
		Valid:           true,
		VerifiedAt:      timestamppb.New(verifiedAt),
		VerifierId:      constants.ConformanceVerifierID,
		VerifierVersion: constants.ConformanceVerifierVersion,
		Checks: []*compliancev1.VerificationCheckResult{NewVerificationCheckResult(
			constants.ConformanceVerificationCheck,
			constants.ConformanceVerifierID,
			constants.ConformanceVerifierVersion,
			[]string{"run-1"},
			nil,
		)},
	}
	tests := []struct {
		name      string
		mutate    func(*compliancev1.ComplianceVerificationReport)
		notAfter  time.Time
		targetErr error
	}{
		{name: "valid bound report", notAfter: verifiedAt},
		{name: "wrong report identity", mutate: func(report *compliancev1.ComplianceVerificationReport) { report.ReportId = "run-2" }, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "wrong verifier identity", mutate: func(report *compliancev1.ComplianceVerificationReport) {
			report.VerifierId = constants.EvidenceGraphVerifierID
		}, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "wrong verifier version", mutate: func(report *compliancev1.ComplianceVerificationReport) { report.VerifierVersion = "2.0.0" }, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "invalid report", mutate: func(report *compliancev1.ComplianceVerificationReport) { report.Valid = false }, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "report contains failures", mutate: func(report *compliancev1.ComplianceVerificationReport) {
			report.Failures = []*compliancev1.VerificationFailure{{Code: constants.ErrChecksumMismatch.Error()}}
		}, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "missing verification check", mutate: func(report *compliancev1.ComplianceVerificationReport) { report.Checks = nil }, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "duplicate verification check", mutate: func(report *compliancev1.ComplianceVerificationReport) {
			report.Checks = append(report.Checks, proto.Clone(report.Checks[0]).(*compliancev1.VerificationCheckResult))
		}, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "wrong verification check identity", mutate: func(report *compliancev1.ComplianceVerificationReport) {
			report.Checks[0].CheckId = "unsupported-check"
		}, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "unspecified verification check status", mutate: func(report *compliancev1.ComplianceVerificationReport) {
			report.Checks[0].Status = compliancev1.VerificationCheckStatus_VERIFICATION_CHECK_STATUS_UNSPECIFIED
		}, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "verification check lacks run evidence", mutate: func(report *compliancev1.ComplianceVerificationReport) {
			report.Checks[0].EvidenceRefs = []string{"other-run"}
		}, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "verification check verifier mismatch", mutate: func(report *compliancev1.ComplianceVerificationReport) {
			report.Checks[0].VerifierId = constants.EvidenceGraphVerifierID
		}, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "missing verification time", mutate: func(report *compliancev1.ComplianceVerificationReport) { report.VerifiedAt = nil }, notAfter: verifiedAt, targetErr: constants.ErrReportVerificationFailed},
		{name: "verification after cutoff", notAfter: verifiedAt.Add(-time.Nanosecond), targetErr: constants.ErrReportVerificationFailed},
		{name: "missing cutoff", targetErr: constants.ErrReportVerificationFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := proto.Clone(valid).(*compliancev1.ComplianceVerificationReport)
			if test.mutate != nil {
				test.mutate(report)
			}
			body, err := compliancev1.MarshalCanonical(report)
			require.NoError(t, err)

			decoded, err := ValidateVerificationReport(body, "run-1", constants.ConformanceVerifierID, constants.ConformanceVerifierVersion, test.notAfter)
			if test.targetErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, test.targetErr)
				assert.Nil(t, decoded)
				return
			}
			require.NoError(t, err)
			assert.True(t, proto.Equal(report, decoded))
		})
	}
}

func TestValidateVerificationReport_RejectsNoncanonicalBody(t *testing.T) {
	report, err := ValidateVerificationReport([]byte(`{"report_id": "run-1"}`), "run-1", constants.ConformanceVerifierID, constants.ConformanceVerifierVersion, time.Unix(1_700_000_000, 0).UTC())
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	assert.Nil(t, report)
}
