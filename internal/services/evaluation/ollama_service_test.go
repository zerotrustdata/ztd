// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type recordingOllamaModelCommandDispatcher struct {
	requests   []OllamaModelCommandDispatchRequest
	returnCode int32
	// resident is the provider residency each residency read reports.
	resident []string
}

func (d *recordingOllamaModelCommandDispatcher) DispatchOllamaModelCommand(_ context.Context, request OllamaModelCommandDispatchRequest) (*OllamaModelCommandDispatchResult, error) {
	d.requests = append(d.requests, request)
	if request.ActionType == constants.ActionTypeOllamaModelRelease {
		status := operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
		if d.returnCode != 0 {
			status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		}
		return &OllamaModelCommandDispatchResult{
			Status:  200,
			Success: true,
			ReleaseResult: &operatorv1.OllamaModelReleaseResult{
				ExecutionId: request.ExecutionID,
				Status:      status,
			},
		}, nil
	}
	if request.ActionType == constants.ActionTypeOllamaModelInventory {
		return &OllamaModelCommandDispatchResult{
			Status: 200, Success: true,
			InventoryResult: &operatorv1.OllamaModelInventoryResult{
				ExecutionId: request.ExecutionID, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
				Entries: []*operatorv1.ProviderModelInventoryEntry{{ServedModelTag: "qwen3:0.6b"}},
			},
		}, nil
	}
	if request.ActionType == constants.ActionTypeOllamaModelResidency {
		return &OllamaModelCommandDispatchResult{
			Status: 200, Success: true,
			ResidencyResult: &operatorv1.OllamaModelResidencyResult{
				ExecutionId: request.ExecutionID, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
				Models: residencyModels(d.resident),
			},
		}, nil
	}
	return nil, fmt.Errorf("unexpected action %s: %w", request.ActionType, constants.ErrEvaluationDispatchFailed)
}

func residencyModels(names []string) []*operatorv1.OllamaModelResidencyModel {
	models := make([]*operatorv1.OllamaModelResidencyModel, 0, len(names))
	for _, name := range names {
		models = append(models, &operatorv1.OllamaModelResidencyModel{Name: name})
	}
	return models
}

func newTestProviderModelResidency(t *testing.T, dispatcher OllamaModelCommandDispatcher) *ProviderModelResidency {
	t.Helper()
	minted := 0
	residency, err := NewProviderModelResidency(dispatcher, "inference-session", func(prefix string) string {
		minted++
		return fmt.Sprintf("%s-%d", prefix, minted)
	})
	require.NoError(t, err)
	return residency
}

func TestNewProviderModelResidency_RequiresEveryDependency(t *testing.T) {
	t.Parallel()
	newID := func(prefix string) string { return prefix }
	dispatcher := &recordingOllamaModelCommandDispatcher{}
	for name, build := range map[string]func() (*ProviderModelResidency, error){
		"dispatcher": func() (*ProviderModelResidency, error) {
			return NewProviderModelResidency(nil, "inference-session", newID)
		},
		"operator session": func() (*ProviderModelResidency, error) { return NewProviderModelResidency(dispatcher, "", newID) },
		"execution ID mint": func() (*ProviderModelResidency, error) {
			return NewProviderModelResidency(dispatcher, "inference-session", nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := build()
			require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		})
	}
}

func TestProviderModelResidency_ReleasesEveryResidentModelOutsideRetain(t *testing.T) {
	t.Parallel()
	dispatcher := &recordingOllamaModelCommandDispatcher{resident: []string{"qwen3:0.6b", "qwen3.5:4b", "registry.example/team/gemma3:4b"}}
	residency := newTestProviderModelResidency(t, dispatcher)

	released, err := residency.ReleaseAllExcept(t.Context(), ModelResidencyRelease{RunID: "run-1", Retain: []string{"qwen3.5:4b"}})

	require.NoError(t, err)
	assert.Equal(t, []string{"qwen3:0.6b", "registry.example/team/gemma3:4b"}, released)
	require.Len(t, dispatcher.requests, 2, "one residency read, then one batch release")
	read, release := dispatcher.requests[0], dispatcher.requests[1]
	assert.Equal(t, constants.ActionTypeOllamaModelResidency, read.ActionType)
	assert.Equal(t, constants.ProviderModelResidencyInvestigationID, read.InvestigationID)
	assert.Equal(t, constants.ActionTypeOllamaModelRelease, release.ActionType)
	assert.Equal(t, constants.ProviderModelReleaseInvestigationID, release.InvestigationID)
	for _, request := range dispatcher.requests {
		assert.Equal(t, "inference-session", request.TargetOperatorSessionID)
		assert.Equal(t, "run-1", request.CaseID, "residency receipts are scoped to the run")
	}
	var payload operatorv1.OllamaModelReleaseRequested
	require.NoError(t, proto.Unmarshal(release.Payload, &payload))
	assert.Equal(t, release.ExecutionID, payload.GetExecutionId(), "payload and envelope execution IDs must match")
	assert.Equal(t, released, payload.GetModelTags())
}

