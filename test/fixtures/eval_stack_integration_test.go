// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package fixtures

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestEvalStackUsesDistinctOutboundOperatorRoots verifies that Gateway and
// Operator canonical stores exist below their owner roots and that remote
// Operators actually connect over mTLS through the witness. The replay store
// shares its owner's canonical ztd.db, so distinct canonical DB paths prove
// replay reservations cannot leak between runtimes.
func TestEvalStackUsesDistinctOutboundOperatorRoots(t *testing.T) {
	stack := NewEvalStack(t, EvalStackOptions{})
	_, err := stack.Gateway.FileService.Stat(t.Context(), constants.CanonicalDBRelPath)
	require.NoError(t, err)

	dataFS, err := fs.NewRuntimeFileService(stack.DataHost.WorkingDir, testutil.NewTestLogger())
	require.NoError(t, err)
	_, err = dataFS.Stat(t.Context(), constants.CanonicalDBRelPath)
	require.NoError(t, err)
	inferenceFS, err := fs.NewRuntimeFileService(stack.InferenceHost.WorkingDir, testutil.NewTestLogger())
	require.NoError(t, err)
	_, err = inferenceFS.Stat(t.Context(), constants.CanonicalDBRelPath)
	require.NoError(t, err)

	gatewayDB := stack.Gateway.FileService.Resolve(constants.CanonicalDBRelPath)
	dataDB := dataFS.Resolve(constants.CanonicalDBRelPath)
	inferenceDB := inferenceFS.Resolve(constants.CanonicalDBRelPath)
	require.NotEqual(t, gatewayDB, dataDB)
	require.NotEqual(t, gatewayDB, inferenceDB)
	require.NotEqual(t, dataDB, inferenceDB)
	logger := testutil.NewTestLogger()
	dataSQLDB, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dataDB), logger)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := dataSQLDB.Close(); err != nil {
			t.Errorf("close Data Operator evidence DB connection: %v", err)
		}
	})
	dataReplay, err := storage.NewReplayStore(dataSQLDB, logger)
	require.NoError(t, err)
	probeNonce := "eval-stack-isolation-probe"
	expiresAt := time.Now().Add(time.Minute)
	gatewayReplay := stack.Gateway.Service.GetReplayStore()
	replayed, err := gatewayReplay.ReserveNonce(probeNonce, expiresAt)
	require.NoError(t, err)
	require.False(t, replayed)
	replayed, err = gatewayReplay.ReserveNonce(probeNonce, expiresAt)
	require.NoError(t, err)
	require.True(t, replayed)
	operatorReplay, err := dataReplay.ReserveNonce(probeNonce, expiresAt)
	require.NoError(t, err)
	require.False(t, operatorReplay, "Data Operator replay store must be independent of Gateway replay store")
	t.Cleanup(func() {
		if err := gatewayReplay.ReleaseNonce(probeNonce); err != nil {
			t.Errorf("release Gateway replay evidence nonce: %v", err)
		}
		if err := dataReplay.ReleaseNonce(probeNonce); err != nil {
			t.Errorf("release Data Operator replay evidence nonce: %v", err)
		}
	})
	stack.RequireOutboundMTLS(t, stack.Data)
	stack.RequireOutboundMTLS(t, stack.Inference)
}

// TestEvalStackPublishesGatewayFeedToAnonymousMirror verifies the optional
// fixture listener exposes the real Gateway-owned mirror and accepts a
// publication over the authenticated feed API.
func TestEvalStackPublishesGatewayFeedToAnonymousMirror(t *testing.T) {
	stack := NewEvalStack(t, EvalStackOptions{EnablePublicSpectator: true})
	require.NotEmpty(t, stack.Gateway.PublicMirrorBootstrapURL)

	bootstrapResp, err := http.Get(stack.Gateway.PublicMirrorBootstrapURL)
	require.NoError(t, err)
	defer bootstrapResp.Body.Close()
	require.Equal(t, http.StatusOK, bootstrapResp.StatusCode)

	recordBytes, err := json.Marshal(struct {
		SchemaVersion string `json:"schema_version"`
		Kind          string `json:"kind"`
		DatasetID     string `json:"dataset_id"`
		QualityState  string `json:"quality_state"`
		ObservedAt    string `json:"observed_at"`
	}{"1.3.0", "catalog_snapshot", "eval-stack-publication-probe", "live_in_progress", "2026-09-21T00:00:00Z"})
	require.NoError(t, err)
	digest := sha256.Sum256(recordBytes)
	body, err := json.Marshal([]models.PublicFeedRecord{{
		Sequence: 1, RecordType: models.PublicFeedRecordTypeProjection,
		RecordHash: hex.EncodeToString(digest[:]), RecordBytes: string(recordBytes),
	}})
	require.NoError(t, err)
	client := CreateCLIMTLSClient(t, stack.Gateway, stack.Identity)
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		network.LocalhostHTTPSURL(stack.Gateway.Service.GetHTTPSPort())+constants.APIPaths.PublicFeedBatches,
		bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	publicBaseURL := strings.TrimSuffix(stack.Gateway.PublicMirrorBootstrapURL, "/bootstrap")
	historyResp, err := http.Get(publicBaseURL + "/history?cursor=0&limit=10")
	require.NoError(t, err)
	defer historyResp.Body.Close()
	require.Equal(t, http.StatusOK, historyResp.StatusCode)
	var page models.PublicFeedCursorPage
	require.NoError(t, json.NewDecoder(historyResp.Body).Decode(&page))
	require.NotEmpty(t, page.Items)
	found := false
	for _, item := range page.Items {
		if string(item["dataset_id"]) == `"eval-stack-publication-probe"` {
			found = true
			break
		}
	}
	require.True(t, found, "published catalog projection should be visible through the anonymous mirror")
}
