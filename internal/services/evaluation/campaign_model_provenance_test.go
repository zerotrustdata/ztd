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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func testModelProvenanceWindow(t *testing.T, providerAttemptID string) *evalv1.ModelProvenanceAttestationWindow {
	t.Helper()
	digest := strings.Repeat("a", 64)
	window := &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              model_provenance.SchemaVersion,
		ProviderAttemptId:          providerAttemptID,
		ProvenanceOperatorId:       "test-provenance-operator",
		ServedModelTag:             "probe-model:7b",
		ExpectedModelDigest:        digest,
		ObservedModelDigest:        digest,
		ManifestDigest:             strings.Repeat("b", 64),
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED,
		AttestedAtUnixMs:           1_700_000_000_000,
		DigestMatch:                true,
	}
	attestationDigest, err := model_provenance.ComputeAttestationDigest(window)
	require.NoError(t, err)
	window.AttestationDigest = attestationDigest
	return window
}

func TestVerifyModelProvenanceWindow_InterimPolicy_AllowsMissingWindow(t *testing.T) {
	t.Parallel()
	err := VerifyModelProvenanceWindow(nil, strings.Repeat("a", 64), ModelProvenancePolicyInterim)
	assert.NoError(t, err)
}

func TestVerifyModelProvenanceWindow_StrictPolicy_RequiresWindow(t *testing.T) {
	t.Parallel()
	err := VerifyModelProvenanceWindow(nil, strings.Repeat("a", 64), ModelProvenancePolicyStrict)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
}

func TestVerifyModelProvenanceWindow_StrictPolicy_RequiresDigestMatch(t *testing.T) {
	t.Parallel()
	window := testModelProvenanceWindow(t, "attempt-1")
	window.DigestMatch = false
	attestationDigest, err := model_provenance.ComputeAttestationDigest(window)
	require.NoError(t, err)
	window.AttestationDigest = attestationDigest

	err = VerifyModelProvenanceWindow(window, window.GetExpectedModelDigest(), ModelProvenancePolicyStrict)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrModelProvenanceDigestMismatch)
}

func TestVerifyModelProvenanceWindow_ExpectedDigestMismatch(t *testing.T) {
	t.Parallel()
	window := testModelProvenanceWindow(t, "attempt-1")
	err := VerifyModelProvenanceWindow(window, strings.Repeat("c", 64), ModelProvenancePolicyInterim)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrModelProvenanceDigestMismatch)
}

func TestStore_ModelProvenanceWindowRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	window := testModelProvenanceWindow(t, "attempt-1")
	require.NoError(t, store.SaveModelProvenanceWindow(ctx, "run-1", window))

	loaded, err := store.LoadModelProvenanceWindow(ctx, "run-1", "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, window.GetAttestationDigest(), loaded.GetAttestationDigest())
	_, err = store.LoadModelProvenanceWindow(ctx, "run-1", "attempt-absent")
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestStore_SaveModelProvenanceWindowRejectsInvalidWindow(t *testing.T) {
	t.Parallel()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	window := testModelProvenanceWindow(t, "attempt-1")
	window.AttestationDigest = "invalid"

	err := store.SaveModelProvenanceWindow(t.Context(), "run-1", window)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationReportPersistFailed)
}

func TestStore_LoadModelProvenanceWindowRejectsMissingProviderAttemptID(t *testing.T) {
	t.Parallel()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	_, err := store.LoadModelProvenanceWindow(t.Context(), "run-1", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

type stubModelProvenanceRemote struct {
	window *evalv1.ModelProvenanceAttestationWindow
}

func (s *stubModelProvenanceRemote) Load(_ context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if s == nil || s.window == nil || s.window.GetProviderAttemptId() != providerAttemptID {
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

func TestCampaignModelProvenanceReader_CaptureAssignmentEvidencePersistsGatewayEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	store := NewStore(fileSvc)
	window := testModelProvenanceWindow(t, "attempt-captured")
	reader, err := NewCampaignModelProvenanceReaderWithRemote(store, &stubModelProvenanceRemote{window: window})
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
		RunId:           "run-1",
		ModelInferences: []*evalv1.ModelInferenceRecord{{ProviderAttemptId: window.GetProviderAttemptId(), ModelVariant: &evalv1.ModelVariant{ModelDigest: window.GetExpectedModelDigest()}}},
	}

	require.NoError(t, reader.CaptureAssignmentEvidence(ctx, result))

	// Capture seals the fetched evidence into the run's evaluation evidence and
	// never writes the production inference stores.
	inferenceExists, err := fileSvc.FileExists(ctx, filepath.Join(constants.DataDirname, constants.InferenceDirname))
	require.NoError(t, err)
	assert.False(t, inferenceExists, "capture must not write under data/inference")

	localReader, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	failures, unavailable := localReader.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
}

// TestCampaignModelProvenanceReader_NeverReadsGatewayOnLocalMiss pins that reads
// come only from the run's sealed copies: a local miss is unavailable, and the
// gateway remote is never consulted.
func TestCampaignModelProvenanceReader_NeverReadsGatewayOnLocalMiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-remote"))
	remote.isRecorded = true
	reader, err := NewCampaignModelProvenanceReaderWithRemote(store, remote)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
		RunId: "run-remote",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inference-1",
			ProviderAttemptId: "attempt-remote",
			ModelVariant:      &evalv1.ModelVariant{ModelDigest: strings.Repeat("a", 64)},
		}},
	}

	failures, unavailable := reader.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	assert.NotEmpty(t, failures)
	assert.Equal(t, []string{"model_provenance_missing:attempt-remote"}, unavailable)
	assert.Zero(t, remote.loadCount(), "reads must never consult the gateway")

	_, err = store.LoadModelProvenanceWindow(ctx, "run-remote", "attempt-remote")
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestCampaignModelProvenanceReader_VerifyAssignmentModelProvenance_StrictMissingWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	reader, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
		RunId: "run-1",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inf-1",
			ProviderAttemptId: "attempt-missing",
			ModelVariant:      &evalv1.ModelVariant{ModelDigest: strings.Repeat("a", 64)},
		}},
	}
	failures, unavailable := reader.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	require.NotEmpty(t, failures)
	require.NotEmpty(t, unavailable)
}

