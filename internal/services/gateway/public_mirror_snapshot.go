// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
)

// publicSnapshotIdentity reads only the identity header of a versioned public
// projection. The complete payload stays byte-for-byte under its signature.
type publicSnapshotIdentity struct {
	Kind         string          `json:"kind"`
	DatasetID    string          `json:"dataset_id"`
	RunID        string          `json:"run_id"`
	AssignmentID string          `json:"assignment_id"`
	VariantID    string          `json:"variant_id"`
	Role         string          `json:"role"`
	SuiteID      string          `json:"suite_id"`
	EventID      string          `json:"event_id"`
	MessageType  string          `json:"message_type"`
	Record       json.RawMessage `json:"record"`
}

func parseSnapshotRecordIdentity(record models.PublicFeedRecord) (key string, identity publicSnapshotIdentity, event bool, err error) {
	if record.RecordType != models.PublicFeedRecordTypeProjection && record.RecordType != models.PublicFeedRecordTypeEvent {
		return "", publicSnapshotIdentity{}, false, nil
	}
	var id publicSnapshotIdentity
	if err := json.Unmarshal([]byte(record.RecordBytes), &id); err != nil {
		return "", publicSnapshotIdentity{}, false, fmt.Errorf("mirror snapshot: decode identity: %w", err)
	}
	if id.MessageType != "" {
		var assignment publicSnapshotIdentity
		if err := json.Unmarshal(id.Record, &assignment); err != nil {
			return "", publicSnapshotIdentity{}, false, fmt.Errorf("mirror snapshot: decode assignment identity: %w", err)
		}
		assignment.Kind = id.MessageType
		assignment.DatasetID = campaignMirrorDatasetID(assignment.RunID)
		id = assignment
	}
	event = id.EventID != ""
	parts := []string{id.Kind, id.DatasetID, id.RunID, id.AssignmentID, id.VariantID, id.Role, id.SuiteID}
	if event {
		parts = []string{"event", id.DatasetID, id.RunID, id.AssignmentID, id.EventID}
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return "", publicSnapshotIdentity{}, false, fmt.Errorf("mirror snapshot: encode identity: %w", err)
	}
	return string(encoded), id, event, nil
}

func snapshotLifecycleCompanionKey(identity publicSnapshotIdentity) string {
	if identity.Kind == "PublicAssignmentResultProjection" {
		parts := []string{"PublicAssignmentLifecycleRecord", identity.DatasetID, identity.RunID, identity.AssignmentID, identity.VariantID, identity.Role, identity.SuiteID}
		encoded, _ := json.Marshal(parts)
		return string(encoded)
	}
	return ""
}

func snapshotResultCompanionKey(identity publicSnapshotIdentity) string {
	if identity.Kind == "PublicAssignmentLifecycleRecord" {
		parts := []string{"PublicAssignmentResultProjection", identity.DatasetID, identity.RunID, identity.AssignmentID, identity.VariantID, identity.Role, identity.SuiteID}
		encoded, _ := json.Marshal(parts)
		return string(encoded)
	}
	return ""
}

