// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// publicMirrorSchema is the canonical public mirror SQLite schema, embedded at
// compile time from `public_mirror_schema.sql`. That file is the single source
// of truth - do not inline CREATE TABLE statements in Go code.
//
//go:embed db/public_mirror_schema.sql
var publicMirrorSchema string

const (
	// publicMirrorVerifyPageBatches bounds how many batch headers startup
	// verification holds at once.
	publicMirrorVerifyPageBatches = 1000
	// publicMirrorQueryChunk bounds the parameters of one IN (...) lookup.
	publicMirrorQueryChunk = 500
)

// PublicMirrorSourceSummary is the bounded per-source state of the accepted
// signed batch chain. The mirror holds one per source in memory and treats it
// as immutable: every mutation replaces the value, so a reader may keep the
// pointer it copied under the lock.
type PublicMirrorSourceSummary struct {
	SourceID                  string
	HighWaterSequence         int64
	FeedChainHash             string
	BatchCount                int
	RetainedFromSequence      int64
	RetainedPreviousBatchHash string
	RetainedBatchCount        int
	RetainedRecordCount       int
	RetainedRecordBytes       int64
	Freshness                 models.CampaignFreshness
	LastAcceptedAt            time.Time
	WithdrawnDatasetIDs       map[string]bool
}

func newPublicMirrorSourceSummary(sourceID string) PublicMirrorSourceSummary {
	return PublicMirrorSourceSummary{
		SourceID:                  sourceID,
		FeedChainHash:             constants.PublicFeedZeroHashHex,
		RetainedFromSequence:      1,
		RetainedPreviousBatchHash: constants.PublicFeedZeroHashHex,
		Freshness:                 models.CampaignFreshnessActive,
		WithdrawnDatasetIDs:       map[string]bool{},
	}
}

// PublicMirrorKeys is the mirror's source signing-key registry and revocation
// set, keyed "<source_id>:<key_id>". Values are replaced, never mutated, once
// published to the mirror.
type PublicMirrorKeys struct {
	Registry map[string]ed25519.PublicKey
	Revoked  map[string]time.Time
}

func newPublicMirrorKeys() PublicMirrorKeys {
	return PublicMirrorKeys{Registry: map[string]ed25519.PublicKey{}, Revoked: map[string]time.Time{}}
}

func (keys PublicMirrorKeys) clone() PublicMirrorKeys {
	cloned := PublicMirrorKeys{
		Registry: make(map[string]ed25519.PublicKey, len(keys.Registry)+1),
		Revoked:  make(map[string]time.Time, len(keys.Revoked)+1),
	}
	for id, key := range keys.Registry {
		cloned.Registry[id] = key
	}
	for id, revokedAt := range keys.Revoked {
		cloned.Revoked[id] = revokedAt
	}
	return cloned
}

func publicMirrorKeyRegistryID(sourceID, keyID string) string {
	return sourceID + ":" + keyID
}

// publicMirrorState is the bounded mirror state loaded from the store.
type publicMirrorState struct {
	activeSourceID string
	sources        map[string]*PublicMirrorSourceSummary
	keys           PublicMirrorKeys
}

type publicMirrorBatchHeader struct {
	FirstSequence int64
	LastSequence  int64
	ContentHash   string
}

type publicMirrorProofArtifact struct {
	ByteSize int64
	Entry    *models.PublicProofCatalogEntry
}

// PublicMirrorProofSummary is the stored summary of one source's proof
// manifest.
type PublicMirrorProofSummary struct {
	SegmentIndex    int64
	ProofRootSHA256 string
	ArtifactCount   int
	TotalByteSize   int64
	GeneratedAt     time.Time
}

// PublicMirrorStore is the durable public mirror store: a SQLite database at
// public-mirror/mirror.db holding the retained signed batch chain, key
// registry, withdrawals, and proof manifests, plus content-addressed proof
// artifact files under public-mirror/proofs/<sha256>. Every write is one
// transaction proportional to the change; nothing rewrites the whole store.
type PublicMirrorStore struct {
	db      *sqliteutil.DB
	fileSvc fs.RuntimeFileService
	logger  *slog.Logger
}

// OpenPublicMirrorStore opens the mirror store at constants.PublicMirrorDBPath
// and verifies every retained signature, hash chain, record, revocation, proof
// manifest, and proof artifact before returning. Any inconsistency fails closed
// with ErrPublicFeedMirrorStoreCorrupt.
func OpenPublicMirrorStore(ctx context.Context, fileSvc fs.RuntimeFileService, logger *slog.Logger) (*PublicMirrorStore, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("public mirror store: %w: runtime file service", constants.ErrMissingRequiredField)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := fileSvc.MkdirAll(ctx, constants.PublicMirrorProofsDir, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("public mirror store: create proofs dir: %w", err)
	}
	store, err := openPublicMirrorStoreAt(ctx, fileSvc, logger)
	if err != nil {
		return nil, err
	}
	if err := store.verify(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func openPublicMirrorStoreAt(ctx context.Context, fileSvc fs.RuntimeFileService, logger *slog.Logger) (*PublicMirrorStore, error) {
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.PublicMirrorDBPath)), logger)
	if err != nil {
		return nil, fmt.Errorf("public mirror store: open %s: %w", constants.PublicMirrorDBPath, err)
	}
	if _, err := db.ExecWithRetry(ctx, publicMirrorSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: apply schema: %w", constants.ErrPublicFeedMirrorStoreWrite, err)
	}
	if _, err := db.ExecWithRetry(ctx, `INSERT OR IGNORE INTO mirror_state (id, active_source_id) VALUES (1, '')`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: init mirror state: %w", constants.ErrPublicFeedMirrorStoreWrite, err)
	}
	return &PublicMirrorStore{db: db, fileSvc: fileSvc, logger: logger}, nil
}

// Close closes the store database.
func (s *PublicMirrorStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func publicMirrorCorrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", constants.ErrPublicFeedMirrorStoreCorrupt, fmt.Sprintf(format, args...))
}

func formatPublicMirrorTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parsePublicMirrorTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, publicMirrorCorrupt("parse time %q: %v", value, err)
	}
	return parsed.UTC(), nil
}

func publicMirrorProofArtifactPath(artifactID string) string {
	return filepath.Join(constants.PublicMirrorProofsDir, artifactID)
}

