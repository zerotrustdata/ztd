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

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// inventoryOllamaDispatcher answers every inventory query with entries.
type inventoryOllamaDispatcher struct {
	entries []*operatorv1.ProviderModelInventoryEntry
}

func (d *inventoryOllamaDispatcher) DispatchOllamaModelCommand(_ context.Context, request OllamaModelCommandDispatchRequest) (*OllamaModelCommandDispatchResult, error) {
	if request.ActionType != constants.ActionTypeOllamaModelInventory {
		return nil, fmt.Errorf("unexpected action %s: %w", request.ActionType, constants.ErrEvaluationDispatchFailed)
	}
	return &OllamaModelCommandDispatchResult{
		Status: 200, Success: true,
		InventoryResult: &operatorv1.OllamaModelInventoryResult{
			ExecutionId: request.ExecutionID, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			Entries: d.entries,
		},
	}, nil
}

func hostedInventoryTestEntries() []*operatorv1.ProviderModelInventoryEntry {
	return []*operatorv1.ProviderModelInventoryEntry{
		{ServedModelTag: "qwen3:0.6b", ModelDigest: models.SHA256Hex([]byte("qwen")), ProviderClass: constants.InferenceProviderOllama},
		{ServedModelTag: "gemini-3.6-flash", ModelDigest: models.SHA256Hex([]byte("3.6")), ProviderClass: constants.InferenceProviderGemini, Format: constants.HostedModelFormat},
		{ServedModelTag: "gemini-2.5-flash", ModelDigest: models.SHA256Hex([]byte("2.5")), ProviderClass: constants.InferenceProviderGemini, Format: constants.HostedModelFormat},
	}
}

func hostedInventoryMaintenance() OllamaModelMaintenanceContext {
	minted := 0
	return OllamaModelMaintenanceContext{
		TargetOperatorSessionID: "inference-session",
		NewID: func(prefix string) string {
			minted++
			return fmt.Sprintf("%s-%d", prefix, minted)
		},
	}
}

func servedTags(variants []*evalv1.ModelVariant) []string {
	tags := make([]string, 0, len(variants))
	for _, variant := range variants {
		tags = append(tags, variant.GetServedModelTag())
	}
	return tags
}

func TestFreezeModelInventoryFromProvider_NeverFreezesTheHostedCatalogInBulk(t *testing.T) {
	t.Parallel()
	dispatcher := &inventoryOllamaDispatcher{entries: hostedInventoryTestEntries()}
	freeze, err := FreezeModelInventoryFromProvider(t.Context(), dispatcher, hostedInventoryMaintenance(), "campaign-1", ModelInventoryOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"qwen3:0.6b"}, servedTags(freeze.Variants))
}

func TestFreezeModelInventoryFromProvider_KeepsRegistryHostedModelsAtTheirLiveIdentity(t *testing.T) {
	t.Parallel()
	dispatcher := &inventoryOllamaDispatcher{entries: hostedInventoryTestEntries()}
	held := &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderGemini, ServedModelTag: "gemini-3.6-flash", ModelDigest: models.SHA256Hex([]byte("an older version"))}
	freeze, err := FreezeModelInventoryFromProvider(t.Context(), dispatcher, hostedInventoryMaintenance(), "campaign-1", ModelInventoryOptions{HostedVariants: []*evalv1.ModelVariant{held}})
	require.NoError(t, err)
	require.Equal(t, []string{"gemini-3.6-flash", "qwen3:0.6b"}, servedTags(freeze.Variants))
	hosted := freeze.Variants[0]
	assert.Equal(t, constants.InferenceProviderGemini, hosted.GetProviderClass())
	assert.Equal(t, models.SHA256Hex([]byte("3.6")), hosted.GetModelDigest(), "a freeze binds the provider's current model version")
}

func TestFreezeModelInventoryFromProvider_FailsWhenTheProviderDropsAHeldModel(t *testing.T) {
	t.Parallel()
	dispatcher := &inventoryOllamaDispatcher{entries: hostedInventoryTestEntries()}
	held := &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderGemini, ServedModelTag: "gemini-1.5-pro"}
	_, err := FreezeModelInventoryFromProvider(t.Context(), dispatcher, hostedInventoryMaintenance(), "campaign-1", ModelInventoryOptions{HostedVariants: []*evalv1.ModelVariant{held}})
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
}

