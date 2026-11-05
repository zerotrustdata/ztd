// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

const (
	publicBucketTickInterval    = 10 * time.Second
	publicBucketPointerInterval = time.Minute
	publicBucketLiveRunInterval = time.Minute
	publicBucketSweepInterval   = time.Hour
	// publicBucketSweepGrace outlives the pointer's cache, so a cached pointer
	// never names an object the sweep removed.
	publicBucketSweepGrace   = time.Hour
	publicBucketUploadWorker = 8
	publicBucketPurgeBatch   = 30
)

// publicBucketPut describes one object upload. The payload digest is sent with
// the request, so the bucket rejects bytes that do not match it.
type publicBucketPut struct {
	Key             string
	Body            io.Reader
	Size            int64
	SHA256          string
	ContentType     string
	ContentEncoding string
	CacheControl    string
}

// publicBucketObject is one entry of a bucket listing.
type publicBucketObject struct {
	Key          string
	LastModified time.Time
}

// publicBucketClient is the S3-compatible surface the publisher writes through.
type publicBucketClient interface {
	Put(ctx context.Context, put publicBucketPut) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]publicBucketObject, error)
}

// publicCachePurger removes public URLs from the CDN cache.
type publicCachePurger interface {
	Purge(ctx context.Context, urls []string) error
}

// publicPointerSigner signs a pointer with the source's current key.
type publicPointerSigner func(models.PublicFeedPointer) (models.PublicFeedPointer, error)

// PublicBucketPublisher pushes the colocated mirror's public layout to a bucket.
// Digest-addressed objects go first and pointer.json last, so a partial upload
// never goes live. One goroutine owns all of its state: Run calls tick in a loop.
type PublicBucketPublisher struct {
	cfg      models.PublicBucketConfig
	sourceID string
	source   PublicLayoutSource
	sign     publicPointerSigner
	client   publicBucketClient
	purger   publicCachePurger
	journal  *publicBucketJournal
	logger   *slog.Logger
	now      func() time.Time

	synced      bool
	lastPointer models.PublicFeedPointer
	lastSync    time.Time
	lastSweep   time.Time
	// present holds proof and proof-meta keys known to be in the bucket.
	present map[string]bool
	// active is the artifact set of the last pointer that reached the bucket.
	active       map[string]bool
	proofsRoot   string
	catalogRoot  string
	catalogSHA   string
	segmentsHead int64
	historyHigh  int64

	// withdrawalSweep is set while objects a withdrawal left behind are still owed
	// removal: read models and catalogs that may name the withdrawn data.
	withdrawalSweep bool

	statusKey     string
	statusActive  bool
	liveRunSHA    map[string]string
	lastLiveRuns  time.Time
	liveRunsKnown bool
}

// NewPublicBucketPublisher returns a publisher writing through client and purger.
func newPublicBucketPublisher(ctx context.Context, cfg models.PublicBucketConfig, sourceID string, source PublicLayoutSource, sign publicPointerSigner, client publicBucketClient, purger publicCachePurger, fileSvc fs.RuntimeFileService, logger *slog.Logger) (*PublicBucketPublisher, error) {
	if source == nil || sign == nil || client == nil || purger == nil || sourceID == "" {
		return nil, fmt.Errorf("public-feed: bucket publisher: %w", constants.ErrMissingRequiredField)
	}
	journal, err := loadPublicBucketJournal(ctx, fileSvc)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PublicBucketPublisher{
		cfg:        cfg,
		sourceID:   sourceID,
		source:     source,
		sign:       sign,
		client:     client,
		purger:     purger,
		journal:    journal,
		logger:     logger,
		now:        func() time.Time { return time.Now().UTC() },
		present:    map[string]bool{},
		active:     map[string]bool{},
		liveRunSHA: map[string]string{},
	}, nil
}