func TestProviderModelResidency_EmptyRetainReleasesEveryResidentModel(t *testing.T) {
	t.Parallel()
	dispatcher := &recordingOllamaModelCommandDispatcher{resident: []string{"qwen3:0.6b", "qwen3.5:4b"}}

	released, err := newTestProviderModelResidency(t, dispatcher).ReleaseAllExcept(t.Context(), ModelResidencyRelease{RunID: "run-1"})

	require.NoError(t, err)
	assert.Equal(t, []string{"qwen3:0.6b", "qwen3.5:4b"}, released)
}

func TestProviderModelResidency_DispatchesNoReleaseWhenNothingOutsideRetainIsResident(t *testing.T) {
	t.Parallel()
	for name, resident := range map[string][]string{
		"only retained models resident": {"qwen3.5:4b"},
		"nothing resident":              nil,
	} {
		t.Run(name, func(t *testing.T) {
			dispatcher := &recordingOllamaModelCommandDispatcher{resident: resident}

			released, err := newTestProviderModelResidency(t, dispatcher).ReleaseAllExcept(t.Context(), ModelResidencyRelease{RunID: "run-1", Retain: []string{"qwen3.5:4b"}})

			require.NoError(t, err)
			assert.Empty(t, released)
			require.Len(t, dispatcher.requests, 1)
			assert.Equal(t, constants.ActionTypeOllamaModelResidency, dispatcher.requests[0].ActionType, "only the residency read is dispatched")
		})
	}
}

func TestProviderModelResidency_FailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		dispatcher   *recordingOllamaModelCommandDispatcher
		release      ModelResidencyRelease
		wantErr      error
		wantRequests int
	}{
		{
			name:       "missing run",
			dispatcher: &recordingOllamaModelCommandDispatcher{resident: []string{"qwen3:0.6b"}},
			wantErr:    constants.ErrMissingRequiredField,
		},
		{
			name:         "resident model without a name",
			dispatcher:   &recordingOllamaModelCommandDispatcher{resident: []string{"qwen3:0.6b", ""}},
			release:      ModelResidencyRelease{RunID: "run-1"},
			wantErr:      constants.ErrInferenceProviderResponseInvalid,
			wantRequests: 1,
		},
		{
			name:         "resident tag the served-model validator rejects",
			dispatcher:   &recordingOllamaModelCommandDispatcher{resident: []string{"qwen3:0.6b", "qwen3:0.6b;whoami"}},
			release:      ModelResidencyRelease{RunID: "run-1"},
			wantErr:      constants.ErrInferenceModelTagInvalid,
			wantRequests: 1,
		},
		{
			name:         "release the Operator fails",
			dispatcher:   &recordingOllamaModelCommandDispatcher{resident: []string{"qwen3:0.6b"}, returnCode: 1},
			release:      ModelResidencyRelease{RunID: "run-1"},
			wantErr:      constants.ErrEvaluationDispatchFailed,
			wantRequests: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			released, err := newTestProviderModelResidency(t, test.dispatcher).ReleaseAllExcept(t.Context(), test.release)

			require.ErrorIs(t, err, test.wantErr)
			assert.Empty(t, released)
			assert.Len(t, test.dispatcher.requests, test.wantRequests)
		})
	}
}

// deadlineOllamaModelCommandDispatcher records each request and the context
// deadline it was dispatched under, and answers pull and copy as completed.
type deadlineOllamaModelCommandDispatcher struct {
	requests []OllamaModelCommandDispatchRequest
	deadline time.Time
}

func (d *deadlineOllamaModelCommandDispatcher) DispatchOllamaModelCommand(ctx context.Context, request OllamaModelCommandDispatchRequest) (*OllamaModelCommandDispatchResult, error) {
	d.requests = append(d.requests, request)
	d.deadline, _ = ctx.Deadline()
	completed := operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	return &OllamaModelCommandDispatchResult{
		Status:     200,
		Success:    true,
		PullResult: &operatorv1.OllamaModelPullResult{Status: completed},
		CopyResult: &operatorv1.OllamaModelCopyResult{Status: completed},
	}, nil
}

