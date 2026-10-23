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
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

var errFactory = errors.New("factory boom")

// testCmd builds the consensus group over a fixed API client.
func testCmd(t *testing.T, client authcmd.APIClient) *cobra.Command {
	t.Helper()
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	return cmdWithConfig(cmdtest.ConfigLoaderFor(cfg), func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return client, nil }, cmdtest.FileSvcFactoryFor(fileSvc))
}

func TestConsensusEnrollAppCommands(t *testing.T) {
	policy := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: "reviewer", MemberAppIDs: []string{"reviewer"}, Quorum: 1, RequireDistinct: true, Provider: string(constants.ConsensusProviderApplication), Enabled: true, EndpointURL: "https://reviewer.example/deliberate"}
	policyBody, err := json.Marshal(policy)
	require.NoError(t, err)
	for _, tc := range []struct {
		name         string
		args         []string
		postResponse []byte
		wantError    error
		wantPosts    int
	}{
		{name: "enroll app", args: []string{"enroll", "app", "reviewer", policy.EndpointURL}, postResponse: policyBody, wantPosts: 1},
		{name: "reject plaintext", args: []string{"enroll", "app", "reviewer", "http://reviewer.example"}, wantError: constants.ErrConsensusEndpointInvalid},
		{name: "reject empty app", args: []string{"enroll", "app", " ", policy.EndpointURL}, wantError: constants.ErrMissingRequiredField},
		{name: "reject malformed success", args: []string{"enroll", "app", "reviewer", policy.EndpointURL}, postResponse: []byte(`{}`), wantPosts: 1, wantError: constants.ErrInvalidJSONResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &cmdtest.MockAPIClient{PostResp: tc.postResponse, GetResp: policyBody}
			cmd := testCmd(t, client)
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
				require.Equal(t, models.PlatformEnrollmentBindConsensusRequest{AppID: "reviewer", EndpointURL: policy.EndpointURL, Provider: string(constants.ConsensusProviderApplication)}, client.PostCalls[0].Body)
			}
		})
	}
}

func TestConsensusEnrollApp_SendsKOfNFlags(t *testing.T) {
	policy := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: "reviewer-a", MemberAppIDs: []string{"reviewer-a", "reviewer-b", "reviewer-c"}, Quorum: 2, RequireDistinct: true, Provider: string(constants.ConsensusProviderApplication), Enabled: true, EndpointURL: "https://reviewer-a.example/deliberate"}
	body, err := json.Marshal(policy)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{PostResp: body}
	cmd := testCmd(t, client)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"enroll", "app", "reviewer-a", policy.EndpointURL, "--member-app", "reviewer-a", "--member-app", "reviewer-b", "--member-app", "reviewer-c", "--quorum", "2"})
	require.NoError(t, cmd.Execute())
	require.Len(t, client.PostCalls, 1)
	require.Equal(t, models.PlatformEnrollmentBindConsensusRequest{AppID: "reviewer-a", EndpointURL: policy.EndpointURL, MemberAppIDs: []string{"reviewer-a", "reviewer-b", "reviewer-c"}, Quorum: 2, Provider: string(constants.ConsensusProviderApplication)}, client.PostCalls[0].Body)
}

func TestConsensusEnrollApp_TrimsMemberAppIDsBeforeCompare(t *testing.T) {
	policy := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: "reviewer", MemberAppIDs: []string{"reviewer", "peer"}, Quorum: 2, RequireDistinct: true, Provider: string(constants.ConsensusProviderApplication), Enabled: true, EndpointURL: "https://reviewer.example/deliberate"}
	body, err := json.Marshal(policy)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{PostResp: body}
	cmd := testCmd(t, client)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"enroll", "app", " reviewer ", policy.EndpointURL, "--member-app", " reviewer ", "--member-app", "peer ", "--quorum", "2"})
	require.NoError(t, cmd.Execute())
	require.Len(t, client.PostCalls, 1)
	require.Equal(t, models.PlatformEnrollmentBindConsensusRequest{AppID: "reviewer", EndpointURL: policy.EndpointURL, MemberAppIDs: []string{"reviewer", "peer"}, Quorum: 2, Provider: string(constants.ConsensusProviderApplication)}, client.PostCalls[0].Body)
}