// Run publishes until ctx is cancelled. A failed tick is logged and retried on
// the next one, because nothing is marked done until it reaches the bucket.
func (p *PublicBucketPublisher) Run(ctx context.Context) {
	ticker := time.NewTicker(publicBucketTickInterval)
	defer ticker.Stop()
	for {
		if err := p.tick(ctx); err != nil && ctx.Err() == nil {
			p.logger.Warn("Public bucket publish incomplete", "error", err, "pending_deletes", len(p.journal.deletes), "pending_purges", len(p.journal.purges))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// tick publishes what changed since the last one. Live objects update on their
// own cadence and a failure there does not hold back the pointer.
func (p *PublicBucketPublisher) tick(ctx context.Context) error {
	state, err := p.source.LayoutState(ctx, p.sourceID)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: read layout: %w", err)
	}
	var errs []error
	settled := p.synced && p.lastPointer.Active && !state.Pointer.Active
	// Across a restart the journal holds the digest of the last pointer written, so
	// a withdrawal that landed while the publisher was down is still handled as one.
	lastWithdrawal := p.lastPointer.WithdrawalDigest
	if !p.synced {
		lastWithdrawal = p.journal.withdrawal
	}
	withdrew := (p.synced || p.journal.revision != 0) && lastWithdrawal != state.Pointer.WithdrawalDigest
	force := !p.synced || settled || withdrew
	if err := p.publishLive(ctx, state, withdrew); err != nil {
		errs = append(errs, err)
	}
	changed := state.Pointer.ReadModelSHA256 != p.lastPointer.ReadModelSHA256 || state.Pointer.ProofRootSHA256 != p.lastPointer.ProofRootSHA256
	if force || (changed && p.now().Sub(p.lastSync) >= publicBucketPointerInterval) {
		if err := p.syncLayout(ctx, state, force, withdrew); err != nil {
			errs = append(errs, err)
		}
	}
	if p.withdrawalSweep {
		// A withdrawal leaves no grace: earlier read models and catalogs can name
		// what was withdrawn, and the pointer naming them was purged with it.
		if err := p.sweep(ctx, p.now().Add(time.Second), true); err != nil {
			errs = append(errs, err)
		} else {
			p.withdrawalSweep = false
		}
	} else if p.synced && p.now().Sub(p.lastSweep) >= publicBucketSweepInterval {
		if err := p.sweep(ctx, p.now().Add(-publicBucketSweepGrace), false); err != nil {
			errs = append(errs, err)
		}
	}
	if err := p.drain(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// syncLayout uploads every object the next pointer names and then writes it. The
// in-memory state only advances once the pointer reached the bucket.
func (p *PublicBucketPublisher) syncLayout(ctx context.Context, state PublicLayoutState, force, withdrew bool) error {
	first := !p.synced
	if first {
		if err := p.loadPresent(ctx); err != nil {
			return err
		}
	}
	pointer := state.Pointer
	root := pointer.ProofRootSHA256

	nextActive := p.active
	var withdrawn []string
	if root != "" && (first || root != p.proofsRoot) {
		catalog, err := p.source.LayoutProofCatalog(ctx, p.sourceID)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: read catalog: %w", err)
		}
		nextActive = make(map[string]bool, len(catalog.Entries))
		for _, entry := range catalog.Entries {
			nextActive[entry.ArtifactID] = true
		}
		if err := p.uploadProofs(ctx, catalog.Entries); err != nil {
			return err
		}
		withdrawn = p.withdrawnArtifacts(nextActive, first)
		if err := p.uploadSegments(ctx, pointer.ProofSegmentHead); err != nil {
			return err
		}
		head, ok, err := p.source.LayoutProofHead(ctx, p.sourceID)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: read proof head: %w", err)
		}
		if ok {
			if err := p.putBytes(ctx, constants.PublicLayoutProofHeadKey, head, constants.HeaderValueApplicationJSON, "", constants.PublicLayoutCacheProofHead); err != nil {
				return err
			}
		}
	}
	// During a run the catalog and history stay as they are, since rewriting them
	// every minute is costly and proof-meta objects serve lookups between rewrites.
	// They catch up once no run is active, or at once if an artifact was withdrawn.
	settling := force || !pointer.Active || len(withdrawn) > 0
	if root != "" && settling && p.catalogRoot != root {
		catalog, err := p.source.LayoutProofCatalog(ctx, p.sourceID)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: read catalog: %w", err)
		}
		key := constants.PublicLayoutProofCatalogPrefix + catalog.SHA256 + constants.PublicLayoutJSONGzipSuffix
		if err := p.putBytes(ctx, key, catalog.Gzip, constants.HeaderValueApplicationJSON, constants.HeaderValueGzip, constants.PublicLayoutCacheImmutable); err != nil {
			return err
		}
		p.catalogRoot, p.catalogSHA = root, catalog.SHA256
	}
	historyPages, err := p.publishHistory(ctx, pointer, settling, withdrew)
	if err != nil {
		return err
	}
	readModelKey := constants.PublicLayoutReadModelPrefix + pointer.ReadModelSHA256 + constants.PublicLayoutJSONGzipSuffix
	if err := p.putBytes(ctx, readModelKey, state.ReadModelGzip, constants.HeaderValueApplicationJSON, constants.HeaderValueGzip, constants.PublicLayoutCacheImmutable); err != nil {
		return err
	}

	pointer.ProofCatalogSHA256 = p.catalogSHA
	pointer.Revision = nextPublicFeedPointerRevision(p.journal.revision, p.now())
	signed, err := p.sign(pointer)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: sign pointer: %w", err)
	}
	encoded, err := json.Marshal(signed)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: encode pointer: %w", err)
	}
	if err := p.putBytes(ctx, constants.PublicLayoutPointerKey, encoded, constants.HeaderValueApplicationJSON, "", constants.PublicLayoutCachePointer); err != nil {
		return err
	}

	// Nothing names the withdrawn objects from here on, so they can go.
	entries := []models.PublicBucketJournalEntry{{Kind: models.PublicBucketJournalPointer, Revision: signed.Revision, Sequence: signed.HighWaterSequence, WithdrawalDigest: signed.WithdrawalDigest}}
	for _, artifactID := range withdrawn {
		entries = append(entries, p.deleteEntries(constants.PublicLayoutProofPrefix+artifactID)...)
		entries = append(entries, p.deleteEntries(constants.PublicLayoutProofMetaPrefix+artifactID+constants.PublicLayoutJSONSuffix)...)
	}
	for _, key := range historyPages.deleted {
		entries = append(entries, p.deleteEntries(key)...)
	}
	if withdrew {
		entries = append(entries, p.purgeEntry(constants.PublicLayoutPointerKey), p.purgeEntry(constants.PublicLayoutProofHeadKey))
		for _, key := range historyPages.written {
			entries = append(entries, p.purgeEntry(key))
		}
	}
	if err := p.journal.record(ctx, entries...); err != nil {
		return err
	}
	for _, artifactID := range withdrawn {
		delete(p.present, constants.PublicLayoutProofPrefix+artifactID)
		delete(p.present, constants.PublicLayoutProofMetaPrefix+artifactID+constants.PublicLayoutJSONSuffix)
	}
	p.active = nextActive
	if root != "" {
		p.proofsRoot = root
	}
	p.segmentsHead = max(p.segmentsHead, pointer.ProofSegmentHead)
	if historyPages.high > 0 {
		p.historyHigh = historyPages.high
	}
	p.withdrawalSweep = p.withdrawalSweep || withdrew
	p.lastPointer = signed
	p.lastSync = p.now()
	p.synced = true
	return nil
}

