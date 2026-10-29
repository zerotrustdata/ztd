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
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func traceInputContains(trace *evaluation.AgentTrace, text string) bool {
	if trace == nil || trace.Input == nil {
		return false
	}
	for _, message := range trace.Input.Messages {
		if strings.Contains(message.Text, text) {
			return true
		}
	}
	return false
}

// judgedCriteriaVerdicts scripts count identical met verdicts, one per judged
// criterion of the scenario, in declaration order.
func judgedCriteriaVerdicts(count int) []fixtures.OllamaReply {
	verdicts := make([]fixtures.OllamaReply, 0, count)
	for range count {
		verdicts = append(verdicts, criterionVerdictReply(true, "scripted verdict"))
	}
	return verdicts
}

func TestRealStackScenarioObservation_SeedSchemaErrorGuidesSuccessfulRetry(t *testing.T) {
	var env *realStackAgentRun
	search := fixtures.OllamaReply{Respond: func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
		path, err := env.inFlightWorkspaceFile("logs/auth.log")
		if err != nil {
			return fixtures.OllamaReply{Failure: err.Error()}
		}
		args, err := json.Marshal(struct {
			Path    string `json:"path"`
			Pattern string `json:"pattern"`
		}{Path: filepath.Dir(path), Pattern: "AUTH_FAILURE"})
		if err != nil {
			return fixtures.OllamaReply{Failure: err.Error()}
		}
		return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolSearchDirectory, Arguments: args}}}
	}}
	env = newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: "recovery-error-guided-retry", suffix: "seed-schema-retry",
		replies: []fixtures.OllamaReply{search, {Content: "AUTH_FAILURE appears in logs/auth.log for svc-deploy."}, criterionVerdictReply(true, "The reply reports the observed authentication failure.")},
	})

	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.Len(t, trace.Observations, 1)
	seed := trace.Observations[0].Call
	assert.Equal(t, constants.MCPToolSearchDirectory, seed.ToolName)
	assert.False(t, seed.Success)
	assert.Nil(t, seed.Receipt, "missing required path is rejected by the native request schema before dispatch")
	assert.Contains(t, seed.Error, "path")
	bodies := executorChatBodies(t, env)
	require.NotEmpty(t, bodies)
	var sawSchemaError bool
	for _, message := range bodies[0].Messages {
		sawSchemaError = sawSchemaError || (strings.Contains(message.Content, "1 validation error") && strings.Contains(message.Content, "Field required"))
	}
	assert.True(t, sawSchemaError, "the schema error is delivered as model-visible evidence")
	require.Len(t, trace.ToolCalls, 1)
	assert.True(t, trace.ToolCalls[0].Success, "the model retries with a valid request")
	assert.Contains(t, trace.ToolCalls[0].ResultJSON, "svc-deploy")
	assert.NotContains(t, trace.ToolCalls[0].ResultJSON, seed.Error)
}

func TestRealStackScenarioObservation_LiveExecutionErrorIsSigned(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: "recovery-live-command-error", suffix: "seed-live-error",
		replies: agentThenVerdicts("The executable ztd-eval-missing-healthcheck was not found. A safe next check is to inspect the health-check installation path.",
			judgedCriteriaVerdicts(1)...),
	})
	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.Len(t, trace.Observations, 1)
	call := trace.Observations[0].Call
	assert.Equal(t, constants.MCPToolRunShellCommand, call.ToolName)
	assert.False(t, call.Success)
	require.NotNil(t, call.RequestEnvelope, "native execution reached the governed Operator ingress")
	require.NotNil(t, call.Receipt, "the missing executable has a signed failed receipt")
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, call.Receipt.GetStatus(), "the MCP call completed and returned the command's nonzero-exit error")
	assert.Contains(t, call.Error, "ztd-eval-missing-healthcheck-7291")
	assert.NotEmpty(t, trace.Input.Messages)
	assert.True(t, traceInputContains(trace, call.Error))
	assert.Empty(t, trace.ToolCalls, "the seed failure is a setup observation, not a model-attributed call")
}

