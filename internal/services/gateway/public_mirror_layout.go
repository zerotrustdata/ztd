// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// PublicMirrorServer serves the public layout: the pointer, digest-addressed
// read model, run bundles and fixtures, and paged history and live objects that the
// bucket publisher and browsers read.
var _ PublicLayoutSource = (*PublicMirrorServer)(nil)

// publicEvaluationSummaryKind is the top-level kind of the explorer summary
// record that names a run's lifecycle.
const publicEvaluationSummaryKind = "evaluation_summary"

const (
	// Keep advertised objects beyond the pointer's 30-second HTTP cache. The
	// count and compressed byte limits bound memory during a busy republish.
	publicMirrorReadModelRetention  = time.Minute
	publicMirrorReadModelMaxEntries = 128
	publicMirrorReadModelMaxBytes   = 64 << 20
)

// publicMirrorLayoutCache is the layout state one mirror keeps: the signer
// and last served pointer, and retained read models. The zero
// value is ready to use.
type publicMirrorLayoutCache struct {
	// pointerMu orders pointer composition, signing, and memoization so
	// revisions advance in the order they are served.
	pointerMu sync.Mutex
	signer    func(models.PublicFeedPointer) (models.PublicFeedPointer, error)
	// pointer is the last served pointer. pointerContent is its content with
	// the revision, generation time, and signature cleared.
	pointer        models.PublicFeedPointer
	pointerContent models.PublicFeedPointer

	// offlineBootstraps holds the read model of each source with no accepted batch.
	// The mirror's writeMu guards it.
	offlineBootstraps map[string]cachedBootstrapSnapshot
	// readModels retains only compressed bodies. writeMu guards these fields,
	// and withdrawals invalidate every retained body and any in-flight build.
	readModels                 []publicLayoutReadModel
	readModelWithdrawalVersion uint64
}

type publicLayoutReadModel struct {
	sourceID  string
	digest    string
	gzipBytes []byte
	expiresAt time.Time
}

// publicEvaluationSummaryRow decodes the fields of an explorer summary that
// select the active runs. Unknown fields are ignored.
type publicEvaluationSummaryRow struct {
	Kind           string `json:"kind"`
	DatasetID      string `json:"dataset_id"`
	RunID          string `json:"run_id"`
	LifecycleState string `json:"lifecycle_state"`
}

// SetPointerSigner sets the function that signs each new pointer. A mirror
// without a signer serves pointers unsigned. The next pointer request re-signs
// even when its content is unchanged.
func (m *PublicMirrorServer) SetPointerSigner(sign func(models.PublicFeedPointer) (models.PublicFeedPointer, error)) {
	m.layout.pointerMu.Lock()
	defer m.layout.pointerMu.Unlock()
	m.layout.signer = sign
	m.layout.pointerContent = models.PublicFeedPointer{}
}

// layoutSource captures one source's summary and freshness coherently. The summary
// is nil for a source the mirror has never accepted a batch from.
func (m *PublicMirrorServer) layoutSource(requestedSource string) (string, *PublicMirrorSourceSummary, models.CampaignFreshness, string) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	sourceID, summary := m.sourceView(requestedSource)
	if summary == nil {
		return sourceID, nil, models.CampaignFreshnessSourceOffline, ""
	}
	summaryCopy := *summary
	return sourceID, &summaryCopy, m.sourceFreshness(summary), mirrorWithdrawalDigest(summary.WithdrawnDatasetIDs)
}

// LayoutHead reports the current head without building the read model.
func (m *PublicMirrorServer) LayoutHead(ctx context.Context, requestedSource string) (PublicLayoutHead, error) {
	sourceID, summary, freshness, withdrawalDigest := m.layoutSource(requestedSource)
	runs, err := m.activePublicRuns(ctx, sourceID, freshness)
	if err != nil {
		return PublicLayoutHead{}, err
	}
	head := PublicLayoutHead{FeedChainHash: constants.PublicFeedZeroHashHex, Freshness: freshness, ActiveRuns: runs}
	if summary != nil {
		head.HighWaterSequence = summary.HighWaterSequence
		head.FeedChainHash = summary.FeedChainHash
		head.WithdrawalDigest = withdrawalDigest
	}
	proofHead, found, err := m.store.ProofHead(ctx, sourceID)
	if err != nil {
		return PublicLayoutHead{}, fmt.Errorf("mirror layout: read proof head: %w", err)
	}
	if found {
		head.ProofRootSHA256 = proofHead.ProofRootSHA256
	}
	return head, nil
}

