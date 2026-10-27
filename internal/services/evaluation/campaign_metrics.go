// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	MetricRetrievalHitRate         = "retrieval_hit_rate"
	MetricRedundantCommandRate     = "redundant_command_rate"
	MetricStepsToDiagnosis         = "steps_to_diagnosis"
	MetricDiagnosisCorrectness     = "diagnosis_correctness"
	MetricEvidenceBackedConclusion = "evidence_backed_conclusion"
)

// ComputeInvestigationMetrics computes the 5 core longitudinal evaluation metrics
// for one assignment run:
//
//  1. retrieval_hit_rate: the fraction of the scenario's gold evidence references
//     the agent had in the dispatched conversation or retrieved through a
//     successful tool call.
//  2. redundant_command_rate: commands or queries whose outcome was already
//     present in retrieved history.
//  3. steps_to_diagnosis: model turns and governed actions before the terminal response.
//  4. diagnosis_correctness: deterministic content correctness (1.0 or 0.0).
//  5. evidence_backed_conclusion: claims in the diagnosis are traceable to
//     observations from this run or to retrieved receipts (1.0 or 0.0).
//
// It returns an error wrapping constants.ErrMissingRequiredField when the
// assignment ID or the trace is absent.
func ComputeInvestigationMetrics(assignmentID string, trace *AgentTrace, gold ScenarioGoldCriteria, contentPassed bool, input ScenarioInputFixture) ([]*evalv1.DecomposedScoreRecord, error) {
	if assignmentID == "" || trace == nil {
		return nil, fmt.Errorf("evaluation: compute investigation metrics: assignment and trace: %w", constants.ErrMissingRequiredField)
	}

	retrievalHitRate := computeRetrievalHitRate(trace, gold.GoldEvidenceReferences)
	redundantCommandRate := computeRedundantCommandRate(trace)
	stepsToDiagnosis := computeStepsToDiagnosis(trace)
	diagnosisCorrectness := 0.0
	if contentPassed {
		diagnosisCorrectness = 1.0
	}
	evidenceBacked := computeEvidenceBackedConclusion(trace, gold, contentPassed)

	return []*evalv1.DecomposedScoreRecord{
		{
			ScoreId:           assignmentID + ":" + MetricRetrievalHitRate,
			Dimension:         MetricRetrievalHitRate,
			Value:             retrievalHitRate,
			Unit:              evalv1.EvaluationMetricUnit_EVALUATION_METRIC_UNIT_RATIO,
			Direction:         evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_HIGHER_IS_BETTER,
			MissingDataPolicy: evalv1.EvaluationMissingDataPolicy_EVALUATION_MISSING_DATA_POLICY_FAIL,
		},
		{
			ScoreId:           assignmentID + ":" + MetricRedundantCommandRate,
			Dimension:         MetricRedundantCommandRate,
			Value:             redundantCommandRate,
			Unit:              evalv1.EvaluationMetricUnit_EVALUATION_METRIC_UNIT_RATIO,
			Direction:         evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_LOWER_IS_BETTER,
			MissingDataPolicy: evalv1.EvaluationMissingDataPolicy_EVALUATION_MISSING_DATA_POLICY_FAIL,
		},
		{
			ScoreId:           assignmentID + ":" + MetricStepsToDiagnosis,
			Dimension:         MetricStepsToDiagnosis,
			Value:             stepsToDiagnosis,
			Unit:              evalv1.EvaluationMetricUnit_EVALUATION_METRIC_UNIT_COUNT,
			Direction:         evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_LOWER_IS_BETTER,
			MissingDataPolicy: evalv1.EvaluationMissingDataPolicy_EVALUATION_MISSING_DATA_POLICY_FAIL,
		},
		{
			ScoreId:           assignmentID + ":" + MetricDiagnosisCorrectness,
			Dimension:         MetricDiagnosisCorrectness,
			Value:             diagnosisCorrectness,
			Unit:              evalv1.EvaluationMetricUnit_EVALUATION_METRIC_UNIT_RATIO,
			Direction:         evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_HIGHER_IS_BETTER,
			MissingDataPolicy: evalv1.EvaluationMissingDataPolicy_EVALUATION_MISSING_DATA_POLICY_FAIL,
		},
		{
			ScoreId:           assignmentID + ":" + MetricEvidenceBackedConclusion,
			Dimension:         MetricEvidenceBackedConclusion,
			Value:             evidenceBacked,
			Unit:              evalv1.EvaluationMetricUnit_EVALUATION_METRIC_UNIT_RATIO,
			Direction:         evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_HIGHER_IS_BETTER,
			MissingDataPolicy: evalv1.EvaluationMissingDataPolicy_EVALUATION_MISSING_DATA_POLICY_FAIL,
		},
	}, nil
}

