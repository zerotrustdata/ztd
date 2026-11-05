// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"context"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
)

// RunBundleRecords returns the accepted snapshot rows of one run's assignment
// result projections in one source, ordered by sequence. Withdrawn datasets are
// excluded. Identity is derived by parseSnapshotRecordIdentity, the same
// derivation the snapshot uses to key rows.
func (s *PublicMirrorStore) RunBundleRecords(ctx context.Context, sourceID, runID string) ([]models.PublicFeedRecord, error) {
	rows, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT sequence, record_type, record_hash, record_bytes FROM snapshot_records r
		WHERE source_id = ? AND dataset_id = ? AND NOT EXISTS (SELECT 1 FROM withdrawn_datasets w WHERE w.source_id = r.source_id AND w.dataset_id = r.dataset_id)
		ORDER BY sequence`, []interface{}{sourceID, campaignMirrorDatasetID(runID)}, scanPublicMirrorRecord)
	if err != nil {
		return nil, fmt.Errorf("mirror run bundle: read snapshot rows: %w", err)
	}
	records := make([]models.PublicFeedRecord, 0, len(rows))
	for _, row := range rows {
		key, identity, event, err := parseSnapshotRecordIdentity(row)
		if err != nil {
			return nil, fmt.Errorf("mirror run bundle: %w", err)
		}
		if key == "" || event || identity.Kind != publicSnapshotAssignmentResultKind {
			continue
		}
		records = append(records, row)
	}
	return records, nil
}