// LayoutState composes the current pointer with the read model it names.
func (m *PublicMirrorServer) LayoutState(ctx context.Context, requestedSource string) (PublicLayoutState, error) {
	sourceID, summary, _, _ := m.layoutSource(requestedSource)
	readModel, digest, err := m.layoutReadModel(ctx, sourceID)
	if err != nil {
		return PublicLayoutState{}, err
	}
	runs, err := m.activePublicRuns(ctx, sourceID, readModel.freshness)
	if err != nil {
		return PublicLayoutState{}, err
	}
	pointer := models.PublicFeedPointer{
		SchemaVersion:   constants.PublicFeedPointerSchemaVersion,
		SourceID:        sourceID,
		ReadModelSHA256: digest,
		FeedChainHash:   constants.PublicFeedZeroHashHex,
		Active:          len(runs) > 0,
		GeneratedAt:     time.Now().UTC(),
	}
	if summary != nil {
		pointer.HighWaterSequence = readModel.sequence
		pointer.FeedChainHash = readModel.feedChainHash
		pointer.WithdrawalDigest = readModel.withdrawalDigest
		pointer.HistoryFirstSequence = summary.RetainedFromSequence
	}
	root := ""
	head, found, err := m.store.ProofHead(ctx, sourceID)
	if err != nil {
		return PublicLayoutState{}, fmt.Errorf("mirror layout: read proof head: %w", err)
	}
	if found {
		root = head.ProofRootSHA256
	}
	return PublicLayoutState{Pointer: pointer, ProofRootSHA256: root, ReadModelGzip: readModel.gzipBytes, ActiveRuns: runs}, nil
}

// LayoutDatasetsSince returns the datasets of current records accepted after sequence.
func (m *PublicMirrorServer) LayoutDatasetsSince(ctx context.Context, sourceID string, after int64) ([]string, error) {
	return m.store.snapshotDatasetsSince(ctx, sourceID, after)
}

// LayoutArtifacts selects UI artifacts from the source's internal active catalog.
// Historical private artifact metadata is never encoded for the public layout.
func (m *PublicMirrorServer) LayoutArtifacts(ctx context.Context, sourceID string) ([]models.PublicProofCatalogEntry, error) {
	catalog, found, err := m.store.ProofManifest(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("mirror layout: read internal artifacts: %w", err)
	}
	entries := []models.PublicProofCatalogEntry{}
	if found {
		for _, entry := range catalog.Artifacts {
			if _, allowed := publicLayoutArtifactKey(entry); allowed {
				entries = append(entries, entry)
			}
		}
	}
	return entries, nil
}

// LayoutHistoryPage returns one gzipped history page covering
// [first, first+constants.PublicLayoutHistoryPageRecords).
func (m *PublicMirrorServer) LayoutHistoryPage(ctx context.Context, requestedSource string, first int64) ([]byte, bool, error) {
	if !publicLayoutChunkStart(first, constants.PublicLayoutHistoryPageRecords) {
		return nil, false, nil
	}
	sourceID, summary := m.sourceView(requestedSource)
	if summary == nil {
		return nil, false, nil
	}
	last := first + constants.PublicLayoutHistoryPageRecords - 1
	records, err := m.store.recordsInRange(ctx, sourceID, first, last)
	if err != nil {
		return nil, false, fmt.Errorf("mirror layout: read history page: %w", err)
	}
	items := []models.PublicFeedObject{}
	for _, record := range records {
		item, ok := publicHistoryItem(record, summary.WithdrawnDatasetIDs)
		if ok {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return nil, false, nil
	}
	body, err := json.Marshal(models.PublicFeedCursorPage{
		ProtocolVersion: constants.PublicFeedProtocolVersion,
		Items:           items,
		HasMore:         false,
		Limit:           constants.PublicLayoutHistoryPageRecords,
	})
	if err != nil {
		return nil, false, fmt.Errorf("mirror layout: encode history page: %w", err)
	}
	gzipped, err := publicLayoutGzip(body)
	if err != nil {
		return nil, false, err
	}
	return gzipped, true, nil
}

// LayoutLiveStatus returns live/status.json: the snapshot, the active runs,
// and the newest non-result records.
func (m *PublicMirrorServer) LayoutLiveStatus(ctx context.Context, requestedSource string) (models.PublicLiveStatus, error) {
	sourceID, summary, freshness, _ := m.layoutSource(requestedSource)
	snapshot := offlinePublicMirrorSnapshot(sourceID)
	if summary != nil {
		snapshot = m.snapshotOf(sourceID, summary)
		snapshot.Freshness = freshness
	}
	runs, err := m.activePublicRuns(ctx, sourceID, freshness)
	if err != nil {
		return models.PublicLiveStatus{}, err
	}
	tail, err := m.livePublicTail(ctx, sourceID, summary)
	if err != nil {
		return models.PublicLiveStatus{}, err
	}
	return models.PublicLiveStatus{
		SchemaVersion: constants.PublicLiveStatusSchemaVersion,
		Snapshot:      snapshot,
		ActiveRuns:    runs,
		Tail:          tail,
	}, nil
}

// LayoutLiveRunBundle returns one run's assignment bundle, and false when the
// run has no assignment result records yet.
func (m *PublicMirrorServer) LayoutLiveRunBundle(ctx context.Context, requestedSource, runID string) ([]byte, bool, error) {
	sourceID, _ := m.sourceView(requestedSource)
	records, err := m.store.RunBundleRecords(ctx, sourceID, runID)
	if err != nil {
		return nil, false, fmt.Errorf("mirror layout: read run bundle: %w", err)
	}
	bundle, err := buildRunBundle(records)
	if errors.Is(err, constants.ErrPublicRunBundleEmpty) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("mirror layout: build run bundle: %w", err)
	}
	return bundle.content, true, nil
}

