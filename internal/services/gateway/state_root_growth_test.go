// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// seedHistoryDocuments inserts n receipt-sized documents in one transaction,
// standing in for accumulated audit/receipt history.
func seedHistoryDocuments(tb testing.TB, db *CanonicalDBService, n int) {
	tb.Helper()
	body := strings.Repeat("x", 4096)
	now := timesvc.FormatTimestamp(time.Now().UTC())
	require.NoError(tb, db.db.ExecInTxWithRetry(tb.Context(), func(tx *sql.Tx) error {
		stmt, err := tx.Prepare("INSERT INTO documents (collection, id, data, created_at, updated_at) VALUES ('history', ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 0; i < n; i++ {
			if _, err := stmt.Exec(fmt.Sprintf("h-%06d", i), fmt.Sprintf(`{"seq":%d,"body":%q}`, i, body), now, now); err != nil {
				return err
			}
		}
		return nil
	}))
}

// BenchmarkStateRoot_OneWriteAtGrowingHistory measures the root read that
// follows one ordinary write, at growing history sizes. Each governed mutation
// reads the root after a write (L5 state_after), so this is the per-envelope
// commitment cost. The bounded-work assertion is
// TestStateRootService_OneWriteWorkIsIndependentOfHistory.
func BenchmarkStateRoot_OneWriteAtGrowingHistory(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("history=%d", n), func(b *testing.B) {
			db := newTestDB(b)
			seedHistoryDocuments(b, db, n)
			svc := db.GetStateRootSvc()
			_, err := svc.GetCurrentStateRoot(b.Context())
			require.NoError(b, err)

			i := 0
			for b.Loop() {
				b.StopTimer()
				require.NoError(b, db.GetDocStore().DocSet(b.Context(), "operators", "op-1", json.RawMessage(fmt.Sprintf(`{"heartbeat":%d}`, i))))
				i++
				b.StartTimer()
				_, err := svc.GetCurrentStateRoot(b.Context())
				require.NoError(b, err)
			}
		})
	}
}
