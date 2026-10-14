// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// conformanceGeneratedAt is the bundle generation time the conformance tests use.
var conformanceGeneratedAt = time.Unix(1_700_000_000, 0).UTC()

// conformanceAdmissionID is the protected admission that binds the conformance
// run in the conformance tests.
const conformanceAdmissionID = "conformance-admission-1"

// conformanceReplayBodies returns the bundle bodies of a complete conformance
// source for runID: the protected scope admitting the run, the bundled runtime
// copy of the canonical record and its digest sidecar, and the source
// verification report. Replay tests read the record and sidecar; gate tests
// read all of them.
func conformanceReplayBodies(t *testing.T, runID string) map[string][]byte {
	t.Helper()
	record, err := evalv1.MarshalCanonical(&evalv1.ConformanceRunRecord{RunId: runID, SuiteId: cases.SuiteID, SuiteVersion: cases.SuiteVersion})
	require.NoError(t, err)
	digest := sha256.Sum256(record)
	scope, err := compliancev1.MarshalCanonical(&compliancev1.AssessmentScope{
		AssessmentAsOf:   timestamppb.New(conformanceGeneratedAt),
		SourceAdmissions: []*compliancev1.AssessmentSourceAdmission{{AdmissionId: conformanceAdmissionID, RunId: runID, SourceKind: constants.EvaluationSourceKindConformance}},
	})
	require.NoError(t, err)
	check := evidence.NewVerificationCheckResult(constants.ConformanceVerificationCheck, constants.ConformanceVerifierID, constants.ConformanceVerifierVersion, []string{runID}, nil)
	verification, err := compliancev1.MarshalCanonical(&compliancev1.ComplianceVerificationReport{
		ReportId:        runID,
		Valid:           true,
		VerifiedAt:      timestamppb.New(conformanceGeneratedAt.Add(-time.Minute)),
		VerifierId:      constants.ConformanceVerifierID,
		VerifierVersion: constants.ConformanceVerifierVersion,
		Checks:          []*compliancev1.VerificationCheckResult{check},
	})
	require.NoError(t, err)
	runtimeDir := path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceRuntimeDirname)
	return map[string][]byte{
		constants.ComplianceBundleScopeFilename:                          scope,
		conformanceVerificationBundlePath(runID):                         verification,
		path.Join(runtimeDir, constants.ConformanceRecordFilename):       record,
		path.Join(runtimeDir, constants.ConformanceRecordDigestFilename): []byte(hex.EncodeToString(digest[:])),
	}
}

// conformanceSourceVerifier returns a bundle verifier whose manifest and analysis
// cover the conformance source for runID, so the inventory gate runs over bodies.
func conformanceSourceVerifier(bodies map[string][]byte, runID string) *bundleVerifier {
	v := conformanceReplayVerifier(bodies)
	v.request.Bundle.Manifest = &compliancev1.ComplianceReportManifest{GeneratedAt: timestamppb.New(conformanceGeneratedAt)}
	v.request.Bundle.Analysis = &compliancev1.ComplianceAnalysis{EvidenceResources: []*compliancev1.ComplianceEvidenceReference{{
		ArtifactId:        string(evidence.ArtifactTypeEvalManifest) + ":" + runID,
		ArtifactType:      string(evidence.ArtifactTypeEvalManifest),
		RunId:             runID,
		SourceAdmissionId: conformanceAdmissionID,
	}}}
	return v
}

func conformanceReplayVerifier(bodies map[string][]byte) *bundleVerifier {
	return &bundleVerifier{
		request:                  BundleVerificationRequest{Bundle: &compliancev1.ComplianceReportBundle{}},
		report:                   &compliancev1.ComplianceVerificationReport{},
		bodies:                   bodies,
		replayedNodesByAdmission: map[string][]evidence.EvidenceNode{},
	}
}

func conformanceVerificationBundlePath(runID string) string {
	return path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceVerificationFilename)
}

func TestBundleVerifierConformanceReplayFailsWhenRecordByteIsFlipped(t *testing.T) {
	const runID = "conformance-replay-flipped"
	bodies := conformanceReplayBodies(t, runID)
	recordPath := path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceRuntimeDirname, constants.ConformanceRecordFilename)
	flipped := append([]byte(nil), bodies[recordPath]...)
	flipped[len(flipped)/2] ^= 0x01
	bodies[recordPath] = flipped
	v := conformanceReplayVerifier(bodies)

	v.replayEvalSourceVerification(context.Background(), runID, &compliancev1.ComplianceVerificationReport{ReportId: runID})

	assertVerificationFailure(t, v.report, constants.ErrEvalRunVerificationFailed, conformanceVerificationBundlePath(runID))
	require.Empty(t, v.replayedNodesByAdmission)
}

func TestBundleVerifierConformanceReplayFailsWhenReportNamesAnotherRun(t *testing.T) {
	const runID = "conformance-replay-other"
	v := conformanceReplayVerifier(conformanceReplayBodies(t, runID))

	v.replayEvalSourceVerification(context.Background(), runID, &compliancev1.ComplianceVerificationReport{ReportId: "other-run"})

	assertVerificationFailure(t, v.report, constants.ErrEvalRunVerificationFailed, conformanceVerificationBundlePath(runID))
	require.Empty(t, v.replayedNodesByAdmission)
}

