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
// read model and proof objects, and paged history and live objects that the
// bucket publisher and browsers read.
var _ PublicLayoutSource = (*PublicMirrorServer)(nil)

// publicEvaluationSummaryKind is the top-level kind of the explorer summary
// record that names a run's lifecycle.
const publicEvaluationSummaryKind = "evaluation_summary"

// publicMirrorLayoutCache is the layout state one mirror keeps: the signer
// and last served pointer, and the memoized proof catalog per source. The zero
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

	catalogMu sync.Mutex
	catalogs  map[string]publicLayoutCatalogMemo
}

// publicLayoutCatalogMemo is the last catalog built for one source and the
// proof root it was built from.
type publicLayoutCatalogMemo struct {
	root    string
	catalog PublicLayoutCatalog
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

// LayoutState composes the current pointer with the read model it names.
func (m *PublicMirrorServer) LayoutState(ctx context.Context, requestedSource string) (PublicLayoutState, error) {
	sourceID, summary := m.sourceView(requestedSource)
	readModel, digest, err := m.layoutReadModel(ctx, sourceID)
	if err != nil {
		return PublicLayoutState{}, err
	}
	runs, err := m.activePublicRuns(ctx, sourceID)
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
	head, found, err := m.store.ProofHead(ctx, sourceID)
	if err != nil {
		return PublicLayoutState{}, fmt.Errorf("mirror layout: read proof head: %w", err)
	}
	if found {
		pointer.ProofRootSHA256 = head.ProofRootSHA256
		pointer.ProofSegmentHead = head.SegmentIndex
	}
	return PublicLayoutState{Pointer: pointer, ReadModelGzip: readModel.gzipBytes, ActiveRuns: runs}, nil
}

// LayoutProofCatalog returns the proof catalog at the current proof root. The
// bytes are memoized per source and rebuilt only when the root changes.
func (m *PublicMirrorServer) LayoutProofCatalog(ctx context.Context, sourceID string) (PublicLayoutCatalog, error) {
	head, found, err := m.store.ProofHead(ctx, sourceID)
	if err != nil {
		return PublicLayoutCatalog{}, fmt.Errorf("mirror layout: read proof head: %w", err)
	}
	currentRoot := ""
	if found {
		currentRoot = head.ProofRootSHA256
	}
	m.layout.catalogMu.Lock()
	defer m.layout.catalogMu.Unlock()
	if memo, ok := m.layout.catalogs[sourceID]; ok && memo.root == currentRoot {
		return memo.catalog, nil
	}

	catalog := models.PublicProofCatalog{
		SchemaVersion: constants.PublicProofCatalogSchemaVersion,
		Entries:       []models.PublicProofCatalogEntry{},
	}
	root := ""
	manifest, hasManifest, err := m.store.ProofManifest(ctx, sourceID)
	if err != nil {
		return PublicLayoutCatalog{}, fmt.Errorf("mirror layout: read proof manifest: %w", err)
	}
	if hasManifest {
		catalog = proofCatalogFromManifest(manifest)
		root = manifest.ProofRootSHA256
	}
	if catalog.Entries == nil {
		catalog.Entries = []models.PublicProofCatalogEntry{}
	}
	body, err := json.Marshal(catalog)
	if err != nil {
		return PublicLayoutCatalog{}, fmt.Errorf("mirror layout: encode proof catalog: %w", err)
	}
	gzipped, err := publicLayoutGzip(body)
	if err != nil {
		return PublicLayoutCatalog{}, err
	}
	sum := sha256.Sum256(body)
	result := PublicLayoutCatalog{SHA256: hex.EncodeToString(sum[:]), Gzip: gzipped, Entries: catalog.Entries}
	if m.layout.catalogs == nil {
		m.layout.catalogs = make(map[string]publicLayoutCatalogMemo)
	}
	m.layout.catalogs[sourceID] = publicLayoutCatalogMemo{root: root, catalog: result}
	return result, nil
}

// LayoutProofHead returns proof-head.json, and false when the source has no
// proof manifest.
func (m *PublicMirrorServer) LayoutProofHead(ctx context.Context, sourceID string) ([]byte, bool, error) {
	head, found, err := m.store.ProofHead(ctx, sourceID)
	if err != nil {
		return nil, false, fmt.Errorf("mirror layout: read proof head: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	head.SigningPublicKey = m.fillProofSigningKey(sourceID, head.SigningKeyID, head.SigningPublicKey)
	body, err := json.Marshal(head)
	if err != nil {
		return nil, false, fmt.Errorf("mirror layout: encode proof head: %w", err)
	}
	return body, true, nil
}

// LayoutSegmentChunk returns the gzipped proof segment JSONL for one chunk.
func (m *PublicMirrorServer) LayoutSegmentChunk(ctx context.Context, sourceID string, first int64) ([]byte, bool, error) {
	gzipped, _, found, err := m.layoutSegmentChunk(ctx, sourceID, first)
	return gzipped, found, err
}

// layoutSegmentChunk also reports how many segments the chunk holds, so the
// route can tell a full, immutable chunk from one that is still growing.
func (m *PublicMirrorServer) layoutSegmentChunk(ctx context.Context, sourceID string, first int64) ([]byte, int, bool, error) {
	if !publicLayoutChunkStart(first, constants.PublicLayoutSegmentChunkSize) {
		return nil, 0, false, nil
	}
	segments, err := m.store.proofSegmentsInRange(ctx, sourceID, first, first+constants.PublicLayoutSegmentChunkSize)
	if err != nil {
		return nil, 0, false, err
	}
	if len(segments) == 0 {
		return nil, 0, false, nil
	}
	var lines bytes.Buffer
	for index := range segments {
		segments[index].SigningPublicKey = m.fillProofSigningKey(segments[index].SourceID, segments[index].SigningKeyID, segments[index].SigningPublicKey)
		line, err := json.Marshal(segments[index])
		if err != nil {
			return nil, 0, false, fmt.Errorf("mirror layout: encode proof segment %d: %w", segments[index].SegmentIndex, err)
		}
		lines.Write(line)
		lines.WriteByte('\n')
	}
	gzipped, err := publicLayoutGzip(lines.Bytes())
	if err != nil {
		return nil, 0, false, err
	}
	return gzipped, len(segments), true, nil
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
	sourceID, summary := m.sourceView(requestedSource)
	bootstrap, err := m.currentBootstrap(ctx, sourceID)
	if err != nil {
		return models.PublicLiveStatus{}, err
	}
	runs, err := m.activePublicRuns(ctx, sourceID)
	if err != nil {
		return models.PublicLiveStatus{}, err
	}
	tail, err := m.livePublicTail(ctx, sourceID, summary)
	if err != nil {
		return models.PublicLiveStatus{}, err
	}
	return models.PublicLiveStatus{
		SchemaVersion: constants.PublicLiveStatusSchemaVersion,
		Snapshot:      bootstrap.Snapshot,
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
	return cached, hex.EncodeToString(sum[:]), nil
}

// activePublicRuns returns the runs whose latest explorer summary is queued or
// running, one entry per run, sorted by dataset then run. Withdrawn datasets
// are already absent from the snapshot. The result is never nil.
func (m *PublicMirrorServer) activePublicRuns(ctx context.Context, sourceID string) ([]models.PublicLiveRun, error) {
	records, err := m.store.snapshotRecords(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("mirror layout: read snapshot records: %w", err)
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
	"/" + constants.PublicLayoutProofCatalogPrefix,
	"/" + constants.PublicLayoutProofMetaPrefix,
	"/" + constants.PublicLayoutProofSegmentsPrefix,
	"/" + constants.PublicLayoutHistoryPrefix,
	"/" + constants.PublicLayoutLiveRunsPrefix,
}

// publicMirrorLayoutReadPath reports whether requestPath is an anonymous layout
// read route, so it gets the same CORS and rate limit as the other reads.
func publicMirrorLayoutReadPath(requestPath string) bool {
	switch requestPath {
	case "/" + constants.PublicLayoutPointerKey, "/" + constants.PublicLayoutProofHeadKey, "/" + constants.PublicLayoutLiveStatusKey:
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
	if _, err := w.Write(body); err != nil {
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
	if pointer.ProofRootSHA256 != "" {
		catalog, err := m.LayoutProofCatalog(r.Context(), sourceID)
		if err != nil {
			m.writeStoreUnavailable(w, "mirror: read layout proof catalog", err)
			return
		}
		pointer.ProofCatalogSHA256 = catalog.SHA256
	}
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

// handleLayoutReadModel serves read-model/<sha>.json.gz for the current digest.
func (m *PublicMirrorServer) handleLayoutReadModel(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	name, ok := publicLayoutName(r.URL.Path, "/"+constants.PublicLayoutReadModelPrefix, constants.PublicLayoutJSONGzipSuffix)
	sourceID, _ := m.sourceView(r.URL.Query().Get("source"))
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

// handleLayoutProofCatalog serves proof-catalog/<sha>.json.gz for the current
// catalog digest.
func (m *PublicMirrorServer) handleLayoutProofCatalog(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	name, ok := publicLayoutName(r.URL.Path, "/"+constants.PublicLayoutProofCatalogPrefix, constants.PublicLayoutJSONGzipSuffix)
	sourceID, hasManifest, err := m.resolveProofManifestSource(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: resolve layout proof catalog source", err)
		return
	}
	if !ok || !hasManifest {
		http.NotFound(w, r)
		return
	}
	catalog, err := m.LayoutProofCatalog(r.Context(), sourceID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout proof catalog", err)
		return
	}
	if name != catalog.SHA256 {
		http.NotFound(w, r)
		return
	}
	m.writePublicLayoutBody(w, constants.HeaderValueApplicationJSON, "gzip", constants.PublicLayoutCacheImmutable, catalog.Gzip)
}

// handleLayoutProofMeta serves one active catalog entry by artifact ID. A
// withdrawn, unknown, or malformed ID is not found.
func (m *PublicMirrorServer) handleLayoutProofMeta(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	name, ok := publicLayoutName(r.URL.Path, "/"+constants.PublicLayoutProofMetaPrefix, constants.PublicLayoutJSONSuffix)
	if !ok || !validPublicProofArtifactID(name) {
		http.NotFound(w, r)
		return
	}
	sourceID, hasManifest, err := m.resolveProofManifestSource(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: resolve proof meta source", err)
		return
	}
	if !hasManifest {
		http.NotFound(w, r)
		return
	}
	entry, found, err := m.store.proofCatalogEntry(r.Context(), sourceID, name)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read proof meta", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set(constants.HeaderCacheControl, constants.PublicLayoutCacheImmutable)
	m.writeJSON(w, http.StatusOK, entry)
}

// handleLayoutProofHead serves proof-head.json.
func (m *PublicMirrorServer) handleLayoutProofHead(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	sourceID, hasManifest, err := m.resolveProofManifestSource(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: resolve layout proof head source", err)
		return
	}
	if !hasManifest {
		http.NotFound(w, r)
		return
	}
	body, found, err := m.LayoutProofHead(r.Context(), sourceID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout proof head", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	m.writePublicLayoutBody(w, constants.HeaderValueApplicationJSON, "", constants.PublicLayoutCacheProofHead, body)
}

// handleLayoutProofSegments serves proof-segments/<first>.jsonl.gz. A full
// chunk never changes; a partial one is the chain tail and expires.
func (m *PublicMirrorServer) handleLayoutProofSegments(w http.ResponseWriter, r *http.Request) {
	if !publicLayoutGET(w, r) {
		return
	}
	name, ok := publicLayoutName(r.URL.Path, "/"+constants.PublicLayoutProofSegmentsPrefix, constants.PublicLayoutJSONLGzipSuffix)
	first, indexOK := publicLayoutIndex(name)
	sourceID, hasManifest, err := m.resolveProofManifestSource(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: resolve layout proof segments source", err)
		return
	}
	if !ok || !indexOK || !hasManifest {
		http.NotFound(w, r)
		return
	}
	body, count, found, err := m.layoutSegmentChunk(r.Context(), sourceID, first)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read layout proof segments", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	cacheControl := constants.PublicLayoutCacheTail
	if count == constants.PublicLayoutSegmentChunkSize {
		cacheControl = constants.PublicLayoutCacheImmutable
	}
	m.writePublicLayoutBody(w, constants.HeaderValueApplicationNDJSON, "gzip", cacheControl, body)
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
	sourceID, _ := m.sourceView(r.URL.Query().Get("source"))
	runs, err := m.activePublicRuns(r.Context(), sourceID)
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
