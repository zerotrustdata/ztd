// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type recordingProviderObservationRemote struct {
	loadCalls []string
	window    *evalv1.ProviderBoundaryObservationWindow
	attempt   *operatorv1.InferenceProviderAttemptRecord
}

func (s *recordingProviderObservationRemote) Load(_ context.Context, providerAttemptID string) (*evalv1.ProviderBoundaryObservationWindow, *operatorv1.InferenceProviderAttemptRecord, error) {
	s.loadCalls = append(s.loadCalls, providerAttemptID)
	if s.window == nil || s.attempt == nil || s.window.GetProviderAttemptId() != providerAttemptID {
		return nil, nil, constants.ErrNotFound
	}
	return s.window, s.attempt, nil
}

func (s *recordingProviderObservationRemote) WatchRecorded(ctx context.Context, connected func(), _ func(string)) error {
	connected()
	<-ctx.Done()
	return nil
}

func TestCampaignProviderObservationReader_ReadsGatewayOnLocalMissWithoutPersisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	remote := &recordingProviderObservationRemote{
		window: &evalv1.ProviderBoundaryObservationWindow{
			SchemaVersion:       provider_observer.SchemaVersion,
			ProviderAttemptId:   "attempt-remote",
			ObserverId:          "observer-test",
			ObserverClockSource: provider_observer.DefaultObserverClockSource,
		},
		attempt: &operatorv1.InferenceProviderAttemptRecord{
			ProviderAttemptId: "attempt-remote",
			Status:            operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED,
		},
	}
	digest, err := provider_observer.ComputeObservationDigest(remote.window)
	require.NoError(t, err)
	remote.window.ObservationDigest = digest

	store := NewStore(fileSvc)
	reader, err := NewCampaignProviderObservationReaderWithRemote(store, remote)
	require.NoError(t, err)

	loadedWindow, err := reader.loadWindow(ctx, "run-remote", "attempt-remote")
	require.NoError(t, err)
	assert.Equal(t, "attempt-remote", loadedWindow.GetProviderAttemptId())
	loadedAttempt, err := reader.loadAttempt(ctx, "run-remote", "attempt-remote")
	require.NoError(t, err)
	assert.Equal(t, "attempt-remote", loadedAttempt.GetProviderAttemptId())
	assert.Equal(t, []string{"attempt-remote", "attempt-remote"}, remote.loadCalls)

	// The gateway read-through is never persisted: the run's sealed copy stays absent.
	_, err = store.loadProviderObservationWindow(ctx, "run-remote", "attempt-remote")
	assert.ErrorIs(t, err, constants.ErrNotFound)
	_, err = store.loadProviderAttemptRecord(ctx, "run-remote", "attempt-remote")
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestBindProviderBoundaryObservationRefs_AttachesComplianceRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	window := &evalv1.ProviderBoundaryObservationWindow{
		SchemaVersion:              provider_observer.SchemaVersion,
		ProviderAttemptId:          "attempt-1",
		ObserverId:                 "observer-test",
		ObserverClockSource:        provider_observer.DefaultObserverClockSource,
		WindowStartedAtUnixNanos:   uint64(time.Unix(1_700_000_000, 0).UnixNano()),
		WindowCompletedAtUnixNanos: uint64(time.Unix(1_700_000_010, 0).UnixNano()),
	}
	digest, err := provider_observer.ComputeObservationDigest(window)
	require.NoError(t, err)
	window.ObservationDigest = digest
	store := NewStore(fileSvc)
	require.NoError(t, store.saveProviderObservationWindow(ctx, "run-bind", window))

	reader, err := NewCampaignProviderObservationReader(store)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
		RunId: "run-bind",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inference-1",
			ProviderAttemptId: "attempt-1",
		}},
	}
	require.NoError(t, reader.BindProviderBoundaryObservationRefs(ctx, result))
	ref := result.GetModelInferences()[0].GetProviderBoundaryObservationRef()
	require.NotNil(t, ref)
	assert.Equal(t, "attempt-1", ref.GetArtifactId())
	assert.Equal(t, digest, ref.GetSha256())
}

func TestToolScorecardMetricFromGrade_UsesClosedUnavailableReason(t *testing.T) {
	t.Parallel()
	metric := toolScorecardMetricFromGrade(&evalv1.DeterministicGrade{
		Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE,
	})
	require.NotNil(t, metric)
	assert.Equal(t, "source_unavailable", metric.UnavailableReason)
}

func TestProviderBoundaryObservationRef_ReturnsNilForMissingWindow(t *testing.T) {
	t.Parallel()
	assert.Nil(t, providerBoundaryObservationRef(nil))
	assert.Nil(t, providerBoundaryObservationRef(&evalv1.ProviderBoundaryObservationWindow{}))
}