// OpenLayoutProof opens one stored proof artifact for upload.
func (m *PublicMirrorServer) OpenLayoutProof(ctx context.Context, artifactID string) (*os.File, error) {
	return m.store.openProofArtifact(ctx, artifactID)
}

// layoutReadModel returns the bootstrap read model and its digest, the SHA-256
// of the uncompressed bytes.
func (m *PublicMirrorServer) layoutReadModel(ctx context.Context, sourceID string) (cachedBootstrapSnapshot, string, error) {
	cached, err := m.currentBootstrapCached(ctx, sourceID)
	if err != nil {
		return cachedBootstrapSnapshot{}, "", fmt.Errorf("mirror layout: read model: %w", err)
	}
	sum := sha256.Sum256(cached.jsonBytes)
	digest := hex.EncodeToString(sum[:])
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if cached.withdrawalVersion != m.layout.readModelWithdrawalVersion {
		return cachedBootstrapSnapshot{}, "", fmt.Errorf("mirror layout: withdrawal during read model build: %w", constants.ErrPublicFeedPointerInvalid)
	}
	if err := m.retainLayoutReadModel(sourceID, digest, cached.gzipBytes); err != nil {
		return cachedBootstrapSnapshot{}, "", err
	}
	return cached, digest, nil
}

// retainLayoutReadModel remembers a body before advertising its digest. The
// caller holds writeMu. Retention is bounded globally across all sources.
func (m *PublicMirrorServer) retainLayoutReadModel(sourceID, digest string, body []byte) error {
	if len(body) > publicMirrorReadModelMaxBytes {
		return fmt.Errorf("mirror layout: compressed read model exceeds retention budget: %w", constants.ErrEvidenceArtifactTooLarge)
	}
	now := m.now()
	m.pruneLayoutReadModels(now)
	for i, entry := range m.layout.readModels {
		if entry.sourceID == sourceID && entry.digest == digest {
			m.layout.readModels = slices.Delete(m.layout.readModels, i, i+1)
			break
		}
	}
	m.layout.readModels = append(m.layout.readModels, publicLayoutReadModel{
		sourceID: sourceID, digest: digest, gzipBytes: body,
		expiresAt: now.Add(publicMirrorReadModelRetention),
	})
	byteSize := 0
	for _, entry := range m.layout.readModels {
		byteSize += len(entry.gzipBytes)
	}
	for len(m.layout.readModels) > publicMirrorReadModelMaxEntries || byteSize > publicMirrorReadModelMaxBytes {
		byteSize -= len(m.layout.readModels[0].gzipBytes)
		m.layout.readModels = slices.Delete(m.layout.readModels, 0, 1)
	}
	return nil
}

// pruneLayoutReadModels drops expired bodies. The caller holds writeMu.
func (m *PublicMirrorServer) pruneLayoutReadModels(now time.Time) {
	m.layout.readModels = slices.DeleteFunc(m.layout.readModels, func(entry publicLayoutReadModel) bool {
		return !now.Before(entry.expiresAt)
	})
}

