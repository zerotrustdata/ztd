// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Every public record kind carries the release, release basis, and source
// revision its campaign was measured under, for recorded, asserted, and unknown
// release provenance alike.
func TestCampaignPublication_ReleaseProvenanceReachesEveryPublicRecord(t *testing.T) {
	for _, tc := range []struct {
		name, release, revision string
		basis                   ReleaseBasis
		publicBasis             evalv1.PublicReleaseBasis
		legacy                  bool
	}{
		{name: "recorded", release: testPlatform.Release, basis: ReleaseBasisRecorded, publicBasis: evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_RECORDED, revision: testPlatform.SourceRevision},
		{name: "asserted", release: "v2.2.7", basis: ReleaseBasisAsserted, publicBasis: evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_ASSERTED, legacy: true},
		{name: "unknown", basis: ReleaseBasisUnknown, publicBasis: evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_UNKNOWN, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newArchiveFixture(t)
			runID := f.runIDs[0]
			if tc.legacy {
				saveLegacyCampaign(t, f, "legacy-public")
				run, err := f.controller.StartRun(ctx, RunStartRequest{Governance: testGovernance,
					CampaignID:                "legacy-public",
					RunID:                     "legacy-run",
					PlatformRelease:           testPlatform.Release,
					ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
					ModelProvenancePolicy:     evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
				})
				require.NoError(t, err)
				runID = run.GetRunId()
				_, err = f.controller.ScheduleRun(ctx, runID)
				require.NoError(t, err)
				if tc.release != "" {
					require.NoError(t, f.store.TagCampaignRelease(ctx, "legacy-public", tc.release, archiveTestNow))
				}
			}
			exporter := &recordingCampaignFeedExporter{}
			coordinator := NewCampaignPublicationCoordinator(f.store, f.files, NewMemoryCampaignPublicationStateStore(), exporter)
			f.controller.WithPublication(coordinator)
			_, err := coordinator.PublishRunCatchUp(ctx, runID)
			require.NoError(t, err)
			executeTestAssignments(t, f.controller, f.req, runID, 3)
			_, err = coordinator.PublishRunCompletion(ctx, runID, archiveTestNow)
			require.NoError(t, err)
			run, err := f.store.LoadRun(ctx, runID)
			require.NoError(t, err)
			report := buildBoundTestReport(t, f.store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS)
			_, err = coordinator.PublishRunVerification(ctx, runID, report)
			require.NoError(t, err)

			kinds := map[string]bool{}
			for _, published := range exporter.records {
				var body struct {
					Kind        string          `json:"kind"`
					MessageType string          `json:"message_type"`
					Record      json.RawMessage `json:"record"`
				}
				require.NoError(t, json.Unmarshal([]byte(published.RecordBytes), &body))
				payload := []byte(published.RecordBytes)
				basis := string(tc.basis)
				// Protobuf-owned records (assignment messages and live events)
				// carry the enum name; only the view snapshots keep the lowercase form.
				if body.MessageType != "" {
					payload = body.Record
					body.Kind = body.MessageType
				}
				if body.MessageType != "" || published.RecordType == models.PublicFeedRecordTypeEvent {
					basis = evalv1.PublicReleaseBasis_name[int32(tc.publicBasis)]
				}
				var identity struct {
					Release        string `json:"release"`
					Basis          string `json:"release_basis"`
					SourceRevision string `json:"source_revision"`
				}
				require.NoError(t, json.Unmarshal(payload, &identity))
				assert.Equal(t, tc.release, identity.Release, body.Kind)
				assert.Equal(t, basis, identity.Basis, body.Kind)
				assert.Equal(t, tc.revision, identity.SourceRevision, body.Kind)
				kinds[body.Kind] = true
			}
			for _, kind := range []string{publicMessageTypeAssignmentLifecycle, publicMessageTypeAssignmentResult, "catalog_snapshot", "evaluation_summary", "model_summary", "methodology_snapshot", "stage_updated", "metric_updated"} {
				assert.True(t, kinds[kind], "missing public record %s", kind)
			}
		})
	}
}

// A malformed release tag stops catch-up before any public record is exported.
func TestCampaignPublication_RejectsMalformedReleaseTags(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	saveLegacyCampaign(t, f, "legacy-public")
	run, err := f.controller.StartRun(ctx, RunStartRequest{Governance: testGovernance,
		CampaignID:                "legacy-public",
		RunID:                     "legacy-run",
		PlatformRelease:           testPlatform.Release,
		ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
		ModelProvenancePolicy:     evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
	})
	require.NoError(t, err)
	_, err = f.controller.ScheduleRun(ctx, run.GetRunId())
	require.NoError(t, err)
	require.NoError(t, f.files.WriteFile(ctx, f.store.layout.campaignReleaseTagPath("legacy-public"), []byte(`{"campaign_id":"another","release":"v2.2.7"}`), constants.PermFilePrivate))
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(f.store, f.files, NewMemoryCampaignPublicationStateStore(), exporter)
	_, err = coordinator.PublishRunCatchUp(ctx, run.GetRunId())
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	assert.Empty(t, exporter.records)
}
