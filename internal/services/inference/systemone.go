// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jsonschema"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const maxDecisionBodyBytes = 64 << 10

type systemOneQuestion struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}
type systemOneRequest struct {
	Model     string                       `json:"model"`
	State     string                       `json:"state"`
	Questions map[string]systemOneQuestion `json:"questions"`
	KeepAlive json.RawMessage              `json:"keep_alive,omitempty"`
}
type systemOneAnswer struct {
	Type          string              `json:"type"`
	Noul          *float64            `json:"noul,omitempty"`
	Choice        *string             `json:"choice,omitempty"`
	Score         *float64            `json:"score,omitempty"`
	Probabilities map[string]*float64 `json:"probabilities,omitempty"`
	Confidence    *float64            `json:"confidence,omitempty"`
}
type systemOneUsage struct {
	InputTokens  *int32 `json:"input_tokens"`
	OutputTokens *int32 `json:"output_tokens"`
}
type systemOneResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]systemOneAnswer `json:"answers"`
	Usage   *systemOneUsage            `json:"usage"`
}

func decisionBody(req models.DecisionRequest) ([]byte, error) {
	if err := models.ValidateDecisionRequest(req.Input); err != nil {
		return nil, err
	}
	if req.Model == "" {
		return nil, constants.ErrInferenceModelRefInvalid
	}
	keepAlive, err := encodeOllamaKeepAlive(req.KeepAlive)
	if err != nil {
		return nil, err
	}
	wire := systemOneRequest{Model: req.Model, State: req.Input.State, Questions: make(map[string]systemOneQuestion), KeepAlive: keepAlive}
	for _, q := range req.Input.Questions {
		question := systemOneQuestion{Instructions: q.Instructions}
		switch q.Kind.(type) {
		case *operatorv1.InferenceDecisionQuestion_Noul:
			question.Type = "noul"
			if n := q.GetNoul(); n.TrueLabel != "" || n.FalseLabel != "" {
				question.Criteria, err = json.Marshal(struct {
					True  string `json:"true"`
					False string `json:"false"`
				}{n.TrueLabel, n.FalseLabel})
			}
		case *operatorv1.InferenceDecisionQuestion_Choice:
			question.Type = "choice"
			criteria := make(map[string]string)
			for _, c := range q.GetChoice().Criteria {
				criteria[c.Label] = c.Description
			}
			question.Criteria, err = json.Marshal(criteria)
		case *operatorv1.InferenceDecisionQuestion_Score:
			question.Type = "score"
			question.Criteria, err = json.Marshal(q.GetScore().Criteria)
		}
		if err != nil {
			return nil, fmt.Errorf("decision criteria: %w", err)
		}
		wire.Questions[q.Name] = question
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("decision request: %w", err)
	}
	if len(body) > maxDecisionBodyBytes {
		return nil, constants.ErrInferenceRequestTooLarge
	}
	return body, nil
}

// ValidateDecisionInference enforces exclusive input kinds at both admission and execution.
// Chat's established repeated fields remain wire-compatible; decision requests reject all chat controls.
func ValidateDecisionInference(req *operatorv1.InferenceRequested) error {
	if req.GetDecision() == nil {
		return nil
	}
	if len(req.Messages) != 0 || len(req.Tools) != 0 || req.Stream || req.Temperature != 0 || req.MaxTokens != 0 || req.TopP != nil || req.TopK != nil || req.Seed != nil || len(req.StopSequences) != 0 || req.ResponseFormat != nil || req.ToolChoice != nil || req.ParallelToolCalls != nil || req.Thinking != nil || req.ContextLimit != nil {
		return constants.ErrInferenceDecisionRequestInvalid
	}
	if req.RequestSchemaVersion != constants.InferenceRequestSchemaVersion {
		return constants.ErrInferenceDecisionRequestInvalid
	}
	_, err := decisionBody(models.DecisionRequest{Input: req.Decision, Model: req.Model, KeepAlive: req.KeepAlive})
	return err
}

// Decide calls the local System One endpoint, verifying the frozen model digest before egress.
func (b *OllamaBackend) Decide(ctx context.Context, req models.DecisionRequest) (*models.DecisionResponse, error) {
	if req.AllowHosted || req.Input.GetAllowHosted() {
		return nil, constants.ErrInferenceHostedNotAllowed
	}
	body, err := decisionBody(req)
	if err != nil {
		return nil, err
	}
	if req.ModelDigest != "" {
		digest, err := b.modelDigest(ctx, req.Model)
		if err != nil {
			return nil, err
		}
		if digest != req.ModelDigest {
			return nil, constants.ErrInferenceModelDigestMismatch
		}
	}
	return callSystemOne(ctx, b.client, b.apiURL("v1", "systemone"), "", body, req, b.maxResponseBytes)
}

