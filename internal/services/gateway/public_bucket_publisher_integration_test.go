//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// bucketIntegrationEnv is a real mirror holding one record and one proof
// artifact, with the bucket fixture standing in for the external bucket.
type bucketIntegrationEnv struct {
	env     *mirrorTestEnv
	fixture *bucketS3Fixture
	clock   *bucketFakeClock
	proof   models.PublicProofCatalogEntry
	segment models.PublicProofManifestSegment
}

func newBucketIntegrationEnv(t *testing.T) *bucketIntegrationEnv {
	t.Helper()
	ctx := t.Context()
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{layoutProjection(env, 1)}
	_, accepted := env.sendIngest(env.buildBatch(records, constants.PublicFeedZeroHashHex))
	require.True(t, accepted.Accepted)

	content := []byte(`{"proof":"bucket-integration"}`)
	entry := makeTestCatalogEntry("bucket.json", content)
	segment := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
	require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{
		SourceID: env.sourceID, Segment: &segment,
		Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}},
	}))
	return &bucketIntegrationEnv{env: env, fixture: newBucketS3Fixture(t), clock: newBucketFakeClock(), proof: entry, segment: segment}
}

// publisher returns a real publisher writing to the fixture with creds. Pointers
// are signed with the mirror's source key, as the Gateway signs them. Its clock is
// b.clock, so a test moves time past the pointer interval explicitly.
func (b *bucketIntegrationEnv) publisher(t *testing.T, creds models.PublicBucketCredentials) *PublicBucketPublisher {
	t.Helper()
	sign := func(pointer models.PublicFeedPointer) (models.PublicFeedPointer, error) {
		return models.SignPublicFeedPointer(pointer, b.env.priv, models.PublicProofSigningKeyID(b.env.pub))
	}
	publisher, err := newPublicBucketPublisher(t.Context(), b.fixture.bucketConfig(), b.env.sourceID, b.env.mirror, sign,
		b.fixture.newClient(creds), b.fixture.newPurger(creds), b.env.fileSvc, testutil.NewTestLogger())
	require.NoError(t, err)
	publisher.now = b.clock.Now
	return publisher
}

// pointer decodes pointer.json as the bucket holds it.
func (b *bucketIntegrationEnv) pointer(t *testing.T) models.PublicFeedPointer {
	t.Helper()
	object, ok := b.fixture.object(constants.PublicLayoutPointerKey)
	require.True(t, ok, "pointer.json must be in the bucket")
	var pointer models.PublicFeedPointer
	require.NoError(t, json.Unmarshal(object.body, &pointer))
	return pointer
}

// requireBucketObject fetches an object and fails when it is absent.
func requireBucketObject(t *testing.T, fixture *bucketS3Fixture, key string) bucketS3Object {
	t.Helper()
	object, ok := fixture.object(key)
	require.True(t, ok, "%s must be in the bucket", key)
	return object
}

func TestBucketPublisher_PartialSyncNeverMovesPointer(t *testing.T) {
	b := newBucketIntegrationEnv(t)
	ctx := t.Context()
	creds := b.fixture.credentials()
	publisher := b.publisher(t, creds)
	b.fixture.failPut(constants.PublicLayoutProofPrefix+b.proof.ArtifactID, 1)

	err := publisher.tick(ctx)
	require.ErrorIs(t, err, constants.ErrPublicBucketRequestFailed)
	_, ok := b.fixture.object(constants.PublicLayoutPointerKey)
	assert.False(t, ok, "a sync with a failed PUT must leave pointer.json absent")

	require.NoError(t, publisher.tick(ctx))
	pointer := b.pointer(t)
	requireBucketObject(t, b.fixture, constants.PublicLayoutReadModelPrefix+pointer.ReadModelSHA256+constants.PublicLayoutJSONGzipSuffix)
	requireBucketObject(t, b.fixture, constants.PublicLayoutProofPrefix+b.proof.ArtifactID)
	requireBucketObject(t, b.fixture, constants.PublicLayoutProofMetaPrefix+b.proof.ArtifactID+constants.PublicLayoutJSONSuffix)
	requireBucketObject(t, b.fixture, constants.PublicLayoutProofCatalogPrefix+pointer.ProofCatalogSHA256+constants.PublicLayoutJSONGzipSuffix)
	requireBucketObject(t, b.fixture, bucketFakeSegmentKey(1))
}

