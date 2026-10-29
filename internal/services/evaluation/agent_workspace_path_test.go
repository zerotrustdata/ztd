// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

const workspacePathTestOperatorDir = "/srv/ztd-operator"

func workspacePathTestWorkspace(t *testing.T) ScenarioWorkspace {
	t.Helper()
	workspace, err := NewScenarioWorkspace(workspacePathTestOperatorDir, "run-1", "attempt-1")
	require.NoError(t, err)
	return workspace
}

// workspacePathCases are schema-valid file-tool arguments a model can send.
// Scored cells read only their own attempt workspace; a path that resolves
// anywhere else, including the Operator's root or another attempt, is out of
// scope.
func workspacePathCases(root string) []struct {
	name    string
	tool    string
	args    string
	inScope bool
} {
	return []struct {
		name    string
		tool    string
		args    string
		inScope bool
	}{
		{"read inside the workspace", constants.MCPToolReadFile, `{"path":"` + root + `/logs/app.log"}`, true},
		{"search the workspace root", constants.MCPToolSearchDirectory, `{"path":"` + root + `","pattern":"ERROR"}`, true},
		{"search one workspace file", constants.MCPToolSearchFile, `{"path":"` + root + `/logs/app.log","pattern":"ERROR"}`, true},
		{"read a missing workspace file", constants.MCPToolReadFile, `{"path":"` + root + `/conf/missing.json"}`, true},
		{"read the host password file", constants.MCPToolReadFile, `{"path":"/etc/passwd"}`, false},
		{"search the filesystem root", constants.MCPToolSearchDirectory, `{"path":"/","pattern":"AUTH_FAILURE"}`, false},
		{"search the operator working directory", constants.MCPToolSearchDirectory, `{"path":"` + workspacePathTestOperatorDir + `","pattern":"ERROR"}`, false},
		{"relative path resolves against the operator directory", constants.MCPToolReadFile, `{"path":"logs/app.log"}`, false},
		{"traversal out of the workspace", constants.MCPToolSearchFile, `{"path":"` + root + `/../ws-0000000000000000/a.log","pattern":"x"}`, false},
		{"sibling with the workspace as a name prefix", constants.MCPToolReadFile, `{"path":"` + root + `-other/a.log"}`, false},
	}
}

func workspacePathRequest(t *testing.T, tool, raw string) agentToolRequest {
	t.Helper()
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(tool)
	require.True(t, ok)
	request := decodeAgentToolRequest(spec, true, raw)
	require.Nil(t, request.Failure, "case arguments must satisfy the declared schema")
	return request
}

func TestWorkspacePathFailure_ConfinesFileToolsToTheAttemptWorkspace(t *testing.T) {
	workspace := workspacePathTestWorkspace(t)
	for _, tt := range workspacePathCases(workspace.Root) {
		t.Run(tt.name, func(t *testing.T) {
			args := workspacePathRequest(t, tt.tool, tt.args).Arguments
			failure := args.WorkspacePathFailure(workspace)
			if tt.inScope {
				assert.Nil(t, failure)
				return
			}
			require.NotNil(t, failure)
			assert.Equal(t, agentToolErrorExecution, failure.ErrorType)
			assert.Contains(t, failure.Error, tt.tool)
			assert.Contains(t, failure.Error, "path")
			assert.Contains(t, failure.Error, workspace.Root, "the failure names the workspace the model may use")
		})
	}
}

// Other native tools carry no file path and are not confined here.
func TestWorkspacePathFailure_IgnoresToolsWithoutAPath(t *testing.T) {
	args := workspacePathRequest(t, constants.MCPToolEventReceiptList, `{}`).Arguments
	assert.Nil(t, args.WorkspacePathFailure(workspacePathTestWorkspace(t)))
}

// An out-of-workspace path is a model-visible tool failure recorded before any
// governed dispatch: the run has no executor here, so reaching dispatch panics.
func TestRunNativeMCPTool_RejectsOutOfWorkspacePathBeforeDispatch(t *testing.T) {
	workspace := workspacePathTestWorkspace(t)
	run := &agentRun{trace: &AgentTrace{}, workspace: workspace}
	for _, tt := range workspacePathCases(workspace.Root) {
		if tt.inScope {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			request := workspacePathRequest(t, tt.tool, tt.args)
			record := AgentToolCall{ToolName: tt.tool, ArgumentsJSON: request.ArgumentsJSON}

			var completed AgentToolCall
			var err error
			require.NotPanics(t, func() {
				completed, err = run.runNativeMCPTool(context.Background(), record, request)
			})
			require.NoError(t, err)
			assert.False(t, completed.Success)
			assert.Nil(t, completed.Receipt)
			assert.Empty(t, completed.ActionType)
			assert.Equal(t, agentToolErrorExecution, completed.ErrorType)
			assert.Contains(t, completed.ResultJSON, workspace.Root)
		})
	}
}
