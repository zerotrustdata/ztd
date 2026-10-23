// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package compliance

import (
	"encoding/json"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/uuid"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

// oscalTestCatalog returns a minimal valid KSICatalog for OSCAL tests.
func oscalTestCatalog() *KSICatalog {
	return &KSICatalog{
		Version: "test-1.0",
		Source:  "test-source",
		KSIs: []KSI{
			{
				ID:                "KSI-CMT-01",
				Title:             "Logging Changes",
				Category:          KSICategoryCMT,
				ControlRefs:       []string{"AU-2", "CM-3"},
				ApplicableClasses: []CertificationClass{ClassB, ClassC},
				ValidationCycle:   ValidationCycleMachine,
				AutomatedMethods: []AutomatedMethod{
					{Name: "audit_events_check", Description: "Verifies audit events exist"},
					{Name: "ledger_commits_check", Description: "Verifies ledger commits exist"},
				},
			},
			{
				ID:                "KSI-MLA-07",
				Title:             "Non-Repudiation",
				Category:          KSICategoryMLA,
				ControlRefs:       []string{"AU-10"},
				ApplicableClasses: []CertificationClass{ClassC},
				ValidationCycle:   ValidationCycleMachine,
				AutomatedMethods: []AutomatedMethod{
					{Name: "commitment_chain_check", Description: "Verifies commitment chain"},
				},
			},
		},
	}
}

// oscalTestAnalysis returns canonical analysis with satisfied and not-satisfied control assessments.
func oscalTestAnalysis() *compliancev1.ComplianceAnalysis {
	const (
		receiptDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		metricDigest  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	receiptID := "action-receipt:sha256:" + receiptDigest
	metricID := "eval-metric:sha256:" + metricDigest
	generatedAt := time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)
	firstAssessmentID := "assertion-assessment:sha256:" + receiptDigest
	secondAssessmentID := "assertion-assessment:sha256:" + metricDigest
	return &compliancev1.ComplianceAnalysis{
		AnalysisId:            "compliance-analysis:sha256:" + receiptDigest,
		AnalysisSchemaVersion: constants.AnalysisSchemaVersion,
		ScopeRef:              "scope-1",
		GeneratedAt:           timestamppb.New(generatedAt),
		GeneratorIdentity:     constants.AnalysisBuilderID,
		GeneratorVersion:      constants.AnalysisBuilderVersion,
		AssertionAssessments: []*compliancev1.ControlAssertionAssessment{
			{
				AssessmentId: firstAssessmentID, ScopeId: "scope-1", AssertionRef: &compliancev1.VersionedReference{Id: "ZTD-GOV-BLOCK-001", Version: "1.0.0"}, Status: "satisfied", EvidenceLevel: "L3", EvaluatedAt: timestamppb.New(generatedAt), VerifierRef: &compliancev1.VersionedReference{Id: constants.AssertionGraderID, Version: constants.AssertionGraderVersion}, EvidenceRefs: []string{receiptID}, FreshnessStatus: "fresh",
			},
			{
				AssessmentId: secondAssessmentID, ScopeId: "scope-1", AssertionRef: &compliancev1.VersionedReference{Id: "ZTD-CM-STATE-001", Version: "1.0.0"}, Status: "not_satisfied", EvidenceLevel: "L2", EvaluatedAt: timestamppb.New(generatedAt), VerifierRef: &compliancev1.VersionedReference{Id: constants.AssertionGraderID, Version: constants.AssertionGraderVersion}, EvidenceRefs: []string{metricID}, MetricRefs: []string{metricID}, FreshnessStatus: "fresh", FailureReason: "required deterministic grader did not pass",
			},
		},
		FrameworkAssessments: []*compliancev1.FrameworkControlAssessment{
			{AssessmentId: "framework-assessment:sha256:" + receiptDigest, ScopeId: "scope-1", FrameworkRef: &compliancev1.VersionedReference{Id: "fedramp-20x", Version: "CR26-2026-06-24"}, ControlId: "KSI-CMT-01", Status: "satisfied", Responsibility: "platform", AssertionAssessmentRefs: []string{firstAssessmentID}, EvidenceLevel: "L3"},
			{AssessmentId: "framework-assessment:sha256:" + metricDigest, ScopeId: "scope-1", FrameworkRef: &compliancev1.VersionedReference{Id: "fedramp-20x", Version: "CR26-2026-06-24"}, ControlId: "KSI-MLA-07", Status: "not_satisfied", Responsibility: "shared", AssertionAssessmentRefs: []string{secondAssessmentID}, EvidenceLevel: "L2"},
		},
		EvidenceResources: []*compliancev1.ComplianceEvidenceReference{
			{ArtifactId: receiptID, ArtifactType: "action-receipt", Sha256: receiptDigest, MediaType: constants.MediaTypeJSON, SchemaRef: "ztd.operator.v1.ActionReceipt", ProducerIdentity: "gateway", ScopeId: "scope-1", VerificationStatus: "verified", VerifierId: constants.ReceiptEvidenceVerifierID, VerifierVersion: constants.ReceiptEvidenceVerifierVersion, BundlePath: oscalTestRecordBundlePath},
			{ArtifactId: metricID, ArtifactType: "eval-metric", Sha256: metricDigest, MediaType: constants.MediaTypeJSON, SchemaRef: "ztd.eval.v1.EvaluationMetric", ProducerIdentity: "policy_outcome@1.0.0", ScopeId: "scope-1", VerificationStatus: "verified", VerifierId: constants.ConformanceVerifierID, VerifierVersion: constants.ConformanceVerifierVersion, BundlePath: oscalTestRecordBundlePath},
		},
		EvidenceGraphValid: true,
	}
}

// TestOSCALExporter_GenerateAssessmentResults asserts canonical assertion observations, framework findings, and evidence resources.
// oscalTestRecordBundlePath is a conformance record bundle path. The fixtures
// that use it need an evidence bundle path only; nothing reads the body.
var oscalTestRecordBundlePath = path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, "run-1", constants.ComplianceBundleSourceRuntimeDirname, constants.ConformanceRecordFilename)

