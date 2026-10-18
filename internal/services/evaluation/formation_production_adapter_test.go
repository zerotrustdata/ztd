// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type stubFormationProvenancePreflight struct{}

func (stubFormationProvenancePreflight) PreflightSovereignModel(_ context.Context, model FormationModel) (*FormationAttestation, error) {
	return &FormationAttestation{Verified: true, Digest: model.ModelDigest}, nil
}

type stubFormationObservationLoader struct {
	windows map[string]*evalv1.ProviderBoundaryObservationWindow
}

func (s *stubFormationObservationLoader) LoadObservationWindow(_ context.Context, providerAttemptID string) (*evalv1.ProviderBoundaryObservationWindow, error) {
	if s == nil || s.windows[providerAttemptID] == nil {
		return nil, constants.ErrNotFound
	}
	return s.windows[providerAttemptID], nil
}

func seedFormationObservationWindow(loader *stubFormationObservationLoader, attemptID string) {
	loader.windows[attemptID] = &evalv1.ProviderBoundaryObservationWindow{
		ProviderAttemptId: attemptID,
		Samples: []*evalv1.ProviderBoundaryHardwareSample{{
			VramBytesAvailability: evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED,
			VramUsedBytes:         1024 * 1024 * 1024,
		}},
	}
}

type stubFormationProvenanceLoader struct {
	windows map[string]*evalv1.ModelProvenanceAttestationWindow
}

func (s *stubFormationProvenanceLoader) LoadProvenanceWindow(_ context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if s == nil || s.windows[providerAttemptID] == nil {
		return nil, constants.ErrNotFound
	}
	return s.windows[providerAttemptID], nil
}

func seedFormationProvenanceWindow(loader *stubFormationProvenanceLoader, attemptID, modelDigest string) {
	loader.windows[attemptID] = &evalv1.ModelProvenanceAttestationWindow{
		ProviderAttemptId:   attemptID,
		ExpectedModelDigest: modelDigest,
		ObservedModelDigest: modelDigest,
		AttestationDigest:   "digest-" + attemptID,
		DigestMatch:         true,
	}
}

type recordingFormationInferenceDispatcher struct {
	requests []*operatorv1.InferenceDispatchRequest
}

func (d *recordingFormationInferenceDispatcher) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	d.requests = append(d.requests, req)
	attemptID := req.GetProviderAttemptId()
	return &operatorv1.InferenceDispatchResponse{
		Result: &operatorv1.InferenceResult{
			ProviderAttemptId: attemptID,
			RequestedModel:    req.GetModel(),
			ResultDigest:      "digest-" + attemptID,
			Parts: []*operatorv1.InferenceResponsePart{{
				Part: &operatorv1.InferenceResponsePart_Text{Text: "role-output:" + attemptID},
			}},
			PromptTokens:         24,
			CompletionTokens:     12,
			UsageReported:        true,
			TimeToFirstTokenNs:   ptrInt64(2_000_000),
			GenerationDurationNs: ptrInt64(10_000_000),
			CampaignId:           req.GetCampaignId(),
			ModelRegistryDigest:  req.GetModelRegistryDigest(),
		},
		Receipt: &operatorv1.ActionReceipt{ResultSummary: "digest-" + attemptID},
	}, nil
}

func ptrInt64(value int64) *int64 {
	return &value
}

func ultraEfficientSpeedsterProductionDeps(t *testing.T, dispatcher *recordingFormationInferenceDispatcher, modelDispatcher *recordingOllamaModelCommandDispatcher) FormationProductionDependencies {
	t.Helper()
	variants := ultraEfficientSpeedsterRegistryVariants()
	inferenceVariants := InferenceVariantsFromEvalRegistry(variants)
	registryDigest, err := models.ComputeInferenceModelRegistryDigest("formation-campaign", inferenceVariants)
	require.NoError(t, err)
	observer := &stubFormationObservationLoader{windows: map[string]*evalv1.ProviderBoundaryObservationWindow{}}
	provenanceLoader := &stubFormationProvenanceLoader{windows: map[string]*evalv1.ModelProvenanceAttestationWindow{}}
	for _, item := range []struct {
		attemptID   string
		modelDigest string
	}{
		{"ultra-efficient-speedster-lite-id", variants[2].GetModelDigest()},
		{"ultra-efficient-speedster-assistant-id", variants[1].GetModelDigest()},
		{"ultra-efficient-speedster-primary-id", variants[0].GetModelDigest()},
	} {
		seedFormationObservationWindow(observer, item.attemptID)
		seedFormationProvenanceWindow(provenanceLoader, item.attemptID, item.modelDigest)
	}
	return FormationProductionDependencies{
		RunContext: FormationRunContext{
			CampaignID:          "formation-campaign",
			RunID:               "formation-run",
			AssignmentID:        "formation-assignment",
			EvaluationAttemptID: "formation-attempt",
			ModelRegistryDigest: registryDigest,
			InferenceSessionID:  "sess-inf-1",
		},
		Variants:               variants,
		ProvenancePreflight:    stubFormationProvenancePreflight{},
		ProvenanceLoader:       provenanceLoader,
		ObservationLoader:      observer,
		InferenceDispatcher:    dispatcher,
		ModelCommandDispatcher: modelDispatcher,
		OllamaEnvironment:      map[string]string{"ZTD_OLLAMA_ENDPOINT": "http://127.0.0.1:11434"},
		NewID:                  func(prefix string) string { return prefix + "-id" },
		Now:                    func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
}

func TestRunFormationProduction_ExecutesUltraEfficientSpeedsterThroughGovernedPath(t *testing.T) {
	dispatcher := &recordingFormationInferenceDispatcher{}
	modelDispatcher := &recordingOllamaModelCommandDispatcher{}
	deps := ultraEfficientSpeedsterProductionDeps(t, dispatcher, modelDispatcher)

	result, err := RunFormationProduction(context.Background(), ultraEfficientSpeedsterBindingRequest(t), deps, []byte("initial"))
	require.NoError(t, err)
	require.True(t, result.Passed)
	assert.Equal(t, "ultra-efficient-speedster", result.FormationID)
	assert.Len(t, result.Roles, 3)
	assert.True(t, result.MutationIntercepted)
	assert.Equal(t, []FormationRole{
		FormationRoleLite,
		FormationRoleAssistant,
		FormationRolePrimary,
	}, []FormationRole{result.Roles[0].Role, result.Roles[1].Role, result.Roles[2].Role})
	for _, role := range result.Roles {
		assert.Equal(t, evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED, role.UsageAvailability)
		assert.Equal(t, uint32(24), role.PromptTokens)
		assert.Equal(t, uint32(12), role.GenerationTokens)
	}

	assert.Len(t, dispatcher.requests, 6)
	for _, dispatchReq := range dispatcher.requests {
		assert.Equal(t, "sess-inf-1", dispatchReq.GetTargetOperatorSessionId())
		assert.Equal(t, "formation-campaign", dispatchReq.GetCampaignId())
		assert.Equal(t, "formation-run", dispatchReq.GetRunId())
		assert.Equal(t, "formation-assignment", dispatchReq.GetAssignmentId())
		assert.Equal(t, deps.RunContext.ModelRegistryDigest, dispatchReq.GetModelRegistryDigest())
		assert.NotEmpty(t, dispatchReq.GetModelDigest())
	}
	assert.Len(t, modelDispatcher.requests, 3)
}

func TestNewFormationProductionRunner_RequiresGovernedBinding(t *testing.T) {
	_, err := NewFormationProductionRunner(FormationProductionDependencies{})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationRunnerDependency)
}