// validPublicProofArtifactID reports whether id is a lowercase hex SHA-256,
// the only form a content-addressed artifact file name may take.
func validPublicProofArtifactID(id string) bool {
	if len(id) != sha256.Size*2 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// loadState reads the bounded mirror state: the active source, per-source
// summaries with retained counts and withdrawals, and the key registry.
func (s *PublicMirrorStore) loadState(ctx context.Context) (publicMirrorState, error) {
	state := publicMirrorState{sources: map[string]*PublicMirrorSourceSummary{}, keys: newPublicMirrorKeys()}
	if err := s.db.QueryRowWithRetry(ctx, `SELECT active_source_id FROM mirror_state WHERE id = 1`).Scan(&state.activeSourceID); err != nil {
		return publicMirrorState{}, fmt.Errorf("public mirror store: read mirror state: %w", err)
	}
	summaries, err := sqliteutil.MaterializeRows(ctx, s.db, `
		SELECT source_id, high_water_sequence, feed_chain_hash, batch_count,
		       retained_from_sequence, retained_previous_batch_hash, freshness, last_accepted_at,
		       retained_batch_count, retained_record_count, retained_record_bytes
		FROM sources`, nil, func(rows *sql.Rows) (PublicMirrorSourceSummary, error) {
		var summary PublicMirrorSourceSummary
		var freshness, lastAcceptedAt string
		if err := rows.Scan(&summary.SourceID, &summary.HighWaterSequence, &summary.FeedChainHash, &summary.BatchCount,
			&summary.RetainedFromSequence, &summary.RetainedPreviousBatchHash, &freshness, &lastAcceptedAt,
			&summary.RetainedBatchCount, &summary.RetainedRecordCount, &summary.RetainedRecordBytes); err != nil {
			return PublicMirrorSourceSummary{}, err
		}
		summary.Freshness = models.CampaignFreshness(freshness)
		acceptedAt, err := parsePublicMirrorTime(lastAcceptedAt)
		if err != nil {
			return PublicMirrorSourceSummary{}, err
		}
		summary.LastAcceptedAt = acceptedAt
		summary.WithdrawnDatasetIDs = map[string]bool{}
		return summary, nil
	})
	if err != nil {
		return publicMirrorState{}, fmt.Errorf("public mirror store: read sources: %w", err)
	}
	for index := range summaries {
		state.sources[summaries[index].SourceID] = &summaries[index]
	}
	type withdrawal struct{ sourceID, datasetID string }
	withdrawals, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT source_id, dataset_id FROM withdrawn_datasets`, nil, func(rows *sql.Rows) (withdrawal, error) {
		var row withdrawal
		return row, rows.Scan(&row.sourceID, &row.datasetID)
	})
	if err != nil {
		return publicMirrorState{}, fmt.Errorf("public mirror store: read withdrawals: %w", err)
	}
	for _, row := range withdrawals {
		summary, ok := state.sources[row.sourceID]
		if !ok {
			return publicMirrorState{}, publicMirrorCorrupt("withdrawal for unknown source %s", row.sourceID)
		}
		summary.WithdrawnDatasetIDs[row.datasetID] = true
	}
	type keyRow struct {
		id  string
		key []byte
	}
	keyRows, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT registry_id, public_key FROM key_registry`, nil, func(rows *sql.Rows) (keyRow, error) {
		var row keyRow
		return row, rows.Scan(&row.id, &row.key)
	})
	if err != nil {
		return publicMirrorState{}, fmt.Errorf("public mirror store: read key registry: %w", err)
	}
	for _, row := range keyRows {
		state.keys.Registry[row.id] = ed25519.PublicKey(row.key)
	}
	type revokedRow struct{ id, revokedAt string }
	revokedRows, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT registry_id, revoked_at FROM revoked_keys`, nil, func(rows *sql.Rows) (revokedRow, error) {
		var row revokedRow
		return row, rows.Scan(&row.id, &row.revokedAt)
	})
	if err != nil {
		return publicMirrorState{}, fmt.Errorf("public mirror store: read revoked keys: %w", err)
	}
	for _, row := range revokedRows {
		revokedAt, err := parsePublicMirrorTime(row.revokedAt)
		if err != nil {
			return publicMirrorState{}, err
		}
		state.keys.Revoked[row.id] = revokedAt
	}
	return state, nil
}

// SourceSummary returns the durable summary of one source.
func (s *PublicMirrorStore) SourceSummary(ctx context.Context, sourceID string) (PublicMirrorSourceSummary, bool, error) {
	state, err := s.loadState(ctx)
	if err != nil {
		return PublicMirrorSourceSummary{}, false, err
	}
	summary, ok := state.sources[sourceID]
	if !ok {
		return PublicMirrorSourceSummary{}, false, nil
	}
	return *summary, true, nil
}

// Keys returns the durable key registry and revocation set.
func (s *PublicMirrorStore) Keys(ctx context.Context) (PublicMirrorKeys, error) {
	state, err := s.loadState(ctx)
	if err != nil {
		return PublicMirrorKeys{}, err
	}
	return state.keys, nil
}

// ProofArtifactCount returns how many content-addressed proof artifacts the
// store holds.
func (s *PublicMirrorStore) ProofArtifactCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowWithRetry(ctx, `SELECT COUNT(*) FROM proof_artifacts`).Scan(&count); err != nil {
		return 0, fmt.Errorf("public mirror store: count proof artifacts: %w", err)
	}
	return count, nil
}

func upsertPublicMirrorSource(ctx context.Context, tx *sql.Tx, summary PublicMirrorSourceSummary) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sources (source_id, high_water_sequence, feed_chain_hash, batch_count,
		                     retained_from_sequence, retained_previous_batch_hash, freshness, last_accepted_at,
		                     retained_batch_count, retained_record_count, retained_record_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET
			high_water_sequence = excluded.high_water_sequence,
			feed_chain_hash = excluded.feed_chain_hash,
			batch_count = excluded.batch_count,
			retained_from_sequence = excluded.retained_from_sequence,
			retained_previous_batch_hash = excluded.retained_previous_batch_hash,
			freshness = excluded.freshness,
			last_accepted_at = excluded.last_accepted_at,
			retained_batch_count = excluded.retained_batch_count,
			retained_record_count = excluded.retained_record_count,
			retained_record_bytes = excluded.retained_record_bytes`,
		summary.SourceID, summary.HighWaterSequence, summary.FeedChainHash, summary.BatchCount,
		summary.RetainedFromSequence, summary.RetainedPreviousBatchHash, string(summary.Freshness), formatPublicMirrorTime(summary.LastAcceptedAt),
		summary.RetainedBatchCount, summary.RetainedRecordCount, summary.RetainedRecordBytes)
	if err != nil {
		return fmt.Errorf("upsert source %s: %w", summary.SourceID, err)
	}
	return nil
}

func (s *PublicMirrorStore) write(ctx context.Context, operation string, fn func(*sql.Tx) error) error {
	if err := s.db.ExecInTxWithRetry(ctx, fn); err != nil {
		return fmt.Errorf("%w: %s: %w", constants.ErrPublicFeedMirrorStoreWrite, operation, err)
	}
	return nil
}

// putSourceKey stores one source signing key. A non-empty activeSourceID also
// marks that source active.
func (s *PublicMirrorStore) putSourceKey(ctx context.Context, registryID string, key ed25519.PublicKey, activeSourceID string) error {
	return s.write(ctx, "put source key", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO key_registry (registry_id, public_key) VALUES (?, ?)
			ON CONFLICT(registry_id) DO UPDATE SET public_key = excluded.public_key`, registryID, []byte(key)); err != nil {
			return err
		}
		if activeSourceID == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE mirror_state SET active_source_id = ? WHERE id = 1`, activeSourceID)
		return err
	})
}

func (s *PublicMirrorStore) revokeKey(ctx context.Context, registryID string, revokedAt time.Time) error {
	return s.write(ctx, "revoke key", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO revoked_keys (registry_id, revoked_at) VALUES (?, ?)
			ON CONFLICT(registry_id) DO UPDATE SET revoked_at = excluded.revoked_at`, registryID, formatPublicMirrorTime(revokedAt))
		return err
	})
}

func (s *PublicMirrorStore) putSourceSummary(ctx context.Context, summary PublicMirrorSourceSummary) error {
	return s.write(ctx, "put source", func(tx *sql.Tx) error {
		return upsertPublicMirrorSource(ctx, tx, summary)
	})
}

func (s *PublicMirrorStore) withdrawDataset(ctx context.Context, summary PublicMirrorSourceSummary, datasetID string) error {
	return s.write(ctx, "withdraw dataset", func(tx *sql.Tx) error {
		if err := upsertPublicMirrorSource(ctx, tx, summary); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO withdrawn_datasets (source_id, dataset_id) VALUES (?, ?)`, summary.SourceID, datasetID)
		return err
	})
}

func insertPublicMirrorBatch(ctx context.Context, tx *sql.Tx, batch models.PublicFeedBatch) error {
	if err := insertPublicMirrorBatchHeader(ctx, tx, batch); err != nil {
		return err
	}
	return insertPublicMirrorRecords(ctx, tx, batch.SourceID, batch.Records)
}

