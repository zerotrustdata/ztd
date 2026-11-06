// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// PublicLayoutState is the mirror's current public layout head: everything a
// pointer names, with the read model those fields describe.
type PublicLayoutState struct {
	// Pointer is unsigned with Revision zero.
	Pointer models.PublicFeedPointer
	// ProofRootSHA256 is internal artifact synchronization state, never public.
	ProofRootSHA256 string
	// ReadModelGzip is the gzipped read model. Pointer.ReadModelSHA256 is the
	// SHA-256 of its uncompressed bytes.
	ReadModelGzip []byte
	// ActiveRuns are the runs still executing.
	ActiveRuns []models.PublicLiveRun
}

// PublicLayoutHead is the mirror's current head, read without touching the full
// read model, so the publisher can check it every tick.
type PublicLayoutHead struct {
	HighWaterSequence int64
	FeedChainHash     string
	WithdrawalDigest  string
	ProofRootSHA256   string
	Freshness         models.CampaignFreshness
	// ActiveRuns are the runs still executing. Never nil.
	ActiveRuns []models.PublicLiveRun
}

// PublicLayoutSource is what the bucket publisher reads from the colocated
// mirror. The mirror's own layout routes serve the same bytes, so the bucket and
// the loopback listener share one read path.
type PublicLayoutSource interface {
	// LayoutHead is cheap enough to call on every tick.
	LayoutHead(ctx context.Context, sourceID string) (PublicLayoutHead, error)
	// LayoutState rebuilds the read model when the head moved, which scans every
	// current record. Call it only when a pointer write is due.
	LayoutState(ctx context.Context, sourceID string) (PublicLayoutState, error)
	// LayoutDatasetsSince returns the distinct non-empty dataset IDs of current
	// records accepted after sequence, so a held pointer moves for records of a run
	// that is not executing.
	LayoutDatasetsSince(ctx context.Context, sourceID string, after int64) ([]string, error)
	// LayoutArtifacts returns active UI artifacts from the internal proof log.
	// It does not encode or expose a catalog projection.
	LayoutArtifacts(ctx context.Context, sourceID string) ([]models.PublicProofCatalogEntry, error)
	// LayoutHistoryPage returns one gzipped page of retained records in
	// [first, first+constants.PublicLayoutHistoryPageRecords) under /history's
	// withdrawal filter, and false when the span holds none.
	LayoutHistoryPage(ctx context.Context, sourceID string, first int64) ([]byte, bool, error)
	LayoutLiveStatus(ctx context.Context, sourceID string) (models.PublicLiveStatus, error)
	// LayoutLiveRunBundle returns the current run bundle bytes for a run, and false
	// when it has no result records yet.
	LayoutLiveRunBundle(ctx context.Context, sourceID, runID string) ([]byte, bool, error)
	// OpenLayoutProof opens one stored proof artifact for upload.
	OpenLayoutProof(ctx context.Context, artifactID string) (*os.File, error)
}

// nextPublicFeedPointerRevision returns a revision above last that also stays
// above any revision written before a restart that lost last.
func nextPublicFeedPointerRevision(last int64, now time.Time) int64 {
	return max(last+1, now.UnixMilli())
}

// publicLayoutArtifactKey selects the UI artifacts from the internal proof log.
// Historical traces and audit databases remain stored, but have no layout key.
func publicLayoutArtifactKey(entry models.PublicProofCatalogEntry) (string, bool) {
	if !validPublicProofArtifactID(entry.ArtifactID) {
		return "", false
	}
	if strings.HasSuffix(entry.Filename, constants.PublicRunBundleFilenameSuffix) {
		return constants.PublicLayoutRunsPrefix + entry.ArtifactID + constants.PublicLayoutJSONLGzipSuffix, true
	}
	if strings.HasPrefix(entry.Filename, "scenario-") && (strings.HasSuffix(entry.Filename, ".input.json") || strings.HasSuffix(entry.Filename, ".gold.json")) {
		return constants.PublicLayoutFixturesPrefix + entry.ArtifactID, true
	}
	return "", false
}

// publicLayoutArtifactID extracts the digest from a selected artifact's layout key.
func publicLayoutArtifactID(key string) string {
	if digest, ok := strings.CutPrefix(key, constants.PublicLayoutRunsPrefix); ok {
		return strings.TrimSuffix(digest, constants.PublicLayoutJSONLGzipSuffix)
	}
	return strings.TrimPrefix(key, constants.PublicLayoutFixturesPrefix)
}
