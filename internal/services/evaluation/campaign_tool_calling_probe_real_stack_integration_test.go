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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestAgentEvidenceIntegration_ToolCallingProbeExcludesUnsupportedToolCell(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole:        true,
		suffix:            "tool-probe-unsupported",
		scenarioID:        "tool-select-file-read",
		toolProbeRequired: true,
		replies: []fixtures.OllamaReply{{
			Content: `{"name":"read_file","arguments":{"path":"/workspace/secret"}}`,
		}},
	})
	require.Equal(t, 1, env.scheduledCount)
	assert.True(t, env.req.Catalog.GetToolCallingProbeRequired())

	result := env.executeNext(t)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
	assert.Contains(t, result.GetFailureReason(), "capability_unsupported")
	assert.Equal(t, 1, env.stack.Provider.ChatCount(), "the model receives the governed probe and no scored request")

	_, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, result.GetAssignmentId())
	require.ErrorIs(t, err, constants.ErrNotFound, "an excluded tool cell has no scored agent trace")
	run, err := env.store.LoadRun(ctx, env.req.RunID)
	require.NoError(t, err)
	require.Len(t, run.GetToolCallingProbes(), 1)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, run.GetToolCallingProbes()[0].GetObservation().GetOutcome())

	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), strings.Join(report.GetFailureReasons(), "\n"))
}

func TestAgentEvidenceIntegration_ToolCallingProbeStaysOutsideScoredTrace(t *testing.T) {
	ctx := t.Context()
	var env *realStackAgentRun
	env = newRealStackAgentRun(t, realStackOptions{
		singleRole:        true,
		suffix:            "tool-probe-supported",
		scenarioID:        "tool-select-file-read",
		toolProbeRequired: true,
		replies: []fixtures.OllamaReply{
			{ToolCalls: []fixtures.OllamaToolCall{{Name: "probe_echo", Arguments: json.RawMessage(`{"message":"capability-probe-tool"}`)}}},
			{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
				return realStackReadFileReply(env, "config/retry-config.env")
			}},
			{Content: `{"retry_limit":3}`},
			{Content: `{"met":true,"rationale":"The reply reports retry_limit 3 from the active configuration."}`},
		},
	})
	require.Equal(t, 1, env.scheduledCount)
	assert.True(t, env.req.Catalog.GetToolCallingProbeRequired())

	result := env.executeNext(t)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), result.GetInvalidEvidenceDetail())
	assert.Equal(t, 4, env.stack.Provider.ChatCount(), "probe, scored tool call, scored final response, and content judge are separate calls")

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, result.GetAssignmentId())
	require.NoError(t, err)
	require.Len(t, trace.ModelCalls, 2, "only the two scored turns belong to the agent trace")
	require.NotNil(t, trace.Bracket)
	for _, call := range trace.ModelCalls {
		assert.NotContains(t, call.ProviderAttemptID, "tool-calling-probe")
	}
	run, err := env.store.LoadRun(ctx, env.req.RunID)
	require.NoError(t, err)
	require.Len(t, run.GetToolCallingProbes(), 1)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, run.GetToolCallingProbes()[0].GetObservation().GetOutcome())
	probeEvidence, err := env.store.LoadToolCallingProbeEvidence(ctx, env.req.RunID, run.GetToolCallingProbes()[0].GetEvidenceRef())
	require.NoError(t, err)
	assert.Equal(t, "tool-calling-probe", probeEvidence.GetRequest().GetScenarioId())
	assert.NotEqual(t, trace.EvaluationAttemptID, probeEvidence.GetRequest().GetEvaluationAttemptId())
	assert.NotEqual(t, probeEvidence.GetResponse().GetTransactionId(), trace.Bracket.Before.GetTransactionId())
	assert.NotEqual(t, probeEvidence.GetResponse().GetTransactionId(), trace.Bracket.After.GetTransactionId())
	for _, call := range trace.ModelCalls {
		assert.NotEqual(t, probeEvidence.GetRequest().GetProviderAttemptId(), call.ProviderAttemptID)
		assert.NotEqual(t, probeEvidence.GetResponse().GetTransactionId(), call.TransactionID)
	}

	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), strings.Join(report.GetFailureReasons(), "\n"))
}

func TestAgentEvidenceIntegration_FailedProbeLeavesAnswerOnlyCellEligible(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole:            true,
		suffix:                "tool-probe-answer-only",
		scenarioID:            "tool-select-file-read",
		additionalScenarioIDs: []string{"instruction-classify-severity"},
		toolProbeRequired:     true,
		replies: []fixtures.OllamaReply{
			{Content: `probe_echo {"message":"capability-probe-tool"}`},
			{Content: "ERROR"},
		},
	})
	require.Equal(t, 2, env.scheduledCount)

	unsupported := env.executeNext(t)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, unsupported.GetLifecycleStatus())
	assert.Contains(t, unsupported.GetFailureReason(), "capability_unsupported")

	answer := env.executeNext(t)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, answer.GetLifecycleStatus(), answer.GetInvalidEvidenceDetail())
	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, answer.GetAssignmentId())
	require.NoError(t, err)
	assert.Equal(t, "instruction-classify-severity", trace.ScenarioID)
	assert.Equal(t, 2, env.stack.Provider.ChatCount(), "the failed probe and answer-only turn each dispatch once")

	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), strings.Join(report.GetFailureReasons(), "\n"))
}
