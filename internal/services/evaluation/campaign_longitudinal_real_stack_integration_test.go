// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// Real-stack longitudinal cases: sequential runs that share a persistent
// investigation, a fresh-investigation control, and the planted-fault
// investigation scenarios. The Data Operator answers audit_receipt_list from its
// own ledger and runs the workspace's reads and shell commands; only the model
// provider is scripted. Rig: agent_evidence_real_stack_integration_test.go.
package evaluation_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

const (
	longitudinalScenarioID      = "wrong-port-config"
	longitudinalInvestigationID = "inv-persistent-wrong-port"
	longitudinalDiagnosis       = "Diagnosis: upstream health check expects port 8080, but inventory-api is configured with listen_port = 8081 in app/service.conf. Changing listen_port to 8080 resolves the issue."
	longitudinalControlAnswer   = "Diagnosis: upstream health check expects port 8080, but inventory-api is configured with listen_port = 8081 in app/service.conf."
)

// readFileOn is a provider turn that reads one workspace-relative fixture through
// read_file. env is bound after the rig starts, so the turn resolves the attempt
// in flight when the provider is asked.
func readFileOn(env **realStackAgentRun, relPath string) fixtures.OllamaReply {
	return fixtures.OllamaReply{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
		return realStackReadFileReply(*env, relPath)
	}}
}

// shellScriptOn is a provider turn that proposes running one workspace-relative
// script with sh, from the attempt in flight, followed by the Tribunal rounds
// that author it: investigation scenarios route run_shell_command through
// Tribunal, so five matching candidates, the risk check, and the audit approve
// the proposed command.
func shellScriptOn(env **realStackAgentRun, relPath string) []fixtures.OllamaReply {
	script := func(reply func(json.RawMessage) fixtures.OllamaReply) fixtures.OllamaReply {
		return fixtures.OllamaReply{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
			path, err := (*env).inFlightWorkspaceFile(relPath)
			if err != nil {
				return fixtures.OllamaReply{Failure: err.Error()}
			}
			return reply(realStackShellArguments("sh", path))
		}}
	}
	proposal := script(func(arguments json.RawMessage) fixtures.OllamaReply {
		return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: arguments}}}
	})
	candidate := script(func(arguments json.RawMessage) fixtures.OllamaReply {
		return fixtures.OllamaReply{Content: string(arguments)}
	})
	return slices.Concat([]fixtures.OllamaReply{proposal},
		slices.Repeat([]fixtures.OllamaReply{candidate}, 5),
		tribunalTurn(tribunalRealLowRisk), tribunalTurn(tribunalRealApprove))
}

// longitudinalPersistent binds a campaign to one persistent investigation on the
// Data Operator the rig resolved, before the spec is frozen.
func longitudinalPersistent(req *evaluation.CampaignInitRequest, dataOperatorID string) {
	req.Investigation = &evalv1.EvaluationInvestigation{
		Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
		InvestigationId: longitudinalInvestigationID,
		DataOperatorId:  dataOperatorID,
	}
}

// longitudinalFresh binds a campaign to a fresh investigation, the control.
func longitudinalFresh(req *evaluation.CampaignInitRequest, _ string) {
	req.Investigation = &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH}
}

// investigationScores maps one assignment's decomposed scores by dimension.
func investigationScores(res *evalv1.EvaluationAssignmentResult) map[string]float64 {
	scores := make(map[string]float64)
	for _, score := range res.GetDecomposedScores() {
		scores[score.GetDimension()] = score.GetValue()
	}
	return scores
}