// lateObservationRemote records its window only after the connect reconcile
// has missed it, then announces it like the Gateway's observation-recorded
// event.
type lateObservationRemote struct {
	window  *evalv1.ProviderBoundaryObservationWindow
	attempt *operatorv1.InferenceProviderAttemptRecord
	missed  chan struct{}

	mu         sync.Mutex
	isRecorded bool
	loads      int
}

func newLateObservationRemote(window *evalv1.ProviderBoundaryObservationWindow, attempt *operatorv1.InferenceProviderAttemptRecord) *lateObservationRemote {
	return &lateObservationRemote{window: window, attempt: attempt, missed: make(chan struct{})}
}

func (r *lateObservationRemote) Load(_ context.Context, providerAttemptID string) (*evalv1.ProviderBoundaryObservationWindow, *operatorv1.InferenceProviderAttemptRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads++
	if !r.isRecorded || r.window.GetProviderAttemptId() != providerAttemptID {
		if r.loads == 1 {
			close(r.missed)
		}
		return nil, nil, constants.ErrNotFound
	}
	return r.window, r.attempt, nil
}

func (r *lateObservationRemote) WatchRecorded(ctx context.Context, connected func(), recorded func(string)) error {
	connected()
	select {
	case <-r.missed:
	case <-ctx.Done():
		return nil
	}
	r.mu.Lock()
	r.isRecorded = true
	r.mu.Unlock()
	recorded(r.window.GetProviderAttemptId())
	<-ctx.Done()
	return nil
}

func formationObservationWindow(t *testing.T, attemptID string) (*evalv1.ProviderBoundaryObservationWindow, *operatorv1.InferenceProviderAttemptRecord) {
	t.Helper()
	attempt := &operatorv1.InferenceProviderAttemptRecord{
		ProviderAttemptId: attemptID,
		Status:            operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED,
		StartedAtUnixMs:   time.Unix(1_700_000_000, 0).UnixMilli(),
		CompletedAtUnixMs: time.Unix(1_700_000_010, 0).UnixMilli(),
	}
	window := &evalv1.ProviderBoundaryObservationWindow{
		SchemaVersion:              provider_observer.SchemaVersion,
		ProviderAttemptId:          attemptID,
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

func TestAwaitingFormationObservationLoader_LoadsWindowOnRecordedEvent(t *testing.T) {
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	window, attempt := formationObservationWindow(t, "attempt-late")
	remote := newLateObservationRemote(window, attempt)
	reader, err := NewCampaignProviderObservationReaderWithRemote(fileSvc, remote)
	require.NoError(t, err)

	loaded, err := NewAwaitingFormationObservationLoader(reader).LoadObservationWindow(context.Background(), "attempt-late")
	require.NoError(t, err)
	assert.Equal(t, window.GetObservationDigest(), loaded.GetObservationDigest())
	remote.mu.Lock()
	defer remote.mu.Unlock()
	assert.Equal(t, 2, remote.loads, "connect reconcile misses, recorded event captures")
}

func TestAwaitingFormationObservationLoader_WithoutGatewayIsWitnessUnavailable(t *testing.T) {
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	reader, err := NewCampaignProviderObservationReader(fileSvc)
	require.NoError(t, err)

	_, err = NewAwaitingFormationObservationLoader(reader).LoadObservationWindow(context.Background(), "attempt-missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationWitnessUnavailable)
	assert.ErrorIs(t, err, constants.ErrGatewayNotReachable)
}

func TestAwaitingFormationObservationLoader_RequiresReader(t *testing.T) {
	_, err := NewAwaitingFormationObservationLoader(nil).LoadObservationWindow(context.Background(), "attempt-1")
	assert.ErrorIs(t, err, constants.ErrFormationRunnerDependency)
}
