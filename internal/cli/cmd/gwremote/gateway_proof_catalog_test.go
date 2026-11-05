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
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

func auditProofInput(id string) evaluation.AssignmentAuditProofInput {
	return evaluation.AssignmentAuditProofInput{
		CampaignID:       "campaign-" + id,
		CampaignRevision: "rev-" + id,
		RunID:            "run-" + id,
		AssignmentID:     "assignment-" + id,
		IndexDigest:      "digest-" + id,
	}
}

// The Gateway's answers to proof publication, prune, flush and feed export are
// covered against a real Gateway in gateway_publication_integration_test.go.
// The tests here keep only the guards that return before any Gateway call and
// the pure classification helpers.

func TestRemoteGatewayCampaignProofPublisher_IngestAssignmentAuditSlices(t *testing.T) {
	t.Run("a nil publisher or client reports a missing required field", func(t *testing.T) {
		var nilPublisher *remoteGatewayCampaignProofPublisher
		_, err := nilPublisher.IngestAssignmentAuditSlices(context.Background(), []evaluation.AssignmentAuditProofInput{auditProofInput("1")}, false)
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)

		_, err = (&remoteGatewayCampaignProofPublisher{}).IngestAssignmentAuditSlices(context.Background(), []evaluation.AssignmentAuditProofInput{auditProofInput("1")}, false)
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	})

	t.Run("no inputs is a no-op that never calls the gateway", func(t *testing.T) {
		client := &cmdtest.MockAPIClient{}
		publisher := &remoteGatewayCampaignProofPublisher{client: client}

		responses, err := publisher.IngestAssignmentAuditSlices(context.Background(), nil, true)
		require.NoError(t, err)
		assert.Empty(t, responses)
		assert.Empty(t, client.PostCalls)
	})
}

func TestRemoteGatewayCampaignProofPublisher_PruneRunProofCatalog(t *testing.T) {
	t.Run("is a no-op for a nil publisher, nil client, or empty run id", func(t *testing.T) {
		var nilPublisher *remoteGatewayCampaignProofPublisher
		require.NoError(t, nilPublisher.PruneRunProofCatalog(context.Background(), "run-1"))
		require.NoError(t, (&remoteGatewayCampaignProofPublisher{}).PruneRunProofCatalog(context.Background(), "run-1"))

		client := &cmdtest.MockAPIClient{}
		require.NoError(t, (&remoteGatewayCampaignProofPublisher{client: client}).PruneRunProofCatalog(context.Background(), ""))
		assert.Empty(t, client.PostCalls)
	})
}

func TestRemoteGatewayCampaignProofPublisher_PublishRunBundlePostsCampaignIdentity(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	responseBody, err := json.Marshal(models.PublicRunBundleResponse{Accepted: true, RunID: "run-1", BundleSHA256: digest})
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{PostResp: responseBody}
	publisher := &remoteGatewayCampaignProofPublisher{client: client}

	got, err := publisher.PublishRunBundle(context.Background(), evaluation.RunBundleInput{
		CampaignID:       "campaign-1",
		CampaignRevision: "rev-1",
		RunID:            "run-1",
	})
	require.NoError(t, err)
	assert.Equal(t, digest, got)

	require.Len(t, client.PostCalls, 1)
	assert.Equal(t, constants.APIPaths.PublicFeedProofsRunBundle, client.PostCalls[0].Path)
	posted, err := json.Marshal(client.PostCalls[0].Body)
	require.NoError(t, err)
	var fields map[string]string
	require.NoError(t, json.Unmarshal(posted, &fields))
	assert.Equal(t, map[string]string{
		"campaign_id":       "campaign-1",
		"campaign_revision": "rev-1",
		"run_id":            "run-1",
	}, fields)
}

func TestRemoteGatewayCampaignProofPublisher_FlushProofCatalog(t *testing.T) {
	t.Run("a nil publisher or client reports a missing required field", func(t *testing.T) {
		var nilPublisher *remoteGatewayCampaignProofPublisher
		require.ErrorIs(t, nilPublisher.FlushProofCatalog(context.Background()), constants.ErrMissingRequiredField)
		require.ErrorIs(t, (&remoteGatewayCampaignProofPublisher{}).FlushProofCatalog(context.Background()), constants.ErrMissingRequiredField)
	})
}

func TestNewCampaignProofPublisher(t *testing.T) {
	t.Run("requires a healthy gateway", func(t *testing.T) {
		cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)
		cfg.Paths.Host = "http://127.0.0.1:1"

		publisher, err := NewCampaignProofPublisher(context.Background(), fileSvc, cfg)
		require.ErrorIs(t, err, constants.ErrGatewayUnhealthy)
		assert.Nil(t, publisher)
	})
}

func TestIsPublicFeedOutboxPublicationError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "outbox equivocation", err: constants.ErrPublicFeedOutboxEquivocation, want: true},
		{name: "outbox corrupt", err: constants.ErrPublicFeedOutboxCorrupt, want: true},
		{name: "hash chain mismatch", err: constants.ErrPublicFeedHashChainMismatch, want: true},
		{name: "sentinel survives wrapping and HTTP body embedding", err: fmt.Errorf("gateway publication: %w", api.DecodeGatewayError(http.StatusConflict, []byte(fmt.Sprintf(`{"error":%q}`, constants.ErrPublicFeedOutboxCorrupt.Error())))), want: true},
		{name: "outbox empty is not a publication fault", err: constants.ErrPublicFeedOutboxEmpty, want: false},
		{name: "unrelated error", err: errors.New("connection reset"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isPublicFeedOutboxPublicationError(tt.err))
		})
	}
}

func TestIsPublicFeedPublicationRetryable(t *testing.T) {
	assert.True(t, isPublicFeedPublicationRetryable(constants.ErrPublicFeedSequenceOutOfOrder))
	assert.True(t, isPublicFeedPublicationRetryable(constants.ErrPublicFeedHashChainMismatch))
	assert.False(t, isPublicFeedPublicationRetryable(errors.New("connection reset")))
	assert.False(t, isPublicFeedPublicationRetryable(nil))
}

func TestRemoteGatewayCampaignFeedExporter_HighWaterSequence(t *testing.T) {
	t.Run("a cached high water is served without calling the gateway", func(t *testing.T) {
		// The exporter has no client, so any Gateway call would panic the test.
		exporter := &remoteGatewayCampaignFeedExporter{highWater: 9}

		got, err := exporter.HighWaterSequence(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(9), got)
	})
}
