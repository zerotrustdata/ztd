// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package judge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	judgeModelTag    = "judge-model:latest"
	judgeCampaignID  = "campaign-1"
	judgeSessionID   = "infer-session"
	judgeObjective   = "Explain the open incident"
	judgeFinalReply  = "The incident was a stale lock."
	judgeMemoryFact1 = "stale lock on the release queue"
	judgeRunID       = "run-1"
	judgeAssignment  = "assignment-1"
	judgeScenarioID  = "scenario-1"
)

type fakeDispatcher struct {
	requests []*operatorv1.InferenceDispatchRequest
	respond  func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error)
}

func (f *fakeDispatcher) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	f.requests = append(f.requests, req)
	return f.respond(req)
}

// judgmentResponse builds a response that passes ValidateInferenceProbeResponse for req,
// carrying text as the judge's result.
func judgmentResponse(req *operatorv1.InferenceDispatchRequest, text string) *operatorv1.InferenceDispatchResponse {
	result := &operatorv1.InferenceResult{
		ProviderAttemptId:   req.GetProviderAttemptId(),
		RequestedModel:      req.GetModel(),
		ResultDigest:        "digest",
		CampaignId:          req.GetCampaignId(),
		ModelRegistryDigest: req.GetModelRegistryDigest(),
		RunId:               req.GetRunId(),
		AssignmentId:        req.GetAssignmentId(),
		ScenarioId:          req.GetScenarioId(),
		Parts:               []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: text}}},
	}
	return &operatorv1.InferenceDispatchResponse{
		TransactionId: "tx-" + req.GetProviderAttemptId(),
		Result:        result,
		Receipt:       &operatorv1.ActionReceipt{ResultSummary: result.GetResultDigest()},
	}
}

func judgeVariant() *evalv1.ModelVariant {
	return &evalv1.ModelVariant{
		VariantId:      "judge",
		ServedModelTag: judgeModelTag,
		ModelDigest:    strings.Repeat("d", 64),
	}
}

func judgeRegistry(t *testing.T) ([]*operatorv1.InferenceModelVariant, string) {
	t.Helper()
	registry := []*operatorv1.InferenceModelVariant{{Model: judgeModelTag, Digest: strings.Repeat("d", 64)}}
	digest, err := models.ComputeInferenceModelRegistryDigest(judgeCampaignID, registry)
	require.NoError(t, err)
	return registry, digest
}

