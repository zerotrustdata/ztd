// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
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
//     that the agent retrieved through context tools or direct tool observations.
//  2. redundant_command_rate: commands or queries whose outcome was already
//     present in retrieved history.
//  3. steps_to_diagnosis: model turns and governed actions before the terminal response.
//  4. diagnosis_correctness: deterministic content correctness (1.0 or 0.0).
//  5. evidence_backed_conclusion: claims in the diagnosis are traceable to
//     observations from this run or to retrieved event receipts (1.0 or 0.0).
func ComputeInvestigationMetrics(assignmentID string, trace *AgentTrace, gold ScenarioGoldCriteria, contentPassed bool, input ScenarioInputFixture) []*evalv1.DecomposedScoreRecord {
	if assignmentID == "" || trace == nil {
		return nil
	}

	retrievalHitRate := computeRetrievalHitRate(trace, gold.GoldEvidenceReferences)
	redundantCommandRate := computeRedundantCommandRate(trace)
	stepsToDiagnosis := computeStepsToDiagnosis(trace)
	diagnosisCorrectness := 0.0
	if contentPassed {
		diagnosisCorrectness = 1.0
	}
	evidenceBacked := computeEvidenceBackedConclusion(trace, gold, contentPassed, input)

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
	}
}

func computeRetrievalHitRate(trace *AgentTrace, references []ScenarioGoldEvidenceReference) float64 {
	if len(references) == 0 {
		return 1.0
	}
	hitCount := 0
	for _, ref := range references {
		matched := false
		for _, call := range trace.ToolCalls {
			resLower := strings.ToLower(call.ResultJSON)
			argsLower := strings.ToLower(call.ArgumentsJSON)
			cmdLower := strings.ToLower(call.Command)

			// 1. Check if retrieved via context retrieval
			if call.Retrieval != nil {
				if len(call.Retrieval.EventReceipts) > 0 {
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
						matched = true
					}
				}
			}

			// 2. Check if observed in direct tool call
			if !matched {
				if ref.TargetResource != "" && strings.Contains(argsLower, strings.ToLower(ref.TargetResource)) {
					if ref.ExpectedContent == "" || strings.Contains(resLower, strings.ToLower(ref.ExpectedContent)) {
						matched = true
					}
				} else if ref.CommandPattern != "" && (strings.Contains(cmdLower, strings.ToLower(ref.CommandPattern)) || strings.Contains(argsLower, strings.ToLower(ref.CommandPattern))) {
					if ref.ExpectedContent == "" || strings.Contains(resLower, strings.ToLower(ref.ExpectedContent)) {
						matched = true
					}
				} else if ref.ExpectedContent != "" && (strings.Contains(resLower, strings.ToLower(ref.ExpectedContent)) || strings.Contains(argsLower, strings.ToLower(ref.ExpectedContent))) {
					matched = true
				}
			}

			if matched {
				break
			}
		}
		if matched {
			hitCount++
		}
	}
	return float64(hitCount) / float64(len(references))
}

func computeRedundantCommandRate(trace *AgentTrace) float64 {
	var retrievedHistory []string
	totalCommands := 0
	redundantCount := 0

	for _, call := range trace.ToolCalls {
		// Populate known history from context retrieval
		if call.Retrieval != nil && len(call.Retrieval.EventReceipts) > 0 {
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

func computeEvidenceBackedConclusion(trace *AgentTrace, gold ScenarioGoldCriteria, contentPassed bool, input ScenarioInputFixture) float64 {
	if !contentPassed {
		return 0.0
	}

	// Collect all evidence sources available to the agent
	var evidenceCorpus []string
	for _, inline := range input.InlineContext {
		evidenceCorpus = append(evidenceCorpus, foldContentTerm(inline.Content))
	}
	for _, call := range trace.ToolCalls {
		evidenceCorpus = append(evidenceCorpus, foldContentTerm(call.ResultJSON))
		evidenceCorpus = append(evidenceCorpus, foldContentTerm(call.ArgumentsJSON))
	}
	for _, clarification := range trace.Clarifications {
		evidenceCorpus = append(evidenceCorpus, foldContentTerm(clarification.UserResponse))
	}
	if input.Seed.CaseMemory != nil {
		evidenceCorpus = append(evidenceCorpus, foldContentTerm(input.Seed.CaseMemory.InvestigationSummary))
	}
	for _, turn := range input.Seed.Turns {
		evidenceCorpus = append(evidenceCorpus, foldContentTerm(turn.Content))
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
