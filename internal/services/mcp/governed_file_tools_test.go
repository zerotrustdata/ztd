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

func governedTool(t *testing.T, name string) GovernedActionTool {
	t.Helper()
	registry := NewToolRegistry()
	require.NoError(t, RegisterNativeTools(registry))
	tool, ok := registry.Get(name)
	require.True(t, ok, "tool %s is not registered", name)
	governed, ok := tool.(GovernedActionTool)
	require.True(t, ok, "tool %s is not a governed action tool", name)
	return governed
}

var testBinding = GovernedActionBinding{OperatorSessionID: "session-verified", ExecutionID: "exec-1"}

// Each model-facing file tool is its existing typed action, and only the two
// mutations change state, so L2 and L3 gate them under every posture that
// requires those layers.
func TestGovernedFileTools_MapToTheirTypedActions(t *testing.T) {
	cases := []struct {
		tool         string
		args         string
		action       constants.ActionType
		changesState bool
	}{
		{constants.MCPToolListDirectory, `{"path":"/srv/app"}`, constants.ActionTypeFsList, false},
		{constants.MCPToolFileEdit, `{"file_path":"/srv/app/main.go","operation":"replace","old_content":"a","new_content":"b","justification":"fix"}`, constants.ActionTypeFileEdit, true},
		{constants.MCPToolFileHistory, `{"file_path":"/srv/app/main.go"}`, constants.ActionTypeFetchFileHistory, false},
		{constants.MCPToolFileDiff, `{"file_path":"/srv/app/main.go"}`, constants.ActionTypeFetchFileDiff, false},
		{constants.MCPToolRestoreFile, `{"file_path":"/srv/app/main.go","commit_hash":"abc123"}`, constants.ActionTypeRestoreFile, true},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			request, err := governedTool(t, tc.tool).GovernedAction(json.RawMessage(tc.args), testBinding)
			require.NoError(t, err)
			assert.Equal(t, tc.action, request.ActionType)
			wantEvent, err := constants.RequestEventForAction(tc.action)
			require.NoError(t, err)
			assert.Equal(t, wantEvent, request.EventType)
			assert.Equal(t, tc.changesState, request.ActionType.ChangesState(request.TargetResource))
			_, err = request.MarshalPayload()
			require.NoError(t, err)
		})
	}
}

// Reached as an MCP_CALL, a governed action tool refuses: an MCP_CALL wrapper
// must never skip the typed action's L1 checks, path policy or ledger.
func TestGovernedFileTools_FailClosedAsMCPCall(t *testing.T) {
	for _, name := range []string{constants.MCPToolListDirectory, constants.MCPToolFileEdit, constants.MCPToolFileHistory, constants.MCPToolFileDiff, constants.MCPToolRestoreFile} {
		_, err := governedTool(t, name).Execute(context.Background(), json.RawMessage(`{}`))
		require.ErrorIs(t, err, constants.ErrMCPToolGovernedAction, name)
	}
}

func TestFileEditTool_BuildsTypedFileEdit(t *testing.T) {
	tool := governedTool(t, constants.MCPToolFileEdit)
	request, err := tool.GovernedAction(json.RawMessage(`{"file_path":"/srv/app/config.yaml","operation":"write","content":"tls_verify: true\n","justification":"re-enable TLS verification"}`), testBinding)
	require.NoError(t, err)
	assert.Equal(t, "/srv/app/config.yaml", request.TargetResource)
	want := &operatorv1.FileEditRequested{
		FilePath:        "/srv/app/config.yaml",
		Operation:       string(constants.FileOperationWrite),
		ExecutionId:     "exec-1",
		Justification:   "re-enable TLS verification",
		Content:         "tls_verify: true\n",
		CreateIfMissing: true,
	}
	assert.True(t, proto.Equal(want, request.Payload), "payload = %v", request.Payload)
}

func TestFileEditTool_RejectsWhatTheModelMayNotRequest(t *testing.T) {
	tool := governedTool(t, constants.MCPToolFileEdit)
	cases := map[string]struct {
		args string
		want error
	}{
		"read stays on read_file":       {`{"file_path":"/a","operation":"read","justification":"x"}`, constants.ErrFileEditUnsupportedOperation},
		"justification is required":     {`{"file_path":"/a","operation":"write","content":"x"}`, constants.ErrMissingRequiredField},
		"backup is the ledger's job":    {`{"file_path":"/a","operation":"write","content":"x","justification":"x","create_backup":true}`, constants.ErrGatewayInvalidToolArguments},
		"trailing content is rejected":  {`{"file_path":"/a","operation":"write","content":"x","justification":"x"} {}`, constants.ErrGatewayInvalidToolArguments},
		"file_path is required":         {`{"operation":"write","content":"x","justification":"x"}`, constants.ErrMissingRequiredField},
		"operation is required":         {`{"file_path":"/a","content":"x","justification":"x"}`, constants.ErrMissingRequiredField},
		"unknown operation is rejected": {`{"file_path":"/a","operation":"chmod","justification":"x"}`, constants.ErrFileEditUnsupportedOperation},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tool.GovernedAction(json.RawMessage(tc.args), testBinding)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// The ledger tools read only the verified session's ledger. The platform binds
// that session; a model cannot name another one.
func TestLedgerTools_BindThePlatformSessionOnly(t *testing.T) {
	cases := []struct {
		tool    string
		args    string
		session func(proto.Message) string
	}{
		{constants.MCPToolFileHistory, `{"file_path":"/srv/app/main.go"}`, func(m proto.Message) string {
			return m.(*operatorv1.FetchFileHistoryRequested).GetOperatorSessionId()
		}},
		{constants.MCPToolFileDiff, `{"diff_id":"7"}`, func(m proto.Message) string {
			return m.(*operatorv1.FetchFileDiffRequested).GetOperatorSessionId()
		}},
		{constants.MCPToolRestoreFile, `{"file_path":"/srv/app/main.go","commit_hash":"abc123"}`, func(m proto.Message) string {
			return m.(*operatorv1.RestoreFileRequested).GetOperatorSessionId()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			tool := governedTool(t, tc.tool)
			request, err := tool.GovernedAction(json.RawMessage(tc.args), testBinding)
			require.NoError(t, err)
			assert.Equal(t, testBinding.OperatorSessionID, tc.session(request.Payload))

			var withSession map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.args), &withSession))
			withSession["operator_session_id"] = "session-other"
			forged, err := json.Marshal(withSession)
			require.NoError(t, err)
			_, err = tool.GovernedAction(forged, testBinding)
			require.ErrorIs(t, err, constants.ErrGatewayInvalidToolArguments)

			_, err = tool.GovernedAction(json.RawMessage(tc.args), GovernedActionBinding{ExecutionID: "exec-1"})
			require.ErrorIs(t, err, constants.ErrMCPToolSessionRequired)
		})
	}
}

func TestFileDiffTool_RequiresAFileOrADiff(t *testing.T) {
	_, err := governedTool(t, constants.MCPToolFileDiff).GovernedAction(json.RawMessage(`{}`), testBinding)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}
