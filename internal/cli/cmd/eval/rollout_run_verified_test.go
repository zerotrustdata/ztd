// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

// markVerifiedOn queues qwen3:4b as verified by a prepared run of a formation
// campaign over the given suite.
func markVerifiedOn(t *testing.T, env *runEnv, suiteID string) {
	t.Helper()
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	campaignID := "verified-on-" + suiteID
	_, err := createCampaign(context.Background(), env.deps, env.fileSvc(t), campaignCreateSpec{
		Platform:     testPlatform,
		CampaignID:   campaignID,
		Variants:     testFormationCatalogCLIVariants(),
		Repetitions:  1,
		Formations:   true,
		FormationIDs: []string{testFormationID},
		SuiteID:      suiteID,
	})
	require.NoError(t, err)
	runID := env.startPrepared(t, campaignID, "run-verified")
	_, err = evaluation.MarkCampaignQueueEntry(evaluation.MarkCampaignQueueEntryRequest{
		FileService:   env.fileSvc(t),
		VariantID:     "qwen3-4b",
		Status:        evaluation.QueueStatusVerified,
		VerifiedRunID: runID,
	})
	require.NoError(t, err)
}
