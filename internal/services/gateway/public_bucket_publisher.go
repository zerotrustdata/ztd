// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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

	synced        bool
	lastPointer   models.PublicFeedPointer
	lastFreshness models.CampaignFreshness
	lastSync      time.Time
	lastSweep     time.Time
	// lastActiveRuns is the active run set the last pointer was written with.
	lastActiveRuns map[publicLiveRunKey]bool
	// heldThrough is the highest sequence checked for records outside the active
	// runs while the pointer held.
	heldThrough int64
	// activeRoot is the proof root whose catalog active reflects. Uploads while the
	// pointer holds advance it ahead of proofsRoot.
	activeRoot     string
	lastHeldProofs time.Time
	// present holds UI artifact keys known to be in the bucket.
	present map[string]bool
	// active holds layout keys, including artifacts uploaded during a pointer hold.
	active      map[string]bool
	proofsRoot  string
	historyHigh int64

	// withdrawalSweep is set while objects a withdrawal left behind are still owed
	// removal: read models that may name the withdrawn data.
	withdrawalSweep bool

	statusKey     string
	statusActive  bool
	liveRunSHA    map[string]string
	lastLiveRuns  time.Time
	liveRunsKnown bool
}

// newPublicBucketPublisher returns a publisher writing through client and purger.
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

// publicLiveRunKey identifies one active run independent of its lifecycle state.
type publicLiveRunKey struct{ dataset, run string }

// liveRunSet returns the identities of runs.
func liveRunSet(runs []models.PublicLiveRun) map[publicLiveRunKey]bool {
	set := make(map[publicLiveRunKey]bool, len(runs))
	for _, run := range runs {
		set[publicLiveRunKey{dataset: run.DatasetID, run: run.RunID}] = true
	}
	return set
}