func TestConsensusEnrollApp_RejectsEmptyMemberAppLocally(t *testing.T) {
	for _, member := range []string{"", "   "} {
		client := &cmdtest.MockAPIClient{}
		cmd := testCmd(t, client)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"enroll", "app", "reviewer", "https://reviewer.example/deliberate", "--member-app", "reviewer", "--member-app", member, "--quorum", "1"})
		err := cmd.Execute()
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		require.Empty(t, client.PostCalls)
	}
}

func TestConsensusEnrollApp_HelpListsMemberAndQuorumOptions(t *testing.T) {
	cmd := enrollAppCmd(nil)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), "--member-app")
	require.Contains(t, output.String(), "--quorum")
}

func TestConsensusEnrollApp_PrintsPostureSuspension(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			suspension := models.PlatformEnrollmentL3SuspensionResponse{Status: models.PlatformEnrollmentStatusL3Required, Posture: posture, TxHash: "binding-hash", ApprovalURL: "https://gateway.example/approve/binding-hash", ExpiresAt: time.Now().Add(time.Minute)}
			body, err := json.Marshal(suspension)
			require.NoError(t, err)
			client := &cmdtest.MockAPIClient{PostResp: body}
			cmd := testCmd(t, client)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs([]string{"enroll", "app", "reviewer", "https://reviewer.example/deliberate"})
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

func TestConsensusEnrollApp_JSONSuspension(t *testing.T) {
	suspension := models.PlatformEnrollmentL3SuspensionResponse{Status: models.PlatformEnrollmentStatusL3Required, Posture: constants.PostureNotary, TxHash: "binding-hash", ApprovalURL: "https://gateway.example/approve/binding-hash", ExpiresAt: time.Now().UTC().Truncate(time.Second)}
	body, err := json.Marshal(suspension)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{PostResp: body}
	cmd := testCmd(t, client)
	cmd.PersistentFlags().Bool("json", false, "JSON output")
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"enroll", "app", "reviewer", "https://reviewer.example", "--json"})
	require.NoError(t, cmd.Execute())
	var actual models.PlatformEnrollmentL3SuspensionResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &actual))
	require.Equal(t, suspension, actual)
}

func TestConsensusCommands_FileServiceFailure(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"enroll", "app", "reviewer", "https://reviewer.example"}} {
		_, cfg := cmdtest.NewCmdTestEnv(t)
		cmd := cmdWithConfig(cmdtest.ConfigLoaderFor(cfg), authcmd.PanickingClientFactory(), cmdtest.FailingFileSvcFactory(errFactory))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		err := cmd.Execute()
		require.ErrorIs(t, err, constants.ErrFileServiceInit)
		require.ErrorIs(t, err, errFactory)
	}
}

func TestDecodeConsensusBinding_ProviderShapes(t *testing.T) {
	application := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: "reviewer", MemberAppIDs: []string{"reviewer"}, Quorum: 1, Provider: string(constants.ConsensusProviderApplication), Enabled: true, EndpointURL: "https://reviewer.example/deliberate"}
	tribunal := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: constants.TribunalConsensusMemberAppIDs()[0], MemberAppIDs: constants.TribunalConsensusMemberAppIDs(), Quorum: constants.TribunalConsensusDefaultQuorum, Provider: string(constants.ConsensusProviderTribunal), ModelTag: "vote-model", ModelDigest: "sha256:abc", Enabled: true}
	tribunalWithEndpoint := tribunal
	tribunalWithEndpoint.EndpointURL = "https://tribunal.example/deliberate"
	applicationNoEndpoint := application
	applicationNoEndpoint.EndpointURL = ""
	applicationWithoutProvider := application
	applicationWithoutProvider.Provider = ""
	for _, tc := range []struct {
		name      string
		policy    models.ConsensusPolicy
		wantError error
	}{
		{name: "application", policy: application},
		{name: "application without provider", policy: applicationWithoutProvider, wantError: constants.ErrConsensusProviderInvalid},
		{name: "tribunal without endpoint", policy: tribunal},
		{name: "tribunal with endpoint", policy: tribunalWithEndpoint, wantError: constants.ErrConsensusTribunalEndpointSet},
		{name: "application without endpoint", policy: applicationNoEndpoint, wantError: constants.ErrMissingRequiredField},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.policy)
			require.NoError(t, err)
			decoded, err := decodeConsensusBinding(body)
			if tc.wantError != nil {
				require.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
				require.ErrorIs(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.policy, *decoded)
		})
	}
}
