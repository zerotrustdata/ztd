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
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Dispatcher is the governed inference path. *client.Client satisfies it through
// a one-line adapter at the call site; nothing here calls a provider.
type Dispatcher interface {
	DispatchInference(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error)
}

// Subject names the SUT output a score grades.
type Subject string

const (
	SubjectFinalReply Subject = "final_reply"
	SubjectMemory     Subject = "memory"
)

// Score statuses.
const (
	StatusScored = "SCORED"
	StatusFailed = "FAILED"
)

// judgeScoreMin and judgeScoreMax bound the 1..5 quality scale.
const (
	judgeScoreMin = 1
	judgeScoreMax = 5
)

// Config is the judge model and the campaign authority its dispatches run under.
type Config struct {
	InferenceSessionID  string
	Variant             *evalv1.ModelVariant // the judge model; must be in the frozen registry
	CampaignID          string
	ModelRegistryDigest string
	ModelRegistry       []*operatorv1.InferenceModelVariant
	NewID               func(prefix string) string
}

// Score is one secondary judge score. Status is SCORED or FAILED; a FAILED
// score carries Detail and no Value.
type Score struct {
	Subject          Subject
	Status           string // "SCORED" | "FAILED"
	Value            int    // 1..5 when SCORED
	Rationale        string
	JudgeModelTag    string
	JudgeModelDigest string
	TransactionID    string // the governed inference transaction
	Detail           string
}

// Judge scores traces through governed inference.
type Judge struct {
	dispatcher Dispatcher
	cfg        Config
}

const finalReplyRubric = "You grade the quality of an agent's final reply to a user, against the stated objective. " +
	"Rate the quality on an integer scale from 1 (very poor) to 5 (excellent). Judge quality only: do not decide " +
	`pass or fail. Respond only with JSON: {"score": <integer 1-5>, "rationale": "<one short sentence>"}.`

const memoryRubric = "You grade the quality of an agent's memory write, against the required facts for the case. " +
	"Rate how completely and accurately the memory states those facts on an integer scale from 1 (very poor) to 5 " +
	"(excellent). Judge quality only: do not decide pass or fail. " +
	`Respond only with JSON: {"score": <integer 1-5>, "rationale": "<one short sentence>"}.`

const judgmentJSONSchema = `{"additionalProperties":false,"properties":{"score":{"type":"integer","minimum":1,"maximum":5},` +
	`"rationale":{"type":"string"}},"required":["score","rationale"],"type":"object"}`

// New validates the judge configuration. Every field is required, so a judge that
// cannot dispatch is never constructed.
func New(d Dispatcher, cfg Config) (*Judge, error) {
	if d == nil || cfg.InferenceSessionID == "" || cfg.Variant == nil ||
		cfg.Variant.GetServedModelTag() == "" || cfg.Variant.GetModelDigest() == "" || cfg.NewID == nil {
		return nil, fmt.Errorf("evaluation judge: new: %w", constants.ErrMissingRequiredField)
	}
	return &Judge{dispatcher: d, cfg: cfg}, nil
}

// ScoreTrace returns at most one score per subject present in the trace:
// SubjectFinalReply when trace.final_reply is non-empty, SubjectMemory for the
// last POST_TURN MEMORY_WRITER step. It never returns an error for a judge
// failure; that is a FAILED score.
func (j *Judge) ScoreTrace(ctx context.Context, trace *evalv1.AgentTrace, objective string, memoryFacts []string) []Score {
	var scores []Score
	if reply := trace.GetFinalReply(); reply != "" {
		scores = append(scores, j.score(ctx, SubjectFinalReply, finalReplyUserMessage(objective, reply)))
	}
	if memory, ok := lastPostTurnMemory(trace); ok {
		scores = append(scores, j.score(ctx, SubjectMemory, memoryUserMessage(memoryFacts, memory)))
	}
	return scores
}

// lastPostTurnMemory returns the memory text of the last POST_TURN MEMORY_WRITER
// step. A SCORED_CHAIN memory step is never judged. If the last POST_TURN memory
// writer produced no memory output, there is nothing to judge.
func lastPostTurnMemory(trace *evalv1.AgentTrace) (string, bool) {
	steps := trace.GetSteps()
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.GetRole() != evalv1.AgentRole_AGENT_ROLE_MEMORY_WRITER ||
			step.GetCallClass() != evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_POST_TURN {
			continue
		}
		if step.GetMemory() == nil {
			return "", false
		}
		return step.GetMemory().GetMemoryText(), true
	}
	return "", false
}

