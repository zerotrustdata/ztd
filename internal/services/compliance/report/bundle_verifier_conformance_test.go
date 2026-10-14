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

// conformanceReplayBodies returns the bundle bodies a conformance source for
// runID needs for replay: the protected scope, and the bundled runtime copy of
// the canonical record and its digest sidecar.
func conformanceReplayBodies(t *testing.T, runID string) map[string][]byte {
	t.Helper()
	record, err := evalv1.MarshalCanonical(&evalv1.ConformanceRunRecord{RunId: runID, SuiteId: cases.SuiteID, SuiteVersion: cases.SuiteVersion})
	require.NoError(t, err)
	digest := sha256.Sum256(record)
	scope, err := compliancev1.MarshalCanonical(&compliancev1.AssessmentScope{AssessmentAsOf: timestamppb.New(time.Unix(1_700_000_000, 0).UTC())})
	require.NoError(t, err)
	runtimeDir := path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceRuntimeDirname)
	return map[string][]byte{
		constants.ComplianceBundleScopeFilename:                          scope,
		path.Join(runtimeDir, constants.ConformanceRecordFilename):       record,
		path.Join(runtimeDir, constants.ConformanceRecordDigestFilename): []byte(hex.EncodeToString(digest[:])),
	}
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
