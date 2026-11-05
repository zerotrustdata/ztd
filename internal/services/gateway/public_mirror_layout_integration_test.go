//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// layoutResponse is the status and headers of one layout GET; the body is already read and closed.
type layoutResponse struct {
	StatusCode int
	Header     http.Header
}

// layoutGet performs an anonymous GET and returns the stored response bytes.
// Transparent decompression is off, so Content-Encoding bodies stay gzipped.
func layoutGet(t *testing.T, client *http.Client, baseURL, path string) (layoutResponse, []byte) {
	t.Helper()
	response, err := client.Get(baseURL + path)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	return layoutResponse{StatusCode: response.StatusCode, Header: response.Header}, body
}

// layoutClient returns a client that never adds Accept-Encoding or unpacks a
// Content-Encoding body, so tests compare the bytes the mirror served.
func layoutClient(t *testing.T) *http.Client {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

// gunzipLayout decodes one gzip body the mirror served.
func gunzipLayout(t *testing.T, body []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)
	plain, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	return plain
}

// layoutProjection builds a projection record with a dataset per sequence.
func layoutProjection(env *mirrorTestEnv, sequence int64) models.PublicFeedRecord {
	return env.makeRecord(sequence, models.NewPublicFeedObject(map[string]string{
		"kind":           "catalog_snapshot",
		"dataset_id":     fmt.Sprintf("ds-layout-%d", sequence),
		"schema_version": "1.0.0",
	}))
}

func TestPublicMirrorLayout_PointerNamesServedReadModel(t *testing.T) {
	env := newMirrorTestEnv(t)
	client := layoutClient(t)
	records := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{
			"kind":            "evaluation_summary",
			"dataset_id":      campaignMirrorDatasetID("run-1"),
			"run_id":          "run-1",
			"lifecycle_state": string(models.RunLifecycleStatusRunning),
			"schema_version":  "1.5.0",
		})),
	}
	_, accepted := env.sendIngest(env.buildBatch(records, constants.PublicFeedZeroHashHex))
	require.True(t, accepted.Accepted)

	pointerResponse, pointerBody := layoutGet(t, client, env.publicServer.URL, "/pointer.json")
	require.Equal(t, http.StatusOK, pointerResponse.StatusCode)
	assert.Equal(t, constants.HeaderValueApplicationJSON, pointerResponse.Header.Get(constants.HeaderContentType))
	assert.Equal(t, constants.PublicLayoutCachePointer, pointerResponse.Header.Get(constants.HeaderCacheControl))
	assert.Equal(t, "*", pointerResponse.Header.Get(constants.HeaderAccessControlAllowOrigin))
	var pointer models.PublicFeedPointer
	require.NoError(t, json.Unmarshal(pointerBody, &pointer))
	assert.Equal(t, constants.PublicFeedPointerSchemaVersion, pointer.SchemaVersion)
	assert.Equal(t, env.sourceID, pointer.SourceID)
	assert.Equal(t, int64(1), pointer.HighWaterSequence)
	assert.Equal(t, int64(1), pointer.HistoryFirstSequence)
	assert.True(t, pointer.Active, "a running evaluation summary marks the pointer active")
	assert.NotContains(t, string(pointerBody), "proof_")

	readModelResponse, readModelBody := layoutGet(t, client, env.publicServer.URL, "/read-model/"+pointer.ReadModelSHA256+".json.gz")
	require.Equal(t, http.StatusOK, readModelResponse.StatusCode)
	assert.Equal(t, constants.HeaderValueApplicationJSON, readModelResponse.Header.Get(constants.HeaderContentType))
	assert.Equal(t, "gzip", readModelResponse.Header.Get("Content-Encoding"))
	assert.Equal(t, constants.PublicLayoutCacheImmutable, readModelResponse.Header.Get(constants.HeaderCacheControl))
	plain := gunzipLayout(t, readModelBody)
	digest := sha256.Sum256(plain)
	assert.Equal(t, pointer.ReadModelSHA256, hex.EncodeToString(digest[:]))

	_, bootstrapBody := layoutGet(t, client, env.publicServer.URL, "/bootstrap?source="+env.sourceID)
	assert.Equal(t, bootstrapBody, plain, "the read model is the bootstrap bytes")
}

