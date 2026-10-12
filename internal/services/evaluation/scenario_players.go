// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"github.com/zerotrustdata/ztd/internal/constants"
)

// MarshalRisk is the risk level Marshal assigns a command (agent RiskLevel).
type MarshalRisk string

const (
	MarshalRiskLow    MarshalRisk = "LOW"
	MarshalRiskMedium MarshalRisk = "MEDIUM"
	MarshalRiskHigh   MarshalRisk = "HIGH"
)

// TriageExpectation is what Triage must classify for the scenario's user
// message. A field left empty is not graded; a listed value is accepted.
type TriageExpectation struct {
	Complexity []constants.TriageComplexity `json:"complexity,omitempty"`
	Intent     []constants.TriageIntent     `json:"intent,omitempty"`
	Posture    []constants.TriagePosture    `json:"posture,omitempty"`
}

// MarshalExpectation is the risk Marshal may assign the command the Tribunal
// chose, when that command is the one the scenario anticipates. A command that
// breaks the scenario's command expectation is judged against MEDIUM or HIGH
// instead: a Marshal that waves a dangerous command through is wrong whatever
// the scenario says about the intended one.
type MarshalExpectation struct {
	Risk []MarshalRisk `json:"risk"`
}

// ScenarioPlayerExpectations is the per-player gold for a scenario that runs
// the real chain. A nil expectation leaves that player ungraded for the
// scenario; a player the chain never reached (no host command reached the
// Tribunal, say) is not graded either, and the persona's own grade says why.
type ScenarioPlayerExpectations struct {
	// Triage is what Triage must classify.
	Triage *TriageExpectation `json:"triage,omitempty"`
	// Command is the contract the Tribunal's commands must meet: required and
	// forbidden terms, matched case-insensitively against each command. It
	// applies to the four honest seats, the vote's winner, and the command the
	// Auditor lets through. Nemesis is held only to its forbidden terms.
	Command *ScenarioContentCheck `json:"command,omitempty"`
	// Marshal is the risk band for an acceptable command; see MarshalExpectation.
	Marshal *MarshalExpectation `json:"marshal,omitempty"`
	// Codex is the contract Codex's memory text must meet, in addition to the
	// built-in redaction check (no IP addresses or credentials).
	Codex *ScenarioContentCheck `json:"codex,omitempty"`
}
