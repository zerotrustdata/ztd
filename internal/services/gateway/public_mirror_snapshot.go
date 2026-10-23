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

func snapshotRecordIdentity(record models.PublicFeedRecord) (key, dataset string, event bool, err error) {
	if record.RecordType != models.PublicFeedRecordTypeProjection && record.RecordType != models.PublicFeedRecordTypeEvent {
		return "", "", false, nil
	}
	var identity publicSnapshotIdentity
	if err := json.Unmarshal([]byte(record.RecordBytes), &identity); err != nil {
		return "", "", false, fmt.Errorf("mirror snapshot: decode identity: %w", err)
	}
	if identity.MessageType != "" {
		var assignment publicSnapshotIdentity
		if err := json.Unmarshal(identity.Record, &assignment); err != nil {
			return "", "", false, fmt.Errorf("mirror snapshot: decode assignment identity: %w", err)
		}
		assignment.Kind = identity.MessageType
		assignment.DatasetID = campaignMirrorDatasetID(assignment.RunID)
		identity = assignment
	}
	event = identity.EventID != ""
	parts := []string{identity.Kind, identity.DatasetID, identity.RunID, identity.AssignmentID, identity.VariantID, identity.Role, identity.SuiteID}
	if event {
		parts = []string{"event", identity.EventID}
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return "", "", false, fmt.Errorf("mirror snapshot: encode identity: %w", err)
	}
	return string(encoded), identity.DatasetID, event, nil
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
	for _, record := range batch.Records {
		key, dataset, event, err := snapshotRecordIdentity(record)
		if err != nil {
			return err
		}
		if key == "" {
			continue
		}
		var previous int64
		err = tx.QueryRowContext(ctx, `SELECT first_sequence FROM snapshot_records WHERE source_id = ? AND record_key = ?`, batch.SourceID, key).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			obsolete[previous] = true
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot_records
			(source_id, record_key, dataset_id, sequence, first_sequence, is_event, record_type, record_hash, record_bytes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_id, record_key) DO UPDATE SET
			sequence=excluded.sequence, first_sequence=excluded.first_sequence,
			record_type=excluded.record_type, record_hash=excluded.record_hash, record_bytes=excluded.record_bytes`,
			batch.SourceID, key, dataset, record.Sequence, batch.FirstSequence, event, record.RecordType, record.RecordHash, record.RecordBytes); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT first_sequence, record_key FROM snapshot_records WHERE source_id = ? AND is_event = 1 ORDER BY sequence DESC LIMIT -1 OFFSET ?`, batch.SourceID, constants.PublicFeedSSEReplayMaxRecords)
	if err != nil {
		return err
	}
	var expired []string
	for rows.Next() {
		var first int64
		var key string
		if err := rows.Scan(&first, &key); err != nil {
			_ = rows.Close()
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
	for _, key := range expired {
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshot_records WHERE source_id = ? AND record_key = ?`, batch.SourceID, key); err != nil {
			return err
		}
	}
	for first := range obsolete {
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshot_batches WHERE source_id = ? AND first_sequence = ? AND NOT EXISTS
			(SELECT 1 FROM snapshot_records r WHERE r.source_id = snapshot_batches.source_id AND r.first_sequence = snapshot_batches.first_sequence)`, batch.SourceID, first); err != nil {
			return err
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

// initializePublicSnapshots upgrades verified retained history once. Existing
// snapshots are verified, never silently rebuilt over corruption or data loss.
func (s *PublicMirrorStore) initializePublicSnapshots(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowWithRetry(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	state, err := s.loadState(ctx)
	if err != nil {
		return err
	}
	for sourceID, summary := range state.sources {
		var head int64
		err := s.db.QueryRowWithRetry(ctx, `SELECT high_water_sequence FROM snapshot_heads WHERE source_id = ?`, sourceID).Scan(&head)
		if err == nil {
			if head != summary.HighWaterSequence {
				return publicMirrorCorrupt("snapshot cursor mismatch for %s", sourceID)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if version >= 1 {
			return publicMirrorCorrupt("snapshot initialization marker missing for %s", sourceID)
		}
		batches, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT first_sequence, last_sequence, protocol_version, schema_version, previous_batch_hash, generated_at, content_hash, signing_key_id, signature FROM batches WHERE source_id = ? ORDER BY first_sequence`, []interface{}{sourceID}, func(rows *sql.Rows) (models.PublicFeedBatch, error) {
			batch := models.PublicFeedBatch{SourceID: sourceID}
			var generated string
			if err := rows.Scan(&batch.FirstSequence, &batch.LastSequence, &batch.ProtocolVersion, &batch.SchemaVersion, &batch.PreviousBatchHash, &generated, &batch.ContentHash, &batch.SigningKeyID, &batch.Signature); err != nil {
				return batch, err
			}
			parsed, err := time.Parse(time.RFC3339Nano, generated)
			batch.GeneratedAt = parsed
			return batch, err
		})
		if err != nil {
			return err
		}
		// One transaction includes the initialization marker, so interrupted
		// migration retries cannot mistake a partial snapshot for a complete one.
		for i := range batches {
			batches[i].Records, err = s.recordsInRange(ctx, sourceID, batches[i].FirstSequence, batches[i].LastSequence)
			if err != nil {
				return err
			}
			for _, record := range batches[i].Records {
				batches[i].RecordHashes = append(batches[i].RecordHashes, record.RecordHash)
			}
		}
		if err := s.write(ctx, "initialize snapshot", func(tx *sql.Tx) error {
			for _, batch := range batches {
				if err := updatePublicSnapshot(ctx, tx, batch); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if err := s.verifyPublicSnapshots(ctx, state.keys); err != nil {
		return err
	}
	if _, err := s.db.ExecWithRetry(ctx, `PRAGMA user_version = 1`); err != nil {
		return fmt.Errorf("mirror snapshot: record migration: %w", err)
	}
	return nil
}

func (s *PublicMirrorStore) verifyPublicSnapshots(ctx context.Context, keys PublicMirrorKeys) error {
	type identity struct{ source, key string }
	type projection struct {
		identity
		dataset string
		event   bool
		first   int64
		record  models.PublicFeedRecord
	}
	expected := make(map[identity]projection)
	batches, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT source_id, first_sequence, batch_json FROM snapshot_batches`, nil, func(rows *sql.Rows) (models.PublicFeedBatch, error) {
		var source, raw string
		var first int64
		var batch models.PublicFeedBatch
		if err := rows.Scan(&source, &first, &raw); err != nil {
			return batch, err
		}
		if err := decodePublicMirrorJSON(raw, &batch); err != nil {
			return batch, err
		}
		if batch.SourceID != source || batch.FirstSequence != first {
			return batch, publicMirrorCorrupt("snapshot batch identity mismatch")
		}
		return batch, nil
	})
	if err != nil {
		return publicMirrorCorrupt("read signed snapshot: %v", err)
	}
	for _, batch := range batches {
		key, found := keys.Registry[publicMirrorKeyRegistryID(batch.SourceID, batch.SigningKeyID)]
		if !found || verifyBatchSignature(batch, key) != nil || len(batch.Records) != len(batch.RecordHashes) || int64(len(batch.Records)) != batch.LastSequence-batch.FirstSequence+1 {
			return publicMirrorCorrupt("invalid signed snapshot batch %s@%d", batch.SourceID, batch.FirstSequence)
		}
		for i, record := range batch.Records {
			hash := sha256.Sum256([]byte(record.RecordBytes))
			if record.RecordHash != hex.EncodeToString(hash[:]) || record.RecordHash != batch.RecordHashes[i] || record.Sequence != batch.FirstSequence+int64(i) {
				return publicMirrorCorrupt("invalid snapshot record")
			}
			key, dataset, event, err := snapshotRecordIdentity(record)
			if err != nil {
				return publicMirrorCorrupt("decode signed snapshot identity: %v", err)
			}
			if key == "" {
				continue
			}
			id := identity{batch.SourceID, key}
			if prior, found := expected[id]; !found || record.Sequence > prior.record.Sequence {
				expected[id] = projection{id, dataset, event, batch.FirstSequence, record}
			}
		}
	}
	// A retained signed batch may also contain older events. Derive the same
	// bounded per-source tail as ingest before checking every materialized row.
	events := make(map[string][]projection)
	for _, p := range expected {
		if p.event {
			events[p.source] = append(events[p.source], p)
		}
	}
	for _, tail := range events {
		sort.Slice(tail, func(i, j int) bool { return tail[i].record.Sequence > tail[j].record.Sequence })
		for _, old := range tail[min(len(tail), constants.PublicFeedSSEReplayMaxRecords):] {
			delete(expected, old.identity)
		}
	}
	records, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT source_id, record_key, dataset_id, is_event, first_sequence, sequence, record_type, record_hash, record_bytes FROM snapshot_records`, nil, func(rows *sql.Rows) (projection, error) {
		var p projection
		err := rows.Scan(&p.source, &p.key, &p.dataset, &p.event, &p.first, &p.record.Sequence, &p.record.RecordType, &p.record.RecordHash, &p.record.RecordBytes)
		return p, err
	})
	if err != nil {
		return err
	}
	for _, p := range records {
		want, found := expected[p.identity]
		if !found || want != p {
			return publicMirrorCorrupt("snapshot row differs from signed current state")
		}
		delete(expected, p.identity)
	}
	if len(expected) != 0 {
		return publicMirrorCorrupt("snapshot current records missing")
	}
	return nil
}
