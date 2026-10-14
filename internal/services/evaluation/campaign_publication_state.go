// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import "context"

// CampaignPublishedProofArtifacts stores content-addressed proof hashes for one
// assignment so force restore can skip rebuilding unchanged audit exports.
type CampaignPublishedProofArtifacts struct {
	DatabaseSHA256 string
	VaultKeySHA256 string
}

// CampaignPublicationState tracks exported public projection idempotency keys
// for one campaign run. The gateway document store is authoritative.
type CampaignPublicationState struct {
	SchemaVersion           string
	RunID                   string
	PublishedIdempotency    []string
	PublishedProofArtifacts map[string]CampaignPublishedProofArtifacts
	LastPublishedSequence   int64
}

// CampaignPublicationStateStore persists publication idempotency in the
// gateway-owned eval state plane. Delete discards the run's publication: the
// Gateway also withdraws the run from the public mirror and prunes its proof
// artifacts.
type CampaignPublicationStateStore interface {
	Load(ctx context.Context, runID string) (*CampaignPublicationState, error)
	Save(ctx context.Context, state *CampaignPublicationState) error
	Delete(ctx context.Context, runID string) error
}

func cloneCampaignPublicationState(state *CampaignPublicationState) *CampaignPublicationState {
	if state == nil {
		return nil
	}
	keys := append([]string(nil), state.PublishedIdempotency...)
	proofArtifacts := make(map[string]CampaignPublishedProofArtifacts, len(state.PublishedProofArtifacts))
	for assignmentID, artifacts := range state.PublishedProofArtifacts {
		proofArtifacts[assignmentID] = artifacts
	}
	return &CampaignPublicationState{
		SchemaVersion:           state.SchemaVersion,
		RunID:                   state.RunID,
		PublishedIdempotency:    keys,
		PublishedProofArtifacts: proofArtifacts,
		LastPublishedSequence:   state.LastPublishedSequence,
	}
}
