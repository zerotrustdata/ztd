// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// stubProviderObservationRemote serves one provider attempt's window and record,
// and records every attempt the capture asks it for.
type stubProviderObservationRemote struct {
	window    *evalv1.ProviderBoundaryObservationWindow
	attempt   *operatorv1.InferenceProviderAttemptRecord
	loadCalls []string
}

func (s *stubProviderObservationRemote) Load(_ context.Context, providerAttemptID string) (*evalv1.ProviderBoundaryObservationWindow, *operatorv1.InferenceProviderAttemptRecord, error) {
	s.loadCalls = append(s.loadCalls, providerAttemptID)
	if s.window == nil || s.attempt == nil || s.window.GetProviderAttemptId() != providerAttemptID {
		return nil, nil, constants.ErrNotFound
	}
	return s.window, s.attempt, nil
}

// WatchRecorded connects once and delivers no recordings: the connect reconcile
// alone must capture an already-recorded window.
func (s *stubProviderObservationRemote) WatchRecorded(ctx context.Context, connected func(), _ func(string)) error {
	connected()
	<-ctx.Done()
	return nil
}

// stubModelProvenanceRemote serves one attempt's attestation window, and records
// every attempt the capture asks it for.
type stubModelProvenanceRemote struct {
	window    *evalv1.ModelProvenanceAttestationWindow
	loadCalls []string
}

func (s *stubModelProvenanceRemote) Load(_ context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	s.loadCalls = append(s.loadCalls, providerAttemptID)
	if s.window == nil || s.window.GetProviderAttemptId() != providerAttemptID {
		return nil, constants.ErrNotFound
	}
	return s.window, nil
}

// WatchRecorded connects once and delivers no recordings: the connect
// reconciliation alone must capture every window the stub already holds.
func (s *stubModelProvenanceRemote) WatchRecorded(ctx context.Context, connected func(), _ func(string)) error {
	connected()
	<-ctx.Done()
	return nil
}

// recordingProvenanceRemote models the Gateway: a window becomes readable
// only once it is recorded, and each recording is announced on the stream.
type recordingProvenanceRemote struct {
	window   *evalv1.ModelProvenanceAttestationWindow
	loadErr  error
	watchErr error
	events   chan string
	missed   chan struct{}

	mu         sync.Mutex
	isRecorded bool
	loads      int
	missOnce   sync.Once
}

func newRecordingProvenanceRemote(window *evalv1.ModelProvenanceAttestationWindow) *recordingProvenanceRemote {
	return &recordingProvenanceRemote{window: window, events: make(chan string), missed: make(chan struct{})}
}

func (r *recordingProvenanceRemote) Load(ctx context.Context, id string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	if !r.isRecorded || id != r.window.GetProviderAttemptId() {
		r.missOnce.Do(func() { close(r.missed) })
		return nil, constants.ErrNotFound
	}
	return r.window, nil
}

func (r *recordingProvenanceRemote) WatchRecorded(ctx context.Context, connected func(), recorded func(string)) error {
	connected()
	if r.watchErr != nil {
		return r.watchErr
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case id := <-r.events:
			recorded(id)
		}
	}
}

// record persists the window Gateway-side, then announces it.
func (r *recordingProvenanceRemote) record(t *testing.T, id string) {
	t.Helper()
	r.mu.Lock()
	r.isRecorded = id == r.window.GetProviderAttemptId()
	r.mu.Unlock()
	select {
	case r.events <- id:
	case <-t.Context().Done():
	}
}

func (r *recordingProvenanceRemote) loadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loads
}