// loadPresent lists uploaded proofs, so an interrupted first sync resumes where it stopped.
func (p *PublicBucketPublisher) loadPresent(ctx context.Context) error {
	for _, prefix := range []string{constants.PublicLayoutProofPrefix, constants.PublicLayoutProofMetaPrefix} {
		objects, err := p.client.List(ctx, prefix)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: list %s: %w", prefix, err)
		}
		for _, object := range objects {
			p.present[object.Key] = true
		}
	}
	return nil
}

// withdrawnArtifacts returns artifacts in the bucket that left the active set. On
// a first sync the listing stands in for the previous set, which also catches a
// withdrawal that landed while the publisher was down.
func (p *PublicBucketPublisher) withdrawnArtifacts(next map[string]bool, first bool) []string {
	var withdrawn []string
	if first {
		for key := range p.present {
			artifactID, ok := strings.CutPrefix(key, constants.PublicLayoutProofPrefix)
			if ok && !next[artifactID] {
				withdrawn = append(withdrawn, artifactID)
			}
		}
	} else {
		for artifactID := range p.active {
			if !next[artifactID] {
				withdrawn = append(withdrawn, artifactID)
			}
		}
	}
	slices.Sort(withdrawn)
	return withdrawn
}

// uploadProofs uploads missing artifacts and their catalog entries with a bounded
// worker pool. An interrupted upload resumes from what present records.
func (p *PublicBucketPublisher) uploadProofs(ctx context.Context, entries []models.PublicProofCatalogEntry) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan models.PublicProofCatalogEntry)
	var mu sync.Mutex
	var firstErr error
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	var workers sync.WaitGroup
	for range publicBucketUploadWorker {
		workers.Go(func() {
			for entry := range work {
				if err := p.uploadProof(workCtx, entry, &mu); err != nil {
					fail(err)
				}
			}
		})
	}