func TestOSCALExporter_GenerateAssessmentResults(t *testing.T) {
	analysis := oscalTestAnalysis()
	results, err := NewOSCALExporter(oscalTestCatalog()).GenerateAssessmentResults(analysis)
	require.NoError(t, err)
	require.NotNil(t, results)

	assert.NotEmpty(t, results.AssessmentResults.UUID)
	assert.Equal(t, "ztd Compliance Assessment Results", results.AssessmentResults.Metadata.Title)
	assert.Equal(t, constants.AnalysisSchemaVersion, results.AssessmentResults.Metadata.Version)
	assert.Equal(t, "1.1.2", results.AssessmentResults.Metadata.OscalVersion)
	assert.Equal(t, analysis.GetGeneratedAt().AsTime().UTC().Format(time.RFC3339), results.AssessmentResults.Metadata.Published)

	require.Len(t, results.AssessmentResults.Results, 1)
	result := results.AssessmentResults.Results[0]
	assert.Contains(t, result.Title, analysis.GetScopeRef())
	assert.NotEmpty(t, result.Start)
	require.Len(t, result.Observations, 2)
	assert.Contains(t, result.Observations[0].Title, "ZTD-GOV-BLOCK-001")
	assert.Equal(t, "TEST", result.Observations[0].Methods[0])
	assert.Equal(t, constants.AssertionGraderID+"@"+constants.AssertionGraderVersion, oscalPropValue(result.Observations[0].Props, "ztd-verifier"))
	assert.Equal(t, analysis.GetAssertionAssessments()[0].GetAssessmentId(), result.Observations[0].Subjects[0].Title)
	require.Len(t, result.Findings, 2)
	assert.Equal(t, "satisfied", result.Findings[0].Target.Status.State)
	assert.Equal(t, "KSI-CMT-01", result.Findings[0].Target.TargetID)
	assert.Equal(t, "not-satisfied", result.Findings[1].Target.Status.State)
	assert.Equal(t, "KSI-MLA-07", result.Findings[1].Target.TargetID)
	require.Len(t, results.AssessmentResults.BackMatter.Resources, 2)
}

