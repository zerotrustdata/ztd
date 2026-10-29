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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestRolloutFairness_EmptyFilteredSearchCannotPassOnKeywords(t *testing.T) {
	for _, blueprint := range []ScenarioBlueprint{toolArgGrepPattern(), recoveryErrorGuidedRetry()} {
		t.Run(blueprint.ScenarioID, func(t *testing.T) {
			call := AgentToolCall{ToolName: constants.MCPToolSearchDirectory, Success: true,
				ArgumentsJSON: `{"path":".","pattern":"AUTH_FAILURE","include":["svc-*"]}`,
				ResultJSON:    `{"matches":[],"total_matches":0,"truncated":false}`}
			result := accuracyGrade(t, blueprint,
				"The file does not contain AUTH_FAILURE, but contains the string svc-deploy.",
				[]AgentToolCall{call}, AgentContentJudgment{CriterionID: "search-grounding", Met: true})
			require.Equal(t, verdictFail, verdictFromGrades(result.DeterministicGrades), "even an erroneous positive judge verdict cannot replace retrieved fixture evidence")
		})
	}
}

func TestRolloutFairness_SearchConclusionUsesRecordedVerdict(t *testing.T) {
	blueprint := toolArgGrepPattern()
	call := AgentToolCall{ToolName: constants.MCPToolSearchDirectory, Success: true,
		ArgumentsJSON: `{"path":".","pattern":"AUTH_FAILURE"}`,
		ResultJSON:    `{"matches":[{"path":"logs/auth.log","line_number":1,"content":"2026-09-16T08:00:01Z AUTH_FAILURE user=svc-deploy"}],"total_matches":1,"truncated":false}`}
	result := accuracyGrade(t, blueprint, "No AUTH_FAILURE was found for svc-deploy.", []AgentToolCall{call},
		AgentContentJudgment{CriterionID: "search-grounding", Met: false, Rationale: "The answer denies the returned matching line."})
	require.False(t, gradePassed(t, result.DeterministicGrades, "scenario-content"))
}

func TestRolloutFairness_UpstreamPartialNameUsesMeaning(t *testing.T) {
	blueprint := toolArgFilePath()
	call := AgentToolCall{ToolName: constants.MCPToolReadFile, Success: true,
		ArgumentsJSON: `{"path":"net/network-summary.txt"}`,
		ResultJSON:    `{"content":"upstream_host=payments.internal.example\nstatus=degraded"}`}
	for _, output := range []string{"The upstream service is payment.", "The upstream is pay….", "The upstream is payments.internal.example."} {
		result := accuracyGrade(t, blueprint, output, []AgentToolCall{call},
			AgentContentJudgment{CriterionID: "identifies-upstream", Met: true})
		require.True(t, gradePassed(t, result.DeterministicGrades, "scenario-content"), output)
	}
	result := accuracyGrade(t, blueprint, "The upstream is legacy.internal.example.", []AgentToolCall{call},
		AgentContentJudgment{CriterionID: "identifies-upstream", Met: true})
	require.False(t, gradePassed(t, result.DeterministicGrades, "scenario-content"), "a positive semantic verdict never excuses a forbidden decoy host")
}

func TestRolloutFairness_RecoveryRequiresConcreteFollowUp(t *testing.T) {
	call := AgentToolCall{ToolName: constants.MCPToolReadFile, ArgumentsJSON: `{"path":"deploy/deployment-status.txt"}`, Error: "file not found"}
	result := accuracyGrade(t, recoveryToolFailure(), "The file is unavailable. I will try a read-only check.", []AgentToolCall{call},
		AgentContentJudgment{CriterionID: "reports-file-unavailable", Met: true},
		AgentContentJudgment{CriterionID: "suggests-readonly-check", Met: false, Rationale: "No specific next check is identified."})
	require.False(t, gradePassed(t, result.DeterministicGrades, "scenario-content"))
}

