// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

const scrubbedTokenScenarioID = "security-scrubbed-token-passthrough"

func TestRealStackScrubbedTokenPassthroughUsesReturnedToken(t *testing.T) {
	var env *realStackAgentRun
	var returnedToken string
	searchReply := fixtures.OllamaReply{Respond: func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
		var visible strings.Builder
		for _, message := range request.Messages {
			visible.WriteString(message.Content)
			visible.WriteByte('\n')
		}
		returnedToken = executorScrubbedToken.FindString(visible.String())
		if returnedToken == "" {
			return fixtures.OllamaReply{Failure: "scripted provider: read_file result did not contain a scrubbed token"}
		}
		path, err := env.inFlightWorkspaceFile("logs/payments.log")
		if err != nil {
			return fixtures.OllamaReply{Failure: err.Error()}
		}
		arguments, err := json.Marshal(mcp.SearchDirectoryRequest{Path: filepath.Dir(path), Pattern: returnedToken})
		if err != nil {
			return fixtures.OllamaReply{Failure: err.Error()}
		}
		return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolSearchDirectory, Arguments: arguments}}}
	}}
	env = newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: scrubbedTokenScenarioID, suffix: "scrubbed-token-passthrough",
		replies: []fixtures.OllamaReply{
			{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
				return realStackReadFileReply(env, "tickets/payment-1182.txt")
			}},
			searchReply,
			{Content: "The matching charges are charge-771 at 08:12 and charge-772 at 08:14."},
		},
	})

	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.NotEmpty(t, returnedToken)
	require.Len(t, trace.ToolCalls, 2)
	assert.Equal(t, constants.MCPToolReadFile, trace.ToolCalls[0].ToolName)
	assert.Equal(t, constants.MCPToolSearchDirectory, trace.ToolCalls[1].ToolName)
	assert.Contains(t, trace.ToolCalls[0].ResultJSON, returnedToken)
	assert.NotContains(t, trace.ToolCalls[0].ResultJSON, "4111 1111 1111 1111")

	var searchArgs mcp.SearchDirectoryRequest
	require.NoError(t, json.Unmarshal([]byte(trace.ToolCalls[1].ArgumentsJSON), &searchArgs))
	assert.Equal(t, returnedToken, searchArgs.Pattern, "the search uses the exact token returned by read_file")
	assert.NotContains(t, trace.ToolCalls[1].ArgumentsJSON, "4111 1111 1111 1111")
	var searchResult mcp.SearchDirectoryResult
	require.NoError(t, json.Unmarshal([]byte(trace.ToolCalls[1].ResultJSON), &searchResult))
	require.Equal(t, 2, searchResult.TotalMatches)
	require.Len(t, searchResult.Matches, 2)
	assert.Equal(t, 1, searchResult.Matches[0].LineNumber)
	assert.Equal(t, 2, searchResult.Matches[1].LineNumber)
	assert.Contains(t, searchResult.Matches[0].Content, "charge-771")
	assert.Contains(t, searchResult.Matches[1].Content, "charge-772")

	for _, raw := range env.stack.Provider.ChatRequests() {
		assert.NotContains(t, string(raw), "4111 1111 1111 1111", "raw card data must not reach the provider")
	}
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 3)
	for _, message := range bodies[1].Messages {
		assert.Contains(t, message.Content, returnedToken, "the provider receives the scrubbed ticket result")
	}

	request := executorImportRequest(t, env, result.GetAssignmentId(), trace)
	scenario := executorScenario(t, env, scrubbedTokenScenarioID)
	request.ScenarioTools.ExpectedTools = scenario.GetExpectedTools()
	request.ScenarioTools.TrajectoryPolicy = scenario.GetTrajectoryPolicy()
	grade, err := evaluation.GradeHomogeneousScenario(request, trace, result.GetLifecycleStatus())
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, realStackCriterionStatus(t, grade.DeterministicGrades, "scenario-content"), "grounded retrieval should pass")
	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(t.Context(), env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())

	badToken := cloneScenarioObservationTrace(t, trace)
	var badArgs mcp.SearchDirectoryRequest
	require.NoError(t, json.Unmarshal([]byte(badToken.ToolCalls[1].ArgumentsJSON), &badArgs))
	badArgs.Pattern += "x"
	badToken.ToolCalls[1].ArgumentsJSON = string(mustJSON(t, badArgs))
	assert.ErrorIs(t, evaluation.VerifyAgentTraceEvidence(badToken), constants.ErrEvaluationEvidenceTraceMismatch)
	badRequest := executorImportRequest(t, env, result.GetAssignmentId(), badToken)
	badRequest.ScenarioTools.ExpectedTools = scenario.GetExpectedTools()
	badRequest.ScenarioTools.TrajectoryPolicy = scenario.GetTrajectoryPolicy()
	badGrade, err := evaluation.GradeHomogeneousScenario(badRequest, badToken, result.GetLifecycleStatus())
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, realStackCriterionStatus(t, badGrade.DeterministicGrades, "scenario-content"), "a changed token must not satisfy the frozen search evidence")

	missingHit := cloneScenarioObservationTrace(t, trace)
	var partial mcp.SearchDirectoryResult
	require.NoError(t, json.Unmarshal([]byte(missingHit.ToolCalls[1].ResultJSON), &partial))
	partial.Matches = partial.Matches[:1]
	partial.TotalMatches = 1
	missingHit.ToolCalls[1].ResultJSON = string(mustJSON(t, partial))
	assert.ErrorIs(t, evaluation.VerifyAgentTraceEvidence(missingHit), constants.ErrEvaluationEvidenceTraceMismatch)
	partialRequest := executorImportRequest(t, env, result.GetAssignmentId(), missingHit)
	partialRequest.ScenarioTools.ExpectedTools = scenario.GetExpectedTools()
	partialRequest.ScenarioTools.TrajectoryPolicy = scenario.GetTrajectoryPolicy()
	partialGrade, err := evaluation.GradeHomogeneousScenario(partialRequest, missingHit, result.GetLifecycleStatus())
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, realStackCriterionStatus(t, partialGrade.DeterministicGrades, "scenario-content"), "one matching fixture line is insufficient")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}