// discardLayoutReadModels invalidates retained bodies and builds that started
// before a committed withdrawal. The caller holds writeMu.
func (m *PublicMirrorServer) discardLayoutReadModels() {
	m.layout.readModels = nil
	m.layout.readModelWithdrawalVersion++
}

// activePublicRuns returns the runs whose latest explorer summary is queued or
// running, one entry per run, sorted by dataset then run. A source that stopped
// publishing is executing nothing, so a run it left unsettled is not active.
// Withdrawn datasets are already absent. The result is never nil.
func (m *PublicMirrorServer) activePublicRuns(ctx context.Context, sourceID string, freshness models.CampaignFreshness) ([]models.PublicLiveRun, error) {
	switch freshness {
	case models.CampaignFreshnessSourceOffline, models.CampaignFreshnessIntentionallyStopped, models.CampaignFreshnessSafetyStopped:
		return []models.PublicLiveRun{}, nil
	}
	records, err := m.store.snapshotRecordsOfKind(ctx, sourceID, publicEvaluationSummaryKind)
	if err != nil {
		return nil, fmt.Errorf("mirror layout: read summary records: %w", err)
	}
	type runKey struct{ dataset, run string }
	seen := make(map[runKey]bool)
	runs := []models.PublicLiveRun{}
	for _, record := range records {
		var row publicEvaluationSummaryRow
		if err := json.Unmarshal([]byte(record.RecordBytes), &row); err != nil {
			return nil, fmt.Errorf("mirror layout: decode snapshot record %d: %w", record.Sequence, err)
		}
		if row.Kind != publicEvaluationSummaryKind {
			continue
		}
		state := models.RunLifecycleStatus(row.LifecycleState)
		if state != models.RunLifecycleStatusQueued && state != models.RunLifecycleStatusRunning {
			continue
		}
		key := runKey{dataset: row.DatasetID, run: row.RunID}
		if seen[key] {
			continue
		}
		seen[key] = true
		runs = append(runs, models.PublicLiveRun{DatasetID: row.DatasetID, RunID: row.RunID, LifecycleState: row.LifecycleState})
	}
	slices.SortFunc(runs, func(a, b models.PublicLiveRun) int {
		return cmp.Or(cmp.Compare(a.DatasetID, b.DatasetID), cmp.Compare(a.RunID, b.RunID))
	})
	return runs, nil
}

// livePublicTail returns the newest constants.PublicLiveStatusTailLimit
// retained records that are not assignment results and not withdrawn, oldest
// first. It walks the retained window backward one page at a time.
func (m *PublicMirrorServer) livePublicTail(ctx context.Context, sourceID string, summary *PublicMirrorSourceSummary) ([]models.PublicLiveRecord, error) {
	tail := []models.PublicLiveRecord{}
	if summary == nil {
		return tail, nil
	}
	lowest := summary.RetainedFromSequence
	last := summary.HighWaterSequence
	for last >= lowest && len(tail) < constants.PublicLiveStatusTailLimit {
		first := max(lowest, last-publicMirrorReadPageRecords+1)
		records, err := m.store.recordsInRange(ctx, sourceID, first, last)
		if err != nil {
			return nil, fmt.Errorf("mirror layout: read live tail: %w", err)
		}
		for index := len(records) - 1; index >= 0 && len(tail) < constants.PublicLiveStatusTailLimit; index-- {
			record := records[index]
			isResult, err := isPublicAssignmentResultRecord(record)
			if err != nil {
				return nil, fmt.Errorf("mirror layout: decode live tail record %d: %w", record.Sequence, err)
			}
			if isResult || mirrorRecordWithdrawn(summary.WithdrawnDatasetIDs, record) {
				continue
			}
			tail = append(tail, models.PublicLiveRecord{Sequence: record.Sequence, RecordType: record.RecordType, RecordBytes: record.RecordBytes})
		}
		last = first - 1
	}
	slices.Reverse(tail)
	return tail, nil
}

// publicLayoutGzip compresses body with the best level and no gzip header
// name or modification time, so identical bytes always compress identically.
func publicLayoutGzip(body []byte) ([]byte, error) {
	var out bytes.Buffer
	writer, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("mirror layout: gzip: %w", err)
	}
	if _, err := writer.Write(body); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("mirror layout: gzip write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("mirror layout: gzip close: %w", err)
	}
	return out.Bytes(), nil
}