func TestPublicMirrorLayout_AdvertisedReadModelSurvivesIngest(t *testing.T) {
	env := newMirrorTestEnv(t)
	client := layoutClient(t)
	first := env.buildBatch([]models.PublicFeedRecord{layoutProjection(env, 1)}, constants.PublicFeedZeroHashHex)
	_, accepted := env.sendIngest(first)
	require.True(t, accepted.Accepted)
	_, body := layoutGet(t, client, env.publicServer.URL, "/pointer.json")
	var pointer models.PublicFeedPointer
	require.NoError(t, json.Unmarshal(body, &pointer))

	// A publisher can accept another batch between the browser's pointer and
	// read-model requests. The advertised digest must still be downloadable.
	second := env.buildBatch([]models.PublicFeedRecord{layoutProjection(env, 2)}, first.ContentHash)
	_, accepted = env.sendIngest(second)
	require.True(t, accepted.Accepted)
	path := "/read-model/" + pointer.ReadModelSHA256 + ".json.gz"
	response, body := layoutGet(t, client, env.publicServer.URL, path)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, pointer.ReadModelSHA256, models.SHA256Hex(gunzipLayout(t, body)))
	original := bytes.Clone(body)

	_, body = layoutGet(t, client, env.publicServer.URL, "/pointer.json")
	var current models.PublicFeedPointer
	require.NoError(t, json.Unmarshal(body, &current))
	require.NotEqual(t, pointer.ReadModelSHA256, current.ReadModelSHA256)
	response, body = layoutGet(t, client, env.publicServer.URL, path)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, original, body, "another reader's newer pointer must not invalidate an in-flight download")

	response, _ = layoutGet(t, client, env.publicServer.URL, path+"?source=another-source")
	assert.Equal(t, http.StatusNotFound, response.StatusCode, "retained objects stay source-scoped")
}

func TestPublicMirrorLayout_WithdrawalInvalidatesRetainedReadModels(t *testing.T) {
	for _, kind := range []string{"dataset", "artifact"} {
		t.Run(kind, func(t *testing.T) {
			env := newMirrorTestEnv(t)
			client := layoutClient(t)
			ctx := t.Context()
			batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{
				"kind": "evaluation_summary", "dataset_id": campaignMirrorDatasetID("run-1"),
				"run_id": "run-1", "schema_version": "1.5.0",
			}))}, constants.PublicFeedZeroHashHex)
			_, accepted := env.sendIngest(batch)
			require.True(t, accepted.Accepted)
			content := []byte("fixture")
			entry := makeTestCatalogEntry("scenario-test.input.json", content)
			segment := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
			require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{
				SourceID: env.sourceID, Segment: &segment,
				Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}},
			}))
			_, body := layoutGet(t, client, env.publicServer.URL, "/pointer.json")
			var pointer models.PublicFeedPointer
			require.NoError(t, json.Unmarshal(body, &pointer))

			if kind == "dataset" {
				require.NoError(t, env.mirror.WithdrawCampaignDataset(ctx, "run-1"))
			} else {
				withdrawal := makeTestSegment(env, 2, segment.ProofRootSHA256, nil, []string{entry.ArtifactID})
				require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &withdrawal}))
			}
			response, _ := layoutGet(t, client, env.publicServer.URL, "/read-model/"+pointer.ReadModelSHA256+".json.gz")
			assert.Equal(t, http.StatusNotFound, response.StatusCode, "withdrawals leave no retention grace")
			_, body = layoutGet(t, client, env.publicServer.URL, "/pointer.json")
			var current models.PublicFeedPointer
			require.NoError(t, json.Unmarshal(body, &current))
			require.NotEqual(t, pointer.ReadModelSHA256, current.ReadModelSHA256)
			response, body = layoutGet(t, client, env.publicServer.URL, "/read-model/"+current.ReadModelSHA256+".json.gz")
			require.Equal(t, http.StatusOK, response.StatusCode)
			assert.Equal(t, current.ReadModelSHA256, models.SHA256Hex(gunzipLayout(t, body)))
		})
	}
}

