// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package persona

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// RouterLabels are the expected Router outputs for a scenario.
type RouterLabels struct {
	Complexity string `json:"complexity"`
	Intent     string `json:"intent"`
	Posture    string `json:"posture"`
	// Alternatives are explicit allowed labels, including the primary label.
	// An absent list retains the single-label comparison used by older gold.
	ComplexityAlternatives []string `json:"complexity_alternatives,omitempty"`
	IntentAlternatives     []string `json:"intent_alternatives,omitempty"`
	PostureAlternatives    []string `json:"posture_alternatives,omitempty"`
}

// RiskLabel is the expected risk level of one labelled subject.
type RiskLabel struct {
	SubjectKind string `json:"subject_kind"` // command | file | error
	Subject     string `json:"subject"`
	Risk        string `json:"risk"` // LOW | MEDIUM | HIGH
}

// ReviewLabel is the expected Reviewer verdict on the labelled planted-bad candidate.
type ReviewLabel struct {
	PlantedCommand string `json:"planted_command"`
	Verdict        string `json:"verdict"` // APPROVE | REVISE | SWAP
}

// Labels are the scenario facts the deterministic graders compare against. A role with no label is graded UNLABELED.
type Labels struct {
	Router        *RouterLabels `json:"router,omitempty"`
	Risks         []RiskLabel   `json:"risks,omitempty"`
	Review        *ReviewLabel  `json:"review,omitempty"`
	MemoryFacts   []string      `json:"memory_facts,omitempty"`
	TitleConcepts []string      `json:"title_concepts,omitempty"`
}

var (
	subjectKinds = map[string]bool{"command": true, "file": true, "error": true}
	riskLabels   = map[string]evalv1.AgentRiskLevel{
		"low":    evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_LOW,
		"medium": evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_MEDIUM,
		"high":   evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_HIGH,
	}
	verdictLabels = map[string]evalv1.AgentReviewVerdict{
		"approve": evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE,
		"revise":  evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_REVISE,
		"swap":    evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_SWAP,
	}
)

// Validate rejects unknown enum strings and empty required fields with ErrInvestigationScenarioInvalid (wrapped).
func (l Labels) Validate() error {
	if r := l.Router; r != nil {
		if norm(r.Complexity) == "" || norm(r.Intent) == "" || norm(r.Posture) == "" {
			return fmt.Errorf("router labels: complexity, intent and posture are required: %w",
				constants.ErrInvestigationScenarioInvalid)
		}
		for _, alternatives := range [][]string{r.ComplexityAlternatives, r.IntentAlternatives, r.PostureAlternatives} {
			for _, value := range alternatives {
				if norm(value) == "" {
					return fmt.Errorf("router labels: empty alternative: %w", constants.ErrInvestigationScenarioInvalid)
				}
			}
		}
	}
	for _, risk := range l.Risks {
		if !subjectKinds[norm(risk.SubjectKind)] {
			return fmt.Errorf("risk label: unknown subject kind %q: %w", risk.SubjectKind,
				constants.ErrInvestigationScenarioInvalid)
		}
		if norm(risk.Subject) == "" {
			return fmt.Errorf("risk label: subject is required: %w", constants.ErrInvestigationScenarioInvalid)
		}
		if _, ok := riskLabels[norm(risk.Risk)]; !ok {
			return fmt.Errorf("risk label for %q: unknown risk %q: %w", risk.Subject, risk.Risk,
				constants.ErrInvestigationScenarioInvalid)
		}
	}
	if r := l.Review; r != nil {
		if norm(r.PlantedCommand) == "" {
			return fmt.Errorf("review label: planted command is required: %w", constants.ErrInvestigationScenarioInvalid)
		}
		if _, ok := verdictLabels[norm(r.Verdict)]; !ok {
			return fmt.Errorf("review label: unknown verdict %q: %w", r.Verdict, constants.ErrInvestigationScenarioInvalid)
		}
	}
	for _, fact := range l.MemoryFacts {
		if norm(fact) == "" {
			return fmt.Errorf("memory fact: empty fact: %w", constants.ErrInvestigationScenarioInvalid)
		}
	}
	for _, concept := range l.TitleConcepts {
		if norm(concept) == "" {
			return fmt.Errorf("title concept: empty concept: %w", constants.ErrInvestigationScenarioInvalid)
		}
	}
	return nil
}

// norm is the comparison form for every label string: trimmed and lower-cased.
func norm(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
