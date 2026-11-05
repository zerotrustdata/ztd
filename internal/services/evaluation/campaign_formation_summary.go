// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

package evaluation

import (
	"fmt"
	"sort"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Each frozen stack is scored as a unit, independently of its designated model.
// Reuse the run collectors on its exact assignment population, never sum role
// summaries (one assignment can invoke several roles).
type formationAggregate struct {
	Stack       *evalv1.HeterogeneousStackDefinition
	Assignments []*evalv1.EvaluationAssignment
	State       *runAggregateState
}

type formationSummaryRecord struct {
	StackID             string                 `json:"stack_id"`
	StackDigest         string                 `json:"stack_digest"`
	ModelRoleMapping    map[string]string      `json:"model_role_mapping"`
	AssignmentTotal     uint32                 `json:"assignment_total"`
	TerminalOutcomes    map[string]uint32      `json:"terminal_outcomes"`
	PassRate            *modelPassRateRecord   `json:"pass_rate,omitempty"`
	SecurityPassRate    *modelPassRateRecord   `json:"security_pass_rate,omitempty"`
	LatencyP50MS        modelMetricValueRecord `json:"latency_p50_ms"`
	OutputTokens        modelMetricValueRecord `json:"output_tokens"`
	OutputThroughputP50 modelMetricValueRecord `json:"output_throughput_p50"`
	RunSeconds          *float64               `json:"run_seconds,omitempty"`
	ParameterCount      *uint64                `json:"parameter_count,omitempty"`
	ArtifactSizeBytes   *uint64                `json:"artifact_size_bytes,omitempty"`
}

func collectFormationAggregates(assignments []*evalv1.EvaluationAssignment, results map[string]*evalv1.EvaluationAssignmentResult) ([]*formationAggregate, error) {
	byID := map[string]*formationAggregate{}
	for _, assignment := range assignments {
		if !IsHeterogeneousAssignment(assignment) {
			continue
		}
		stack, err := HeterogeneousStackFromAssignment(assignment)
		if err != nil {
			return nil, err
		}
		formation := byID[stack.GetStackId()]
		if formation == nil {
			formation = &formationAggregate{Stack: stack}
			byID[stack.GetStackId()] = formation
		} else if formation.Stack.GetStackDigest() != stack.GetStackDigest() {
			return nil, fmt.Errorf("evaluation: aggregate formation %s has conflicting frozen stacks: %w", stack.GetStackId(), constants.ErrFormationStackMismatch)
		}
		formation.Assignments = append(formation.Assignments, assignment)
	}
	formations := make([]*formationAggregate, 0, len(byID))
	for _, formation := range byID {
		state, err := collectAssignmentAggregateState(formation.Assignments, results)
		if err != nil {
			return nil, err
		}
		formation.State = state
		formations = append(formations, formation)
	}
	sort.Slice(formations, func(i, j int) bool { return formations[i].Stack.GetStackId() < formations[j].Stack.GetStackId() })
	return formations, nil
}

func buildFormationSummaryRecords(state *runAggregateState) []formationSummaryRecord {
	var records []formationSummaryRecord
	for _, formation := range state.Formations {
		s := formation.State
		record := formationSummaryRecord{
			StackID: formation.Stack.GetStackId(), StackDigest: formation.Stack.GetStackDigest(),
			ModelRoleMapping: buildModelRoleMapping(s), AssignmentTotal: s.Scheduled,
			TerminalOutcomes: aggregateTerminalOutcomes(s),
			PassRate:         judgedPassRate(s.Passed, s.Judged), SecurityPassRate: judgedPassRate(s.SecurityPassed, s.SecurityJudged),
			LatencyP50MS:        modelMetricFromRunMetric(s.Headline.LatencyP50MS),
			OutputTokens:        modelMetricFromRunMetric(s.Headline.OutputTokensP50),
			OutputThroughputP50: modelMetricFromRunMetric(s.Headline.OutputThroughputP50), RunSeconds: s.RunSeconds,
		}
		// Count each installed variant once even if it occupies multiple slots.
		// An unknown member specification makes the whole total unavailable.
		seen := map[string]bool{}
		var parameters, artifacts uint64
		parametersKnown, artifactsKnown := true, true
		for _, bucket := range s.VariantRoles {
			if seen[bucket.VariantID] {
				continue
			}
			seen[bucket.VariantID] = true
			parametersKnown = parametersKnown && bucket.ParameterCount > 0
			artifactsKnown = artifactsKnown && bucket.ArtifactSizeBytes > 0
			parameters += bucket.ParameterCount
			artifacts += bucket.ArtifactSizeBytes
		}
		if parametersKnown {
			record.ParameterCount = &parameters
		}
		if artifactsKnown {
			record.ArtifactSizeBytes = &artifacts
		}
		records = append(records, record)
	}
	return records
}

func judgedPassRate(passed, judged uint32) *modelPassRateRecord {
	if judged == 0 {
		return nil
	}
	estimate := float64(passed) / float64(judged)
	return &modelPassRateRecord{Estimate: estimate, Lower: estimate, Upper: estimate, Denominator: judged}
}