func TestPublicMirrorLayout_InternalProofRoutesRemoved(t *testing.T) {
	env := newMirrorTestEnv(t)
	client := layoutClient(t)
	content := []byte(`{"private":"historical"}`)
	entry := makeTestCatalogEntry("assignment-audit.db", content)
	segment := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
	require.NoError(t, env.mirror.storeProofPackage(t.Context(), models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &segment, Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}}}))
	for _, path := range []string{"/proofs/" + entry.ArtifactID, "/proof-catalog", "/proof-head", "/proof-segments", "/proof-catalog/" + entry.ArtifactID + ".json.gz", "/proof-meta/" + entry.ArtifactID + ".json", "/proof-head.json", "/proof-segments/1.jsonl.gz"} {
		response, body := layoutGet(t, client, env.publicServer.URL, path)
		assert.Equal(t, http.StatusNotFound, response.StatusCode, "%s: %s", path, body)
	}
	// Internal metadata and files remain intact on an existing root.
	_, found, err := env.store.proofCatalogEntry(t.Context(), env.sourceID, entry.ArtifactID)
	require.NoError(t, err)
	assert.True(t, found)
	file, err := env.store.openProofArtifact(t.Context(), entry.ArtifactID)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func TestPublicMirrorLayout_LiveStatusTailBoundedAndOmitsResults(t *testing.T) {
	env := newMirrorTestEnv(t)
	client := layoutClient(t)
	const totalRecords = 130
	records := make([]models.PublicFeedRecord, 0, totalRecords)
	for sequence := int64(1); sequence <= totalRecords; sequence++ {
		if sequence%10 == 0 {
			records = append(records, taskIndexTestResultRecord(t, sequence, fmt.Sprintf("assignment-%d", sequence), "task-alpha@1.0.0", "2026-09-01T00:00:00Z"))
			continue
		}
		records = append(records, layoutProjection(env, sequence))
	}
	first := env.buildBatch(records[:constants.PublicFeedBatchMaxRecords], constants.PublicFeedZeroHashHex)
	_, accepted := env.sendIngest(first)
	require.True(t, accepted.Accepted)
	second := env.buildBatch(records[constants.PublicFeedBatchMaxRecords:], first.ContentHash)
	_, accepted = env.sendIngest(second)
	require.True(t, accepted.Accepted)

	var expected []int64
	for sequence := int64(totalRecords); sequence >= 1 && len(expected) < constants.PublicLiveStatusTailLimit; sequence-- {
		if sequence%10 != 0 {
			expected = append(expected, sequence)
		}
	}
	for left, right := 0, len(expected)-1; left < right; left, right = left+1, right-1 {
		expected[left], expected[right] = expected[right], expected[left]
	}

	response, body := layoutGet(t, client, env.publicServer.URL, "/live/status.json")
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	assert.Equal(t, constants.PublicLayoutCacheLive, response.Header.Get(constants.HeaderCacheControl))
	var status models.PublicLiveStatus
	require.NoError(t, json.Unmarshal(body, &status))
	assert.Equal(t, constants.PublicLiveStatusSchemaVersion, status.SchemaVersion)
	assert.Equal(t, int64(totalRecords), status.Snapshot.HighWaterSequence)
	assert.NotNil(t, status.ActiveRuns)
	assert.Empty(t, status.ActiveRuns)
	require.Len(t, status.Tail, constants.PublicLiveStatusTailLimit)

	sequences := make([]int64, 0, len(status.Tail))
	for _, tailRecord := range status.Tail {
		sequences = append(sequences, tailRecord.Sequence)
		var item models.PublicFeedObject
		require.NoError(t, json.Unmarshal([]byte(tailRecord.RecordBytes), &item))
		messageType, _ := item.StringField("message_type")
		assert.NotEqual(t, publicSnapshotAssignmentResultKind, messageType, "the tail omits assignment results")
	}
	assert.Equal(t, expected, sequences, "the tail is the newest non-result records, oldest first")
}