// publicLayoutChunkStart reports whether first begins a chunk of size records.
// Indices start at one, so chunk starts are 1, 1+size, 1+2*size, and so on.
func publicLayoutChunkStart(first, size int64) bool {
	return first >= 1 && (first-1)%size == 0
}

// publicPointerContent is the pointer's content without the fields that change
// on every revision: the revision, generation time, and signature.
func publicPointerContent(pointer models.PublicFeedPointer) models.PublicFeedPointer {
	pointer.Revision = 0
	pointer.GeneratedAt = time.Time{}
	pointer.SigningKeyID = ""
	pointer.SigningPublicKey = ""
	pointer.Signature = ""
	return pointer
}

// publicMirrorLayoutReadPrefixes are the layout routes that take a path suffix.
// Exact layout routes are named in publicMirrorLayoutReadPath.
var publicMirrorLayoutReadPrefixes = []string{
	"/" + constants.PublicLayoutReadModelPrefix,
	"/" + constants.PublicLayoutRunsPrefix,
	"/" + constants.PublicLayoutFixturesPrefix,
	"/" + constants.PublicLayoutHistoryPrefix,
	"/" + constants.PublicLayoutLiveRunsPrefix,
}

// publicMirrorLayoutReadPath reports whether requestPath is an anonymous layout
// read route, so it gets the same CORS and rate limit as the other reads.
func publicMirrorLayoutReadPath(requestPath string) bool {
	switch requestPath {
	case "/" + constants.PublicLayoutPointerKey, "/" + constants.PublicLayoutLiveStatusKey:
		return true
	}
	for _, prefix := range publicMirrorLayoutReadPrefixes {
		if strings.HasPrefix(requestPath, prefix) {
			return true
		}
	}
	return false
}

// publicLayoutName returns the middle of a route of the form prefix<name>suffix.
// It reports false when the path has another shape or the name is empty.
func publicLayoutName(requestPath, prefix, suffix string) (string, bool) {
	name, ok := strings.CutPrefix(requestPath, prefix)
	if !ok {
		return "", false
	}
	name, ok = strings.CutSuffix(name, suffix)
	return name, ok && name != ""
}

// publicLayoutIndex parses a canonical positive decimal path segment. Signs,
// leading zeros, and spaces are rejected, so each index has one path.
func publicLayoutIndex(name string) (int64, bool) {
	index, err := strconv.ParseInt(name, 10, 64)
	if err != nil || index < 1 || strconv.FormatInt(index, 10) != name {
		return 0, false
	}
	return index, true
}