// TestOSCALExporter_NilCatalog asserts that analysis rendering does not require a catalog.
func TestOSCALExporter_NilCatalog(t *testing.T) {
	exporter := NewOSCALExporter(nil)

	_, err := exporter.GenerateAssessmentResults(oscalTestAnalysis())
	require.NoError(t, err)
}

// TestOSCALExporter_NilAnalysis asserts that nil canonical analysis produces ErrValidationFailed.
func TestOSCALExporter_NilAnalysis(t *testing.T) {
	_, err := NewOSCALExporter(oscalTestCatalog()).GenerateAssessmentResults(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrValidationFailed)
}

// TestOSCALExporter_EmptyAnalysisAssessments asserts that analysis without assessments produces empty observations and findings.
func TestOSCALExporter_EmptyAnalysisAssessments(t *testing.T) {
	analysis := oscalTestAnalysis()
	analysis.AssertionAssessments = nil
	analysis.FrameworkAssessments = nil

	results, err := NewOSCALExporter(oscalTestCatalog()).GenerateAssessmentResults(analysis)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrValidationFailed)
	assert.Nil(t, results)
}

// TestOSCALAssessmentResults_MarshalJSON verifies JSON serialization produces
// a valid OSCAL structure with expected top-level keys.
func TestOSCALAssessmentResults_MarshalJSON(t *testing.T) {
	exporter := NewOSCALExporter(oscalTestCatalog())

	results, err := exporter.GenerateAssessmentResults(oscalTestAnalysis())
	require.NoError(t, err)

	data, err := json.Marshal(results)
	require.NoError(t, err)

	var decoded OSCALAssessmentResults
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.NotEmpty(t, decoded.AssessmentResults.UUID)
	assert.NotEmpty(t, decoded.AssessmentResults.Metadata.Title)
	assert.NotEmpty(t, decoded.AssessmentResults.ImportAP.Href)
	require.Len(t, decoded.AssessmentResults.Results, 1)
	result := decoded.AssessmentResults.Results[0]
	assert.NotEmpty(t, result.UUID)
	assert.NotEmpty(t, result.Title)
	assert.NotEmpty(t, result.Start)
	assert.NotNil(t, result.ReviewedControls)
	assert.NotEmpty(t, result.Observations)
	assert.NotEmpty(t, result.Findings)
}

// TestOSCALExporter_GenerateAssessmentResults_MapsCanonicalStatuses verifies OSCAL-compatible framework finding statuses.
func TestOSCALExporter_GenerateAssessmentResults_MapsCanonicalStatuses(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		expected string
	}{
		{name: "satisfied", status: "satisfied", expected: "satisfied"},
		{name: "not satisfied", status: "not_satisfied", expected: "not-satisfied"},
		{name: "not applicable", status: "not_applicable", expected: ""},
		{name: "unverifiable", status: "unverifiable", expected: ""},
		{name: "customer attestation required", status: "customer_attestation_required", expected: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis := oscalTestAnalysis()
			analysis.FrameworkAssessments = analysis.FrameworkAssessments[:1]
			analysis.FrameworkAssessments[0].Status = tt.status
			results, err := NewOSCALExporter(nil).GenerateAssessmentResults(analysis)
			require.NoError(t, err)
			if tt.expected == "" {
				assert.Empty(t, results.AssessmentResults.Results[0].Findings)
				assert.Contains(t, oscalPropValue(results.AssessmentResults.Results[0].Props, "ztd-framework-status"), tt.status)
				return
			}
			require.Len(t, results.AssessmentResults.Results[0].Findings, 1)
			assert.Equal(t, tt.expected, results.AssessmentResults.Results[0].Findings[0].Target.Status.State)
			assert.Contains(t, results.AssessmentResults.Results[0].Findings[0].Description, tt.status)
		})
	}
}

