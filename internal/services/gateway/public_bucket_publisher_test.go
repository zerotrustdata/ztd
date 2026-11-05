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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// The publisher tests in this file drive tick directly against fakes for the
// layout source, the bucket client and the cache purger. The journal is real and
// lives on a runtime file service under testutil.TempDir.

const (
	bucketFakeSourceID     = "source-bucket-test"
	bucketFakeSigningKeyID = "bucket-test-key"
	bucketFakeOrigin       = "https://feed.example"
	bucketFakeBucketName   = "feed-bucket"
	bucketFakeZoneID       = "zone-bucket-test"
)

// bucketFakeClock is the publisher's clock. Tests move it explicitly.
type bucketFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newBucketFakeClock() *bucketFakeClock {
	return &bucketFakeClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *bucketFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *bucketFakeClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

func (c *bucketFakeClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// bucketFakeSource stands in for the colocated mirror. Its fields are the state
// the next LayoutState or LayoutHead call reports.
type bucketFakeSource struct {
	mu sync.Mutex

	head         PublicLayoutHead
	segmentHead  int64
	historyFirst int64
	readModel    []byte
	catalog      PublicLayoutCatalog
	proofHead    []byte
	chunks       map[int64][]byte
	history      map[int64][]byte
	liveStatus   models.PublicLiveStatus
	liveBundles  map[string][]byte
	proofFiles   map[string]string

	layoutStateCalls int
	liveBundleCalls  int
}

func newBucketFakeSource() *bucketFakeSource {
	return &bucketFakeSource{
		head:         PublicLayoutHead{FeedChainHash: constants.PublicFeedZeroHashHex, ActiveRuns: []models.PublicLiveRun{}},
		historyFirst: 1,
		readModel:    []byte(`{"read":"model-0"}`),
		chunks:       map[int64][]byte{},
		history:      map[int64][]byte{},
		liveBundles:  map[string][]byte{},
		proofFiles:   map[string]string{},
	}
}

func (s *bucketFakeSource) LayoutHead(_ context.Context, _ string) (PublicLayoutHead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := s.head
	head.ActiveRuns = slices.Clone(s.head.ActiveRuns)
	if head.ActiveRuns == nil {
		head.ActiveRuns = []models.PublicLiveRun{}
	}
	return head, nil
}

func (s *bucketFakeSource) LayoutState(_ context.Context, _ string) (PublicLayoutState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layoutStateCalls++
	pointer := models.PublicFeedPointer{
		SchemaVersion:        constants.PublicFeedPointerSchemaVersion,
		SourceID:             bucketFakeSourceID,
		ReadModelSHA256:      hashBytes(s.readModel),
		ProofRootSHA256:      s.head.ProofRootSHA256,
		ProofSegmentHead:     s.segmentHead,
		HighWaterSequence:    s.head.HighWaterSequence,
		HistoryFirstSequence: s.historyFirst,
		FeedChainHash:        s.head.FeedChainHash,
		WithdrawalDigest:     s.head.WithdrawalDigest,
		Active:               len(s.head.ActiveRuns) > 0,
		GeneratedAt:          time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	}
	return PublicLayoutState{
		Pointer:       pointer,
		ReadModelGzip: slices.Clone(s.readModel),
		ActiveRuns:    slices.Clone(s.head.ActiveRuns),
	}, nil
}

func (s *bucketFakeSource) LayoutProofCatalog(_ context.Context, _ string) (PublicLayoutCatalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	catalog := s.catalog
	catalog.Entries = slices.Clone(s.catalog.Entries)
	return catalog, nil
}

func (s *bucketFakeSource) LayoutProofHead(_ context.Context, _ string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.proofHead), s.proofHead != nil, nil
}

func (s *bucketFakeSource) LayoutSegmentChunk(_ context.Context, _ string, first int64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chunk, ok := s.chunks[first]
	return slices.Clone(chunk), ok, nil
}

func (s *bucketFakeSource) LayoutHistoryPage(_ context.Context, _ string, first int64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page, ok := s.history[first]
	return slices.Clone(page), ok, nil
}

func (s *bucketFakeSource) LayoutLiveStatus(_ context.Context, _ string) (models.PublicLiveStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveStatus, nil
}

func (s *bucketFakeSource) LayoutLiveRunBundle(_ context.Context, _ string, runID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveBundleCalls++
	bundle, ok := s.liveBundles[runID]
	return slices.Clone(bundle), ok, nil
}

func (s *bucketFakeSource) OpenLayoutProof(_ context.Context, artifactID string) (*os.File, error) {
	s.mu.Lock()
	path, ok := s.proofFiles[artifactID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("bucket fake: no proof %s: %w", artifactID, constants.ErrNotFound)
	}
	return os.Open(path)
}

// bucketFakeObject is one object the fake bucket holds.
type bucketFakeObject struct {
	body            []byte
	lastModified    time.Time
	contentType     string
	contentEncoding string
	cacheControl    string
}

// bucketFakeClient is an in-memory bucket. It records every Put and Delete in
// order and fails the keys it was told to fail.
type bucketFakeClient struct {
	mu        sync.Mutex
	clock     *bucketFakeClock
	objects   map[string]bucketFakeObject
	log       []string
	putErr    map[string]error
	deleteErr map[string]error
}

func newBucketFakeClient(clock *bucketFakeClock) *bucketFakeClient {
	return &bucketFakeClient{
		clock:     clock,
		objects:   map[string]bucketFakeObject{},
		putErr:    map[string]error{},
		deleteErr: map[string]error{},
	}
}

func (c *bucketFakeClient) Put(_ context.Context, put publicBucketPut) error {
	body, err := io.ReadAll(put.Body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, "put "+put.Key)
	if injected := c.putErr[put.Key]; injected != nil {
		return injected
	}
	// A bucket that verifies the digest rejects bytes that do not match it.
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != put.SHA256 {
		return fmt.Errorf("%w: put %s: digest mismatch", constants.ErrPublicBucketRequestFailed, put.Key)
	}
	c.objects[put.Key] = bucketFakeObject{
		body:            body,
		lastModified:    c.clock.Now(),
		contentType:     put.ContentType,
		contentEncoding: put.ContentEncoding,
		cacheControl:    put.CacheControl,
	}
	return nil
}

func (c *bucketFakeClient) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, "delete "+key)
	if injected := c.deleteErr[key]; injected != nil {
		return injected
	}
	delete(c.objects, key)
	return nil
}