func callSystemOne(ctx context.Context, client *http.Client, endpoint, key string, body []byte, req models.DecisionRequest, limit int64) (*models.DecisionResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("decision request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, transportError(ctx, "systemone", "decision", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBodyBytes))
		switch response.StatusCode {
		case http.StatusNotFound:
			return nil, constants.ErrInferenceModelNotFound
		case http.StatusRequestEntityTooLarge:
			return nil, constants.ErrInferenceRequestTooLarge
		case http.StatusBadRequest:
			return nil, constants.ErrInferenceDecisionRequestInvalid
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return nil, constants.ErrInferenceBackendTimeout
		default:
			return nil, constants.ErrInferenceBackendUnavailable
		}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, transportError(ctx, "systemone", "decision", err)
	}
	if int64(len(raw)) > limit {
		return nil, constants.ErrInferenceDecisionResponseInvalid
	}
	if store := attemptStoreFromContext(ctx); store != nil {
		if err := store.RecordRawResponse(context.WithoutCancel(ctx), req.ProviderAttemptID, raw); err != nil {
			return nil, fmt.Errorf("decision raw response: %w", err)
		}
	}
	acceptAll := true
	if failures := jsonschema.NewValidator().Validate(&jsonschema.Schema{Boolean: &acceptAll}, raw); len(failures) != 0 {
		return nil, constants.ErrInferenceDecisionResponseInvalid
	}
	var wire systemOneResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, constants.ErrInferenceDecisionResponseInvalid
	}
	if wire.Model != req.Model || wire.Usage == nil || wire.Usage.InputTokens == nil || wire.Usage.OutputTokens == nil || *wire.Usage.InputTokens < 0 || *wire.Usage.OutputTokens < 0 || int64(*wire.Usage.InputTokens)+int64(*wire.Usage.OutputTokens) > 2147483647 || len(wire.Answers) != len(req.Input.Questions) {
		return nil, constants.ErrInferenceDecisionResponseInvalid
	}
	result := &operatorv1.InferenceDecisionResult{}
	for _, q := range req.Input.Questions {
		v, ok := wire.Answers[q.Name]
		if !ok {
			return nil, constants.ErrInferenceDecisionResponseInvalid
		}
		a := &operatorv1.InferenceDecisionAnswer{Name: q.Name}
		probabilities := make([]*operatorv1.InferenceDecisionProbability, 0, len(v.Probabilities))
		switch q.Kind.(type) {
		case *operatorv1.InferenceDecisionQuestion_Noul:
			if v.Type != "noul" || v.Noul == nil || v.Choice != nil || v.Score != nil {
				return nil, constants.ErrInferenceDecisionResponseInvalid
			}
			a.Kind = &operatorv1.InferenceDecisionAnswer_Noul{Noul: *v.Noul}
		case *operatorv1.InferenceDecisionQuestion_Choice:
			if v.Type != "choice" || v.Choice == nil || v.Confidence == nil || v.Noul != nil || v.Score != nil || len(v.Probabilities) != len(q.GetChoice().Criteria) {
				return nil, constants.ErrInferenceDecisionResponseInvalid
			}
			for _, c := range q.GetChoice().Criteria {
				p, ok := v.Probabilities[c.Label]
				if !ok || p == nil {
					return nil, constants.ErrInferenceDecisionResponseInvalid
				}
				probabilities = append(probabilities, &operatorv1.InferenceDecisionProbability{Label: c.Label, Probability: *p})
			}
			a.Kind = &operatorv1.InferenceDecisionAnswer_Choice{Choice: &operatorv1.InferenceChoiceAnswer{Selection: *v.Choice, Probabilities: probabilities, Confidence: *v.Confidence}}
		case *operatorv1.InferenceDecisionQuestion_Score:
			if v.Type != "score" || v.Score == nil || v.Confidence == nil || v.Noul != nil || v.Choice != nil || len(v.Probabilities) != len(q.GetScore().Criteria) {
				return nil, constants.ErrInferenceDecisionResponseInvalid
			}
			for i := range q.GetScore().Criteria {
				label := fmt.Sprint(i)
				p, ok := v.Probabilities[label]
				if !ok || p == nil {
					return nil, constants.ErrInferenceDecisionResponseInvalid
				}
				probabilities = append(probabilities, &operatorv1.InferenceDecisionProbability{Label: label, Probability: *p})
			}
			a.Kind = &operatorv1.InferenceDecisionAnswer_Score{Score: &operatorv1.InferenceScoreAnswer{Score: *v.Score, Probabilities: probabilities, Confidence: *v.Confidence}}
		}
		result.Answers = append(result.Answers, a)
	}
	if err := models.ValidateDecisionResult(req.Input, result); err != nil {
		return nil, err
	}
	hash, err := models.ComputeDecisionOutputHash(result)
	if err != nil {
		return nil, err
	}
	return &models.DecisionResponse{Decision: result, Model: wire.Model, PromptTokens: *wire.Usage.InputTokens, CompletionTokens: *wire.Usage.OutputTokens, TotalTokens: *wire.Usage.InputTokens + *wire.Usage.OutputTokens, UsageReported: true, FinishReason: "stop", NormalizedRequestHash: models.SHA256Hex(body), OutputHash: hash, ServedModelDigest: req.ModelDigest}, nil
}

// ValidateDecisionDispatchRequest rejects mixed input before an HTTP stream is opened.
func ValidateDecisionDispatchRequest(req *operatorv1.InferenceDispatchRequest) error {
	if req.GetDecision() == nil {
		return nil
	}
	return ValidateDecisionInference(&operatorv1.InferenceRequested{
		Model: req.GetModel(), Decision: req.GetDecision(), Messages: req.GetMessages(), Tools: req.GetTools(), Stream: req.GetStream(),
		Temperature: req.GetTemperature(), MaxTokens: req.GetMaxTokens(), TopP: req.TopP, TopK: req.TopK, Seed: req.Seed, StopSequences: req.GetStopSequences(), ResponseFormat: req.GetResponseFormat(), ToolChoice: req.GetToolChoice(), ParallelToolCalls: req.ParallelToolCalls, Thinking: req.GetThinking(), ContextLimit: req.ContextLimit, RequestSchemaVersion: req.GetRequestSchemaVersion(), KeepAlive: req.GetKeepAlive(),
	})
}