func TestOSCALExporter_GenerateAssessmentResults_RejectsInvalidGraphAndEvidenceReferences(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*compliancev1.ComplianceAnalysis)
		target error
	}{
		{name: "invalid graph", mutate: func(analysis *compliancev1.ComplianceAnalysis) { analysis.EvidenceGraphValid = false }, target: constants.ErrInvalidEvidenceGraph},
		{name: "missing evidence resource", mutate: func(analysis *compliancev1.ComplianceAnalysis) {
			analysis.EvidenceResources = analysis.EvidenceResources[:1]
		}, target: constants.ErrUnresolvedReference},
		{name: "digest mismatch", mutate: func(analysis *compliancev1.ComplianceAnalysis) {
			analysis.EvidenceResources[0].Sha256 = analysis.EvidenceResources[1].Sha256
		}, target: constants.ErrUnresolvedReference},
		{name: "cross scope evidence", mutate: func(analysis *compliancev1.ComplianceAnalysis) { analysis.EvidenceResources[0].ScopeId = "scope-2" }, target: constants.ErrEvidenceScopeMismatch},
		{name: "missing bundle path", mutate: func(analysis *compliancev1.ComplianceAnalysis) { analysis.EvidenceResources[0].BundlePath = "" }, target: constants.ErrUnresolvedReference},
		{name: "invalid OSCAL media type", mutate: func(analysis *compliancev1.ComplianceAnalysis) {
			analysis.EvidenceResources[0].MediaType = "invalid/media-type"
		}, target: constants.ErrOSCALValidationFailed},
		{name: "missing assertion assessment", mutate: func(analysis *compliancev1.ComplianceAnalysis) {
			analysis.FrameworkAssessments[0].AssertionAssessmentRefs = []string{"missing"}
		}, target: constants.ErrUnresolvedReference},
		{name: "missing evidence link target", mutate: func(analysis *compliancev1.ComplianceAnalysis) {
			analysis.EvidenceLinks = []*compliancev1.EvidenceLink{{SourceRef: analysis.EvidenceResources[0].ArtifactId, TargetRef: "missing", LinkType: "references"}}
		}, target: constants.ErrUnresolvedReference},
		{name: "missing evidence link source", mutate: func(analysis *compliancev1.ComplianceAnalysis) {
			analysis.EvidenceLinks = []*compliancev1.EvidenceLink{{SourceRef: "missing", TargetRef: analysis.EvidenceResources[0].ArtifactId, LinkType: "references"}}
		}, target: constants.ErrUnresolvedReference},
		{name: "unsupported evidence link type", mutate: func(analysis *compliancev1.ComplianceAnalysis) {
			analysis.EvidenceLinks = []*compliancev1.EvidenceLink{{SourceRef: analysis.EvidenceResources[0].ArtifactId, TargetRef: analysis.EvidenceResources[1].ArtifactId, LinkType: "derived-from"}}
		}, target: constants.ErrUnresolvedReference},
		{name: "duplicate evidence link", mutate: func(analysis *compliancev1.ComplianceAnalysis) {
			link := &compliancev1.EvidenceLink{SourceRef: analysis.EvidenceResources[0].ArtifactId, TargetRef: analysis.EvidenceResources[1].ArtifactId, LinkType: "references"}
			analysis.EvidenceLinks = []*compliancev1.EvidenceLink{link, link}
		}, target: constants.ErrEvidenceDuplicateID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis := oscalTestAnalysis()
			tt.mutate(analysis)
			_, err := NewOSCALExporter(nil).GenerateAssessmentResults(analysis)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.target)
		})
	}
}

// TestGenerateUUID_DeterministicV5 verifies stable identity-bound RFC 4122 UUID v5 generation.
func TestGenerateUUID_DeterministicV5(t *testing.T) {
	u1 := generateUUID("finding", "analysis-1", "control-1")
	u2 := generateUUID("finding", "analysis-1", "control-1")
	u3 := generateUUID("finding", "analysis-1", "control-2")

	assert.Equal(t, u1, u2)
	assert.Equal(t, "e59757ff-42aa-5836-b667-d9fafdc79179", u1)
	assert.NotEqual(t, u1, u3)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, u1)

	parsed, err := uuid.Parse(u1)
	require.NoError(t, err)
	assert.Equal(t, byte(5), parsed[6]>>4)
}

