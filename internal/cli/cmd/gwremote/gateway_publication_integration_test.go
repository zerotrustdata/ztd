// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gwremote

import (
	"encoding/json"
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
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// newPublicMirrorGateway starts a real Gateway with its public mirror enabled,
// installs an owner CLI runtime that publishes to it, and points the gwremote
// mirror readers at the mirror's bootstrap and history listeners.
func newPublicMirrorGateway(t *testing.T) (*fixtures.GatewayFixture, fs.RuntimeFileService, *config.Config) {
	t.Helper()
	gateway := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:              "gwremote-publication",
		Posture:               constants.PostureDoctrine,
		NoDownstream:          true,
		EnablePublicSpectator: true,
	})
	gateway.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, gateway, "gwremote-publication-owner")
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	identity.InstallCLIRuntime(t, gateway, fileSvc, cfg)

	setPublicMirrorOrigin(t, gateway.PublicMirrorOrigin)
	return gateway, fileSvc, cfg
}

// writeCatalogSnapshotJSONL writes one catalog-snapshot projection per dataset
// ID as a JSONL record input file, the format PublishJSONLViaGateway reads.
func writeCatalogSnapshotJSONL(t *testing.T, datasetIDs ...string) string {
	t.Helper()
	var content []byte
	for _, datasetID := range datasetIDs {
		recordBytes, err := json.Marshal(struct {
			SchemaVersion string `json:"schema_version"`
			Kind          string `json:"kind"`
			DatasetID     string `json:"dataset_id"`
			QualityState  string `json:"quality_state"`
			ObservedAt    string `json:"observed_at"`
		}{"1.3.0", "catalog_snapshot", datasetID, "live_in_progress", "2026-09-21T00:00:00Z"})
		require.NoError(t, err)
		line, err := json.Marshal(models.PublicFeedRecordInput{
			RecordType:  models.PublicFeedRecordTypeProjection,
			RecordBytes: string(recordBytes),
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

func TestHTTPCampaignMirrorProbe_DatasetPresentFromBootstrap(t *testing.T) {
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

func TestHTTPCampaignMirrorProbe_IndexesMirrorOnceForMultipleDatasets(t *testing.T) {
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
	assert.Equal(t, int32(1), bootstrapRequests.Load())
	assert.Equal(t, int32(1), historyRequests.Load())
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

	first, err := fetchPublicMirrorHistory(t.Context(), nil, sourceID, "", 2)
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	assert.True(t, first.HasMore)
	require.NotEmpty(t, first.Cursor)
	assertDatasetIDs(t, first.Items, "eval-run-1", "eval-run-2")

	second, err := fetchPublicMirrorHistory(t.Context(), nil, sourceID, first.Cursor, 2)
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
