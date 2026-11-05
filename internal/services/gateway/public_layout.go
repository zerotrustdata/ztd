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
	"time"

	"github.com/zerotrustdata/ztd/internal/models"
)

// PublicLayoutState is the mirror's current public layout head: everything a
// pointer names, with the read model those fields describe.
type PublicLayoutState struct {
	// Pointer is unsigned with Revision zero. Its ProofCatalogSHA256 is empty
	// until the caller fills it from LayoutProofCatalog.
	Pointer models.PublicFeedPointer
	// ReadModelGzip is the gzipped read model. Pointer.ReadModelSHA256 is the
	// SHA-256 of its uncompressed bytes.
	ReadModelGzip []byte
	// ActiveRuns are the runs whose latest evaluation summary is queued or running.
	ActiveRuns []models.PublicLiveRun
}

// PublicLayoutCatalog is the proof catalog as one layout object.
type PublicLayoutCatalog struct {
	// SHA256 covers the uncompressed catalog JSON.
	SHA256 string
	Gzip   []byte
	// Entries are the active artifacts the catalog lists.
	Entries []models.PublicProofCatalogEntry
}

// PublicLayoutSource is what the bucket publisher reads from the colocated
// mirror. The mirror's own layout routes serve the same bytes, so the bucket and
// the loopback listener share one read path.
type PublicLayoutSource interface {
	LayoutState(ctx context.Context, sourceID string) (PublicLayoutState, error)
	// LayoutProofCatalog returns the catalog at the current proof root. Bytes are
	// stable for one proof root.
	LayoutProofCatalog(ctx context.Context, sourceID string) (PublicLayoutCatalog, error)
	// LayoutProofHead returns proof-head.json and false when no head exists.
	LayoutProofHead(ctx context.Context, sourceID string) ([]byte, bool, error)
	// LayoutSegmentChunk returns the gzipped JSONL of the segments in
	// [first, first+constants.PublicLayoutSegmentChunkSize) with signing_public_key
	// filled in, and false when the range holds none.
	LayoutSegmentChunk(ctx context.Context, sourceID string, first int64) ([]byte, bool, error)
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
