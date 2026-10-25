// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

var conformanceTestVerifiedAt = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// stubConformanceVerifier returns a verdict set for runID with the given suite
// verdict, in place of the catalogue-driven verify.VerifyPersisted.
func stubConformanceVerifier(runID string, suite evalv1.ConformanceSuiteVerdict) conformanceVerifier {
	return func([]byte, string) (*evalv1.ConformanceVerdictSet, error) {
		return &evalv1.ConformanceVerdictSet{RunId: runID, SuiteVerdict: suite}, nil
	}
}

// conformanceTamperReader serves replacement bytes, or an error, for one path
// and delegates every other read to the wrapped reader.
type conformanceTamperReader struct {
	complianceevidence.ArtifactReader
	path string
	body []byte
	err  error
}

func (r *conformanceTamperReader) ReadFile(ctx context.Context, sourcePath string) ([]byte, error) {
	if sourcePath != r.path {
		return r.ArtifactReader.ReadFile(ctx, sourcePath)
	}
	if r.err != nil {
		return nil, r.err
	}
	return append([]byte(nil), r.body...), nil
}

// saveConformanceRunForCompliance persists a conformance record for runID and
// returns the file service that holds it.
func saveConformanceRunForCompliance(t *testing.T, runID string) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	record := newConformanceRecord(runID)
	record.FinishedAt = timestamppb.New(conformanceTestVerifiedAt.Add(-time.Minute))
	require.NoError(t, NewStore(files).SaveConformanceRecord(context.Background(), record))
	return files
}

func conformanceRecordPath(runID string) string {
	return path.Join(evaluationRunDir(runID), constants.ConformanceRecordFilename)
}

func conformanceDigestPath(runID string) string {
	return path.Join(evaluationRunDir(runID), constants.ConformanceRecordDigestFilename)
}

func TestConformanceComplianceAdmitsPassingRun(t *testing.T) {
	ctx := context.Background()
	const runID = "conformance-pass"
	files := saveConformanceRunForCompliance(t, runID)

	report, set, err := verifyConformanceSourceWith(ctx, files, runID, conformanceTestVerifiedAt, stubConformanceVerifier(runID, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS))
	require.NoError(t, err)
	assert.True(t, report.GetValid())
	assert.Empty(t, report.GetFailures())
	assert.Equal(t, runID, report.GetReportId())
	assert.Equal(t, constants.ConformanceVerifierID, report.GetVerifierId())
	assert.Equal(t, constants.ConformanceVerifierVersion, report.GetVerifierVersion())
	assert.True(t, report.GetVerifiedAt().AsTime().Equal(conformanceTestVerifiedAt))
	require.Len(t, report.GetChecks(), 1)
	assert.Equal(t, constants.ConformanceVerificationCheck, report.GetChecks()[0].GetCheckId())
	assert.Equal(t, []string{runID}, report.GetChecks()[0].GetEvidenceRefs())
	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS, set.GetSuiteVerdict())

	body, err := compliancev1.MarshalCanonical(report)
	require.NoError(t, err)
	validated, err := complianceevidence.ValidateVerificationReport(body, runID, constants.ConformanceVerifierID, constants.ConformanceVerifierVersion, conformanceTestVerifiedAt.Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, validated.GetValid())
}

func TestConformanceComplianceRejectsNonPassingVerdicts(t *testing.T) {
	const runID = "conformance-verdict"
	tests := []struct {
		name  string
		suite evalv1.ConformanceSuiteVerdict
	}{
		{name: "incomplete is not admitted", suite: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE},
		{name: "fail is not admitted", suite: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_FAIL},
		{name: "invalid evidence is not admitted", suite: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := saveConformanceRunForCompliance(t, runID)
			report, _, err := verifyConformanceSourceWith(context.Background(), files, runID, conformanceTestVerifiedAt, stubConformanceVerifier(runID, test.suite))
			require.NoError(t, err)
			assert.False(t, report.GetValid())
			require.Len(t, report.GetFailures(), 1)
			assert.Equal(t, test.suite.String(), report.GetFailures()[0].GetCode())
			require.Len(t, report.GetChecks(), 1)
			assert.Equal(t, report.GetFailures(), report.GetChecks()[0].GetFailures())
		})
	}
}

func TestConformanceComplianceRejectsRecordBelongingToAnotherRun(t *testing.T) {
	const runID = "conformance-scope"
	files := saveConformanceRunForCompliance(t, runID)
	report, _, err := verifyConformanceSourceWith(context.Background(), files, runID, conformanceTestVerifiedAt, stubConformanceVerifier("other-run", evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS))
	require.NoError(t, err)
	assert.False(t, report.GetValid())
	require.Len(t, report.GetFailures(), 1)
	assert.Equal(t, constants.ErrEvidenceScopeMismatch.Error(), report.GetFailures()[0].GetCode())
}

func TestConformanceComplianceRealVerifierRejectsFlippedRecordByte(t *testing.T) {
	ctx := context.Background()
	const runID = "conformance-flipped"
	files := saveConformanceRunForCompliance(t, runID)
	raw, err := files.ReadFile(ctx, conformanceRecordPath(runID))
	require.NoError(t, err)
	flipped := append([]byte(nil), raw...)
	flipped[len(flipped)/2] ^= 0x01
	tampered := &conformanceTamperReader{ArtifactReader: files, path: conformanceRecordPath(runID), body: flipped}

	report, set, err := VerifyConformanceSource(ctx, tampered, runID, conformanceTestVerifiedAt)
	require.NoError(t, err)
	assert.False(t, report.GetValid())
	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, set.GetSuiteVerdict())
	require.Len(t, report.GetFailures(), 1)
	assert.Contains(t, report.GetFailures()[0].GetCode(), "INVALID_EVIDENCE", report.GetFailures()[0].GetCode())
}

func TestConformanceComplianceRejectsMissingDigestSidecar(t *testing.T) {
	ctx := context.Background()
	const runID = "conformance-no-sidecar"
	files := saveConformanceRunForCompliance(t, runID)
	missing := &conformanceTamperReader{ArtifactReader: files, path: conformanceDigestPath(runID), err: constants.ErrNotFound}

	_, _, err := VerifyConformanceSource(ctx, missing, runID, conformanceTestVerifiedAt)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestConformanceComplianceRejectsMissingVerificationTime(t *testing.T) {
	const runID = "conformance-no-time"
	files := saveConformanceRunForCompliance(t, runID)
	_, _, err := verifyConformanceSourceWith(context.Background(), files, runID, time.Time{}, stubConformanceVerifier(runID, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
}
