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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestOperatorTerminateCmd_PostsToOperatorPath(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	saveTestCredentials(t, fileSvc, cfg, "user-001")
	resp, err := json.Marshal(models.TerminateOperatorResponse{Success: true, Message: "Operator terminated"})
	require.NoError(t, err)
	mockClient := &cmdtest.MockAPIClient{PostResp: resp}

	cmd := operatorTerminateCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), authcmd.MockClientFactory(mockClient), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs([]string{"a2bd9714-da8b-4449-ae5c-8d58d3b12ef1", "--reason", "actuator key rotated"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.Execute())
	require.Len(t, mockClient.PostCalls, 1)
	assert.Equal(t, constants.APIPaths.OperatorsByID+"a2bd9714-da8b-4449-ae5c-8d58d3b12ef1", mockClient.PostCalls[0].Path)
	request := mockClient.PostCalls[0].Body.(models.TerminateOperatorRequest)
	assert.Equal(t, "a2bd9714-da8b-4449-ae5c-8d58d3b12ef1", request.OperatorID)
	assert.Empty(t, request.UserID, "the Gateway takes the owner from the authenticated caller")
	assert.Equal(t, "actuator key rotated", request.Reason)
	assert.Contains(t, buf.String(), "terminated")
}

func TestOperatorTerminateCmd_ReportsGatewayRefusal(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	saveTestCredentials(t, fileSvc, cfg, "user-001")
	mockClient := &cmdtest.MockAPIClient{PostErr: errors.New("embedded operator cannot be terminated")}

	cmd := operatorTerminateCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), authcmd.MockClientFactory(mockClient), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs([]string{"embedded-operator"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operator terminate embedded-operator")
	assert.Contains(t, err.Error(), "embedded operator cannot be terminated")
}
