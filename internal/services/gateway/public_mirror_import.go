// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

// One-time import of the legacy JSON mirror store (public-mirror/state.json)
// into the SQLite mirror store. Remove this file after v1.1.3.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

type legacyPublicMirrorSourceState struct {
	HighWaterSequence         int64                     `json:"high_water_sequence"`
	FeedChainHash             string                    `json:"feed_chain_hash"`
	BatchCount                int                       `json:"batch_count"`
	RetainedFromSequence      int64                     `json:"retained_from_sequence"`
	RetainedPreviousBatchHash string                    `json:"retained_previous_batch_hash"`
	Records                   []models.PublicFeedRecord `json:"records"`
	Batches                   []models.PublicFeedBatch  `json:"batches"`
	Freshness                 models.CampaignFreshness  `json:"freshness"`
	LastAcceptedAt            time.Time                 `json:"last_accepted_at"`
	WithdrawnDatasetIDs       map[string]bool           `json:"withdrawn_dataset_ids,omitempty"`
}

type legacyPublicMirrorStoreState struct {
	ActiveSourceID string                                    `json:"active_source_id"`
	Sources        map[string]*legacyPublicMirrorSourceState `json:"sources"`
	KeyRegistry    map[string]ed25519.PublicKey              `json:"key_registry"`
	RevokedKeys    map[string]time.Time                      `json:"revoked_keys"`
	ProofArtifacts map[string][]byte                         `json:"proof_artifacts"`
	ProofManifests map[string]models.PublicProofManifest     `json:"proof_manifests"`
}

// importLegacyPublicMirrorState converts a legacy state.json into mirror.db
// once. The import builds a separate database, verifies it with the same
// checks as every open, and only then renames it into place and removes
// state.json, so a failed import leaves the legacy file untouched.
func importLegacyPublicMirrorState(ctx context.Context, fileSvc fs.RuntimeFileService, logger *slog.Logger) error {
	legacyExists, err := fileSvc.FileExists(ctx, constants.PublicMirrorStatePath)
	if err != nil {
		return fmt.Errorf("public mirror import: stat legacy state: %w", err)
	}
	if !legacyExists {
		return nil
	}
	dbExists, err := fileSvc.FileExists(ctx, constants.PublicMirrorDBPath)
	if err != nil {
		return fmt.Errorf("public mirror import: stat mirror db: %w", err)
	}
	if dbExists {
		return constants.ErrPublicFeedMirrorLegacyStateConflict
	}
	data, err := fileSvc.ReadFile(ctx, constants.PublicMirrorStatePath)
	if err != nil {
		return fmt.Errorf("public mirror import: read legacy state: %w", err)
	}
	var legacy legacyPublicMirrorStoreState
	if err := json.Unmarshal(data, &legacy); err != nil {
		return publicMirrorCorrupt("decode legacy state: %v", err)
	}
	data = nil
	if err := normalizeLegacyPublicMirrorState(&legacy); err != nil {
		return err
	}
	for artifactID, content := range legacy.ProofArtifacts {
		hash := sha256.Sum256(content)
		if artifactID != hex.EncodeToString(hash[:]) || len(content) > constants.PublicFeedMaxArtifactBytes {
			return publicMirrorCorrupt("invalid legacy proof artifact")
		}
		if err := fileSvc.WriteFile(ctx, publicMirrorProofArtifactPath(artifactID), content, constants.PermFilePrivate); err != nil {
			return fmt.Errorf("%w: write proof artifact %s: %w", constants.ErrPublicFeedMirrorStoreWrite, artifactID, err)
		}
	}
	if err := removePublicMirrorImportDB(ctx, fileSvc); err != nil {
		return err
	}
	store, err := openPublicMirrorStoreAt(ctx, fileSvc, logger, constants.PublicMirrorImportDBPath)
	if err != nil {
		return err
	}
	err = store.importLegacy(ctx, legacy)
	if err == nil {
		err = store.verify(ctx)
	}
	if err != nil {
		_ = store.Close()
		return errors.Join(err, removePublicMirrorImportDB(ctx, fileSvc))
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("public mirror import: close import db: %w", err)
	}
	if err := fileSvc.Rename(ctx, constants.PublicMirrorImportDBPath, constants.PublicMirrorDBPath); err != nil {
		return fmt.Errorf("public mirror import: install mirror db: %w", err)
	}
	if err := removePublicMirrorImportDB(ctx, fileSvc); err != nil {
		return err
	}
	if err := fileSvc.Remove(ctx, constants.PublicMirrorStatePath); err != nil {
		return fmt.Errorf("public mirror import: remove legacy state: %w", err)
	}
	logger.Info("public mirror: imported legacy state.json into mirror.db",
		"sources", len(legacy.Sources), "proof_artifacts", len(legacy.ProofArtifacts), "proof_manifests", len(legacy.ProofManifests))
	return nil
}

// removePublicMirrorImportDB clears the import database and its SQLite
// sidecar files.
func removePublicMirrorImportDB(ctx context.Context, fileSvc fs.RuntimeFileService) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		relPath := constants.PublicMirrorImportDBPath + suffix
		exists, err := fileSvc.FileExists(ctx, relPath)
		if err != nil {
			return fmt.Errorf("public mirror import: stat %s: %w", relPath, err)
		}
		if !exists {
			continue
		}
		if err := fileSvc.Remove(ctx, relPath); err != nil {
			return fmt.Errorf("public mirror import: remove %s: %w", relPath, err)
		}
	}
	return nil
}