feed:
	for _, entry := range entries {
		mu.Lock()
		missing := !p.present[constants.PublicLayoutProofPrefix+entry.ArtifactID] || !p.present[constants.PublicLayoutProofMetaPrefix+entry.ArtifactID+constants.PublicLayoutJSONSuffix]
		mu.Unlock()
		if !missing {
			continue
		}
		select {
		case work <- entry:
		case <-workCtx.Done():
			break feed
		}
	}
	close(work)
	workers.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// uploadProof uploads the artifact and then its catalog entry. The entry going
// second means a proof-meta hit always has its artifact.
func (p *PublicBucketPublisher) uploadProof(ctx context.Context, entry models.PublicProofCatalogEntry, mu *sync.Mutex) error {
	proofKey := constants.PublicLayoutProofPrefix + entry.ArtifactID
	mu.Lock()
	proofPresent := p.present[proofKey]
	mu.Unlock()
	if !proofPresent {
		file, err := p.source.OpenLayoutProof(ctx, entry.ArtifactID)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: open proof %s: %w", entry.ArtifactID, err)
		}
		defer func() { _ = file.Close() }()
		// The artifact ID is the content digest, so the bucket verifies the bytes.
		if err := p.client.Put(ctx, publicBucketPut{
			Key:          proofKey,
			Body:         file,
			Size:         entry.ByteSize,
			SHA256:       entry.ArtifactID,
			ContentType:  entry.MediaType,
			CacheControl: constants.PublicLayoutCacheImmutable,
		}); err != nil {
			return err
		}
		mu.Lock()
		p.present[proofKey] = true
		mu.Unlock()
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: encode proof entry %s: %w", entry.ArtifactID, err)
	}
	metaKey := constants.PublicLayoutProofMetaPrefix + entry.ArtifactID + constants.PublicLayoutJSONSuffix
	if err := p.putBytes(ctx, metaKey, encoded, constants.HeaderValueApplicationJSON, "", constants.PublicLayoutCacheImmutable); err != nil {
		return err
	}
	mu.Lock()
	p.present[metaKey] = true
	mu.Unlock()
	return nil
}

// uploadSegments writes each chunk from the one holding the first segment not yet
// uploaded through the one holding head. Only the last chunk can still change.
func (p *PublicBucketPublisher) uploadSegments(ctx context.Context, head int64) error {
	if head <= 0 {
		return nil
	}
	size := int64(constants.PublicLayoutSegmentChunkSize)
	start := ((p.segmentsHead+1)-1)/size*size + 1
	for first := start; first <= head; first += size {
		chunk, ok, err := p.source.LayoutSegmentChunk(ctx, p.sourceID, first)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: read segment chunk %d: %w", first, err)
		}
		if !ok {
			continue
		}
		cache := constants.PublicLayoutCacheTail
		if first+size-1 <= head {
			cache = constants.PublicLayoutCacheImmutable
		}
		key := constants.PublicLayoutProofSegmentsPrefix + strconv.FormatInt(first, 10) + constants.PublicLayoutJSONLGzipSuffix
		if err := p.putBytes(ctx, key, chunk, constants.HeaderValueApplicationNDJSON, constants.HeaderValueGzip, cache); err != nil {
			return err
		}
	}
	return nil
}

type publicBucketHistoryResult struct {
	written []string
	deleted []string
	high    int64
}

// publishHistory rewrites retained history pages. Pages change only when a run
// settles or a withdrawal lands, so the signed chain is not rewritten every
// minute during a run. A withdrawal changes what every page filters, so it
// rewrites all of them.
func (p *PublicBucketPublisher) publishHistory(ctx context.Context, pointer models.PublicFeedPointer, settling, withdrew bool) (publicBucketHistoryResult, error) {
	var result publicBucketHistoryResult
	if !settling || pointer.HighWaterSequence <= 0 {
		return result, nil
	}
	span := int64(constants.PublicLayoutHistoryPageRecords)
	from := max(pointer.HistoryFirstSequence, 1)
	if !withdrew && p.historyHigh > 0 {
		from = max(from, p.historyHigh)
	}
	start := (from-1)/span*span + 1
	for first := start; first <= pointer.HighWaterSequence; first += span {
		key := constants.PublicLayoutHistoryPrefix + strconv.FormatInt(first, 10) + constants.PublicLayoutJSONGzipSuffix
		page, ok, err := p.source.LayoutHistoryPage(ctx, p.sourceID, first)
		if err != nil {
			return result, fmt.Errorf("public-feed: bucket: read history page %d: %w", first, err)
		}
		if !ok {
			// Every record of the page was withdrawn, so the page has to go too.
			if withdrew {
				result.deleted = append(result.deleted, key)
			}
			continue
		}
		if err := p.putBytes(ctx, key, page, constants.HeaderValueApplicationJSON, constants.HeaderValueGzip, constants.PublicLayoutCacheHistory); err != nil {
			return result, err
		}
		result.written = append(result.written, key)
	}
	result.high = pointer.HighWaterSequence
	return result, nil
}