func TestPublicMirrorLayout_RevisionAdvancesOnlyWhenContentChanges(t *testing.T) {
	env := newMirrorTestEnv(t)
	client := layoutClient(t)
	env.mirror.SetPointerSigner(func(pointer models.PublicFeedPointer) (models.PublicFeedPointer, error) {
		return models.SignPublicFeedPointer(pointer, env.priv, models.PublicProofSigningKeyID(env.pub))
	})
	first := env.buildBatch([]models.PublicFeedRecord{layoutProjection(env, 1)}, constants.PublicFeedZeroHashHex)
	_, accepted := env.sendIngest(first)
	require.True(t, accepted.Accepted)

	firstResponse, firstBody := layoutGet(t, client, env.publicServer.URL, "/pointer.json")
	require.Equal(t, http.StatusOK, firstResponse.StatusCode)
	var firstPointer models.PublicFeedPointer
	require.NoError(t, json.Unmarshal(firstBody, &firstPointer))
	require.NoError(t, models.VerifyPublicFeedPointer(firstPointer, env.pub))

	_, unchangedBody := layoutGet(t, client, env.publicServer.URL, "/pointer.json")
	assert.Equal(t, firstBody, unchangedBody, "unchanged content serves the same pointer")

	second := env.buildBatch([]models.PublicFeedRecord{layoutProjection(env, 2)}, first.ContentHash)
	_, accepted = env.sendIngest(second)
	require.True(t, accepted.Accepted)

	_, changedBody := layoutGet(t, client, env.publicServer.URL, "/pointer.json")
	var changed models.PublicFeedPointer
	require.NoError(t, json.Unmarshal(changedBody, &changed))
	assert.Greater(t, changed.Revision, firstPointer.Revision)
	assert.Equal(t, int64(2), changed.HighWaterSequence)
	require.NoError(t, models.VerifyPublicFeedPointer(changed, env.pub))
}

func TestPublicMirrorLayout_MalformedPathsReturnNotFound(t *testing.T) {
	env := newMirrorTestEnv(t)
	client := layoutClient(t)
	env.mirror.SetAnonymousReadRateLimit(1000, time.Minute)
	ctx := t.Context()
	content := []byte(`{"segment":"table"}`)
	entry := makeTestCatalogEntry("table.json", content)
	segment := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
	require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{
		SourceID: env.sourceID, Segment: &segment,
		Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}},
	}))
	_, accepted := env.sendIngest(env.buildBatch([]models.PublicFeedRecord{layoutProjection(env, 1)}, constants.PublicFeedZeroHashHex))
	require.True(t, accepted.Accepted)

	zeroDigest := strings.Repeat("0", sha256.Size*2)
	tests := []struct {
		name string
		path string
	}{
		{name: "read model not a digest", path: "/read-model/not-a-digest.json.gz"},
		{name: "read model wrong digest", path: "/read-model/" + zeroDigest + ".json.gz"},
		{name: "proof catalog wrong digest", path: "/proof-catalog/" + zeroDigest + ".json.gz"},
		{name: "proof meta not hex", path: "/proof-meta/XYZ.json"},
		{name: "proof meta uppercase digest", path: "/proof-meta/" + strings.ToUpper(entry.ArtifactID) + ".json"},
		{name: "proof meta unknown digest", path: "/proof-meta/" + zeroDigest + ".json"},
		{name: "proof segments zero", path: "/proof-segments/0.jsonl.gz"},
		{name: "proof segments signed", path: "/proof-segments/+1.jsonl.gz"},
		{name: "proof segments leading zero", path: "/proof-segments/01.jsonl.gz"},
		{name: "proof segments unaligned", path: "/proof-segments/2.jsonl.gz"},
		{name: "proof segments wrong suffix", path: "/proof-segments/1.jsonl"},
		{name: "history zero", path: "/history/0.json.gz"},
		{name: "history not numeric", path: "/history/first.json.gz"},
		{name: "history unaligned", path: "/history/2.json.gz"},
		{name: "history wrong suffix", path: "/history/1.json"},
		{name: "live run slash", path: "/live/runs/a%2Fb.jsonl.gz"},
		{name: "live run not active", path: "/live/runs/run-absent.jsonl.gz"},
		{name: "live status trailing segment", path: "/live/status.json/extra"},
		{name: "pointer trailing segment", path: "/pointer.json/extra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, body := layoutGet(t, client, env.publicServer.URL, tt.path)
			assert.Equal(t, http.StatusNotFound, response.StatusCode, string(body))
		})
	}
}

