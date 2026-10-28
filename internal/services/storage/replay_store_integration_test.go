// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newTestReplayStore builds a ReplayStore over a fresh on-disk ztd.db.
func newTestReplayStore(t *testing.T) (*ReplayStore, *sqliteutil.DB) {
	t.Helper()
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	rs, err := NewReplayStore(db, logger)
	require.NoError(t, err)
	return rs, db
}

func TestReplayStore_NilDB(t *testing.T) {
	rs, err := NewReplayStore(nil, testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrStorageConfigRequired)
	assert.Nil(t, rs)
}

func TestReplayStore_ReserveReplayReleaseReserve(t *testing.T) {
	rs, _ := newTestReplayStore(t)
	expiresAt := time.Now().UTC().Add(time.Hour)

	replay, err := rs.ReserveNonce("n1", expiresAt)
	require.NoError(t, err)
	assert.False(t, replay, "first reservation is not a replay")

	replay, err = rs.ReserveNonce("n1", expiresAt)
	require.NoError(t, err)
	assert.True(t, replay, "second reservation is a replay")

	require.NoError(t, rs.ReleaseNonce("n1"))

	replay, err = rs.ReserveNonce("n1", expiresAt)
	require.NoError(t, err)
	assert.False(t, replay, "released nonce can be reserved again")

	require.NoError(t, rs.FinalizeNonce("n1"))
	replay, err = rs.ReserveNonce("n1", expiresAt)
	require.NoError(t, err)
	assert.True(t, replay, "finalized nonce stays a replay")
}

func TestReplayStore_ReserveNonce_Concurrent(t *testing.T) {
	rs, _ := newTestReplayStore(t)
	expiresAt := time.Now().UTC().Add(time.Hour)

	const workers = 8
	var wg sync.WaitGroup
	results := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replay, err := rs.ReserveNonce("concurrent", expiresAt)
			assert.NoError(t, err)
			results <- !replay
		}()
	}
	wg.Wait()
	close(results)

	won := 0
	for ok := range results {
		if ok {
			won++
		}
	}
	assert.Equal(t, 1, won, "exactly one concurrent reservation wins")
}

func TestReplayStore_FinalizeAndReleaseAreScopedToReserved(t *testing.T) {
	rs, _ := newTestReplayStore(t)
	expiresAt := time.Now().UTC().Add(time.Hour)

	require.NoError(t, rs.FinalizeNonce("never-reserved"), "finalizing an unknown nonce is a no-op")
	require.NoError(t, rs.ReleaseNonce("never-reserved"), "releasing an unknown nonce is a no-op")

	_, err := rs.ReserveNonce("used", expiresAt)
	require.NoError(t, err)
	require.NoError(t, rs.FinalizeNonce("used"))
	require.NoError(t, rs.ReleaseNonce("used"))

	rows, err := rs.ListNonces(10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1, "release must not delete a finalized nonce")
	assert.Equal(t, "used", rows[0].Status)
	require.NotNil(t, rows[0].UsedAt)
}

func TestReplayStore_CleanupExpiredNonces(t *testing.T) {
	rs, _ := newTestReplayStore(t)

	_, err := rs.ReserveNonce("expired", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	_, err = rs.ReserveNonce("valid", time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)

	require.NoError(t, rs.CleanupExpiredNonces())

	rows, err := rs.ListNonces(10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "valid", rows[0].Nonce)
}

func TestReplayStore_ListNonces_Pagination(t *testing.T) {
	rs, _ := newTestReplayStore(t)
	expiresAt := time.Now().UTC().Add(time.Hour)

	for _, n := range []string{"a", "b", "c"} {
		_, err := rs.ReserveNonce(n, expiresAt)
		require.NoError(t, err)
	}

	page1, err := rs.ListNonces(2, 0)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	page2, err := rs.ListNonces(2, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)

	assert.Equal(t, "reserved", page1[0].Status)
	assert.Nil(t, page1[0].UsedAt)
	assert.WithinDuration(t, expiresAt, page1[0].ExpiresAt, time.Second)
	assert.WithinDuration(t, time.Now().UTC(), page1[0].ReservedAt, time.Minute)

	all, err := rs.ListNonces(0, 0)
	require.NoError(t, err)
	assert.Len(t, all, 3, "non-positive limit falls back to the default page size")
}

func TestReplayStore_ClosedDatabaseFailsClosed(t *testing.T) {
	rs, db := newTestReplayStore(t)
	require.NoError(t, db.Close())

	replay, err := rs.ReserveNonce("n", time.Now().UTC().Add(time.Hour))
	require.Error(t, err, "a database failure must not read as a successful reservation")
	assert.False(t, replay)
}