func TestCampaignModelProvenanceReader_VerifyAssignmentModelProvenance_StrictDigestMatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	window := testModelProvenanceWindow(t, "attempt-1")
	require.NoError(t, store.SaveModelProvenanceWindow(ctx, "run-1", window))
	reader, err := NewCampaignModelProvenanceReader(store)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
		RunId: "run-1",
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inf-1",
			ProviderAttemptId: "attempt-1",
			ModelVariant:      &evalv1.ModelVariant{ModelDigest: window.GetExpectedModelDigest()},
		}},
	}
	failures, unavailable := reader.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
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

func TestCampaignModelProvenanceReader_WaitForEvidenceCapturesOnRecordingEvent(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-delayed"))
	store := NewStore(newCampaignMemoryFileService())
	reader, err := NewCampaignModelProvenanceReaderWithRemote(store, remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		// The connect reconciliation finds nothing; only the event can deliver it.
		<-remote.missed
		remote.record(t, "attempt-delayed")
	}()

	require.NoError(t, reader.WaitForEvidence(ctx, "run-1", []string{"attempt-delayed", "attempt-delayed"}))
	assert.Equal(t, 2, remote.loadCount())
	// Captured evidence is sealed in the run: a second wait opens no stream.
	require.NoError(t, reader.WaitForEvidence(ctx, "run-1", []string{"attempt-delayed"}))
	assert.Equal(t, 2, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidenceReconcilesOnConnect(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-early"))
	remote.isRecorded = true
	reader, err := NewCampaignModelProvenanceReaderWithRemote(NewStore(newCampaignMemoryFileService()), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.NoError(t, reader.WaitForEvidence(ctx, "run-1", []string{"attempt-early"}))
	assert.Equal(t, 1, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidenceIgnoresOtherAttempts(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-pending"))
	reader, err := NewCampaignModelProvenanceReaderWithRemote(NewStore(newCampaignMemoryFileService()), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		<-remote.missed
		remote.record(t, "attempt-unrelated")
		remote.record(t, "attempt-pending")
	}()

	require.NoError(t, reader.WaitForEvidence(ctx, "run-1", []string{"attempt-pending"}))
	// Reconcile plus the matching event; the unrelated recording costs no read.
	assert.Equal(t, 2, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidenceReportsMissingOnDeadline(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-never"))
	reader, err := NewCampaignModelProvenanceReaderWithRemote(NewStore(newCampaignMemoryFileService()), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()

	err = reader.WaitForEvidence(ctx, "run-1", []string{"attempt-never"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	assert.Contains(t, err.Error(), "1 missing")
	assert.Contains(t, err.Error(), "attempt-never")
	// No recording event arrived, so nothing was re-read after the reconcile.
	assert.Equal(t, 1, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidencePropagatesReadFailure(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-failed"))
	remote.loadErr = constants.ErrServiceUnavailable
	reader, err := NewCampaignModelProvenanceReaderWithRemote(NewStore(newCampaignMemoryFileService()), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.ErrorIs(t, reader.WaitForEvidence(ctx, "run-1", []string{"attempt-failed"}), constants.ErrServiceUnavailable)
	assert.Equal(t, 1, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidenceFailsWhenStreamEnds(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-orphaned"))
	remote.watchErr = constants.ErrCLISessionRefreshRequired
	reader, err := NewCampaignModelProvenanceReaderWithRemote(NewStore(newCampaignMemoryFileService()), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err = reader.WaitForEvidence(ctx, "run-1", []string{"attempt-orphaned"})
	require.ErrorIs(t, err, constants.ErrCLISessionRefreshRequired)
	assert.Contains(t, err.Error(), "attempt-orphaned")
}

func TestCampaignModelProvenanceReader_WaitForEvidenceWithoutGatewayFailsFast(t *testing.T) {
	reader, err := NewCampaignModelProvenanceReader(NewStore(newCampaignMemoryFileService()))
	require.NoError(t, err)

	err = reader.WaitForEvidence(t.Context(), "run-1", []string{"attempt-missing"})
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	require.ErrorIs(t, err, constants.ErrGatewayNotReachable)
	assert.Contains(t, err.Error(), "attempt-missing")
}

func TestCampaignModelProvenanceReader_WaitForEvidenceHonorsCancellation(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-cancelled"))
	reader, err := NewCampaignModelProvenanceReaderWithRemote(NewStore(newCampaignMemoryFileService()), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-remote.missed
		cancel()
	}()
	require.ErrorIs(t, reader.WaitForEvidence(ctx, "run-1", []string{"attempt-cancelled"}), context.Canceled)
}

func TestCampaignModelProvenanceReader_WaitForEvidenceRejectsInvalidWindow(t *testing.T) {
	remote := &stubModelProvenanceRemote{window: testModelProvenanceWindow(t, "attempt-invalid")}
	remote.window.AttestationDigest = "invalid"
	reader, err := NewCampaignModelProvenanceReaderWithRemote(NewStore(newCampaignMemoryFileService()), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.Error(t, reader.WaitForEvidence(ctx, "run-1", []string{"attempt-invalid"}))
}