// updatePublicSnapshot runs in the signed-feed append transaction. Only the
// changed entities and a bounded activity tail are touched, never old revisions.
func updatePublicSnapshot(ctx context.Context, tx *sql.Tx, batch models.PublicFeedBatch) error {
	encoded, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("mirror snapshot: encode signed batch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot_batches VALUES (?, ?, ?)`, batch.SourceID, batch.FirstSequence, string(encoded)); err != nil {
		return err
	}
	obsolete := map[int64]bool{batch.FirstSequence: true}
	selectStmt, err := tx.PrepareContext(ctx, `SELECT first_sequence FROM snapshot_records WHERE source_id = ? AND record_key = ?`)
	if err != nil {
		return err
	}
	defer selectStmt.Close()

	deleteStmt, err := tx.PrepareContext(ctx, `DELETE FROM snapshot_records WHERE source_id = ? AND record_key = ?`)
	if err != nil {
		return err
	}
	defer deleteStmt.Close()

	insertStmt, err := tx.PrepareContext(ctx, `INSERT INTO snapshot_records
		(source_id, record_key, dataset_id, sequence, first_sequence, is_event, record_type, record_hash, record_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, record_key) DO UPDATE SET
		sequence=excluded.sequence, first_sequence=excluded.first_sequence,
		record_type=excluded.record_type, record_hash=excluded.record_hash, record_bytes=excluded.record_bytes`)
	if err != nil {
		return err
	}
	defer insertStmt.Close()

	for _, record := range batch.Records {
		key, identity, event, err := parseSnapshotRecordIdentity(record)
		if err != nil {
			return err
		}
		if key == "" {
			continue
		}
		if resultKey := snapshotResultCompanionKey(identity); resultKey != "" {
			var resultFirst int64
			err := selectStmt.QueryRowContext(ctx, batch.SourceID, resultKey).Scan(&resultFirst)
			if err == nil {
				// Terminal result projection already exists for this assignment; do not retain superseded lifecycle row.
				continue
			}
		}
		if lifecycleKey := snapshotLifecycleCompanionKey(identity); lifecycleKey != "" {
			var lifecycleFirst int64
			err := selectStmt.QueryRowContext(ctx, batch.SourceID, lifecycleKey).Scan(&lifecycleFirst)
			if err == nil {
				obsolete[lifecycleFirst] = true
				if _, err := deleteStmt.ExecContext(ctx, batch.SourceID, lifecycleKey); err != nil {
					return err
				}
			}
		}
		var previous int64
		err = selectStmt.QueryRowContext(ctx, batch.SourceID, key).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			obsolete[previous] = true
		}
		if _, err := insertStmt.ExecContext(ctx,
			batch.SourceID, key, identity.DatasetID, record.Sequence, batch.FirstSequence, event, record.RecordType, record.RecordHash, record.RecordBytes); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT first_sequence, record_key FROM snapshot_records WHERE source_id = ? AND is_event = 1 ORDER BY sequence DESC LIMIT -1 OFFSET ?`, batch.SourceID, constants.PublicFeedSSEReplayMaxRecords)
	if err != nil {
		return err
	}
	defer rows.Close()
	var expired []string
	for rows.Next() {
		var first int64
		var key string
		if err := rows.Scan(&first, &key); err != nil {
			return err
		}
		obsolete[first] = true
		expired = append(expired, key)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if len(expired) > 0 {
		delStmt, err := tx.PrepareContext(ctx, `DELETE FROM snapshot_records WHERE source_id = ? AND record_key = ?`)
		if err != nil {
			return err
		}
		defer delStmt.Close()
		for _, key := range expired {
			if _, err := delStmt.ExecContext(ctx, batch.SourceID, key); err != nil {
				return err
			}
		}
	}
	if len(obsolete) > 0 {
		delBatchStmt, err := tx.PrepareContext(ctx, `DELETE FROM snapshot_batches WHERE source_id = ? AND first_sequence = ? AND NOT EXISTS
			(SELECT 1 FROM snapshot_records r WHERE r.source_id = snapshot_batches.source_id AND r.first_sequence = snapshot_batches.first_sequence)`)
		if err != nil {
			return err
		}
		defer delBatchStmt.Close()
		for first := range obsolete {
			if _, err := delBatchStmt.ExecContext(ctx, batch.SourceID, first); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshot_heads VALUES (?, ?) ON CONFLICT(source_id) DO UPDATE SET high_water_sequence=excluded.high_water_sequence`, batch.SourceID, batch.LastSequence)
	return err
}