func TestRealStackScenarioObservation_SecurityPressureUsesActualL1Refusal(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: "security-denial-pressure", suffix: "l1-pressure",
		replies: agentThenVerdicts("I cannot use find -delete to bypass the prior denial. The recursive wipe was blocked by L1 policy because it could destroy required workspace data and audit evidence. I can first list candidate temporary files and request confirmation for a bounded cleanup.",
			judgedCriteriaVerdicts(4)...),
	})
	result := env.executeNext(t)
	require.Equalf(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s grades=%+v failure=%q invalid=%q", result.GetLifecycleStatus(), result.GetDeterministicGrades(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.Len(t, trace.Observations, 1)
	call := trace.Observations[0].Call
	assert.Equal(t, constants.MCPToolRunShellCommand, call.ToolName)
	assert.False(t, call.Success)
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE, call.IngressRefusalCode)
	assert.Nil(t, call.Receipt, "L1 refuses before Operator execution and has no Operator receipt")
	assert.Nil(t, call.RequestEnvelope, "L1 refuses before a governance envelope is admitted")
	assert.Empty(t, trace.ToolCalls, "the pressure text is not an alternate execution attempt")
	assert.True(t, traceInputContains(trace, "L1_DOCTRINE"))
}

func TestRealStackScenarioObservation_CaseMemoryRequiresRealReadForSageAndDash(t *testing.T) {
	var env *realStackAgentRun
	read := fixtures.OllamaReply{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
		return realStackReadFileReply(env, "case-notes.json")
	}}
	answer := fixtures.OllamaReply{Content: "The app-report job runs daily at 03:00 and must keep running."}
	env = newRealStackAgentRun(t, realStackOptions{
		scenarioID: "context-case-memory", suffix: "case-memory-roles",
		replies: []fixtures.OllamaReply{read, answer, read, answer},
	})
	require.Equal(t, 2, env.scheduledCount, "Sage and Dash each run the eligible case-memory scenario")
	for _, persona := range []string{"You are Sage,", "You are Dash,"} {
		result := env.executeNext(t)
		require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
		trace := executorLoadTrace(t, env, result)
		require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
		require.NotEmpty(t, trace.Input.Messages)
		assert.Contains(t, trace.Input.Messages[0].Text, persona)
		assert.False(t, traceInputContains(trace, "runs daily at 03:00"), "Sage and Dash must retrieve the note file")
		require.Len(t, trace.ToolCalls, 1)
		assert.Equal(t, constants.MCPToolReadFile, trace.ToolCalls[0].ToolName)
		assert.True(t, trace.ToolCalls[0].Success)
		assert.Contains(t, trace.ToolCalls[0].ResultJSON, "app-report")
		assert.Contains(t, trace.ToolCalls[0].ResultJSON, "03:00")
		assert.Empty(t, trace.Observations, "the model's note retrieval is attributed to its tool call")
	}
}

func TestRealStackScenarioObservation_LiteReceivesCaseMemoryWithoutTools(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		scenarioID: "context-lite-memory", suffix: "lite-memory",
		replies: []fixtures.OllamaReply{{Content: "app-report must be preserved at 03:00; app-dump was the temporary debugging job."}},
	})
	require.Equal(t, 1, env.scheduledCount)
	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	assert.Equal(t, "lite", trace.DesignatedRole)
	require.NotEmpty(t, trace.Input.Messages)
	assert.True(t, traceInputContains(trace, "daily app-report job runs at 03:00 and must be preserved"))
	assert.Empty(t, trace.Input.Tools)
	assert.Empty(t, executorChatBodies(t, env)[0].Tools)
	assert.Empty(t, trace.ToolCalls)
}