// insertPublicMirrorBatchHeader stores the signed batch header without its
// records.
func insertPublicMirrorBatchHeader(ctx context.Context, tx *sql.Tx, batch models.PublicFeedBatch) error {
	recordCount := len(batch.Records)
	var byteSize int64
	for _, r := range batch.Records {
		byteSize += int64(len(r.RecordBytes))
	}
	if recordCount == 0 && batch.LastSequence >= batch.FirstSequence {
		recordCount = int(batch.LastSequence - batch.FirstSequence + 1)
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(LENGTH(record_bytes)), 0) FROM records
			WHERE source_id = ? AND sequence BETWEEN ? AND ?`,
			batch.SourceID, batch.FirstSequence, batch.LastSequence).Scan(&byteSize)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO batches (source_id, first_sequence, last_sequence, record_count, byte_size,
		                     protocol_version, schema_version, previous_batch_hash,
		                     generated_at, content_hash, signing_key_id, signature)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		batch.SourceID, batch.FirstSequence, batch.LastSequence, recordCount, byteSize,
		batch.ProtocolVersion, batch.SchemaVersion, batch.PreviousBatchHash,
		batch.GeneratedAt.UTC().Format(time.RFC3339Nano), batch.ContentHash, batch.SigningKeyID, batch.Signature); err != nil {
		return fmt.Errorf("insert batch %s@%d: %w", batch.SourceID, batch.FirstSequence, err)
	}
	return nil
}

// insertPublicMirrorRecords stores records and indexes their event records in
// the caller's transaction, so a record and its index row commit or roll back
// together.
func insertPublicMirrorRecords(ctx context.Context, tx *sql.Tx, sourceID string, records []models.PublicFeedRecord) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO records (source_id, sequence, record_type, record_hash, record_bytes) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare record insert: %w", err)
	}
	defer stmt.Close()
	for _, record := range records {
		if _, err := stmt.ExecContext(ctx, sourceID, record.Sequence, string(record.RecordType), record.RecordHash, record.RecordBytes); err != nil {
			return fmt.Errorf("insert record %s@%d: %w", sourceID, record.Sequence, err)
		}
	}
	return insertAssignmentEventRecords(ctx, tx, sourceID, records)
}

// insertAssignmentEventRecords indexes every event record by the run and
// assignment its body names. The body is decoded with the same canonical
// decoder the assignment audit database builder uses, so a record that cannot
// be built into a database fails the insert instead of being stored unindexed.
func insertAssignmentEventRecords(ctx context.Context, tx *sql.Tx, sourceID string, records []models.PublicFeedRecord) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO assignment_event_records (source_id, sequence, run_id, assignment_id) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare assignment event insert: %w", err)
	}
	defer stmt.Close()
	for _, record := range records {
		if record.RecordType != models.PublicFeedRecordTypeEvent {
			continue
		}
		event := &evalv1.PublicLiveEvent{}
		if err := evalv1.UnmarshalCanonical([]byte(record.RecordBytes), event); err != nil {
			return fmt.Errorf("index event record %s@%d: %w: %w", sourceID, record.Sequence, constants.ErrPublicFeedRecordSchemaInvalid, err)
		}
		if _, err := stmt.ExecContext(ctx, sourceID, record.Sequence, event.GetRunId(), event.GetAssignmentId()); err != nil {
			return fmt.Errorf("insert assignment event record %s@%d: %w", sourceID, record.Sequence, err)
		}
	}
	return nil
}

// AssignmentEventRecords returns the retained event records of one run's
// assignment, oldest first. It returns no records when none are retained.
func (s *PublicMirrorStore) AssignmentEventRecords(ctx context.Context, sourceID, runID, assignmentID string) ([]models.PublicFeedRecord, error) {
	records, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT r.sequence, r.record_type, r.record_hash, r.record_bytes
		FROM assignment_event_records a
		JOIN records r ON r.source_id = a.source_id AND r.sequence = a.sequence
		WHERE a.source_id = ? AND a.run_id = ? AND a.assignment_id = ?
		ORDER BY a.sequence`,
		[]interface{}{sourceID, runID, assignmentID}, scanPublicMirrorRecord)
	if err != nil {
		return nil, fmt.Errorf("public mirror store: read assignment event records: %w", err)
	}
	return records, nil
}

func insertPublicMirrorRevocations(ctx context.Context, tx *sql.Tx, revocations []models.PublicKeyRevocationRecord) error {
	for _, revocation := range revocations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO revoked_keys (registry_id, revoked_at) VALUES (?, ?)
			ON CONFLICT(registry_id) DO UPDATE SET revoked_at = excluded.revoked_at`,
			publicMirrorKeyRegistryID(revocation.SourceID, revocation.RevokedKeyID), formatPublicMirrorTime(revocation.RevokedAt)); err != nil {
			return fmt.Errorf("insert revocation: %w", err)
		}
	}
	return nil
}

// appendBatch durably appends one accepted batch in a single transaction:
// batch and records, revocations, the source summary, and retention pruning
// of the oldest batches once more than maxRetainedBatches or maxRetainedBytes
// are retained. next is the summary after the append; the returned summary
// reflects pruning.
func (s *PublicMirrorStore) appendBatch(ctx context.Context, batch models.PublicFeedBatch, next PublicMirrorSourceSummary, revocations []models.PublicKeyRevocationRecord, maxRetainedBatches int, maxRetainedBytes int64) (PublicMirrorSourceSummary, error) {
	var stored PublicMirrorSourceSummary
	err := s.write(ctx, "append batch", func(tx *sql.Tx) error {
		summary := next
		if err := upsertPublicMirrorSource(ctx, tx, summary); err != nil {
			return err
		}
		if err := insertPublicMirrorBatch(ctx, tx, batch); err != nil {
			return err
		}
		if (maxRetainedBatches > 0 && summary.RetainedBatchCount > maxRetainedBatches) ||
			(maxRetainedBytes > 0 && summary.RetainedRecordBytes > maxRetainedBytes) {
			rows, err := tx.QueryContext(ctx, `SELECT first_sequence, last_sequence, content_hash, record_count, byte_size
				FROM batches
				WHERE source_id = ?
				ORDER BY first_sequence ASC`, summary.SourceID)
			if err != nil {
				return fmt.Errorf("select retention boundary: %w", err)
			}
			defer rows.Close()
			prunedBatches := 0
			prunedRecords := 0
			var prunedBytes int64
			var lastRemoved int64
			var lastRemovedHash string

			for rows.Next() {
				var firstSeq, lastSeq int64
				var contentHash string
				var batchRecordCount int
				var batchByteCount int64
				if err := rows.Scan(&firstSeq, &lastSeq, &contentHash, &batchRecordCount, &batchByteCount); err != nil {
					return fmt.Errorf("scan retention candidate: %w", err)
				}
				prunedBatches++
				prunedRecords += batchRecordCount
				prunedBytes += batchByteCount
				lastRemoved = lastSeq
				lastRemovedHash = contentHash

				batchesRemaining := summary.RetainedBatchCount - prunedBatches
				bytesRemaining := summary.RetainedRecordBytes - prunedBytes

				batchLimitMet := maxRetainedBatches <= 0 || batchesRemaining <= maxRetainedBatches
				byteLimitMet := maxRetainedBytes <= 0 || bytesRemaining <= maxRetainedBytes

				if (batchLimitMet && byteLimitMet) || batchesRemaining <= 1 {
					break
				}
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("read retention candidates: %w", err)
			}

			if prunedBatches > 0 {
				retainedFrom := lastRemoved + 1
				if _, err := tx.ExecContext(ctx, `DELETE FROM batches WHERE source_id = ? AND first_sequence < ?`, summary.SourceID, retainedFrom); err != nil {
					return fmt.Errorf("prune batches: %w", err)
				}
				result, err := tx.ExecContext(ctx, `DELETE FROM records WHERE source_id = ? AND sequence < ?`, summary.SourceID, retainedFrom)
				if err != nil {
					return fmt.Errorf("prune records: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM assignment_event_records WHERE source_id = ? AND sequence < ?`, summary.SourceID, retainedFrom); err != nil {
					return fmt.Errorf("prune assignment event records: %w", err)
				}
				actualPrunedRecords, err := result.RowsAffected()
				if err != nil {
					return fmt.Errorf("count pruned records: %w", err)
				}
				summary.RetainedFromSequence = retainedFrom
				summary.RetainedPreviousBatchHash = lastRemovedHash
				summary.RetainedBatchCount -= prunedBatches
				summary.RetainedRecordCount -= int(actualPrunedRecords)
				summary.RetainedRecordBytes -= prunedBytes
				if summary.RetainedRecordBytes < 0 {
					summary.RetainedRecordBytes = 0
				}
				if err := upsertPublicMirrorSource(ctx, tx, summary); err != nil {
					return err
				}
			}
		}
		if err := insertPublicMirrorRevocations(ctx, tx, revocations); err != nil {
			return err
		}
		stored = summary
		return nil
	})
	if err != nil {
		return PublicMirrorSourceSummary{}, err
	}
	return stored, nil
}

