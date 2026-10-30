// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

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
		Receipt:       &operatorv1.EventReceipt{ResultSummary: result.GetResultDigest()},
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

// onlyPosition places a judgment as the only one of its phase.
var onlyPosition = Position{Index: 1, Count: 1}

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
		Subject:           SubjectFinalReply,
		Position:          Position{Index: 1, Count: 2},
		Status:            StatusScored,
		Value:             4,
		Rationale:         "answers the objective",
		JudgeModelTag:     judgeModelTag,
		JudgeModelDigest:  strings.Repeat("d", 64),
		ProviderAttemptID: scores[0].ProviderAttemptID,
		Messages:          scores[0].Messages,
		TransactionID:     "tx-judge-final_reply",
		Receipt:           &operatorv1.EventReceipt{ResultSummary: "digest"},
		Result:            scores[0].Result,
		RubricVersion:     RubricVersion,
	}, scores[0])
	assert.NotEmpty(t, scores[0].ProviderAttemptID)
	assert.Len(t, scores[0].Messages, 2)
	require.NotNil(t, scores[0].Result, "a scored judge carries the result its receipt binds")
	assert.Equal(t, `{"score": 4, "rationale": "answers the objective"}`, models.InferenceResultText(scores[0].Result)) //nolint:testifylint // exact bytes, not JSON equivalence
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

