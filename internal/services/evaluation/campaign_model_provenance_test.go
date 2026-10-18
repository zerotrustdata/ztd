// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
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

func TestLocalModelProvenanceReader_LoadRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	store, err := model_provenance.NewWindowStore(fileSvc)
	require.NoError(t, err)
	window := testModelProvenanceWindow(t, "attempt-1")
	require.NoError(t, store.Save(ctx, window))

	reader := &localModelProvenanceReader{store: store}
	loaded, err := reader.Load(ctx, "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, window.GetAttestationDigest(), loaded.GetAttestationDigest())
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

func TestLocalModelProvenanceReader_RejectsMissingProviderAttemptID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	store, err := model_provenance.NewWindowStore(fileSvc)
	require.NoError(t, err)
	reader := &localModelProvenanceReader{store: store}
	_, err = reader.Load(ctx, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCampaignModelProvenanceReader_CaptureAssignmentEvidencePersistsGatewayEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	window := testModelProvenanceWindow(t, "attempt-captured")
	reader, err := NewCampaignModelProvenanceReaderWithRemote(fileSvc, &stubModelProvenanceRemote{window: window})
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{ModelInferences: []*evalv1.ModelInferenceRecord{{ProviderAttemptId: window.GetProviderAttemptId(), ModelVariant: &evalv1.ModelVariant{ModelDigest: window.GetExpectedModelDigest()}}}}

	require.NoError(t, reader.CaptureAssignmentEvidence(ctx, result))
	localReader, err := NewCampaignModelProvenanceReader(fileSvc)
	require.NoError(t, err)
	failures, unavailable := localReader.VerifyAssignmentModelProvenance(ctx, result, ModelProvenancePolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
}

func TestCampaignModelProvenanceReader_VerifyAssignmentModelProvenance_StrictMissingWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	reader, err := NewCampaignModelProvenanceReader(fileSvc)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
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
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	store, err := model_provenance.NewWindowStore(fileSvc)
	require.NoError(t, err)
	window := testModelProvenanceWindow(t, "attempt-1")
	require.NoError(t, store.Save(ctx, window))
	reader, err := NewCampaignModelProvenanceReader(fileSvc)
	require.NoError(t, err)
	result := &evalv1.EvaluationAssignmentResult{
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
	reader, err := NewCampaignModelProvenanceReaderWithRemote(newCampaignMemoryFileService(), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		// The connect reconciliation finds nothing; only the event can deliver it.
		<-remote.missed
		remote.record(t, "attempt-delayed")
	}()

	require.NoError(t, reader.WaitForEvidence(ctx, []string{"attempt-delayed", "attempt-delayed"}))
	assert.Equal(t, 2, remote.loadCount())
	// Captured evidence is persisted locally: a second wait opens no stream.
	require.NoError(t, reader.WaitForEvidence(ctx, []string{"attempt-delayed"}))
	assert.Equal(t, 2, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidenceReconcilesOnConnect(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-early"))
	remote.isRecorded = true
	reader, err := NewCampaignModelProvenanceReaderWithRemote(newCampaignMemoryFileService(), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.NoError(t, reader.WaitForEvidence(ctx, []string{"attempt-early"}))
	assert.Equal(t, 1, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidenceIgnoresOtherAttempts(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-pending"))
	reader, err := NewCampaignModelProvenanceReaderWithRemote(newCampaignMemoryFileService(), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		<-remote.missed
		remote.record(t, "attempt-unrelated")
		remote.record(t, "attempt-pending")
	}()

	require.NoError(t, reader.WaitForEvidence(ctx, []string{"attempt-pending"}))
	// Reconcile plus the matching event; the unrelated recording costs no read.
	assert.Equal(t, 2, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidenceReportsMissingOnDeadline(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-never"))
	reader, err := NewCampaignModelProvenanceReaderWithRemote(newCampaignMemoryFileService(), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()

	err = reader.WaitForEvidence(ctx, []string{"attempt-never"})
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
	reader, err := NewCampaignModelProvenanceReaderWithRemote(newCampaignMemoryFileService(), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.ErrorIs(t, reader.WaitForEvidence(ctx, []string{"attempt-failed"}), constants.ErrServiceUnavailable)
	assert.Equal(t, 1, remote.loadCount())
}

func TestCampaignModelProvenanceReader_WaitForEvidenceFailsWhenStreamEnds(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-orphaned"))
	remote.watchErr = constants.ErrCLISessionRefreshRequired
	reader, err := NewCampaignModelProvenanceReaderWithRemote(newCampaignMemoryFileService(), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err = reader.WaitForEvidence(ctx, []string{"attempt-orphaned"})
	require.ErrorIs(t, err, constants.ErrCLISessionRefreshRequired)
	assert.Contains(t, err.Error(), "attempt-orphaned")
}

func TestCampaignModelProvenanceReader_WaitForEvidenceWithoutGatewayFailsFast(t *testing.T) {
	reader, err := NewCampaignModelProvenanceReader(newCampaignMemoryFileService())
	require.NoError(t, err)

	err = reader.WaitForEvidence(t.Context(), []string{"attempt-missing"})
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	require.ErrorIs(t, err, constants.ErrGatewayNotReachable)
	assert.Contains(t, err.Error(), "attempt-missing")
}

func TestCampaignModelProvenanceReader_WaitForEvidenceHonorsCancellation(t *testing.T) {
	remote := newRecordingProvenanceRemote(testModelProvenanceWindow(t, "attempt-cancelled"))
	reader, err := NewCampaignModelProvenanceReaderWithRemote(newCampaignMemoryFileService(), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-remote.missed
		cancel()
	}()
	require.ErrorIs(t, reader.WaitForEvidence(ctx, []string{"attempt-cancelled"}), context.Canceled)
}

func TestCampaignModelProvenanceReader_WaitForEvidenceRejectsInvalidWindow(t *testing.T) {
	remote := &stubModelProvenanceRemote{window: testModelProvenanceWindow(t, "attempt-invalid")}
	remote.window.AttestationDigest = "invalid"
	reader, err := NewCampaignModelProvenanceReaderWithRemote(newCampaignMemoryFileService(), remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.Error(t, reader.WaitForEvidence(ctx, []string{"attempt-invalid"}))
}
