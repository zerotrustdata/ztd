// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storagetest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
)

// ChaosEventSummary is one category/outcome aggregate of a chaos run's chaos_events table.
type ChaosEventSummary struct {
	Category string
	Outcome  string
	Count    int
}

// ChaosRunDatabaseExists reports whether the chaos run rooted at runFileSvc has
// the chaos.db that OpenChaosEventStore writes.
func ChaosRunDatabaseExists(ctx context.Context, runFileSvc fs.RuntimeFileService) (bool, error) {
	return runFileSvc.FileExists(ctx, constants.ChaosDBRelPath)
}

// ReadChaosEventSummary opens the chaos run's chaos.db read-only and
// aggregates its chaos_events by category and outcome.
func ReadChaosEventSummary(ctx context.Context, runFileSvc fs.RuntimeFileService, logger *slog.Logger) ([]ChaosEventSummary, error) {
	db, err := sqliteutil.OpenReadOnlyDB(sqliteutil.DefaultDBConfig(runFileSvc.Resolve(constants.ChaosDBRelPath)), logger)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreOpenDBFailed, err)
	}
	defer db.Close()

	const query = "SELECT category, outcome, COUNT(*) FROM chaos_events GROUP BY category, outcome ORDER BY category, outcome"
	results, err := sqliteutil.MaterializeRows(ctx, db, query, nil, func(rows *sql.Rows) (ChaosEventSummary, error) {
		var s ChaosEventSummary
		if err := rows.Scan(&s.Category, &s.Outcome, &s.Count); err != nil {
			return s, fmt.Errorf("%w: %w", constants.ErrAuditScanFailed, err)
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditQueryFailed, err)
	}
	return results, nil
}