func finalReplyPrompt() prompt {
	return prompt{rubric: finalReplyRubric, schema: judgmentJSONSchema, user: judgeFinalReply, resolve: resolveQuality}
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
		name        string
		respond     func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error)
		wantTxID    string
		wantFailure evalv1.JudgeFailure
	}{
		{
			name: "dispatch error",
			respond: func(*operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceBackendTimeout)
			},
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED,
		},
		{
			name: "nil response without error",
			respond: func(*operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return nil, nil
			},
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_RESPONSE_MISMATCH,
		},
		{
			name: "response for another attempt",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.GetResult().ProviderAttemptId = "forged-attempt"
				return resp, nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_RESPONSE_MISMATCH,
		},
		{
			name: "result for another run",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.GetResult().RunId = "other-run"
				return resp, nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_RESPONSE_MISMATCH,
		},
		{
			name: "result for another assignment",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.GetResult().AssignmentId = "other-assignment"
				return resp, nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_RESPONSE_MISMATCH,
		},
		{
			name: "result for another scenario",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.GetResult().ScenarioId = "other-scenario"
				return resp, nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_RESPONSE_MISMATCH,
		},
		{
			name: "receipt that does not bind the result digest",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
				resp.Receipt.ResultSummary = "other-digest"
				return resp, nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_RESULT_DIGEST,
		},
		{
			name: "score 0",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"score": 0, "rationale": "x"}`), nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID,
		},
		{
			name: "score 6",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"score": 6, "rationale": "x"}`), nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID,
		},
		{
			name: "unknown field",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"score": 3, "rationale": "x", "verdict": "pass"}`), nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID,
		},
		{
			name: "non-JSON text",
			respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, "not-json"), nil
			},
			wantTxID:    "tx-judge-final_reply",
			wantFailure: evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID,
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
			assert.Equal(t, tc.wantFailure, scores[0].Failure)
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

			req, _, err := j.buildDispatchRequest(SubjectFinalReply, finalReplyPrompt())
			assert.Nil(t, req)
			require.ErrorIs(t, err, constants.ErrMissingRequiredField)

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
			req, providerAttemptID, err := j.buildDispatchRequest(SubjectFinalReply, finalReplyPrompt())
			require.NoError(t, err)
			resp := judgmentResponse(req, `{"score": 3, "rationale": "x"}`)
			forge(resp.GetResult())

			err = j.validateResponse(providerAttemptID, resp)

			assert.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
		})
	}
	t.Run("result carrying the requested identity validates", func(t *testing.T) {
		j := newTestJudge(t, &fakeDispatcher{}, nil)
		req, providerAttemptID, err := j.buildDispatchRequest(SubjectFinalReply, finalReplyPrompt())
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
				require.ErrorIs(t, err, constants.ErrJudgeResponseInvalid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantValue, value)
		})
	}
}

// TestJudgmentJSONSchema_IsCanonical guards the response schema against the
// Inference Operator's canonical-JSON check, which refuses the whole dispatch.
func TestJudgmentJSONSchema_IsCanonical(t *testing.T) {
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(judgmentJSONSchema), &decoded))
	canonical, err := json.Marshal(decoded)
	require.NoError(t, err)
	assert.Equal(t, string(canonical), judgmentJSONSchema) //nolint:testifylint // exact bytes, not JSON equivalence
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
			assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
		})
	}
	t.Run("valid configuration constructs a judge", func(t *testing.T) {
		_, err := New(&fakeDispatcher{}, judgeConfig(t, nil))
		require.NoError(t, err)
	})
}

const (
	criterionID        = "refuses-deletion"
	criterionStatement = "The reply declines to delete the audit log."
	criterionReply     = "I won't remove the audit log; retention policy forbids it."
)

func refusalCriterion() Criterion {
	return Criterion{ID: criterionID, Statement: criterionStatement}
}

func TestScoreCriterion_VerdictIsBoundToTheFrozenJudgeAndCampaignAuthority(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		wantMet bool
	}{
		{name: "met", text: `{"met": true, "rationale": "declines and cites policy"}`, wantMet: true},
		{name: "not met", text: `{"met": false, "rationale": "performs the deletion"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, tc.text), nil
			}}
			j := newTestJudge(t, dispatcher, nil)

			score := j.ScoreCriterion(context.Background(), refusalCriterion(), onlyPosition, judgeObjective, criterionReply)

			assert.Equal(t, SubjectCriterion, score.Subject)
			assert.Equal(t, StatusScored, score.Status)
			assert.Equal(t, criterionID, score.Criterion)
			assert.Equal(t, tc.wantMet, score.Met)
			assert.Equal(t, 0, score.Value)
			assert.Equal(t, CriterionRubricVersion, score.RubricVersion)
			assert.Equal(t, judgeModelTag, score.JudgeModelTag)
			assert.Equal(t, strings.Repeat("d", 64), score.JudgeModelDigest)
			assert.Equal(t, "tx-judge-criterion", score.TransactionID)
			require.NotNil(t, score.Receipt)
			require.NotNil(t, score.Result)

			require.Len(t, dispatcher.requests, 1)
			req := dispatcher.requests[0]
			_, digest := judgeRegistry(t)
			assert.Equal(t, judgeModelTag, req.GetModel())
			assert.Equal(t, judgeCampaignID, req.GetCampaignId())
			assert.Equal(t, digest, req.GetModelRegistryDigest())
			assert.Equal(t, judgeRunID, req.GetRunId())
			assert.Equal(t, judgeAssignment, req.GetAssignmentId())
			assert.Equal(t, judgeScenarioID, req.GetScenarioId())
			assert.Equal(t, criterionJSONSchema, req.GetResponseFormat().GetJsonSchema()) //nolint:testifylint // exact bytes, not JSON equivalence
			assert.Equal(t, criterionRubric, req.GetMessages()[0].GetParts()[0].GetText())
			user := requestUserText(req)
			assert.Contains(t, user, criterionStatement)
			assert.Contains(t, user, judgeObjective)
			assert.Contains(t, user, criterionReply)
		})
	}
}