// judgeConfig is a valid campaign-authorized configuration; mutate breaks one field per test.
func judgeConfig(t *testing.T, mutate func(*Config)) Config {
	t.Helper()
	registry, digest := judgeRegistry(t)
	cfg := Config{
		InferenceSessionID:  judgeSessionID,
		Variant:             judgeVariant(),
		CampaignID:          judgeCampaignID,
		ModelRegistryDigest: digest,
		ModelRegistry:       registry,
		RunID:               judgeRunID,
		AssignmentID:        judgeAssignment,
		ScenarioID:          judgeScenarioID,
		NewID:               func(prefix string) string { return prefix },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

func newTestJudge(t *testing.T, d Dispatcher, mutate func(*Config)) *Judge {
	t.Helper()
	j, err := New(d, judgeConfig(t, mutate))
	require.NoError(t, err)
	return j
}

func agentStep(role evalv1.AgentRole, class evalv1.EvaluationCallClassification, memory string) *evalv1.AgentStep {
	return &evalv1.AgentStep{
		Role:      role,
		CallClass: class,
		Output:    &evalv1.AgentStep_Memory{Memory: &evalv1.MemoryOutput{MemoryText: memory}},
	}
}

func postTurnMemory(memory string) *evalv1.AgentStep {
	return agentStep(evalv1.AgentRole_AGENT_ROLE_MEMORY_WRITER, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_POST_TURN, memory)
}

func scoredChainMemory(memory string) *evalv1.AgentStep {
	return agentStep(evalv1.AgentRole_AGENT_ROLE_MEMORY_WRITER, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN, memory)
}

func trace(finalReply string, steps ...*evalv1.AgentStep) *evalv1.AgentTrace {
	return &evalv1.AgentTrace{FinalReply: finalReply, Steps: steps}
}

func TestScoreTrace_FinalReplyAndMemoryAreScoredThroughCampaignAuthorizedDispatch(t *testing.T) {
	dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		if req.GetProviderAttemptId() == "judge-"+string(SubjectFinalReply) {
			return judgmentResponse(req, `{"score": 4, "rationale": "answers the objective"}`), nil
		}
		return judgmentResponse(req, `{"score": 3, "rationale": "states most facts"}`), nil
	}}
	j := newTestJudge(t, dispatcher, nil)

	scores := j.ScoreTrace(context.Background(), trace(judgeFinalReply, postTurnMemory("stale lock noted")), judgeObjective, []string{judgeMemoryFact1})

	require.Len(t, scores, 2)
	assert.Equal(t, Score{
		Subject:          SubjectFinalReply,
		Status:           StatusScored,
		Value:            4,
		Rationale:        "answers the objective",
		JudgeModelTag:    judgeModelTag,
		JudgeModelDigest: strings.Repeat("d", 64),
		TransactionID:    "tx-judge-final_reply",
		Receipt:          &operatorv1.ActionReceipt{ResultSummary: "digest"},
		RubricVersion:    RubricVersion,
	}, scores[0])
	assert.Equal(t, SubjectMemory, scores[1].Subject)
	assert.Equal(t, StatusScored, scores[1].Status)
	assert.Equal(t, 3, scores[1].Value)
	assert.Equal(t, "tx-judge-memory", scores[1].TransactionID)

	require.Len(t, dispatcher.requests, 2)
	_, digest := judgeRegistry(t)
	for _, req := range dispatcher.requests {
		assert.Equal(t, judgeModelTag, req.GetModel())
		assert.Equal(t, strings.Repeat("d", 64), req.GetModelDigest())
		assert.Equal(t, judgeSessionID, req.GetTargetOperatorSessionId())
		assert.Equal(t, judgeCampaignID, req.GetCampaignId())
		assert.Equal(t, judgeRunID, req.GetRunId())
		assert.Equal(t, judgeAssignment, req.GetAssignmentId())
		assert.Equal(t, judgeScenarioID, req.GetScenarioId())
		assert.Equal(t, digest, req.GetModelRegistryDigest())
		assert.Len(t, req.GetModelRegistry(), 1)
		assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, req.GetMessages()[0].GetRole())
		assert.Equal(t, "application/json", req.GetResponseFormat().GetMediaType())
		assert.Contains(t, req.GetResponseFormat().GetJsonSchema(), `"score"`)
		assert.Contains(t, req.GetResponseFormat().GetJsonSchema(), `"rationale"`)
	}
	finalUser := requestUserText(dispatcher.requests[0])
	assert.Contains(t, finalUser, judgeObjective)
	assert.Contains(t, finalUser, judgeFinalReply)
	memoryUser := requestUserText(dispatcher.requests[1])
	assert.Contains(t, memoryUser, judgeMemoryFact1)
	assert.Contains(t, memoryUser, "stale lock noted")
}

func requestUserText(req *operatorv1.InferenceDispatchRequest) string {
	return req.GetMessages()[1].GetParts()[0].GetText()
}