// batchHeader returns the retained batch starting at firstSequence.
func (s *PublicMirrorStore) batchHeader(ctx context.Context, sourceID string, firstSequence int64) (publicMirrorBatchHeader, bool, error) {
	var header publicMirrorBatchHeader
	err := s.db.QueryRowWithRetry(ctx, `SELECT first_sequence, last_sequence, content_hash FROM batches
		WHERE source_id = ? AND first_sequence = ?`, sourceID, firstSequence).Scan(&header.FirstSequence, &header.LastSequence, &header.ContentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return publicMirrorBatchHeader{}, false, nil
	}
	if err != nil {
		return publicMirrorBatchHeader{}, false, fmt.Errorf("public mirror store: read batch: %w", err)
	}
	return header, true, nil
}

// batchAnchor returns the content hash of the retained batch ending at
// lastSequence and how many retained batches through highWater follow it.
func (s *PublicMirrorStore) batchAnchor(ctx context.Context, sourceID string, lastSequence, highWater int64) (string, int, bool, error) {
	var firstSequence int64
	var contentHash string
	err := s.db.QueryRowWithRetry(ctx, `SELECT first_sequence, content_hash FROM batches
		WHERE source_id = ? AND last_sequence = ? AND last_sequence <= ?`, sourceID, lastSequence, highWater).Scan(&firstSequence, &contentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("public mirror store: read batch anchor: %w", err)
	}
	var later int
	if err := s.db.QueryRowWithRetry(ctx, `SELECT COUNT(*) FROM batches WHERE source_id = ? AND first_sequence > ? AND last_sequence <= ?`,
		sourceID, firstSequence, highWater).Scan(&later); err != nil {
		return "", 0, false, fmt.Errorf("public mirror store: count later batches: %w", err)
	}
	return contentHash, later, true, nil
}

func scanPublicMirrorRecord(rows *sql.Rows) (models.PublicFeedRecord, error) {
	var record models.PublicFeedRecord
	var recordType string
	if err := rows.Scan(&record.Sequence, &recordType, &record.RecordHash, &record.RecordBytes); err != nil {
		return models.PublicFeedRecord{}, err
	}
	record.RecordType = models.PublicFeedRecordType(recordType)
	return record, nil
}

// recordsAfter returns up to limit retained records with after < sequence <=
// through, oldest first.
func (s *PublicMirrorStore) recordsAfter(ctx context.Context, sourceID string, after, through int64, limit int) ([]models.PublicFeedRecord, error) {
	records, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT sequence, record_type, record_hash, record_bytes FROM records
		WHERE source_id = ? AND sequence > ? AND sequence <= ? ORDER BY sequence LIMIT ?`,
		[]interface{}{sourceID, after, through, limit}, scanPublicMirrorRecord)
	if err != nil {
		return nil, fmt.Errorf("public mirror store: read records: %w", err)
	}
	return records, nil
}

// recordsBefore returns up to limit retained records of one type with
// sequence < before, newest first.
func (s *PublicMirrorStore) recordsBefore(ctx context.Context, sourceID string, recordType models.PublicFeedRecordType, before int64, limit int) ([]models.PublicFeedRecord, error) {
	records, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT sequence, record_type, record_hash, record_bytes FROM records
		WHERE source_id = ? AND record_type = ? AND sequence < ? ORDER BY sequence DESC LIMIT ?`,
		[]interface{}{sourceID, string(recordType), before, limit}, scanPublicMirrorRecord)
	if err != nil {
		return nil, fmt.Errorf("public mirror store: read recent records: %w", err)
	}
	return records, nil
}

func (s *PublicMirrorStore) recordsInRange(ctx context.Context, sourceID string, first, last int64) ([]models.PublicFeedRecord, error) {
	records, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT sequence, record_type, record_hash, record_bytes FROM records
		WHERE source_id = ? AND sequence BETWEEN ? AND ? ORDER BY sequence`,
		[]interface{}{sourceID, first, last}, scanPublicMirrorRecord)
	if err != nil {
		return nil, fmt.Errorf("public mirror store: read record range: %w", err)
	}
	return records, nil
}

// proofManifestSourceID resolves which source's proof manifest an unscoped
// read serves: the preferred source when it has one, else the lowest source.
func (s *PublicMirrorStore) proofManifestSourceID(ctx context.Context, preferred string) (string, bool, error) {
	var sourceID string
	err := s.db.QueryRowWithRetry(ctx, `SELECT source_id FROM proof_manifests ORDER BY (source_id = ?) DESC, source_id LIMIT 1`, preferred).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("public mirror store: resolve proof manifest source: %w", err)
	}
	return sourceID, true, nil
}

// ProofManifest returns a legacy signed manifest or a segment-backed active
// projection. For a projection, the root and signature belong to SegmentIndex.
func (s *PublicMirrorStore) ProofManifest(ctx context.Context, sourceID string) (models.PublicProofManifest, bool, error) {
	var headerJSON, root string
	var count int
	err := s.db.QueryRowWithRetry(ctx, `SELECT manifest_json, proof_root_sha256, artifact_count FROM proof_manifests WHERE source_id = ?`, sourceID).
		Scan(&headerJSON, &root, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return models.PublicProofManifest{}, false, nil
	}
	if err != nil {
		return models.PublicProofManifest{}, false, fmt.Errorf("public mirror store: read proof manifest: %w", err)
	}
	var manifest models.PublicProofManifest
	if err := decodePublicMirrorJSON(headerJSON, &manifest); err != nil {
		return models.PublicProofManifest{}, false, publicMirrorCorrupt("decode proof manifest %s: %v", sourceID, err)
	}
	rows, err := sqliteutil.MaterializeRows(ctx, s.db, `
		SELECT entry_json FROM proof_manifest_entries
		WHERE source_id = ?
		  AND artifact_id NOT IN (SELECT artifact_id FROM proof_withdrawn_artifacts WHERE source_id = ?)
		ORDER BY segment_index, ordinal`,
		[]interface{}{sourceID, sourceID}, func(rows *sql.Rows) (string, error) {
			var jsonStr string
			return jsonStr, rows.Scan(&jsonStr)
		})
	if err != nil {
		return models.PublicProofManifest{}, false, fmt.Errorf("public mirror store: read proof manifest entries: %w", err)
	}
	manifest.Artifacts = make([]models.PublicProofCatalogEntry, len(rows))
	for index, rawJSON := range rows {
		if err := decodePublicMirrorJSON(rawJSON, &manifest.Artifacts[index]); err != nil {
			return models.PublicProofManifest{}, false, publicMirrorCorrupt("decode proof manifest %s entry: %v", sourceID, err)
		}
	}
	manifest.ArtifactCount = len(manifest.Artifacts)
	if manifest.ProofRootSHA256 != root || manifest.ArtifactCount != count {
		return models.PublicProofManifest{}, false, publicMirrorCorrupt("proof manifest %s summary mismatch", sourceID)
	}
	return manifest, true, nil
}

// proofSegmentHead reads one indexed chain position. Index zero means the head.
func (s *PublicMirrorStore) proofSegmentHead(ctx context.Context, sourceID string, index int64) (int64, string, error) {
	query := `SELECT segment_index, proof_root_sha256 FROM proof_manifest_segments WHERE source_id = ? ORDER BY segment_index DESC LIMIT 1`
	args := []interface{}{sourceID}
	if index > 0 {
		query = `SELECT segment_index, proof_root_sha256 FROM proof_manifest_segments WHERE source_id = ? AND segment_index = ?`
		args = append(args, index)
	}
	var foundIndex int64
	var root string
	err := s.db.QueryRowWithRetry(ctx, query, args...).Scan(&foundIndex, &root)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, constants.PublicFeedZeroHashHex, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("public mirror store: read proof chain position: %w", err)
	}
	return foundIndex, root, nil
}

// ProofSegments returns all signed proof manifest segments for one source in order.
func (s *PublicMirrorStore) ProofSegments(ctx context.Context, sourceID string) ([]models.PublicProofManifestSegment, error) {
	segments, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT segment_index, previous_root, proof_root_sha256, signing_key_id, signature, generated_at, segment_json FROM proof_manifest_segments WHERE source_id = ? ORDER BY segment_index`,
		[]interface{}{sourceID}, func(rows *sql.Rows) (models.PublicProofManifestSegment, error) {
			var seg models.PublicProofManifestSegment
			var index int64
			var previousRoot, root, keyID, signature, generatedAt, raw string
			if err := rows.Scan(&index, &previousRoot, &root, &keyID, &signature, &generatedAt, &raw); err != nil {
				return seg, err
			}
			if err := decodePublicMirrorJSON(raw, &seg); err != nil {
				return seg, publicMirrorCorrupt("decode proof segment %s #%d: %v", sourceID, index, err)
			}
			if seg.SourceID != sourceID || seg.SegmentIndex != index || seg.PreviousRoot != previousRoot ||
				seg.ProofRootSHA256 != root || seg.SigningKeyID != keyID || seg.Signature != signature || formatPublicMirrorTime(seg.GeneratedAt) != generatedAt {
				return seg, publicMirrorCorrupt("proof segment columns differ from signed data for %s #%d", sourceID, index)
			}
			return seg, nil
		})
	if err != nil {
		return nil, fmt.Errorf("public mirror store: read proof segments: %w", err)
	}
	return segments, nil
}

