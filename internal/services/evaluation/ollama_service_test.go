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
	"testing"

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
	return &OllamaModelCommandDispatchResult{
		Status:  200,
		Success: true,
		CommandResult: &operatorv1.CommandResult{
			Status:     operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ReturnCode: d.returnCode,
		},
	}, nil
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
		assert.Empty(t, request.Command, "residency commands are typed actions, not shell commands")
		assert.Empty(t, request.Environment, "the Operator serves residency from its own configured provider")
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

func TestMarshalOllamaModelCommandPayload_CarriesExecutionFields(t *testing.T) {
	t.Parallel()
	payload, err := MarshalOllamaModelCommandPayload(OllamaModelCommandDispatchRequest{
		Command:          "ollama stop qwen3:0.6b",
		ExecutionID:      "exec-1",
		Environment:      map[string]string{"ZTD_OLLAMA_ENDPOINT": "http://provider.example:11434"},
		WorkingDirectory: "/root",
		TimeoutSeconds:   30,
	})
	require.NoError(t, err)
	var command operatorv1.CommandRequested
	require.NoError(t, proto.Unmarshal(payload, &command))
	assert.Equal(t, "ollama stop qwen3:0.6b", command.GetCommand())
	assert.Equal(t, "exec-1", command.GetExecutionId())
	assert.Equal(t, map[string]string{"ZTD_OLLAMA_ENDPOINT": "http://provider.example:11434"}, command.GetEnvironment())
	assert.Equal(t, "/root", command.GetWorkingDirectory())
	assert.Equal(t, int32(30), command.GetTimeoutSeconds())
}

func TestValidateOllamaModelCommandResult_RejectsNonzeroExit(t *testing.T) {
	t.Parallel()
	err := ValidateOllamaModelCommandResult("ollama stop qwen3:0.6b", &OllamaModelCommandDispatchResult{
		Status:  200,
		Success: true,
		CommandResult: &operatorv1.CommandResult{
			Status:     operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ReturnCode: 1,
		},
	})
	require.Error(t, err)
}

func TestValidateOllamaModelCommandResult_NonzeroExitNamesStderrCause(t *testing.T) {
	t.Parallel()
	err := ValidateOllamaModelCommandResult("ztd operator model pull qwen3:0.6b", &OllamaModelCommandDispatchResult{
		Status:  200,
		Success: true,
		CommandResult: &operatorv1.CommandResult{
			Status:     operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ReturnCode: 1,
			Stderr:     "Usage:\n  ztd operator model pull\n\nError: operator: pull model: manifest for \"qwen3:0.6b\" not found\n",
		},
	})
	require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
	assert.Contains(t, err.Error(), "exited with code 1: Error: operator: pull model: manifest for \"qwen3:0.6b\" not found")
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