func TestHostedModelVariant(t *testing.T) {
	t.Parallel()
	entries := inference.ProviderModelInventoryEntriesFromProto(hostedInventoryTestEntries())

	variant, err := HostedModelVariant(entries, constants.InferenceProviderGemini, "gemini-2.5-flash", "")
	require.NoError(t, err)
	assert.Equal(t, constants.InferenceProviderGemini, variant.GetProviderClass())
	assert.Equal(t, "gemini-2.5-flash", variant.GetServedModelTag())
	assert.Equal(t, models.SHA256Hex([]byte("2.5")), variant.GetModelDigest())
	assert.Equal(t, constants.HostedModelFormat, variant.GetModelFormat())

	pinned, err := HostedModelVariant(entries, constants.InferenceProviderGemini, "gemini-2.5-flash", models.SHA256Hex([]byte("2.5")))
	require.NoError(t, err, "a digest that agrees with the live identity")
	assert.Equal(t, variant.GetModelDigest(), pinned.GetModelDigest())
	_, err = HostedModelVariant(entries, constants.InferenceProviderGemini, "gemini-2.5-flash", models.SHA256Hex([]byte("stale")))
	require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch, "a caller's digest never overrides the provider's identity")

	_, err = HostedModelVariant(entries, constants.InferenceProviderGemini, "gemini-9-ultra", "")
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
	_, err = HostedModelVariant(entries, constants.InferenceProviderOpenAI, "gemini-2.5-flash", "")
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound, "a model is resolved only from its own provider class")
	_, err = HostedModelVariant(entries, constants.InferenceProviderOllama, "qwen3:0.6b", "")
	require.ErrorIs(t, err, constants.ErrInferenceHostedNotAllowed)

	undigested := []inference.ProviderModelInventoryEntry{{ServedModelTag: "gemini-2.5-flash", ProviderClass: constants.InferenceProviderGemini}}
	_, err = HostedModelVariant(undigested, constants.InferenceProviderGemini, "gemini-2.5-flash", "")
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestDispatchesLocalModels(t *testing.T) {
	t.Parallel()
	local := &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderOllama}
	llamacpp := &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderLlamaCpp}
	hosted := &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderGemini}

	assert.False(t, DispatchesLocalModels([]*evalv1.ModelVariant{hosted}, hosted), "a hosted-only campaign needs no observer")
	assert.False(t, DispatchesLocalModels([]*evalv1.ModelVariant{hosted}, nil))
	assert.True(t, DispatchesLocalModels([]*evalv1.ModelVariant{hosted}, local), "a local judge runs on an observable host")
	assert.True(t, DispatchesLocalModels([]*evalv1.ModelVariant{hosted, llamacpp}, hosted))
	assert.True(t, DispatchesLocalModels([]*evalv1.ModelVariant{local}, nil))
}

func TestObservableModelInferences_ExcludesHostedInferences(t *testing.T) {
	t.Parallel()
	local := &evalv1.ModelInferenceRecord{ProviderAttemptId: "local", ModelVariant: &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderOllama}}
	hosted := &evalv1.ModelInferenceRecord{ProviderAttemptId: "hosted", ModelVariant: &evalv1.ModelVariant{ProviderClass: constants.InferenceProviderGemini}}
	result := &evalv1.EvaluationAssignmentResult{ModelInferences: []*evalv1.ModelInferenceRecord{local, nil, hosted}}

	assert.Equal(t, []*evalv1.ModelInferenceRecord{local, hosted}, scoredModelInferences(result), "every scored inference still needs provenance")
	assert.Equal(t, []*evalv1.ModelInferenceRecord{local}, observableModelInferences(result), "only a local inference has a host to observe")
	assert.Empty(t, observableModelInferences(nil))
	assert.True(t, ProviderObservable(local))
	assert.False(t, ProviderObservable(hosted))
}

func TestAgentTraceContextMessage_CarriesTheProviderSignature(t *testing.T) {
	t.Parallel()
	held := AgentTraceContextMessage{
		Role:      operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT.String(),
		ToolCalls: []AgentTraceContextToolCall{{CallID: "c1", ProviderSignature: "sig-opaque", Name: "lookup", ArgumentsJSON: `{}`}},
	}
	message, err := held.inferenceMessage()
	require.NoError(t, err)
	require.Len(t, message.GetParts(), 1)
	assert.Equal(t, "sig-opaque", message.GetParts()[0].GetToolCall().GetProviderSignature(), "a hosted continuation returns the signature the provider issued")
}
