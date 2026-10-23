// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestProofIndexPendingAfter_ResumesOnlyMatchingPrefixes(t *testing.T) {
	index := newPublicProofIndex()
	index.commit(models.PublicProofManifestSegment{SegmentIndex: 1, ProofRootSHA256: "root-1"}, 0, 10)
	index.commit(models.PublicProofManifestSegment{SegmentIndex: 2, ProofRootSHA256: "root-2"}, 10, 20)
	for _, tc := range []struct {
		name    string
		root    models.PublicProofRootResponse
		roots   []string
		start   int64
		wantErr error
	}{
		{name: "empty mirror", roots: []string{"root-1", "root-2"}},
		{name: "legacy mirror", root: models.PublicProofRootResponse{ProofRootSHA256: "legacy"}, roots: []string{"root-1", "root-2"}},
		{name: "partial prefix", root: models.PublicProofRootResponse{SegmentIndex: 1, ProofRootSHA256: "root-1"}, roots: []string{"root-2"}, start: 10},
		{name: "current mirror", root: models.PublicProofRootResponse{SegmentIndex: 2, ProofRootSHA256: "root-2"}, roots: []string{}, start: 30},
		{name: "divergent mirror", root: models.PublicProofRootResponse{SegmentIndex: 1, ProofRootSHA256: "other"}, wantErr: constants.ErrPublicFeedProofSegmentEquivocation},
		{name: "advanced mirror", root: models.PublicProofRootResponse{SegmentIndex: 3}, wantErr: constants.ErrPublicFeedProofSegmentOutOfOrder},
		{name: "negative index", root: models.PublicProofRootResponse{SegmentIndex: -1}, wantErr: constants.ErrPublicFeedProofSegmentOutOfOrder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending, err := index.pendingAfter(tc.root)
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr != nil {
				return
			}
			assert.Equal(t, tc.roots, pending.roots)
			assert.Equal(t, tc.start, pending.start)
			assert.Equal(t, int64(30), pending.end)
		})
	}
}

func TestProofIndexCheck_EnforcesMirrorChainRules(t *testing.T) {
	const idA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const idB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	index := newPublicProofIndex()
	genesis := models.PublicProofManifestSegment{
		SegmentIndex:    1,
		PreviousRoot:    constants.PublicFeedZeroHashHex,
		ProofRootSHA256: "root-1",
		Entries:         []models.PublicProofCatalogEntry{{ArtifactID: idA, SourceRunID: "run-a"}},
	}
	require.NoError(t, index.check(genesis))
	index.commit(genesis, 0, 10)
	withdrawal := models.PublicProofManifestSegment{SegmentIndex: 2, PreviousRoot: "root-1", ProofRootSHA256: "root-2", WithdrawnArtifactIDs: []string{idA}}
	require.NoError(t, index.check(withdrawal))
	index.commit(withdrawal, 10, 10)
	assert.Empty(t, index.catalog().Entries)
	assert.Empty(t, index.activeRunArtifactIDs("run-a"))

	for _, tc := range []struct {
		name    string
		segment models.PublicProofManifestSegment
		wantErr error
	}{
		{name: "gap", segment: models.PublicProofManifestSegment{SegmentIndex: 4, PreviousRoot: "root-2"}, wantErr: constants.ErrPublicFeedProofSegmentOutOfOrder},
		{name: "fork", segment: models.PublicProofManifestSegment{SegmentIndex: 3, PreviousRoot: "root-1"}, wantErr: constants.ErrPublicFeedProofSegmentOutOfOrder},
		{name: "relist withdrawn", segment: models.PublicProofManifestSegment{SegmentIndex: 3, PreviousRoot: "root-2", Entries: []models.PublicProofCatalogEntry{{ArtifactID: idA}}}, wantErr: constants.ErrPublicFeedProofSegmentEquivocation},
		{name: "withdraw inactive", segment: models.PublicProofManifestSegment{SegmentIndex: 3, PreviousRoot: "root-2", WithdrawnArtifactIDs: []string{idB}}, wantErr: constants.ErrPublicFeedProofSegmentInvalid},
		{name: "next link", segment: models.PublicProofManifestSegment{SegmentIndex: 3, PreviousRoot: "root-2", Entries: []models.PublicProofCatalogEntry{{ArtifactID: idB}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, index.check(tc.segment), tc.wantErr)
		})
	}
}
