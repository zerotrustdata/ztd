// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storagetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestReadChaosEventSummary_ReadsStoreWrittenRun proves the summary reader finds
// the chaos.db at the location the chaos event store writes it and aggregates its rows.
func TestReadChaosEventSummary_ReadsStoreWrittenRun(t *testing.T) {
	ctx := context.Background()
	runDir := testutil.TempDir(t)
	runFileSvc := NewTestFileSvc(t, runDir)

	exists, err := ChaosRunDatabaseExists(ctx, runFileSvc)
	require.NoError(t, err)
	require.False(t, exists)

	store, err := OpenChaosEventStore(runFileSvc, testutil.NewTestLogger())
	require.NoError(t, err)

	now := time.Now().UTC()
	require.NoError(t, store.RecordChaosEvents([]*ChaosEvent{
		{OperatorSessionID: "chaos-session", Timestamp: now, ChaosID: 1, Category: "doctrine", Outcome: "blocked"},
		{OperatorSessionID: "chaos-session", Timestamp: now, ChaosID: 2, Category: "doctrine", Outcome: "blocked"},
		{OperatorSessionID: "chaos-session", Timestamp: now, ChaosID: 3, Category: "warden", Outcome: "allowed"},
	}))
	require.NoError(t, store.Close())

	exists, err = ChaosRunDatabaseExists(ctx, runFileSvc)
	require.NoError(t, err)
	require.True(t, exists)

	summary, err := ReadChaosEventSummary(ctx, runFileSvc, testutil.NewTestLogger())
	require.NoError(t, err)
	require.Equal(t, []ChaosEventSummary{
		{Category: "doctrine", Outcome: "blocked", Count: 2},
		{Category: "warden", Outcome: "allowed", Count: 1},
	}, summary)
}
