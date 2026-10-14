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
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/verify"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type EvidenceImporter struct {
	reader          complianceevidence.ArtifactReader
	runID           string
	now             func() time.Time
	verifyPersisted conformanceVerifier
}

func NewEvidenceImporter(reader complianceevidence.ArtifactReader, runID string, now func() time.Time) *EvidenceImporter {
	if now == nil {
		now = time.Now
	}
	return &EvidenceImporter{reader: reader, runID: runID, now: now, verifyPersisted: verify.VerifyPersisted}
}

func (i *EvidenceImporter) SourceID() string {
	return constants.EvaluationSourceKindConformance
}

// RunID returns the run ID this importer is bound to.
func (i *EvidenceImporter) RunID() string {
	if i == nil {
		return ""
	}
	return i.runID
}

// Import verifies the persisted conformance record and returns its two
// evidence nodes: the record itself and the suite verdict set derived from it.
func (i *EvidenceImporter) Import(ctx context.Context) ([]complianceevidence.EvidenceNode, error) {
	if i == nil || i.reader == nil || !complianceevidence.ValidPathElement(i.runID) || i.now == nil || i.verifyPersisted == nil {
		return nil, fmt.Errorf("%w: conformance evaluation importer is incomplete", constants.ErrInvalidEvidenceGraph)
	}
	raw, digest, err := readConformanceRecordRaw(ctx, i.reader, i.runID)
	if err != nil {
		return nil, err
	}
	report, set, err := verifyConformanceRecord(raw, digest, i.runID, i.now(), i.verifyPersisted)
	if err != nil {
		return nil, err
	}
	if !report.GetValid() {
		return nil, fmt.Errorf("%w: conformance run %s is not admitted: suite verdict %s", constants.ErrEvalRunVerificationFailed, i.runID, set.GetSuiteVerdict().String())
	}
	record := &evalv1.ConformanceRunRecord{}
	if err := evalv1.UnmarshalCanonical(raw, record); err != nil {
		return nil, fmt.Errorf("%w: conformance run record: %v", constants.ErrEvidenceArtifactMalformed, err)
	}
	setBody, err := evalv1.MarshalCanonical(set)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize conformance verdict set: %w", constants.ErrEvidenceArtifactMalformed, err)
	}
	scopeID := complianceevidence.EvalScopeID(record.GetSuiteId())
	verifiedAt := report.GetVerifiedAt().AsTime()
	producedAt := record.GetFinishedAt().AsTime()
	return []complianceevidence.EvidenceNode{
		conformanceEvidenceNode(complianceevidence.ArtifactTypeEvalManifest, "ztd.eval.v1.ConformanceRunRecord", constants.ConformanceRecordFilename, raw, scopeID, producedAt, verifiedAt, i.runID),
		conformanceEvidenceNode(complianceevidence.ArtifactTypeEvalMetric, "ztd.eval.v1.ConformanceVerdictSet", constants.ConformanceVerdictSetFilename, setBody, scopeID, producedAt, verifiedAt, i.runID),
	}, nil
}

// conformanceEvidenceNode builds one verified node over canonical protojson bytes.
func conformanceEvidenceNode(artifactType complianceevidence.ArtifactType, schemaRef, bundlePath string, body []byte, scopeID string, producedAt, verifiedAt time.Time, runID string) complianceevidence.EvidenceNode {
	artifactID := complianceevidence.ContentAddress(artifactType, body)
	_, digest, _ := complianceevidence.ParseContentAddress(artifactID)
	return complianceevidence.EvidenceNode{
		ArtifactID:         artifactID,
		ArtifactType:       artifactType,
		SHA256:             digest,
		MediaType:          constants.MediaTypeJSON,
		SchemaRef:          schemaRef,
		ProducerIdentity:   constants.ConformanceVerifierID,
		ProducedAt:         producedAt,
		ScopeID:            scopeID,
		RunID:              runID,
		VerificationStatus: complianceevidence.VerificationStatusVerified,
		VerifierID:         constants.ConformanceVerifierID,
		VerifierVersion:    constants.ConformanceVerifierVersion,
		VerifiedAt:         verifiedAt,
		BundlePath:         bundlePath,
		CanonicalBytes:     body,
	}
}
