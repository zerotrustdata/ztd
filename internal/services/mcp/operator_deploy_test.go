// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorDeployTool_Name(t *testing.T) {
	tool := &OperatorDeployTool{}
	require.Equal(t, constants.MCPToolOperatorDeploy, tool.Name())
}

func TestOperatorDeployTool_Description(t *testing.T) {
	tool := &OperatorDeployTool{}
	require.NotEmpty(t, tool.Description())
	require.Contains(t, tool.Description(), "operator")
	require.Contains(t, tool.Description(), "human")
}

func TestOperatorDeployTool_InputSchema(t *testing.T) {
	tool := &OperatorDeployTool{}
	schema := tool.InputSchema()

	require.Equal(t, "object", schema.Type)
	require.Equal(t, []string{"hostnames"}, schema.Required)

	require.Len(t, schema.Properties, 1)
	require.Contains(t, schema.Properties, "hostnames")
	require.Equal(t, "array", schema.Properties["hostnames"].Type)
}

func TestOperatorDeployTool_ActionType(t *testing.T) {
	tool := governedTool(t, constants.MCPToolOperatorDeploy)
	assert.Equal(t, constants.ActionTypeOperatorDeploy, tool.ActionType())
}

func TestOperatorDeployTool_FailClosedAsMCPCall(t *testing.T) {
	tool := governedTool(t, constants.MCPToolOperatorDeploy)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"hostnames":["localhost"]}`))
	require.ErrorIs(t, err, constants.ErrMCPToolGovernedAction)
}

func TestOperatorDeployTool_GovernedAction(t *testing.T) {
	tool := governedTool(t, constants.MCPToolOperatorDeploy)
	binding := GovernedActionBinding{OperatorSessionID: "session-1", ExecutionID: "exec-deploy-1"}

	req, err := tool.GovernedAction(json.RawMessage(`{"hostnames":["worker-1","worker-2"]}`), binding)
	require.NoError(t, err)
	assert.Equal(t, constants.ActionTypeOperatorDeploy, req.ActionType)
	assert.Equal(t, constants.EventOperatorDeployRequested, req.EventType)
	assert.Equal(t, "worker-1,worker-2", req.TargetResource)
	assert.True(t, req.ActionType.ChangesState(req.TargetResource))

	want := &operatorv1.OperatorDeployRequested{
		ExecutionId: "exec-deploy-1",
		Hostnames:   []string{"worker-1", "worker-2"},
	}
	assert.True(t, proto.Equal(want, req.Payload), "got payload %v", req.Payload)
	_, err = req.MarshalPayload()
	require.NoError(t, err)
}

func TestOperatorDeployTool_GovernedAction_Validation(t *testing.T) {
	tool := governedTool(t, constants.MCPToolOperatorDeploy)
	binding := GovernedActionBinding{OperatorSessionID: "session-1", ExecutionID: "exec-deploy-1"}

	tests := []struct {
		name    string
		args    string
		wantErr error
	}{
		{
			name:    "empty hostnames",
			args:    `{"hostnames":[]}`,
			wantErr: constants.ErrMCPValidateHostnamesEmpty,
		},
		{
			name:    "missing hostnames",
			args:    `{}`,
			wantErr: constants.ErrMCPValidateHostnamesEmpty,
		},
		{
			name:    "empty hostname element",
			args:    `{"hostnames":["localhost",""]}`,
			wantErr: constants.ErrMCPValidateHostnameEmpty,
		},
		{
			name:    "whitespace hostname",
			args:    `{"hostnames":[" localhost"]}`,
			wantErr: constants.ErrMCPValidateHostnameWhitespace,
		},
		{
			name:    "null byte hostname",
			args:    "{\"hostnames\":[\"host\\u0000name\"]}",
			wantErr: constants.ErrMCPValidateHostnameNullBytes,
		},
		{
			name:    "shell injection hostname",
			args:    `{"hostnames":["host; rm -rf /"]}`,
			wantErr: constants.ErrMCPValidateHostnameDangerousChar,
		},
		{
			name:    "leading dash hostname",
			args:    `{"hostnames":["-oProxyCommand=evil"]}`,
			wantErr: constants.ErrGatewayInvalidToolArguments,
		},
		{
			name:    "duplicate hostnames",
			args:    `{"hostnames":["host1","host1"]}`,
			wantErr: constants.ErrGatewayInvalidToolArguments,
		},
		{
			name:    "unknown field operator_binary rejected",
			args:    `{"hostnames":["localhost"],"operator_binary":"/bin/sh"}`,
			wantErr: constants.ErrGatewayInvalidToolArguments,
		},
		{
			name:    "unknown field operator_args rejected",
			args:    `{"hostnames":["localhost"],"operator_args":["-c","id"]}`,
			wantErr: constants.ErrGatewayInvalidToolArguments,
		},
		{
			name:    "unknown field timeout rejected",
			args:    `{"hostnames":["localhost"],"timeout":30}`,
			wantErr: constants.ErrGatewayInvalidToolArguments,
		},
		{
			name:    "trailing JSON content rejected",
			args:    `{"hostnames":["localhost"]} {}`,
			wantErr: constants.ErrGatewayInvalidToolArguments,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.GovernedAction(json.RawMessage(tt.args), binding)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
