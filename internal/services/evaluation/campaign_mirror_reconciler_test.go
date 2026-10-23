// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingCampaignMirrorProbe struct{}

func (blockingCampaignMirrorProbe) DatasetPresent(ctx context.Context, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func TestCampaignMirrorReconciler_ReportsMissingHostEvidence(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	reconciler := NewCampaignMirrorReconciler(coordinator, store, blockingCampaignMirrorProbe{})
	result, err := reconciler.reconcileRuns(context.Background(), []string{"run-absent"}, time.Minute, true, true, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"run-absent"}, result.HostAbsentRunIDs)
	assert.Empty(t, result.FailedRuns)
	assert.Empty(t, exporter.records)
}