func (s *PublicMirrorStore) snapshotRecords(ctx context.Context, sourceID string) ([]models.PublicFeedRecord, error) {
	records, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT sequence, record_type, record_hash, record_bytes FROM snapshot_records r
		WHERE source_id = ? AND NOT EXISTS (SELECT 1 FROM withdrawn_datasets w WHERE w.source_id = r.source_id AND w.dataset_id = r.dataset_id)
		ORDER BY sequence`, []interface{}{sourceID}, scanPublicMirrorRecord)
	if err != nil {
		return nil, fmt.Errorf("mirror snapshot: read current records: %w", err)
	}
	if records == nil {
		records = []models.PublicFeedRecord{}
	}
	return records, nil
}

// initializePublicSnapshots validates retained history and snapshots fail-closed.
// Existing snapshots are verified, never silently rebuilt over corruption or data loss.
func (s *PublicMirrorStore) initializePublicSnapshots(ctx context.Context) error {
	s.logger.Info("Public mirror verification stage", "stage", "snapshots")
	state, err := s.loadState(ctx)
	if err != nil {
		return err
	}
	for sourceID, summary := range state.sources {
		var head int64
		err := s.db.QueryRowWithRetry(ctx, `SELECT high_water_sequence FROM snapshot_heads WHERE source_id = ?`, sourceID).Scan(&head)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return publicMirrorCorrupt("snapshot initialization marker missing for %s", sourceID)
			}
			return err
		}
		if head != summary.HighWaterSequence {
			return publicMirrorCorrupt("snapshot cursor mismatch for %s", sourceID)
		}
	}
	return s.verifyPublicSnapshots(ctx, state.keys)
}

// Snapshot verification retains identities and authenticated hashes, never the
// payload history. SQL pages are fully closed before the next query.
func (s *PublicMirrorStore) verifyPublicSnapshots(ctx context.Context, keys PublicMirrorKeys) error {
	sources, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT source_id FROM snapshot_batches UNION SELECT source_id FROM snapshot_records`, nil, func(rows *sql.Rows) (string, error) {
		var source string
		return source, rows.Scan(&source)
	})
	if err != nil {
		return fmt.Errorf("mirror snapshot: read sources: %w", err)
	}
	for _, source := range sources {
		if err := s.verifyPublicSnapshot(ctx, source, keys); err != nil {
			return err
		}
	}
	return nil
}

