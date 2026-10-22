// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gwremote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// newOwnerGateway starts a real Gateway with the public spectator set as given
// and installs an owner CLI runtime that talks to it.
func newOwnerGateway(t *testing.T, testName string, publicSpectator bool) (*fixtures.GatewayFixture, fs.RuntimeFileService, *config.Config) {
	t.Helper()
	gateway := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:              testName,
		Posture:               constants.PostureDoctrine,
		NoDownstream:          true,
		EnablePublicSpectator: publicSpectator,
	})
	gateway.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, gateway, testName+"-owner")
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	identity.InstallCLIRuntime(t, gateway, fileSvc, cfg)
	return gateway, fileSvc, cfg
}

// newPublicMirrorGateway starts a real Gateway with its public mirror enabled,
// installs an owner CLI runtime that publishes to it, and points the gwremote
// mirror readers at the mirror's bootstrap and history listeners.
func newPublicMirrorGateway(t *testing.T) (*fixtures.GatewayFixture, fs.RuntimeFileService, *config.Config) {
	t.Helper()
	gateway, fileSvc, cfg := newOwnerGateway(t, "gwremote-publication", true)
	setPublicMirrorOrigin(t, gateway.PublicMirrorOrigin)
	return gateway, fileSvc, cfg
}

// catalogSnapshotRecordBytes returns the catalog-snapshot projection body for
// one dataset ID, the record the Gateway validates and mirrors.
func catalogSnapshotRecordBytes(t *testing.T, datasetID string) string {
	t.Helper()
	recordBytes, err := json.Marshal(struct {
		SchemaVersion string `json:"schema_version"`
		Kind          string `json:"kind"`
		DatasetID     string `json:"dataset_id"`
		QualityState  string `json:"quality_state"`
		ObservedAt    string `json:"observed_at"`
	}{"1.3.0", "catalog_snapshot", datasetID, "live_in_progress", "2026-09-21T00:00:00Z"})
	require.NoError(t, err)
	return string(recordBytes)
}

// writeCatalogSnapshotJSONL writes one catalog-snapshot projection per dataset
// ID as a JSONL record input file, the format PublishJSONLViaGateway reads.
func writeCatalogSnapshotJSONL(t *testing.T, datasetIDs ...string) string {
	t.Helper()
	var content []byte
	for _, datasetID := range datasetIDs {
		line, err := json.Marshal(models.PublicFeedRecordInput{
			RecordType:  models.PublicFeedRecordTypeProjection,
			RecordBytes: catalogSnapshotRecordBytes(t, datasetID),
		})
		require.NoError(t, err)
		content = append(append(content, line...), '\n')
	}
	recordsPath := filepath.Join(testutil.TempDir(t), "records.jsonl")
	require.NoError(t, os.WriteFile(recordsPath, content, 0o600))
	return recordsPath
}

// publishCatalogSnapshots publishes one catalog snapshot per dataset ID through
// the production CLI publication path and requires every record to be accepted.
func publishCatalogSnapshots(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config, datasetIDs ...string) {
	t.Helper()
	published, err := PublishJSONLViaGateway(t.Context(), fileSvc, cfg, writeCatalogSnapshotJSONL(t, datasetIDs...))
	require.NoError(t, err)
	require.Equal(t, len(datasetIDs), published)
}