func TestOSCALExporter_GenerateAssessmentResultsFromCanonicalAnalysis_ResolvesEvidenceResources(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	artifactID := "action-receipt:sha256:" + digest
	generatedAt := time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)
	analysis := &compliancev1.ComplianceAnalysis{
		AnalysisId:            "compliance-analysis:sha256:" + digest,
		AnalysisSchemaVersion: constants.AnalysisSchemaVersion,
		ScopeRef:              "scope-1",
		GeneratedAt:           timestamppb.New(generatedAt),
		GeneratorIdentity:     constants.AnalysisBuilderID,
		GeneratorVersion:      constants.AnalysisBuilderVersion,
		AssertionAssessments: []*compliancev1.ControlAssertionAssessment{{
			AssessmentId:    "assertion-assessment:sha256:" + digest,
			ScopeId:         "scope-1",
			AssertionRef:    &compliancev1.VersionedReference{Id: "ZTD-GOV-BLOCK-001", Version: "1.0.0"},
			Status:          "satisfied",
			EvidenceLevel:   "L3",
			EvaluatedAt:     timestamppb.New(generatedAt),
			VerifierRef:     &compliancev1.VersionedReference{Id: constants.AssertionGraderID, Version: constants.AssertionGraderVersion},
			EvidenceRefs:    []string{artifactID},
			FreshnessStatus: "fresh",
		}},
		FrameworkAssessments: []*compliancev1.FrameworkControlAssessment{{
			AssessmentId:            "framework-assessment:sha256:" + digest,
			ScopeId:                 "scope-1",
			FrameworkRef:            &compliancev1.VersionedReference{Id: "fedramp-20x", Version: "CR26-2026-06-24"},
			ControlId:               "KSI-MLA-07",
			Status:                  "satisfied",
			Responsibility:          "platform",
			AssertionAssessmentRefs: []string{"assertion-assessment:sha256:" + digest},
			EvidenceLevel:           "L3",
		}},
		EvidenceResources: []*compliancev1.ComplianceEvidenceReference{{
			ArtifactId:         artifactID,
			ArtifactType:       "action-receipt",
			Sha256:             digest,
			MediaType:          constants.MediaTypeJSON,
			SchemaRef:          "ztd.operator.v1.ActionReceipt",
			ProducerIdentity:   "gateway",
			ScopeId:            "scope-1",
			VerificationStatus: "verified",
			VerifierId:         constants.ReceiptEvidenceVerifierID,
			VerifierVersion:    constants.ReceiptEvidenceVerifierVersion,
			BundlePath:         oscalTestRecordBundlePath,
		}},
		EvidenceGraphValid: true,
	}

	doc, err := NewOSCALExporter(oscalTestCatalog()).GenerateAssessmentResults(analysis)
	require.NoError(t, err)
	require.Len(t, doc.AssessmentResults.BackMatter.Resources, 1)
	resource := doc.AssessmentResults.BackMatter.Resources[0]
	assert.Equal(t, artifactID, oscalPropValue(resource.Props, "artifact-id"))
	assert.Equal(t, digest, oscalPropValue(resource.Props, "sha256"))
	require.Len(t, doc.AssessmentResults.Results, 1)
	require.Len(t, doc.AssessmentResults.Results[0].Observations, 1)
	require.Len(t, doc.AssessmentResults.Results[0].Observations[0].RelevantEvidence, 1)
	assert.Equal(t, "#"+resource.UUID, doc.AssessmentResults.Results[0].Observations[0].RelevantEvidence[0].Href)
}

