// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const (
	runBundleTestCampaignID = "camp-run-bundle"
	runBundleTestRevision   = "rev-1"
)

// runBundleResultRecord returns one assignment result projection in the
// campaign envelope the publisher validates: the canonical 1.0.0 result shape.
func runBundleResultRecord(t *testing.T, sequence int64, runID, assignmentID string) models.PublicFeedRecord {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"schema_version":  "1.0.0",
		"message_type":    "PublicAssignmentResultProjection",
		"idempotency_key": runID + ":" + assignmentID + ":result",
		"record": map[string]string{
			"assignment_id":       assignmentID,
			"run_id":              runID,
			"scenario_id":         "instruction-exact-1",
			"scenario_category":   "EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE",
			"lane":                "EVALUATION_LANE_MODEL_ROLE",
			"designated_role":     "MODEL_CAMPAIGN_ROLE_PRIMARY",
			"variant_id":          "qwen3-4b",
			"lifecycle_status":    "EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED",
			"summary_status":      "EVALUATION_VERDICT_STATUS_PASS",
			"result_digest":       strings.Repeat("e", 64),
			"verification_status": "verified",
			"completed_at":        "2026-09-16T14:00:00Z",
		},
	})
	require.NoError(t, err)
	sum := sha256.Sum256(body)
	return models.PublicFeedRecord{
		Sequence:    sequence,
		RecordType:  models.PublicFeedRecordTypeProjection,
		RecordHash:  hex.EncodeToString(sum[:]),
		RecordBytes: string(body),
	}
}

// startRunBundleSpectator starts a spectator runtime whose proof log has no
// segments yet, and returns it with a feed controller over it.
func startRunBundleSpectator(t *testing.T) (*PublicSpectatorRuntime, *PublicFeedController) {
	t.Helper()
	fileSvc := newProducerFileSvc(t)
	logger := testutil.NewTestLogger()
	ks := newTestKeystore(t, fileSvc, logger)
	runtime, err := NewPublicSpectatorRuntime(PublicSpectatorConfig{
		Enabled:              true,
		PrivateListenAddress: "127.0.0.1:0",
		PublicListenAddress:  "127.0.0.1:0",
		SourceID:             "test-source",
	}, fileSvc, ks, logger)
	require.NoError(t, err)
	require.NoError(t, runtime.Start(t.Context()))
	t.Cleanup(func() { _ = runtime.Stop(context.Background()) })

	controller := newPublicFeedController(PublicFeedControllerDeps{
		Logger:    logger,
		Responder: response.NewWriter(logger),
		Spectator: func() *PublicSpectatorRuntime { return runtime },
	})
	return runtime, controller
}

// startRunBundleRuntime starts a spectator runtime with a proof log seeded by
// one other artifact of runID: an agent trace that must survive every bundle
// replacement.
func startRunBundleRuntime(t *testing.T, runID string) (*PublicSpectatorRuntime, *PublicFeedController) {
	t.Helper()
	runtime, controller := startRunBundleSpectator(t)

	trace := []byte(`{"trace":"agent"}`)
	_, err := runtime.Publisher().BuildProofPackage(t.Context(), runBundleTestCampaignID, runBundleTestRevision, "index-1", true, []ProofArtifactInput{{
		Filename:    "assignment-trace-a.agent-trace.json",
		MediaType:   constants.MediaTypeJSON,
		Content:     trace,
		CampaignID:  runBundleTestCampaignID,
		SourceRunID: runID,
	}})
	require.NoError(t, err)
	return runtime, controller
}

// exportResultRecords publishes result projections through the real publisher
// and mirror, returning the records as they were accepted.
func exportResultRecords(t *testing.T, runtime *PublicSpectatorRuntime, runID string, assignmentIDs ...string) []models.PublicFeedRecord {
	t.Helper()
	publisher := runtime.Publisher()
	publisher.mu.Lock()
	sequence := publisher.highWaterSeq + 1
	publisher.mu.Unlock()
	var records []models.PublicFeedRecord
	for _, assignmentID := range assignmentIDs {
		records = append(records, runBundleResultRecord(t, sequence, runID, assignmentID))
		sequence++
	}
	require.NoError(t, publisher.ExportBatch(t.Context(), records))
	return records
}

