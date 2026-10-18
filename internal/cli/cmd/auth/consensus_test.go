// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package authcmd

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestConsensusEnrollmentCommands(t *testing.T) {
	policy := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), MemberAppIDs: []string{"reviewer"}, Quorum: 1, RequireDistinct: true, Enabled: true, EndpointURL: "https://reviewer.example/deliberate"}
	policyBody, err := json.Marshal(policy)
	require.NoError(t, err)
	for _, tc := range []struct {
		name         string
		args         []string
		postResponse []byte
		wantError    error
		wantPosts    int
	}{
		{name: "bind", args: []string{"bind", "reviewer", policy.EndpointURL}, postResponse: policyBody, wantPosts: 1},
		{name: "show", args: []string{"show"}},
		{name: "reject plaintext", args: []string{"bind", "reviewer", "http://reviewer.example"}, wantError: constants.ErrConsensusEndpointInvalid},
		{name: "reject empty app", args: []string{"bind", " ", policy.EndpointURL}, wantError: constants.ErrMissingRequiredField},
		{name: "reject malformed success", args: []string{"bind", "reviewer", policy.EndpointURL}, postResponse: []byte(`{}`), wantPosts: 1, wantError: constants.ErrInvalidJSONResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			client := &cmdtest.MockAPIClient{PostResp: tc.postResponse, GetResp: policyBody}
			cmd := consensusEnrollmentCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return client, nil }, cmdtest.FileSvcFactoryFor(fileSvc))
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if tc.wantError != nil {
				require.ErrorIs(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
				var actual models.ConsensusPolicy
				require.NoError(t, json.Unmarshal(output.Bytes(), &actual))
				require.Equal(t, policy, actual)
			}
			require.Len(t, client.PostCalls, tc.wantPosts)
			if tc.wantPosts == 1 {
				require.Equal(t, constants.APIPaths.AuthPlatformEnrollmentConsensus, client.PostCalls[0].Path)
				require.Equal(t, models.PlatformEnrollmentBindConsensusRequest{AppID: "reviewer", EndpointURL: policy.EndpointURL}, client.PostCalls[0].Body)
			}
			if tc.name == "show" {
				require.Equal(t, []string{constants.APIPaths.AuthPlatformEnrollmentConsensus}, client.GetCalls)
			}
		})
	}
}

func TestConsensusEnrollmentBind_PrintsPostureSuspension(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			suspension := models.PlatformEnrollmentL3SuspensionResponse{Status: models.PlatformEnrollmentStatusL3Required, Posture: posture, TxHash: "binding-hash", ApprovalURL: "https://gateway.example/approve/binding-hash", ExpiresAt: time.Now().Add(time.Minute)}
			body, err := json.Marshal(suspension)
			require.NoError(t, err)
			client := &cmdtest.MockAPIClient{PostResp: body}
			cmd := consensusEnrollmentCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return client, nil }, cmdtest.FileSvcFactoryFor(fileSvc))
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs([]string{"bind", "reviewer", "https://reviewer.example/deliberate"})
			require.NoError(t, cmd.Execute())
			require.Contains(t, output.String(), posture.HumanApprovalReason())
			require.Contains(t, output.String(), suspension.ApprovalURL)
			require.Contains(t, output.String(), suspension.TxHash)
			require.Contains(t, output.String(), suspension.ExpiresAt.Format(time.RFC3339))
			require.Len(t, client.PostCalls, 1)
			require.Empty(t, client.GetCalls)
		})
	}
}

func TestConsensusEnrollmentBind_JSONSuspension(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	suspension := models.PlatformEnrollmentL3SuspensionResponse{Status: models.PlatformEnrollmentStatusL3Required, Posture: constants.PostureNotary, TxHash: "binding-hash", ApprovalURL: "https://gateway.example/approve/binding-hash", ExpiresAt: time.Now().UTC().Truncate(time.Second)}
	body, err := json.Marshal(suspension)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{PostResp: body}
	cmd := consensusEnrollmentCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return client, nil }, cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.PersistentFlags().Bool("json", false, "JSON output")
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"bind", "reviewer", "https://reviewer.example", "--json"})
	require.NoError(t, cmd.Execute())
	var actual models.PlatformEnrollmentL3SuspensionResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &actual))
	require.Equal(t, suspension, actual)
}

func TestConsensusEnrollmentCommands_FileServiceFailure(t *testing.T) {
	for _, args := range [][]string{{"show"}, {"bind", "reviewer", "https://reviewer.example"}} {
		_, cfg := cmdtest.NewCmdTestEnv(t)
		cmd := consensusEnrollmentCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), PanickingClientFactory(), cmdtest.FailingFileSvcFactory(errFactory))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		err := cmd.Execute()
		require.ErrorIs(t, err, constants.ErrFileServiceInit)
		require.ErrorIs(t, err, errFactory)
	}
}
