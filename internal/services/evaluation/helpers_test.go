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

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	verdictPass            = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
	verdictFail            = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	verdictInvalidEvidence = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE
)

func repeatHex(ch byte, count int) string {
	out := make([]byte, count)
	for i := range out {
		out[i] = ch
	}
	return string(out)
}

const campaignPublicationStateSchemaVersion = CampaignSchemaVersion

type memoryCampaignPublicationStateStore struct {
	byRun map[string]*CampaignPublicationState
}

func NewMemoryCampaignPublicationStateStore() *memoryCampaignPublicationStateStore {
	return &memoryCampaignPublicationStateStore{byRun: map[string]*CampaignPublicationState{}}
}

func (s *memoryCampaignPublicationStateStore) Load(_ context.Context, runID string) (*CampaignPublicationState, error) {
	if s == nil || runID == "" {
		return nil, fmt.Errorf("evaluation: load publication state: %w", constants.ErrMissingRequiredField)
	}
	state, ok := s.byRun[runID]
	if !ok {
		return &CampaignPublicationState{
			SchemaVersion:           campaignPublicationStateSchemaVersion,
			RunID:                   runID,
			PublishedIdempotency:    []string{},
			PublishedProofArtifacts: map[string]CampaignPublishedProofArtifacts{},
		}, nil
	}
	return cloneCampaignPublicationState(state), nil
}

func (s *memoryCampaignPublicationStateStore) Save(_ context.Context, state *CampaignPublicationState) error {
	if s == nil || state == nil || state.RunID == "" {
		return fmt.Errorf("evaluation: save publication state: %w", constants.ErrMissingRequiredField)
	}
	s.byRun[state.RunID] = cloneCampaignPublicationState(state)
	return nil
}

func (s *memoryCampaignPublicationStateStore) Delete(_ context.Context, runID string) error {
	if s == nil || runID == "" {
		return fmt.Errorf("evaluation: delete publication state: %w", constants.ErrMissingRequiredField)
	}
	delete(s.byRun, runID)
	return nil
}

// StubHomogeneousAssignmentModelInferences returns minimal reported inference rows
// for homogeneous assignment stubs used in publication and export tests.
func StubHomogeneousAssignmentModelInferences(assignment *evalv1.EvaluationAssignment) []*evalv1.ModelInferenceRecord {
	if assignment == nil {
		return nil
	}
	homogeneous, ok := assignment.GetTarget().(*evalv1.EvaluationAssignment_Homogeneous)
	if !ok || homogeneous.Homogeneous == nil {
		return nil
	}
	role := homogeneous.Homogeneous.GetDesignatedRole()
	if role == evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED {
		role = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
	}
	variant := homogeneous.Homogeneous.GetCandidateVariant()
	if variant == nil {
		variant = &evalv1.ModelVariant{VariantId: "qwen3-4b"}
	}
	return []*evalv1.ModelInferenceRecord{{
		InferenceRecordId: "stub-inference",
		Classification:    evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
		ModelRole:         role,
		ModelVariant:      variant,
		AgentPersona:      "sage",
		UsageAvailability: evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED,
	}}
}

// ToModelRegistryFreeze converts the eval inventory into the inference registry
// shape used by test fixtures.
func (freeze *ModelInventoryFreeze) ToModelRegistryFreeze() *ModelRegistryFreeze {
	if freeze == nil {
		return nil
	}
	return &ModelRegistryFreeze{
		CampaignID: freeze.CampaignID,
		Digest:     freeze.RegistryDigest,
		Variants:   freeze.InferenceVariants,
	}
}

func findDeterministicGradeByID(grades []*evalv1.DeterministicGrade, gradeID string) *evalv1.DeterministicGrade {
	for _, grade := range grades {
		if grade.GetGradeId() == gradeID {
			return grade
		}
	}
	return nil
}
