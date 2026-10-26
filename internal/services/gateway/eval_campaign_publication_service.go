// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

const evalCampaignPublicationStateSchemaVersion = "1.0.0"

// EvalCampaignPublicationService persists native eval campaign publication
// idempotency in the gateway document store and discards a run's publication
// from the public spectator runtime.
type EvalCampaignPublicationService struct {
	docStore  *DocumentStoreService
	spectator func() *PublicSpectatorRuntime
}

// NewEvalCampaignPublicationService returns the service. spectator resolves
// the public spectator runtime, which may be absent or stopped.
func NewEvalCampaignPublicationService(docStore *DocumentStoreService, spectator func() *PublicSpectatorRuntime) (*EvalCampaignPublicationService, error) {
	if docStore == nil || spectator == nil {
		return nil, fmt.Errorf("eval campaign publication: %w", constants.ErrMissingRequiredField)
	}
	return &EvalCampaignPublicationService{docStore: docStore, spectator: spectator}, nil
}

// Discard withdraws one run's dataset from anonymous mirror reads, removes its
// proof artifacts from the proof catalog and pushes the catalog, then clears
// its publication idempotency. The signed batch chain is not rewritten. Each
// step is idempotent, so a failed discard can be retried.
func (s *EvalCampaignPublicationService) Discard(ctx context.Context, runID string) error {
	if s == nil || s.spectator == nil || runID == "" {
		return fmt.Errorf("eval campaign publication: %w", constants.ErrMissingRequiredField)
	}
	runtime := s.spectator()
	if mirror := runtime.Mirror(); mirror != nil {
		if err := mirror.WithdrawCampaignDataset(ctx, runID); err != nil {
			return fmt.Errorf("eval campaign publication: withdraw dataset: %w", err)
		}
	}
	if publisher := runtime.Publisher(); publisher != nil && runtime.Running() {
		removed, err := publisher.PruneProofCatalogForRun(ctx, runID)
		if err != nil {
			return fmt.Errorf("eval campaign publication: prune proof catalog: %w", err)
		}
		if removed > 0 {
			if _, err := publisher.FlushProofPackage(ctx); err != nil {
				return fmt.Errorf("eval campaign publication: push proof catalog: %w", err)
			}
		}
	}
	return s.Delete(ctx, runID)
}

func (s *EvalCampaignPublicationService) Get(ctx context.Context, runID string) (*models.EvalCampaignPublicationState, error) {
	if s == nil || s.docStore == nil || runID == "" {
		return nil, fmt.Errorf("eval campaign publication: %w", constants.ErrMissingRequiredField)
	}
	collection := marshaler.CollectionName(constants.CollectionEvalCampaignPublicationState)
	doc, err := s.docStore.DocGet(ctx, collection, runID)
	if err != nil {
		return nil, fmt.Errorf("eval campaign publication: get: %w", err)
	}
	if doc == nil {
		return &models.EvalCampaignPublicationState{
			SchemaVersion:        evalCampaignPublicationStateSchemaVersion,
			RunID:                runID,
			PublishedIdempotency: []string{},
		}, nil
	}
	state := &models.EvalCampaignPublicationState{}
	if err := doc.DecodeData(state); err != nil {
		return nil, fmt.Errorf("eval campaign publication: decode: %w", err)
	}
	if state.SchemaVersion == "" {
		state.SchemaVersion = evalCampaignPublicationStateSchemaVersion
	}
	if state.RunID == "" {
		state.RunID = runID
	}
	if state.PublishedIdempotency == nil {
		state.PublishedIdempotency = []string{}
	}
	return state, nil
}

func (s *EvalCampaignPublicationService) Delete(ctx context.Context, runID string) error {
	if s == nil || s.docStore == nil || runID == "" {
		return fmt.Errorf("eval campaign publication: %w", constants.ErrMissingRequiredField)
	}
	collection := marshaler.CollectionName(constants.CollectionEvalCampaignPublicationState)
	if err := s.docStore.DocDelete(ctx, collection, runID); err != nil {
		return fmt.Errorf("eval campaign publication: delete: %w", err)
	}
	return nil
}

func (s *EvalCampaignPublicationService) Put(ctx context.Context, state models.EvalCampaignPublicationState) error {
	if s == nil || s.docStore == nil || state.RunID == "" {
		return fmt.Errorf("eval campaign publication: %w", constants.ErrMissingRequiredField)
	}
	if state.SchemaVersion == "" {
		state.SchemaVersion = evalCampaignPublicationStateSchemaVersion
	}
	if state.PublishedIdempotency == nil {
		state.PublishedIdempotency = []string{}
	}
	sort.Strings(state.PublishedIdempotency)
	body, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("eval campaign publication: marshal: %w", err)
	}
	collection := marshaler.CollectionName(constants.CollectionEvalCampaignPublicationState)
	if err := s.docStore.DocSet(ctx, collection, state.RunID, body); err != nil {
		return fmt.Errorf("eval campaign publication: put: %w", err)
	}
	return nil
}