// ProofCatalog returns the derived catalog of active proof artifacts.
func (s *PublicMirrorStore) ProofCatalog(ctx context.Context, sourceID string) (models.PublicProofCatalog, bool, error) {
	manifest, found, err := s.ProofManifest(ctx, sourceID)
	if err != nil || !found {
		return models.PublicProofCatalog{}, found, err
	}
	return proofCatalogFromManifest(manifest), true, nil
}

func decodePublicMirrorJSON(value string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func (s *PublicMirrorStore) proofSummary(ctx context.Context, sourceID string) (PublicMirrorProofSummary, bool, error) {
	var summary PublicMirrorProofSummary
	var generatedAt string
	err := s.db.QueryRowWithRetry(ctx, `SELECT proof_root_sha256, artifact_count, total_byte_size, generated_at,
		COALESCE((SELECT MAX(segment_index) FROM proof_manifest_segments WHERE source_id = proof_manifests.source_id), 0)
		FROM proof_manifests WHERE source_id = ?`, sourceID).
		Scan(&summary.ProofRootSHA256, &summary.ArtifactCount, &summary.TotalByteSize, &generatedAt, &summary.SegmentIndex)
	if errors.Is(err, sql.ErrNoRows) {
		return PublicMirrorProofSummary{}, false, nil
	}
	if err != nil {
		return PublicMirrorProofSummary{}, false, fmt.Errorf("public mirror store: read proof summary: %w", err)
	}
	parsed, err := parsePublicMirrorTime(generatedAt)
	if err != nil {
		return PublicMirrorProofSummary{}, false, err
	}
	summary.GeneratedAt = parsed
	return summary, true, nil
}

// proofArtifact returns a stored artifact's size and, when a manifest lists
// it, the catalog entry that supplies its download headers.
func (s *PublicMirrorStore) proofArtifact(ctx context.Context, artifactID string) (publicMirrorProofArtifact, bool, error) {
	var artifact publicMirrorProofArtifact
	var entryJSON sql.NullString
	err := s.db.QueryRowWithRetry(ctx, `SELECT a.byte_size,
		(SELECT e.entry_json FROM proof_manifest_entries e WHERE e.artifact_id = a.artifact_id
		 AND NOT EXISTS (SELECT 1 FROM proof_withdrawn_artifacts w WHERE w.source_id = e.source_id AND w.artifact_id = e.artifact_id)
		 ORDER BY e.source_id, e.segment_index, e.ordinal LIMIT 1)
		FROM proof_artifacts a WHERE a.artifact_id = ?`, artifactID).Scan(&artifact.ByteSize, &entryJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return publicMirrorProofArtifact{}, false, nil
	}
	if err != nil {
		return publicMirrorProofArtifact{}, false, fmt.Errorf("public mirror store: read proof artifact: %w", err)
	}
	if !entryJSON.Valid {
		return publicMirrorProofArtifact{}, false, nil
	}
	if entryJSON.Valid {
		var entry models.PublicProofCatalogEntry
		if err := decodePublicMirrorJSON(entryJSON.String, &entry); err != nil {
			return publicMirrorProofArtifact{}, false, publicMirrorCorrupt("decode proof entry %s: %v", artifactID, err)
		}
		artifact.Entry = &entry
	}
	return artifact, true, nil
}

// proofArtifactSizes returns the stored size of every listed artifact the
// store already holds.
func (s *PublicMirrorStore) proofArtifactSizes(ctx context.Context, artifactIDs []string) (map[string]int64, error) {
	sizes := make(map[string]int64, len(artifactIDs))
	for start := 0; start < len(artifactIDs); start += publicMirrorQueryChunk {
		chunk := artifactIDs[start:min(start+publicMirrorQueryChunk, len(artifactIDs))]
		args := make([]interface{}, len(chunk))
		for index, id := range chunk {
			args[index] = id
		}
		type sizeRow struct {
			id   string
			size int64
		}
		rows, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT artifact_id, byte_size FROM proof_artifacts WHERE artifact_id IN (?`+
			strings.Repeat(",?", len(chunk)-1)+`)`, args, func(rows *sql.Rows) (sizeRow, error) {
			var row sizeRow
			return row, rows.Scan(&row.id, &row.size)
		})
		if err != nil {
			return nil, fmt.Errorf("public mirror store: read proof artifact sizes: %w", err)
		}
		for _, row := range rows {
			info, err := s.fileSvc.Lstat(ctx, publicMirrorProofArtifactPath(row.id))
			if errors.Is(err, constants.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("public mirror store: inspect proof artifact: %w", err)
			}
			if !info.Mode().IsRegular() {
				return nil, constants.ErrPublicFeedProofSymlinkRejected
			}
			if info.Size() != row.size {
				return nil, constants.ErrPublicFeedProofSizeMismatch
			}
			sizes[row.id] = row.size
		}
	}
	return sizes, nil
}

// openProofArtifact opens one stored artifact file for reading.
func (s *PublicMirrorStore) openProofArtifact(ctx context.Context, artifactID string) (*os.File, error) {
	if !validPublicProofArtifactID(artifactID) {
		return nil, publicMirrorCorrupt("invalid proof artifact id")
	}
	file, err := s.fileSvc.OpenForRead(ctx, publicMirrorProofArtifactPath(artifactID))
	if err != nil {
		return nil, fmt.Errorf("public mirror store: open proof artifact %s: %w", artifactID, err)
	}
	return file, nil
}

// putProofSegment writes any new inline proof artifact files and in one
// transaction records the signed segment, appends its entries, records any
// withdrawals, and updates the source's proof summary.
func (s *PublicMirrorStore) putProofSegment(ctx context.Context, sourceID string, segment models.PublicProofManifestSegment, artifacts []models.PublicProofIngestArtifact, storedSizes map[string]int64) error {
	newArtifacts := make([]models.PublicProofIngestArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, stored := storedSizes[artifact.ArtifactID]; stored {
			continue
		}
		if !validPublicProofArtifactID(artifact.ArtifactID) {
			return constants.ErrPublicFeedProofHashMismatch
		}
		if err := s.fileSvc.WriteFile(ctx, publicMirrorProofArtifactPath(artifact.ArtifactID), artifact.Content, constants.PermFilePrivate); err != nil {
			return fmt.Errorf("%w: write proof artifact %s: %w", constants.ErrPublicFeedMirrorStoreWrite, artifact.ArtifactID, err)
		}
		newArtifacts = append(newArtifacts, artifact)
	}

	segmentJSON, err := json.Marshal(segment)
	if err != nil {
		return fmt.Errorf("%w: encode proof segment: %w", constants.ErrPublicFeedMirrorStoreWrite, err)
	}

	entryJSON := make([]string, len(segment.Entries))
	for index, entry := range segment.Entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("%w: encode proof manifest entry: %w", constants.ErrPublicFeedMirrorStoreWrite, err)
		}
		entryJSON[index] = string(encoded)
	}

	storedAt := formatPublicMirrorTime(time.Now())
	return s.write(ctx, "put proof segment", func(tx *sql.Tx) error {
		for _, artifact := range newArtifacts {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO proof_artifacts (artifact_id, byte_size, stored_at) VALUES (?, ?, ?)`,
				artifact.ArtifactID, len(artifact.Content), storedAt); err != nil {
				return fmt.Errorf("insert proof artifact: %w", err)
			}
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO proof_manifest_segments (source_id, segment_index, previous_root, proof_root_sha256, signing_key_id, signature, segment_json, generated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sourceID, segment.SegmentIndex, segment.PreviousRoot, segment.ProofRootSHA256, segment.SigningKeyID, segment.Signature, string(segmentJSON), formatPublicMirrorTime(segment.GeneratedAt)); err != nil {
			return fmt.Errorf("upsert proof manifest segment: %w", err)
		}

		var activeCount int
		var totalByteSize int64
		if segment.SegmentIndex == 1 {
			// The source signs its first segment with the complete initial set.
			// Replace the legacy projection without inventing signed links.
			if _, err := tx.ExecContext(ctx, `DELETE FROM proof_manifest_entries WHERE source_id = ? AND segment_index = 0`, sourceID); err != nil {
				return fmt.Errorf("remove legacy proof projection: %w", err)
			}
		} else if err := tx.QueryRowContext(ctx, `SELECT artifact_count, total_byte_size FROM proof_manifests WHERE source_id = ?`, sourceID).Scan(&activeCount, &totalByteSize); err != nil {
			return fmt.Errorf("read proof summary: %w", err)
		}
		for _, entry := range segment.Entries {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM proof_manifest_entries WHERE source_id = ? AND artifact_id = ?)`, sourceID, entry.ArtifactID).Scan(&exists); err != nil {
				return fmt.Errorf("read prior proof entry: %w", err)
			}
			if exists {
				return constants.ErrPublicFeedProofSegmentEquivocation
			}
			activeCount++
			totalByteSize += entry.ByteSize
		}
		if activeCount > constants.PublicFeedProofMaxArtifacts {
			return constants.ErrPublicFeedProofCatalogMismatch
		}

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO proof_manifest_entries (source_id, segment_index, ordinal, artifact_id, entry_json) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare proof manifest entry upsert: %w", err)
		}
		defer stmt.Close()
		for index, encoded := range entryJSON {
			if _, err := stmt.ExecContext(ctx, sourceID, segment.SegmentIndex, index, segment.Entries[index].ArtifactID, encoded); err != nil {
				return fmt.Errorf("upsert proof manifest entry: %w", err)
			}
		}

		withdrawnStmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO proof_withdrawn_artifacts (source_id, artifact_id, segment_index, withdrawn_at) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare proof withdrawn insert: %w", err)
		}
		defer withdrawnStmt.Close()
		for _, artID := range segment.WithdrawnArtifactIDs {
			var byteSize int64
			err := tx.QueryRowContext(ctx, `SELECT a.byte_size FROM proof_manifest_entries e
				JOIN proof_artifacts a ON a.artifact_id = e.artifact_id
				WHERE e.source_id = ? AND e.artifact_id = ? AND NOT EXISTS
				(SELECT 1 FROM proof_withdrawn_artifacts w WHERE w.source_id = e.source_id AND w.artifact_id = e.artifact_id)`, sourceID, artID).Scan(&byteSize)
			if errors.Is(err, sql.ErrNoRows) {
				return constants.ErrPublicFeedProofSegmentInvalid
			}
			if err != nil {
				return fmt.Errorf("read withdrawn proof entry: %w", err)
			}
			activeCount--
			totalByteSize -= byteSize

			if _, err := withdrawnStmt.ExecContext(ctx, sourceID, artID, segment.SegmentIndex, storedAt); err != nil {
				return fmt.Errorf("insert proof withdrawn: %w", err)
			}
		}

		header := publicProofManifestProjection(segment, nil)
		header.ArtifactCount = activeCount
		headerJSON, err := json.Marshal(header)
		if err != nil {
			return fmt.Errorf("encode proof manifest header: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO proof_manifests (source_id, manifest_json, proof_root_sha256, artifact_count, total_byte_size, generated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_id) DO UPDATE SET
				manifest_json = excluded.manifest_json,
				proof_root_sha256 = excluded.proof_root_sha256,
				artifact_count = excluded.artifact_count,
				total_byte_size = excluded.total_byte_size,
				generated_at = excluded.generated_at`,
			sourceID, string(headerJSON), segment.ProofRootSHA256, activeCount, totalByteSize, formatPublicMirrorTime(segment.GeneratedAt)); err != nil {
			return fmt.Errorf("upsert proof manifest summary: %w", err)
		}

		return nil
	})
}