// postRunBundleRequest posts request to the run bundle route.
func postRunBundleRequest(t *testing.T, controller *PublicFeedController, request models.PublicRunBundleRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.PublicFeedProofsRunBundle, bytes.NewReader(body))
	rr := httptest.NewRecorder()
	controller.handlePublicFeedProofsRunBundle(rr, req)
	return rr
}

// postRunBundle posts a bundle request for runID under the test campaign.
func postRunBundle(t *testing.T, controller *PublicFeedController, runID string) *httptest.ResponseRecorder {
	t.Helper()
	return postRunBundleRequest(t, controller, models.PublicRunBundleRequest{
		CampaignID:       runBundleTestCampaignID,
		CampaignRevision: runBundleTestRevision,
		RunID:            runID,
	})
}

func publishRunBundleOK(t *testing.T, controller *PublicFeedController, runID string) models.PublicRunBundleResponse {
	t.Helper()
	rr := postRunBundle(t, controller, runID)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var response models.PublicRunBundleResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
	require.True(t, response.Accepted)
	return response
}

func TestRunBundle_RecordBytesMatchAcceptedSnapshotRows(t *testing.T) {
	const runID = "run-bundle-fidelity"
	runtime, controller := startRunBundleRuntime(t, runID)
	accepted := exportResultRecords(t, runtime, runID, "assignment-a", "assignment-b")
	exportResultRecords(t, runtime, "run-other", "assignment-x")

	response := publishRunBundleOK(t, controller, runID)

	assert.Equal(t, runID, response.RunID)
	assert.Equal(t, len(accepted), response.RecordCount)
	require.Len(t, response.BundleSHA256, 64)

	served := httptest.NewRecorder()
	runtime.Mirror().PublicHandler().ServeHTTP(served, httptest.NewRequest(http.MethodGet, "/runs/"+response.BundleSHA256+".jsonl.gz", nil))
	require.Equal(t, http.StatusOK, served.Code)
	assert.Empty(t, served.Header().Get("Content-Encoding"))
	assert.Equal(t, constants.MediaTypeGzip, served.Header().Get(constants.HeaderContentType))
	digest := sha256.Sum256(served.Body.Bytes())
	assert.Equal(t, response.BundleSHA256, hex.EncodeToString(digest[:]))
	assert.Equal(t, response.ByteSize, int64(served.Body.Len()))

	_, lines := gunzipRunBundle(t, served.Body.Bytes())
	require.Len(t, lines, len(accepted))
	for index, line := range lines {
		assert.Equal(t, accepted[index].Sequence, line.Sequence)
		assert.Equal(t, accepted[index].RecordBytes, line.RecordBytes)
		assert.Equal(t, accepted[index].RecordHash, line.RecordHash)
	}
}

