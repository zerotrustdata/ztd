// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// outboxTestChain builds count chained entries whose batches are signed by publisher.
func outboxTestChain(t *testing.T, publisher *PublicPublisherService, count int) []models.PublicOutboxEntry {
	t.Helper()
	entries := make([]models.PublicOutboxEntry, count)
	for index := range entries {
		sequence := int64(index + 1)
		batch, err := publisher.BuildBatch([]models.PublicFeedRecord{
			makeProjectionRecord(t, sequence, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"})),
		})
		require.NoError(t, err)
		encoded, err := json.Marshal(batch)
		require.NoError(t, err)
		entries[index] = models.PublicOutboxEntry{
			Sequence:   sequence,
			BatchHash:  batch.ContentHash,
			BatchBytes: string(encoded),
			Status:     models.PublicFeedOutboxStatusPending,
			CreatedAt:  time.Now().UTC(),
		}
		publisher.mu.Lock()
		publisher.highWaterSeq = batch.LastSequence
		publisher.feedChainHash = batch.ContentHash
		publisher.mu.Unlock()
	}
	return entries
}

func outboxTestFileInfo(t *testing.T, publisher *PublicPublisherService) os.FileInfo {
	t.Helper()
	info, err := publisher.fileSvc.Lstat(context.Background(), constants.PublicFeedOutboxPath)
	require.NoError(t, err)
	return info
}

// A status change appends one small line to the same file instead of rewriting
// the outbox, and a fresh store replays the journal to the same entries.
func TestOutbox_StatusChangesAppendAndReplay(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	ctx := context.Background()
	entries := outboxTestChain(t, publisher, 3)
	for _, entry := range entries {
		require.NoError(t, publisher.outbox.Append(ctx, entry))
	}
	before := outboxTestFileInfo(t, publisher)

	now := time.Now().UTC()
	acknowledged := entries[1]
	acknowledged.Status = models.PublicFeedOutboxStatusAcknowledged
	acknowledged.AcknowledgedAt = &now
	require.NoError(t, publisher.outbox.Update(ctx, acknowledged))

	after := outboxTestFileInfo(t, publisher)
	assert.True(t, os.SameFile(before, after), "a status change must not replace the outbox file")
	assert.Less(t, after.Size()-before.Size(), int64(len(entries[1].BatchBytes)), "a status change must not rewrite the batch")

	replayed, err := newRuntimePublicOutboxStore(publisher.fileSvc).List(ctx)
	require.NoError(t, err)
	require.Len(t, replayed, 3)
	assert.Equal(t, models.PublicFeedOutboxStatusAcknowledged, replayed[1].Status)
	assert.Equal(t, entries[1].BatchBytes, replayed[1].BatchBytes)
	assert.Equal(t, models.PublicFeedOutboxStatusPending, replayed[2].Status)

	raw, err := ReadPublicOutboxEntries(ctx, publisher.fileSvc)
	require.NoError(t, err)
	require.Len(t, raw, 3)
	assert.Equal(t, models.PublicFeedOutboxStatusAcknowledged, raw[1].Status)
}

// A store sees lines another process appended, and a full rewrite by another
// process, without being told.
func TestOutbox_FollowsOtherProcessWrites(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	ctx := context.Background()
	entries := outboxTestChain(t, publisher, 3)
	require.NoError(t, publisher.outbox.Append(ctx, entries[0]))

	other := newRuntimePublicOutboxStore(publisher.fileSvc)
	require.NoError(t, other.Append(ctx, entries[1]))
	listed, err := publisher.outbox.List(ctx)
	require.NoError(t, err)
	assert.Len(t, listed, 2)

	require.NoError(t, other.Replace(ctx, entries[:1]))
	listed, err = publisher.outbox.List(ctx)
	require.NoError(t, err)
	assert.Len(t, listed, 1)
}

// A line torn by a crash was never transmitted: it is not applied, and the next
// append discards it.
func TestOutbox_DiscardsTornTail(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	ctx := context.Background()
	entries := outboxTestChain(t, publisher, 2)
	require.NoError(t, publisher.outbox.Append(ctx, entries[0]))

	file, err := publisher.fileSvc.OpenForAppend(ctx, constants.PublicFeedOutboxPath, constants.PermFilePrivate)
	require.NoError(t, err)
	_, err = file.WriteString(`{"sequence":2,"batch_hash":"torn`)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	store := newRuntimePublicOutboxStore(publisher.fileSvc)
	listed, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, listed, 1)

	require.NoError(t, store.Append(ctx, entries[1]))
	listed, err = newRuntimePublicOutboxStore(publisher.fileSvc).List(ctx)
	require.NoError(t, err)
	assert.Len(t, listed, 2)
}

// Pruning takes entries out of the view at once and leaves a small journal in
// place. Past the size floor, it compacts the journal to what is live.
func TestOutbox_PruneCompactsPastThreshold(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	ctx := context.Background()
	entries := outboxTestChain(t, publisher, 3)
	store := publisher.outbox.(*runtimePublicOutboxStore)
	acknowledgedAt := time.Now().UTC().Add(-time.Hour)
	for index := range entries[:2] {
		entries[index].Status = models.PublicFeedOutboxStatusAcknowledged
		entries[index].AcknowledgedAt = &acknowledgedAt
	}
	require.NoError(t, store.Append(ctx, entries[0]))
	initial := outboxTestFileInfo(t, publisher)

	require.NoError(t, store.PruneAcknowledged(ctx, time.Now().UTC()))
	listed, err := store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listed)
	assert.True(t, os.SameFile(initial, outboxTestFileInfo(t, publisher)), "a journal under the floor is left in place")

	require.NoError(t, store.Append(ctx, entries[1]))
	require.NoError(t, store.Append(ctx, entries[2]))
	store.compactMinBytes = 1
	require.NoError(t, store.PruneAcknowledged(ctx, time.Now().UTC()))
	listed, err = store.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, entries[2].Sequence, listed[0].Sequence)
	assert.False(t, os.SameFile(initial, outboxTestFileInfo(t, publisher)), "a journal past the floor is compacted")

	replayed, err := newRuntimePublicOutboxStore(publisher.fileSvc).List(ctx)
	require.NoError(t, err)
	require.Len(t, replayed, 1)
	assert.Equal(t, entries[2].Sequence, replayed[0].Sequence)
}