func (s *PublicMirrorStore) putProofPackage(ctx context.Context, sourceID string, manifest models.PublicProofManifest, artifacts []models.PublicProofIngestArtifact, storedSizes map[string]int64) error {
	newArtifacts := make([]models.PublicProofIngestArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, stored := storedSizes[artifact.ArtifactID]; stored {
			continue
		}
		if !validPublicProofArtifactID(artifact.ArtifactID) {
			return constants.ErrPublicFeedProofHashMismatch
		}
		if err := s.fileSvc.WriteFile(ctx, publicMirrorProofArtifactPath(artifact.ArtifactID), artifact.Content, constants.PermFilePrivate); err != nil {
			return fmt.Errorf("%w: write proof artifact %s: %w", constants.ErrPublicFeedMirrorStoreWrite, artifact.ArtifactID, err)
		}
		newArtifacts = append(newArtifacts, artifact)
	}
	header := manifest
	header.Artifacts = nil
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("%w: encode proof manifest: %w", constants.ErrPublicFeedMirrorStoreWrite, err)
	}
	entryJSON := make([]string, len(manifest.Artifacts))
	var totalByteSize int64
	for index, entry := range manifest.Artifacts {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("%w: encode proof manifest entry: %w", constants.ErrPublicFeedMirrorStoreWrite, err)
		}
		entryJSON[index] = string(encoded)
		totalByteSize += entry.ByteSize
	}
	storedAt := formatPublicMirrorTime(time.Now())
	return s.write(ctx, "put proof package", func(tx *sql.Tx) error {
		for _, artifact := range newArtifacts {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO proof_artifacts (artifact_id, byte_size, stored_at) VALUES (?, ?, ?)`,
				artifact.ArtifactID, len(artifact.Content), storedAt); err != nil {
				return fmt.Errorf("insert proof artifact: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO proof_manifests (source_id, manifest_json, proof_root_sha256, artifact_count, total_byte_size, generated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_id) DO UPDATE SET
				manifest_json = excluded.manifest_json,
				proof_root_sha256 = excluded.proof_root_sha256,
				artifact_count = excluded.artifact_count,
				total_byte_size = excluded.total_byte_size,
				generated_at = excluded.generated_at`,
			sourceID, string(headerJSON), manifest.ProofRootSHA256, manifest.ArtifactCount, totalByteSize, formatPublicMirrorTime(manifest.GeneratedAt)); err != nil {
			return fmt.Errorf("upsert proof manifest: %w", err)
		}
		existing, err := queryPublicMirrorManifestEntries(ctx, tx, sourceID)
		if err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO proof_manifest_entries (source_id, segment_index, ordinal, artifact_id, entry_json) VALUES (?, 0, ?, ?, ?)
			ON CONFLICT(source_id, segment_index, ordinal) DO UPDATE SET artifact_id = excluded.artifact_id, entry_json = excluded.entry_json`)
		if err != nil {
			return fmt.Errorf("prepare proof manifest entry upsert: %w", err)
		}
		defer stmt.Close()
		for index, encoded := range entryJSON {
			if index < len(existing) && existing[index] == encoded {
				continue
			}
			if _, err := stmt.ExecContext(ctx, sourceID, index, manifest.Artifacts[index].ArtifactID, encoded); err != nil {
				return fmt.Errorf("upsert proof manifest entry: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM proof_manifest_entries WHERE source_id = ? AND segment_index = 0 AND ordinal >= ?`, sourceID, len(entryJSON)); err != nil {
			return fmt.Errorf("trim proof manifest entries: %w", err)
		}
		return nil
	})
}