func TestRunBundle_ReplacementWithdrawsOnlyThePreviousBundle(t *testing.T) {
	const runID = "run-bundle-replace"
	runtime, controller := startRunBundleRuntime(t, runID)
	publisher := runtime.Publisher()
	exportResultRecords(t, runtime, runID, "assignment-a")
	first := publishRunBundleOK(t, controller, runID)

	exportResultRecords(t, runtime, runID, "assignment-b")
	second := publishRunBundleOK(t, controller, runID)
	require.NotEqual(t, first.BundleSHA256, second.BundleSHA256)

	catalog, err := publisher.GetProofCatalog(t.Context())
	require.NoError(t, err)
	active := make(map[string]models.PublicProofCatalogEntry)
	for _, entry := range catalog.Entries {
		active[entry.ArtifactID] = entry
	}
	assert.NotContains(t, active, first.BundleSHA256, "the previous bundle must be withdrawn")
	require.Contains(t, active, second.BundleSHA256)
	assert.Equal(t, runBundleFilename(runID), active[second.BundleSHA256].Filename)
	assert.Equal(t, constants.MediaTypeGzip, active[second.BundleSHA256].MediaType)
	assert.Equal(t, models.PublicFeedProofClassificationPublicSafe, active[second.BundleSHA256].Classification)
	assert.Equal(t, "/proofs/"+second.BundleSHA256, active[second.BundleSHA256].ImmutableURL)

	var traceActive bool
	for _, entry := range catalog.Entries {
		if strings.HasSuffix(entry.Filename, ".agent-trace.json") && entry.SourceRunID == runID {
			traceActive = true
		}
	}
	assert.True(t, traceActive, "the run's agent trace must stay active")
	bundles := 0
	for _, entry := range catalog.Entries {
		if entry.SourceRunID == runID && entry.Filename == runBundleFilename(runID) {
			bundles++
		}
	}
	assert.Equal(t, 1, bundles)

	requireLinearProofChain(t, publisher)
}

func TestRunBundle_UnchangedRepublishAppendsNoSegment(t *testing.T) {
	const runID = "run-bundle-unchanged"
	runtime, controller := startRunBundleRuntime(t, runID)
	publisher := runtime.Publisher()
	exportResultRecords(t, runtime, runID, "assignment-a")
	first := publishRunBundleOK(t, controller, runID)
	segmentsBefore := requireLinearProofChain(t, publisher)
	sizeBefore := proofLogSize(t, publisher)

	second := publishRunBundleOK(t, controller, runID)

	assert.Equal(t, first.BundleSHA256, second.BundleSHA256)
	segmentsAfter := requireLinearProofChain(t, publisher)
	assert.Len(t, segmentsAfter, len(segmentsBefore))
	assert.Equal(t, sizeBefore, proofLogSize(t, publisher))
}

func TestRunBundle_RetryWithdrawsPreviousBundleLeftActiveByAnInterruptedReplacement(t *testing.T) {
	const runID = "run-bundle-interrupted"
	runtime, controller := startRunBundleRuntime(t, runID)
	publisher := runtime.Publisher()
	exportResultRecords(t, runtime, runID, "assignment-a")
	first := publishRunBundleOK(t, controller, runID)

	// A replacement that appended the new bundle and stopped before withdrawing
	// the old one leaves both active.
	exportResultRecords(t, runtime, runID, "assignment-b")
	records, err := runtime.Mirror().store.RunBundleRecords(t.Context(), "test-source", runID)
	require.NoError(t, err)
	bundle, err := buildRunBundle(records)
	require.NoError(t, err)
	publisher.proofMu.Lock()
	held, err := publisher.lockProofLog(t.Context())
	require.NoError(t, err)
	require.NoError(t, publisher.syncProofIndexLocked(t.Context()))
	entries, err := publisher.writeProofArtifacts(t.Context(), runBundleTestCampaignID, []ProofArtifactInput{{
		Filename:    runBundleFilename(runID),
		MediaType:   constants.MediaTypeGzip,
		Content:     bundle.content,
		CampaignID:  runBundleTestCampaignID,
		SourceRunID: runID,
	}})
	require.NoError(t, err)
	_, err = publisher.appendPublicationLocked(t.Context(), runBundleTestCampaignID, runBundleTestRevision, hashBytes(bundle.content), entries)
	require.NoError(t, err)
	require.NoError(t, held.Close())
	publisher.proofMu.Unlock()

	retried := publishRunBundleOK(t, controller, runID)

	require.NotEqual(t, first.BundleSHA256, retried.BundleSHA256)
	catalog, err := publisher.GetProofCatalog(t.Context())
	require.NoError(t, err)
	var active []string
	for _, entry := range catalog.Entries {
		if entry.SourceRunID == runID && entry.Filename == runBundleFilename(runID) {
			active = append(active, entry.ArtifactID)
		}
	}
	assert.Equal(t, []string{retried.BundleSHA256}, active)
	requireLinearProofChain(t, publisher)
}