func TestBundleVerifierConformanceReplayFailsWithoutDigestSidecar(t *testing.T) {
	const runID = "conformance-replay-no-sidecar"
	bodies := conformanceReplayBodies(t, runID)
	delete(bodies, path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceRuntimeDirname, constants.ConformanceRecordDigestFilename))
	v := conformanceReplayVerifier(bodies)

	v.replayEvalSourceVerification(context.Background(), runID, &compliancev1.ComplianceVerificationReport{ReportId: runID})

	assertVerificationFailure(t, v.report, constants.ErrEvalRunVerificationFailed, conformanceVerificationBundlePath(runID))
}

// TestBundleVerifierConformanceSourceClearsInventoryGate proves a complete
// conformance source is admitted by the bundle inventory gate and reaches
// replay. The synthetic record carries no posture, so replay rejects it as not
// a PASS; that replay failure is the only one.
func TestBundleVerifierConformanceSourceClearsInventoryGate(t *testing.T) {
	const runID = "conformance-gate-admitted"
	v := conformanceSourceVerifier(conformanceReplayBodies(t, runID), runID)

	v.verifyEvalSourceVerificationReports(context.Background())

	require.Len(t, v.report.GetFailures(), 1)
	assertVerificationFailure(t, v.report, constants.ErrEvalRunVerificationFailed, conformanceVerificationBundlePath(runID))
}

// TestBundleVerifierConformanceSourceInventoryGateFailsClosed mutates a complete
// conformance source. A missing record, sidecar or verification report fails the
// inventory gate. Any other runtime path fails as an unexpected artifact, even
// when the record and sidecar are present. A flipped record byte passes the
// inventory gate and fails at replay.
func TestBundleVerifierConformanceSourceInventoryGateFailsClosed(t *testing.T) {
	const runID = "conformance-gate-negative"
	runtimeDir := path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceRuntimeDirname)
	recordPath := path.Join(runtimeDir, constants.ConformanceRecordFilename)
	digestPath := path.Join(runtimeDir, constants.ConformanceRecordDigestFilename)
	legacyReportPath := path.Join(runtimeDir, "report.json")
	legacyEvidencePath := path.Join(runtimeDir, "evidence", "receipt.json")
	nestedRecordPath := path.Join(runtimeDir, "archive", constants.ConformanceRecordFilename)
	tests := []struct {
		name    string
		mutate  func(bodies map[string][]byte)
		code    error
		subject string
	}{
		{
			name:    "missing digest sidecar",
			mutate:  func(bodies map[string][]byte) { delete(bodies, digestPath) },
			code:    constants.ErrEvalRunVerificationFailed,
			subject: runID,
		},
		{
			name:    "missing conformance record",
			mutate:  func(bodies map[string][]byte) { delete(bodies, recordPath) },
			code:    constants.ErrEvalRunVerificationFailed,
			subject: runID,
		},
		{
			name:    "missing verification report",
			mutate:  func(bodies map[string][]byte) { delete(bodies, conformanceVerificationBundlePath(runID)) },
			code:    constants.ErrEvalRunVerificationFailed,
			subject: runID,
		},
		{
			name:    "stray legacy report.json",
			mutate:  func(bodies map[string][]byte) { bodies[legacyReportPath] = []byte(`{}`) },
			code:    constants.ErrUnexpectedEvidenceArtifact,
			subject: legacyReportPath,
		},
		{
			name:    "legacy evidence tree",
			mutate:  func(bodies map[string][]byte) { bodies[legacyEvidencePath] = []byte(`{}`) },
			code:    constants.ErrUnexpectedEvidenceArtifact,
			subject: legacyEvidencePath,
		},
		{
			name: "record copy below a runtime subdirectory",
			mutate: func(bodies map[string][]byte) {
				bodies[nestedRecordPath] = bodies[recordPath]
			},
			code:    constants.ErrUnexpectedEvidenceArtifact,
			subject: nestedRecordPath,
		},
		{
			name: "flipped record byte",
			mutate: func(bodies map[string][]byte) {
				flipped := append([]byte(nil), bodies[recordPath]...)
				flipped[len(flipped)/2] ^= 0x01
				bodies[recordPath] = flipped
			},
			code:    constants.ErrEvalRunVerificationFailed,
			subject: conformanceVerificationBundlePath(runID),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bodies := conformanceReplayBodies(t, runID)
			test.mutate(bodies)
			v := conformanceSourceVerifier(bodies, runID)

			v.verifyEvalSourceVerificationReports(context.Background())

			assertVerificationFailure(t, v.report, test.code, test.subject)
		})
	}
}

// TestBundleVerifierConformanceRuntimeArtifactRejectsDuplicates proves a second
// record or digest sidecar in one conformance source fails with
// ErrEvidenceDuplicateID. A bundle body map cannot hold two copies of one path,
// so the check is exercised on the inventory directly.
func TestBundleVerifierConformanceRuntimeArtifactRejectsDuplicates(t *testing.T) {
	const runID = "conformance-gate-duplicate"
	tests := []struct {
		name string
		file string
	}{
		{name: "duplicate conformance record", file: constants.ConformanceRecordFilename},
		{name: "duplicate digest sidecar", file: constants.ConformanceRecordDigestFilename},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundlePath := path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceRuntimeDirname, test.file)
			parts := strings.Split(bundlePath, "/")
			v := conformanceReplayVerifier(map[string][]byte{})
			inventory := &evalSourceInventory{}

			v.markConformanceRuntimeArtifact(bundlePath, parts, inventory)
			require.Empty(t, v.report.GetFailures())
			v.markConformanceRuntimeArtifact(bundlePath, parts, inventory)

			assertVerificationFailure(t, v.report, constants.ErrEvidenceDuplicateID, bundlePath)
		})
	}
}