// testProviderObservationWindow returns one complete provider attempt's window and
// record, with the window digest bound.
func testProviderObservationWindow(t *testing.T, providerAttemptID string) (*evalv1.ProviderBoundaryObservationWindow, *operatorv1.InferenceProviderAttemptRecord) {
	t.Helper()
	attempt := &operatorv1.InferenceProviderAttemptRecord{
		ProviderAttemptId: providerAttemptID,
		TransactionId:     "transaction-" + providerAttemptID,
		Status:            operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED,
		StartedAtUnixMs:   time.Unix(1_700_000_000, 0).UnixMilli(),
		CompletedAtUnixMs: time.Unix(1_700_000_010, 0).UnixMilli(),
	}
	window := &evalv1.ProviderBoundaryObservationWindow{
		SchemaVersion:              provider_observer.SchemaVersion,
		ProviderAttemptId:          providerAttemptID,
		ObserverId:                 "observer-test",
		ObserverClockSource:        provider_observer.DefaultObserverClockSource,
		WindowStartedAtUnixNanos:   uint64(time.Unix(1_700_000_000, 0).UnixNano()),
		WindowCompletedAtUnixNanos: uint64(time.Unix(1_700_000_010, 0).UnixNano()),
		AttemptStartedAtUnixMs:     attempt.GetStartedAtUnixMs(),
		AttemptCompletedAtUnixMs:   attempt.GetCompletedAtUnixMs(),
		Samples: []*evalv1.ProviderBoundaryHardwareSample{{
			ObservedAtUnixNanos:        uint64(time.Unix(1_700_000_001, 0).UnixNano()),
			GpuUtilizationAvailability: evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED,
			GpuUtilizationPercent:      10,
		}},
	}
	digest, err := provider_observer.ComputeObservationDigest(window)
	require.NoError(t, err)
	window.ObservationDigest = digest
	return window, attempt
}

// newWitnessCaptureForTest builds the capture over store with the given remotes.
func newWitnessCaptureForTest(t *testing.T, store *Store, providers ProviderObservationRemote, provenance ModelProvenanceRemote) *CampaignWitnessCapture {
	t.Helper()
	capture, err := NewCampaignWitnessCapture(store, providers, provenance)
	require.NoError(t, err)
	return capture
}

// withScoredAttempts persists the fixture's completed result with one scored
// inference per attempt ID, so the run names those attempts as witnessed.
func (f completedRunFixture) withScoredAttempts(t *testing.T, attemptIDs ...string) {
	t.Helper()
	result := proto.Clone(f.imported).(*evalv1.EvaluationAssignmentResult)
	result.ModelInferences = make([]*evalv1.ModelInferenceRecord, 0, len(attemptIDs))
	for _, id := range attemptIDs {
		result.ModelInferences = append(result.ModelInferences, &evalv1.ModelInferenceRecord{ProviderAttemptId: id})
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	require.NoError(t, f.store.SaveAssignmentResult(context.Background(), result))
}

func TestCampaignWitnessCapture_RequiresStoreAndBothRemotes(t *testing.T) {
	t.Parallel()
	store := NewStore(newCampaignMemoryFileService())
	providers := &stubProviderObservationRemote{}
	provenance := &stubModelProvenanceRemote{}
	tests := []struct {
		name       string
		store      *Store
		providers  ProviderObservationRemote
		provenance ModelProvenanceRemote
	}{
		{name: "missing store", providers: providers, provenance: provenance},
		{name: "missing provider observation remote", store: store, provenance: provenance},
		{name: "missing model provenance remote", store: store, providers: providers},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewCampaignWitnessCapture(tt.store, tt.providers, tt.provenance)
			require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		})
	}
}