func TestRunBundle_RunWithoutResultRowsReturnsSentinelThroughRoute(t *testing.T) {
	const runID = "run-bundle-empty"
	_, controller := startRunBundleRuntime(t, runID)

	rr := postRunBundle(t, controller, "run-without-results")

	assert.GreaterOrEqual(t, rr.Code, http.StatusBadRequest)
	assert.Less(t, rr.Code, http.StatusInternalServerError)
	assert.Contains(t, rr.Body.String(), constants.ErrPublicRunBundleEmpty.Error())
}

func TestRunBundle_EntryAndSegmentCarryTheRequestCampaignNotTheLogHead(t *testing.T) {
	const runID = "run-bundle-campaign"
	const otherCampaignID = "camp-other-run-bundle"
	const otherRevision = "rev-other"
	runtime, controller := startRunBundleRuntime(t, runID)
	publisher := runtime.Publisher()
	exportResultRecords(t, runtime, runID, "assignment-a")

	rr := postRunBundleRequest(t, controller, models.PublicRunBundleRequest{
		CampaignID:       otherCampaignID,
		CampaignRevision: otherRevision,
		RunID:            runID,
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var response models.PublicRunBundleResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))

	catalog, err := publisher.GetProofCatalog(t.Context())
	require.NoError(t, err)
	active := make(map[string]models.PublicProofCatalogEntry)
	for _, entry := range catalog.Entries {
		active[entry.ArtifactID] = entry
	}
	require.Contains(t, active, response.BundleSHA256)
	assert.Equal(t, otherCampaignID, active[response.BundleSHA256].CampaignID)

	segments := requireLinearProofChain(t, publisher)
	require.NotEmpty(t, segments)
	head := segments[len(segments)-1]
	assert.Equal(t, otherCampaignID, head.CampaignID)
	assert.Equal(t, otherRevision, head.CampaignRevision)
	assert.Equal(t, response.BundleSHA256, head.VerifiedIndexGenerationHash)
}

func TestRunBundle_MissingCampaignIDReturnsBadRequestAndAppendsNothing(t *testing.T) {
	const runID = "run-bundle-no-campaign"
	runtime, controller := startRunBundleRuntime(t, runID)
	publisher := runtime.Publisher()
	exportResultRecords(t, runtime, runID, "assignment-a")
	segmentsBefore := requireLinearProofChain(t, publisher)
	sizeBefore := proofLogSize(t, publisher)

	rr := postRunBundleRequest(t, controller, models.PublicRunBundleRequest{
		CampaignRevision: runBundleTestRevision,
		RunID:            runID,
	})

	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrMissingRequiredField.Error())
	assert.Len(t, requireLinearProofChain(t, publisher), len(segmentsBefore))
	assert.Equal(t, sizeBefore, proofLogSize(t, publisher))
}

func TestRunBundle_PublishesOnLogWithoutPriorSegments(t *testing.T) {
	const runID = "run-bundle-fresh-log"
	runtime, controller := startRunBundleSpectator(t)
	publisher := runtime.Publisher()
	exportResultRecords(t, runtime, runID, "assignment-a")

	response := publishRunBundleOK(t, controller, runID)

	segments := requireLinearProofChain(t, publisher)
	require.Len(t, segments, 1)
	assert.Equal(t, runBundleTestCampaignID, segments[0].CampaignID)
	assert.Equal(t, runBundleTestRevision, segments[0].CampaignRevision)
	assert.Equal(t, response.BundleSHA256, segments[0].VerifiedIndexGenerationHash)
	catalog, err := publisher.GetProofCatalog(t.Context())
	require.NoError(t, err)
	var active []string
	for _, entry := range catalog.Entries {
		if entry.SourceRunID == runID && entry.Filename == runBundleFilename(runID) {
			active = append(active, entry.ArtifactID)
		}
	}
	assert.Equal(t, []string{response.BundleSHA256}, active)
}
