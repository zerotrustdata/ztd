// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestInvestigationGrading_LocalFilesystemChangesCannotReplaceSignedObservations(t *testing.T) {
	blueprints, err := InvestigationScenarioBlueprints()
	require.NoError(t, err)
	var bp ScenarioBlueprint
	for _, b := range blueprints {
		if b.ScenarioID == "wrong-port-config" {
			bp = b
			break
		}
	}
	require.NotEmpty(t, bp.ScenarioID)

	wsRoot := testutil.TempDir(t)
	// Materialize files for wrong-port-config in wsRoot
	for _, f := range bp.Input.WorkspaceFiles {
		fullPath := filepath.Join(wsRoot, f.RelPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), constants.PermDirStandard))
		require.NoError(t, os.WriteFile(fullPath, []byte(f.Content), constants.PermFileExecutable))
	}

	req := AssignmentExecutionRequest{
		Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-inv-fixed", RunId: "run-inv-fixed"},
		ScenarioInput: bp.Input,
		ScenarioGold:  bp.Gold,
		ScenarioTools: ScenarioToolExpectations{
			TrajectoryPolicy: bp.TrajectoryPolicy,
			AllowedTools:     bp.AllowedTools,
			ExpectedTools:    bp.ExpectedTools,
			ForbiddenTools:   bp.ForbiddenTools,
		},
	}

	ws := ScenarioWorkspace{Root: wsRoot, OperatorWorkingDirectory: wsRoot}
	trace := &AgentTrace{
		RunID:          "run-inv-fixed",
		AssignmentID:   "assign-inv-fixed",
		DesignatedRole: "primary",
		Workspace:      AgentTraceWorkspace(ws),
		ModelCalls: []AgentModelCall{
			{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-turn", ResultDigest: "digest-turn"},
		},
		ToolCalls: []AgentToolCall{
			{ToolName: constants.MCPToolReadFile, Success: true},
		},
		FinalOutput: "The inventory-api service port is fixed to 8080 configuration.",
	}

	// 1. Before fixing the planted fault: file-state and governed-probe should fail.
	gradingUnfixed, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)
	assert.True(t, gradePassed(t, gradingUnfixed.DeterministicGrades, "trajectory"))
	assert.True(t, gradePassed(t, gradingUnfixed.DeterministicGrades, "required-evidence:tool_call"))
	assert.False(t, gradePassed(t, gradingUnfixed.DeterministicGrades, "file-state"))
	assert.False(t, gradePassed(t, gradingUnfixed.DeterministicGrades, "governed-probe"))
	assert.False(t, gradePassed(t, gradingUnfixed.DeterministicGrades, "primary-responsibility"))

	// 2. Fix the planted fault: update app/service.conf to listen_port = 8080.
	serviceConf := filepath.Join(wsRoot, "app", "service.conf")
	fixedContent := "# service configuration\nname = inventory-api\nlisten_port = 8080\nlog_file = app/logs/service.log\n"
	require.NoError(t, os.WriteFile(serviceConf, []byte(fixedContent), constants.PermFilePublic))

	// Host-side writes alter the fixture but do not create native Operator
	// observations. Grading must still reject the file state and probe.
	gradingFixed, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)
	assert.Empty(t, trace.Observations, "local filesystem changes do not create governed evidence")
	assert.False(t, gradePassed(t, gradingFixed.DeterministicGrades, "file-state"))
	assert.False(t, gradePassed(t, gradingFixed.DeterministicGrades, "governed-probe"))
	assert.False(t, gradePassed(t, gradingFixed.DeterministicGrades, "primary-responsibility"))
}