func (c *bucketFakeClient) List(_ context.Context, prefix string) ([]publicBucketObject, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var listed []publicBucketObject
	for key, object := range c.objects {
		if strings.HasPrefix(key, prefix) {
			listed = append(listed, publicBucketObject{Key: key, LastModified: object.lastModified})
		}
	}
	slices.SortFunc(listed, func(a, b publicBucketObject) int { return strings.Compare(a.Key, b.Key) })
	return listed, nil
}

// seed places an object in the bucket as if an earlier publisher had written it.
func (c *bucketFakeClient) seed(key string, lastModified time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[key] = bucketFakeObject{body: []byte("seeded"), lastModified: lastModified}
}

func (c *bucketFakeClient) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.objects[key]
	return ok
}

func (c *bucketFakeClient) object(key string) (bucketFakeObject, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	object, ok := c.objects[key]
	return object, ok
}

func (c *bucketFakeClient) setPutErr(key string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putErr[key] = err
}

// logMark returns the current length of the request log, for slicing one tick's calls.
func (c *bucketFakeClient) logMark() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.log)
}

// logSince returns the calls recorded after mark.
func (c *bucketFakeClient) logSince(mark int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.log[mark:])
}

// bucketFakePurger records purged URLs and fails while fail is set.
type bucketFakePurger struct {
	mu   sync.Mutex
	fail bool
	urls []string
}

func (p *bucketFakePurger) Purge(_ context.Context, urls []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return fmt.Errorf("%w: injected: %d urls", constants.ErrPublicBucketPurgeFailed, len(urls))
	}
	p.urls = append(p.urls, urls...)
	return nil
}

func (p *bucketFakePurger) setFail(fail bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = fail
}

func (p *bucketFakePurger) purged() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.urls)
}

// bucketFakeSign signs nothing: Tier 1 tests check what the publisher writes,
// and signature checks belong to the Tier 2 tests.
func bucketFakeSign(pointer models.PublicFeedPointer) (models.PublicFeedPointer, error) {
	pointer.SigningKeyID = bucketFakeSigningKeyID
	return pointer, nil
}

// bucketHarness wires one publisher to the fakes over a shared runtime root, so a
// second publisher built later sees the first one's journal and bucket.
type bucketHarness struct {
	t       *testing.T
	dir     string
	fileSvc fs.RuntimeFileService
	clock   *bucketFakeClock
	source  *bucketFakeSource
	client  *bucketFakeClient
	purger  *bucketFakePurger
	cfg     models.PublicBucketConfig
}

func newBucketHarness(t *testing.T) *bucketHarness {
	t.Helper()
	dir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	clock := newBucketFakeClock()
	return &bucketHarness{
		t:       t,
		dir:     dir,
		fileSvc: fileSvc,
		clock:   clock,
		source:  newBucketFakeSource(),
		client:  newBucketFakeClient(clock),
		purger:  &bucketFakePurger{},
		cfg: models.PublicBucketConfig{
			Name:         bucketFakeBucketName,
			Endpoint:     "https://acct.r2.example",
			PublicOrigin: bucketFakeOrigin,
			ZoneID:       bucketFakeZoneID,
		},
	}
}

// newPublisher builds a publisher over the harness's fakes and fake clock. The
// journal is loaded from the harness root, so a publisher built after another
// one behaves like a restarted process.
func (h *bucketHarness) newPublisher() *PublicBucketPublisher {
	h.t.Helper()
	publisher, err := newPublicBucketPublisher(context.Background(), h.cfg, bucketFakeSourceID, h.source, bucketFakeSign, h.client, h.purger, h.fileSvc, testutil.NewTestLogger())
	require.NoError(h.t, err)
	publisher.now = h.clock.Now
	return publisher
}

// proof writes one proof artifact to disk and returns its catalog entry. The
// artifact ID is the content digest, as the mirror assigns it.
func (h *bucketHarness) proof(filename string, content []byte) models.PublicProofCatalogEntry {
	h.t.Helper()
	sum := sha256.Sum256(content)
	artifactID := hex.EncodeToString(sum[:])
	path := filepath.Join(h.dir, artifactID)
	require.NoError(h.t, os.WriteFile(path, content, 0o600))
	h.source.mu.Lock()
	h.source.proofFiles[artifactID] = path
	h.source.mu.Unlock()
	return models.PublicProofCatalogEntry{
		ArtifactID:          artifactID,
		Filename:            filename,
		MediaType:           "application/json",
		ByteSize:            int64(len(content)),
		SHA256:              artifactID,
		Classification:      models.PublicFeedProofClassificationPublicSafe,
		CampaignID:          "c1",
		GeneratedAt:         h.clock.Now(),
		VerificationCommand: "sha256sum " + filename,
		ImmutableURL:        constants.PublicLayoutProofPrefix + artifactID,
	}
}