func TestBucketPublisher_WrongSecretIsRejected(t *testing.T) {
	b := newBucketIntegrationEnv(t)
	creds := b.fixture.credentials()
	creds.SecretAccessKey = "wrong-secret-value-0001"
	publisher := b.publisher(t, creds)

	err := publisher.tick(t.Context())
	require.ErrorIs(t, err, constants.ErrPublicBucketRequestFailed)
	assert.Empty(t, b.fixture.keys(""), "a request signed with the wrong secret must write nothing")
}

func TestBucketPublisher_ObjectHeaders(t *testing.T) {
	b := newBucketIntegrationEnv(t)
	ctx := t.Context()
	publisher := b.publisher(t, b.fixture.credentials())
	require.NoError(t, publisher.tick(ctx))
	pointer := b.pointer(t)

	for _, key := range b.fixture.keys("") {
		object := requireBucketObject(t, b.fixture, key)
		assert.NotEmpty(t, object.cacheControl, "%s must carry a Cache-Control header", key)
	}

	readModel := requireBucketObject(t, b.fixture, constants.PublicLayoutReadModelPrefix+pointer.ReadModelSHA256+constants.PublicLayoutJSONGzipSuffix)
	assert.Equal(t, constants.HeaderValueApplicationJSON, readModel.contentType)
	assert.Equal(t, constants.HeaderValueGzip, readModel.contentEncoding)
	assert.Equal(t, constants.PublicLayoutCacheImmutable, readModel.cacheControl)
	plain := gunzipLayout(t, readModel.body)
	digest := sha256.Sum256(plain)
	assert.Equal(t, pointer.ReadModelSHA256, hex.EncodeToString(digest[:]), "the pointer's digest covers the decoded read model")

	catalog, err := b.env.mirror.LayoutProofCatalog(ctx, b.env.sourceID)
	require.NoError(t, err)
	require.Len(t, catalog.Entries, 1)
	proof := requireBucketObject(t, b.fixture, constants.PublicLayoutProofPrefix+b.proof.ArtifactID)
	assert.Equal(t, catalog.Entries[0].MediaType, proof.contentType, "a proof is served with its catalog media type")
	assert.Empty(t, proof.contentEncoding, "a proof is stored as is, without Content-Encoding")
}

