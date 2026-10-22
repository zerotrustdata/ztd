// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package report

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/compliance/catalog"
	"github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/conformancetest"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// conformanceGeneratedAt is the bundle generation time the conformance tests use.
var conformanceGeneratedAt = time.Unix(1_700_000_000, 0).UTC()

// conformanceAdmissionID is the protected admission that binds the conformance
// run in the conformance tests.
const conformanceAdmissionID = "conformance-admission-1"

// conformanceReplayBodies returns the bundle bodies of a complete conformance
// source for runID: the protected scope admitting the run, plus the source
// bodies of record. Replay tests read the record and sidecar; gate tests read
// all of them.
func conformanceReplayBodies(t *testing.T, runID string, record []byte) map[string][]byte {
	t.Helper()
	scope, err := compliancev1.MarshalCanonical(&compliancev1.AssessmentScope{
		AssessmentAsOf:   timestamppb.New(conformanceGeneratedAt),
		SourceAdmissions: []*compliancev1.AssessmentSourceAdmission{{AdmissionId: conformanceAdmissionID, RunId: runID, SourceKind: constants.EvaluationSourceKindConformance}},
	})
	require.NoError(t, err)
	bodies := conformanceSourceBodies(t, runID, record, conformanceGeneratedAt)
	bodies[constants.ComplianceBundleScopeFilename] = scope
	return bodies
}

// conformanceSourceBodies returns the runtime and verification bodies of a
// conformance source for runID: the bundled copy of record, its digest sidecar,
// and the source verification report. Replay reproduces the report at the
// protected assessment time, so verifiedAt must be that time.
func conformanceSourceBodies(t *testing.T, runID string, record []byte, verifiedAt time.Time) map[string][]byte {
	t.Helper()
	digest := sha256.Sum256(record)
	check := evidence.NewVerificationCheckResult(constants.ConformanceVerificationCheck, constants.ConformanceVerifierID, constants.ConformanceVerifierVersion, []string{runID}, nil)
	verification, err := compliancev1.MarshalCanonical(&compliancev1.ComplianceVerificationReport{
		ReportId:        runID,
		Valid:           true,
		VerifiedAt:      timestamppb.New(verifiedAt),
		VerifierId:      constants.ConformanceVerifierID,
		VerifierVersion: constants.ConformanceVerifierVersion,
		Checks:          []*compliancev1.VerificationCheckResult{check},
	})
	require.NoError(t, err)
	runtimeDir := path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceRuntimeDirname)
	return map[string][]byte{
		conformanceVerificationBundlePath(runID):                         verification,
		path.Join(runtimeDir, constants.ConformanceRecordFilename):       record,
		path.Join(runtimeDir, constants.ConformanceRecordDigestFilename): []byte(hex.EncodeToString(digest[:])),
	}
}

// syntheticConformanceRecord is a record with no posture and no cases. Replay
// rejects it as not a PASS.
func syntheticConformanceRecord(t *testing.T, runID string) []byte {
	t.Helper()
	record, err := evalv1.MarshalCanonical(&evalv1.ConformanceRunRecord{RunId: runID, SuiteId: cases.SuiteID, SuiteVersion: cases.SuiteVersion})
	require.NoError(t, err)
	return record
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
	bodies := conformanceReplayBodies(t, runID, syntheticConformanceRecord(t, runID))
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
	v := conformanceReplayVerifier(conformanceReplayBodies(t, runID, syntheticConformanceRecord(t, runID)))

	v.replayEvalSourceVerification(context.Background(), runID, &compliancev1.ComplianceVerificationReport{ReportId: "other-run"})

	assertVerificationFailure(t, v.report, constants.ErrEvalRunVerificationFailed, conformanceVerificationBundlePath(runID))
	require.Empty(t, v.replayedNodesByAdmission)
}

