// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestEvidenceImporterSourceIDIsConformance(t *testing.T) {
	t.Parallel()
	assert.Equal(t, constants.EvaluationSourceKindConformance, NewEvidenceImporter(nil, "run-1", nil).SourceID())
}

func TestEvidenceImporterEmitsRecordAndVerdictSetNodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "conformance-import"
	files := saveConformanceRunForCompliance(t, runID)
	raw, err := files.ReadFile(ctx, conformanceRecordPath(runID))
	require.NoError(t, err)
	set := &evalv1.ConformanceVerdictSet{RunId: runID, SuiteVerdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS}
	importer := NewEvidenceImporter(files, runID, func() time.Time { return conformanceTestVerifiedAt })
	importer.verifyPersisted = func([]byte, string) (*evalv1.ConformanceVerdictSet, error) { return set, nil }

	nodes, err := importer.Import(ctx)
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	recordSum := sha256.Sum256(raw)
	record := nodes[0]
	assert.Equal(t, complianceevidence.ArtifactTypeEvalManifest, record.ArtifactType)
	assert.Equal(t, "ztd.eval.v1.ConformanceRunRecord", record.SchemaRef)
	assert.Equal(t, constants.ConformanceRecordFilename, record.BundlePath)
	assert.Equal(t, hex.EncodeToString(recordSum[:]), record.SHA256)
	assert.Equal(t, raw, record.CanonicalBytes)
	assert.Equal(t, constants.MediaTypeJSON, record.MediaType)

	setBody, err := evalv1.MarshalCanonical(set)
	require.NoError(t, err)
	verdicts := nodes[1]
	assert.Equal(t, complianceevidence.ArtifactTypeEvalMetric, verdicts.ArtifactType)
	assert.Equal(t, "ztd.eval.v1.ConformanceVerdictSet", verdicts.SchemaRef)
	assert.Equal(t, constants.ConformanceVerdictSetFilename, verdicts.BundlePath)
	assert.Equal(t, setBody, verdicts.CanonicalBytes)
	assert.Equal(t, constants.MediaTypeJSON, verdicts.MediaType)

	for _, node := range nodes {
		assert.Equal(t, constants.EvalScopePrefix+cases.SuiteID, node.ScopeID)
		assert.Equal(t, runID, node.RunID)
		assert.Equal(t, complianceevidence.VerificationStatusVerified, node.VerificationStatus)
		assert.Equal(t, constants.ConformanceVerifierID, node.VerifierID)
		assert.Equal(t, constants.ConformanceVerifierVersion, node.VerifierVersion)
		assert.Equal(t, constants.ConformanceVerifierID, node.ProducerIdentity)
		assert.True(t, node.VerifiedAt.Equal(conformanceTestVerifiedAt))
		assert.True(t, node.ProducedAt.Equal(conformanceTestVerifiedAt.Add(-time.Minute)))
	}
}

func TestEvidenceImporterRejectsRunWithoutPassingSuite(t *testing.T) {
	t.Parallel()
	const runID = "conformance-import-incomplete"
	files := saveConformanceRunForCompliance(t, runID)
	importer := NewEvidenceImporter(files, runID, func() time.Time { return conformanceTestVerifiedAt })
	importer.verifyPersisted = stubConformanceVerifier(runID, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE)

	nodes, err := importer.Import(context.Background())
	require.Error(t, err)
	assert.Nil(t, nodes)
	assert.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
}

func TestEvidenceImporterRejectsTamperedRecordWithRealVerifier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "conformance-import-tampered"
	files := saveConformanceRunForCompliance(t, runID)
	raw, err := files.ReadFile(ctx, conformanceRecordPath(runID))
	require.NoError(t, err)
	flipped := append([]byte(nil), raw...)
	flipped[len(flipped)/2] ^= 0x01
	tampered := &conformanceTamperReader{ArtifactReader: files, path: conformanceRecordPath(runID), body: flipped}

	nodes, err := NewEvidenceImporter(tampered, runID, func() time.Time { return conformanceTestVerifiedAt }).Import(ctx)
	require.Error(t, err)
	assert.Nil(t, nodes)
	assert.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
}

func TestEvidenceImporterRejectsIncompleteImporter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, err := NewEvidenceImporter(nil, "run-1", func() time.Time { return conformanceTestVerifiedAt }).Import(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)

	files := saveConformanceRunForCompliance(t, "conformance-import-no-verifier")
	noVerifier := NewEvidenceImporter(files, "conformance-import-no-verifier", func() time.Time { return conformanceTestVerifiedAt })
	noVerifier.verifyPersisted = nil
	_, err = noVerifier.Import(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
}