func TestBucketPublisher_DownloadedFilesVerify(t *testing.T) {
	b := newBucketIntegrationEnv(t)
	ctx := t.Context()
	publisher := b.publisher(t, b.fixture.credentials())
	require.NoError(t, publisher.tick(ctx))

	// Download the pointer and verify its signature under the source key.
	pointer := b.pointer(t)
	require.NoError(t, models.VerifyPublicFeedPointer(pointer, b.env.pub))

	// Join the proof-segments chunks in first-index order and verify the chain.
	type chunk struct {
		first int64
		key   string
	}
	var chunks []chunk
	for _, key := range b.fixture.keys(constants.PublicLayoutProofSegmentsPrefix) {
		name := strings.TrimPrefix(key, constants.PublicLayoutProofSegmentsPrefix)
		first, err := strconv.ParseInt(strings.TrimSuffix(name, constants.PublicLayoutJSONLGzipSuffix), 10, 64)
		require.NoError(t, err)
		chunks = append(chunks, chunk{first: first, key: key})
	}
	slices.SortFunc(chunks, func(a, b chunk) int { return int(a.first - b.first) })
	require.NotEmpty(t, chunks)
	var joined []byte
	for _, c := range chunks {
		joined = append(joined, requireBucketObject(t, b.fixture, c.key).body...)
	}
	lines := strings.Split(strings.TrimSpace(string(gunzipLayout(t, joined))), "\n")
	require.Len(t, lines, int(pointer.ProofSegmentHead))
	previous := constants.PublicFeedZeroHashHex
	for _, line := range lines {
		var segment models.PublicProofManifestSegment
		require.NoError(t, json.Unmarshal([]byte(line), &segment))
		require.NoError(t, models.VerifyPublicProofSegment(b.env.sourceID, segment, b.env.pub))
		assert.Equal(t, previous, segment.PreviousRoot, "each segment links to the one before it")
		previous = segment.ProofRootSHA256
	}
	assert.Equal(t, pointer.ProofRootSHA256, previous, "the chain ends at the pointer's proof root")

	// The catalog decodes to the digest the pointer names.
	catalog := gunzipLayout(t, requireBucketObject(t, b.fixture, constants.PublicLayoutProofCatalogPrefix+pointer.ProofCatalogSHA256+constants.PublicLayoutJSONGzipSuffix).body)
	catalogDigest := sha256.Sum256(catalog)
	assert.Equal(t, pointer.ProofCatalogSHA256, hex.EncodeToString(catalogDigest[:]))

	// Each listed artifact downloads and hashes to its own ID.
	metaKeys := b.fixture.keys(constants.PublicLayoutProofMetaPrefix)
	require.Len(t, metaKeys, 1)
	var entry models.PublicProofCatalogEntry
	require.NoError(t, json.Unmarshal(requireBucketObject(t, b.fixture, metaKeys[0]).body, &entry))
	proof := requireBucketObject(t, b.fixture, constants.PublicLayoutProofPrefix+entry.ArtifactID)
	proofDigest := sha256.Sum256(proof.body)
	assert.Equal(t, entry.ArtifactID, hex.EncodeToString(proofDigest[:]))
}

func TestBucketPublisher_WithdrawalFlowRetriesFailingPurge(t *testing.T) {
	b := newBucketIntegrationEnv(t)
	ctx := t.Context()
	creds := b.fixture.credentials()
	publisher := b.publisher(t, creds)
	require.NoError(t, publisher.tick(ctx))

	// A dataset withdrawal moves the withdrawal digest, which is the signal that
	// purges the pointer. The artifact withdrawal removes the proof objects.
	withdrawal := makeTestSegment(b.env, 2, b.segment.ProofRootSHA256, nil, []string{b.proof.ArtifactID})
	require.NoError(t, b.env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: b.env.sourceID, Segment: &withdrawal}))
	require.NoError(t, b.env.mirror.WithdrawCampaignDataset(ctx, "run-withdrawn"))
	b.clock.Advance(time.Minute)

	b.fixture.setPurgeFail(true)
	err := publisher.tick(ctx)
	require.ErrorIs(t, err, constants.ErrPublicBucketPurgeFailed)
	assert.Empty(t, b.fixture.keys(constants.PublicLayoutProofPrefix+b.proof.ArtifactID), "the withdrawn artifact is deleted before the purge")
	pending, err := ReadPublicBucketStatus(ctx, b.env.fileSvc, models.PublicExportConfig{Bucket: ptrTo(b.fixture.bucketConfig())})
	require.NoError(t, err)
	require.NotZero(t, pending.PendingPurges, "the failed purge stays owed in the journal")

	b.fixture.setPurgeFail(false)
	require.NoError(t, publisher.tick(ctx))
	cleared, err := ReadPublicBucketStatus(ctx, b.env.fileSvc, models.PublicExportConfig{Bucket: ptrTo(b.fixture.bucketConfig())})
	require.NoError(t, err)
	assert.Zero(t, cleared.PendingPurges)
	assert.Contains(t, b.fixture.purged(), bucketS3TestPublicHost+"/"+constants.PublicLayoutPointerKey)
}

