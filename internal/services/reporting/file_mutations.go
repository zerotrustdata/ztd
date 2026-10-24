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

func reportFileMutations(ctx context.Context, outDir string, store *storage.LocalFirstEventsStore) (FileResult, error) {
	path := filepath.Join(outDir, constants.ReportFileMutationsFilename)

	var rows []Row
	offset := 0
	const batchSize = 500
	for {
		if ctx.Err() != nil {
			return FileResult{}, ctx.Err()
		}
		batch, err := store.ListFileMutations(batchSize, offset)
		if err != nil {
			return FileResult{}, fmt.Errorf("%w: file_mutations: %w", constants.ErrReportStoreUnavailable, err)
		}
		for _, m := range batch {
			rows = append(rows, FileMutationRow{
				ID:               m.ID,
				EventID:          m.EventID,
				Operation:        string(m.Operation),
				LedgerHashBefore: m.LedgerHashBefore,
				LedgerHashAfter:  m.LedgerHashAfter,
			})
		}
		if len(batch) < batchSize {
			break
		}
		offset += batchSize
	}

	res, err := writeCSV(path, FileMutationRow{}.Columns(), rows)
	if err != nil {
		return FileResult{}, fmt.Errorf("%w: file_mutations: %w", constants.ErrReportWriteFailed, err)
	}
	res.Filename = constants.ReportFileMutationsFilename
	return res, nil
}