func TestPublicMirrorLayout_LiveRunBundleServedWhileActive(t *testing.T) {
	env := newMirrorTestEnv(t)
	client := layoutClient(t)
	runID := "run-1"
	summary := env.makeRecord(1, models.NewPublicFeedObject(map[string]string{
		"kind":            "evaluation_summary",
		"dataset_id":      campaignMirrorDatasetID(runID),
		"run_id":          runID,
		"lifecycle_state": string(models.RunLifecycleStatusRunning),
		"schema_version":  "1.5.0",
	}))
	results := []models.PublicFeedRecord{
		taskIndexTestResultRecord(t, 2, "assignment-a", "task-alpha@1.0.0", "2026-09-01T00:00:00Z"),
		taskIndexTestResultRecord(t, 3, "assignment-b", "task-alpha@1.0.1", "2026-09-02T00:00:00Z"),
	}
	_, accepted := env.sendIngest(env.buildBatch(append([]models.PublicFeedRecord{summary}, results...), constants.PublicFeedZeroHashHex))
	require.True(t, accepted.Accepted)

	response, body := layoutGet(t, client, env.publicServer.URL, "/live/runs/"+runID+".jsonl.gz")
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	assert.Equal(t, constants.MediaTypeGzip, response.Header.Get(constants.HeaderContentType))
	assert.Empty(t, response.Header.Get("Content-Encoding"), "the bundle is a gzip file, not a content-encoded body")
	assert.Equal(t, constants.PublicLayoutCacheLiveRun, response.Header.Get(constants.HeaderCacheControl))
	lines := strings.Split(strings.TrimSpace(string(gunzipLayout(t, body))), "\n")
	assert.Len(t, lines, len(results))
}

func TestPublicMirrorLayout_DatasetsSinceReportsDatasetsAboveSequence(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := t.Context()
	runID := "run-1"
	records := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{
			"kind":            "evaluation_summary",
			"dataset_id":      campaignMirrorDatasetID(runID),
			"run_id":          runID,
			"lifecycle_state": string(models.RunLifecycleStatusRunning),
			"schema_version":  "1.5.0",
		})),
		taskIndexTestResultRecord(t, 2, "assignment-a", "task-alpha@1.0.0", "2026-09-01T00:00:00Z"),
		layoutProjection(env, 3),
	}
	_, accepted := env.sendIngest(env.buildBatch(records, constants.PublicFeedZeroHashHex))
	require.True(t, accepted.Accepted)

	all, err := env.mirror.LayoutDatasetsSince(ctx, env.sourceID, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"ds-layout-3", campaignMirrorDatasetID(runID)}, all, "datasets come back sorted")

	afterRun, err := env.mirror.LayoutDatasetsSince(ctx, env.sourceID, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"ds-layout-3"}, afterRun, "only records above the sequence count")

	none, err := env.mirror.LayoutDatasetsSince(ctx, env.sourceID, 3)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestPublicMirrorLayout_IngestListenerServesNoLayoutRoutes(t *testing.T) {
	env := newMirrorTestEnv(t)
	zeroDigest := strings.Repeat("0", sha256.Size*2)
	paths := []string{
		"/pointer.json",
		"/read-model/" + zeroDigest + ".json.gz",
		"/runs/" + zeroDigest + ".jsonl.gz",
		"/fixtures/" + zeroDigest,
		"/proof-catalog/" + zeroDigest + ".json.gz",
		"/proof-meta/" + zeroDigest + ".json",
		"/proof-head.json",
		"/proof-segments/1.jsonl.gz",
		"/history/1.json.gz",
		"/live/status.json",
		"/live/runs/run-1.jsonl.gz",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, env.server.URL+path, nil)
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer "+mirrorTestIngestToken)
			response, err := env.server.Client().Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, http.StatusNotFound, response.StatusCode)
			assert.Empty(t, response.Header.Get(constants.HeaderAccessControlAllowOrigin))
		})
	}
}

