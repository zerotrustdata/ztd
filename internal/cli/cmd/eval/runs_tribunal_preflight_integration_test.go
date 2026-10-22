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
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestRunsStart_RefusesSharedRuntimeUnderTribunalConsensusBeforePersistingRun(t *testing.T) {
	env, run := newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{Posture: constants.PostureConsensus, SharedRuntime: true})
	bindRunTribunalConsensus(t, run.stack.Gateway, run.stack.Identity)
	governancePosture, err := cases.WirePosture(constants.PostureConsensus)
	require.NoError(t, err)
	data, err := run.stack.Data.Workload()
	require.NoError(t, err)
	_, err = createCampaign(context.Background(), env.deps, env.fileSvc(t), campaignCreateSpec{
		GovernancePosture: governancePosture,
		Platform:          testPlatform,
		CampaignID:        "eval-shared-tribunal-runtime",
		Variants:          []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions:       1,
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: "shared-tribunal-investigation",
			DataOperatorId:  data.OperatorID,
		},
	})
	require.NoError(t, err)

	_, err = runStartFlow(env.cmd, env.deps, runStartFlowOptions{
		CampaignID: "eval-shared-tribunal-runtime",
		RunID:      "run-shared-tribunal-runtime",
	})
	require.ErrorIs(t, err, constants.ErrEvaluationSharedRuntimeTribunalL2)
	assert.Zero(t, run.stack.Provider.ChatCount(), "preflight must refuse before Tribunal inference")
	runIDs, err := evaluation.NewStore(env.fileSvc(t)).ListRunIDs(context.Background())
	require.NoError(t, err)
	assert.Empty(t, runIDs, "preflight must refuse before persisting a run")
}
