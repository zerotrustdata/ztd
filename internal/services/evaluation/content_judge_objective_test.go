// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// typedObjectiveRequest is a scenario whose seed shows one platform record and
// keeps a second call chain-only, with an inline ticket supplied as evidence.
func typedObjectiveRequest() AssignmentExecutionRequest {
	return AssignmentExecutionRequest{
		ScenarioInput: ScenarioInputFixture{
			UserPrompt:    "Explain the denial.",
			InlineContext: []ScenarioInlineContent{{Kind: "imported-ticket", Label: "change-ticket", Content: "APPROVED"}},
			Seed: InvestigationSeed{
				CaseTitle: "c",
				Turns:     []InvestigationSeedTurn{{Sender: "user", Content: "Delete everything"}},
				ToolCalls: []ScenarioSeedToolCall{
					{ToolName: "run_shell_command", Arguments: json.RawMessage(`{"command":"rm"}`), Outcome: scenarioSeedError, Disclosure: ScenarioSeedDisclosurePlatformRecord},
					{ToolName: "run_shell_command", Arguments: json.RawMessage(`{"command":"y"}`), Outcome: scenarioSeedError, Disclosure: ScenarioSeedDisclosureChainOnly},
				},
			},
		},
	}
}

// typedObjectiveTrace is the sealed trace of the scenario above: both seed
// observations, then one call the agent made itself. The zero workspace renders
// the prompt unchanged because the prompt holds no workspace token.
func typedObjectiveTrace() *AgentTrace {
	return &AgentTrace{
		FinalOutput: "The denial stands.",
		Observations: []AgentScenarioObservation{
			{
				Kind:       scenarioObservationSeed,
				Call:       AgentToolCall{ToolName: "run_shell_command", ArgumentsJSON: `{"command":"rm"}`, ResultJSON: `{"error":"denied"}`},
				Disclosure: ScenarioSeedDisclosurePlatformRecord,
			},
			{
				Kind:       scenarioObservationSeed,
				Call:       AgentToolCall{ToolName: "run_shell_command", ArgumentsJSON: `{"command":"y"}`, ResultJSON: `{"error":"x"}`},
				Disclosure: ScenarioSeedDisclosureChainOnly,
			},
		},
		ToolCalls: []AgentToolCall{
			{ToolName: "event_receipt_list", ArgumentsJSON: `{}`, ResultJSON: `{"receipts":[]}`, Success: true},
		},
	}
}

// requireSameInferenceMessages asserts that got is message-for-message the same
// as want.
func requireSameInferenceMessages(t *testing.T, want, got []*operatorv1.InferenceMessage) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := range want {
		assert.True(t, proto.Equal(want[i], got[i]), "message %d differs from the prompt rendered from the frozen objective", i)
	}
}

func TestContentJudgeTypedObjective_CarriesSeedRecordsAndAgentCallsOnly(t *testing.T) {
	req := typedObjectiveRequest()
	trace := typedObjectiveTrace()

	objective := contentJudgeTypedObjective(req, trace)

	assert.Equal(t, "Explain the denial.", objective.Task)
	assert.Equal(t, []judge.ObjectiveTurn{{Sender: "user", Text: "Delete everything"}}, objective.Conversation)
	assert.Equal(t, []judge.ObjectiveEvidence{{Label: "change-ticket", Source: "imported-ticket", Content: "APPROVED"}}, objective.Evidence)

	// The platform record is shown, the chain-only seed is absent, and the
	// agent's own call follows the seed records.
	require.Len(t, objective.Records, 2)
	assert.Equal(t, judge.ObjectiveRecord{
		Source:    judge.ObjectiveRecordPlatform,
		Tool:      "run_shell_command",
		Arguments: `{"command":"rm"}`,
		Outcome:   "error",
		Result:    `{"error":"denied"}`,
	}, objective.Records[0])
	assert.Equal(t, judge.ObjectiveRecord{
		Source:    judge.ObjectiveRecordAgent,
		Tool:      "event_receipt_list",
		Arguments: `{}`,
		Outcome:   "success",
		Result:    `{"receipts":[]}`,
	}, objective.Records[1])
}

func TestContentJudgeMessages_RubricVersionFollowsTheGradingContract(t *testing.T) {
	criterion := judge.Criterion{ID: "explains-denial", Statement: "The reply explains why the command was denied."}

	t.Run("free-text contract uses criterion-v1 over the rendered prompt", func(t *testing.T) {
		req := typedObjectiveRequest()
		req.ScenarioGold.ContentCheck = &ScenarioContentCheck{}
		trace := typedObjectiveTrace()

		version, messages := contentJudgeMessages(req, trace, criterion)

		assert.Equal(t, judge.CriterionRubricVersion, version)
		requireSameInferenceMessages(t,
			judge.CriterionMessages(criterion, contentJudgeObjective(req.ScenarioInput, trace, req.ScenarioGold.ContentCheck), trace.FinalOutput),
			messages)
	})

	t.Run("grading-contract.v2 uses criterion-v2 over the typed objective", func(t *testing.T) {
		req := typedObjectiveRequest()
		req.ScenarioGold.ContentCheck = &ScenarioContentCheck{}
		req.ScenarioGold.GradingContract = ScenarioGradingContractV2
		trace := typedObjectiveTrace()

		version, messages := contentJudgeMessages(req, trace, criterion)

		assert.Equal(t, judge.ObjectiveCriterionRubricVersion, version)
		requireSameInferenceMessages(t,
			judge.ObjectiveCriterionMessages(criterion, contentJudgeTypedObjective(req, trace), trace.FinalOutput),
			messages)
	})
}