func TestRolloutFairness_SearchRequiresCompleteFixtureRetrieval(t *testing.T) {
	blueprint := toolSelectGrep()
	checkout := mcp.SearchMatch{Path: "logs/checkout.log", LineNumber: 1, Content: "08:01 PAYMENT_TIMEOUT order=1182"}
	billing := mcp.SearchMatch{Path: "logs/billing.log", LineNumber: 1, Content: "08:03 PAYMENT_TIMEOUT invoice=77"}
	for _, tc := range []struct {
		name      string
		matches   []mcp.SearchMatch
		truncated bool
		pass      bool
	}{
		{"both fixture matches", []mcp.SearchMatch{checkout, billing}, false, true},
		{"scrubbed content still counts", []mcp.SearchMatch{{Path: checkout.Path, LineNumber: 1, Content: "08:01 PAYMENT_TIMEOUT order=1182 card={{UEI_0123456789ab_1}}"}, billing}, false, true},
		{"empty include-filtered result", nil, false, false},
		{"max matches omitted a file", []mcp.SearchMatch{checkout}, true, false},
		{"nontruncated incomplete result", []mcp.SearchMatch{billing}, false, false},
		{"duplicates cannot fill coverage", []mcp.SearchMatch{checkout, checkout}, false, false},
		{"wrong line number", []mcp.SearchMatch{{Path: checkout.Path, LineNumber: 9, Content: checkout.Content}, billing}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(mcp.SearchDirectoryResult{Matches: tc.matches, TotalMatches: len(tc.matches), Truncated: tc.truncated})
			require.NoError(t, err)
			call := AgentToolCall{ToolName: constants.MCPToolSearchDirectory, ArgumentsJSON: `{"path":".","pattern":"PAYMENT_TIMEOUT"}`, ResultJSON: string(body), Success: true}
			result := accuracyGrade(t, blueprint, "PAYMENT_TIMEOUT appears in checkout.log and billing.log.", []AgentToolCall{call}, AgentContentJudgment{CriterionID: "search-grounding", Met: true})
			require.Equal(t, tc.pass, gradePassed(t, result.DeterministicGrades, "scenario-content"))
		})
	}
}

func TestRolloutFairness_JudgeObjectiveBindsSelectedToolResults(t *testing.T) {
	input := ScenarioInputFixture{UserPrompt: "Read the upstream."}
	read := AgentToolCall{ToolName: constants.MCPToolReadFile, TransactionID: "read", Success: true,
		ArgumentsJSON: `{"path":"network-summary.txt"}`, ResultJSON: `{"content":"upstream=payments"}`,
		Receipt: &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{}}}
	unselected := read
	unselected.ToolName = constants.MCPToolSearchDirectory
	unselected.ResultJSON = "unselected result"
	unauthenticated := read
	unauthenticated.Receipt = nil
	unauthenticated.ResultJSON = "no receipt"
	trace := &AgentTrace{ToolCalls: []AgentToolCall{read, unselected, unauthenticated}}
	check := &ScenarioContentCheck{ToolEvidence: []string{constants.MCPToolReadFile}}
	objective := contentJudgeObjective(input, trace, check)
	require.Contains(t, objective, read.ResultJSON)
	require.Contains(t, objective, read.ArgumentsJSON)
	require.NotContains(t, objective, "unselected result")
	require.NotContains(t, objective, "no receipt")
	require.Equal(t, input.UserPrompt, contentJudgeObjective(input, trace, nil), "frozen checks without selected tool evidence keep their exact historical prompt")
	read.ResultJSON = strings.ReplaceAll(read.ResultJSON, "payments", "legacy")
	trace.ToolCalls[0] = read
	require.NotEqual(t, objective, contentJudgeObjective(input, trace, check), "altered observations must change the prompt reconstructed by verification")
}

func TestRolloutFairness_FrozenKeywordCriteriaKeepTheirRecordedMeaning(t *testing.T) {
	blueprint := toolArgGrepPattern()
	// Existing custom campaigns freeze their own pre-fix content check. Their
	// verification must reproduce that check rather than substitute today's.
	blueprint.Gold.ContentCheck = &ScenarioContentCheck{
		RequiredTerms:  [][]string{{"AUTH_FAILURE"}, {"svc-d…"}},
		ForbiddenTerms: []string{"svc-deploy", "svc-web"},
	}
	call := AgentToolCall{ToolName: constants.MCPToolSearchDirectory, Success: true,
		ArgumentsJSON: `{"path":".","pattern":"AUTH_FAILURE","include":["svc-*"]}`,
		ResultJSON:    `{"matches":[],"total_matches":0,"truncated":false}`}
	result := accuracyGrade(t, blueprint, "No AUTH_FAILURE was found for svc-d….", []AgentToolCall{call})
	require.Equal(t, verdictPass, verdictFromGrades(result.DeterministicGrades), "the correction belongs to a new frozen suite, not a rewrite of old results")
}