func TestScoreTrace_TraceWithoutFinalReplyOrMemoryWriteGivesNoScoresAndNoDispatch(t *testing.T) {
	cases := map[string]*evalv1.AgentTrace{
		"nil trace":                      nil,
		"empty trace":                    {},
		"scored-chain memory write only": trace("", scoredChainMemory("chain memory")),
		"memory writer without its output": trace("", &evalv1.AgentStep{
			Role:      evalv1.AgentRole_AGENT_ROLE_MEMORY_WRITER,
			CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_POST_TURN,
		}),
	}
	for name, tr := range cases {
		t.Run(name, func(t *testing.T) {
			dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"score": 5, "rationale": "x"}`), nil
			}}
			j := newTestJudge(t, dispatcher, nil)

			scores := j.ScoreTrace(context.Background(), tr, judgeObjective, []string{judgeMemoryFact1})

			assert.Empty(t, scores)
			assert.Empty(t, dispatcher.requests)
		})
	}
}

func TestScoreTrace_OnlyLastPostTurnMemoryWriteIsJudged(t *testing.T) {
	dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		return judgmentResponse(req, `{"score": 2, "rationale": "x"}`), nil
	}}
	j := newTestJudge(t, dispatcher, nil)

	scores := j.ScoreTrace(context.Background(), trace("",
		postTurnMemory("earlier-write"),
		postTurnMemory("latest-write"),
		scoredChainMemory("scored-chain-write"),
	), judgeObjective, nil)

	require.Len(t, scores, 1)
	assert.Equal(t, SubjectMemory, scores[0].Subject)
	require.Len(t, dispatcher.requests, 1)
	memoryUser := requestUserText(dispatcher.requests[0])
	assert.Contains(t, memoryUser, "latest-write")
	assert.NotContains(t, memoryUser, "earlier-write")
	assert.NotContains(t, memoryUser, "scored-chain-write")
}

func TestScoreTrace_ScoreFailuresAreFailedScoresNeverErrors(t *testing.T) {
	cases := []struct {
		name     string
		respond  func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error)
		wantTxID string
	}{
		{
			name: "dispatch error",
			respond: func(*operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceBackendTimeout)
			},
		},
		{
			name: "nil response without error",
			respond: func(*operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return nil, nil
			},
		},
		{
			name: "response for another attempt",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.GetResult().ProviderAttemptId = "forged-attempt"
				return resp, nil
			},
			wantTxID: "tx-judge-final_reply",
		},
		{
			name: "result for another run",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.GetResult().RunId = "other-run"
				return resp, nil
			},
			wantTxID: "tx-judge-final_reply",
		},
		{
			name: "result for another assignment",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.GetResult().AssignmentId = "other-assignment"
				return resp, nil
			},
			wantTxID: "tx-judge-final_reply",
		},
		{
			name: "result for another scenario",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.GetResult().ScenarioId = "other-scenario"
				return resp, nil
			},
			wantTxID: "tx-judge-final_reply",
		},
		{
			name: "score 0",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"score": 0, "rationale": "x"}`), nil
			},
			wantTxID: "tx-judge-final_reply",
		},
		{
			name: "score 6",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"score": 6, "rationale": "x"}`), nil
			},
			wantTxID: "tx-judge-final_reply",
		},
		{
			name: "unknown field",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"score": 3, "rationale": "x", "verdict": "pass"}`), nil
			},
			wantTxID: "tx-judge-final_reply",
		},
		{
			name: "non-JSON text",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, "not-json"), nil
			},
			wantTxID: "tx-judge-final_reply",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &fakeDispatcher{respond: tc.respond}
			j := newTestJudge(t, dispatcher, nil)

			scores := j.ScoreTrace(context.Background(), trace(judgeFinalReply), judgeObjective, nil)

			require.Len(t, scores, 1)
			assert.Equal(t, StatusFailed, scores[0].Status)
			assert.Equal(t, 0, scores[0].Value)
			assert.NotEmpty(t, scores[0].Detail)
			assert.Equal(t, tc.wantTxID, scores[0].TransactionID)
		})
	}
}

func TestScoreTrace_CampaignAuthorityMismatchFailsWithoutDispatch(t *testing.T) {
	dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		return judgmentResponse(req, `{"score": 3, "rationale": "x"}`), nil
	}}
	j := newTestJudge(t, dispatcher, func(cfg *Config) { cfg.ModelRegistryDigest = "not-the-registry-digest" })

	scores := j.ScoreTrace(context.Background(), trace(judgeFinalReply), judgeObjective, nil)

	require.Len(t, scores, 1)
	assert.Equal(t, StatusFailed, scores[0].Status)
	assert.Empty(t, dispatcher.requests)
}