func TestOSCALExporter_GenerateAssessmentResults_ResolvesEveryEvidenceLink(t *testing.T) {
	analysis := oscalTestAnalysis()
	analysis.EvidenceLinks = []*compliancev1.EvidenceLink{{SourceRef: analysis.EvidenceResources[0].ArtifactId, TargetRef: analysis.EvidenceResources[1].ArtifactId, LinkType: "references"}}
	document, err := NewOSCALExporter(oscalTestCatalog()).GenerateAssessmentResults(analysis)
	require.NoError(t, err)
	assert.Len(t, document.AssessmentResults.BackMatter.Resources, len(analysis.EvidenceResources))
}

func TestOSCALExporter_GenerateAssessmentResults_ConformsToV112RequiredShape(t *testing.T) {
	document, err := NewOSCALExporter(oscalTestCatalog()).GenerateAssessmentResults(oscalTestAnalysis())
	require.NoError(t, err)

	assessmentResults := document.AssessmentResults
	assert.NotEmpty(t, assessmentResults.UUID)
	assert.NotEmpty(t, assessmentResults.ImportAP.Href)
	require.Len(t, assessmentResults.Results, 1)
	result := assessmentResults.Results[0]
	require.NotNil(t, result.ReviewedControls)
	require.NotEmpty(t, result.ReviewedControls.ControlSelections)
	require.NotEmpty(t, result.Observations)
	assert.NotEmpty(t, result.Observations[0].Collected)
	require.NotEmpty(t, result.Findings)
	for _, finding := range result.Findings {
		assert.Equal(t, "objective-id", finding.Target.Type)
		assert.Contains(t, []string{"satisfied", "not-satisfied"}, finding.Target.Status.State)
	}

	body, err := json.Marshal(document)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"assessment-results"`)
	assert.Contains(t, string(body), `"reviewed-controls"`)
	assert.Contains(t, string(body), `"status":{"state":`)
}

func TestValidateOSCALAssessmentResults_RejectsSchemaViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OSCALAssessmentResults)
		target error
	}{
		{name: "missing document", target: constants.ErrValidationFailed},
		{name: "invalid root UUID", mutate: func(document *OSCALAssessmentResults) { document.AssessmentResults.UUID = "invalid" }, target: constants.ErrValidationFailed},
		{name: "missing assessment plan import", mutate: func(document *OSCALAssessmentResults) { document.AssessmentResults.ImportAP.Href = "" }, target: constants.ErrValidationFailed},
		{name: "missing reviewed controls", mutate: func(document *OSCALAssessmentResults) { document.AssessmentResults.Results[0].ReviewedControls = nil }, target: constants.ErrValidationFailed},
		{name: "empty reviewed control selections", mutate: func(document *OSCALAssessmentResults) {
			document.AssessmentResults.Results[0].ReviewedControls.ControlSelections = nil
		}, target: constants.ErrValidationFailed},
		{name: "missing observation collection time", mutate: func(document *OSCALAssessmentResults) {
			document.AssessmentResults.Results[0].Observations[0].Collected = ""
		}, target: constants.ErrValidationFailed},
		{name: "invalid observation method", mutate: func(document *OSCALAssessmentResults) {
			document.AssessmentResults.Results[0].Observations[0].Methods = []string{"AUTOMATED"}
		}, target: constants.ErrValidationFailed},
		{name: "invalid finding status", mutate: func(document *OSCALAssessmentResults) {
			document.AssessmentResults.Results[0].Findings[0].Target.Status.State = "unknown"
		}, target: constants.ErrValidationFailed},
		{name: "unresolved relevant evidence", mutate: func(document *OSCALAssessmentResults) {
			document.AssessmentResults.Results[0].Observations[0].RelevantEvidence[0].Href = "#00000000-0000-5000-8000-000000000000"
		}, target: constants.ErrUnresolvedReference},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var document *OSCALAssessmentResults
			if tt.mutate != nil {
				var err error
				document, err = NewOSCALExporter(oscalTestCatalog()).GenerateAssessmentResults(oscalTestAnalysis())
				require.NoError(t, err)
				tt.mutate(document)
			}
			err := validateOSCALAssessmentResults(document)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.target)
		})
	}
}

func oscalPropValue(props []OSCALProp, name string) string {
	for _, prop := range props {
		if prop.Name == name {
			return prop.Value
		}
	}
	return ""
}
