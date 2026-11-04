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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// stagingOllamaModelCommandDispatcher records each typed action and fails the
// pull of any tag named in failPull with an Operator-typed failure.
type stagingOllamaModelCommandDispatcher struct {
	actions  []constants.ActionType
	pulled   []string
	failPull map[string]bool
}

func (d *stagingOllamaModelCommandDispatcher) DispatchOllamaModelCommand(_ context.Context, request OllamaModelCommandDispatchRequest) (*OllamaModelCommandDispatchResult, error) {
	d.actions = append(d.actions, request.ActionType)
	completed := operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	result := &OllamaModelCommandDispatchResult{Status: 200, Success: true}
	switch request.ActionType {
	case constants.ActionTypeOllamaModelPull:
		var pull operatorv1.OllamaModelPullRequested
		if err := proto.Unmarshal(request.Payload, &pull); err != nil {
			return nil, err
		}
		d.pulled = append(d.pulled, pull.GetModelTag())
		result.PullResult = &operatorv1.OllamaModelPullResult{Status: completed}
		if d.failPull[pull.GetModelTag()] {
			result.PullResult = &operatorv1.OllamaModelPullResult{
				Status:       operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
				ErrorMessage: "ollama api: model not found",
				ErrorType:    "provider",
			}
		}
	case constants.ActionTypeOllamaModelCopy:
		result.CopyResult = &operatorv1.OllamaModelCopyResult{Status: completed}
	}
	return result, nil
}

func TestPullCatalogModels_PullsAndAliases(t *testing.T) {
	t.Parallel()

	catalogPath := writeModelPullCatalog(t, ModelPullCatalog{
		Models: []ModelPullEntry{{
			VariantID:      "qwen3-8-27b",
			ServedModelTag: "qwen3.8:27b",
			Staging: ModelPullStaging{
				Method:     "ollama_hf_pull",
				OllamaPull: "huggingface.co/unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M",
				Alias:      "qwen3.8:27b",
			},
		}},
	})
	dispatcher := &stagingOllamaModelCommandDispatcher{}
	result, err := PullCatalogModels(t.Context(), ModelPullRequest{
		CatalogPath:        catalogPath,
		Dispatcher:         dispatcher,
		InferenceSessionID: "infer-session",
		NewID:              func(prefix string) string { return prefix },
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"huggingface.co/unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M"}, result.Pulled)
	assert.Equal(t, []string{"qwen3.8:27b"}, result.Aliased)
	assert.Equal(t, []constants.ActionType{constants.ActionTypeOllamaModelPull, constants.ActionTypeOllamaModelCopy}, dispatcher.actions)
}

func TestPullCatalogModels_SkipsManualAndPendingEntries(t *testing.T) {
	t.Parallel()

	catalogPath := writeModelPullCatalog(t, ModelPullCatalog{
		Models: []ModelPullEntry{
			{
				VariantID:      "glm-5-3-air",
				ServedModelTag: "glm-5.3-air",
				Staging:        ModelPullStaging{Method: "pending", Status: "awaiting_single_file_gguf"},
			},
			{
				VariantID:      "glm-5-3-flash",
				ServedModelTag: "glm-5.3-flash",
				Staging: ModelPullStaging{
					Method:          "manual_create",
					OllamaPullError: "sharded GGUF",
				},
			},
		},
	})

	result, err := PullCatalogModels(t.Context(), ModelPullRequest{
		CatalogPath:        catalogPath,
		Dispatcher:         &stagingOllamaModelCommandDispatcher{},
		InferenceSessionID: "infer-session",
		NewID:              func(prefix string) string { return prefix },
	})
	require.NoError(t, err)
	require.Len(t, result.Skipped, 2)
	assert.Equal(t, "awaiting_single_file_gguf", result.Skipped[0].Reason)
	assert.Equal(t, "sharded GGUF", result.Skipped[1].Reason)
}

func TestPullFormationModels_PullsMissingLibraryTag(t *testing.T) {
	t.Parallel()

	dispatcher := &stagingOllamaModelCommandDispatcher{}
	result, err := PullFormationModels(t.Context(), ModelPullRequest{
		Dispatcher:         dispatcher,
		InferenceSessionID: "infer-session",
		VariantIDs:         []string{"gemma3-1b"},
		NewID:              func(prefix string) string { return prefix },
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"gemma3:1b"}, result.Pulled)
	assert.Equal(t, []string{"gemma3:1b"}, dispatcher.pulled)
}

func TestModelPull_InvalidTimeoutFailsTheRunBeforeAnyPull(t *testing.T) {
	t.Parallel()

	catalogPath := writeModelPullCatalog(t, ModelPullCatalog{
		Models: []ModelPullEntry{
			{VariantID: "one", ServedModelTag: "one:1b", Staging: ModelPullStaging{Method: "ollama_library_pull", OllamaPull: "one:1b"}},
		},
	})
	dispatcher := &stagingOllamaModelCommandDispatcher{}
	req := ModelPullRequest{
		CatalogPath:        catalogPath,
		Timeout:            500 * time.Millisecond,
		Dispatcher:         dispatcher,
		InferenceSessionID: "infer-session",
		NewID:              func(prefix string) string { return prefix },
	}

	_, err := PullCatalogModels(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
	_, err = PullFormationModels(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
	assert.Empty(t, dispatcher.actions)
}

func TestPullCatalogModels_ReportsAFailedPullWhenItHappensAndContinues(t *testing.T) {
	t.Parallel()

	catalogPath := writeModelPullCatalog(t, ModelPullCatalog{
		Models: []ModelPullEntry{
			{VariantID: "broken", ServedModelTag: "broken:1b", Staging: ModelPullStaging{Method: "ollama_library_pull", OllamaPull: "broken:1b"}},
			{VariantID: "next", ServedModelTag: "next:1b", Staging: ModelPullStaging{Method: "ollama_library_pull", OllamaPull: "next:1b"}},
		},
	})
	dispatcher := &stagingOllamaModelCommandDispatcher{failPull: map[string]bool{"broken:1b": true}}
	var events []ModelPullEvent

	result, err := PullCatalogModels(t.Context(), ModelPullRequest{
		CatalogPath:        catalogPath,
		Dispatcher:         dispatcher,
		InferenceSessionID: "infer-session",
		NewID:              func(prefix string) string { return prefix },
		Progress:           func(event ModelPullEvent) { events = append(events, event) },
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"next:1b"}, result.Pulled)
	require.Len(t, result.Failed, 1)
	require.ErrorIs(t, result.Failed[0].Cause, constants.ErrActionExecutionFailed)
	assert.Contains(t, result.Failed[0].Err, "ollama api: model not found")
	require.GreaterOrEqual(t, len(events), 3)
	assert.Equal(t, ModelPullStatusFailed, events[1].Status, "the failure is reported before the next model starts")
	assert.Contains(t, events[1].Detail, "ollama api: model not found")
	assert.Equal(t, "next:1b", events[2].ServedModelTag)
}

func writeModelPullCatalog(t *testing.T, catalog ModelPullCatalog) string {
	t.Helper()
	path := testutil.TempDir(t) + "/model-pull-catalog.json"
	payload, err := json.Marshal(catalog)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, payload, constants.PermFilePublic))
	return path
}