func TestBundleVerifierConformanceReplayFailsWithoutDigestSidecar(t *testing.T) {
	const runID = "conformance-replay-no-sidecar"
	bodies := conformanceReplayBodies(t, runID, syntheticConformanceRecord(t, runID))
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
	v := conformanceSourceVerifier(conformanceReplayBodies(t, runID, syntheticConformanceRecord(t, runID)), runID)

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
	legacyEvidencePath := path.Join(runtimeDir, "evidence", "event_receipt.json")
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
			bodies := conformanceReplayBodies(t, runID, syntheticConformanceRecord(t, runID))
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

// pinnedConformanceRecord returns the bytes of the pinned M4 doctrine record and
// the run it belongs to.
func pinnedConformanceRecord(t *testing.T) ([]byte, string) {
	t.Helper()
	raw, _ := conformancetest.DoctrineRecord(t)
	record := &evalv1.ConformanceRunRecord{}
	require.NoError(t, evalv1.UnmarshalCanonical(raw, record))
	return raw, record.GetRunId()
}

// conformanceImportedNodes returns the nodes the eval importer replays from the
// bundled runtime bodies of runID, verified at verifiedAt. The reader maps the
// run's runtime root onto the bundled copy, as the bundle replay does.
func conformanceImportedNodes(t *testing.T, bodies map[string][]byte, runID string, verifiedAt time.Time) []evidence.EvidenceNode {
	t.Helper()
	reader := &bundledRuntimeArtifactReader{
		bodies:      bodies,
		runID:       runID,
		sourceDir:   constants.ComplianceBundleSourceEvalsDirname,
		runtimeRoot: filepath.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, runID),
	}
	nodes, err := evaluation.NewEvidenceImporter(reader, runID, func() time.Time { return verifiedAt }).Import(context.Background())
	require.NoError(t, err)
	return nodes
}

// conformanceSignedBundleFixture assembles and signs a complete bundle whose only
// evidence source is the conformance source for record. The protected scope
// admits the run, and the analysis is generated from the nodes the eval importer
// replays from the source bodies. mutateSource, if set, changes the source bodies
// after generation, so the bundle can carry bytes the analysis was not built from.
func conformanceSignedBundleFixture(t *testing.T, record []byte, runID string, mutateSource func(bodies map[string][]byte)) (*compliancev1.ComplianceReportBundle, *bundleArtifactReaderStub, *compliancev1.ComplianceReportTrustPolicy, time.Time) {
	t.Helper()
	run := &evalv1.ConformanceRunRecord{}
	require.NoError(t, evalv1.UnmarshalCanonical(record, run))
	// The bundle is assessed a minute after the run finished. The window opens an
	// hour before the run started, so the run's evidence is fresh in the window.
	generatedAt := run.GetFinishedAt().AsTime().Add(time.Minute)
	request, _ := bundleAssemblyFixture(t)
	request.GeneratedAt = generatedAt
	scope := validGenerationScope(run.GetStartedAt().AsTime().Add(-time.Hour), generatedAt)
	scope.ScopeId = evidence.EvalScopeID(cases.SuiteID)
	scope.AssessmentAsOf = timestamppb.New(generatedAt)
	request.ScopeRef = scope.GetScopeId()
	admission := &compliancev1.AssessmentSourceAdmission{
		AdmissionId:              conformanceAdmissionID,
		SourceKind:               constants.EvaluationSourceKindConformance,
		SourceVersion:            cases.SuiteVersion,
		SourceScopeId:            scope.GetScopeId(),
		OwnerRuntimeBoundary:     "gateway-1",
		AcquisitionBoundary:      "gateway-local-export",
		RunId:                    runID,
		VerifierRef:              &compliancev1.VersionedReference{Id: constants.ConformanceVerifierID, Version: constants.ConformanceVerifierVersion},
		DisclosureClassification: constants.ComplianceBundleProfileRestricted,
	}
	scope.SourceAdmissions = []*compliancev1.AssessmentSourceAdmission{admission}

	bodies := conformanceSourceBodies(t, runID, record, generatedAt)
	nodes := conformanceImportedNodes(t, bodies, runID, generatedAt)
	for index := range nodes {
		nodes[index].SourceAdmissionID = admission.GetAdmissionId()
	}
	assertions, frameworks, crosswalks, err := catalog.LoadCanonicalCatalogs()
	require.NoError(t, err)
	generation := GenerationRequest{
		Scope: scope,
		Sources: []GenerationSource{{
			AdmissionID: admission.GetAdmissionId(),
			Importer:    protectedDecisionImporter{admissionID: admission.GetAdmissionId(), nodes: nodes},
		}},
		Assertions: assertions,
		Frameworks: frameworks,
		Crosswalks: crosswalks,
	}
	generated, err := GenerateComplianceAnalysis(context.Background(), generation)
	require.NoError(t, err)
	request.Analysis = generated.Analysis
	request.Profiles = generated.Profiles
	request.FrameworkRefs = request.FrameworkRefs[:0]
	for _, framework := range frameworks.GetFrameworks() {
		request.FrameworkRefs = append(request.FrameworkRefs, &compliancev1.VersionedReference{Id: framework.GetFrameworkId(), Version: framework.GetFrameworkVersion()})
	}
	request.RenderedFormats, err = renderAllFormats(request.Analysis, request.Profile)
	require.NoError(t, err)
	controlAssessmentRef := path.Join(constants.ComplianceBundleAssessmentsDirname, constants.ComplianceBundleControlAssessmentsFilename)
	request.AssessmentRefs = append(request.AssessmentRefs, controlAssessmentRef)
	request.SourceArtifacts, err = canonicalReportSourceArtifacts(generation, generated)
	require.NoError(t, err)

	if mutateSource != nil {
		mutateSource(bodies)
	}
	bundlePaths := make([]string, 0, len(bodies))
	for bundlePath := range bodies {
		bundlePaths = append(bundlePaths, bundlePath)
	}
	sort.Strings(bundlePaths)
	for _, bundlePath := range bundlePaths {
		request.SourceArtifacts = append(request.SourceArtifacts, SourceArtifact{BundlePath: bundlePath, Body: bodies[bundlePath], MediaType: conformanceMediaType(bundlePath)})
	}

	result, err := AssembleBundle(request)
	require.NoError(t, err)
	identity := bundleSigningIdentityAtFixture(t, generatedAt)
	require.NoError(t, SignBundle(result, identity))
	artifactBodies := make(map[string][]byte, len(result.ArtifactBodies))
	for _, artifact := range result.ArtifactBodies {
		artifactBodies[artifact.BundlePath] = append([]byte(nil), artifact.Body...)
	}
	publicKey := identity.privateKey.Public().(ed25519.PublicKey)
	policy := &compliancev1.ComplianceReportTrustPolicy{
		PolicyId:      "policy-1",
		PolicyVersion: "1.0.0",
		TrustedKeys: []*compliancev1.ComplianceReportTrustedKey{{
			Metadata:         identity.metadata,
			PublicKey:        hex.EncodeToString(publicKey),
			AssessmentId:     "assessment-1",
			AssessorIdentity: "assessor-1",
			AssessedAt:       timestamppb.New(request.GeneratedAt),
			AllowedScopeRefs: []string{request.ScopeRef},
		}},
	}
	return result.Bundle, &bundleArtifactReaderStub{bodies: artifactBodies}, policy, request.GeneratedAt.Add(time.Hour)
}

