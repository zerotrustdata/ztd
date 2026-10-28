// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestNamedRollout_RealStackStartResumeAndRerun(t *testing.T) {
	env, stack := newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{
		Posture: constants.PostureDoctrine, EnablePublicSpectator: true, Provenance: true,
		ModelTags: []string{evaluation.DefaultJudgeServedModelTag},
	})
	previousOrigin := gwremote.PublicMirrorOrigin
	gwremote.PublicMirrorOrigin = stack.stack.Gateway.PublicMirrorOrigin
	t.Cleanup(func() { gwremote.PublicMirrorOrigin = previousOrigin })
	variant := testQwenVariant()
	variant.ModelDigest = fixtures.ScriptedModelDigest(t, variant.ServedModelTag)
	writeTestFrozenInventory(t, env.root, variant, scriptedJudgeVariant(t))
	suite := evaluation.DefaultScenarioSuite()
	suite.ID = "rollout-answer-suite"
	for _, scenario := range suite.Scenarios {
		if scenario.ScenarioID == "instruction-exact-format" {
			suite.Scenarios = []evaluation.SuiteScenario{scenario}
			break
		}
	}
	require.Len(t, suite.Scenarios, 1)
	require.NoError(t, env.store(t).CreateSuite(stack.ctx, suite))
	env.mustRun(t, "campaigns", "create", "control", variant.ServedModelTag, "--suite", suite.ID)
	env.mustRun(t, "campaigns", "create", "longitudinal", variant.ServedModelTag, "--suite", suite.ID, "--condition", "longitudinal", "--data-operator", env.dataOperatorID)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal")
	var result evaluation.NamedRollout
	require.NoError(t, env.runJSON(t, &result, "rollout", "run", "nightly", "--until", "1"))
	require.Len(t, result.Executions, 1)
	assert.Equal(t, evaluation.RolloutStepSucceeded, result.Executions[0].Steps[0].Status)
	assert.Equal(t, evaluation.RolloutStepPending, result.Executions[0].Steps[1].Status)
	assert.Equal(t, 1, stack.stack.Provider.ChatCount())

	// Model a process that stopped just after preparing its next campaign run.
	// Resume must execute that run, preserving the run ID and strict policies.
	const stoppedRunID = "longitudinal-stopped"
	_, err := runStartFlow(env.cmd, env.deps, runStartFlowOptions{
		CampaignID: "longitudinal", RunID: stoppedRunID, PrepareOnly: true,
		Publish: true, RequireObservation: true, RequireProvenance: true,
	})
	require.NoError(t, err)
	step := &result.Executions[0].Steps[1]
	step.Status = evaluation.RolloutStepStopped
	step.Attempts = []evaluation.RolloutAttempt{{RunID: stoppedRunID}}
	require.NoError(t, evaluation.SaveNamedRollout(stack.ctx, env.fileSvc(t), &result))
	require.NoError(t, env.runJSON(t, &result, "rollout", "resume", "nightly"))
	assert.Equal(t, 2, stack.stack.Provider.ChatCount(), "successful campaigns are skipped on resume")
	assert.Equal(t, stoppedRunID, result.Executions[0].Steps[1].Attempts[0].RunID)

	require.NoError(t, env.runJSON(t, &result, "rollout", "run", "nightly"))
	require.Len(t, result.Executions, 2)
	assert.Equal(t, 4, stack.stack.Provider.ChatCount(), "run by name starts the entire sequence afresh")
	for _, execution := range result.Executions {
		for _, step := range execution.Steps {
			assert.Equal(t, evaluation.RolloutStepSucceeded, step.Status)
			run, err := env.store(t).LoadRun(stack.ctx, step.Attempts[0].RunID)
			require.NoError(t, err)
			assert.Equal(t, evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT, run.GetProviderObservationPolicy())
			assert.Equal(t, evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT, run.GetModelProvenancePolicy())
		}
	}
}