// setProofRoot moves the source's proof root to the root of entries.
func (h *bucketHarness) setProofRoot(entries ...models.PublicProofCatalogEntry) {
	h.t.Helper()
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ArtifactID)
	}
	encoded, err := json.Marshal(entries)
	require.NoError(h.t, err)
	h.source.mu.Lock()
	defer h.source.mu.Unlock()
	h.source.head.ProofRootSHA256 = hashBytes([]byte(strings.Join(ids, ",")))
	h.source.catalog = PublicLayoutCatalog{SHA256: hashBytes(encoded), Gzip: encoded, Entries: entries}
}

func (h *bucketHarness) setReadModel(body string) {
	h.source.mu.Lock()
	defer h.source.mu.Unlock()
	h.source.readModel = []byte(body)
}

func (h *bucketHarness) setActiveRuns(runs ...models.PublicLiveRun) {
	h.source.mu.Lock()
	defer h.source.mu.Unlock()
	h.source.head.ActiveRuns = runs
}

func (h *bucketHarness) setHead(highWater int64) {
	h.source.mu.Lock()
	defer h.source.mu.Unlock()
	h.source.head.HighWaterSequence = highWater
	h.source.head.FeedChainHash = hashBytes([]byte(strconv.FormatInt(highWater, 10)))
}

func (h *bucketHarness) setWithdrawalDigest(digest string) {
	h.source.mu.Lock()
	defer h.source.mu.Unlock()
	h.source.head.WithdrawalDigest = digest
}

func (h *bucketHarness) setSegmentHead(head int64) {
	h.source.mu.Lock()
	defer h.source.mu.Unlock()
	h.source.segmentHead = head
	if head > 0 {
		h.source.chunks[1] = []byte("segment-chunk")
	}
}

// pointerInBucket decodes pointer.json as the bucket holds it.
func (h *bucketHarness) pointerInBucket() models.PublicFeedPointer {
	h.t.Helper()
	object, ok := h.client.object(constants.PublicLayoutPointerKey)
	require.True(h.t, ok, "pointer.json must be in the bucket")
	var pointer models.PublicFeedPointer
	require.NoError(h.t, json.Unmarshal(object.body, &pointer))
	return pointer
}

// putsIn returns the Put keys in a call log, in order.
func putsIn(calls []string) []string {
	var keys []string
	for _, call := range calls {
		if key, ok := strings.CutPrefix(call, "put "); ok {
			keys = append(keys, key)
		}
	}
	return keys
}

