// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// TestCampaignWitnessEvidence_DecoupledObservationAndProvenance exercises the
// Gateway witness capture and the verification that reads its sealed copies, for
// provider-boundary observation and storage-side model provenance over a real
// filesystem-backed evaluation store.
func TestCampaignWitnessEvidence_DecoupledObservationAndProvenance(t *testing.T) {
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	store := NewStore(fileSvc)

	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	idGen := func(prefix string) string { return prefix + "-integ" }
	controller := NewCampaignController(store, &stubCampaignExecutor{}, clock, idGen)

	req := testCampaignInitRequest(t)
	spec, err := controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount,
		Platform:          req.Platform,
	})
	require.NoError(t, err)
	require.NotNil(t, spec)

	// Idempotent re-creation across source revisions must succeed against real store
	recreated, err := controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount,
		Platform:          req.Platform,
	})
	require.NoError(t, err)
	assert.Equal(t, spec.GetCampaignDigest(), recreated.GetCampaignDigest())

	// Parameter drift must trigger campaign conflict
	_, err = controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount + 1,
		Platform:          req.Platform,
	})
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)

	// Initialize and schedule run
	run, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	count, err := controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	assignments, err := store.ListAssignments(context.Background(), req.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, assignments)

	// The completed assignment's scored inference names a provider attempt the
	// Gateway holds a window and record for.
	const attemptID = "attempt-integ"
	first := assignments[0]
	inferences := StubHomogeneousAssignmentModelInferences(first)
	require.NotEmpty(t, inferences)
	inferences[0].ProviderAttemptId = attemptID
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    first.GetAssignmentId(),
		RunId:           req.RunID,
		CampaignId:      req.CampaignID,
		Lane:            first.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		CompletedAt:     timestamppb.Now(),
		ModelInferences: inferences,
	}
	resultDigest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = resultDigest
	require.NoError(t, store.SaveAssignmentResult(context.Background(), result))

	window, attempt := testProviderObservationWindow(t, attemptID)
	provenanceWindow := testModelProvenanceWindow(t, attemptID)
	witness := newWitnessCaptureForTest(t, store,
		&stubProviderObservationRemote{window: window, attempt: attempt},
		&stubModelProvenanceRemote{window: provenanceWindow})

	ctx := context.Background()

	// Capture seals the Gateway windows into the run's evidence.
	require.NoError(t, witness.CaptureRunProviderObservationEvidence(ctx, req.RunID))
	require.NoError(t, witness.CaptureRunModelProvenanceEvidence(ctx, req.RunID))
	_, err = store.LoadProviderObservationWindow(ctx, req.RunID, attemptID)
	require.NoError(t, err)
	_, err = store.LoadModelProvenanceWindow(ctx, req.RunID, attemptID)
	require.NoError(t, err)
	inferenceExists, err := fileSvc.FileExists(ctx, filepath.Join(constants.DataDirname, constants.InferenceDirname))
	require.NoError(t, err)
	assert.False(t, inferenceExists, "capture must not write under data/inference")

	// Verification reads only the sealed copies.
	observations, err := NewCampaignProviderObservationReader(store)
	require.NoError(t, err)
	provenance, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	verifier := NewCampaignRunVerifier(clock).
		WithProviderObservationReader(observations, ProviderObservationPolicyInterim).
		WithModelProvenanceReader(provenance, ModelProvenancePolicyInterim)

	report, err := verifier.VerifyRun(ctx, store, req.RunID, req.Catalog, req.ScenarioArtifacts)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, run.GetRunId(), report.GetRunId())
}