func TestCampaignWitnessCapture_SealsBothArtifacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	store := NewStore(fileSvc)
	window, attempt := testProviderObservationWindow(t, "attempt-captured")
	provenanceWindow := testModelProvenanceWindow(t, "attempt-captured")
	capture := newWitnessCaptureForTest(t, store,
		&stubProviderObservationRemote{window: window, attempt: attempt},
		&stubModelProvenanceRemote{window: provenanceWindow})
	result := &evalv1.EvaluationAssignmentResult{
		RunId: "run-captured",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			ProviderAttemptId: attempt.GetProviderAttemptId(),
			ModelVariant:      &evalv1.ModelVariant{ModelDigest: provenanceWindow.GetExpectedModelDigest()},
		}},
	}

	require.NoError(t, capture.captureAssignmentProviderEvidence(ctx, result))
	require.NoError(t, capture.captureAssignmentModelProvenance(ctx, result))

	// Capture seals the fetched evidence into the run's evaluation evidence and
	// never writes the production inference stores.
	inferenceExists, err := fileSvc.FileExists(ctx, filepath.Join(constants.DataDirname, constants.InferenceDirname))
	require.NoError(t, err)
	assert.False(t, inferenceExists, "capture must not write under data/inference")

	_, err = store.LoadProviderObservationWindow(ctx, "run-captured", "attempt-captured")
	require.NoError(t, err)
	_, err = store.LoadProviderAttemptRecord(ctx, "run-captured", "attempt-captured")
	require.NoError(t, err)
	_, err = store.LoadModelProvenanceWindow(ctx, "run-captured", "attempt-captured")
	require.NoError(t, err)

	// The sealed copies verify without any Gateway remote.
	observations, err := NewCampaignProviderObservationReader(store)
	require.NoError(t, err)
	failures, unavailable := observations.VerifyAssignmentProviderObservations(ctx, result, ProviderObservationPolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
	provenance, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	failures, unavailable = provenance.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
}

func TestCampaignWitnessCapture_SkipsAttemptsAlreadySealed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	const runID = "run-skip"

	sealedWindow, sealedAttempt := testProviderObservationWindow(t, "attempt-sealed")
	require.NoError(t, store.SaveProviderObservationWindow(ctx, runID, sealedWindow))
	require.NoError(t, store.SaveProviderAttemptRecord(ctx, runID, sealedAttempt))
	require.NoError(t, store.SaveModelProvenanceWindow(ctx, runID, testModelProvenanceWindow(t, "attempt-sealed")))

	newWindow, newAttempt := testProviderObservationWindow(t, "attempt-new")
	providers := &stubProviderObservationRemote{window: newWindow, attempt: newAttempt}
	provenance := &stubModelProvenanceRemote{window: testModelProvenanceWindow(t, "attempt-new")}
	capture := newWitnessCaptureForTest(t, store, providers, provenance)
	result := &evalv1.EvaluationAssignmentResult{
		RunId: runID,
		ModelInferences: []*evalv1.ModelInferenceRecord{
			{ProviderAttemptId: "attempt-sealed"},
			{ProviderAttemptId: "attempt-new"},
		},
	}

	require.NoError(t, capture.captureAssignmentProviderEvidence(ctx, result))
	require.NoError(t, capture.captureAssignmentModelProvenance(ctx, result))

	// The Gateway is asked only for the attempt the run does not hold yet.
	assert.Equal(t, []string{"attempt-new"}, providers.loadCalls)
	assert.Equal(t, []string{"attempt-new"}, provenance.loadCalls)
	_, err := store.LoadProviderObservationWindow(ctx, runID, "attempt-new")
	require.NoError(t, err)
	_, err = store.LoadModelProvenanceWindow(ctx, runID, "attempt-new")
	require.NoError(t, err)
}

func TestCampaignWitnessCapture_RunMethodsRejectEmptyRunID(t *testing.T) {
	t.Parallel()
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, &stubModelProvenanceRemote{})
	ctx := context.Background()

	require.ErrorIs(t, capture.CaptureRunProviderObservationEvidence(ctx, ""), constants.ErrMissingRequiredField)
	require.ErrorIs(t, capture.CaptureRunModelProvenanceEvidence(ctx, ""), constants.ErrMissingRequiredField)
	require.ErrorIs(t, capture.WaitForRunModelProvenanceEvidence(ctx, ""), constants.ErrMissingRequiredField)
}

