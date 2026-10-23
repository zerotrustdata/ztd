// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"fmt"
	"math"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

// DecisionRequest carries decision input and the same provider identity as chat.
type DecisionRequest struct {
	Input             *operatorv1.InferenceDecisionRequest
	Model             string
	ModelDigest       string
	KeepAlive         string
	ProviderAttemptID string
	AllowHosted       bool
}

// DecisionResponse retains the common signed inference evidence and typed answers.
type DecisionResponse = GenerateResponse

func cloneDecisionRequest(req *operatorv1.InferenceDecisionRequest) *operatorv1.InferenceDecisionRequest {
	if req == nil {
		return nil
	}
	return proto.Clone(req).(*operatorv1.InferenceDecisionRequest)
}

// ValidateDecisionRequest rejects ambiguous or unbounded decision schemas.
func ValidateDecisionRequest(req *operatorv1.InferenceDecisionRequest) error {
	invalid := constants.ErrInferenceDecisionRequestInvalid
	if req == nil || strings.TrimSpace(req.GetState()) == "" || len(req.GetQuestions()) < 1 || len(req.GetQuestions()) > 64 {
		return invalid
	}
	names := make(map[string]bool)
	for _, q := range req.GetQuestions() {
		if q == nil || q.Name == "" || len(q.Name) > 100 || strings.TrimSpace(q.Instructions) == "" || names[q.Name] {
			return invalid
		}
		for _, c := range q.Name {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
				return invalid
			}
		}
		names[q.Name] = true
		switch kind := q.Kind.(type) {
		case *operatorv1.InferenceDecisionQuestion_Noul:
			if kind.Noul == nil {
				return invalid
			}
		case *operatorv1.InferenceDecisionQuestion_Choice:
			if len(kind.Choice.GetCriteria()) < 2 || len(kind.Choice.GetCriteria()) > 26 {
				return invalid
			}
			labels := make(map[string]bool)
			for _, c := range kind.Choice.GetCriteria() {
				if c.GetLabel() == "" || labels[c.GetLabel()] {
					return invalid
				}
				labels[c.GetLabel()] = true
			}
		case *operatorv1.InferenceDecisionQuestion_Score:
			if len(kind.Score.GetCriteria()) < 2 || len(kind.Score.GetCriteria()) > 26 {
				return invalid
			}
			for _, c := range kind.Score.GetCriteria() {
				if strings.TrimSpace(c) == "" {
					return invalid
				}
			}
		default:
			return invalid
		}
	}
	return nil
}

func probability(p float64) bool { return !math.IsNaN(p) && !math.IsInf(p, 0) && p >= 0 && p <= 1 }

// ValidateDecisionResult requires exactly one matching typed answer for every question.
func ValidateDecisionResult(req *operatorv1.InferenceDecisionRequest, result *operatorv1.InferenceDecisionResult) error {
	if err := ValidateDecisionRequest(req); err != nil {
		return err
	}
	invalid := constants.ErrInferenceDecisionResponseInvalid
	if result == nil || len(result.Answers) != len(req.Questions) {
		return invalid
	}
	answers := make(map[string]*operatorv1.InferenceDecisionAnswer)
	for _, a := range result.Answers {
		if a == nil || answers[a.Name] != nil {
			return invalid
		}
		answers[a.Name] = a
	}
	for _, q := range req.Questions {
		a := answers[q.Name]
		if a == nil {
			return invalid
		}
		switch q.Kind.(type) {
		case *operatorv1.InferenceDecisionQuestion_Noul:
			v, ok := a.Kind.(*operatorv1.InferenceDecisionAnswer_Noul)
			if !ok || !probability(v.Noul) {
				return invalid
			}
		case *operatorv1.InferenceDecisionQuestion_Choice:
			v, ok := a.Kind.(*operatorv1.InferenceDecisionAnswer_Choice)
			if !ok || v.Choice == nil || !probability(v.Choice.Confidence) {
				return invalid
			}
			labels := make([]string, 0)
			selected := false
			for _, c := range q.GetChoice().Criteria {
				labels = append(labels, c.Label)
				selected = selected || c.Label == v.Choice.Selection
			}
			if !selected || !validDistribution(labels, v.Choice.Probabilities) {
				return invalid
			}
		case *operatorv1.InferenceDecisionQuestion_Score:
			v, ok := a.Kind.(*operatorv1.InferenceDecisionAnswer_Score)
			if !ok || v.Score == nil || !probability(v.Score.Confidence) {
				return invalid
			}
			labels := make([]string, len(q.GetScore().Criteria))
			for i := range labels {
				labels[i] = fmt.Sprint(i)
			}
			if !validDistribution(labels, v.Score.Probabilities) || math.IsNaN(v.Score.Score) || math.IsInf(v.Score.Score, 0) || v.Score.Score < 0 || v.Score.Score > float64(len(labels)-1) {
				return invalid
			}
		}
	}
	return nil
}
func validDistribution(labels []string, values []*operatorv1.InferenceDecisionProbability) bool {
	if len(labels) != len(values) {
		return false
	}
	seen := make(map[string]bool)
	sum := 0.0
	for _, v := range values {
		if v == nil || seen[v.Label] || !probability(v.Probability) {
			return false
		}
		seen[v.Label] = true
		sum += v.Probability
	}
	for _, label := range labels {
		if !seen[label] {
			return false
		}
	}
	return math.Abs(sum-1) <= 0.0026 // provider probabilities may be rounded to four places
}

// ComputeDecisionOutputHash binds the ordered typed answers with deterministic protobuf.
func ComputeDecisionOutputHash(result *operatorv1.InferenceDecisionResult) (string, error) {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("decision output hash: %w", err)
	}
	return SHA256Hex(data), nil
}

// ValidateDecisionCompletion checks the decision branch and its signed egress declaration.
func ValidateDecisionCompletion(input *operatorv1.InferenceDecisionRequest, result *operatorv1.InferenceResult) error {
	if input == nil {
		if result.GetDecision() != nil || result.GetHostedEgress() != nil {
			return constants.ErrInferenceDecisionResponseInvalid
		}
		return nil
	}
	if len(result.GetParts()) != 0 {
		return constants.ErrInferenceDecisionResponseInvalid
	}
	if err := ValidateDecisionResult(input, result.GetDecision()); err != nil {
		return err
	}
	hash, err := ComputeDecisionOutputHash(result.GetDecision())
	if err != nil {
		return err
	}
	if hash != result.GetOutputHash() {
		return constants.ErrInferenceEvidenceHashInvalid
	}
	e := result.GetHostedEgress()
	if input.GetAllowHosted() {
		if e == nil || !e.GetExplicitConsent() || e.GetProviderOrigin() != "https://api.typesafe.ai" || e.GetProvenance() != "hosted-unpinned" || e.GetModelVersion() != result.GetModel() || !IsPinnedJevModel(e.GetModelVersion()) || result.GetServedModelDigest() != "" || result.GetRequestedModelDigest() != "" || e.GetNormalizedRequestHash() != result.GetNormalizedRequestHash() {
			return constants.ErrInferenceDecisionResponseInvalid
		}
	} else if e != nil {
		return constants.ErrInferenceDecisionResponseInvalid
	}
	return nil
}