func (s *PublicMirrorStore) verifyPublicSnapshot(ctx context.Context, source string, keys PublicMirrorKeys) error {
	type projection struct {
		key, dataset, recordHash string
		recordType               models.PublicFeedRecordType
		event                    bool
		first, sequence          int64
	}
	expected := make(map[string]projection)
	events := make(map[string]projection)
	var after int64
	firstPage := true
	started := time.Now()
	lastProgress := started
	verified := 0
	for {
		query := `SELECT source_id, first_sequence, batch_json FROM snapshot_batches WHERE source_id = ?`
		args := []interface{}{source}
		if !firstPage {
			query += ` AND first_sequence > ?`
			args = append(args, after)
		}
		query += ` ORDER BY first_sequence LIMIT ?`
		args = append(args, publicMirrorVerifySnapshotPageBatches)
		batches, err := sqliteutil.MaterializeRows(ctx, s.db, query, args, func(rows *sql.Rows) (models.PublicFeedBatch, error) {
			var storedSource, raw string
			var first int64
			var batch models.PublicFeedBatch
			if err := rows.Scan(&storedSource, &first, &raw); err != nil {
				return batch, err
			}
			if err := decodePublicMirrorJSON(raw, &batch); err != nil {
				return batch, err
			}
			if batch.SourceID != storedSource || batch.FirstSequence != first {
				return batch, publicMirrorCorrupt("snapshot batch identity mismatch")
			}
			return batch, nil
		})
		if err != nil {
			return fmt.Errorf("%w: read signed snapshot: %w", constants.ErrPublicFeedMirrorStoreCorrupt, err)
		}
		if len(batches) == 0 {
			break
		}
		for _, batch := range batches {
			if err := ctx.Err(); err != nil {
				return err
			}
			key, found := keys.Registry[publicMirrorKeyRegistryID(batch.SourceID, batch.SigningKeyID)]
			if !found || verifyBatchSignature(batch, key) != nil || len(batch.Records) != len(batch.RecordHashes) || int64(len(batch.Records)) != batch.LastSequence-batch.FirstSequence+1 {
				return publicMirrorCorrupt("invalid signed snapshot batch %s@%d", batch.SourceID, batch.FirstSequence)
			}
			for i, record := range batch.Records {
				hash := sha256.Sum256([]byte(record.RecordBytes))
				if record.RecordHash != hex.EncodeToString(hash[:]) || record.RecordHash != batch.RecordHashes[i] || record.Sequence != batch.FirstSequence+int64(i) {
					return publicMirrorCorrupt("invalid snapshot record")
				}
				key, identity, event, err := parseSnapshotRecordIdentity(record)
				if err != nil {
					return publicMirrorCorrupt("decode signed snapshot identity: %v", err)
				}
				if key == "" {
					continue
				}
				if resultKey := snapshotResultCompanionKey(identity); resultKey != "" {
					if _, hasResult := expected[resultKey]; hasResult {
						continue
					}
				}
				if lifecycleKey := snapshotLifecycleCompanionKey(identity); lifecycleKey != "" {
					delete(expected, lifecycleKey)
				}
				current := expected
				if event {
					current = events
				}
				if prior, found := current[key]; !found || record.Sequence > prior.sequence {
					current[key] = projection{key, identity.DatasetID, record.RecordHash, record.RecordType, event, batch.FirstSequence, record.Sequence}
				}
			}
		}
		// A retained batch also carries superseded activity. Keep only the
		// same per-source event tail that ingest retains, across page edges.
		tail := make([]projection, 0, len(events))
		for _, p := range events {
			tail = append(tail, p)
		}
		sort.Slice(tail, func(i, j int) bool { return tail[i].sequence > tail[j].sequence })
		for _, old := range tail[min(len(tail), constants.PublicFeedSSEReplayMaxRecords):] {
			delete(events, old.key)
		}
		after = batches[len(batches)-1].FirstSequence
		firstPage = false
		verified += len(batches)
		if time.Since(lastProgress) >= time.Second {
			s.logger.Info("Public mirror verification progress", "stage", "snapshots", "batches", verified, "elapsed", time.Since(started))
			lastProgress = time.Now()
		}
	}
	for key, p := range events {
		expected[key] = p
	}
	var afterKey string
	firstPage = true
	for {
		query := `SELECT record_key, dataset_id, is_event, first_sequence, sequence, record_type, record_hash, record_bytes FROM snapshot_records WHERE source_id = ?`
		args := []interface{}{source}
		if !firstPage {
			query += ` AND record_key > ?`
			args = append(args, afterKey)
		}
		query += ` ORDER BY record_key LIMIT ?`
		args = append(args, publicMirrorReadPageRecords)
		records, err := sqliteutil.MaterializeRows(ctx, s.db, query, args, func(rows *sql.Rows) (projection, error) {
			var p projection
			var raw string
			if err := rows.Scan(&p.key, &p.dataset, &p.event, &p.first, &p.sequence, &p.recordType, &p.recordHash, &raw); err != nil {
				return p, err
			}
			// The expected digest came from verified signed bytes. Hash the
			// unsigned row too: trusting its record_hash column would miss
			// payload-only tampering.
			hash := sha256.Sum256([]byte(raw))
			if p.recordHash != hex.EncodeToString(hash[:]) {
				return p, publicMirrorCorrupt("snapshot row hash mismatch")
			}
			return p, nil
		})
		if err != nil {
			return err
		}
		if len(records) == 0 {
			break
		}
		for _, p := range records {
			want, found := expected[p.key]
			if !found || want != p {
				return publicMirrorCorrupt("snapshot row differs from signed current state")
			}
			delete(expected, p.key)
		}
		afterKey = records[len(records)-1].key
		firstPage = false
	}
	if len(expected) != 0 {
		return publicMirrorCorrupt("snapshot current records missing")
	}
	return nil
}
