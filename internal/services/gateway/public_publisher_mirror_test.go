// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zerotrustdata/ztd/internal/models"
)

func pendingTestManifest() *models.PublicProofManifest {
	return &models.PublicProofManifest{
		ProofRootSHA256: "root-3",
		ArtifactCount:   3,
		Artifacts: []models.PublicProofCatalogEntry{
			{ArtifactID: "aaa"},
			{ArtifactID: "bbb"},
			{ArtifactID: "ccc"},
		},
	}
}

func TestProofMirrorPendingArtifactIDs_ReturnsSuffixAfterSyncedRoot(t *testing.T) {
	syncState := &models.PublicProofMirrorSyncState{ProofRootSHA256: "root-1", ArtifactCount: 1}
	pending := proofMirrorPendingArtifactIDs(pendingTestManifest(), syncState, models.PublicProofRootResponse{ProofRootSHA256: "root-1"})
	assert.Equal(t, []string{"bbb", "ccc"}, pending)
}

func TestProofMirrorPendingArtifactIDs_NoneWhenMirrorRootDiffers(t *testing.T) {
	syncState := &models.PublicProofMirrorSyncState{ProofRootSHA256: "root-1", ArtifactCount: 1}
	pending := proofMirrorPendingArtifactIDs(pendingTestManifest(), syncState, models.PublicProofRootResponse{ProofRootSHA256: "other"})
	assert.Empty(t, pending)
}

func TestProofMirrorPendingArtifactIDs_NoneWithoutSyncState(t *testing.T) {
	assert.Empty(t, proofMirrorPendingArtifactIDs(pendingTestManifest(), nil, models.PublicProofRootResponse{}))
	assert.Empty(t, proofMirrorPendingArtifactIDs(pendingTestManifest(), &models.PublicProofMirrorSyncState{}, models.PublicProofRootResponse{}))
}

func TestProofMirrorPendingArtifactIDs_NoneWhenSyncedCountExceedsManifest(t *testing.T) {
	syncState := &models.PublicProofMirrorSyncState{ProofRootSHA256: "root-9", ArtifactCount: 9}
	pending := proofMirrorPendingArtifactIDs(pendingTestManifest(), syncState, models.PublicProofRootResponse{ProofRootSHA256: "root-9"})
	assert.Empty(t, pending)
}