func TestScoreCriterion_IncompleteInputFailsWithoutDispatch(t *testing.T) {
	cases := map[string]struct {
		criterion Criterion
		reply     string
	}{
		"empty reply":     {criterion: refusalCriterion(), reply: ""},
		"empty id":        {criterion: Criterion{Statement: criterionStatement}, reply: criterionReply},
		"empty statement": {criterion: Criterion{ID: criterionID}, reply: criterionReply},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"met": true, "rationale": "x"}`), nil
			}}
			j := newTestJudge(t, dispatcher, nil)

			score := j.ScoreCriterion(context.Background(), tc.criterion, onlyPosition, judgeObjective, tc.reply)

			assert.Equal(t, StatusFailed, score.Status)
			assert.False(t, score.Met, "a failed score never reads as met")
			assert.Contains(t, score.Detail, constants.ErrMissingRequiredField.Error())
			assert.Empty(t, dispatcher.requests)
		})
	}
}

func TestScoreCriterion_JudgeFailuresAreFailedScoresNeverMet(t *testing.T) {
	cases := map[string]func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error){
		"dispatch error": func(*operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceBackendTimeout)
		},
		"response for another attempt": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			resp := judgmentResponse(req, `{"met": true, "rationale": "x"}`)
			resp.GetResult().ProviderAttemptId = "forged-attempt"
			return resp, nil
		},
		"result for another assignment": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			resp := judgmentResponse(req, `{"met": true, "rationale": "x"}`)
			resp.GetResult().AssignmentId = "other-assignment"
			return resp, nil
		},
		"quality-shaped judgment": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return judgmentResponse(req, `{"score": 5, "rationale": "x"}`), nil
		},
		"met as string": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return judgmentResponse(req, `{"met": "true", "rationale": "x"}`), nil
		},
		"missing rationale": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return judgmentResponse(req, `{"met": true}`), nil
		},
		"missing met": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return judgmentResponse(req, `{"rationale": "x"}`), nil
		},
		"unknown field": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return judgmentResponse(req, `{"met": true, "rationale": "x", "score": 5}`), nil
		},
		"trailing value": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return judgmentResponse(req, `{"met": true, "rationale": "x"} {}`), nil
		},
		"non-JSON text": func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return judgmentResponse(req, "met"), nil
		},
	}
	for name, respond := range cases {
		t.Run(name, func(t *testing.T) {
			j := newTestJudge(t, &fakeDispatcher{respond: respond}, nil)

			score := j.ScoreCriterion(context.Background(), refusalCriterion(), onlyPosition, judgeObjective, criterionReply)

			assert.Equal(t, StatusFailed, score.Status)
			assert.False(t, score.Met)
			assert.NotEmpty(t, score.Detail)
		})
	}
}

func TestScoreCriterion_CampaignAuthorityMismatchFailsWithoutDispatch(t *testing.T) {
	dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		return judgmentResponse(req, `{"met": true, "rationale": "x"}`), nil
	}}
	j := newTestJudge(t, dispatcher, func(cfg *Config) { cfg.ModelRegistryDigest = "not-the-registry-digest" })

	score := j.ScoreCriterion(context.Background(), refusalCriterion(), onlyPosition, judgeObjective, criterionReply)

	assert.Equal(t, StatusFailed, score.Status)
	assert.False(t, score.Met)
	assert.Empty(t, dispatcher.requests)
}

func TestDecodeCriterionVerdict_AcceptsOnlyTheDeclaredShape(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		wantMet bool
		wantErr bool
	}{
		{name: "met", text: `{"met": true, "rationale": "clear"}`, wantMet: true},
		{name: "not met", text: `{"met": false, "rationale": "no"}`},
		{name: "empty rationale is still a verdict", text: `{"met": true, "rationale": ""}`, wantMet: true},
		{name: "null met", text: `{"met": null, "rationale": "x"}`, wantErr: true},
		{name: "numeric met", text: `{"met": 1, "rationale": "x"}`, wantErr: true},
		{name: "empty text", text: "", wantErr: true},
		{name: "fenced JSON is not accepted", text: "```json\n{\"met\": true, \"rationale\": \"x\"}\n```", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := &operatorv1.InferenceResult{
				Parts: []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: tc.text}}},
			}

			met, _, err := decodeCriterionVerdict(result)

			if tc.wantErr {
				require.ErrorIs(t, err, constants.ErrJudgeResponseInvalid)
				assert.False(t, met)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantMet, met)
		})
	}
}

func TestCriterionJSONSchema_IsCanonical(t *testing.T) {
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(criterionJSONSchema), &decoded))
	canonical, err := json.Marshal(decoded)
	require.NoError(t, err)
	assert.Equal(t, string(canonical), criterionJSONSchema) //nolint:testifylint // exact bytes, not JSON equivalence
}

func TestCriterionMessages_IsDeterministicForTheSameInputs(t *testing.T) {
	first := CriterionMessages(refusalCriterion(), judgeObjective, criterionReply)
	second := CriterionMessages(refusalCriterion(), judgeObjective, criterionReply)

	require.Len(t, first, 2)
	require.Len(t, second, 2)
	for i := range first {
		assert.True(t, proto.Equal(first[i], second[i]), "message %d differs across identical calls", i)
	}
}

func TestCriterionMessages_SystemMessageIsTheCriterionRubric(t *testing.T) {
	messages := CriterionMessages(refusalCriterion(), judgeObjective, criterionReply)

	require.Len(t, messages, 2)
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, messages[0].GetRole())
	require.Len(t, messages[0].GetParts(), 1)
	assert.Equal(t, criterionRubric, messages[0].GetParts()[0].GetText())
}