// publishLive writes live/status.json when it changed and refreshes each active
// run's mutable bundle at most once a minute. A settled run's bundle is deleted. A
// withdrawal rewrites the status too, since its tail may name what was withdrawn.
func (p *PublicBucketPublisher) publishLive(ctx context.Context, state PublicLayoutState, withdrew bool) error {
	active := len(state.ActiveRuns) > 0
	if !active && !p.statusActive && !withdrew && len(p.liveRunSHA) == 0 && p.liveRunsKnown {
		return nil
	}
	var errs []error
	if active || p.statusActive || withdrew {
		if err := p.publishStatus(ctx, active); err != nil {
			errs = append(errs, err)
		}
	}
	if err := p.publishLiveRuns(ctx, state); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (p *PublicBucketPublisher) publishStatus(ctx context.Context, active bool) error {
	status, err := p.source.LayoutLiveStatus(ctx, p.sourceID)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: read live status: %w", err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: encode live status: %w", err)
	}
	// generated_at moves on every read, so it does not count as a change.
	unstamped := status
	unstamped.Snapshot.GeneratedAt = time.Time{}
	stable, err := json.Marshal(unstamped)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: encode live status: %w", err)
	}
	key := hashBytes(stable)
	if key == p.statusKey && active == p.statusActive {
		return nil
	}
	if err := p.putBytes(ctx, constants.PublicLayoutLiveStatusKey, encoded, constants.HeaderValueApplicationJSON, "", constants.PublicLayoutCacheLive); err != nil {
		return err
	}
	p.statusKey, p.statusActive = key, active
	return nil
}

func (p *PublicBucketPublisher) publishLiveRuns(ctx context.Context, state PublicLayoutState) error {
	if !p.liveRunsKnown {
		objects, err := p.client.List(ctx, constants.PublicLayoutLiveRunsPrefix)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: list live runs: %w", err)
		}
		for _, object := range objects {
			runID, ok := strings.CutSuffix(strings.TrimPrefix(object.Key, constants.PublicLayoutLiveRunsPrefix), constants.PublicLayoutJSONLGzipSuffix)
			if ok {
				p.liveRunSHA[runID] = ""
			}
		}
		p.liveRunsKnown = true
	}
	current := make(map[string]bool, len(state.ActiveRuns))
	for _, run := range state.ActiveRuns {
		current[run.RunID] = true
	}
	var entries []models.PublicBucketJournalEntry
	for runID := range p.liveRunSHA {
		if !current[runID] {
			entries = append(entries, models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: liveRunKey(runID)})
			delete(p.liveRunSHA, runID)
		}
	}
	if err := p.journal.record(ctx, entries...); err != nil {
		return err
	}
	if len(current) == 0 || p.now().Sub(p.lastLiveRuns) < publicBucketLiveRunInterval {
		return nil
	}
	var errs []error
	for _, run := range state.ActiveRuns {
		bundle, ok, err := p.source.LayoutLiveRunBundle(ctx, p.sourceID, run.RunID)
		if err != nil {
			errs = append(errs, fmt.Errorf("public-feed: bucket: read live run %s: %w", run.RunID, err))
			continue
		}
		if !ok {
			continue
		}
		digest := hashBytes(bundle)
		if p.liveRunSHA[run.RunID] == digest {
			continue
		}
		if err := p.putBytes(ctx, liveRunKey(run.RunID), bundle, constants.MediaTypeGzip, "", constants.PublicLayoutCacheLiveRun); err != nil {
			errs = append(errs, err)
			continue
		}
		p.liveRunSHA[run.RunID] = digest
	}
	if len(errs) == 0 {
		p.lastLiveRuns = p.now()
	}
	return errors.Join(errs...)
}

func liveRunKey(runID string) string {
	return constants.PublicLayoutLiveRunsPrefix + runID + constants.PublicLayoutJSONLGzipSuffix
}

