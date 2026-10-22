// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorRunCmdWithConfig_Success(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	saveTestCredentials(t, fileSvc, cfg, "user-001")

	sessionA := "4881d566-90a9-44c9-9e3e-c6bb51e07f5c"
	sessionB := "286d7a56-b961-4cff-9c69-063a84b69afd"

	listBody, err := json.Marshal(models.OperatorSlotResponse{
		Success: true,
		Operators: []*operatorv1.OperatorDocument{
			{Id: "op-a", OperatorSessionId: sessionA, OperatorType: string(constants.OperatorTypeRemote), Status: string(constants.OperatorStatusActive)},
			{Id: "op-b", OperatorSessionId: sessionB, OperatorType: string(constants.OperatorTypeRemote), Status: string(constants.OperatorStatusActive)},
		},
	})
	require.NoError(t, err)

	mockClient := &cmdtest.MockAPIClient{GetResp: listBody, PostResp: mustMarshalDispatchSuccess(t, "welcome to host-a")}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	cmd := operatorRunCmdWithConfig(
		loader,
		func(_ fs.RuntimeFileService, _ *config.Config, _ api.ClientOptions) (authcmd.APIClient, error) {
			return mockClient, nil
		},
		cmdtest.FileSvcFactoryFor(fileSvc),
	)
	cmd.SetArgs([]string{sessionA, sessionB, "--cmd", "echo 'welcome to ' $(hostname)"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.Execute())
	assert.Len(t, mockClient.PostCalls, 2)
	assert.Contains(t, buf.String(), "exit: 0")
	assert.Contains(t, buf.String(), "welcome to host-a")
}

func TestOperatorRunCmdWithConfig_OperatorNotFound(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	saveTestCredentials(t, fileSvc, cfg, "user-001")

	listBody, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{}})
	require.NoError(t, err)

	loader := func(string) (*config.Config, error) { return cfg, nil }
	cmd := operatorRunCmdWithConfig(
		loader,
		func(_ fs.RuntimeFileService, _ *config.Config, _ api.ClientOptions) (authcmd.APIClient, error) {
			return &cmdtest.MockAPIClient{GetResp: listBody}, nil
		},
		cmdtest.FileSvcFactoryFor(fileSvc),
	)
	cmd.SetArgs([]string{"4881d566-90a9-44c9-9e3e-c6bb51e07f5c", "--cmd", "echo hi"})

	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no operator found with session id")
}

func TestOperatorStopCmdWithConfig_SendsTargetedShutdown(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	saveTestCredentials(t, fileSvc, cfg, "user-001")

	sessionID := "4881d566-90a9-44c9-9e3e-c6bb51e07f5c"
	listBody, err := json.Marshal(models.OperatorSlotResponse{
		Success: true,
		Operators: []*operatorv1.OperatorDocument{{
			Id: "op-a", OperatorSessionId: sessionID, OperatorType: string(constants.OperatorTypeRemote), Status: string(constants.OperatorStatusActive),
		}},
	})
	require.NoError(t, err)
	stopBody, err := json.Marshal(models.StopOperatorResponse{Success: true, OperatorID: "op-a", OperatorSessionID: sessionID})
	require.NoError(t, err)
	mockClient := &cmdtest.MockAPIClient{GetResp: listBody, PostResp: stopBody}

	cmd := operatorStopCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), authcmd.MockClientFactory(mockClient), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs([]string{sessionID, "--reason", "maintenance"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.Execute())
	require.Len(t, mockClient.PostCalls, 1)
	assert.Equal(t, constants.APIPaths.OperatorsStop, mockClient.PostCalls[0].Path)
	request := mockClient.PostCalls[0].Body.(models.StopOperatorRequest)
	assert.Equal(t, sessionID, request.OperatorSessionID)
	assert.Equal(t, "maintenance", request.Reason)
	assert.Contains(t, buf.String(), "Stop requested")
}

func TestOperatorStopCmdWithConfig_RejectsEmbeddedOperator(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	saveTestCredentials(t, fileSvc, cfg, "user-001")

	listBody, err := json.Marshal(models.OperatorSlotResponse{
		Success: true,
		Operators: []*operatorv1.OperatorDocument{{
			Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: "embedded-session", OperatorType: string(constants.OperatorTypeEmbedded), Status: string(constants.OperatorStatusActive),
		}},
	})
	require.NoError(t, err)
	mockClient := &cmdtest.MockAPIClient{GetResp: listBody}
	cmd := operatorStopCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), authcmd.MockClientFactory(mockClient), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs([]string{"embedded-session"})

	err = cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrOperatorStopEmbedded)
	assert.Empty(t, mockClient.PostCalls)
}

func TestDedupeOperatorSessionIDs(t *testing.T) {
	ids := dedupeOperatorSessionIDs([]string{
		"4881d566-90a9-44c9-9e3e-c6bb51e07f5c",
		"4881d566-90a9-44c9-9e3e-c6bb51e07f5c",
		"286d7a56-b961-4cff-9c69-063a84b69afd",
	})
	assert.Equal(t, []string{
		"4881d566-90a9-44c9-9e3e-c6bb51e07f5c",
		"286d7a56-b961-4cff-9c69-063a84b69afd",
	}, ids)
}

func mustMarshalDispatchSuccess(t *testing.T, stdout string) []byte {
	t.Helper()
	resultPayload, err := proto.Marshal(&operatorv1.CommandResult{
		Status:     operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		Stdout:     stdout,
		ReturnCode: 0,
	})
	require.NoError(t, err)
	body, err := protojson.Marshal(&operatorv1.GovernedDispatchResult{
		TransactionId: "tx-1",
		EventType:     "ztd.v1.operator.command.completed",
		ActionType:    "EXECUTE_BASH",
		ResultPayload: resultPayload,
		Receipt: &operatorv1.ActionReceipt{
			TransactionId: "tx-1",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			SignerKeyId:   "key-1",
		},
	})
	require.NoError(t, err)
	return body
}