func TestPullOllamaModel_SendsTypedPullThatOutlivesItsTimeout(t *testing.T) {
	t.Parallel()
	dispatcher := &deadlineOllamaModelCommandDispatcher{}
	maintenance := OllamaModelMaintenanceContext{TargetOperatorSessionID: "session", Timeout: 2 * time.Hour, NewID: func(prefix string) string { return prefix }}
	before := time.Now()

	require.NoError(t, PullOllamaModel(t.Context(), dispatcher, maintenance, "qwen3:0.6b"))

	require.Len(t, dispatcher.requests, 1)
	request := dispatcher.requests[0]
	assert.Equal(t, constants.ActionTypeOllamaModelPull, request.ActionType)
	var pull operatorv1.OllamaModelPullRequested
	require.NoError(t, proto.Unmarshal(request.Payload, &pull))
	assert.Equal(t, "qwen3:0.6b", pull.GetModelTag())
	assert.Equal(t, int32(7200), pull.GetTimeoutSeconds())
	assert.Greater(t, dispatcher.deadline.Sub(before), 2*time.Hour, "the dispatch must outwait the Operator's own deadline")
}

func TestPullOllamaModel_DefaultsToTheModelPullTimeout(t *testing.T) {
	t.Parallel()
	dispatcher := &deadlineOllamaModelCommandDispatcher{}
	maintenance := OllamaModelMaintenanceContext{TargetOperatorSessionID: "session", NewID: func(prefix string) string { return prefix }}

	require.NoError(t, PullOllamaModel(t.Context(), dispatcher, maintenance, "qwen3:0.6b"))

	var pull operatorv1.OllamaModelPullRequested
	require.NoError(t, proto.Unmarshal(dispatcher.requests[0].Payload, &pull))
	assert.Equal(t, int32(constants.ModelPullDefaultTimeout/time.Second), pull.GetTimeoutSeconds())
}

func TestPullOllamaModel_RejectsATimeoutTheRequestCannotCarry(t *testing.T) {
	t.Parallel()
	tests := map[string]time.Duration{
		"negative":            -time.Hour,
		"under one second":    500 * time.Millisecond,
		"past int32 seconds":  (math.MaxInt32 + 1) * time.Second,
		"wraps back positive": (1<<32 + 60) * time.Second,
	}
	for name, timeout := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dispatcher := &deadlineOllamaModelCommandDispatcher{}
			maintenance := OllamaModelMaintenanceContext{TargetOperatorSessionID: "session", Timeout: timeout, NewID: func(prefix string) string { return prefix }}

			require.ErrorIs(t, PullOllamaModel(t.Context(), dispatcher, maintenance, "qwen3:0.6b"), constants.ErrEvaluationFlagsInvalid)
			assert.Empty(t, dispatcher.requests)
		})
	}
}

func TestPullOllamaModel_CarriesTheTimeoutBounds(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		timeout time.Duration
		want    int32
	}{
		"one second":    {timeout: time.Second, want: 1},
		"largest int32": {timeout: math.MaxInt32 * time.Second, want: math.MaxInt32},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dispatcher := &deadlineOllamaModelCommandDispatcher{}
			maintenance := OllamaModelMaintenanceContext{TargetOperatorSessionID: "session", Timeout: tt.timeout, NewID: func(prefix string) string { return prefix }}

			require.NoError(t, PullOllamaModel(t.Context(), dispatcher, maintenance, "qwen3:0.6b"))

			var pull operatorv1.OllamaModelPullRequested
			require.NoError(t, proto.Unmarshal(dispatcher.requests[0].Payload, &pull))
			assert.Equal(t, tt.want, pull.GetTimeoutSeconds())
		})
	}
}

func TestPullOllamaModel_RejectsInvalidTagBeforeDispatch(t *testing.T) {
	t.Parallel()
	dispatcher := &deadlineOllamaModelCommandDispatcher{}
	maintenance := OllamaModelMaintenanceContext{TargetOperatorSessionID: "session", NewID: func(prefix string) string { return prefix }}

	require.ErrorIs(t, PullOllamaModel(t.Context(), dispatcher, maintenance, "qwen3;whoami"), constants.ErrInferenceModelTagInvalid)
	assert.Empty(t, dispatcher.requests)
}

func TestCopyOllamaModel_SendsTypedCopy(t *testing.T) {
	t.Parallel()
	dispatcher := &deadlineOllamaModelCommandDispatcher{}
	maintenance := OllamaModelMaintenanceContext{TargetOperatorSessionID: "session", NewID: func(prefix string) string { return prefix }}

	require.NoError(t, CopyOllamaModel(t.Context(), dispatcher, maintenance, "hf.co/a/b:Q4", "b:latest"))

	request := dispatcher.requests[0]
	assert.Equal(t, constants.ActionTypeOllamaModelCopy, request.ActionType)
	var modelCopy operatorv1.OllamaModelCopyRequested
	require.NoError(t, proto.Unmarshal(request.Payload, &modelCopy))
	assert.Equal(t, "hf.co/a/b:Q4", modelCopy.GetSource())
	assert.Equal(t, "b:latest", modelCopy.GetDestination())
}

