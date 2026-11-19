// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package testcmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestVerifyPublicLoopPublicSurface_RequiresMutationRoutesAbsent(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.Handler
		wantError bool
	}{
		{name: "read-only surface returns not found", handler: http.NotFoundHandler()},
		{name: "private surface authentication response is rejected", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusUnauthorized) }), wantError: true},
		{name: "private surface authorization response is rejected", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusForbidden) }), wantError: true},
		{name: "registered route method response is rejected", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusMethodNotAllowed) }), wantError: true},
		{name: "successful mutation response is rejected", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			t.Cleanup(server.Close)
			err := verifyPublicLoopMutationRoutesAbsent(context.Background(), server)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVerifyPublicLoopProofRouteAbsent_RequiresAnonymousProofDownloadNotFound(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.Handler
		wantError bool
	}{
		{name: "public listener without proof downloads returns not found", handler: http.NotFoundHandler()},
		{name: "served proof download is rejected", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }), wantError: true},
		{name: "access-controlled proof download is rejected", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusUnauthorized) }), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			t.Cleanup(server.Close)
			err := verifyPublicLoopProofRouteAbsent(context.Background(), server, "/proofs/"+strings.Repeat("a", 64))
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVerifyPublicLoopProofHead_RequiresAuthenticatedMatchingSegment(t *testing.T) {
	const sourceID = "public-loop-source"
	const ingestToken = "public-loop-token"
	manifest := models.PublicProofManifest{
		SegmentIndex:    1,
		ProofRootSHA256: strings.Repeat("b", 64),
		Artifacts:       []models.PublicProofCatalogEntry{{ArtifactID: strings.Repeat("c", 64)}},
	}
	matching := models.PublicProofRootResponse{SourceID: sourceID, SegmentIndex: 1, ProofRootSHA256: manifest.ProofRootSHA256, ArtifactCount: 1}
	tests := []struct {
		name      string
		head      models.PublicProofRootResponse
		status    int
		wantError bool
	}{
		{name: "head matching the pushed segment is accepted", head: matching, status: http.StatusOK},
		{name: "divergent proof root is rejected", head: models.PublicProofRootResponse{SourceID: sourceID, SegmentIndex: 1, ProofRootSHA256: strings.Repeat("d", 64), ArtifactCount: 1}, status: http.StatusOK, wantError: true},
		{name: "missing artifact is rejected", head: models.PublicProofRootResponse{SourceID: sourceID, SegmentIndex: 1, ProofRootSHA256: manifest.ProofRootSHA256}, status: http.StatusOK, wantError: true},
		{name: "other source head is rejected", head: models.PublicProofRootResponse{SourceID: "other-source", SegmentIndex: 1, ProofRootSHA256: manifest.ProofRootSHA256, ArtifactCount: 1}, status: http.StatusOK, wantError: true},
		{name: "lost proof manifest is rejected", status: http.StatusNotFound, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get(constants.HeaderAuthorization) != constants.BearerScheme+ingestToken || request.URL.Query().Get("source") != sourceID {
					writer.WriteHeader(http.StatusUnauthorized)
					return
				}
				if test.status != http.StatusOK {
					writer.WriteHeader(test.status)
					return
				}
				assert.NoError(t, json.NewEncoder(writer).Encode(test.head))
			}))
			t.Cleanup(server.Close)
			err := verifyPublicLoopProofHead(context.Background(), server, ingestToken, sourceID, manifest)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRunPublicLoop_EmitsCandidateBoundZeroInferenceEvidence(t *testing.T) {
	root := testutil.TempDir(t)
	candidatePath := filepath.Join(root, constants.TestQualificationCandidateFilename)
	outputPath := filepath.Join(root, constants.TestPublicLoopEvidenceFilename)
	candidate := publicLoopCandidate{
		SourceTreeHash:              strings.Repeat("1", 64),
		ExecutionSourceManifestHash: strings.Repeat("2", 64),
		BinarySHA256:                strings.Repeat("3", 64),
		Images: []publicLoopCandidateImage{
			{Components: []string{"gateway", "operator"}, ImageID: "sha256:" + strings.Repeat("4", 64)},
			{Components: []string{"dashboard"}, ImageID: "sha256:" + strings.Repeat("5", 64)},
		},
	}
	var err error
	candidate.ContentHash, err = canonicalContentHash(candidate)
	require.NoError(t, err)
	candidateBytes, err := json.Marshal(candidate)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(candidatePath, candidateBytes, constants.PermFilePrivate))

	require.NoError(t, runPublicLoop(context.Background(), candidatePath, outputPath))

	evidenceBytes, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	var evidence publicLoopEvidence
	require.NoError(t, json.Unmarshal(evidenceBytes, &evidence))
	assert.Equal(t, candidate.ContentHash, evidence.CandidateContentHash)
	assert.Equal(t, 0, evidence.InferenceInvocations)
	assert.Equal(t, int64(4), evidence.HighWaterSequence)
	assert.True(t, evidence.OldKeyRevoked)
	assert.True(t, evidence.ReplacementKeyAcceptedAfterRestart)
	assert.True(t, evidence.MirrorRestartRecovered)
	assert.Equal(t, 1, evidence.RetryCount)
	reproducedHash, err := canonicalContentHash(evidence)
	require.NoError(t, err)
	assert.Equal(t, reproducedHash, evidence.ContentHash)
}
