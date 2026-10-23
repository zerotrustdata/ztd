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
	"github.com/zerotrustdata/ztd/internal/services/fs"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// CampaignImporter loads persisted model-campaign artifacts into the evidence
// graph without invoking inference or mutation.
type CampaignImporter struct {
	reader fs.RuntimeFileService
	runID  string
	policy CampaignVerificationPolicy
}

type campaignAssertionMapping struct {
	EvidenceKind          string
	NativeAssertionRefs   []*compliancev1.VersionedReference
	RequiredEvidenceTypes []complianceevidence.ArtifactType
	ClaimLimit            string
}

func reviewedCampaignAssertionMappings() []campaignAssertionMapping {
	return []campaignAssertionMapping{
		{
			EvidenceKind:          "campaign_verification",
			RequiredEvidenceTypes: []complianceevidence.ArtifactType{complianceevidence.ArtifactTypeEvalManifest},
			ClaimLimit:            "Campaign verification proves persisted source consistency and population binding only.",
		},
		{
			EvidenceKind:          "deterministic_grade",
			RequiredEvidenceTypes: []complianceevidence.ArtifactType{complianceevidence.ArtifactTypeEvalManifest},
			ClaimLimit:            "Campaign deterministic grades measure application evaluation criteria, not protocol authorization or governed execution.",
		},
		{
			EvidenceKind:          "semantic_grade",
			RequiredEvidenceTypes: []complianceevidence.ArtifactType{complianceevidence.ArtifactTypeEvalManifest},
			ClaimLimit:            "Campaign semantic grades measure model output quality, not independently observed governed state.",
		},
		{
			EvidenceKind:          "application_policy_outcome",
			RequiredEvidenceTypes: []complianceevidence.ArtifactType{complianceevidence.ArtifactTypeEvalManifest},
			ClaimLimit:            "Campaign application policy outcomes do not replace signed governance-stage or policy-outcome evidence.",
		},
		{
			EvidenceKind: "governed_action_binding",
			NativeAssertionRefs: []*compliancev1.VersionedReference{
				{Id: "ZTD-AU-PERSIST-001", Version: "2.0.0"},
				{Id: "ZTD-AU-RECEIPT-001", Version: "2.0.0"},
				{Id: "ZTD-CM-STATE-001", Version: "2.1.0"},
				{Id: "ZTD-GOV-ALLOW-001", Version: "2.0.0"},
				{Id: "ZTD-GOV-BLOCK-001", Version: "2.0.0"},
			},
			RequiredEvidenceTypes: []complianceevidence.ArtifactType{
				complianceevidence.ArtifactTypeActionReceipt,
				complianceevidence.ArtifactTypeReceiptPersistence,
				complianceevidence.ArtifactTypeProtocolChain,
				complianceevidence.ArtifactTypeStateObservation,
			},
			ClaimLimit: "A governed-action binding supports a native claim only when each referenced signed governance artifact is independently imported and verified for the same subject.",
		},
	}
}

func NewCampaignImporter(reader fs.RuntimeFileService, runID string, policy CampaignVerificationPolicy) *CampaignImporter {
	if policy.AssessmentTime == nil {
		policy.AssessmentTime = time.Now
	}
	return &CampaignImporter{reader: reader, runID: runID, policy: policy}
}

func (i *CampaignImporter) SourceID() string {
	return constants.EvaluationSourceKindCampaign
}

func (i *CampaignImporter) RunID() string {
	if i == nil {
		return ""
	}
	return i.runID
}

func (i *CampaignImporter) Import(ctx context.Context) ([]complianceevidence.EvidenceNode, error) {
	if i == nil || i.reader == nil || i.policy.VerifierReleaseVersion == "" || i.policy.AssessmentTime == nil {
		return nil, fmt.Errorf("%w: campaign importer is incomplete", constants.ErrInvalidEvidenceGraph)
	}
	if err := complianceevidence.ValidatePathElement(i.runID); err != nil {
		return nil, fmt.Errorf("%w: campaign importer run ID: %w", constants.ErrInvalidEvidenceGraph, err)
	}
	result, err := verifyCampaignRunReadOnly(ctx, NewStore(i.reader), i.runID, i.policy)
	if err != nil {
		return nil, err
	}
	body, err := evalv1.MarshalCanonical(result.Report)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical campaign verification report: %v", constants.ErrEvidenceArtifactMalformed, err)
	}
	artifactID := complianceevidence.ContentAddress(complianceevidence.ArtifactTypeEvalManifest, body)
	_, digest, ok := complianceevidence.ParseContentAddress(artifactID)
	if !ok {
		return nil, fmt.Errorf("%w: campaign verification report content address is invalid", constants.ErrEvidenceArtifactMalformed)
	}
	verifiedAt := result.Report.GetVerifiedAt().AsTime().UTC()
	return []complianceevidence.EvidenceNode{{
		ArtifactID:         artifactID,
		ArtifactType:       complianceevidence.ArtifactTypeEvalManifest,
		SHA256:             digest,
		MediaType:          constants.MediaTypeJSON,
		SchemaRef:          "ztd.eval.v1.EvaluationVerificationReport",
		ProducerIdentity:   constants.CampaignVerifierID,
		ProducedAt:         verifiedAt,
		ScopeID:            complianceevidence.EvalScopeID(result.Run.GetCampaignBinding().GetCampaignId()),
		RunID:              i.runID,
		VerificationStatus: complianceevidence.VerificationStatusVerified,
		VerifierID:         constants.CampaignVerifierID,
		VerifierVersion:    constants.CampaignVerifierVersion,
		VerifiedAt:         verifiedAt,
		BundlePath:         constants.CampaignVerificationFilename,
		CanonicalBytes:     body,
		Diagnostics:        campaignVerificationDiagnostics(result.Report),
	}}, nil
}

func campaignVerificationDiagnostics(report *evalv1.EvaluationVerificationReport) []*compliancev1.AssessmentDiagnostic {
	if report == nil {
		return nil
	}
	result := make([]*compliancev1.AssessmentDiagnostic, 0, 2)
	subject := &compliancev1.AssessmentSubjectSelection{RunId: report.GetRunId()}
	conditionalMappingCount := 0
	for _, mapping := range reviewedCampaignAssertionMappings() {
		if len(mapping.NativeAssertionRefs) > 0 {
			conditionalMappingCount++
		}
	}
	result = append(result, &compliancev1.AssessmentDiagnostic{
		Code:     "campaign_native_assertion_unmapped",
		Severity: "warning",
		Subject:  subject,
		Message:  fmt.Sprintf("campaign verification and application evidence do not satisfy a native assertion; %d reviewed mappings require independently verified signed governance artifacts for the same subject", conditionalMappingCount),
	})
	if report.GetExpectedAssignmentCount() > report.GetVerifiedAssignmentCount() {
		result = append(result, &compliancev1.AssessmentDiagnostic{
			Code:     "campaign_population_incomplete",
			Severity: "warning",
			Subject:  subject,
			Message:  fmt.Sprintf("campaign verification covered %d of %d expected assignments", report.GetVerifiedAssignmentCount(), report.GetExpectedAssignmentCount()),
		})
	}
	for _, reason := range report.GetFailureReasons() {
		if reason == "" {
			continue
		}
		result = append(result, &compliancev1.AssessmentDiagnostic{
			Code:     "campaign_verification_failure",
			Severity: "error",
			Subject:  subject,
			Message:  reason,
		})
	}
	return result
}
