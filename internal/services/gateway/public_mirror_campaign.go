// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

const campaignMirrorDatasetPrefix = "ds-live-"

func campaignMirrorDatasetID(runID string) string {
	return campaignMirrorDatasetPrefix + runID
}

func mirrorProjectionMatchesCampaignRun(item models.PublicFeedObject, runID string) bool {
	if runID == "" {
		return false
	}
	datasetID := campaignMirrorDatasetID(runID)
	if id, ok := item.StringField("dataset_id"); ok && id == datasetID {
		return true
	}
	if id, ok := item.StringField("run_id"); ok && id == runID {
		return true
	}
	if key, ok := item.StringField("idempotency_key"); ok && strings.HasPrefix(key, runID+":") {
		return true
	}
	if recordBytes, ok := item["record"]; ok {
		var record models.PublicFeedObject
		if err := json.Unmarshal(recordBytes, &record); err == nil {
			if id, ok := record.StringField("run_id"); ok && id == runID {
				return true
			}
			if id, ok := record.StringField("dataset_id"); ok && id == datasetID {
				return true
			}
		}
	}
	return false
}

func mirrorProjectionWithdrawn(withdrawn map[string]bool, item models.PublicFeedObject) bool {
	if len(withdrawn) == 0 {
		return false
	}
	if datasetID, ok := item.StringField("dataset_id"); ok && withdrawn[datasetID] {
		return true
	}
	if runID, ok := item.StringField("run_id"); ok && withdrawn[campaignMirrorDatasetID(runID)] {
		return true
	}
	for withdrawnDatasetID := range withdrawn {
		if !strings.HasPrefix(withdrawnDatasetID, campaignMirrorDatasetPrefix) {
			continue
		}
		runID := strings.TrimPrefix(withdrawnDatasetID, campaignMirrorDatasetPrefix)
		if mirrorProjectionMatchesCampaignRun(item, runID) {
			return true
		}
	}
	return false
}

// mirrorWithdrawalDigest is the SHA-256 over the source's sorted withdrawn
// dataset IDs, or the zero hash when nothing is withdrawn.
func mirrorWithdrawalDigest(withdrawn map[string]bool) string {
	if len(withdrawn) == 0 {
		return constants.PublicFeedZeroHashHex
	}
	ids := make([]string, 0, len(withdrawn))
	for id, isWithdrawn := range withdrawn {
		if isWithdrawn {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return constants.PublicFeedZeroHashHex
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return hex.EncodeToString(sum[:])
}

func mirrorRecordWithdrawn(withdrawn map[string]bool, record models.PublicFeedRecord) bool {
	if len(withdrawn) == 0 {
		return false
	}
	var item models.PublicFeedObject
	if err := json.Unmarshal([]byte(record.RecordBytes), &item); err != nil {
		return false
	}
	return mirrorProjectionWithdrawn(withdrawn, item)
}

// WithdrawCampaignDataset hides one live campaign dataset from anonymous mirror
// reads without mutating the signed batch chain. Open streams of the source
// receive a fresh snapshot so browsers holding the dataset resynchronize.
func (m *PublicMirrorServer) WithdrawCampaignDataset(ctx context.Context, runID string) error {
	if m == nil || runID == "" {
		return fmt.Errorf("public mirror: withdraw campaign dataset: %w", constants.ErrMissingRequiredField)
	}
	datasetID := campaignMirrorDatasetID(runID)
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.RLock()
	sourceID := m.activeSourceIDLocked()
	m.mu.RUnlock()
	if sourceID == "" {
		return nil
	}
	next := m.currentSourceSummary(sourceID)
	if next.WithdrawnDatasetIDs[datasetID] {
		return nil
	}
	withdrawn := make(map[string]bool, len(next.WithdrawnDatasetIDs)+1)
	for id, isWithdrawn := range next.WithdrawnDatasetIDs {
		withdrawn[id] = isWithdrawn
	}
	withdrawn[datasetID] = true
	next.WithdrawnDatasetIDs = withdrawn
	if err := m.store.withdrawDataset(ctx, next, datasetID); err != nil {
		return fmt.Errorf("public mirror: withdraw campaign dataset %s: %w", runID, err)
	}
	m.mu.Lock()
	m.sources[sourceID] = &next
	m.mu.Unlock()
	m.requestSnapshotResync(sourceID)
	return nil
}

// CampaignDatasetWithdrawn reports whether one live campaign dataset is hidden
// from anonymous mirror reads.
func (m *PublicMirrorServer) CampaignDatasetWithdrawn(runID string) bool {
	if m == nil || runID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	summary := m.sources[m.activeSourceIDLocked()]
	if summary == nil {
		return false
	}
	return summary.WithdrawnDatasetIDs[campaignMirrorDatasetID(runID)]
}
