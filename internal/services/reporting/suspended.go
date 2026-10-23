// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package reporting

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/storage"
)

func reportSuspendedTransactions(ctx context.Context, outDir string, sts storage.SuspendedTransactionStore) (FileResult, error) {
	path := filepath.Join(outDir, constants.ReportSuspendedTxFilename)

	if ctx.Err() != nil {
		return FileResult{}, ctx.Err()
	}

	active, err := sts.ListSuspendedTransactions(ctx, "")
	if err != nil {
		return FileResult{}, fmt.Errorf("%w: suspended_transactions (active): %w", constants.ErrReportStoreUnavailable, err)
	}

	expired, err := sts.GetExpiredSuspendedTransactions(ctx)
	if err != nil {
		return FileResult{}, fmt.Errorf("%w: suspended_transactions (expired): %w", constants.ErrReportStoreUnavailable, err)
	}

	rows := make([]Row, 0, len(active)+len(expired))
	for i, tx := range append(active, expired...) {
		status := constants.SuspendedTxStatusPending
		switch {
		case tx.ClaimedAt != nil:
			status = constants.SuspendedTxStatusClaimed
		case i >= len(active):
			status = constants.SuspendedTxStatusExpired
		}

		rows = append(rows, SuspendedTxRow{
			TxHash:       tx.TransactionHash,
			UserID:       tx.UserID,
			ActionType:   tx.ToolName,
			Status:       string(status),
			CreatedAtUTC: utcRFC3339(tx.CreatedAt),
			ExpiresAtUTC: utcRFC3339(tx.ExpiresAt),
		})
	}

	res, err := writeCSV(path, SuspendedTxRow{}.Columns(), rows)
	if err != nil {
		return FileResult{}, fmt.Errorf("%w: suspended_transactions: %w", constants.ErrReportWriteFailed, err)
	}
	res.Filename = constants.ReportSuspendedTxFilename
	return res, nil
}