// conformanceMediaType is the media type of a conformance source body: the
// digest sidecar is hex text, everything else is canonical JSON.
func conformanceMediaType(bundlePath string) string {
	if path.Base(bundlePath) == constants.ConformanceRecordDigestFilename {
		return constants.MediaTypeText
	}
	return constants.MediaTypeJSON
}

// TestVerifyComplianceReportBundle_AcceptsPinnedDoctrineRecordConformanceSource
// proves the pinned M4 doctrine record passes the whole bundle verification
// offline: the source gate admits its bundled copy, replay verifies it to PASS,
// and the importer reproduces the protected analysis.
func TestVerifyComplianceReportBundle_AcceptsPinnedDoctrineRecordConformanceSource(t *testing.T) {
	raw, runID := pinnedConformanceRecord(t)
	bundle, reader, policy, verifiedAt := conformanceSignedBundleFixture(t, raw, runID, nil)

	report, err := VerifyComplianceReportBundle(context.Background(), BundleVerificationRequest{Bundle: bundle, Reader: reader, TrustPolicy: policy, VerifiedAt: verifiedAt})

	require.NoError(t, err)
	assert.True(t, report.GetValid())
	assert.Empty(t, report.GetFailures())
	for _, check := range report.GetChecks() {
		assert.Equal(t, compliancev1.VerificationCheckStatus_VERIFICATION_CHECK_STATUS_PASSED, check.GetStatus(), check.GetCheckId())
	}
}

// TestVerifyComplianceReportBundle_RejectsPinnedDoctrineRecordWithFlippedByte
// proves one flipped byte in the bundled record fails the conformance source at
// replay, against the same bundle that otherwise passes.
func TestVerifyComplianceReportBundle_RejectsPinnedDoctrineRecordWithFlippedByte(t *testing.T) {
	raw, runID := pinnedConformanceRecord(t)
	recordPath := path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, runID, constants.ComplianceBundleSourceRuntimeDirname, constants.ConformanceRecordFilename)
	bundle, reader, policy, verifiedAt := conformanceSignedBundleFixture(t, raw, runID, func(bodies map[string][]byte) {
		flipped := append([]byte(nil), bodies[recordPath]...)
		flipped[len(flipped)/2] ^= 0x01
		bodies[recordPath] = flipped
	})

	report, err := VerifyComplianceReportBundle(context.Background(), BundleVerificationRequest{Bundle: bundle, Reader: reader, TrustPolicy: policy, VerifiedAt: verifiedAt})

	require.NoError(t, err)
	assert.False(t, report.GetValid())
	assertVerificationFailure(t, report, constants.ErrEvalRunVerificationFailed, conformanceVerificationBundlePath(runID))
}
