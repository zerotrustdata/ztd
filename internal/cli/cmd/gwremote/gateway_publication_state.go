// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"context"
	"encoding/json"
	"fmt"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// remoteGatewayCampaignPublicationStateStore keeps a run's publication
// idempotency in the Gateway. Delete discards the run's publication, which
// withdraws its mirror dataset and pushes the pruned proof catalog, so it
// uses the campaign publication client timeout.
type remoteGatewayCampaignPublicationStateStore struct {
	client authcmd.APIClient
}

// NewCampaignPublicationStateStore returns the Gateway-backed publication
// state store. It requires a healthy Gateway.
func NewCampaignPublicationStateStore(fileSvc fs.RuntimeFileService, cfg *config.Config) (evaluation.CampaignPublicationStateStore, error) {
	if !IsGatewayHealthy() {
		return nil, fmt.Errorf("campaign publication: publication state: %w", constants.ErrGatewayUnhealthy)
	}
	client, err := campaignPublicationAPIClientFactory(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("campaign publication: create gateway publication state client: %w", err)
	}
	return &remoteGatewayCampaignPublicationStateStore{client: client}, nil
}

func (s *remoteGatewayCampaignPublicationStateStore) Load(_ context.Context, runID string) (*evaluation.CampaignPublicationState, error) {
	if s == nil || s.client == nil || runID == "" {
		return nil, fmt.Errorf("evaluation: load publication state: %w", constants.ErrMissingRequiredField)
	}
	body, err := s.client.Get(constants.EvalCampaignPublicationStatePath(runID))
	if err != nil {
		return nil, fmt.Errorf("evaluation: load publication state: %w", err)
	}
	var remote models.EvalCampaignPublicationState
	if err := json.Unmarshal(body, &remote); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	proofArtifacts := make(map[string]evaluation.CampaignPublishedProofArtifacts, len(remote.PublishedProofArtifacts))
	for assignmentID, artifacts := range remote.PublishedProofArtifacts {
		proofArtifacts[assignmentID] = evaluation.CampaignPublishedProofArtifacts{
			DatabaseSHA256:      artifacts.DatabaseSHA256,
			AgentTraceSHA256:    artifacts.AgentTraceSHA256,
			ScenarioInputSHA256: artifacts.ScenarioInputSHA256,
			ScenarioGoldSHA256:  artifacts.ScenarioGoldSHA256,
		}
	}
	return &evaluation.CampaignPublicationState{
		SchemaVersion:           remote.SchemaVersion,
		RunID:                   remote.RunID,
		PublishedIdempotency:    remote.PublishedIdempotency,
		PublishedProofArtifacts: proofArtifacts,
		LastPublishedSequence:   remote.LastPublishedSequence,
	}, nil
}

func (s *remoteGatewayCampaignPublicationStateStore) Delete(_ context.Context, runID string) error {
	if s == nil || s.client == nil || runID == "" {
		return fmt.Errorf("evaluation: delete publication state: %w", constants.ErrMissingRequiredField)
	}
	body, err := s.client.Delete(constants.EvalCampaignPublicationStatePath(runID))
	if err != nil {
		return fmt.Errorf("evaluation: delete publication state: %w", err)
	}
	var resp models.EvalCampaignPublicationDiscardResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if !resp.Discarded || resp.RunID != runID {
		return fmt.Errorf("evaluation: delete publication state %s: %w", runID, constants.ErrEvidenceScopeMismatch)
	}
	return nil
}

func (s *remoteGatewayCampaignPublicationStateStore) Save(_ context.Context, state *evaluation.CampaignPublicationState) error {
	if s == nil || s.client == nil || state == nil || state.RunID == "" {
		return fmt.Errorf("evaluation: save publication state: %w", constants.ErrMissingRequiredField)
	}
	proofArtifacts := make(map[string]models.EvalCampaignPublishedProofArtifacts, len(state.PublishedProofArtifacts))
	for assignmentID, artifacts := range state.PublishedProofArtifacts {
		proofArtifacts[assignmentID] = models.EvalCampaignPublishedProofArtifacts{
			DatabaseSHA256:      artifacts.DatabaseSHA256,
			AgentTraceSHA256:    artifacts.AgentTraceSHA256,
			ScenarioInputSHA256: artifacts.ScenarioInputSHA256,
			ScenarioGoldSHA256:  artifacts.ScenarioGoldSHA256,
		}
	}
	if _, err := s.client.Put(constants.EvalCampaignPublicationStatePath(state.RunID), models.EvalCampaignPublicationState{
		SchemaVersion:           state.SchemaVersion,
		RunID:                   state.RunID,
		PublishedIdempotency:    state.PublishedIdempotency,
		PublishedProofArtifacts: proofArtifacts,
		LastPublishedSequence:   state.LastPublishedSequence,
	}); err != nil {
		return fmt.Errorf("evaluation: save publication state: %w", err)
	}
	return nil
}