func TestPublicMirrorLayout_UIArtifactsAreSelectedAndWithdrawn(t *testing.T) {
	env := newMirrorTestEnv(t)
	client := layoutClient(t)
	ctx := t.Context()
	bundleBody := []byte("gzipped assignment bundle")
	fixtureBody := []byte(`{"scenario_id":"old"}`)
	traceBody := []byte("historical private trace")
	databaseBody := []byte("historical private database")
	bundle := makeTestCatalogEntry("old"+constants.PublicRunBundleFilenameSuffix, bundleBody)
	fixture := makeTestCatalogEntry("scenario-old.input.json", fixtureBody)
	trace := makeTestCatalogEntry("old.agent-trace.json", traceBody)
	database := makeTestCatalogEntry("old.audit.db", databaseBody)
	segment := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{bundle, fixture, trace, database}, nil)
	require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{
		SourceID: env.sourceID, Segment: &segment,
		Artifacts: []models.PublicProofIngestArtifact{
			{ArtifactID: bundle.ArtifactID, Content: bundleBody},
			{ArtifactID: fixture.ArtifactID, Content: fixtureBody},
			{ArtifactID: trace.ArtifactID, Content: traceBody},
			{ArtifactID: database.ArtifactID, Content: databaseBody},
		},
	}))
	bundlePath := "/runs/" + bundle.ArtifactID + ".jsonl.gz"
	fixturePath := "/fixtures/" + fixture.ArtifactID
	for _, tc := range []struct {
		path string
		body []byte
	}{{bundlePath, bundleBody}, {fixturePath, fixtureBody}} {
		response, body := layoutGet(t, client, env.publicServer.URL, tc.path)
		require.Equal(t, http.StatusOK, response.StatusCode, string(body))
		assert.Equal(t, tc.body, body)
		assert.Empty(t, response.Header.Get("Content-Encoding"), "artifact digests cover the stored bytes, without transport decoding")
		assert.Equal(t, constants.PublicLayoutCacheImmutable, response.Header.Get(constants.HeaderCacheControl))
		assert.Equal(t, "*", response.Header.Get(constants.HeaderAccessControlAllowOrigin))
	}
	for _, path := range []string{
		"/fixtures/" + bundle.ArtifactID,
		"/runs/" + fixture.ArtifactID + ".jsonl.gz",
		"/runs/" + bundle.ArtifactID,
		"/runs/" + strings.ToUpper(bundle.ArtifactID) + ".jsonl.gz",
		"/fixtures/invalid",
		"/fixtures/" + strings.Repeat("0", 64),
		"/fixtures/" + trace.ArtifactID,
		"/runs/" + trace.ArtifactID + ".jsonl.gz",
		"/fixtures/" + database.ArtifactID,
		"/runs/" + database.ArtifactID + ".jsonl.gz",
	} {
		response, _ := layoutGet(t, client, env.publicServer.URL, path)
		assert.Equal(t, http.StatusNotFound, response.StatusCode, path)
	}
	withdrawal := makeTestSegment(env, 2, segment.ProofRootSHA256, nil, []string{bundle.ArtifactID, fixture.ArtifactID})
	require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &withdrawal}))
	for _, path := range []string{bundlePath, fixturePath} {
		response, _ := layoutGet(t, client, env.publicServer.URL, path)
		assert.Equal(t, http.StatusNotFound, response.StatusCode, path)
	}
	// Layout withdrawal leaves the historical private artifacts in the store.
	for _, entry := range []models.PublicProofCatalogEntry{trace, database} {
		_, found, err := env.store.proofArtifact(ctx, entry.ArtifactID)
		require.NoError(t, err)
		assert.True(t, found)
	}
}