func computeRetrievalHitRate(trace *AgentTrace, references []ScenarioGoldEvidenceReference) float64 {
	if len(references) == 0 {
		return 1.0
	}
	supplied := suppliedEvidence(trace)
	hitCount := 0
	for _, ref := range references {
		if referenceSupplied(ref, supplied) {
			hitCount++
			continue
		}
		for _, call := range trace.ToolCalls {
			if referenceObserved(ref, call) {
				hitCount++
				break
			}
		}
	}
	return float64(hitCount) / float64(len(references))
}

// suppliedEvidence returns folded text from the exact conversation dispatched
// to the model, including held conversation context.
func suppliedEvidence(trace *AgentTrace) []string {
	var corpus []string
	if trace == nil {
		return corpus
	}
	if trace.HeldContext != nil {
		for _, message := range trace.HeldContext.Messages {
			if message.Role == "INFERENCE_MESSAGE_ROLE_USER" && message.Text != "" {
				corpus = append(corpus, foldContentTerm(message.Text))
			} else if message.Role == "INFERENCE_MESSAGE_ROLE_TOOL" {
				if message.Text != "" {
					corpus = append(corpus, foldContentTerm(message.Text))
				}
				if message.ToolResult != nil && message.ToolResult.ResultJSON != "" {
					corpus = append(corpus, foldContentTerm(message.ToolResult.ResultJSON))
				}
			}
		}
	}
	if trace.Input != nil {
		for _, message := range trace.Input.Messages {
			if (message.Role == "INFERENCE_MESSAGE_ROLE_USER" || message.Role == "INFERENCE_MESSAGE_ROLE_TOOL") && message.Text != "" {
				corpus = append(corpus, foldContentTerm(message.Text))
			}
		}
	}
	return corpus
}

// referenceSupplied reports whether the scenario input already carries a
// content-only reference, so the agent had it without retrieving it. A
// reference naming an action, resource, or command needs an observation.
func referenceSupplied(ref ScenarioGoldEvidenceReference, supplied []string) bool {
	if ref.ExpectedContent == "" || ref.ActionType != "" || ref.TargetResource != "" || ref.CommandPattern != "" {
		return false
	}
	content := foldContentTerm(ref.ExpectedContent)
	for _, text := range supplied {
		if strings.Contains(text, content) {
			return true
		}
	}
	return false
}

// referenceObserved reports whether call retrieved or observed ref: through
// context retrieval receipts, or directly through the resource, tool, or
// command the reference names.
func referenceObserved(ref ScenarioGoldEvidenceReference, call AgentToolCall) bool {
	if !call.Success || call.ResultJSON == "" {
		return false
	}
	resLower := strings.ToLower(call.ResultJSON)
	command, _ := investigationCommand(call)
	cmdLower := strings.ToLower(command)

	if call.Retrieval != nil && len(call.Retrieval.Receipts) > 0 {
		refMatches := true
		if ref.ActionType != "" && !strings.Contains(resLower, strings.ToLower(ref.ActionType)) && !strings.EqualFold(call.Retrieval.Query.ActionType, ref.ActionType) {
			refMatches = false
		}
		if ref.TargetResource != "" && !strings.Contains(resLower, strings.ToLower(ref.TargetResource)) {
			refMatches = false
		}
		if ref.CommandPattern != "" && !strings.Contains(resLower, strings.ToLower(ref.CommandPattern)) {
			refMatches = false
		}
		if ref.ExpectedContent != "" && !strings.Contains(resLower, strings.ToLower(ref.ExpectedContent)) {
			refMatches = false
		}
		if refMatches {
			return true
		}
	}

	if ref.ActionType != "" && !strings.EqualFold(call.ActionType, ref.ActionType) {
		return false
	}
	if ref.TargetResource != "" {
		if ref.TargetResource != call.ToolName {
			requestPath, ok := toolRequestPath(call)
			if !ok || !resourceRequestMatches(call.ToolName, requestPath, ref.TargetResource) || !strings.Contains(resLower, strings.ToLower(filepath.ToSlash(filepath.Clean(ref.TargetResource)))) {
				return false
			}
		}
	}
	if ref.CommandPattern != "" && !strings.Contains(cmdLower, strings.ToLower(ref.CommandPattern)) {
		return false
	}
	return ref.ExpectedContent == "" || strings.Contains(resLower, strings.ToLower(ref.ExpectedContent))
}

func toolRequestPath(call AgentToolCall) (string, bool) {
	var request struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &request); err != nil || request.Path == "" {
		return "", false
	}
	return request.Path, true
}