func TestCampaignWitnessCapture_WaitForRunProvenanceCoversScoredAttemptsOnly(t *testing.T) {
	fixture := newCompletedRunFixture(t, nil)
	fixture.withScoredAttempts(t, "attempt-scored")
	capture := newWitnessCaptureForTest(t, fixture.store, &stubProviderObservationRemote{},
		&stubModelProvenanceRemote{window: testModelProvenanceWindow(t, "attempt-scored")})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	require.NoError(t, capture.WaitForRunModelProvenanceEvidence(ctx, fixture.req.RunID))
	// Other scheduled assignments have no result and require no windows.
	window, err := fixture.store.LoadModelProvenanceWindow(ctx, fixture.req.RunID, "attempt-scored")
	require.NoError(t, err)
	assert.Equal(t, "attempt-scored", window.GetProviderAttemptId())
}

func TestCampaignWitnessCapture_WaitForProvenanceCapturesOnRecordingEvent(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-delayed"))
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, remote)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		// The connect reconciliation finds nothing; only the event can deliver it.
		<-remote.missed
		remote.record(t, "attempt-delayed")
	}()

	require.NoError(t, capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-delayed", "attempt-delayed"}))
	assert.Equal(t, 2, remote.loadCount())
	// Captured evidence is sealed in the run: a second wait opens no stream.
	require.NoError(t, capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-delayed"}))
	assert.Equal(t, 2, remote.loadCount())
}

func TestCampaignWitnessCapture_WaitForProvenanceReconcilesOnConnect(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-early"))
	remote.isRecorded = true
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, remote)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.NoError(t, capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-early"}))
	assert.Equal(t, 1, remote.loadCount())
}

func TestCampaignWitnessCapture_WaitForProvenanceIgnoresOtherAttempts(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-pending"))
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, remote)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		<-remote.missed
		remote.record(t, "attempt-unrelated")
		remote.record(t, "attempt-pending")
	}()

	require.NoError(t, capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-pending"}))
	// Reconcile plus the matching event; the unrelated recording costs no read.
	assert.Equal(t, 2, remote.loadCount())
}

func TestCampaignWitnessCapture_WaitForProvenanceReportsMissingOnDeadline(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-never"))
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, remote)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()

	err := capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-never"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	assert.Contains(t, err.Error(), "1 missing")
	assert.Contains(t, err.Error(), "attempt-never")
	// No recording event arrived, so nothing was re-read after the reconcile.
	assert.Equal(t, 1, remote.loadCount())
}

func TestCampaignWitnessCapture_WaitForProvenancePropagatesReadFailure(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-failed"))
	remote.loadErr = constants.ErrServiceUnavailable
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, remote)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.ErrorIs(t, capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-failed"}), constants.ErrServiceUnavailable)
	assert.Equal(t, 1, remote.loadCount())
}

func TestCampaignWitnessCapture_WaitForProvenanceFailsWhenStreamEnds(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-orphaned"))
	remote.watchErr = constants.ErrCLISessionRefreshRequired
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, remote)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-orphaned"})
	require.ErrorIs(t, err, constants.ErrCLISessionRefreshRequired)
	assert.Contains(t, err.Error(), "attempt-orphaned")
}

func TestCampaignWitnessCapture_WaitForProvenanceHonorsCancellation(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-cancelled"))
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, remote)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-remote.missed
		cancel()
	}()
	require.ErrorIs(t, capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-cancelled"}), context.Canceled)
}

func TestCampaignWitnessCapture_WaitForProvenanceRejectsInvalidWindow(t *testing.T) {
	remote := &stubModelProvenanceRemote{window: testModelProvenanceWindow(t, "attempt-invalid")}
	remote.window.AttestationDigest = "invalid"
	capture := newWitnessCaptureForTest(t, NewStore(newCampaignMemoryFileService()), &stubProviderObservationRemote{}, remote)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.Error(t, capture.waitForModelProvenanceEvidence(ctx, "run-1", []string{"attempt-invalid"}))
}