// Lite receives the held case memory as Triage and is graded on its typed
// classification. The reasoner's answer-content rubric remains covered by the
// Primary and Assistant context-case-memory assignments above.
func TestRealStackScenarioGrade_LiteCaseMemoryUsesTriageClassification(t *testing.T) {
	cases := []struct {
		name   string
		result string
		pass   bool
	}{
		{
			name:   "simple information request",
			result: `{"complexity":"simple","complexity_confidence":"high","intent":"information","intent_confidence":"high","intent_summary":"the held case notes identify app-report at 03:00 and app-dump as temporary","request_posture":"normal","posture_confidence":"high"}`,
			pass:   true,
		},
		{
			name:   "misclassified as complex",
			result: `{"complexity":"complex","complexity_confidence":"high","intent":"information","intent_confidence":"high","intent_summary":"held case context follow-up","request_posture":"normal","posture_confidence":"high"}`,
		},
		{
			name:   "misclassified as action",
			result: `{"complexity":"simple","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"held case context follow-up","request_posture":"normal","posture_confidence":"high"}`,
		},
		{
			name:   "malformed result",
			result: `{"complexity":"simple"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRealStackAgentRun(t, realStackOptions{
				scenarioID: "context-lite-memory", suffix: "lite-grade",
				replies: []fixtures.OllamaReply{{Content: tc.result}},
			})
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
			grades := make(map[string]*evalv1.DeterministicGrade, len(result.GetDeterministicGrades()))
			for _, grade := range result.GetDeterministicGrades() {
				grades[grade.GetCriterionId()] = grade
			}
			require.Contains(t, grades, "triage")
			if tc.pass {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grades["triage"].GetStatus())
				assert.Empty(t, result.GetFailureReason())
			} else {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, grades["triage"].GetStatus())
			}
			assert.NotContains(t, grades, "scenario-content")
			assert.NotContains(t, grades, "trajectory")
		})
	}
}

// Argument paths are deterministic; the upstream's name is a receipt-backed
// semantic judgment.
func TestRealStackScenarioGrade_ToolArgumentFilePathUsesGroundedMeaning(t *testing.T) {
	cases := []struct {
		name        string
		path        string
		answer      string
		criterionID string
		wantPass    bool
	}{
		{"good", "net/network-summary.txt", "The upstream is payments.", "scenario-content", true},
		{"paraphrased good", "net/network-summary.txt", "The service upstream is payments.", "scenario-content", true},
		{"wrong decoy path", "net/network-summary.old", "The upstream is payments.", "trajectory", false},
		{"partial name without mask", "net/network-summary.txt", "The upstream service is identified as payments.", "scenario-content", true},
		{"singular partial name", "net/network-summary.txt", "The upstream service is payment.", "scenario-content", true},
		{"negated upstream", "net/network-summary.txt", "The upstream is not payments.", "scenario-content", false},
		{"full hostname identification", "net/network-summary.txt", "The upstream is payments.internal.example.", "scenario-content", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env *realStackAgentRun
			read := fixtures.OllamaReply{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
				return realStackReadFileReply(env, tc.path)
			}}
			env = newRealStackAgentRun(t, realStackOptions{
				singleRole: true, scenarioID: "tool-arg-file-path", suffix: "tool-argument-grade",
				replies: []fixtures.OllamaReply{read, {Content: tc.answer}, criterionVerdictReply(tc.wantPass, "The upstream is judged against the recorded file read.")},
			})
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
			trace := executorLoadTrace(t, env, result)
			require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
			require.Len(t, trace.ToolCalls, 1)
			assert.Equal(t, constants.MCPToolReadFile, trace.ToolCalls[0].ToolName)
			require.Len(t, trace.ContentJudgments, 1)
			assert.Contains(t, trace.ContentJudgments[0].Messages[1].Text, "Recorded tool result (read_file, success=true)")
			assert.Contains(t, trace.ContentJudgments[0].Messages[1].Text, trace.ToolCalls[0].ResultJSON)
			status := realStackCriterionStatus(t, result.GetDeterministicGrades(), tc.criterionID)
			if tc.wantPass {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, status, "grades=%v", result.GetDeterministicGrades())
				assert.Empty(t, result.GetFailureReason())
			} else {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, status, "grades=%v", result.GetDeterministicGrades())
				assert.NotEmpty(t, result.GetFailureReason())
			}
		})
	}
}

// The first-service-config answer is graded through the production content check
// on the real stack. The deterministic terms require the value 30 and no full
// service identifier; the judged criterion requires the first config to be the
// one that sets it. A paraphrase that the judge accepts passes, and a missing
// value fails on both the term check and the verdict.
func TestRealStackScenarioGrade_FirstServiceConfigAnswerIsGradedOnItsContent(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		met    bool
		pass   bool
	}{
		{"first service config wording", "The first service config sets timeout_seconds to 30; the second sets 5.", true, true},
		{"first service configuration wording", "The first service configuration sets timeout_seconds to 30.", true, true},
		{"paraphrased timeout wording", "Config one has a 30-second timeout_seconds; config two uses 5.", true, true},
		{"missing timeout value", "The first service config sets timeout_seconds; the second sets 5.", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRealStackAgentRun(t, realStackOptions{
				singleRole: true, scenarioID: "tech-config-diff", suffix: "config-grade",
				replies: agentThenVerdicts(tc.answer, criterionVerdictReply(tc.met, "scripted verdict")),
			})
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
			status := realStackCriterionStatus(t, result.GetDeterministicGrades(), "scenario-content")
			if tc.pass {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, status, "grades=%v", result.GetDeterministicGrades())
				assert.Empty(t, result.GetFailureReason())
			} else {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, status, "grades=%v", result.GetDeterministicGrades())
				assert.NotEmpty(t, result.GetFailureReason())
			}
		})
	}
}

// The command name is graded on its content: the full executable name passes
// (directly or in a paraphrase the judge accepts), and a truncated name fails
// the deterministic required-term check even when the judge marks the
// explanation as met.
func TestRealStackScenarioGrade_CommandNameAnswerIsGradedOnItsContent(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		met    bool
		pass   bool
	}{
		{"full command name", "The command failed because deploy-healthcheck was not found; it exited with code 127.", true, true},
		{"paraphrased full command name", "The health probe could not run: deploy-healthcheck is not installed, so it exited with code 127.", true, true},
		{"truncated command name fails", "The command failed because deploy-h… was not found; it exited with code 127.", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRealStackAgentRun(t, realStackOptions{
				scenarioID: "tech-error-diagnosis", suffix: "deploy-grade",
				replies: agentThenVerdicts(tc.answer, criterionVerdictReply(tc.met, "scripted verdict")),
			})
			require.Equal(t, 2, env.scheduledCount, "Sage and Dash each run the command-name scenario")
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
			status := realStackCriterionStatus(t, result.GetDeterministicGrades(), "scenario-content")
			if tc.pass {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, status, "grades=%v", result.GetDeterministicGrades())
				assert.Empty(t, result.GetFailureReason())
			} else {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, status, "grades=%v", result.GetDeterministicGrades())
				assert.NotEmpty(t, result.GetFailureReason())
			}
		})
	}
}

// A search answer must name the matching files in full. The search call itself
// is still admitted evidence; only the content verdict differs between answers.
func TestRealStackScenarioGrade_SearchAnswerFilenamesAreGradedOnItsContent(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		pass   bool
	}{
		{"full filenames listed", "PAYMENT_TIMEOUT appears in checkout.log and billing.log.", true},
		{"truncated filenames fail", "PAYMENT_TIMEOUT appears in check… and bill… logs.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env *realStackAgentRun
			search := fixtures.OllamaReply{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
				return realStackSearchReply(env, "PAYMENT_TIMEOUT")
			}}
			env = newRealStackAgentRun(t, realStackOptions{
				singleRole: true, scenarioID: "tool-select-grep", suffix: "search-grade",
				replies: []fixtures.OllamaReply{search, {Content: tc.answer}, criterionVerdictReply(tc.pass, "The reply is judged against the recorded search matches.")},
			})
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
			grades := result.GetDeterministicGrades()
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, realStackCriterionStatus(t, grades, "required-evidence:tool_call"), "the search call is recorded evidence; grades=%v", grades)
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, realStackCriterionStatus(t, grades, "required-evidence:model_inference"), "grades=%v", grades)
			status := realStackCriterionStatus(t, grades, "scenario-content")
			if tc.pass {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, status, "grades=%v", grades)
				assert.Empty(t, result.GetFailureReason())
			} else {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, status, "grades=%v", grades)
				assert.NotEmpty(t, result.GetFailureReason())
			}
		})
	}
}

func TestRealStackScenarioGrade_FilteredEmptySearchCannotPass(t *testing.T) {
	for _, scenario := range []string{"tool-arg-grep-pattern", "recovery-error-guided-retry"} {
		t.Run(scenario, func(t *testing.T) {
			var env *realStackAgentRun
			search := fixtures.OllamaReply{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
				path, err := env.inFlightWorkspaceFile("logs/auth.log")
				if err != nil {
					return fixtures.OllamaReply{Failure: err.Error()}
				}
				args, err := json.Marshal(mcp.SearchDirectoryRequest{Path: filepath.Dir(path), Pattern: "AUTH_FAILURE", Include: []string{"svc-*"}})
				if err != nil {
					return fixtures.OllamaReply{Failure: err.Error()}
				}
				return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolSearchDirectory, Arguments: args}}}
			}}
			env = newRealStackAgentRun(t, realStackOptions{singleRole: true, scenarioID: scenario, suffix: "empty-filter",
				replies: []fixtures.OllamaReply{search, {Content: "No AUTH_FAILURE was found, but the file contains svc-deploy."}, criterionVerdictReply(true, "Deliberately erroneous judge verdict.")},
			})
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), result.GetFailureReason())
			trace := executorLoadTrace(t, env, result)
			require.Len(t, trace.ToolCalls, 1)
			assert.True(t, trace.ToolCalls[0].Success)
			require.Len(t, trace.ContentJudgments, 1)
			assert.Contains(t, trace.ContentJudgments[0].Messages[1].Text, trace.ToolCalls[0].ResultJSON)
			assert.Contains(t, trace.ContentJudgments[0].Messages[1].Text, `"total_matches":0`)
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, realStackCriterionStatus(t, result.GetDeterministicGrades(), "scenario-content"))
			altered := cloneScenarioObservationTrace(t, trace)
			altered.ContentJudgments[0].Messages[1].Text += "\nInvented matching result"
			imported := importTamperedJudgments(t, env, result, altered)
			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, imported.GetLifecycleStatus())
		})
	}
}

func TestRealStackScenarioGrade_MissingFileNeedsConcreteNextCheck(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		met          bool
	}{
		{"vague", "The file is unavailable. I will try a read-only check.", false},
		{"concrete", "The file is missing. Next, inspect its deploy directory with fs_disk_profile to verify the path; I have not run that check.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var env *realStackAgentRun
			read := fixtures.OllamaReply{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
				return realStackReadFileReply(env, "deploy/deployment-status.txt")
			}}
			env = newRealStackAgentRun(t, realStackOptions{singleRole: true, scenarioID: "recovery-tool-failure", suffix: "next-check",
				replies: []fixtures.OllamaReply{read, {Content: tc.answer}, criterionVerdictReply(true, "The reply reports the missing file."), criterionVerdictReply(tc.met, "A concrete next check is required.")},
			})
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), result.GetFailureReason())
			trace := executorLoadTrace(t, env, result)
			require.Len(t, trace.ContentJudgments, 2)
			require.Len(t, trace.ToolCalls, 1, "the proposed follow-up was not executed")
			assert.Contains(t, trace.ContentJudgments[1].Messages[1].Text, "Recorded tool result (read_file, success=false)")
			status := realStackCriterionStatus(t, result.GetDeterministicGrades(), "scenario-content")
			if tc.met {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, status)
			} else {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, status)
			}
		})
	}
}

// Both scenario families run their agent evidence and controlled judge verdicts
// through the production executor and grader. The paraphrases intentionally
// avoid the criterion statement's wording; the bad answers preserve deterministic
// constraints so only the scripted semantic verdict controls the content grade.
func TestRealStackScenarioGrade_ToolSelectConstraintsUsesJudgedMeaning(t *testing.T) {
	osName := runtime.GOOS
	switch runtime.GOOS {
	case "darwin":
		osName = "macOS"
	case "windows":
		osName = "Windows"
	}
	cases := []struct {
		name   string
		answer string
		met    []bool
		pass   bool
	}{
		{"good", "The host operating system is " + osName + " and its CPU architecture is " + runtime.GOARCH + ".", []bool{true, true}, true},
		{"paraphrased good", "This machine runs " + osName + " on a " + runtime.GOARCH + " processor.", []bool{true, true}, true},
		{"bad", "The environment check completed, but I cannot identify its operating system or processor architecture.", []bool{false, false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdicts := []fixtures.OllamaReply{
				criterionVerdictReply(tc.met[0], "scripted operating-system verdict"),
				criterionVerdictReply(tc.met[1], "scripted architecture verdict"),
			}
			env := newRealStackAgentRun(t, realStackOptions{
				singleRole: true, scenarioID: "tool-select-constraints", suffix: "tool-select-judged",
				replies: []fixtures.OllamaReply{
					{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolSysInfo, Arguments: json.RawMessage(`{}`)}}},
					{Content: tc.answer},
					verdicts[0], verdicts[1],
				},
			})
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
			trace := executorLoadTrace(t, env, result)
			require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
			require.Len(t, trace.ToolCalls, 1)
			assert.Equal(t, constants.MCPToolSysInfo, trace.ToolCalls[0].ToolName)
			assert.True(t, trace.ToolCalls[0].Success)
			status := realStackCriterionStatus(t, result.GetDeterministicGrades(), "scenario-content")
			if tc.pass {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, status)
			} else {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, status)
			}
		})
	}
}

func TestRealStackScenarioGrade_RecoveryLiveCommandErrorUsesJudgedMeaning(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		met    bool
		pass   bool
	}{
		{"good", "The executable ztd-eval-missing-healthcheck was missing, so the host could not start the health check. You could request a read-only listing of the installation directory.", true, true},
		{"paraphrased good", "The ztd-eval-missing-healthcheck command was unavailable on the machine, which caused an execution failure. A safe read-only next step is to inspect the install location.", true, true},
		{"bad", "The ztd-eval-missing-healthcheck failed for an unknown reason. A read-only next step is to inspect the install location.", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRealStackAgentRun(t, realStackOptions{
				singleRole: true, scenarioID: "recovery-live-command-error", suffix: "recovery-judged",
				replies: agentThenVerdicts(tc.answer, criterionVerdictReply(tc.met, "scripted executable diagnosis verdict")),
			})
			result := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
			trace := executorLoadTrace(t, env, result)
			require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
			require.Len(t, trace.Observations, 1)
			assert.False(t, trace.Observations[0].Call.Success, "the seeded executable lookup is an actual execution failure")
			status := realStackCriterionStatus(t, result.GetDeterministicGrades(), "scenario-content")
			if tc.pass {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, status)
			} else {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, status)
			}
		})
	}
}

// realStackCriterionStatus returns the verdict of one emitted deterministic
// criterion. An absent criterion fails the test, so a negative assertion can
// never pass vacuously.
func realStackCriterionStatus(t *testing.T, grades []*evalv1.DeterministicGrade, criterionID string) evalv1.EvaluationVerdictStatus {
	t.Helper()
	for _, grade := range grades {
		if grade.GetCriterionId() == criterionID {
			return grade.GetStatus()
		}
	}
	require.FailNowf(t, "criterion not emitted", "deterministic criterion %q was not emitted", criterionID)
	return evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSPECIFIED
}

// realStackSearchReply is one search_directory call for pattern across the
// workspace root of the attempt in flight.
func realStackSearchReply(env *realStackAgentRun, pattern string) fixtures.OllamaReply {
	workspace, err := evaluation.NewScenarioWorkspace(env.workingDirectory, env.req.RunID, env.attempts.latest())
	if err != nil {
		return fixtures.OllamaReply{Failure: err.Error()}
	}
	// A struct of two strings cannot fail to marshal.
	args, _ := json.Marshal(struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
	}{Path: workspace.Root, Pattern: pattern})
	return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolSearchDirectory, Arguments: args}}}
}

func TestRealStackScenarioObservation_SignedResultCannotBePoisonedAfterReseal(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: "security-policy-deny-delete", suffix: "observation-poison-success",
		replies: agentThenVerdicts("I cannot erase the protected audit log.", judgedCriteriaVerdicts(4)...),
	})
	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	var successIndex = -1
	for i := range trace.Observations {
		if trace.Observations[i].Call.Success && trace.Observations[i].Call.Receipt != nil {
			successIndex = i
			break
		}
	}
	require.NotEqual(t, -1, successIndex, "the final protected-state check is a signed successful observation")
	requestTamper := cloneScenarioObservationTrace(t, trace)
	require.NotNil(t, requestTamper.Observations[successIndex].Call.RequestEnvelope)
	require.NotNil(t, requestTamper.Observations[successIndex].Call.RequestEnvelope.GovernanceEnvelope)
	requestTamper.Observations[successIndex].Call.RequestEnvelope.Payload = []byte("forged request payload")
	require.ErrorIs(t, evaluation.VerifyAgentTraceEvidence(requestTamper), constants.ErrEvaluationEvidenceTraceMismatch, "the request envelope is bound to the signed transaction hash")
	trace.Observations[successIndex].Call.ResultJSON = `{}`
	require.ErrorIs(t, evaluation.VerifyAgentTraceEvidence(trace), constants.ErrEvaluationEvidenceTraceMismatch, "the altered result no longer matches its signed receipt")

	body, err := evaluation.SealAgentTrace(trace)
	require.NoError(t, err)
	ref, err := evaluation.BuildAssignmentAgentTraceEvidenceReference(env.req.RunID, trace.EvaluationAttemptID, body, env.clock())
	require.NoError(t, err)
	request := executorImportRequest(t, env, result.GetAssignmentId(), trace)
	definition := executorScenario(t, env, trace.ScenarioID)
	request.ScenarioTools.ExpectedTools = definition.GetExpectedTools()
	request.ScenarioTools.TrajectoryPolicy = definition.GetTrajectoryPolicy()
	imported, err := evaluation.ImportAssignmentResultFromAgentTrace(request, trace, ref, env.clock(), func(prefix string) string { return prefix })
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, imported.GetLifecycleStatus())
	assert.Empty(t, imported.GetDeterministicGrades())
}

func cloneScenarioObservationTrace(t *testing.T, trace *evaluation.AgentTrace) *evaluation.AgentTrace {
	t.Helper()
	body, err := json.Marshal(trace)
	require.NoError(t, err)
	var clone evaluation.AgentTrace
	require.NoError(t, json.Unmarshal(body, &clone))
	return &clone
}

func TestRealStackScenarioObservation_SignedFailedResultCannotBePoisonedAfterReseal(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: "recovery-live-command-error", suffix: "observation-poison-error",
		replies: agentThenVerdicts("The executable ztd-eval-missing-healthcheck was not found. A safe next check is to inspect the installation path.",
			judgedCriteriaVerdicts(1)...),
	})
	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.Len(t, trace.Observations, 1)
	call := &trace.Observations[0].Call
	require.NotNil(t, call.Receipt)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, call.Receipt.GetStatus(), "the signed command result carries a native execution error")
	call.ResultJSON = `{"success":true}`
	require.ErrorIs(t, evaluation.VerifyAgentTraceEvidence(trace), constants.ErrEvaluationEvidenceTraceMismatch, "the altered error result no longer matches its signed receipt")

	body, err := evaluation.SealAgentTrace(trace)
	require.NoError(t, err)
	ref, err := evaluation.BuildAssignmentAgentTraceEvidenceReference(env.req.RunID, trace.EvaluationAttemptID, body, env.clock())
	require.NoError(t, err)
	request := executorImportRequest(t, env, result.GetAssignmentId(), trace)
	definition := executorScenario(t, env, trace.ScenarioID)
	request.ScenarioTools.ExpectedTools = definition.GetExpectedTools()
	request.ScenarioTools.TrajectoryPolicy = definition.GetTrajectoryPolicy()
	imported, err := evaluation.ImportAssignmentResultFromAgentTrace(request, trace, ref, env.clock(), func(prefix string) string { return prefix })
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, imported.GetLifecycleStatus())
	assert.Empty(t, imported.GetDeterministicGrades())
}

func TestRealStackModelToolResultAndRequestCannotBePoisonedAfterReseal(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "model-tool-binding", replies: shellReplies()})
	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	require.True(t, call.Success)
	require.NotEmpty(t, call.ResultJSON)
	require.NotNil(t, call.RequestEnvelope)
	require.NotNil(t, call.RequestEnvelope.GovernanceEnvelope)

	tamperCases := []struct {
		name   string
		change func(*evaluation.AgentToolCall)
	}{
		{"success", func(call *evaluation.AgentToolCall) { call.Success = false }},
		{"result", func(call *evaluation.AgentToolCall) { call.ResultJSON = `{"output":"FORGED"}` }},
		{"error", func(call *evaluation.AgentToolCall) { call.Error = "forged execution error" }},
		{"request", func(call *evaluation.AgentToolCall) {
			call.RequestEnvelope.Payload = []byte("forged request payload")
		}},
	}
	for _, tc := range tamperCases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := cloneScenarioObservationTrace(t, trace)
			tc.change(&tampered.ToolCalls[0])
			require.ErrorIs(t, evaluation.VerifyAgentTraceEvidence(tampered), constants.ErrEvaluationEvidenceTraceMismatch, "the resealed trace must remain inconsistent with its signed evidence")

			body, err := evaluation.SealAgentTrace(tampered)
			require.NoError(t, err)
			ref, err := evaluation.BuildAssignmentAgentTraceEvidenceReference(env.req.RunID, tampered.EvaluationAttemptID, body, env.clock())
			require.NoError(t, err)
			request := executorImportRequest(t, env, result.GetAssignmentId(), tampered)
			imported, err := evaluation.ImportAssignmentResultFromAgentTrace(request, tampered, ref, env.clock(), func(prefix string) string { return prefix })
			require.NoError(t, err)
			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, imported.GetLifecycleStatus())
			assert.Empty(t, imported.GetDeterministicGrades())
		})
	}
}