func TestValidateOllamaModelPullResult_TypesTheOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		result        *OllamaModelCommandDispatchResult
		wantErr       []error
		wantInMessage string
	}{
		{name: "completed", result: &OllamaModelCommandDispatchResult{Status: 200, Success: true, PullResult: &operatorv1.OllamaModelPullResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}}},
		{name: "no result", wantErr: []error{constants.ErrEvaluationDispatchFailed}},
		{name: "rejected", result: &OllamaModelCommandDispatchResult{Status: 403}, wantErr: []error{constants.ErrEvaluationDispatchFailed}, wantInMessage: "status 403"},
		{name: "missing typed result", result: &OllamaModelCommandDispatchResult{Status: 200, Success: true}, wantErr: []error{constants.ErrEvaluationDispatchFailed}},
		{
			name: "operator failure names kind and cause",
			result: &OllamaModelCommandDispatchResult{Status: 200, Success: true, PullResult: &operatorv1.OllamaModelPullResult{
				Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, ErrorMessage: "ollama api: model not found", ErrorType: "provider",
			}},
			wantErr:       []error{constants.ErrEvaluationDispatchFailed, constants.ErrActionExecutionFailed},
			wantInMessage: "failed (provider): ollama api: model not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOllamaModelPullResult(tt.result)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			for _, want := range tt.wantErr {
				require.ErrorIs(t, err, want)
			}
			assert.Contains(t, err.Error(), tt.wantInMessage)
		})
	}
}

func TestValidateOllamaModelCopyResult_FailedCopyIsAnExecutionFailure(t *testing.T) {
	t.Parallel()
	err := ValidateOllamaModelCopyResult(&OllamaModelCommandDispatchResult{Status: 200, Success: true, CopyResult: &operatorv1.OllamaModelCopyResult{
		Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, ErrorMessage: "ollama api: status 404",
	}})
	require.ErrorIs(t, err, constants.ErrActionExecutionFailed)
}

func TestValidateOllamaModelReleaseResult_RequiresCompletedTypedResult(t *testing.T) {
	t.Parallel()
	require.ErrorIs(t, ValidateOllamaModelReleaseResult(&OllamaModelCommandDispatchResult{Status: 200, Success: true}), constants.ErrEvaluationDispatchFailed)
	require.ErrorIs(t, ValidateOllamaModelReleaseResult(&OllamaModelCommandDispatchResult{
		Status:  200,
		Success: true,
		ReleaseResult: &operatorv1.OllamaModelReleaseResult{
			Status:       operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			ErrorMessage: "model remains resident",
		},
	}), constants.ErrEvaluationDispatchFailed)
	require.NoError(t, ValidateOllamaModelReleaseResult(&OllamaModelCommandDispatchResult{
		Status:  200,
		Success: true,
		ReleaseResult: &operatorv1.OllamaModelReleaseResult{
			Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		},
	}))
}

func TestOllamaProviderQueries_UsePayloadExecutionID(t *testing.T) {
	for _, action := range []constants.ActionType{constants.ActionTypeOllamaModelInventory, constants.ActionTypeOllamaModelResidency} {
		t.Run(string(action), func(t *testing.T) {
			dispatcher := &recordingOllamaModelCommandDispatcher{}
			minted := 0
			maintenance := OllamaModelMaintenanceContext{
				TargetOperatorSessionID: "inference-session",
				NewID: func(prefix string) string {
					minted++
					return fmt.Sprintf("%s-%d", prefix, minted)
				},
			}
			var payloadID string
			if action == constants.ActionTypeOllamaModelInventory {
				_, err := ListOllamaProviderInventory(t.Context(), dispatcher, maintenance)
				require.NoError(t, err)
				require.Len(t, dispatcher.requests, 1)
				var payload operatorv1.OllamaModelInventoryRequested
				require.NoError(t, proto.Unmarshal(dispatcher.requests[0].Payload, &payload))
				payloadID = payload.GetExecutionId()
			} else {
				_, err := readProviderResidency(t.Context(), dispatcher, maintenance)
				require.NoError(t, err)
				require.Len(t, dispatcher.requests, 1)
				var payload operatorv1.OllamaModelResidencyRequested
				require.NoError(t, proto.Unmarshal(dispatcher.requests[0].Payload, &payload))
				payloadID = payload.GetExecutionId()
			}
			assert.Equal(t, payloadID, dispatcher.requests[0].ExecutionID)
			assert.Equal(t, "inference-session", dispatcher.requests[0].TargetOperatorSessionID)
			assert.Equal(t, 2, minted, "one execution ID and one task ID are minted")
		})
	}
}
