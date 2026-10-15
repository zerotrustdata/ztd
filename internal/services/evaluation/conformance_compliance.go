// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/verify"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// readConformanceRecordRaw returns the persisted record bytes and the hex
// digest stored next to them, unparsed.
func readConformanceRecordRaw(ctx context.Context, reader complianceevidence.ArtifactReader, runID string) ([]byte, string, error) {
	if reader == nil || !complianceevidence.ValidPathElement(runID) {
		return nil, "", fmt.Errorf("%w: artifact reader and canonical run ID are required", constants.ErrConformanceRecordInvalid)
	}
	runDir := evaluationRunDir(runID)
	raw, err := reader.ReadFile(ctx, filepath.Join(runDir, constants.ConformanceRecordFilename))
	if err != nil {
		return nil, "", fmt.Errorf("evaluation: read conformance record: %w", err)
	}
	digest, err := reader.ReadFile(ctx, filepath.Join(runDir, constants.ConformanceRecordDigestFilename))
	if err != nil {
		return nil, "", fmt.Errorf("evaluation: read conformance record digest: %w", err)
	}
	return raw, string(digest), nil
}

// conformanceVerifier is verify.VerifyPersisted. The seam lets tests give a
// verdict without building the full catalogue; production always passes
// verify.VerifyPersisted.
type conformanceVerifier func(raw []byte, digestHex string) (*evalv1.ConformanceVerdictSet, error)

// VerifyConformanceSource verifies a persisted conformance run for compliance
// admission. The report is valid only when the suite verdict is PASS.
func VerifyConformanceSource(ctx context.Context, reader complianceevidence.ArtifactReader, runID string, verifiedAt time.Time) (*compliancev1.ComplianceVerificationReport, *evalv1.ConformanceVerdictSet, error) {
	return verifyConformanceSourceWith(ctx, reader, runID, verifiedAt, verify.VerifyPersisted)
}

func verifyConformanceSourceWith(ctx context.Context, reader complianceevidence.ArtifactReader, runID string, verifiedAt time.Time, verifyPersisted conformanceVerifier) (*compliancev1.ComplianceVerificationReport, *evalv1.ConformanceVerdictSet, error) {
	raw, digest, err := readConformanceRecordRaw(ctx, reader, runID)
	if err != nil {
		return nil, nil, err
	}
	return verifyConformanceRecord(raw, digest, runID, verifiedAt, verifyPersisted)
}

// verifyConformanceRecord verifies bytes the caller has already read, so the
// importer verifies and decodes the same bytes.
func verifyConformanceRecord(raw []byte, digest, runID string, verifiedAt time.Time, verifyPersisted conformanceVerifier) (*compliancev1.ComplianceVerificationReport, *evalv1.ConformanceVerdictSet, error) {
	if !complianceevidence.ValidPathElement(runID) || verifiedAt.IsZero() || verifyPersisted == nil {
		return nil, nil, fmt.Errorf("%w: canonical run ID, verification time and verifier are required", constants.ErrInvalidEvidenceGraph)
	}
	set, err := verifyPersisted(raw, digest)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: verify conformance record: %w", err)
	}
	if set == nil {
		return nil, nil, fmt.Errorf("%w: conformance verifier returned no verdict set", constants.ErrConformanceRecordInvalid)
	}
	var failures []*compliancev1.VerificationFailure
	switch {
	case set.GetSuiteVerdict() != evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS:
		failures = append(failures, &compliancev1.VerificationFailure{Code: set.GetSuiteVerdict().String(), SubjectRef: runID, Reason: "conformance suite verdict is not PASS"})
	case set.GetRunId() != runID:
		failures = append(failures, &compliancev1.VerificationFailure{Code: constants.ErrEvidenceScopeMismatch.Error(), SubjectRef: runID, Reason: "conformance record belongs to another run"})
	}
	report := &compliancev1.ComplianceVerificationReport{
		ReportId:        runID,
		VerifiedAt:      timestamppb.New(verifiedAt.UTC()),
		VerifierId:      constants.ConformanceVerifierID,
		VerifierVersion: constants.ConformanceVerifierVersion,
		Failures:        failures,
		Valid:           len(failures) == 0,
	}
	report.Checks = []*compliancev1.VerificationCheckResult{complianceevidence.NewVerificationCheckResult(constants.ConformanceVerificationCheck, constants.ConformanceVerifierID, constants.ConformanceVerifierVersion, []string{runID}, failures)}
	return report, set, nil
}
