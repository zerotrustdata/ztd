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

func TestReleaseOllamaModels_TargetsExactInferenceSession(t *testing.T) {
	t.Parallel()
	dispatcher := &recordingOllamaModelCommandDispatcher{}
	err := ReleaseOllamaModels(context.Background(), dispatcher, "inference-session", "run-1", []string{"qwen3:0.6b", "registry.example/team/gemma3:4b"}, func(prefix string) string { return prefix + "-id" })
	require.NoError(t, err)
	require.Len(t, dispatcher.requests, 1)
	assert.Equal(t, "inference-session", dispatcher.requests[0].TargetOperatorSessionID)
	assert.Equal(t, constants.ActionTypeOllamaModelRelease, dispatcher.requests[0].ActionType)
	assert.Empty(t, dispatcher.requests[0].Command, "release is a typed action, not a shell command")
	assert.Empty(t, dispatcher.requests[0].Environment, "the Operator releases through its own configured provider")
	var payload operatorv1.OllamaModelReleaseRequested
	require.NoError(t, proto.Unmarshal(dispatcher.requests[0].Payload, &payload))
	assert.Equal(t, dispatcher.requests[0].ExecutionID, payload.GetExecutionId(), "payload and envelope execution ids must match")
	assert.Equal(t, []string{"qwen3:0.6b", "registry.example/team/gemma3:4b"}, payload.GetModelTags())
}

func TestReleaseOllamaModels_ValidatesWholeBatchBeforeDispatch(t *testing.T) {
	t.Parallel()
	dispatcher := &recordingOllamaModelCommandDispatcher{}
	err := ReleaseOllamaModels(context.Background(), dispatcher, "inference-session", "run-1", []string{"qwen3:0.6b", "qwen3:0.6b;whoami"}, func(prefix string) string { return prefix + "-id" })
	require.ErrorIs(t, err, constants.ErrInferenceModelTagInvalid)
	assert.Empty(t, dispatcher.requests)
}

func TestReleaseOllamaModels_PropagatesFailedOperatorCommand(t *testing.T) {
	t.Parallel()
	dispatcher := &recordingOllamaModelCommandDispatcher{returnCode: 1}
	err := ReleaseOllamaModels(context.Background(), dispatcher, "inference-session", "run-1", []string{"qwen3:0.6b"}, func(prefix string) string { return prefix + "-id" })
	require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
	assert.Len(t, dispatcher.requests, 1)
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
				_, err := ReadOllamaProviderResidency(t.Context(), dispatcher, maintenance)
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