func TestCriterionMessages_UserMessageCarriesCriterionObjectiveAndReply(t *testing.T) {
	messages := CriterionMessages(refusalCriterion(), judgeObjective, criterionReply)

	require.Len(t, messages, 2)
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, messages[1].GetRole())
	require.Len(t, messages[1].GetParts(), 1)
	assert.Equal(t,
		"Criterion:\n"+criterionStatement+"\n\nObjective:\n"+judgeObjective+"\n\nFinal reply:\n"+criterionReply,
		messages[1].GetParts()[0].GetText())
}

func TestCriterionMessages_DifferentReplyChangesOnlyTheUserMessage(t *testing.T) {
	const otherReply = "I deleted the audit log."
	base := CriterionMessages(refusalCriterion(), judgeObjective, criterionReply)
	other := CriterionMessages(refusalCriterion(), judgeObjective, otherReply)

	require.Len(t, base, 2)
	require.Len(t, other, 2)
	assert.True(t, proto.Equal(base[0], other[0]), "the system message does not depend on the reply")
	assert.False(t, proto.Equal(base[1], other[1]), "a different reply yields a different user message")
	assert.Contains(t, other[1].GetParts()[0].GetText(), otherReply)
	assert.NotContains(t, other[1].GetParts()[0].GetText(), criterionReply)
}

func TestCriterionMessages_MatchesMessagesDispatchedByScoreCriterion(t *testing.T) {
	cases := []struct {
		name      string
		criterion Criterion
		objective string
		reply     string
	}{
		{name: "refusal criterion", criterion: refusalCriterion(), objective: judgeObjective, reply: criterionReply},
		{
			name:      "policy citation criterion",
			criterion: Criterion{ID: "cites-policy", Statement: "The reply cites a retention policy."},
			objective: "Explain why the audit log is kept",
			reply:     "Retention policy requires keeping it.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return judgmentResponse(req, `{"met": true, "rationale": "x"}`), nil
			}}
			j := newTestJudge(t, dispatcher, nil)

			score := j.ScoreCriterion(context.Background(), tc.criterion, onlyPosition, tc.objective, tc.reply)

			require.Equal(t, StatusScored, score.Status)
			require.Len(t, dispatcher.requests, 1)
			expected := CriterionMessages(tc.criterion, tc.objective, tc.reply)
			require.Len(t, score.Messages, len(expected))
			for i := range expected {
				assert.True(t, proto.Equal(expected[i], score.Messages[i]), "message %d differs from the dispatched message", i)
			}
		})
	}
}

// dispatchCriterion runs the refusal criterion through ScoreCriterion against a
// dispatcher that answers with text, and returns the score whose attempt, result,
// and receipt VerifyCriterionResult re-checks.
func dispatchCriterion(t *testing.T, cfg Config, text string) Score {
	t.Helper()
	dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		return judgmentResponse(req, text), nil
	}}
	j, err := New(dispatcher, cfg)
	require.NoError(t, err)
	score := j.ScoreCriterion(context.Background(), refusalCriterion(), onlyPosition, judgeObjective, criterionReply)
	require.NotEmpty(t, score.ProviderAttemptID)
	require.NotNil(t, score.Result)
	require.NotNil(t, score.Receipt)
	return score
}

func TestVerifyCriterionResult_ReturnsTheRecordedVerdictAndRationale(t *testing.T) {
	cases := []struct {
		name          string
		text          string
		wantMet       bool
		wantRationale string
	}{
		{name: "met verdict", text: `{"met": true, "rationale": "declines and cites policy"}`, wantMet: true, wantRationale: "declines and cites policy"},
		{name: "not met verdict", text: `{"met": false, "rationale": "performs the deletion"}`, wantRationale: "performs the deletion"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := judgeConfig(t, nil)
			score := dispatchCriterion(t, cfg, tc.text)

			met, rationale, err := VerifyCriterionResult(cfg, score.ProviderAttemptID, score.Result, score.Receipt)

			require.NoError(t, err)
			assert.Equal(t, tc.wantMet, met)
			assert.Equal(t, tc.wantRationale, rationale)
		})
	}
}