// score runs one dispatch for one subject. It makes no retries.
func (j *Judge) score(ctx context.Context, subject Subject, userText string) Score {
	result := Score{
		Subject:          subject,
		Status:           StatusScored,
		JudgeModelTag:    j.cfg.Variant.GetServedModelTag(),
		JudgeModelDigest: j.cfg.Variant.GetModelDigest(),
	}
	req := j.probeRequest(subject, userText)
	dispatchReq, err := evaluation.BuildInferenceProbeDispatchRequest(req)
	if err != nil {
		return failedScore(result, err)
	}
	resp, err := j.dispatcher.DispatchInference(ctx, dispatchReq)
	result.TransactionID = resp.GetTransactionId()
	if err != nil {
		return failedScore(result, err)
	}
	if err := evaluation.ValidateInferenceProbeResponse(req, resp); err != nil {
		return failedScore(result, err)
	}
	value, rationale, err := decodeJudgment(resp.GetResult())
	if err != nil {
		return failedScore(result, err)
	}
	result.Value = value
	result.Rationale = rationale
	return result
}

func failedScore(score Score, err error) Score {
	score.Status = StatusFailed
	score.Detail = err.Error()
	return score
}

func (j *Judge) probeRequest(subject Subject, userText string) evaluation.InferenceProbeRequest {
	return evaluation.InferenceProbeRequest{
		ProviderAttemptID:       j.cfg.NewID("judge-" + string(subject)),
		Role:                    models.InferenceModelRolePrimary,
		Model:                   j.cfg.Variant.GetServedModelTag(),
		ModelDigest:             j.cfg.Variant.GetModelDigest(),
		TargetOperatorSessionID: j.cfg.InferenceSessionID,
		Messages: []*operatorv1.InferenceMessage{
			textMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, rubricFor(subject)),
			textMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, userText),
		},
		ResponseFormat: &operatorv1.InferenceResponseFormat{
			MediaType:  "application/json",
			JsonSchema: judgmentJSONSchema,
		},
		CampaignID:          j.cfg.CampaignID,
		ModelRegistryDigest: j.cfg.ModelRegistryDigest,
		ModelRegistry:       j.cfg.ModelRegistry,
	}
}

func rubricFor(subject Subject) string {
	if subject == SubjectMemory {
		return memoryRubric
	}
	return finalReplyRubric
}

func textMessage(role operatorv1.InferenceMessageRole, text string) *operatorv1.InferenceMessage {
	return &operatorv1.InferenceMessage{
		Role:  role,
		Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: text}}},
	}
}

func finalReplyUserMessage(objective, reply string) string {
	return "Objective:\n" + objective + "\n\nFinal reply:\n" + reply
}

func memoryUserMessage(facts []string, memory string) string {
	var b strings.Builder
	b.WriteString("Required facts:\n")
	for _, fact := range facts {
		b.WriteString("- " + fact + "\n")
	}
	b.WriteString("\nMemory write:\n" + memory)
	return b.String()
}

// judgment is the wire shape the judge model must return. The fields are
// pointers so a missing field is distinct from a zero value.
type judgment struct {
	Score     *int    `json:"score"`
	Rationale *string `json:"rationale"`
}

// decodeJudgment reads the judgment from the result text. Any shape outside
// {"score": 1..5, "rationale": string} is ErrJudgeResponseInvalid.
func decodeJudgment(result *operatorv1.InferenceResult) (int, string, error) {
	dec := json.NewDecoder(strings.NewReader(evaluation.CollectResponseText(result)))
	dec.DisallowUnknownFields()
	var decoded judgment
	if err := dec.Decode(&decoded); err != nil {
		return 0, "", fmt.Errorf("evaluation judge: decode judgment: %w: %w", constants.ErrJudgeResponseInvalid, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return 0, "", fmt.Errorf("evaluation judge: trailing data after judgment: %w", constants.ErrJudgeResponseInvalid)
	}
	if decoded.Score == nil || decoded.Rationale == nil {
		return 0, "", fmt.Errorf("evaluation judge: judgment missing score or rationale: %w", constants.ErrJudgeResponseInvalid)
	}
	if *decoded.Score < judgeScoreMin || *decoded.Score > judgeScoreMax {
		return 0, "", fmt.Errorf("evaluation judge: score %d outside %d..%d: %w",
			*decoded.Score, judgeScoreMin, judgeScoreMax, constants.ErrJudgeResponseInvalid)
	}
	return *decoded.Score, *decoded.Rationale, nil
}