// countingMirrorProxy forwards every request to the real public mirror and
// counts the bootstrap and history reads that reach it.
func countingMirrorProxy(t *testing.T, mirrorBaseURL string, bootstrapRequests, historyRequests *atomic.Int32) string {
	t.Helper()
	target, err := url.Parse(mirrorBaseURL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bootstrap":
			bootstrapRequests.Add(1)
		case "/history":
			historyRequests.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestHTTPCampaignMirrorProbe_ReportsPresentAndAbsentDatasets(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)
	publishCatalogSnapshots(t, fileSvc, cfg, "eval-run-1")

	probe := NewHTTPCampaignMirrorProbe(t.Context())
	present, err := probe.DatasetPresent(t.Context(), "eval-run-1")
	require.NoError(t, err)
	assert.True(t, present)

	absent, err := probe.DatasetPresent(t.Context(), "eval-run-absent")
	require.NoError(t, err)
	assert.False(t, absent)
}

func TestHTTPCampaignMirrorProbe_DatasetPresentFromHistory(t *testing.T) {
	gateway, fileSvc, cfg := newPublicMirrorGateway(t)
	// A one-projection bootstrap window pushes the older dataset out of the
	// bootstrap, so only the cursor history can report it.
	gateway.Service.GetPublicMirror().SetMaxBootstrapProjections(1)
	publishCatalogSnapshots(t, fileSvc, cfg, "eval-run-history", "eval-run-recent")

	bootstrap, err := FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	require.Len(t, bootstrap.RecentProjections, 1, "bootstrap must hold only the newest projection")

	probe := NewHTTPCampaignMirrorProbe(t.Context())
	for datasetID, expected := range map[string]bool{
		"eval-run-history": true,
		"eval-run-recent":  true,
		"eval-run-missing": false,
	} {
		present, err := probe.DatasetPresent(t.Context(), datasetID)
		require.NoError(t, err)
		assert.Equal(t, expected, present, datasetID)
	}
}

func TestHTTPCampaignMirrorProbe_ReadsOneFilteredHistoryPagePerDataset(t *testing.T) {
	gateway, fileSvc, cfg := newPublicMirrorGateway(t)
	publishCatalogSnapshots(t, fileSvc, cfg, "eval-run-history", "eval-run-recent")

	var bootstrapRequests, historyRequests atomic.Int32
	proxyURL := countingMirrorProxy(t, gateway.PublicMirrorOrigin, &bootstrapRequests, &historyRequests)
	PublicMirrorOrigin = proxyURL

	probe := NewHTTPCampaignMirrorProbe(t.Context())
	for datasetID, expected := range map[string]bool{
		"eval-run-recent":  true,
		"eval-run-history": true,
		"eval-run-missing": false,
	} {
		present, err := probe.DatasetPresent(t.Context(), datasetID)
		require.NoError(t, err)
		assert.Equal(t, expected, present, datasetID)
	}
	assert.Equal(t, int32(0), bootstrapRequests.Load())
	assert.Equal(t, int32(3), historyRequests.Load())
}

func TestGatewayPublisherStatus_ReturnsBootstrapFields(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)
	publishCatalogSnapshots(t, fileSvc, cfg, "eval-run-a", "eval-run-b")

	bootstrap, err := FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	status, err := GatewayPublisherStatus(t.Context(), fileSvc, cfg)
	require.NoError(t, err)
	assert.True(t, status.Enabled)
	assert.NotEmpty(t, status.SourceID)
	assert.Equal(t, bootstrap.Snapshot.SourceID, status.SourceID)
	assert.Equal(t, int64(2), status.HighWaterSequence)
	assert.NotEmpty(t, status.FeedChainHash)
	assert.Equal(t, bootstrap.Snapshot.FeedChainHash, status.FeedChainHash)
	assert.Equal(t, 1, status.BatchCount)
	assert.Equal(t, bootstrap.Snapshot.BatchCount, status.BatchCount)
}

func TestPublishJSONLViaGateway_ExportsRecords(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)

	published, err := PublishJSONLViaGateway(t.Context(), fileSvc, cfg, writeCatalogSnapshotJSONL(t, "campaign-a"))
	require.NoError(t, err)
	assert.Equal(t, 1, published)

	// The mirror must serve the record that the Gateway accepted.
	present, err := NewHTTPCampaignMirrorProbe(t.Context()).DatasetPresent(t.Context(), "campaign-a")
	require.NoError(t, err)
	assert.True(t, present)
}

func TestFetchPublicMirrorHistory_ReturnsCursorPage(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)
	publishCatalogSnapshots(t, fileSvc, cfg, "eval-run-1", "eval-run-2", "eval-run-3")
	bootstrap, err := FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	sourceID := bootstrap.Snapshot.SourceID

	first, err := fetchPublicMirrorHistory(t.Context(), nil, sourceID, "", "", 2)
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	assert.True(t, first.HasMore)
	require.NotEmpty(t, first.Cursor)
	assertDatasetIDs(t, first.Items, "eval-run-1", "eval-run-2")

	second, err := fetchPublicMirrorHistory(t.Context(), nil, sourceID, "", first.Cursor, 2)
	require.NoError(t, err)
	assert.False(t, second.HasMore)
	assertDatasetIDs(t, second.Items, "eval-run-3")
}

func assertDatasetIDs(t *testing.T, items []models.PublicFeedObject, want ...string) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		datasetID, _ := item.StringField("dataset_id")
		got = append(got, datasetID)
	}
	assert.Equal(t, want, got)
}