func queryPublicMirrorManifestEntries(ctx context.Context, tx *sql.Tx, sourceID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT entry_json FROM proof_manifest_entries WHERE source_id = ? AND segment_index = 0 ORDER BY ordinal`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("read proof manifest entries: %w", err)
	}
	defer rows.Close()
	var entries []string
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			return nil, fmt.Errorf("scan proof manifest entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read proof manifest entries: %w", err)
	}
	return entries, nil
}

// verify re-checks every durable invariant the mirror serves from: key
// registry shape, each source's retained batch chain (signatures, content
// hashes, record hashes, revocations, summary), every proof artifact file,
// and every proof manifest.
func (s *PublicMirrorStore) verify(ctx context.Context) error {
	state, err := s.loadState(ctx)
	if err != nil {
		return err
	}
	for registryID, key := range state.keys.Registry {
		if registryID == "" || len(key) != ed25519.PublicKeySize {
			return publicMirrorCorrupt("invalid key registry entry")
		}
	}
	if state.activeSourceID != "" {
		activeKeyKnown := false
		for registryID := range state.keys.Registry {
			if strings.HasPrefix(registryID, state.activeSourceID+":") {
				activeKeyKnown = true
				break
			}
		}
		if !activeKeyKnown {
			return publicMirrorCorrupt("active source key is missing")
		}
	}
	for _, summary := range state.sources {
		if err := s.verifySource(ctx, *summary, state.keys); err != nil {
			return err
		}
	}
	sizes, err := s.verifyProofArtifacts(ctx)
	if err != nil {
		return err
	}
	return s.verifyProofManifests(ctx, state.keys, sizes)
}

func (s *PublicMirrorStore) verifySource(ctx context.Context, summary PublicMirrorSourceSummary, keys PublicMirrorKeys) error {
	sourceID := summary.SourceID
	if sourceID == "" || summary.RetainedFromSequence <= 0 || summary.RetainedPreviousBatchHash == "" || summary.BatchCount < summary.RetainedBatchCount {
		return publicMirrorCorrupt("invalid source state %s", sourceID)
	}
	previousHash := summary.RetainedPreviousBatchHash
	previousSequence := summary.RetainedFromSequence - 1
	verifiedRecords := 0
	var verifiedBytes int64
	after := int64(0)
	for {
		headers, err := sqliteutil.MaterializeRows(ctx, s.db, `
			SELECT first_sequence, last_sequence, protocol_version, schema_version, previous_batch_hash,
			       generated_at, content_hash, signing_key_id, signature
			FROM batches WHERE source_id = ? AND first_sequence > ? ORDER BY first_sequence LIMIT ?`,
			[]interface{}{sourceID, after, publicMirrorVerifyPageBatches}, func(rows *sql.Rows) (models.PublicFeedBatch, error) {
				batch := models.PublicFeedBatch{SourceID: sourceID}
				var generatedAt string
				if err := rows.Scan(&batch.FirstSequence, &batch.LastSequence, &batch.ProtocolVersion, &batch.SchemaVersion,
					&batch.PreviousBatchHash, &generatedAt, &batch.ContentHash, &batch.SigningKeyID, &batch.Signature); err != nil {
					return models.PublicFeedBatch{}, err
				}
				parsed, err := time.Parse(time.RFC3339Nano, generatedAt)
				if err != nil {
					return models.PublicFeedBatch{}, publicMirrorCorrupt("parse batch time: %v", err)
				}
				batch.GeneratedAt = parsed.UTC()
				return batch, nil
			})
		if err != nil {
			return fmt.Errorf("public mirror store: read batches: %w", err)
		}
		if len(headers) == 0 {
			break
		}
		for _, batch := range headers {
			if batch.FirstSequence != previousSequence+1 || batch.PreviousBatchHash != previousHash || batch.LastSequence < batch.FirstSequence {
				return publicMirrorCorrupt("invalid source batch chain %s@%d", sourceID, batch.FirstSequence)
			}
			records, err := s.recordsInRange(ctx, sourceID, batch.FirstSequence, batch.LastSequence)
			if err != nil {
				return err
			}
			if int64(len(records)) != batch.LastSequence-batch.FirstSequence+1 {
				return publicMirrorCorrupt("missing source records %s@%d", sourceID, batch.FirstSequence)
			}
			batch.Records = records
			batch.RecordHashes = make([]string, len(records))
			for index, record := range records {
				hash := sha256.Sum256([]byte(record.RecordBytes))
				if record.Sequence != batch.FirstSequence+int64(index) || record.RecordHash != hex.EncodeToString(hash[:]) {
					return publicMirrorCorrupt("invalid source record %s@%d", sourceID, record.Sequence)
				}
				batch.RecordHashes[index] = record.RecordHash
				verifiedBytes += int64(len(record.RecordBytes))
			}
			key, ok := keys.Registry[publicMirrorKeyRegistryID(sourceID, batch.SigningKeyID)]
			if !ok || verifyBatchSignature(batch, key) != nil {
				return publicMirrorCorrupt("invalid source batch signature %s@%d", sourceID, batch.FirstSequence)
			}
			revocations, err := validatePublicKeyRevocations(batch, keys)
			if err != nil {
				return publicMirrorCorrupt("invalid key revocation: %v", err)
			}
			for _, revocation := range revocations {
				revokedAt, ok := keys.Revoked[publicMirrorKeyRegistryID(revocation.SourceID, revocation.RevokedKeyID)]
				if !ok || !revokedAt.Equal(revocation.RevokedAt) {
					return publicMirrorCorrupt("key revocation state mismatch")
				}
			}
			previousHash = batch.ContentHash
			previousSequence = batch.LastSequence
			verifiedRecords += len(records)
		}
		after = headers[len(headers)-1].FirstSequence
	}
	if summary.RetainedBatchCount > 0 {
		if summary.HighWaterSequence != previousSequence || summary.FeedChainHash != previousHash || summary.RetainedRecordCount != verifiedRecords || summary.RetainedRecordBytes != verifiedBytes || summary.LastAcceptedAt.IsZero() {
			return publicMirrorCorrupt("source summary mismatch %s", sourceID)
		}
	} else if summary.HighWaterSequence != 0 || summary.BatchCount != 0 || summary.RetainedRecordCount != 0 || summary.RetainedRecordBytes != 0 {
		return publicMirrorCorrupt("empty source summary mismatch %s", sourceID)
	}
	return nil
}

// verifyProofArtifacts rehashes every stored artifact file and returns the
// verified size of each.
func (s *PublicMirrorStore) verifyProofArtifacts(ctx context.Context) (map[string]int64, error) {
	type artifactRow struct {
		id   string
		size int64
	}
	rows, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT artifact_id, byte_size FROM proof_artifacts`, nil, func(rows *sql.Rows) (artifactRow, error) {
		var row artifactRow
		return row, rows.Scan(&row.id, &row.size)
	})
	if err != nil {
		return nil, fmt.Errorf("public mirror store: read proof artifacts: %w", err)
	}
	sizes := make(map[string]int64, len(rows))
	for _, row := range rows {
		if !validPublicProofArtifactID(row.id) || row.size < 0 || row.size > constants.PublicFeedMaxArtifactBytes {
			return nil, publicMirrorCorrupt("invalid proof artifact %s", row.id)
		}
		info, err := s.fileSvc.Lstat(ctx, publicMirrorProofArtifactPath(row.id))
		if err != nil {
			return nil, publicMirrorCorrupt("stat proof artifact %s: %v", row.id, err)
		}
		if !info.Mode().IsRegular() || info.Size() != row.size {
			return nil, publicMirrorCorrupt("proof artifact %s file mismatch", row.id)
		}
		digest, err := s.hashProofArtifact(ctx, row.id)
		if err != nil {
			return nil, err
		}
		if digest != row.id {
			return nil, publicMirrorCorrupt("proof artifact %s hash mismatch", row.id)
		}
		sizes[row.id] = row.size
	}
	return sizes, nil
}

