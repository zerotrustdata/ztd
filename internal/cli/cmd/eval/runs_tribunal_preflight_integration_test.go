// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestRunsStart_AllowsSharedRuntimeUnderTribunalConsensus(t *testing.T) {
	env, run := newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{Posture: constants.PostureConsensus, SharedRuntime: true})
	bindRunTribunalConsensus(t, run.stack.Gateway, run.stack.Identity)
	data, err := run.stack.Data.Workload()
	require.NoError(t, err)

	// One answer-only cell is enough to prove the shared runtime starts; the full default suite would execute every cell inline.
	suite := evaluation.DefaultScenarioSuite()
	suite.ID = "shared-tribunal-runtime-suite"
	suite.Version = evaluation.SuiteSchemaVersion
	for _, scenario := range evaluation.DefaultScenarioSuite().Scenarios {
		if scenario.ScenarioID == "instruction-classify-severity" {
			// The catalog scenario is eligible for primary and assistant; the fixture executes
			// one turn per repetition, so restrict it to the primary role.
			scenario.EligibleRoles = []string{"primary"}
			suite.Scenarios = []evaluation.SuiteScenario{scenario}
			break
		}
	}
	require.Len(t, suite.Scenarios, 1)
	require.NoError(t, evaluation.NewStore(env.fileSvc(t)).CreateSuite(context.Background(), suite))

	_, err = createCampaign(context.Background(), env.deps, env.fileSvc(t), campaignCreateSpec{
		Platform:    testPlatform,
		CampaignID:  "eval-shared-tribunal-runtime",
		Variants:    []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions: 1,
		SuiteID:     suite.ID,
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: "shared-tribunal-investigation",
			DataOperatorId:  data.OperatorID,
		},
		Judge: testJudgeVariant(),
	})
	require.NoError(t, err)

	_, err = runStartFlow(env.cmd, env.deps, runStartFlowOptions{
		CampaignID: "eval-shared-tribunal-runtime",
		RunID:      "run-shared-tribunal-runtime",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, run.stack.Provider.ChatCount(), "the first answer-only assignment executes without a Tribunal vote round")
	runIDs, err := evaluation.NewStore(env.fileSvc(t)).ListRunIDs(context.Background())
	require.NoError(t, err)
	assert.Contains(t, runIDs, "run-shared-tribunal-runtime")
}
