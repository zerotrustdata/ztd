// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
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

// TriageResult is the typed JSON contract emitted by the Triage persona. The
// trace stores the three classification labels in RouterOutput; confidence and
// summary remain part of the signed final text and are validated on ingest.
type TriageResult struct {
	Complexity           constants.TriageComplexity `json:"complexity"`
	ComplexityConfidence string                     `json:"complexity_confidence"`
	Intent               constants.TriageIntent     `json:"intent"`
	IntentConfidence     string                     `json:"intent_confidence"`
	IntentSummary        string                     `json:"intent_summary"`
	RequestPosture       constants.TriagePosture    `json:"request_posture"`
	PostureConfidence    string                     `json:"posture_confidence"`
}

// triageResultJSONSchema is the TriageResult contract as the canonical JSON
// Schema (sorted keys, no whitespace) the Inference Operator requires. A Lite
// cell dispatches its one Triage turn with it, so the provider constrains the
// answer to the typed object and the cell measures the classification rather
// than whether prose leaked around it.
const triageResultJSONSchema = `{"additionalProperties":false,"properties":{` +
	`"complexity":{"enum":["simple","complex"],"type":"string"},` +
	`"complexity_confidence":{"enum":["high","low"],"type":"string"},` +
	`"intent":{"enum":["information","action","unknown"],"type":"string"},` +
	`"intent_confidence":{"enum":["high","low"],"type":"string"},` +
	`"intent_summary":{"minLength":1,"type":"string"},` +
	`"posture_confidence":{"enum":["high","low"],"type":"string"},` +
	`"request_posture":{"enum":["normal","escalated","adversarial","confused"],"type":"string"}},` +
	`"required":["complexity","complexity_confidence","intent","intent_confidence","intent_summary","request_posture","posture_confidence"],` +
	`"type":"object"}`

// triageResultResponseFormat is the typed response format of a Triage turn.
func triageResultResponseFormat() *operatorv1.InferenceResponseFormat {
	return &operatorv1.InferenceResponseFormat{MediaType: "application/json", JsonSchema: triageResultJSONSchema}
}

func singleTriageResultCall(trace *AgentTrace) bool {
	if trace == nil || len(trace.ModelCalls) != 1 || len(trace.ToolCalls) != 0 || len(trace.Clarifications) != 0 || trace.ToolTurnLimitReached {
		return false
	}
	call := trace.ModelCalls[0]
	if !call.Completed() || !call.IsTurn() || call.LoopTurn != 1 || call.ToolCallCount != 0 || len(call.ToolsDeclared) != 0 ||
		trace.FinalOutput != call.Text {
		return false
	}
	_, signalled := ParseClarificationSignal(call.Text)
	return !signalled
}

func parseTriageResult(output string) (TriageResult, error) {
	var result TriageResult
	if err := validateUniqueJSONObjectFields(output); err != nil {
		return TriageResult{}, fmt.Errorf("decode TriageResult: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return TriageResult{}, fmt.Errorf("decode TriageResult: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return TriageResult{}, fmt.Errorf("decode TriageResult: trailing JSON value")
		}
		return TriageResult{}, fmt.Errorf("decode TriageResult: trailing content: %w", err)
	}
	if !triageComplexityValid(result.Complexity) || !triageIntentValid(result.Intent) || !triagePostureValid(result.RequestPosture) ||
		!triageConfidenceValid(result.ComplexityConfidence) || !triageConfidenceValid(result.IntentConfidence) || !triageConfidenceValid(result.PostureConfidence) ||
		strings.TrimSpace(result.IntentSummary) == "" {
		return TriageResult{}, fmt.Errorf("decode TriageResult: required fields or enum values are invalid")
	}
	return result, nil
}

func validateUniqueJSONObjectFields(output string) error {
	decoder := json.NewDecoder(bytes.NewBufferString(output))
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := opening.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("expected a JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("object key is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate object field")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok = closing.(json.Delim)
	if !ok || delim != '}' {
		return fmt.Errorf("JSON object is not closed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing content: %w", err)
	}
	return nil
}

func (r TriageResult) routerOutput() *evalv1.RouterOutput {
	return &evalv1.RouterOutput{Complexity: string(r.Complexity), Intent: string(r.Intent), Posture: string(r.RequestPosture)}
}

func triageComplexityValid(value constants.TriageComplexity) bool {
	return value == constants.TriageComplexitySimple || value == constants.TriageComplexityComplex
}

func triageIntentValid(value constants.TriageIntent) bool {
	return value == constants.TriageIntentInformation || value == constants.TriageIntentAction || value == constants.TriageIntentUnknown
}

func triagePostureValid(value constants.TriagePosture) bool {
	return value == constants.TriagePostureNormal || value == constants.TriagePostureEscalated || value == constants.TriagePostureAdversarial || value == constants.TriagePostureConfused
}

func triageConfidenceValid(value string) bool {
	return value == "high" || value == "low"
}

func triageExpectation(posture constants.TriagePosture) *ScenarioPlayerExpectations {
	return &ScenarioPlayerExpectations{Triage: &TriageExpectation{
		Complexity: []constants.TriageComplexity{constants.TriageComplexityComplex},
		Intent:     []constants.TriageIntent{constants.TriageIntentAction},
		Posture:    []constants.TriagePosture{posture},
	}}
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
