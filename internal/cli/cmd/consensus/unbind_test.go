// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package consensuscmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestConsensusUnbind_ClearsBindingJSON(t *testing.T) {
	cleared := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: "reviewer", MemberAppIDs: []string{"reviewer"}, Quorum: 1, Provider: string(constants.ConsensusProviderApplication), Enabled: false, EndpointURL: "https://reviewer.example/deliberate"}
	body, err := json.Marshal(cleared)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{DeleteResp: body}
	cmd := testCmd(t, client)
	cmd.PersistentFlags().Bool("json", false, "JSON output")
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"unbind", "--json"})
	require.NoError(t, cmd.Execute())
	var actual models.ConsensusPolicy
	require.NoError(t, json.Unmarshal(output.Bytes(), &actual))
	require.Equal(t, cleared, actual)
	require.Equal(t, []string{constants.APIPaths.AuthPlatformEnrollmentConsensus}, client.DeleteCalls)
	require.Empty(t, client.PostCalls)
}

func TestConsensusUnbind_GatewayErrorIsReturned(t *testing.T) {
	statusErr := &api.StatusError{StatusCode: http.StatusNotFound, Body: constants.ErrConsensusBindingNotFound.Error()}
	client := &cmdtest.MockAPIClient{DeleteErr: statusErr}
	cmd := testCmd(t, client)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"unbind"})
	err := cmd.Execute()
	require.Error(t, err)
	var got *api.StatusError
	require.ErrorAs(t, err, &got)
	require.Equal(t, http.StatusNotFound, got.StatusCode)
	require.Len(t, client.DeleteCalls, 1)
}

func TestConsensusUnbind_PrintsPostureSuspension(t *testing.T) {
	suspension := models.PlatformEnrollmentL3SuspensionResponse{Status: models.PlatformEnrollmentStatusL3Required, Posture: constants.PostureRatify, TxHash: "unbind-hash", ApprovalURL: "https://gateway.example/approve/unbind-hash", ExpiresAt: time.Now().Add(time.Minute)}
	body, err := json.Marshal(suspension)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{DeleteResp: body}
	cmd := testCmd(t, client)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"unbind"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), suspension.Posture.HumanApprovalReason())
	require.Contains(t, output.String(), suspension.ApprovalURL)
	require.Contains(t, output.String(), suspension.TxHash)
	require.Len(t, client.DeleteCalls, 1)
	require.Empty(t, client.PostCalls)
}

func TestConsensusUnbind_JSONSuspension(t *testing.T) {
	suspension := models.PlatformEnrollmentL3SuspensionResponse{Status: models.PlatformEnrollmentStatusL3Required, Posture: constants.PostureNotary, TxHash: "unbind-hash", ApprovalURL: "https://gateway.example/approve/unbind-hash", ExpiresAt: time.Now().UTC().Truncate(time.Second)}
	body, err := json.Marshal(suspension)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{DeleteResp: body}
	cmd := testCmd(t, client)
	cmd.PersistentFlags().Bool("json", false, "JSON output")
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"unbind", "--json"})
	require.NoError(t, cmd.Execute())
	var actual models.PlatformEnrollmentL3SuspensionResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &actual))
	require.Equal(t, suspension, actual)
}

func TestConsensusUnbind_RejectsMalformedSuccess(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{}`),
		[]byte(`not json`),
	} {
		client := &cmdtest.MockAPIClient{DeleteResp: body}
		cmd := testCmd(t, client)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"unbind"})
		err := cmd.Execute()
		require.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
	}
}

func TestConsensusUnbind_StillEnabledBindingIsRejected(t *testing.T) {
	still := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: "reviewer", MemberAppIDs: []string{"reviewer"}, Quorum: 1, Provider: string(constants.ConsensusProviderApplication), Enabled: true, EndpointURL: "https://reviewer.example/deliberate"}
	body, err := json.Marshal(still)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{DeleteResp: body}
	cmd := testCmd(t, client)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"unbind"})
	require.ErrorIs(t, cmd.Execute(), constants.ErrInvalidJSONResponse)
}

func TestConsensusUnbind_RejectsArgs(t *testing.T) {
	client := &cmdtest.MockAPIClient{}
	cmd := testCmd(t, client)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"unbind", "extra"})
	require.Error(t, cmd.Execute())
	require.Empty(t, client.DeleteCalls)
}
