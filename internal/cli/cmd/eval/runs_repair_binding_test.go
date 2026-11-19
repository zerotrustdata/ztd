// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// rewriteRunWithRetiredBinding stores runID's record as a binary before the
// harness roster wrote it, with ModelCampaignBinding field 7
// data_operator_session_id.
func rewriteRunWithRetiredBinding(t *testing.T, env *runEnv, runID string) {
	t.Helper()
	ctx := context.Background()
	run, err := env.store(t).LoadRun(ctx, runID)
	require.NoError(t, err)
	run.CampaignBinding.DataRoster = nil
	body, err := evalv1.MarshalCanonical(run)
	require.NoError(t, err)
	field := `"inference_operator_session_id":"` + run.GetCampaignBinding().GetInferenceOperatorSessionId() + `"`
	require.Contains(t, string(body), field)
	legacy := strings.Replace(string(body), field, field+`,"data_operator_session_id":"retired-data-session"`, 1)
	statePath := path.Join(constants.EvaluationRunsPath, runID, constants.EvaluationRunStateFilename)
	require.NoError(t, env.fileSvc(t).WriteFile(ctx, statePath, []byte(legacy), constants.PermFilePrivate))
}

func TestRunsRepair_RewritesRetiredBindingSoRunsListLoads(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	rewriteRunWithRetiredBinding(t, env, "run-a-1")

	_, err := env.run(t, "runs", "list")
	require.ErrorIs(t, err, constants.ErrEvaluationRunRetiredBindingField)
	assert.Contains(t, err.Error(), "run-a-1")

	assert.Contains(t, env.mustRun(t, "runs", "repair", "run-a-1"), "Removed the retired data_operator_session_id binding field from run run-a-1")
	assert.Contains(t, env.mustRun(t, "runs", "list"), "run-a-1")

	var payload runRepairJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "repair", "run-a-1"))
	assert.False(t, payload.BindingRewritten, "a repaired record needs no second rewrite")
}