// countPuts counts the Put calls in a log whose key has prefix.
func countPuts(calls []string, prefix string) int {
	count := 0
	for _, key := range putsIn(calls) {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

func bucketFakeKeyURL(key string) string {
	return bucketFakeOrigin + "/" + key
}

func bucketFakeReadModelKey(readModel []byte) string {
	return constants.PublicLayoutReadModelPrefix + hashBytes(readModel) + constants.PublicLayoutJSONGzipSuffix
}

func bucketFakeSegmentKey(first int64) string {
	return constants.PublicLayoutProofSegmentsPrefix + strconv.FormatInt(first, 10) + constants.PublicLayoutJSONLGzipSuffix
}

func bucketFakeHistoryKey(first int64) string {
	return constants.PublicLayoutHistoryPrefix + strconv.FormatInt(first, 10) + constants.PublicLayoutJSONGzipSuffix
}

func TestBucketPublisher_PointerWrittenLast(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	first := h.proof("a.json", []byte(`{"artifact":"a"}`))
	second := h.proof("b.json", []byte(`{"artifact":"b"}`))
	h.setProofRoot(first, second)
	h.setSegmentHead(1)
	h.source.mu.Lock()
	h.source.proofHead = []byte(`{"head":"proof"}`)
	h.source.history[1] = []byte(`{"page":1}`)
	h.source.mu.Unlock()
	h.setHead(2)

	publisher := h.newPublisher()
	require.NoError(t, publisher.tick(ctx))

	puts := putsIn(h.client.logSince(0))
	require.NotEmpty(t, puts)
	pointerIndex := len(puts) - 1
	assert.Equal(t, constants.PublicLayoutPointerKey, puts[pointerIndex], "pointer.json must be the last Put of a sync")
	assert.Equal(t, 1, countPuts(h.client.logSince(0), constants.PublicLayoutPointerKey), "one pointer Put per sync")

	lastIndex := func(key string) int {
		return slices.Index(puts, key)
	}
	for _, key := range []string{
		constants.PublicLayoutProofPrefix + first.ArtifactID,
		constants.PublicLayoutProofPrefix + second.ArtifactID,
		constants.PublicLayoutProofMetaPrefix + first.ArtifactID + constants.PublicLayoutJSONSuffix,
		constants.PublicLayoutProofMetaPrefix + second.ArtifactID + constants.PublicLayoutJSONSuffix,
		bucketFakeSegmentKey(1),
		constants.PublicLayoutProofHeadKey,
		bucketFakeHistoryKey(1),
		bucketFakeReadModelKey(h.source.readModel),
	} {
		index := lastIndex(key)
		require.NotEqual(t, -1, index, "expected a Put of %s", key)
		assert.Less(t, index, pointerIndex, "%s must be written before pointer.json", key)
	}
	assert.Less(t, lastIndex(constants.PublicLayoutProofPrefix+first.ArtifactID), lastIndex(constants.PublicLayoutProofMetaPrefix+first.ArtifactID+constants.PublicLayoutJSONSuffix),
		"a proof-meta object must follow its artifact")
	assert.Less(t, lastIndex(constants.PublicLayoutProofPrefix+second.ArtifactID), lastIndex(constants.PublicLayoutProofMetaPrefix+second.ArtifactID+constants.PublicLayoutJSONSuffix),
		"a proof-meta object must follow its artifact")
	catalogPuts := countPuts(h.client.logSince(0), constants.PublicLayoutProofCatalogPrefix)
	assert.Equal(t, 1, catalogPuts, "a first sync writes the catalog")
	assert.Less(t, slices.IndexFunc(puts, func(key string) bool { return strings.HasPrefix(key, constants.PublicLayoutProofCatalogPrefix) }), pointerIndex)
}

func TestBucketPublisher_FailedUploadLeavesPointerUnmoved(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	keep := h.proof("keep.json", []byte(`{"keep":true}`))
	failing := h.proof("failing.json", []byte(`{"failing":true}`))
	h.setProofRoot(keep, failing)
	injected := errors.New("injected put failure")
	h.client.setPutErr(constants.PublicLayoutProofPrefix+failing.ArtifactID, injected)

	publisher := h.newPublisher()
	err := publisher.tick(ctx)
	require.ErrorIs(t, err, injected)
	assert.False(t, h.client.has(constants.PublicLayoutPointerKey), "a failed upload must not move pointer.json")
	assert.NotContains(t, h.client.logSince(0), "put "+constants.PublicLayoutPointerKey)

	h.client.setPutErr(constants.PublicLayoutProofPrefix+failing.ArtifactID, nil)
	mark := h.client.logMark()
	require.NoError(t, publisher.tick(ctx))

	retry := h.client.logSince(mark)
	assert.Contains(t, retry, "put "+constants.PublicLayoutProofPrefix+failing.ArtifactID, "the retry uploads the missing artifact")
	assert.Contains(t, retry, "put "+constants.PublicLayoutProofMetaPrefix+failing.ArtifactID+constants.PublicLayoutJSONSuffix)
	assert.Zero(t, countPuts(retry, constants.PublicLayoutProofPrefix+keep.ArtifactID), "the artifact already in the bucket is not uploaded again")
	assert.Zero(t, countPuts(retry, constants.PublicLayoutProofMetaPrefix+keep.ArtifactID))
	assert.True(t, h.client.has(constants.PublicLayoutPointerKey), "the retry writes the pointer")
}

func TestBucketPublisher_ResumesFromBucketListing(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	first := h.proof("one.json", []byte(`{"one":1}`))
	second := h.proof("two.json", []byte(`{"two":2}`))
	h.setProofRoot(first, second)
	require.NoError(t, h.newPublisher().tick(ctx))

	mark := h.client.logMark()
	restarted := h.newPublisher()
	require.NoError(t, restarted.tick(ctx))

	calls := h.client.logSince(mark)
	assert.Zero(t, countPuts(calls, constants.PublicLayoutProofPrefix), "objects the bucket already lists are not uploaded again")
	assert.Zero(t, countPuts(calls, constants.PublicLayoutProofMetaPrefix))
	assert.True(t, h.client.has(constants.PublicLayoutPointerKey))
}

func TestBucketPublisher_RevisionAdvancesAcrossRestart(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	require.NoError(t, h.newPublisher().tick(ctx))
	journaled := h.pointerInBucket().Revision
	require.NotZero(t, journaled)

	status, err := ReadPublicBucketStatus(ctx, h.fileSvc, models.PublicExportConfig{Bucket: &h.cfg})
	require.NoError(t, err)
	require.Equal(t, journaled, status.PointerRevision)

	h.clock.Set(h.clock.Now().Add(-time.Hour))
	require.NoError(t, h.newPublisher().tick(ctx))

	restarted := h.pointerInBucket().Revision
	assert.Greater(t, restarted, journaled, "a restarted publisher must write a revision above the journal's, even with the clock behind")
}

func TestBucketPublisher_CoalescesPointerDuringRun(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	run := models.PublicLiveRun{DatasetID: "ds-1", RunID: "run-1", LifecycleState: "running"}
	h.setActiveRuns(run)
	publisher := h.newPublisher()

	const ticks = 7 // one per 10 seconds, spanning exactly one minute after the first
	for index := range ticks {
		h.setHead(int64(index + 1))
		require.NoError(t, publisher.tick(ctx))
		h.clock.Advance(publicBucketTickInterval)
	}

	calls := h.client.logSince(0)
	pointerPuts := countPuts(calls, constants.PublicLayoutPointerKey)
	assert.Equal(t, 2, pointerPuts, "a pointer is written at the first tick and once more a minute later")
	h.source.mu.Lock()
	stateCalls := h.source.layoutStateCalls
	h.source.mu.Unlock()
	assert.Equal(t, pointerPuts, stateCalls, "LayoutState runs only when a pointer write is due")
}

func TestBucketPublisher_SettleWritesImmediatelyAndRewritesCatalog(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	run := models.PublicLiveRun{DatasetID: "ds-1", RunID: "run-1", LifecycleState: "running"}
	first := h.proof("first.json", []byte(`{"first":1}`))
	second := h.proof("second.json", []byte(`{"second":2}`))
	h.setActiveRuns(run)
	h.setProofRoot(first)
	publisher := h.newPublisher()
	require.NoError(t, publisher.tick(ctx))
	require.Equal(t, 1, countPuts(h.client.logSince(0), constants.PublicLayoutProofCatalogPrefix))

	h.clock.Advance(time.Minute)
	h.setProofRoot(first, second)
	h.setHead(2)
	mark := h.client.logMark()
	require.NoError(t, publisher.tick(ctx))
	duringRun := h.client.logSince(mark)
	assert.Zero(t, countPuts(duringRun, constants.PublicLayoutProofCatalogPrefix), "a catalog is not rewritten while a run is active")
	assert.Equal(t, constants.PublicLayoutPointerKey, putsIn(duringRun)[len(putsIn(duringRun))-1], "the pointer still moves during the run")

	h.clock.Advance(publicBucketTickInterval)
	h.setActiveRuns()
	mark = h.client.logMark()
	require.NoError(t, publisher.tick(ctx))
	settle := h.client.logSince(mark)
	settlePuts := putsIn(settle)
	require.Equal(t, 1, countPuts(settle, constants.PublicLayoutProofCatalogPrefix), "the catalog is written on the tick the run settles")
	catalogKey := constants.PublicLayoutProofCatalogPrefix + hashBytes(mustJSONForBucket(t, []models.PublicProofCatalogEntry{first, second})) + constants.PublicLayoutJSONGzipSuffix
	assert.Contains(t, settlePuts, catalogKey)
	assert.Less(t, slices.Index(settlePuts, catalogKey), len(settlePuts)-1)
	assert.Equal(t, constants.PublicLayoutPointerKey, settlePuts[len(settlePuts)-1])
}

// mustJSONForBucket encodes value the way the harness encodes the catalog.
func mustJSONForBucket(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func TestBucketPublisher_IdleProofChangeRewritesCatalog(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	first := h.proof("first.json", []byte(`{"first":1}`))
	second := h.proof("second.json", []byte(`{"second":2}`))
	h.setProofRoot(first)
	publisher := h.newPublisher()
	require.NoError(t, publisher.tick(ctx))

	h.clock.Advance(time.Minute)
	h.setProofRoot(first, second)
	mark := h.client.logMark()
	require.NoError(t, publisher.tick(ctx))

	calls := putsIn(h.client.logSince(mark))
	require.Equal(t, 1, countPuts(h.client.logSince(mark), constants.PublicLayoutProofCatalogPrefix), "with no active run the catalog is written with the next pointer")
	assert.Equal(t, constants.PublicLayoutPointerKey, calls[len(calls)-1])
	pointer := h.pointerInBucket()
	catalogKey := constants.PublicLayoutProofCatalogPrefix + pointer.ProofCatalogSHA256 + constants.PublicLayoutJSONGzipSuffix
	assert.Contains(t, calls, catalogKey, "the pointer names the catalog it was written with")
	assert.True(t, h.client.has(catalogKey), "the pointer's catalog is in the bucket")
}

func TestBucketPublisher_StatusWrittenOnlyOnChange(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	run := models.PublicLiveRun{DatasetID: "ds-1", RunID: "run-1", LifecycleState: "running"}
	generatedAt := h.clock.Now()
	setStatus := func(runs []models.PublicLiveRun, at time.Time) {
		h.source.mu.Lock()
		defer h.source.mu.Unlock()
		h.source.liveStatus = models.PublicLiveStatus{
			SchemaVersion: constants.PublicFeedPointerSchemaVersion,
			Snapshot:      models.PublicFeedSnapshot{GeneratedAt: at},
			ActiveRuns:    runs,
		}
	}
	publisher := h.newPublisher()

	setStatus(nil, generatedAt)
	require.NoError(t, publisher.tick(ctx))
	assert.Zero(t, countPuts(h.client.logSince(0), constants.PublicLayoutLiveStatusKey), "an idle quiet start writes no status")

	h.setActiveRuns(run)
	setStatus([]models.PublicLiveRun{run}, generatedAt)
	require.NoError(t, publisher.tick(ctx))
	require.Equal(t, 1, countPuts(h.client.logSince(0), constants.PublicLayoutLiveStatusKey), "the first active tick writes the status")

	h.clock.Advance(publicBucketTickInterval)
	setStatus([]models.PublicLiveRun{run}, h.clock.Now())
	require.NoError(t, publisher.tick(ctx))
	assert.Equal(t, 1, countPuts(h.client.logSince(0), constants.PublicLayoutLiveStatusKey), "a change to generated_at alone does not rewrite the status")

	h.clock.Advance(publicBucketTickInterval)
	h.setActiveRuns()
	setStatus(nil, h.clock.Now())
	require.NoError(t, publisher.tick(ctx))
	assert.Equal(t, 2, countPuts(h.client.logSince(0), constants.PublicLayoutLiveStatusKey), "the settle writes the status once more with no active runs")

	h.clock.Advance(publicBucketTickInterval)
	setStatus(nil, h.clock.Now())
	require.NoError(t, publisher.tick(ctx))
	assert.Equal(t, 2, countPuts(h.client.logSince(0), constants.PublicLayoutLiveStatusKey), "after the settle the status is not written again")
}

func TestBucketPublisher_SettledRunLiveBundleDeleted(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	run := models.PublicLiveRun{DatasetID: "ds-1", RunID: "run-settle", LifecycleState: "running"}
	h.source.mu.Lock()
	h.source.liveBundles[run.RunID] = []byte(`{"bundle":"live"}`)
	h.source.mu.Unlock()
	h.setActiveRuns(run)
	publisher := h.newPublisher()
	require.NoError(t, publisher.tick(ctx))
	require.True(t, h.client.has(liveRunKey(run.RunID)), "an active run's bundle is published")

	h.clock.Advance(time.Minute)
	h.setActiveRuns()
	require.NoError(t, publisher.tick(ctx))

	assert.Contains(t, h.client.logSince(0), "delete "+liveRunKey(run.RunID), "a settled run's bundle is deleted")
	assert.False(t, h.client.has(liveRunKey(run.RunID)))
	status, err := ReadPublicBucketStatus(ctx, h.fileSvc, models.PublicExportConfig{Bucket: &h.cfg})
	require.NoError(t, err)
	assert.Zero(t, status.PendingDeletes, "a completed delete leaves no journal entry")
}

func TestBucketPublisher_LiveBundleRefreshesAtMostOncePerMinute(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	run := models.PublicLiveRun{DatasetID: "ds-1", RunID: "run-refresh", LifecycleState: "running"}
	h.source.mu.Lock()
	h.source.liveBundles[run.RunID] = []byte(`{"bundle":"v1"}`)
	h.source.mu.Unlock()
	h.setActiveRuns(run)
	publisher := h.newPublisher()
	require.NoError(t, publisher.tick(ctx))

	h.clock.Advance(publicBucketTickInterval)
	h.source.mu.Lock()
	h.source.liveBundles[run.RunID] = []byte(`{"bundle":"v2"}`)
	h.source.mu.Unlock()
	require.NoError(t, publisher.tick(ctx))
	h.source.mu.Lock()
	assert.Equal(t, 1, h.source.liveBundleCalls, "a bundle within a minute of the last refresh is not read again")
	h.source.mu.Unlock()
	assert.Equal(t, 1, countPuts(h.client.logSince(0), liveRunKey(run.RunID)))

	h.clock.Advance(publicBucketTickInterval * 5)
	require.NoError(t, publisher.tick(ctx))
	h.source.mu.Lock()
	assert.Equal(t, 2, h.source.liveBundleCalls)
	h.source.mu.Unlock()
	object, ok := h.client.object(liveRunKey(run.RunID))
	require.True(t, ok)
	assert.Equal(t, []byte(`{"bundle":"v2"}`), object.body, "the refreshed bundle replaces the earlier one")
	assert.Equal(t, 2, countPuts(h.client.logSince(0), liveRunKey(run.RunID)))
}

// bucketWithdrawal is a withdrawal fixture: the artifact withdrawn, and the read
// model and catalog keys the bucket held before the withdrawal.
type bucketWithdrawal struct {
	artifactID      string
	oldReadModelKey string
	oldCatalogKey   string
}

// bucketWithdrawalSetup publishes two artifacts with one tick, then withdraws the
// first with a new read model and catalog. The publisher's next tick carries the
// withdrawal.
func bucketWithdrawalSetup(t *testing.T, h *bucketHarness, publisher *PublicBucketPublisher) bucketWithdrawal {
	t.Helper()
	ctx := t.Context()
	withdrawn := h.proof("withdrawn.json", []byte(`{"withdrawn":true}`))
	kept := h.proof("kept.json", []byte(`{"kept":true}`))
	h.setReadModel(`{"read":"model-0"}`)
	h.setProofRoot(withdrawn, kept)
	h.setHead(2)
	h.source.mu.Lock()
	h.source.history[1] = []byte(`{"page":"one"}`)
	h.source.historyFirst = 1
	h.source.mu.Unlock()
	require.NoError(t, publisher.tick(ctx))
	before := h.pointerInBucket()
	h.clock.Advance(time.Minute)

	h.setWithdrawalDigest(hashBytes([]byte("withdrawal-1")))
	h.setReadModel(`{"read":"model-1"}`)
	h.setProofRoot(kept)
	return bucketWithdrawal{
		artifactID:      withdrawn.ArtifactID,
		oldReadModelKey: constants.PublicLayoutReadModelPrefix + before.ReadModelSHA256 + constants.PublicLayoutJSONGzipSuffix,
		oldCatalogKey:   constants.PublicLayoutProofCatalogPrefix + before.ProofCatalogSHA256 + constants.PublicLayoutJSONGzipSuffix,
	}
}

func TestBucketPublisher_WithdrawalDeletesAndPurges(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	publisher := h.newPublisher()
	withdrawal := bucketWithdrawalSetup(t, h, publisher)
	require.True(t, h.client.has(withdrawal.oldReadModelKey), "the earlier read model is in the bucket before the withdrawal")
	require.True(t, h.client.has(withdrawal.oldCatalogKey), "the earlier catalog is in the bucket before the withdrawal")
	statusMark := h.client.logMark()

	require.NoError(t, publisher.tick(ctx))

	assert.False(t, h.client.has(constants.PublicLayoutProofPrefix+withdrawal.artifactID), "the withdrawn artifact is deleted from proofs/")
	assert.False(t, h.client.has(constants.PublicLayoutProofMetaPrefix+withdrawal.artifactID+constants.PublicLayoutJSONSuffix), "the withdrawn artifact's proof-meta is deleted")
	assert.False(t, h.client.has(withdrawal.oldReadModelKey), "an earlier read model is deleted with no grace")
	assert.False(t, h.client.has(withdrawal.oldCatalogKey), "an earlier catalog is deleted with no grace")
	assert.True(t, h.client.has(bucketFakeReadModelKey([]byte(`{"read":"model-1"}`))), "the current read model stays")

	calls := h.client.logSince(statusMark)
	assert.Equal(t, 1, countPuts(calls, constants.PublicLayoutLiveStatusKey), "the withdrawal rewrites the status")

	purged := h.purger.purged()
	for _, url := range []string{
		bucketFakeKeyURL(constants.PublicLayoutProofPrefix + withdrawal.artifactID),
		bucketFakeKeyURL(constants.PublicLayoutProofMetaPrefix + withdrawal.artifactID + constants.PublicLayoutJSONSuffix),
		bucketFakeKeyURL(constants.PublicLayoutPointerKey),
		bucketFakeKeyURL(constants.PublicLayoutProofHeadKey),
		bucketFakeKeyURL(withdrawal.oldReadModelKey),
		bucketFakeKeyURL(withdrawal.oldCatalogKey),
	} {
		assert.Contains(t, purged, url)
	}
	pointer := h.pointerInBucket()
	assert.Equal(t, hashBytes([]byte("withdrawal-1")), pointer.WithdrawalDigest)

	status, err := ReadPublicBucketStatus(ctx, h.fileSvc, models.PublicExportConfig{Bucket: &h.cfg})
	require.NoError(t, err)
	assert.Zero(t, status.PendingDeletes)
	assert.Zero(t, status.PendingPurges)
}

func TestBucketPublisher_PurgeFailureStaysInJournalAndRetries(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	publisher := h.newPublisher()
	withdrawal := bucketWithdrawalSetup(t, h, publisher)

	h.purger.setFail(true)
	err := publisher.tick(ctx)
	require.ErrorIs(t, err, constants.ErrPublicBucketPurgeFailed)
	assert.False(t, h.client.has(constants.PublicLayoutProofPrefix+withdrawal.artifactID), "the delete happens even when the purge fails")

	pending, err := ReadPublicBucketStatus(ctx, h.fileSvc, models.PublicExportConfig{Bucket: &h.cfg})
	require.NoError(t, err)
	require.NotZero(t, pending.PendingPurges, "a failed purge stays pending in the journal")

	reloaded := h.newPublisher()
	h.purger.setFail(false)
	require.NoError(t, reloaded.tick(ctx))

	cleared, err := ReadPublicBucketStatus(ctx, h.fileSvc, models.PublicExportConfig{Bucket: &h.cfg})
	require.NoError(t, err)
	assert.Zero(t, cleared.PendingPurges, "a later tick clears the purge")
	assert.Contains(t, h.purger.purged(), bucketFakeKeyURL(constants.PublicLayoutPointerKey))
}

func TestBucketPublisher_WithdrawalWhileDownIsHandledOnRestart(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	first := h.proof("gone.json", []byte(`{"gone":true}`))
	second := h.proof("stay.json", []byte(`{"stay":true}`))
	h.setWithdrawalDigest(hashBytes([]byte("withdrawal-a")))
	h.setProofRoot(first, second)
	require.NoError(t, h.newPublisher().tick(ctx))
	require.True(t, h.client.has(constants.PublicLayoutProofPrefix+first.ArtifactID))

	// The publisher is down while the source records a withdrawal.
	h.clock.Advance(time.Hour)
	h.setWithdrawalDigest(hashBytes([]byte("withdrawal-b")))
	h.setProofRoot(second)
	restarted := h.newPublisher()
	require.NoError(t, restarted.tick(ctx))

	assert.False(t, h.client.has(constants.PublicLayoutProofPrefix+first.ArtifactID), "a withdrawal that landed while down is deleted on restart")
	assert.False(t, h.client.has(constants.PublicLayoutProofMetaPrefix+first.ArtifactID+constants.PublicLayoutJSONSuffix))
	assert.True(t, h.client.has(constants.PublicLayoutProofPrefix+second.ArtifactID))
	assert.Equal(t, hashBytes([]byte("withdrawal-b")), h.pointerInBucket().WithdrawalDigest)
	assert.Contains(t, h.purger.purged(), bucketFakeKeyURL(constants.PublicLayoutProofPrefix+first.ArtifactID))
}

func TestBucketPublisher_SweepHonorsGrace(t *testing.T) {
	h := newBucketHarness(t)
	ctx := t.Context()
	h.source.mu.Lock()
	h.source.historyFirst = 2001
	h.source.mu.Unlock()
	publisher := h.newPublisher()
	require.NoError(t, publisher.tick(ctx))
	now := h.clock.Now()
	current := bucketFakeReadModelKey(h.source.readModel)
	require.True(t, h.client.has(current))

	stale := constants.PublicLayoutReadModelPrefix + hashBytes([]byte("stale")) + constants.PublicLayoutJSONGzipSuffix
	young := constants.PublicLayoutReadModelPrefix + hashBytes([]byte("young")) + constants.PublicLayoutJSONGzipSuffix
	staleHistory := bucketFakeHistoryKey(1)
	h.client.seed(stale, now.Add(-3*time.Hour))
	h.client.seed(young, now.Add(90*time.Minute))
	h.client.seed(staleHistory, now.Add(-3*time.Hour))

	// The sweep runs on the first tick an hour after the last one, with a
	// cutoff an hour before the new time.
	h.clock.Advance(2 * time.Hour)
	require.NoError(t, publisher.tick(ctx))

	assert.False(t, h.client.has(stale), "an unreferenced read model older than the grace is deleted")
	assert.True(t, h.client.has(young), "an unreferenced read model younger than the grace stays")
	assert.True(t, h.client.has(current), "the current read model always stays, however old")
	assert.False(t, h.client.has(staleHistory), "a history page below the first retained sequence is deleted")
	assert.Empty(t, h.purger.purged(), "a grace sweep without a withdrawal purges nothing")
}

// bucketJournalFileSvc returns a runtime file service with an empty journal root.
func bucketJournalFileSvc(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(t.Context()))
	return fileSvc
}

// bucketJournalLine encodes one journal entry as a complete line.
func bucketJournalLine(t *testing.T, entry models.PublicBucketJournalEntry) string {
	t.Helper()
	encoded, err := json.Marshal(entry)
	require.NoError(t, err)
	return string(encoded) + "\n"
}

func TestBucketJournal_DropsIncompleteTail(t *testing.T) {
	ctx := t.Context()
	fileSvc := bucketJournalFileSvc(t)
	complete := bucketJournalLine(t, models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: "read-model/old.json.gz"})
	torn := `{"kind":"delete","key":"read-model/tor`
	require.NoError(t, fileSvc.WriteFile(ctx, constants.PublicFeedBucketJournalPath, []byte(complete+torn), constants.PermFilePrivate))

	journal, err := loadPublicBucketJournal(ctx, fileSvc)
	require.NoError(t, err)
	assert.Equal(t, []string{"read-model/old.json.gz"}, journal.deletes, "a final line with no newline is an append that never finished")

	data, err := fileSvc.ReadFile(ctx, constants.PublicFeedBucketJournalPath)
	require.NoError(t, err)
	assert.Equal(t, complete, string(data), "the incomplete tail is removed from the file")
}

func TestBucketJournal_CorruptLineFailsClosed(t *testing.T) {
	valid := bucketJournalLine(t, models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: "read-model/a.json.gz"})
	tests := []struct {
		name string
		body string
	}{
		{name: "unparseable line", body: valid + "not json\n"},
		{name: "unknown entry kind", body: valid + `{"kind":"rewrite","key":"x"}` + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			fileSvc := bucketJournalFileSvc(t)
			require.NoError(t, fileSvc.WriteFile(ctx, constants.PublicFeedBucketJournalPath, []byte(test.body), constants.PermFilePrivate))

			_, err := loadPublicBucketJournal(ctx, fileSvc)
			require.ErrorIs(t, err, constants.ErrPublicBucketJournalCorrupt)
			_, err = ReadPublicBucketStatus(ctx, fileSvc, models.PublicExportConfig{Bucket: &models.PublicBucketConfig{Name: bucketFakeBucketName}})
			require.ErrorIs(t, err, constants.ErrPublicBucketJournalCorrupt, "the status read fails closed too")
		})
	}
}