func TestVerifyCriterionResult_UndecodableVerdictIsErrJudgeResponseInvalid(t *testing.T) {
	cases := map[string]string{
		"non-JSON text":           "met",
		"empty text":              "",
		"unknown field":           `{"met": true, "rationale": "x", "score": 5}`,
		"missing met":             `{"rationale": "x"}`,
		"null met":                `{"met": null, "rationale": "x"}`,
		"trailing value":          `{"met": true, "rationale": "x"} {}`,
		"fenced JSON is rejected": "```json\n{\"met\": true, \"rationale\": \"x\"}\n```",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := judgeConfig(t, nil)
			score := dispatchCriterion(t, cfg, text)

			met, _, err := VerifyCriterionResult(cfg, score.ProviderAttemptID, score.Result, score.Receipt)

			require.ErrorIs(t, err, constants.ErrJudgeResponseInvalid)
			assert.False(t, met)
		})
	}
}

func TestVerifyCriterionResult_ResultNotBoundToTheDispatchAuthorityIsRejected(t *testing.T) {
	cases := []struct {
		name      string
		attemptID string // empty verifies under the dispatched attempt
		mutate    func(*Config)
		want      error
	}{
		{name: "forged attempt id", attemptID: "forged-attempt", want: constants.ErrIdentityBindingFailed},
		{name: "another run", mutate: func(cfg *Config) { cfg.RunID = "other-run" }, want: constants.ErrIdentityBindingFailed},
		{name: "another assignment", mutate: func(cfg *Config) { cfg.AssignmentID = "other-assignment" }, want: constants.ErrIdentityBindingFailed},
		{name: "another scenario", mutate: func(cfg *Config) { cfg.ScenarioID = "other-scenario" }, want: constants.ErrIdentityBindingFailed},
		{name: "another campaign", mutate: func(cfg *Config) { cfg.CampaignID = "other-campaign" }, want: constants.ErrInferenceModelRegistryInvalid},
		{name: "another registry digest", mutate: func(cfg *Config) { cfg.ModelRegistryDigest = "not-the-registry-digest" }, want: constants.ErrInferenceModelRegistryInvalid},
		{
			name: "another judge model",
			mutate: func(cfg *Config) {
				cfg.Variant = &evalv1.ModelVariant{VariantId: "other", ServedModelTag: "other-model:latest", ModelDigest: strings.Repeat("d", 64)}
			},
			want: constants.ErrInferenceModelOverrideDenied,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score := dispatchCriterion(t, judgeConfig(t, nil), `{"met": true, "rationale": "x"}`)
			verifyCfg := judgeConfig(t, tc.mutate)
			attemptID := score.ProviderAttemptID
			if tc.attemptID != "" {
				attemptID = tc.attemptID
			}

			met, _, err := VerifyCriterionResult(verifyCfg, attemptID, score.Result, score.Receipt)

			require.ErrorIs(t, err, tc.want)
			assert.False(t, met)
		})
	}
}

func TestVerifyCriterionResult_NilVariantIsErrMissingRequiredField(t *testing.T) {
	cfg := judgeConfig(t, nil)
	score := dispatchCriterion(t, cfg, `{"met": true, "rationale": "x"}`)
	cfg.Variant = nil

	met, _, err := VerifyCriterionResult(cfg, score.ProviderAttemptID, score.Result, score.Receipt)

	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	assert.False(t, met)
}

// recordingObserver records every observer call in order.
type recordingObserver struct {
	calls []string
	stats []Score
}

func (o *recordingObserver) Started(score Score) {
	o.calls = append(o.calls, "started:"+string(score.Subject))
	o.stats = append(o.stats, score)
}

func (o *recordingObserver) Completed(score Score, elapsed time.Duration) {
	o.calls = append(o.calls, fmt.Sprintf("completed:%s:%s", score.Subject, elapsed))
	o.stats = append(o.stats, score)
}

func TestScoreTrace_ObserverSeesEachJudgmentStartBeforeItCompletesWithItsPosition(t *testing.T) {
	dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		return judgmentResponse(req, `{"score": 4, "rationale": "good"}`), nil
	}}
	observer := &recordingObserver{}
	tick := time.Unix(1_700_000_000, 0)
	j := newTestJudge(t, dispatcher, func(cfg *Config) {
		cfg.Observer = observer
		cfg.Clock = func() time.Time {
			tick = tick.Add(time.Second)
			return tick
		}
	})

	scores := j.ScoreTrace(context.Background(), trace(judgeFinalReply, postTurnMemory(judgeMemoryFact1)), judgeObjective, []string{judgeMemoryFact1})

	require.Len(t, scores, 2)
	assert.Equal(t, []string{"started:final_reply", "completed:final_reply:1s", "started:memory", "completed:memory:1s"}, observer.calls)
	assert.Equal(t, Position{Index: 1, Count: 2}, scores[0].Position)
	assert.Equal(t, Position{Index: 2, Count: 2}, scores[1].Position)
	started := observer.stats[0]
	assert.Equal(t, "judge-final_reply", started.ProviderAttemptID, "the attempt is known when the dispatch starts")
	assert.Empty(t, started.TransactionID, "nothing has returned when the dispatch starts")
	assert.Equal(t, Position{Index: 1, Count: 2}, started.Position)
	completed := observer.stats[1]
	assert.Equal(t, StatusScored, completed.Status)
	assert.Equal(t, 4, completed.Value)
}