func TestCampaignLongitudinal_ImprovementCurveAcrossSequentialRuns(t *testing.T) {
	var env *realStackAgentRun
	// Run 1 reads the config and log, runs the check script, then diagnoses.
	run1 := slices.Concat(
		[]fixtures.OllamaReply{readFileOn(&env, "app/service.conf"), readFileOn(&env, "app/logs/service.log")},
		shellScriptOn(&env, "check.sh"),
		[]fixtures.OllamaReply{{Content: longitudinalDiagnosis}},
	)
	// Run 2 on the same persistent investigation queries its history and answers.
	run2 := []fixtures.OllamaReply{
		liveContextAuditList(),
		{Content: "Diagnosis: based on prior investigation history, inventory-api listens on 8081 instead of expected port 8080. Update app/service.conf to listen_port = 8080."},
	}
	// The control is a fresh investigation: its history is empty, so it reads.
	control := slices.Concat(
		[]fixtures.OllamaReply{liveContextAuditList(), readFileOn(&env, "app/service.conf"), readFileOn(&env, "app/logs/service.log")},
		shellScriptOn(&env, "check.sh"),
		[]fixtures.OllamaReply{{Content: longitudinalControlAnswer}},
	)
	replies := slices.Concat(run1, run2, control)

	env = newRealStackAgentRun(t, realStackOptions{
		catalog: evaluation.LoadInvestigationsScenarioCatalog, scenarioID: longitudinalScenarioID,
		singleRole: true, suffix: "curve-1", scenario: liveContextSurface,
		request: longitudinalPersistent, replies: replies,
	})

	res1 := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res1.GetLifecycleStatus(), "invalid evidence: %s", res1.GetInvalidEvidenceDetail())
	scores1 := investigationScores(res1)
	steps1 := scores1[evaluation.MetricStepsToDiagnosis]
	require.InDelta(t, 7.0, steps1, 0, "run 1 should take 7 steps (4 model turns + 3 tool calls)")
	require.InDelta(t, 1.0, scores1[evaluation.MetricDiagnosisCorrectness], 0)
	require.InDelta(t, 1.0, scores1[evaluation.MetricEvidenceBackedConclusion], 0)

	// Run 2 is a new run on the same persistent investigation and Data Operator.
	env.startRun(t, realStackRunPlan{
		campaignID: realStackCampaignID, runID: "run-seq-2", singleRole: true, request: longitudinalPersistent,
	})
	res2 := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res2.GetLifecycleStatus(), "invalid evidence: %s", res2.GetInvalidEvidenceDetail())
	scores2 := investigationScores(res2)
	steps2 := scores2[evaluation.MetricStepsToDiagnosis]
	require.InDelta(t, 3.0, steps2, 0, "run 2 should take 3 steps (2 model turns + 1 context query call)")
	require.InDelta(t, 1.0, scores2[evaluation.MetricDiagnosisCorrectness], 0)
	require.InDelta(t, 1.0, scores2[evaluation.MetricEvidenceBackedConclusion], 0)
	require.InDelta(t, 1.0, scores2[evaluation.MetricRetrievalHitRate], 0, "retrieval hit rate should be 100% from prior history")
	require.InDelta(t, 0.0, scores2[evaluation.MetricRedundantCommandRate], 0, "no redundant commands executed")
	assert.Less(t, steps2, steps1, "sequential run on persistent investigation should achieve diagnosis in fewer steps than initial run")

	// The control is its own campaign on a fresh investigation.
	env.startRun(t, realStackRunPlan{
		campaignID: realStackCampaignID + "-fresh", runID: "run-control-2", singleRole: true, request: longitudinalFresh,
	})
	resCtrl := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, resCtrl.GetLifecycleStatus(), "invalid evidence: %s", resCtrl.GetInvalidEvidenceDetail())
	stepsCtrl := investigationScores(resCtrl)[evaluation.MetricStepsToDiagnosis]
	require.InDelta(t, 9.0, stepsCtrl, 0, "control run without persistent context must take extra steps")
	assert.Less(t, steps2, stepsCtrl, "persistent investigation with context retrieval must require fewer steps than fresh-investigation control")
}

func TestCampaignLongitudinal_PlantedFaultFixturesEvaluateAcrossAllThreeScenarios(t *testing.T) {
	blueprints, err := evaluation.InvestigationScenarioBlueprints()
	require.NoError(t, err)
	require.Len(t, blueprints, 3)

	testCases := []struct {
		scenarioID    string
		readFile      string
		diagnosisText string
	}{
		{
			scenarioID:    "wrong-port-config",
			readFile:      "app/logs/service.log",
			diagnosisText: "Diagnosis: port configuration mismatch. Upstream expects port 8080.",
		},
		{
			scenarioID:    "wrong-path-check-script",
			readFile:      "conf/settings.json",
			diagnosisText: "Diagnosis: check script references stale path; settings file is located at conf/settings.json.",
		},
		{
			scenarioID:    "cron-disk-fill",
			readFile:      "cron/app.cron",
			diagnosisText: "Diagnosis: disk filled by cron job app-dump every minute. Remove the app-dump entry from cron.",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.scenarioID, func(t *testing.T) {
			var env *realStackAgentRun
			env = newRealStackAgentRun(t, realStackOptions{
				catalog: evaluation.LoadInvestigationsScenarioCatalog, scenarioID: tc.scenarioID,
				singleRole: true, suffix: tc.scenarioID,
				replies: []fixtures.OllamaReply{readFileOn(&env, tc.readFile), {Content: tc.diagnosisText}},
			})

			res := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

			scores := investigationScores(res)
			assert.InDelta(t, 1.0, scores[evaluation.MetricDiagnosisCorrectness], 0, "diagnosis correctness should pass")
			assert.InDelta(t, 1.0, scores[evaluation.MetricEvidenceBackedConclusion], 0, "conclusion must be evidence backed")
			assert.GreaterOrEqual(t, scores[evaluation.MetricRetrievalHitRate], 0.0)
			assert.InDelta(t, 3.0, scores[evaluation.MetricStepsToDiagnosis], 0)
		})
	}
}