func TestBucketPublisher_RunStopsOnCancel(t *testing.T) {
	b := newBucketIntegrationEnv(t)
	publisher := b.publisher(t, b.fixture.credentials())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		publisher.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		_, ok := b.fixture.object(constants.PublicLayoutPointerKey)
		return ok
	}, 15*time.Second, 10*time.Millisecond, "the loop publishes before it is stopped")
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// bucketRuntimeFixture starts a spectator runtime whose export config names
// bucket, with credentials sealed when creds is set. A nil bucket starts a feed
// with no bucket publishing.
func bucketRuntimeFixture(t *testing.T, bucket *models.PublicBucketConfig, creds *models.PublicBucketCredentials) *PublicSpectatorRuntime {
	t.Helper()
	ctx := t.Context()
	fileSvc := newProducerFileSvc(t)
	logger := testutil.NewTestLogger()
	ks := newTestKeystore(t, fileSvc, logger)
	exportConfig, err := InitializeLocalPublicFeed(ctx, fileSvc, ks, "deployment-bucket", fmt.Sprintf("http://127.0.0.1:%d", constants.PublicSpectatorPrivatePort))
	require.NoError(t, err)
	if bucket != nil {
		exportConfig.Bucket = bucket
		require.NoError(t, WritePublicExportConfig(ctx, fileSvc, exportConfig))
	}
	if creds != nil {
		require.NoError(t, WritePublicBucketCredentials(ctx, fileSvc, ks, *creds))
	}
	runtime, err := NewPublicSpectatorRuntime(PublicSpectatorConfig{
		Enabled:               true,
		PrivateListenAddress:  "127.0.0.1:0",
		PublicListenAddress:   "127.0.0.1:0",
		ExplorerListenAddress: "",
	}, fileSvc, ks, logger)
	require.NoError(t, err)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Stop(stopCtx)
	})
	return runtime
}

// bucketRuntimePublisherDone returns the done channel of the running bucket
// publisher, or nil when none started.
func bucketRuntimePublisherDone(runtime *PublicSpectatorRuntime) chan struct{} {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.bucketDone
}

func TestPublicSpectatorRuntime_StopJoinsBucketPublisher(t *testing.T) {
	fixture := newBucketS3Fixture(t)
	bucket := fixture.bucketConfig()
	creds := fixture.credentials()
	runtime := bucketRuntimeFixture(t, &bucket, &creds)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, runtime.Start(ctx))

	done := bucketRuntimePublisherDone(runtime)
	require.NotNil(t, done, "a configured bucket with credentials starts its publisher")
	require.Eventually(t, func() bool {
		_, ok := fixture.object(constants.PublicLayoutPointerKey)
		return ok
	}, 15*time.Second, 10*time.Millisecond, "the started publisher reaches the bucket")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	require.NoError(t, runtime.Stop(stopCtx))
	select {
	case <-done:
	default:
		t.Fatal("Stop returned before the bucket publisher exited")
	}
}

func TestPublicSpectatorRuntime_NoBucketConfigStartsAndServes(t *testing.T) {
	runtime := bucketRuntimeFixture(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, runtime.Start(ctx))

	assert.Nil(t, bucketRuntimePublisherDone(runtime), "a feed without a bucket starts no bucket publisher")
	requireBootstrapHealthy(t, "http://"+runtime.PublicListenAddress()+"/bootstrap")
}

func TestPublicSpectatorRuntime_MissingBucketCredentialsStillStarts(t *testing.T) {
	fixture := newBucketS3Fixture(t)
	bucket := fixture.bucketConfig()
	runtime := bucketRuntimeFixture(t, &bucket, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, runtime.Start(ctx))

	assert.Nil(t, bucketRuntimePublisherDone(runtime), "missing credentials disable bucket publishing without failing Start")
	requireBootstrapHealthy(t, "http://"+runtime.PublicListenAddress()+"/bootstrap")
}

func ptrTo[T any](value T) *T {
	return &value
}
