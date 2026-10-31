// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package inference

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jitllm/jitllmtest"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// newStoriesBackend runs the embedded engine over a models folder holding
// one real container, stories.jlm, and returns the backend with the
// container's digest.
func newStoriesBackend(t *testing.T) (*JitLLMBackend, string, string) {
	t.Helper()
	root := testutil.TempDir(t)
	path := jitllmtest.WriteChatContainer(t, testutil.TempDir(t), root, "stories")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	backend, err := NewJitLLMBackend(root, testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	return backend, path, models.SHA256Hex(body)
}

func seed(v int32) *int32 { return &v }

func TestJitLLMGenerate_RunsTheModelInProcess(t *testing.T) {
	backend, _, digest := newStoriesBackend(t)
	ctx := testutil.Context(t)

	_, err := backend.Generate(ctx, models.GenerateRequest{
		Model:    "stories",
		Messages: textInferenceMessages("why"),
		Thinking: &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: true}},
	})
	require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)

	maxTokens := int32(8)
	req := models.GenerateRequest{Model: "stories", ModelDigest: digest, Messages: textInferenceMessages("Once upon a time"), MaxTokens: maxTokens, Seed: seed(7), Temperature: 0.8}
	first, err := backend.Generate(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "stories", first.Model)
	assert.Equal(t, digest, first.ServedModelDigest)
	assert.True(t, first.UsageReported)
	assert.Positive(t, first.PromptTokens)
	assert.Positive(t, first.CompletionTokens)
	assert.LessOrEqual(t, first.CompletionTokens, maxTokens)
	assert.Equal(t, first.PromptTokens+first.CompletionTokens, first.TotalTokens)
	assert.Contains(t, []string{"stop", "length"}, first.FinishReason)
	assert.Equal(t, operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER, first.TimingSource)
	require.NotNil(t, first.TimeToFirstTokenNS)
	assert.NotEmpty(t, first.OutputHash)
	assert.NotEmpty(t, first.NormalizedRequestHash)

	second, err := backend.Generate(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, first.OutputHash, second.OutputHash, "a seeded request reproduces its output")
	assert.Equal(t, first.NormalizedRequestHash, second.NormalizedRequestHash)

	residency, err := backend.ReadResidency(ctx)
	require.NoError(t, err)
	assert.Contains(t, residency.Models, ProviderResidencyModel{Name: "stories"}, "keep-alive -1 leaves the model loaded")
}

func TestJitLLMGenerate_StreamsProgressThatReconcilesWithTheResult(t *testing.T) {
	backend, _, _ := newStoriesBackend(t)
	ctx := testutil.Context(t)
	scrubber, err := scrubbing.NewScrubbingService(ctx, &scrubbing.Config{Enabled: true}, testutil.NewTestLogger(), nil)
	require.NoError(t, err)
	var events []*operatorv1.InferenceProgressEvent
	ctx = WithProgressReporter(ctx, func(event *operatorv1.InferenceProgressEvent) error {
		events = append(events, event)
		return nil
	})
	response, err := backend.Generate(ctx, models.GenerateRequest{
		Model: "stories", Messages: textInferenceMessages("Once upon a time"), MaxTokens: 24, Seed: seed(3), Temperature: 0.8,
		Stream: true, Scrubber: scrubber, ProviderAttemptID: "attempt-stream",
	})
	require.NoError(t, err)
	require.NotEmpty(t, events)
	response.ProviderAttemptID = "attempt-stream"
	require.NoError(t, models.ReconcileInferenceProgress(events, response.ToProtoInferenceResult()))
}

func TestJitLLMGenerate_KeepAliveZeroUnloadsAndDigestMismatchRefuses(t *testing.T) {
	backend, _, _ := newStoriesBackend(t)
	ctx := testutil.Context(t)
	maxTokens := int32(2)

	_, err := backend.Generate(ctx, models.GenerateRequest{Model: "stories", Messages: textInferenceMessages("hi"), MaxTokens: maxTokens, KeepAlive: "0"})
	require.NoError(t, err)
	residency, err := backend.ReadResidency(ctx)
	require.NoError(t, err)
	assert.Empty(t, residency.Models)

	_, err = backend.Generate(ctx, models.GenerateRequest{Model: "stories", Messages: textInferenceMessages("hi"), KeepAlive: "5m"})
	require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)

	_, err = backend.Generate(ctx, models.GenerateRequest{Model: "stories", ModelDigest: models.SHA256Hex([]byte("other")), Messages: textInferenceMessages("hi")})
	require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)

	_, err = backend.Generate(ctx, models.GenerateRequest{Model: "absent", Messages: textInferenceMessages("hi")})
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
}

func TestJitLLMDecide_RefusesAModelWithNoDecisionReadout(t *testing.T) {
	backend, _, digest := newStoriesBackend(t)
	_, err := backend.Decide(testutil.Context(t), models.DecisionRequest{Model: "stories", ModelDigest: digest, Input: decisionInput()})
	require.ErrorIs(t, err, constants.ErrInferenceDecisionRequestInvalid)
}

func TestJitLLMReleaseAndInventory(t *testing.T) {
	backend, path, digest := newStoriesBackend(t)
	ctx := testutil.Context(t)

	inventory, err := backend.ListProviderModelInventory(ctx)
	require.NoError(t, err)
	require.Len(t, inventory, 1)
	entry := inventory[0]
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, constants.InferenceProviderJitLLM, entry.ProviderClass)
	assert.Equal(t, "stories", entry.ServedModelTag)
	assert.Equal(t, digest, entry.ModelDigest)
	assert.Equal(t, "llama", entry.ModelFamily)
	assert.Equal(t, "F32", entry.Quantization)
	assert.Equal(t, constants.ModelWeightFormatJLM, entry.Format)
	assert.Equal(t, uint64(info.Size()), entry.ArtifactSizeBytes)
	assert.Positive(t, entry.ParameterCount)
	assert.Equal(t, operatorv1.ParameterCountSource_PARAMETER_COUNT_SOURCE_MODEL_METADATA, entry.ParameterCountSource)

	status, err := backend.Status(ctx)
	require.NoError(t, err)
	assert.True(t, status.Available)
	assert.Contains(t, status.Models, "stories")

	maxTokens := int32(1)
	_, err = backend.Generate(ctx, models.GenerateRequest{Model: "stories", Messages: textInferenceMessages("hi"), MaxTokens: maxTokens})
	require.NoError(t, err)
	require.NoError(t, backend.ReleaseModels(ctx, []string{"stories"}))
	residency, err := backend.ReadResidency(ctx)
	require.NoError(t, err)
	assert.Empty(t, residency.Models)

	require.NoError(t, backend.ReleaseModels(ctx, []string{"stories"}), "releasing a model that is not loaded is a no-op")
	require.ErrorIs(t, backend.ReleaseModels(ctx, []string{"absent"}), constants.ErrInferenceModelNotFound)
}

func TestJitLLMModelDigest_RejectsFileChangedAfterIdentitySnapshot(t *testing.T) {
	backend, path, digest := newStoriesBackend(t)
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	backend.digests.now = func() time.Time {
		changed := append(append([]byte(nil), original...), 0)
		require.NoError(t, os.WriteFile(path, changed, constants.PermFilePrivate))
		return time.Now()
	}
	err = backend.verifyModelDigest(testutil.Context(t), "stories", digest)
	require.ErrorIs(t, err, constants.ErrInferenceModelFileChanged)
}