// tick publishes what changed since the last one. Live objects update on their
// own cadence and a failure there does not hold back the pointer.
//
// While runs are active the pointer, and with it the read model browsers load,
// holds still: live/status.json carries the run. The pointer moves at once when
// the active run set changes or a withdrawal lands, and at most once a minute for
// records of runs that are not executing, such as a republished verification.
// With no run active it moves at most once a minute for any change.
func (p *PublicBucketPublisher) tick(ctx context.Context) error {
	head, err := p.source.LayoutHead(ctx, p.sourceID)
	if err != nil {
		return fmt.Errorf("public-feed: bucket: read layout head: %w", err)
	}
	var errs []error
	// Across a restart the journal holds the digest of the last pointer written, so
	// a withdrawal that landed while the publisher was down is still handled as one.
	lastWithdrawal := p.lastPointer.WithdrawalDigest
	if !p.synced {
		lastWithdrawal = p.journal.withdrawal
	}
	withdrew := (p.synced || p.journal.revision != 0) && lastWithdrawal != head.WithdrawalDigest
	// A run starting is written at once, because browsers poll the live status only
	// after the pointer says a run is active, and a run ending is written at once so
	// its settled results reach the read model.
	runSetChanged := p.synced && !maps.Equal(liveRunSet(head.ActiveRuns), p.lastActiveRuns)
	force := !p.synced || runSetChanged || withdrew
	if err := p.publishLive(ctx, head.ActiveRuns, withdrew); err != nil {
		errs = append(errs, err)
	}
	changed := head.HighWaterSequence != p.lastPointer.HighWaterSequence || head.FeedChainHash != p.lastPointer.FeedChainHash ||
		head.ProofRootSHA256 != p.proofsRoot || head.Freshness != p.lastFreshness
	held := len(head.ActiveRuns) > 0
	due := force
	if !force && changed && p.now().Sub(p.lastSync) >= publicBucketPointerInterval {
		due = true
		if held {
			outside, err := p.recordsOutsideActiveRuns(ctx, head)
			if err != nil {
				errs = append(errs, err)
			}
			due = outside
		}
	}
	if !due && held && p.synced && head.ProofRootSHA256 != "" && head.ProofRootSHA256 != p.activeRoot {
		withdrawn, err := p.uploadHeldProofs(ctx, head.ProofRootSHA256)
		if err != nil {
			errs = append(errs, err)
		}
		// An artifact leaving the catalog is a withdrawal, which moves the pointer.
		due = withdrawn
	}
	if due {
		state, err := p.source.LayoutState(ctx, p.sourceID)
		if err != nil {
			errs = append(errs, fmt.Errorf("public-feed: bucket: read layout: %w", err))
		} else if err := p.syncLayout(ctx, state, head.ActiveRuns, force, withdrew); err != nil {
			errs = append(errs, err)
		} else {
			p.lastFreshness = head.Freshness
		}
	}
	if p.withdrawalSweep {
		// A withdrawal leaves no grace: earlier read models can name
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

// recordsOutsideActiveRuns reports whether a record accepted since the last check
// belongs to a dataset that is not executing. A record with no dataset does not
// count, and waits for the next pointer.
func (p *PublicBucketPublisher) recordsOutsideActiveRuns(ctx context.Context, head PublicLayoutHead) (bool, error) {
	after := max(p.lastPointer.HighWaterSequence, p.heldThrough)
	if head.HighWaterSequence <= after {
		return false, nil
	}
	datasets, err := p.source.LayoutDatasetsSince(ctx, p.sourceID, after)
	if err != nil {
		return false, fmt.Errorf("public-feed: bucket: read changed datasets: %w", err)
	}
	active := make(map[string]bool, 2*len(head.ActiveRuns))
	for _, run := range head.ActiveRuns {
		active[run.DatasetID] = true
		// Assignment results carry the run's mirror dataset.
		active[campaignMirrorDatasetID(run.RunID)] = true
	}
	for _, datasetID := range datasets {
		if !active[datasetID] {
			return true, nil
		}
	}
	p.heldThrough = head.HighWaterSequence
	return false, nil
}

// uploadHeldProofs uploads proofs that joined the catalog while the pointer held,
// at most once a minute, so a live assignment's proofs resolve before the run
// settles. It uploads nothing and reports true when an artifact left the catalog,
// because that is a withdrawal the next pointer must carry.
func (p *PublicBucketPublisher) uploadHeldProofs(ctx context.Context, root string) (bool, error) {
	if p.now().Sub(p.lastHeldProofs) < publicBucketPointerInterval {
		return false, nil
	}
	catalog, err := p.source.LayoutArtifacts(ctx, p.sourceID)
	if err != nil {
		return false, fmt.Errorf("public-feed: bucket: read catalog: %w", err)
	}
	listed := make(map[string]bool, len(catalog))
	for _, entry := range catalog {
		if key, ok := publicLayoutArtifactKey(entry); ok {
			listed[key] = true
		}
	}
	for key := range p.active {
		if !listed[key] {
			return true, nil
		}
	}
	if err := p.uploadProofs(ctx, catalog); err != nil {
		return false, err
	}
	// Uploaded artifacts join the active set, so one withdrawn before the next
	// pointer is still found leaving the catalog and deleted.
	active := maps.Clone(p.active)
	for key := range listed {
		active[key] = true
	}
	p.active = active
	p.activeRoot, p.lastHeldProofs = root, p.now()
	return false, nil
}

// syncLayout uploads every object the next pointer names and then writes it. The
// in-memory state only advances once the pointer reached the bucket.
//
// activeRuns is the set the write was decided on. The pointer reports it and the
// next tick compares against it, so a state read that races the head check
// cannot leave the two disagreeing and force a write on every tick.
func (p *PublicBucketPublisher) syncLayout(ctx context.Context, state PublicLayoutState, activeRuns []models.PublicLiveRun, force, withdrew bool) error {
	first := !p.synced
	if first {
		if err := p.loadUIArtifacts(ctx); err != nil {
			return err
		}
	}
	pointer := state.Pointer
	pointer.Active = len(activeRuns) > 0
	root := state.ProofRootSHA256

	nextActive := p.active
	var withdrawn []string
	if root != "" && (first || root != p.proofsRoot || root != p.activeRoot) {
		catalog, err := p.source.LayoutArtifacts(ctx, p.sourceID)
		if err != nil {
			return fmt.Errorf("public-feed: bucket: read catalog: %w", err)
		}
		nextActive = make(map[string]bool, len(catalog))
		for _, entry := range catalog {
			if key, ok := publicLayoutArtifactKey(entry); ok {
				nextActive[key] = true
			}
		}
		if err := p.uploadProofs(ctx, catalog); err != nil {
			return err
		}
		withdrawn = p.withdrawnArtifacts(nextActive, first)
	}
	// History catches up when the active set changes, no run is active, or an
	// artifact withdrawal forces a new pointer.
	settling := force || !pointer.Active || len(withdrawn) > 0
	historyPages, err := p.publishHistory(ctx, pointer, settling, withdrew)
	if err != nil {
		return err
	}
	readModelKey := constants.PublicLayoutReadModelPrefix + pointer.ReadModelSHA256 + constants.PublicLayoutJSONGzipSuffix
	if err := p.putBytes(ctx, readModelKey, state.ReadModelGzip, constants.HeaderValueApplicationJSON, constants.HeaderValueGzip, constants.PublicLayoutCacheImmutable); err != nil {
		return err
	}

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
	for _, key := range withdrawn {
		entries = append(entries, p.deleteEntries(key)...)
	}
	for _, key := range historyPages.deleted {
		entries = append(entries, p.deleteEntries(key)...)
	}
	// Deleting a run withdraws its dataset and prunes its proofs as two steps, which
	// can land on separate ticks, so either one counts as a withdrawal.
	if withdrew || len(withdrawn) > 0 {
		entries = append(entries, p.purgeEntry(constants.PublicLayoutPointerKey))
		p.withdrawalSweep = true
	}
	if withdrew {
		for _, key := range historyPages.written {
			entries = append(entries, p.purgeEntry(key))
		}
	}
	if err := p.journal.record(ctx, entries...); err != nil {
		return err
	}
	for _, key := range withdrawn {
		delete(p.present, key)
	}
	p.active = nextActive
	if root != "" {
		p.proofsRoot, p.activeRoot = root, root
	}
	if historyPages.high > 0 {
		p.historyHigh = historyPages.high
	}
	p.lastPointer = signed
	p.lastActiveRuns = liveRunSet(activeRuns)
	p.lastSync = p.now()
	p.synced = true
	return nil
}

// loadUIArtifacts lists UI artifact keys, so an interrupted sync resumes without listing historical private proofs.
func (p *PublicBucketPublisher) loadUIArtifacts(ctx context.Context) error {
	for _, prefix := range []string{constants.PublicLayoutRunsPrefix, constants.PublicLayoutFixturesPrefix} {
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
			if (strings.HasPrefix(key, constants.PublicLayoutRunsPrefix) || strings.HasPrefix(key, constants.PublicLayoutFixturesPrefix)) && !next[key] {
				withdrawn = append(withdrawn, key)
			}
		}
	} else {
		for key := range p.active {
			if !next[key] {
				withdrawn = append(withdrawn, key)
			}
		}
	}
	slices.Sort(withdrawn)
	return withdrawn
}

// uploadProofs uploads missing artifacts with a bounded
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
		key, allowed := publicLayoutArtifactKey(entry)
		if !allowed {
			continue
		}
		mu.Lock()
		missing := !p.present[key]
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

// uploadProof uploads one allowed UI artifact.
func (p *PublicBucketPublisher) uploadProof(ctx context.Context, entry models.PublicProofCatalogEntry, mu *sync.Mutex) error {
	proofKey, allowed := publicLayoutArtifactKey(entry)
	if !allowed {
		return nil
	}
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
func (p *PublicBucketPublisher) publishLive(ctx context.Context, runs []models.PublicLiveRun, withdrew bool) error {
	active := len(runs) > 0
	if !active && !p.statusActive && !withdrew && len(p.liveRunSHA) == 0 && p.liveRunsKnown {
		return nil
	}
	var errs []error
	if active || p.statusActive || withdrew {
		if err := p.publishStatus(ctx, active); err != nil {
			errs = append(errs, err)
		}
	}
	if err := p.publishLiveRuns(ctx, runs); err != nil {
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

func (p *PublicBucketPublisher) publishLiveRuns(ctx context.Context, runs []models.PublicLiveRun) error {
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
	current := make(map[string]bool, len(runs))
	for _, run := range runs {
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
	for _, run := range runs {
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

// sweep queues read models the pointer stopped naming and history
// pages below the retained window, when they are older than cutoff. With purge
// their cached copies are purged too.
func (p *PublicBucketPublisher) sweep(ctx context.Context, cutoff time.Time, purge bool) error {
	keep := map[string]bool{
		constants.PublicLayoutReadModelPrefix + p.lastPointer.ReadModelSHA256 + constants.PublicLayoutJSONGzipSuffix: true,
	}
	var entries []models.PublicBucketJournalEntry
	queue := func(key string) {
		if purge {
			entries = append(entries, p.deleteEntries(key)...)
			return
		}
		entries = append(entries, models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: key})
	}
	for _, prefix := range []string{constants.PublicLayoutReadModelPrefix} {
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
