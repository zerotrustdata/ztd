// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// PublicRunBundleSource returns the accepted snapshot rows of one run's
// assignment result projections. The gateway's public mirror store implements it.
type PublicRunBundleSource interface {
	RunBundleRecords(ctx context.Context, sourceID, runID string) ([]models.PublicFeedRecord, error)
}

// SetRunBundleSource sets where run bundles read their accepted rows. Publishing
// a run bundle fails closed without one.
func (s *PublicPublisherService) SetRunBundleSource(source PublicRunBundleSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runBundleSource = source
}

// runBundle is one gzip JSONL bundle and the number of records it holds.
type runBundle struct {
	content     []byte
	recordCount int
}

// buildRunBundle encodes records as JSONL in sequence order and gzips it. Each
// line carries one record's bytes unchanged. Identical records always produce
// identical bytes: the gzip header has no name, comment, or modification time.
func buildRunBundle(records []models.PublicFeedRecord) (runBundle, error) {
	if len(records) == 0 {
		return runBundle{}, constants.ErrPublicRunBundleEmpty
	}
	ordered := slices.Clone(records)
	slices.SortStableFunc(ordered, func(a, b models.PublicFeedRecord) int {
		return cmp.Compare(a.Sequence, b.Sequence)
	})
	var lines bytes.Buffer
	for _, record := range ordered {
		sum := sha256.Sum256([]byte(record.RecordBytes))
		if hex.EncodeToString(sum[:]) != record.RecordHash {
			return runBundle{}, fmt.Errorf("public-feed: run bundle sequence %d: %w", record.Sequence, constants.ErrPublicFeedRecordHashMismatch)
		}
		line, err := json.Marshal(models.PublicRunBundleLine{
			Sequence:    record.Sequence,
			RecordHash:  record.RecordHash,
			RecordBytes: record.RecordBytes,
		})
		if err != nil {
			return runBundle{}, fmt.Errorf("public-feed: run bundle: encode line %d: %w", record.Sequence, err)
		}
		lines.Write(line)
		lines.WriteByte('\n')
	}
	var out bytes.Buffer
	writer, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return runBundle{}, fmt.Errorf("public-feed: run bundle: gzip: %w", err)
	}
	if _, err := writer.Write(lines.Bytes()); err != nil {
		return runBundle{}, fmt.Errorf("public-feed: run bundle: gzip write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return runBundle{}, fmt.Errorf("public-feed: run bundle: gzip close: %w", err)
	}
	return runBundle{content: out.Bytes(), recordCount: len(ordered)}, nil
}

// runBundleFilename names the run's assignment bundle artifact.
func runBundleFilename(runID string) string {
	return runID + constants.PublicRunBundleFilenameSuffix
}

// PublishRunBundle publishes one settled run's assignment result records as a
// proof catalog artifact. Its digest is the artifact ID, so an unchanged run
// reuses the active bundle and appends nothing. Changed records append the new
// bundle and withdraw only the previous one. It is control-plane publication
// and takes no governance envelope.
func (s *PublicPublisherService) PublishRunBundle(ctx context.Context, request models.PublicRunBundleRequest) (models.PublicRunBundleResponse, error) {
	switch {
	case request.RunID == "":
		return models.PublicRunBundleResponse{}, fmt.Errorf("public-feed: publish run bundle: run id: %w", constants.ErrMissingRequiredField)
	case request.CampaignID == "":
		return models.PublicRunBundleResponse{}, fmt.Errorf("public-feed: publish run bundle %s: campaign id: %w", request.RunID, constants.ErrMissingRequiredField)
	case request.CampaignRevision == "":
		return models.PublicRunBundleResponse{}, fmt.Errorf("public-feed: publish run bundle %s: campaign revision: %w", request.RunID, constants.ErrMissingRequiredField)
	}
	s.mu.Lock()
	source := s.runBundleSource
	s.mu.Unlock()
	if source == nil {
		return models.PublicRunBundleResponse{}, fmt.Errorf("public-feed: publish run bundle: record source: %w", constants.ErrMissingRequiredField)
	}
	records, err := source.RunBundleRecords(ctx, s.cfg.SourceID, request.RunID)
	if err != nil {
		return models.PublicRunBundleResponse{}, fmt.Errorf("public-feed: publish run bundle: read records: %w", err)
	}
	bundle, err := buildRunBundle(records)
	if err != nil {
		return models.PublicRunBundleResponse{}, err
	}
	digest := hashBytes(bundle.content)
	if err := s.commitRunBundle(ctx, request, runBundleFilename(request.RunID), bundle.content); err != nil {
		return models.PublicRunBundleResponse{}, err
	}
	// The mirror serves /proofs/<sha256> only after the artifact's segment is delivered.
	if err := s.PushProofPackage(ctx); err != nil {
		return models.PublicRunBundleResponse{}, err
	}
	return models.PublicRunBundleResponse{
		Accepted:     true,
		RunID:        request.RunID,
		BundleSHA256: digest,
		RecordCount:  bundle.recordCount,
		ByteSize:     int64(len(bundle.content)),
	}, nil
}

// commitRunBundle makes content the run's only active bundle under filename.
// It holds the proof log lock for the whole decision. The request supplies the
// campaign identity that labels the bundle's entry and proof segment.
func (s *PublicPublisherService) commitRunBundle(ctx context.Context, request models.PublicRunBundleRequest, filename string, content []byte) error {
	digest := hashBytes(content)
	s.proofMu.Lock()
	defer s.proofMu.Unlock()
	held, err := s.lockProofLog(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = held.Close() }()
	if err := s.syncProofIndexLocked(ctx); err != nil {
		return err
	}
	if len(s.proofIndex.segments) == 0 {
		legacy, err := s.loadLegacyProofCatalog(ctx)
		if err != nil {
			return err
		}
		if len(legacy.Entries) > 0 {
			if err := s.cutOverLegacyProofLogLocked(ctx); err != nil {
				return err
			}
		}
	}
	index := s.proofIndex
	var previous []string
	current := false
	for _, id := range index.activeRunArtifactIDs(request.RunID) {
		if index.active[id].Filename != filename {
			continue
		}
		if id == digest {
			current = true
			continue
		}
		previous = append(previous, id)
	}
	if current {
		// Unchanged bytes append nothing. A previous attempt that appended this
		// bundle and failed before withdrawing the old one is finished here.
		for _, id := range previous {
			if err := s.withdrawProofArtifactLocked(ctx, id); err != nil {
				return err
			}
		}
		return nil
	}
	entries, err := s.writeProofArtifacts(ctx, request.CampaignID, []ProofArtifactInput{{
		Filename:    filename,
		MediaType:   constants.MediaTypeGzip,
		Content:     content,
		CampaignID:  request.CampaignID,
		SourceRunID: request.RunID,
	}})
	if err != nil {
		return err
	}
	if _, err := s.appendPublicationLocked(ctx, request.CampaignID, request.CampaignRevision, digest, entries); err != nil {
		return err
	}
	for _, id := range previous {
		if err := s.withdrawProofArtifactLocked(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