func TestFetchPublicMirrorBootstrap_RetriesRateLimit(t *testing.T) {
	gateway, _, _ := newPublicMirrorGateway(t)
	// A one-request window of two seconds makes the real mirror answer the
	// second request with 429 and Retry-After: 2, without a one-minute wait.
	require.NoError(t, gateway.Service.GetPublicMirror().SetAnonymousReadRateLimit(1, 2*time.Second))

	_, err := FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gateway.PublicMirrorBootstrapURL, nil)
	require.NoError(t, err)
	limited, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.NoError(t, limited.Body.Close())
	require.Equal(t, http.StatusTooManyRequests, limited.StatusCode)
	assert.Equal(t, "2", limited.Header.Get("Retry-After"))

	bootstrap, err := FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	assert.NotEmpty(t, bootstrap.Snapshot.SourceID)
}

func TestCampaignFeedExporter_EmptyGatewayReturnsZeroHighWater(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)

	exporter, err := NewCampaignFeedExporter(t.Context(), fileSvc, cfg)
	require.NoError(t, err)
	assert.IsType(t, &remoteGatewayCampaignFeedExporter{}, exporter)

	// The Gateway answers 404 snapshot-not-found before any batch is accepted,
	// which the exporter reads as high water zero.
	highWater, err := exporter.HighWaterSequence(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(0), highWater)
}

func TestCampaignFeedExporter_ExportBatchRetriesAfterGatewayOutOfOrder(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)
	datasetIDs := make([]string, 10)
	for index := range datasetIDs {
		datasetIDs[index] = fmt.Sprintf("eval-run-%02d", index)
	}
	publishCatalogSnapshots(t, fileSvc, cfg, datasetIDs...)

	exporter, err := NewCampaignFeedExporter(t.Context(), fileSvc, cfg)
	require.NoError(t, err)
	// The Gateway's high water is 10, so sequence 6 is stale: the Gateway
	// rejects it as out of order, and the exporter must refresh and resend at 11.
	recordBytes := catalogSnapshotRecordBytes(t, "eval-run-retry")
	digest := sha256.Sum256([]byte(recordBytes))
	records := []evaluation.CampaignPublicFeedRecord{{
		Sequence:    6,
		RecordHash:  hex.EncodeToString(digest[:]),
		RecordBytes: recordBytes,
	}}
	require.NoError(t, exporter.ExportBatch(t.Context(), records))
	assert.Equal(t, int64(11), records[0].Sequence)

	// A fresh exporter reads the Gateway's own snapshot, not a cached high water.
	fresh, err := NewCampaignFeedExporter(t.Context(), fileSvc, cfg)
	require.NoError(t, err)
	highWater, err := fresh.HighWaterSequence(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(11), highWater)
}

// publishedSingleAssignmentRun runs one assignment on the real stack with the
// public mirror enabled and publishes the run through the production coordinator
// with the proof publisher attached. The catch-up delivers the assignment's events
// and ingests its audit proof through the single-proof route.
func publishedSingleAssignmentRun(t *testing.T) (*evalRunStack, *evalv1.EvaluationAssignmentResult, evaluation.CampaignProofPublisher) {
	t.Helper()
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	result := e.executeOnce(t)
	pointMirrorAt(t, e.stack.Gateway)

	proofs, err := NewCampaignProofPublisher(ctx, e.cliFiles, e.cliConfig)
	require.NoError(t, err)
	publication := newEvalRunPublication(t, e, NewHTTPCampaignMirrorProbe(ctx)).WithProofPublisher(proofs)
	_, err = publication.PublishRunCatchUp(ctx, e.req.RunID)
	require.NoError(t, err)
	return e, result, proofs
}

func TestCampaignProofPublisherRealStack_SingleProofRouteReturnsDatabaseDigestAndMirrorServesFlushedProof(t *testing.T) {
	ctx := t.Context()
	e, result, proofs := publishedSingleAssignmentRun(t)
	assignmentID := result.GetAssignmentId()

	// The catch-up recorded the digest the Gateway answered for this assignment.
	state, err := NewCampaignPublicationStateStore(e.cliFiles, e.cliConfig)
	require.NoError(t, err)
	recorded, err := state.Load(ctx, e.req.RunID)
	require.NoError(t, err)
	want := recorded.PublishedProofArtifacts[assignmentID].DatabaseSHA256
	require.NotEmpty(t, want)

	spec, err := e.store.LoadCampaignSpec(ctx, evalRunCampaignID)
	require.NoError(t, err)
	input := evaluation.AssignmentAuditProofInput{
		CampaignID:       evalRunCampaignID,
		CampaignRevision: spec.GetCampaignDigest(),
		RunID:            e.req.RunID,
		AssignmentID:     assignmentID,
		IndexDigest:      result.GetResultDigest(),
	}
	answers, err := proofs.IngestAssignmentAuditSlices(ctx, []evaluation.AssignmentAuditProofInput{input}, true)
	require.NoError(t, err)
	require.Len(t, answers, 1)
	assert.Equal(t, assignmentID, answers[0].AssignmentID)
	assert.Equal(t, want, answers[0].DatabaseSHA256, "the single-proof route answers the digest the Gateway holds for the assignment")

	require.NoError(t, proofs.FlushProofCatalog(ctx))
	bootstrap, err := FetchPublicMirrorBootstrap(ctx)
	require.NoError(t, err)
	assert.Positive(t, bootstrap.ProofCatalogSummary.ArtifactCount, "the flushed proof catalog is served by the mirror")
}

