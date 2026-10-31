// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"testing"

	"github.com/jitllm/jitllm/engine/model"
	"github.com/jitllm/jitllm/format/jlm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestNewJitLLMBackend_RequiresModelRootAndLogger(t *testing.T) {
	_, err := NewJitLLMBackend(" ", testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrInferenceJitLLMModelRootRequired)
	_, err = NewJitLLMBackend("/srv/jlm", nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestJitLLMUnloadAfterRequest(t *testing.T) {
	for keepAlive, want := range map[string]bool{"": false, "-1": false, "0": true} {
		got, err := jitllmUnloadAfterRequest(keepAlive)
		require.NoError(t, err, keepAlive)
		assert.Equal(t, want, got, keepAlive)
	}
	_, err := jitllmUnloadAfterRequest("5m")
	require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)
}

func TestJitLLMChatRequest_MapsMessagesToolsAndSampling(t *testing.T) {
	parallel := false
	topK := int32(-3)
	req := models.GenerateRequest{
		Model: "qwen",
		Messages: textInferenceMessages("weather?"),
		Tools:             []*operatorv1.InferenceToolDeclaration{{Name: "lookup", Description: "find it", JsonSchema: `{"type":"object"}`}},
		ParallelToolCalls: &parallel,
		Temperature:       0.2,
		TopK:              &topK,
		StopSequences:     []string{"END"},
	}
	chatReq, err := jitllmChatRequestFor(req)
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 1)
	assert.Equal(t, "user", chatReq.Messages[0].Role)
	assert.Equal(t, "weather?", chatReq.Messages[0].Content)
	assert.JSONEq(t, `[{"type":"function","function":{"name":"lookup","description":"find it","parameters":{"type":"object"}}}]`, string(chatReq.Tools))
	assert.True(t, chatReq.SingleToolCall)

	opts := chatReq.generateOptions()
	assert.Equal(t, "qwen", opts.ModelID)
	assert.True(t, opts.Prompt.Chat.AddGenerationPrompt)
	assert.True(t, opts.Prompt.Chat.ToolChoice.Single)
	assert.InDelta(t, 0.2, opts.Sampling.Temp, 1e-6)
	assert.Zero(t, opts.Sampling.TopK, "a negative top_k disables top-k")
	assert.Equal(t, []string{"END"}, opts.Stop)
	assert.Equal(t, int(resolveOutputTokenLimit(0)), opts.MaxTokens)
}

// The engine's System One parser and encoder must agree with ztd's request
// body and response validator for every question type.
func TestJitLLMDecision_RoundTripsThroughTheEngineWire(t *testing.T) {
	req := models.DecisionRequest{Model: "jev", Input: &operatorv1.InferenceDecisionRequest{
		State: "Read the workspace inventory",
		Questions: []*operatorv1.InferenceDecisionQuestion{
			{Name: "safe", Instructions: "Is this read-only?", Kind: &operatorv1.InferenceDecisionQuestion_Noul{Noul: &operatorv1.InferenceNoulQuestion{}}},
			{Name: "tier", Instructions: "Which tier?", Kind: &operatorv1.InferenceDecisionQuestion_Choice{Choice: &operatorv1.InferenceChoiceQuestion{Criteria: []*operatorv1.InferenceDecisionCriterion{{Label: "low"}, {Label: "high", Description: "needs review"}}}}},
			{Name: "risk", Instructions: "How risky?", Kind: &operatorv1.InferenceDecisionQuestion_Score{Score: &operatorv1.InferenceScoreQuestion{Criteria: []string{"none", "some", "severe"}}}},
		},
	}}
	body, err := decisionBody(req)
	require.NoError(t, err)
	_, questions, err := jitllmDecisionInput(body)
	require.NoError(t, err)
	require.Len(t, questions, 3)

	answers := make([]model.DecisionAnswer, len(questions))
	for i, q := range questions {
		probs := make([]float64, len(q.Options))
		for j := range probs {
			probs[j] = 1 / float64(len(probs))
		}
		answers[i] = model.DecisionAnswer{Type: q.Type, Probs: probs, Confidence: 0.5}
		switch q.Type {
		case jlm.QuestionNoul:
			answers[i].Noul = 0.75
		case jlm.QuestionChoice:
			answers[i].Choice = q.Options[1].Key
		case jlm.QuestionScore:
			answers[i].Score = 1
		}
	}
	raw := model.DecisionResponse("jev", questions, answers, 42)
	response, err := decodeSystemOneResponse(testutil.Context(t), raw, body, req, defaultMaxResponseBytes)
	require.NoError(t, err)
	assert.Equal(t, "jev", response.Model)
	assert.Equal(t, int32(42), response.PromptTokens)
}
