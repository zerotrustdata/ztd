// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"path"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

// evalLayout addresses campaign and run directories. The active layout is
// .ztd/data/eval. The archive mirrors it beneath .ztd/data/eval/archive, so an
// archived campaign or run keeps its internal structure and stays readable in
// place. Campaigns and runs archive independently: an archived run can belong
// to a campaign that is still active, so the two roots are separate.
type evalLayout struct {
	campaignsRoot string
	runsRoot      string
}

func activeEvalLayout() evalLayout {
	return evalLayout{
		campaignsRoot: constants.EvaluationCampaignsPath,
		runsRoot:      constants.EvaluationRunsPath,
	}
}

func archivedEvalLayout() evalLayout {
	return evalLayout{
		campaignsRoot: constants.EvaluationArchiveCampaignsPath,
		runsRoot:      constants.EvaluationArchiveRunsPath,
	}
}

// archivedRunLayout addresses an archived run whose campaign is still active.
func archivedRunLayout() evalLayout {
	return evalLayout{
		campaignsRoot: activeEvalLayout().campaignsRoot,
		runsRoot:      archivedEvalLayout().runsRoot,
	}
}

func (l evalLayout) campaignsRootDir() string {
	return l.campaignsRoot
}

func (l evalLayout) campaignDir(campaignID string) string {
	return path.Join(l.campaignsRootDir(), campaignID)
}

func (l evalLayout) runsRootDir() string {
	return l.runsRoot
}

func (l evalLayout) runDir(runID string) string {
	return path.Join(l.runsRootDir(), runID)
}

// evidenceDir is the run's sealed evidence directory. It follows the layout so
// an archived run's evidence is read and written beside its archived records.
func (l evalLayout) evidenceDir(runID string) string {
	return path.Join(l.runDir(runID), constants.EvaluationEvidenceDirname)
}

// providerWindowRefPath and providerAttemptRefPath address the keyed records
// that name the sealed provider-boundary window and attempt record of one
// provider attempt. Content-addressed bodies cannot be found by attempt ID, so
// these records are the attempt-keyed lookup into the evidence directory.
func (l evalLayout) providerWindowRefPath(runID, providerAttemptID string) string {
	return path.Join(l.evidenceDir(runID), constants.EvaluationProviderObservationsDirname, providerAttemptID, constants.EvaluationProviderWindowRefFilename)
}

func (l evalLayout) providerAttemptRefPath(runID, providerAttemptID string) string {
	return path.Join(l.evidenceDir(runID), constants.EvaluationProviderObservationsDirname, providerAttemptID, constants.EvaluationProviderAttemptRefFilename)
}

// modelProvenanceWindowRefPath addresses the keyed record that names the sealed
// model provenance attestation window of one provider attempt.
func (l evalLayout) modelProvenanceWindowRefPath(runID, providerAttemptID string) string {
	return path.Join(l.evidenceDir(runID), constants.EvaluationModelProvenanceDirname, providerAttemptID, constants.EvaluationModelProvenanceWindowRefFilename)
}

func (l evalLayout) campaignSpecPath(campaignID string) string {
	return path.Join(l.campaignDir(campaignID), constants.EvaluationCampaignSpecFilename)
}

func (l evalLayout) campaignReleaseTagPath(campaignID string) string {
	return path.Join(l.campaignDir(campaignID), constants.EvaluationCampaignReleaseTagFilename)
}

func (l evalLayout) heterogeneousStackSetPath(campaignID string) string {
	return path.Join(l.campaignDir(campaignID), constants.EvaluationHeterogeneousStackSetFilename)
}

func (l evalLayout) scenarioCatalogPath(campaignID string) string {
	return path.Join(l.campaignDir(campaignID), constants.EvaluationScenarioCatalogFilename)
}

func (l evalLayout) scenarioArtifactPath(campaignID string, reference *compliancev1.ComplianceEvidenceReference) (string, error) {
	if reference == nil {
		return "", fmt.Errorf("%w: frozen scenario artifact reference is missing", constants.ErrEvidenceArtifactMalformed)
	}
	_, digest, ok := complianceevidence.ParseContentAddress(reference.GetArtifactId())
	if !ok || digest != reference.GetSha256() {
		return "", fmt.Errorf("%w: frozen scenario artifact reference is invalid", constants.ErrEvidenceArtifactMalformed)
	}
	return path.Join(l.campaignDir(campaignID), constants.EvaluationScenarioArtifactsDirname, digest+constants.FileExtJSON), nil
}

func (l evalLayout) assignmentsDir(runID string) string {
	return path.Join(l.runDir(runID), constants.EvaluationAssignmentsDirname)
}

func (l evalLayout) assignmentPath(runID, assignmentID string) string {
	return path.Join(l.assignmentsDir(runID), assignmentID+constants.FileExtJSON)
}

func (l evalLayout) assignmentResultPath(runID, assignmentID string) string {
	return path.Join(l.assignmentsDir(runID), assignmentID+"-result"+constants.FileExtJSON)
}

func (l evalLayout) assignmentAgentTracePath(runID, assignmentID string) string {
	return path.Join(l.assignmentsDir(runID), assignmentID+"-agent-trace"+constants.FileExtJSON)
}

func (l evalLayout) runStatePath(runID string) string {
	return path.Join(l.runDir(runID), constants.EvaluationRunStateFilename)
}

func (l evalLayout) campaignVerificationPath(runID string) string {
	return path.Join(l.runDir(runID), constants.CampaignVerificationFilename)
}

func (l evalLayout) runLeasePath(runID string) string {
	return path.Join(l.runDir(runID), constants.EvaluationRunLeaseFilename)
}