func TestBucketJournal_CompactionKeepsLiveEntries(t *testing.T) {
	ctx := t.Context()
	fileSvc := bucketJournalFileSvc(t)
	journal, err := loadPublicBucketJournal(ctx, fileSvc)
	require.NoError(t, err)
	require.NoError(t, journal.record(ctx,
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPointer, Revision: 5, Sequence: 9, WithdrawalDigest: hashBytes([]byte("w"))},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: "proofs/a"},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: "proofs/b"},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDeleted, Key: "proofs/a"},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPurge, Key: "https://feed.example/pointer.json"},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPurge, Key: "https://feed.example/proof-head.json"},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPurged, Key: "https://feed.example/pointer.json"},
	))
	before, err := fileSvc.ReadFile(ctx, constants.PublicFeedBucketJournalPath)
	require.NoError(t, err)

	require.NoError(t, journal.compact(ctx))

	after, err := fileSvc.ReadFile(ctx, constants.PublicFeedBucketJournalPath)
	require.NoError(t, err)
	assert.Less(t, len(after), len(before), "compaction drops the cleared entries")
	reloaded, err := loadPublicBucketJournal(ctx, fileSvc)
	require.NoError(t, err)
	assert.Equal(t, int64(5), reloaded.revision)
	assert.Equal(t, int64(9), reloaded.sequence)
	assert.Equal(t, hashBytes([]byte("w")), reloaded.withdrawal)
	assert.Equal(t, []string{"proofs/b"}, reloaded.deletes)
	assert.Equal(t, []string{"https://feed.example/proof-head.json"}, reloaded.purges)
}

