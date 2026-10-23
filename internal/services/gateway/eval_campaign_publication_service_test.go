// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newEvalCampaignPublicationServiceTest(t *testing.T) *EvalCampaignPublicationService {
	t.Helper()
	logger := testutil.NewTestLogger()
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(":memory:"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(gatewaySchema)
	require.NoError(t, err)

	service, err := NewEvalCampaignPublicationService(NewDocumentStoreService(db, logger), func() *PublicSpectatorRuntime { return nil })
	require.NoError(t, err)
	return service
}

func TestNewEvalCampaignPublicationService_RejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	logger := testutil.NewTestLogger()
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(":memory:"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = NewEvalCampaignPublicationService(nil, func() *PublicSpectatorRuntime { return nil })
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	_, err = NewEvalCampaignPublicationService(NewDocumentStoreService(db, logger), nil)
	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestEvalCampaignPublicationService_DiscardWithoutSpectatorClearsState(t *testing.T) {
	t.Parallel()

	service := newEvalCampaignPublicationServiceTest(t)
	runID := "eval-init-nemotron-3-nano-4b-1792241875"
	require.NoError(t, service.Put(context.Background(), models.EvalCampaignPublicationState{
		RunID:                runID,
		PublishedIdempotency: []string{"run:key-a"},
	}))

	require.NoError(t, service.Discard(context.Background(), runID))

	loaded, err := service.Get(context.Background(), runID)
	require.NoError(t, err)
	assert.Empty(t, loaded.PublishedIdempotency)
}

func TestEvalCampaignPublicationService_PutAndGet(t *testing.T) {
	t.Parallel()

	service := newEvalCampaignPublicationServiceTest(t)
	state := models.EvalCampaignPublicationState{
		SchemaVersion:         evalCampaignPublicationStateSchemaVersion,
		RunID:                 "eval-init-gemma2-9b-1789729690",
		PublishedIdempotency:  []string{"run-1:key-a", "run-1:key-b"},
		LastPublishedSequence: 42,
	}
	require.NoError(t, service.Put(context.Background(), state))

	loaded, err := service.Get(context.Background(), state.RunID)
	require.NoError(t, err)
	assert.Equal(t, state.RunID, loaded.RunID)
	assert.Equal(t, state.LastPublishedSequence, loaded.LastPublishedSequence)
	assert.Equal(t, state.PublishedIdempotency, loaded.PublishedIdempotency)
}