func TestCampaignProofPublisherRealStack_PruneRunProofCatalogRemovesRunProofsFromMirror(t *testing.T) {
	ctx := t.Context()
	e, _, proofs := publishedSingleAssignmentRun(t)

	before, err := FetchPublicMirrorBootstrap(ctx)
	require.NoError(t, err)
	require.Positive(t, before.ProofCatalogSummary.ArtifactCount)

	require.NoError(t, proofs.PruneRunProofCatalog(ctx, e.req.RunID))
	require.NoError(t, proofs.FlushProofCatalog(ctx))
	after, err := FetchPublicMirrorBootstrap(ctx)
	require.NoError(t, err)
	assert.Less(t, after.ProofCatalogSummary.ArtifactCount, before.ProofCatalogSummary.ArtifactCount, "the pruned run's proofs leave the mirror catalog")
}

func TestCampaignProofPublisherRealStack_GatewayRefusesIncompleteProofWithPublicationContext(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)
	proofs, err := NewCampaignProofPublisher(t.Context(), fileSvc, cfg)
	require.NoError(t, err)

	// Without an index digest the Gateway refuses the single-proof request with 400.
	input := auditProofInput("1")
	input.IndexDigest = ""
	_, err = proofs.IngestAssignmentAuditSlices(t.Context(), []evaluation.AssignmentAuditProofInput{input}, false)
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.Contains(t, err.Error(), "publish assignment audit proof")
}

func TestCampaignPublicationRealStack_DisabledPublicSpectatorRefusesProofAndSnapshotRoutes(t *testing.T) {
	_, fileSvc, cfg := newOwnerGateway(t, "gwremote-publication-disabled", false)
	proofs, err := NewCampaignProofPublisher(t.Context(), fileSvc, cfg)
	require.NoError(t, err)
	exporter, err := NewCampaignFeedExporter(t.Context(), fileSvc, cfg)
	require.NoError(t, err)

	t.Run("prune is refused as unavailable with publication context", func(t *testing.T) {
		err := proofs.PruneRunProofCatalog(t.Context(), "run-1")
		require.ErrorIs(t, err, constants.ErrHTTPStatusError)
		assert.Contains(t, err.Error(), "prune run proof catalog")
	})

	t.Run("flush is refused as unavailable with publication context", func(t *testing.T) {
		err := proofs.FlushProofCatalog(t.Context())
		require.ErrorIs(t, err, constants.ErrHTTPStatusError)
		assert.Contains(t, err.Error(), "flush proof catalog")
	})

	t.Run("a snapshot read failure is wrapped rather than read as an empty feed", func(t *testing.T) {
		_, err := exporter.HighWaterSequence(t.Context())
		require.ErrorIs(t, err, constants.ErrHTTPStatusError)
		assert.Contains(t, err.Error(), "public feed snapshot")
	})
}

func TestCampaignFeedExporter_RejectedBatchIsReturnedWithoutHighWaterRefresh(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)
	exporter, err := NewCampaignFeedExporter(t.Context(), fileSvc, cfg)
	require.NoError(t, err)

	// An empty batch is a non-retryable Gateway rejection: the exporter returns it
	// at once and never reaches the high-water refresh that a retry needs.
	err = exporter.ExportBatch(t.Context(), nil)
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.NotContains(t, err.Error(), "refresh high water")
}

func TestCampaignFeedExporter_CachedHighWaterIsNotLoweredByGatewaySnapshot(t *testing.T) {
	_, fileSvc, cfg := newPublicMirrorGateway(t)
	publishCatalogSnapshots(t, fileSvc, cfg, "eval-run-a", "eval-run-b")

	exporter, err := NewCampaignFeedExporter(t.Context(), fileSvc, cfg)
	require.NoError(t, err)
	cached, ok := exporter.(*remoteGatewayCampaignFeedExporter)
	require.True(t, ok)
	// The Gateway's snapshot is high water 2; the exporter's cache holds 5.
	cached.highWater = 5

	highWater, err := cached.fetchGatewayHighWater()
	require.NoError(t, err)
	assert.Equal(t, int64(5), highWater)
}