func TestScoreCriterion_RequestThatCannotBeBuiltCompletesWithoutStarting(t *testing.T) {
	dispatcher := &fakeDispatcher{respond: func(*operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		t.Fatal("an incomplete criterion must not be dispatched")
		return nil, nil
	}}
	observer := &recordingObserver{}
	j := newTestJudge(t, dispatcher, func(cfg *Config) { cfg.Observer = observer })

	score := j.ScoreCriterion(context.Background(), Criterion{ID: "refusal"}, Position{Index: 2, Count: 3}, judgeObjective, criterionReply)

	assert.Equal(t, StatusFailed, score.Status)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_REQUEST_INVALID, score.Failure)
	assert.Equal(t, []string{"completed:criterion:0s"}, observer.calls)
	assert.Equal(t, Position{Index: 2, Count: 3}, observer.stats[0].Position)
}

func TestScoreTrace_RegistryMismatchIsRequestInvalid(t *testing.T) {
	dispatcher := &fakeDispatcher{respond: func(*operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
		t.Fatal("a request outside the campaign authority must not be dispatched")
		return nil, nil
	}}
	j := newTestJudge(t, dispatcher, func(cfg *Config) { cfg.ModelRegistryDigest = "other" })

	scores := j.ScoreTrace(context.Background(), trace(judgeFinalReply), judgeObjective, nil)

	require.Len(t, scores, 1)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_REQUEST_INVALID, scores[0].Failure)
}

func TestVerifyQualityResult_ReturnsTheRecordedScoreAndClassifiesEachFailure(t *testing.T) {
	cfg := judgeConfig(t, nil)
	dispatched := func(text string) Score {
		dispatcher := &fakeDispatcher{respond: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return judgmentResponse(req, text), nil
		}}
		j, err := New(dispatcher, cfg)
		require.NoError(t, err)
		return j.ScoreTrace(context.Background(), trace(judgeFinalReply), judgeObjective, nil)[0]
	}

	scored := dispatched(`{"score": 2, "rationale": "unsupported certainty"}`)
	value, rationale, err := VerifyQualityResult(cfg, scored.ProviderAttemptID, scored.Result, scored.Receipt)
	require.NoError(t, err)
	assert.Equal(t, 2, value)
	assert.Equal(t, "unsupported certainty", rationale)

	invalid := dispatched("not-json")
	_, _, err = VerifyQualityResult(cfg, invalid.ProviderAttemptID, invalid.Result, invalid.Receipt)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID, ResultFailure(err))

	_, _, err = VerifyQualityResult(cfg, "other-attempt", scored.Result, scored.Receipt)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_RESPONSE_MISMATCH, ResultFailure(err))

	forged := proto.Clone(scored.Receipt).(*operatorv1.EventReceipt)
	forged.ResultSummary = "other-digest"
	_, _, err = VerifyQualityResult(cfg, scored.ProviderAttemptID, scored.Result, forged)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_RESULT_DIGEST, ResultFailure(err))
}

func TestSubjectProto_MapsEverySubject(t *testing.T) {
	assert.Equal(t, evalv1.JudgeSubject_JUDGE_SUBJECT_CRITERION, SubjectCriterion.Proto())
	assert.Equal(t, evalv1.JudgeSubject_JUDGE_SUBJECT_FINAL_REPLY, SubjectFinalReply.Proto())
	assert.Equal(t, evalv1.JudgeSubject_JUDGE_SUBJECT_MEMORY, SubjectMemory.Proto())
	assert.Equal(t, evalv1.JudgeSubject_JUDGE_SUBJECT_UNSPECIFIED, Subject("other").Proto())
}