func (s *PublicMirrorStore) hashProofArtifact(ctx context.Context, artifactID string) (string, error) {
	file, err := s.openProofArtifact(ctx, artifactID)
	if err != nil {
		return "", publicMirrorCorrupt("open proof artifact %s: %v", artifactID, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", publicMirrorCorrupt("read proof artifact %s: %v", artifactID, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *PublicMirrorStore) verifyProofManifests(ctx context.Context, keys PublicMirrorKeys, sizes map[string]int64) error {
	sourceIDs, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT source_id FROM proof_manifests UNION SELECT source_id FROM proof_manifest_segments ORDER BY source_id`, nil, func(rows *sql.Rows) (string, error) {
		var sourceID string
		return sourceID, rows.Scan(&sourceID)
	})
	if err != nil {
		return fmt.Errorf("public mirror store: read proof manifests: %w", err)
	}
	for _, sourceID := range sourceIDs {
		segments, err := s.ProofSegments(ctx, sourceID)
		if err != nil {
			return err
		}
		if len(segments) > 0 {
			expectedPrevRoot := constants.PublicFeedZeroHashHex
			for i, seg := range segments {
				if seg.SegmentIndex != int64(i+1) {
					return publicMirrorCorrupt("proof segment sequence out of order for %s: index %d expected %d", sourceID, seg.SegmentIndex, i+1)
				}
				if seg.PreviousRoot != expectedPrevRoot {
					return publicMirrorCorrupt("proof segment link broken for %s: got %s expected %s", sourceID, seg.PreviousRoot, expectedPrevRoot)
				}
				if err := validatePublicProofSegment(sourceID, seg, keys, false); err != nil {
					return publicMirrorCorrupt("proof segment invalid for %s #%d: %v", sourceID, seg.SegmentIndex, err)
				}
				for _, entry := range seg.Entries {
					if size, ok := sizes[entry.ArtifactID]; !ok || size != entry.ByteSize {
						return publicMirrorCorrupt("proof segment entry missing stored artifact %s for %s", entry.ArtifactID, sourceID)
					}
				}
				expectedPrevRoot = seg.ProofRootSHA256
			}
			summary, _, err := s.proofSummary(ctx, sourceID)
			if err != nil {
				return err
			}
			lastSeg := segments[len(segments)-1]
			if summary.ProofRootSHA256 != lastSeg.ProofRootSHA256 {
				return publicMirrorCorrupt("proof summary root mismatch for %s: got %s expected %s", sourceID, summary.ProofRootSHA256, lastSeg.ProofRootSHA256)
			}
		}

		manifest, found, err := s.ProofManifest(ctx, sourceID)
		if err != nil {
			return err
		}
		if !found {
			return publicMirrorCorrupt("proof manifest missing for %s", sourceID)
		}
		if len(segments) > 0 {
			if err := s.verifyProofSegmentProjection(ctx, sourceID, segments, manifest); err != nil {
				return err
			}
		}
		if len(segments) == 0 {
			if err := validatePublicProofManifest(sourceID, manifest, keys, false); err != nil {
				return publicMirrorCorrupt("proof metadata for %s: %v", sourceID, err)
			}
		}
		if err := validatePublicProofEntryList(manifest.Artifacts, nil, sizes); err != nil {
			return publicMirrorCorrupt("proof metadata for %s: %v", sourceID, err)
		}
		summary, _, err := s.proofSummary(ctx, sourceID)
		if err != nil {
			return err
		}
		var total int64
		for _, entry := range manifest.Artifacts {
			total += entry.ByteSize
		}
		if summary.ProofRootSHA256 != manifest.ProofRootSHA256 || summary.ArtifactCount != manifest.ArtifactCount ||
			summary.TotalByteSize != total || !summary.GeneratedAt.Equal(manifest.GeneratedAt) {
			return publicMirrorCorrupt("proof summary mismatch for %s", sourceID)
		}
	}
	return nil
}

// verifyProofSegmentProjection compares every persisted projection row (including
// withdrawn entries) with the signed log. Content hashes alone cannot detect
// tampered filenames, membership, or withdrawal state.
func (s *PublicMirrorStore) verifyProofSegmentProjection(ctx context.Context, sourceID string, segments []models.PublicProofManifestSegment, manifest models.PublicProofManifest) error {
	type position struct {
		segment int64
		ordinal int
	}
	expected := make(map[position]models.PublicProofCatalogEntry)
	listed := make(map[string]bool)
	withdrawn := make(map[string]int64)
	for _, seg := range segments {
		for ordinal, entry := range seg.Entries {
			if listed[entry.ArtifactID] {
				return publicMirrorCorrupt("repeated segment artifact %s for %s", entry.ArtifactID, sourceID)
			}
			listed[entry.ArtifactID] = true
			entry.GeneratedAt = entry.GeneratedAt.UTC()
			expected[position{seg.SegmentIndex, ordinal}] = entry
		}
		for _, id := range seg.WithdrawnArtifactIDs {
			if !listed[id] || withdrawn[id] != 0 {
				return publicMirrorCorrupt("invalid withdrawal %s for %s", id, sourceID)
			}
			withdrawn[id] = seg.SegmentIndex
		}
	}
	_, err := sqliteutil.MaterializeRows(ctx, s.db, `SELECT segment_index, ordinal, artifact_id, entry_json FROM proof_manifest_entries WHERE source_id = ?`, []interface{}{sourceID}, func(rows *sql.Rows) (bool, error) {
		var pos position
		var id, raw string
		if err := rows.Scan(&pos.segment, &pos.ordinal, &id, &raw); err != nil {
			return false, err
		}
		var actual models.PublicProofCatalogEntry
		if err := decodePublicMirrorJSON(raw, &actual); err != nil {
			return false, publicMirrorCorrupt("decode proof projection for %s: %v", sourceID, err)
		}
		actual.GeneratedAt = actual.GeneratedAt.UTC()
		entry, ok := expected[pos]
		if !ok || actual != entry || id != entry.ArtifactID {
			return false, publicMirrorCorrupt("proof projection differs from segment for %s #%d entry %d", sourceID, pos.segment, pos.ordinal)
		}
		delete(expected, pos)
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("verify proof entries: %w", err)
	}
	if len(expected) != 0 {
		return publicMirrorCorrupt("proof projection entries missing for %s", sourceID)
	}
	_, err = sqliteutil.MaterializeRows(ctx, s.db, `SELECT artifact_id, segment_index FROM proof_withdrawn_artifacts WHERE source_id = ?`, []interface{}{sourceID}, func(rows *sql.Rows) (bool, error) {
		var id string
		var index int64
		if err := rows.Scan(&id, &index); err != nil {
			return false, err
		}
		if expectedIndex, ok := withdrawn[id]; !ok || index != expectedIndex {
			return false, publicMirrorCorrupt("withdrawal projection differs from segment for %s artifact %s", sourceID, id)
		}
		delete(withdrawn, id)
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("verify proof withdrawals: %w", err)
	}
	if len(withdrawn) != 0 {
		return publicMirrorCorrupt("proof withdrawal entries missing for %s", sourceID)
	}
	last := segments[len(segments)-1]
	if manifest.SchemaVersion != constants.PublicProofManifestSchemaVersion || manifest.SegmentIndex != last.SegmentIndex || manifest.ProofRootSHA256 != last.ProofRootSHA256 ||
		manifest.CampaignID != last.CampaignID || manifest.CampaignRevision != last.CampaignRevision ||
		manifest.VerifiedIndexGenerationHash != last.VerifiedIndexGenerationHash || manifest.VerificationOK != last.VerificationOK ||
		manifest.SigningKeyID != last.SigningKeyID || manifest.Signature != last.Signature ||
		manifest.VerifierInstructions != last.VerifierInstructions || !manifest.GeneratedAt.Equal(last.GeneratedAt) {
		return publicMirrorCorrupt("proof header differs from last segment for %s", sourceID)
	}
	return nil
}