// publicLayoutGET rejects every method but GET, writing the 405 itself.
func publicLayoutGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// writePublicLayoutBody writes one precomputed body. encoding is empty when the
// body is served as stored.
func (m *PublicMirrorServer) writePublicLayoutBody(w http.ResponseWriter, contentType, encoding, cacheControl string, body []byte) {
	w.Header().Set(constants.HeaderContentType, contentType)
	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	w.Header().Set(constants.HeaderCacheControl, cacheControl)
	w.Header().Set(constants.HeaderContentLength, strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil { //nolint:gosec // G705: stored public layout objects served with a fixed Content-Type, not reflected request input
		m.logResponseError("mirror: write layout response", err)
	}
}

// handleLayoutPointer serves pointer.json. The pointer is served unchanged
// while its content is the same, so the revision advances only with content.
func (m *PublicMirrorServer) handleLayoutPointer(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	sourceID, _ := m.sourceView(r.URL.Query().Get("source"))
	m.layout.pointerMu.Lock()
	defer m.layout.pointerMu.Unlock()
	state, err := m.LayoutState(r.Context(), sourceID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: compose layout pointer", err)
		return
	}
	pointer := state.Pointer
	content := publicPointerContent(pointer)
	if content != m.layout.pointerContent {
		pointer.Revision = nextPublicFeedPointerRevision(m.layout.pointer.Revision, time.Now())
		if m.layout.signer != nil {
			signed, err := m.layout.signer(pointer)
			if err != nil {
				m.writeStoreUnavailable(w, "mirror: sign layout pointer", err)
				return
			}
			pointer = signed
		}
		m.layout.pointer = pointer
		m.layout.pointerContent = content
	}
	w.Header().Set(constants.HeaderCacheControl, constants.PublicLayoutCachePointer)
	m.writeJSON(w, http.StatusOK, m.layout.pointer)
}

// handleLayoutReadModel serves the current and recently advertised read models.
func (m *PublicMirrorServer) handleLayoutReadModel(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	name, ok := publicLayoutName(r.URL.Path, "/"+constants.PublicLayoutReadModelPrefix, constants.PublicLayoutJSONGzipSuffix)
	if !ok || !validPublicProofArtifactID(name) {
		http.NotFound(w, r)
		return
	}
	sourceID, _ := m.sourceView(r.URL.Query().Get("source"))
	m.writeMu.Lock()
	m.pruneLayoutReadModels(m.now())
	var retained []byte
	for _, entry := range m.layout.readModels {
		if entry.sourceID == sourceID && entry.digest == name {
			retained = entry.gzipBytes
			break
		}
	}
	m.writeMu.Unlock()
	if retained != nil {
		m.writePublicLayoutBody(w, constants.HeaderValueApplicationJSON, "gzip", constants.PublicLayoutCacheImmutable, retained)
		return
	}
	cached, digest, err := m.layoutReadModel(r.Context(), sourceID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout read model", err)
		return
	}
	if !ok || name != digest {
		http.NotFound(w, r)
		return
	}
	m.writePublicLayoutBody(w, constants.HeaderValueApplicationJSON, "gzip", constants.PublicLayoutCacheImmutable, cached.gzipBytes)
}

// handleLayoutHistory serves history/<first>.json.gz.
func (m *PublicMirrorServer) handleLayoutHistory(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	name, ok := publicLayoutName(r.URL.Path, "/"+constants.PublicLayoutHistoryPrefix, constants.PublicLayoutJSONGzipSuffix)
	first, indexOK := publicLayoutIndex(name)
	if !ok || !indexOK {
		http.NotFound(w, r)
		return
	}
	body, found, err := m.LayoutHistoryPage(r.Context(), r.URL.Query().Get("source"), first)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout history", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	m.writePublicLayoutBody(w, constants.HeaderValueApplicationJSON, "gzip", constants.PublicLayoutCacheHistory, body)
}

// handleLayoutLiveStatus serves live/status.json.
func (m *PublicMirrorServer) handleLayoutLiveStatus(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	status, err := m.LayoutLiveStatus(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout live status", err)
		return
	}
	w.Header().Set(constants.HeaderCacheControl, constants.PublicLayoutCacheLive)
	m.writeJSON(w, http.StatusOK, status)
}

// handleLayoutLiveRun serves live/runs/<run>.jsonl.gz for an active run. The
// bundle is a gzip file served as-is, not as a content-encoded body.
func (m *PublicMirrorServer) handleLayoutLiveRun(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	runID, ok := publicLayoutName(r.URL.Path, "/"+constants.PublicLayoutLiveRunsPrefix, constants.PublicLayoutJSONLGzipSuffix)
	if !ok || strings.Contains(runID, "/") || strings.Contains(runID, "..") {
		http.NotFound(w, r)
		return
	}
	sourceID, _, freshness, _ := m.layoutSource(r.URL.Query().Get("source"))
	runs, err := m.activePublicRuns(r.Context(), sourceID, freshness)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout active runs", err)
		return
	}
	if !slices.ContainsFunc(runs, func(run models.PublicLiveRun) bool { return run.RunID == runID }) {
		http.NotFound(w, r)
		return
	}
	body, found, err := m.LayoutLiveRunBundle(r.Context(), sourceID, runID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout run bundle", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	m.writePublicLayoutBody(w, constants.MediaTypeGzip, "", constants.PublicLayoutCacheLiveRun, body)
}

// handleLayoutArtifact serves only active run bundles and scenario fixtures.
// The source catalog determines both eligibility and the exact path, so a
// historical private artifact cannot be fetched by placing its digest here.
func (m *PublicMirrorServer) handleLayoutArtifact(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/")
	artifactID := publicLayoutArtifactID(key)
	if !validPublicProofArtifactID(artifactID) {
		http.NotFound(w, r)
		return
	}
	sourceID, hasManifest, err := m.resolveProofManifestSource(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: resolve layout artifact source", err)
		return
	}
	if !hasManifest {
		http.NotFound(w, r)
		return
	}
	entry, found, err := m.store.proofCatalogEntry(r.Context(), sourceID, artifactID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout artifact", err)
		return
	}
	allowedKey, allowed := publicLayoutArtifactKey(entry)
	if !found || !allowed || key != allowedKey {
		http.NotFound(w, r)
		return
	}
	m.serveProofArtifact(w, r, artifactID)
}