func TestScoreTrace_MissingRunAssignmentOrScenarioIDFailsWithoutDispatch(t *testing.T) {
	cases := map[string]func(*Config){
		"empty run id":        func(cfg *Config) { cfg.RunID = "" },
		"empty assignment id": func(cfg *Config) { cfg.AssignmentID = "" },
		"empty scenario id":   func(cfg *Config) { cfg.ScenarioID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"score": 3, "rationale": "x"}`), nil
			}}
			j := newTestJudge(t, dispatcher, mutate)

			req, _, err := j.buildDispatchRequest(SubjectFinalReply, judgeFinalReply)
			assert.Nil(t, req)
			assert.True(t, errors.Is(err, constants.ErrMissingRequiredField))

			scores := j.ScoreTrace(context.Background(), trace(judgeFinalReply), judgeObjective, nil)

			require.Len(t, scores, 1)
			assert.Equal(t, StatusFailed, scores[0].Status)
			assert.Empty(t, dispatcher.requests)
		})
	}
}

func TestValidateResponse_RejectsResultBoundToAnotherRunAssignmentOrScenario(t *testing.T) {
	cases := map[string]func(*operatorv1.InferenceResult){
		"another run":        func(r *operatorv1.InferenceResult) { r.RunId = "other-run" },
		"another assignment": func(r *operatorv1.InferenceResult) { r.AssignmentId = "other-assignment" },
		"another scenario":   func(r *operatorv1.InferenceResult) { r.ScenarioId = "other-scenario" },
		"missing run":        func(r *operatorv1.InferenceResult) { r.RunId = "" },
	}
	for name, forge := range cases {
		t.Run(name, func(t *testing.T) {
			j := newTestJudge(t, &fakeDispatcher{}, nil)
			req, providerAttemptID, err := j.buildDispatchRequest(SubjectFinalReply, judgeFinalReply)
			require.NoError(t, err)
			resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
			forge(resp.GetResult())

			err = j.validateResponse(providerAttemptID, resp)

			assert.True(t, errors.Is(err, constants.ErrIdentityBindingFailed))
		})
	}
	t.Run("result carrying the requested identity validates", func(t *testing.T) {
		j := newTestJudge(t, &fakeDispatcher{}, nil)
		req, providerAttemptID, err := j.buildDispatchRequest(SubjectFinalReply, judgeFinalReply)
		require.NoError(t, err)

		require.NoError(t, j.validateResponse(providerAttemptID, judgmentResponse(req, `{"score": 3, "rationale": "x"}`)))
	})
}

func TestDecodeJudgment_AcceptsOnlyTheDeclaredShape(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantValue int
		wantErr   bool
	}{
		{name: "valid score 5", text: `{"score": 5, "rationale": "clear"}`, wantValue: 5},
		{name: "valid score 1", text: `{"score": 1, "rationale": "poor"}`, wantValue: 1},
		{name: "score 0", text: `{"score": 0, "rationale": "x"}`, wantErr: true},
		{name: "score 6", text: `{"score": 6, "rationale": "x"}`, wantErr: true},
		{name: "fractional score", text: `{"score": 3.5, "rationale": "x"}`, wantErr: true},
		{name: "unknown field", text: `{"score": 3, "rationale": "x", "extra": true}`, wantErr: true},
		{name: "missing rationale", text: `{"score": 3}`, wantErr: true},
		{name: "missing score", text: `{"rationale": "x"}`, wantErr: true},
		{name: "non-JSON text", text: "not-json", wantErr: true},
		{name: "empty text", text: "", wantErr: true},
		{name: "trailing value after judgment", text: `{"score": 3, "rationale": "x"} {}`, wantErr: true},
		{name: "fenced JSON is not accepted", text: "```json\n{\"score\": 3, \"rationale\": \"x\"}\n```", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := &operatorv1.InferenceResult{
				Parts: []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: tc.text}}},
			}

			value, _, err := decodeJudgment(result)

			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, constants.ErrJudgeResponseInvalid))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantValue, value)
		})
	}
}

func TestNew_RejectsEachMissingRequiredField(t *testing.T) {
	cases := []struct {
		name          string
		nilDispatcher bool
		mutate        func(*Config)
	}{
		{name: "nil dispatcher", nilDispatcher: true},
		{name: "empty session id", mutate: func(cfg *Config) { cfg.InferenceSessionID = "" }},
		{name: "nil variant", mutate: func(cfg *Config) { cfg.Variant = nil }},
		{name: "empty served model tag", mutate: func(cfg *Config) { cfg.Variant.ServedModelTag = "" }},
		{name: "empty model digest", mutate: func(cfg *Config) { cfg.Variant.ModelDigest = "" }},
		{name: "nil NewID", mutate: func(cfg *Config) { cfg.NewID = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d Dispatcher = &fakeDispatcher{}
			if tc.nilDispatcher {
				d = nil
			}
			_, err := New(d, judgeConfig(t, tc.mutate))
			assert.True(t, errors.Is(err, constants.ErrMissingRequiredField))
		})
	}
	t.Run("valid configuration constructs a judge", func(t *testing.T) {
		_, err := New(&fakeDispatcher{}, judgeConfig(t, nil))
		require.NoError(t, err)
	})
}
