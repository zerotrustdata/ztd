// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const harnessInvestigationIDPrefix = "eval-harness:"

// ValidateCampaignInvestigation checks the campaign's scored investigation
// scope. A nil value means the caller omitted the option; materialization
// resolves that to an explicit fresh mode. A missing field on a stored spec is
// retained as legacy data and accepted by digest validation.
func ValidateCampaignInvestigation(investigation *evalv1.EvaluationInvestigation) error {
	if investigation == nil {
		return nil
	}
	switch investigation.GetMode() {
	case evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH:
		if investigation.GetInvestigationId() != "" || investigation.GetDataOperatorId() != "" {
			return fmt.Errorf("evaluation: fresh investigation cannot declare an ID or Data Operator: %w", constants.ErrEvaluationInvestigationInvalid)
		}
	case evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT:
		if strings.TrimSpace(investigation.GetInvestigationId()) == "" || strings.TrimSpace(investigation.GetDataOperatorId()) == "" {
			return fmt.Errorf("evaluation: persistent investigation requires an investigation ID and Data Operator ID: %w", constants.ErrEvaluationInvestigationInvalid)
		}
		if investigation.GetInvestigationId() != strings.TrimSpace(investigation.GetInvestigationId()) || investigation.GetDataOperatorId() != strings.TrimSpace(investigation.GetDataOperatorId()) {
			return fmt.Errorf("evaluation: persistent investigation IDs must not contain surrounding whitespace: %w", constants.ErrEvaluationInvestigationInvalid)
		}
		if strings.HasPrefix(investigation.GetInvestigationId(), harnessInvestigationIDPrefix) {
			return fmt.Errorf("evaluation: investigation ID uses reserved harness namespace: %w", constants.ErrEvaluationInvestigationInvalid)
		}
	default:
		return fmt.Errorf("evaluation: investigation mode is unspecified or unknown: %w", constants.ErrEvaluationInvestigationInvalid)
	}
	return nil
}

// PlatformIdentity names the ztd build that froze a campaign.
type PlatformIdentity struct {
	// Release is the build version (VERSION, for example "v1.0.0"). Required.
	Release string
	// SourceRevision is the source commit of the build, when the build knows it.
	SourceRevision string
}

// MaterializeCampaignSpec binds the frozen catalog and model registry
// into one immutable campaign spec for Phase 4 controller initialization.
// The platform release is part of the campaign digest. holdContext is frozen
// only when there is a later repetition to carry the investigation into.
func MaterializeCampaignSpec(campaignID string, catalog *evalv1.EvaluationScenarioCatalog, inventory *ModelInventoryFreeze, repetitionCount uint32, holdContext bool, platform PlatformIdentity) (*evalv1.EvaluationCampaignSpec, error) {
	return MaterializeCampaignSpecWithInvestigation(campaignID, catalog, inventory, repetitionCount, holdContext, platform, nil)
}

// MaterializeCampaignSpecWithInvestigation binds the requested investigation
// identity alongside the catalog and model registry. A nil investigation
// freezes a fresh identity mode for newly created campaigns.
func MaterializeCampaignSpecWithInvestigation(campaignID string, catalog *evalv1.EvaluationScenarioCatalog, inventory *ModelInventoryFreeze, repetitionCount uint32, holdContext bool, platform PlatformIdentity, investigation *evalv1.EvaluationInvestigation) (*evalv1.EvaluationCampaignSpec, error) {
	if campaignID == "" || catalog == nil || inventory == nil || len(inventory.Variants) == 0 || platform.Release == "" {
		return nil, fmt.Errorf("evaluation: materialize campaign spec: %w", constants.ErrMissingRequiredField)
	}
	if err := ValidateCampaignInvestigation(investigation); err != nil {
		return nil, fmt.Errorf("evaluation: materialize campaign spec: %w", err)
	}
	if err := ValidateModelRegistry(inventory); err != nil {
		return nil, fmt.Errorf("evaluation: materialize campaign spec: %w", err)
	}
	if err := ValidateScenarioCatalogDigest(catalog); err != nil {
		return nil, fmt.Errorf("evaluation: materialize campaign spec: %w", err)
	}
	if repetitionCount == 0 {
		repetitionCount = 1
	}
	if investigation == nil {
		investigation = &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH}
	} else {
		investigation = proto.Clone(investigation).(*evalv1.EvaluationInvestigation)
	}
	spec := &evalv1.EvaluationCampaignSpec{
		SchemaVersion:       CampaignSchemaVersion,
		CampaignId:          campaignID,
		CatalogRef:          catalog.GetCatalogRef(),
		CatalogDigest:       catalog.GetCatalogDigest(),
		ModelRegistry:       append([]*evalv1.ModelVariant(nil), inventory.Variants...),
		ModelRegistryDigest: inventory.RegistryDigest,
		GovernancePosture:   evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		ScenarioCount:       uint32(len(catalog.GetScenarios())),
		RepetitionCount:     repetitionCount,
		HoldContext:         holdContext && repetitionCount > 1,
		PlatformRelease:     platform.Release,
		SourceRevision:      platform.SourceRevision,
		Investigation:       investigation,
	}
	digest, err := ComputeCampaignSpecDigest(spec)
	if err != nil {
		return nil, fmt.Errorf("evaluation: materialize campaign spec: %w", err)
	}
	spec.CampaignDigest = digest
	return spec, nil
}