func normalizeLegacyPublicMirrorState(state *legacyPublicMirrorStoreState) error {
	if state.Sources == nil {
		state.Sources = map[string]*legacyPublicMirrorSourceState{}
	}
	for sourceID, source := range state.Sources {
		if sourceID == "" || source == nil {
			return publicMirrorCorrupt("invalid legacy source state")
		}
		if source.RetainedFromSequence == 0 {
			source.RetainedFromSequence = 1
		}
		if source.RetainedPreviousBatchHash == "" {
			source.RetainedPreviousBatchHash = constants.PublicFeedZeroHashHex
		}
	}
	if state.ActiveSourceID == "" && len(state.Sources) == 1 {
		for sourceID := range state.Sources {
			state.ActiveSourceID = sourceID
		}
	}
	return nil
}

func (s *PublicMirrorStore) importLegacy(ctx context.Context, legacy legacyPublicMirrorStoreState) error {
	sourceIDs := make([]string, 0, len(legacy.Sources))
	for sourceID := range legacy.Sources {
		sourceIDs = append(sourceIDs, sourceID)
	}
	sort.Strings(sourceIDs)
	storedAt := formatPublicMirrorTime(time.Now())
	return s.write(ctx, "import legacy state", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE mirror_state SET active_source_id = ? WHERE id = 1`, legacy.ActiveSourceID); err != nil {
			return err
		}
		for _, sourceID := range sourceIDs {
			source := legacy.Sources[sourceID]
			summary := PublicMirrorSourceSummary{
				SourceID:                  sourceID,
				HighWaterSequence:         source.HighWaterSequence,
				FeedChainHash:             source.FeedChainHash,
				BatchCount:                source.BatchCount,
				RetainedFromSequence:      source.RetainedFromSequence,
				RetainedPreviousBatchHash: source.RetainedPreviousBatchHash,
				Freshness:                 source.Freshness,
				LastAcceptedAt:            source.LastAcceptedAt,
			}
			if err := upsertPublicMirrorSource(ctx, tx, summary); err != nil {
				return err
			}
			if err := insertPublicMirrorRecords(ctx, tx, sourceID, source.Records); err != nil {
				return err
			}
			for _, batch := range source.Batches {
				if batch.SourceID != sourceID {
					return publicMirrorCorrupt("legacy batch source mismatch")
				}
				if err := insertPublicMirrorBatchHeader(ctx, tx, batch); err != nil {
					return err
				}
			}
			for datasetID, withdrawn := range source.WithdrawnDatasetIDs {
				if !withdrawn {
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO withdrawn_datasets (source_id, dataset_id) VALUES (?, ?)`, sourceID, datasetID); err != nil {
					return fmt.Errorf("insert legacy withdrawal: %w", err)
				}
			}
		}
		for registryID, key := range legacy.KeyRegistry {
			if _, err := tx.ExecContext(ctx, `INSERT INTO key_registry (registry_id, public_key) VALUES (?, ?)`, registryID, []byte(key)); err != nil {
				return fmt.Errorf("insert legacy key: %w", err)
			}
		}
		for registryID, revokedAt := range legacy.RevokedKeys {
			if _, err := tx.ExecContext(ctx, `INSERT INTO revoked_keys (registry_id, revoked_at) VALUES (?, ?)`, registryID, formatPublicMirrorTime(revokedAt)); err != nil {
				return fmt.Errorf("insert legacy revocation: %w", err)
			}
		}
		for artifactID, content := range legacy.ProofArtifacts {
			if _, err := tx.ExecContext(ctx, `INSERT INTO proof_artifacts (artifact_id, byte_size, stored_at) VALUES (?, ?, ?)`, artifactID, len(content), storedAt); err != nil {
				return fmt.Errorf("insert legacy proof artifact: %w", err)
			}
		}
		for sourceID, manifest := range legacy.ProofManifests {
			header := manifest
			header.Artifacts = nil
			headerJSON, err := json.Marshal(header)
			if err != nil {
				return fmt.Errorf("encode legacy proof manifest: %w", err)
			}
			var total int64
			for _, entry := range manifest.Artifacts {
				total += entry.ByteSize
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO proof_manifests (source_id, manifest_json, proof_root_sha256, artifact_count, total_byte_size, generated_at)
				VALUES (?, ?, ?, ?, ?, ?)`, sourceID, string(headerJSON), manifest.ProofRootSHA256, manifest.ArtifactCount, total, formatPublicMirrorTime(manifest.GeneratedAt)); err != nil {
				return fmt.Errorf("insert legacy proof manifest: %w", err)
			}
			for ordinal, entry := range manifest.Artifacts {
				entryJSON, err := json.Marshal(entry)
				if err != nil {
					return fmt.Errorf("encode legacy proof manifest entry: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO proof_manifest_entries (source_id, ordinal, artifact_id, entry_json) VALUES (?, ?, ?, ?)`,
					sourceID, ordinal, entry.ArtifactID, string(entryJSON)); err != nil {
					return fmt.Errorf("insert legacy proof manifest entry: %w", err)
				}
			}
		}
		return nil
	})
}