// deleteEntries journals one object's delete and its cache purge.
func (p *PublicBucketPublisher) deleteEntries(key string) []models.PublicBucketJournalEntry {
	return []models.PublicBucketJournalEntry{
		{Kind: models.PublicBucketJournalDelete, Key: key},
		p.purgeEntry(key),
	}
}

func (p *PublicBucketPublisher) purgeEntry(key string) models.PublicBucketJournalEntry {
	return models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPurge, Key: strings.TrimRight(p.cfg.PublicOrigin, "/") + "/" + key}
}

// drain retries every delete and purge still owed. A failure stays in the journal.
func (p *PublicBucketPublisher) drain(ctx context.Context) error {
	var errs []error
	for _, key := range slices.Clone(p.journal.deletes) {
		if err := p.client.Delete(ctx, key); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := p.journal.record(ctx, models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDeleted, Key: key}); err != nil {
			errs = append(errs, err)
		}
	}
	for batch := range slices.Chunk(slices.Clone(p.journal.purges), publicBucketPurgeBatch) {
		if err := p.purger.Purge(ctx, batch); err != nil {
			errs = append(errs, err)
			continue
		}
		entries := make([]models.PublicBucketJournalEntry, 0, len(batch))
		for _, url := range batch {
			entries = append(entries, models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPurged, Key: url})
		}
		if err := p.journal.record(ctx, entries...); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sweep queues read models and catalogs the pointer stopped naming and history
// pages below the retained window, when they are older than cutoff. With purge
// their cached copies are purged too.
func (p *PublicBucketPublisher) sweep(ctx context.Context, cutoff time.Time, purge bool) error {
	keep := map[string]bool{
		constants.PublicLayoutReadModelPrefix + p.lastPointer.ReadModelSHA256 + constants.PublicLayoutJSONGzipSuffix: true,
	}
	if p.lastPointer.ProofCatalogSHA256 != "" {
		keep[constants.PublicLayoutProofCatalogPrefix+p.lastPointer.ProofCatalogSHA256+constants.PublicLayoutJSONGzipSuffix] = true
	}
	var entries []models.PublicBucketJournalEntry
	queue := func(key string) {
		if purge {
			entries = append(entries, p.deleteEntries(key)...)
			return
		}
		entries = append(entries, models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: key})
	}
	for _, prefix := range []string{constants.PublicLayoutReadModelPrefix, constants.PublicLayoutProofCatalogPrefix} {
		objects, err := p.client.List(ctx, prefix)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: list %s: %w", prefix, err)
		}
		for _, object := range objects {
			if !keep[object.Key] && object.LastModified.Before(cutoff) {
				queue(object.Key)
			}
		}
	}
	objects, err := p.client.List(ctx, constants.PublicLayoutHistoryPrefix)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: list %s: %w", constants.PublicLayoutHistoryPrefix, err)
	}
	span := int64(constants.PublicLayoutHistoryPageRecords)
	for _, object := range objects {
		name, ok := strings.CutSuffix(strings.TrimPrefix(object.Key, constants.PublicLayoutHistoryPrefix), constants.PublicLayoutJSONGzipSuffix)
		first, parseErr := strconv.ParseInt(name, 10, 64)
		if ok && parseErr == nil && first+span <= p.lastPointer.HistoryFirstSequence && object.LastModified.Before(cutoff) {
			queue(object.Key)
		}
	}
	if err := p.journal.record(ctx, entries...); err != nil {
		return err
	}
	p.lastSweep = p.now()
	return nil
}

func (p *PublicBucketPublisher) putBytes(ctx context.Context, key string, body []byte, contentType, contentEncoding, cacheControl string) error {
	digest := sha256.Sum256(body)
	return p.client.Put(ctx, publicBucketPut{
		Key:             key,
		Body:            bytes.NewReader(body),
		Size:            int64(len(body)),
		SHA256:          hex.EncodeToString(digest[:]),
		ContentType:     contentType,
		ContentEncoding: contentEncoding,
		CacheControl:    cacheControl,
	})
}

// gzipLayoutBytes gzips one layout object. The header has no name, comment, or
// modification time, so the same bytes always produce the same object.
func gzipLayoutBytes(body []byte) ([]byte, error) {
	var out bytes.Buffer
	writer, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("public-feed: layout gzip: %w", err)
	}
	if _, err := writer.Write(body); err != nil {
		return nil, fmt.Errorf("public-feed: layout gzip write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("public-feed: layout gzip close: %w", err)
	}
	return out.Bytes(), nil
}