func resourceRequestMatches(tool, requestPath, target string) bool {
	requestPath = filepath.ToSlash(filepath.Clean(requestPath))
	target = filepath.ToSlash(filepath.Clean(target))
	if requestPath == target || strings.HasSuffix(requestPath, "/"+target) {
		return true
	}
	if tool != constants.MCPToolSearchDirectory || filepath.IsAbs(target) {
		return false
	}
	if requestPath == "." || requestPath == "" {
		return true
	}
	if filepath.IsAbs(requestPath) {
		return strings.HasSuffix(requestPath, "/"+target)
	}
	relative, err := filepath.Rel(requestPath, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func computeRedundantCommandRate(trace *AgentTrace) float64 {
	var retrievedHistory []string
	totalCommands := 0
	redundantCount := 0

	for _, call := range trace.ToolCalls {
		// Populate known history from context retrieval
		if call.Retrieval != nil && len(call.Retrieval.Receipts) > 0 {
			retrievedHistory = append(retrievedHistory, strings.ToLower(call.ResultJSON))
		}

		command, isCommandOrRead := investigationCommand(call)
		if isCommandOrRead {
			totalCommands++
			argsLower := strings.ToLower(call.ArgumentsJSON)
			cmdLower := strings.ToLower(command)

			redundant := false
			for _, hist := range retrievedHistory {
				// If the command or resource was already documented in retrieved history
				if cmdLower != "" && strings.Contains(hist, cmdLower) {
					redundant = true
					break
				}
				// Check for file paths in arguments
				if argsLower != "" {
					parts := strings.FieldsFunc(argsLower, func(r rune) bool {
						return r == '"' || r == '\'' || r == ' ' || r == '{' || r == '}' || r == ':' || r == ','
					})
					for _, part := range parts {
						if (strings.Contains(part, "/") || strings.Contains(part, ".")) && len(part) > 3 {
							if strings.Contains(hist, part) {
								redundant = true
								break
							}
						}
					}
				}
				if redundant {
					break
				}
			}
			if redundant {
				redundantCount++
			}
		}
	}

	if totalCommands == 0 {
		return 0.0
	}
	return float64(redundantCount) / float64(totalCommands)
}

// investigationCommand reports whether call ran a command or read a resource,
// and returns the command line it ran: the Tribunal's approved command, or the
// full line of a native run_shell_command call.
func investigationCommand(call AgentToolCall) (string, bool) {
	if call.Command != "" {
		return call.Command, true
	}
	switch call.ToolName {
	case constants.MCPToolRunShellCommand:
		var request models.RunShellCommandRequest
		if err := json.Unmarshal([]byte(call.ArgumentsJSON), &request); err != nil {
			return "", true
		}
		return request.CommandLine(), true
	case constants.MCPToolReadFile, constants.MCPToolSearchFile, constants.MCPToolSearchDirectory:
		return "", true
	default:
		return "", false
	}
}

func computeStepsToDiagnosis(trace *AgentTrace) float64 {
	// Tribunal calls are part of their tool call, not steps of their own.
	steps := len(trace.ToolCalls)
	for _, call := range trace.ModelCalls {
		if call.IsTurn() {
			steps++
		}
	}
	if steps == 0 {
		return 1.0
	}
	return float64(steps)
}

func computeEvidenceBackedConclusion(trace *AgentTrace, gold ScenarioGoldCriteria, contentPassed bool) float64 {
	if !contentPassed {
		return 0.0
	}

	// Collect all evidence sources available to the agent
	evidenceCorpus := suppliedEvidence(trace)
	for _, call := range trace.ToolCalls {
		if call.Success && call.ResultJSON != "" {
			evidenceCorpus = append(evidenceCorpus, foldContentTerm(call.ResultJSON))
		}
	}
	for _, clarification := range trace.Clarifications {
		evidenceCorpus = append(evidenceCorpus, foldContentTerm(clarification.UserResponse))
	}

	// If the scenario has content checks with required terms, ensure that
	// the required terms appear somewhere in the evidence corpus or prompt
	if gold.ContentCheck != nil && len(gold.ContentCheck.RequiredTerms) > 0 {
		for _, termGroup := range gold.ContentCheck.RequiredTerms {
			groupFound := false
			for _, term := range termGroup {
				termFolded := foldContentTerm(term)
				for _, corpus := range evidenceCorpus {
					if strings.Contains(corpus, termFolded) {
						groupFound = true
						break
					}
				}
				if groupFound {
					break
				}
			}
			if !groupFound {
				// Claim asserted in final output was not backed by any observed or retrieved evidence
				return 0.0
			}
		}
	}

	return 1.0
}