func TestReadPublicBucketStatus_NilWhenOff(t *testing.T) {
	fileSvc := bucketJournalFileSvc(t)

	status, err := ReadPublicBucketStatus(t.Context(), fileSvc, models.PublicExportConfig{})
	require.NoError(t, err)
	assert.Nil(t, status, "without a bucket configured there is no bucket status")
}

func TestReadPublicBucketStatus_ReportsPending(t *testing.T) {
	ctx := t.Context()
	fileSvc := bucketJournalFileSvc(t)
	journal, err := loadPublicBucketJournal(ctx, fileSvc)
	require.NoError(t, err)
	require.NoError(t, journal.record(ctx,
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPointer, Revision: 42, Sequence: 17},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: "proofs/a"},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: "proofs/b"},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPurge, Key: "https://feed.example/pointer.json"},
	))

	status, err := ReadPublicBucketStatus(ctx, fileSvc, models.PublicExportConfig{Bucket: &models.PublicBucketConfig{Name: bucketFakeBucketName}})
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, bucketFakeBucketName, status.Name)
	assert.Equal(t, int64(42), status.PointerRevision)
	assert.Equal(t, int64(17), status.PointerSequence)
	assert.Equal(t, 2, status.PendingDeletes)
	assert.Equal(t, 1, status.PendingPurges)
}
