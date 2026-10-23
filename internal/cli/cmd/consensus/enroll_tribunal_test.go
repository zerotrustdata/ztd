// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package consensuscmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	tribunalTestTag    = "vote-model"
	tribunalTestDigest = "abababababababababababababababababababababababababababababababab"
)

// fakeAppEnroller completes enrollment without touching keys or the Gateway.
type fakeAppEnroller struct{}

func (fakeAppEnroller) Enroll(context.Context, io.Writer) (*models.PlatformEnrollmentCompleteResponse, error) {
	return nil, nil
}

// writeTribunalRegistry freezes a registry with one variant whose digest is
// stored in the provider's "sha256:" form, so normalization is exercised.
func writeTribunalRegistry(t *testing.T, fileSvc fs.RuntimeFileService) {
	t.Helper()
	ctx := context.Background()
	variants := []*evalv1.ModelVariant{{
		VariantId:      "variant-vote",
		ProviderClass:  "ollama",
		ServedModelTag: tribunalTestTag,
		ModelDigest:    "sha256:" + tribunalTestDigest,
	}}
	freeze, err := evaluation.MaterializeModelRegistry("campaign-tribunal-test", variants)
	require.NoError(t, err)
	raw := make([]json.RawMessage, 0, len(variants))
	for _, variant := range variants {
		encoded, err := protojson.Marshal(variant)
		require.NoError(t, err)
		raw = append(raw, encoded)
	}
	payload, err := json.Marshal(struct {
		CampaignID          string            `json:"campaign_id"`
		ModelRegistryDigest string            `json:"model_registry_digest"`
		Variants            []json.RawMessage `json:"variants"`
	}{
		CampaignID:          "campaign-tribunal-test",
		ModelRegistryDigest: freeze.RegistryDigest,
		Variants:            raw,
	})
	require.NoError(t, err)
	require.NoError(t, fileSvc.MkdirAll(ctx, constants.EvaluationDirname, constants.PermDirStandard))
	require.NoError(t, fileSvc.WriteFile(ctx, evaluation.DefaultModelInventoryRelPath, payload, constants.PermFileReadOnly))
}

// runEnrollTribunal executes `enroll tribunal` over a fresh runtime tree with
// a registry frozen for tribunalTestTag. enrolled marks members whose sealed
// key already exists. It returns stdout and the app IDs that were enrolled.
func runEnrollTribunal(t *testing.T, client authcmd.APIClient, enrolled map[string]bool, args ...string) (string, []string, error) {
	t.Helper()
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	writeTribunalRegistry(t, fileSvc)
	var enrolledNow []string
	deps := tribunalEnrollDeps{
		configLoader:   cmdtest.ConfigLoaderFor(cfg),
		fileSvcFactory: cmdtest.FileSvcFactoryFor(fileSvc),
		enrollerFactory: func(appName string, _ fs.RuntimeFileService, _ *config.Config, _ *slog.Logger, _ ...auth.AppEnrollmentOptions) (authcmd.AppEnroller, error) {
			enrolledNow = append(enrolledNow, appName)
			return fakeAppEnroller{}, nil
		},
		memberEnrolled: func(_ context.Context, _ fs.RuntimeFileService, _ *config.Config, appID string) bool {
			return enrolled[appID]
		},
	}
	cmd := &cobra.Command{Use: "enroll"}
	cmd.AddCommand(enrollTribunalCmd(func() (authcmd.APIClient, error) { return client, nil }, deps))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), enrolledNow, err
}

func tribunalBindingBody(t *testing.T, quorum int) []byte {
	t.Helper()
	members := constants.TribunalConsensusMemberAppIDs()
	body, err := json.Marshal(models.ConsensusPolicy{
		ID:              string(constants.DocIDConsensusBinding),
		AppID:           members[0],
		MemberAppIDs:    members,
		Quorum:          quorum,
		RequireDistinct: true,
		Provider:        string(constants.ConsensusProviderTribunal),
		ModelTag:        tribunalTestTag,
		ModelDigest:     tribunalTestDigest,
		Enabled:         true,
	})
	require.NoError(t, err)
	return body
}

func TestConsensusEnrollTribunal_Validation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		wantError error
	}{
		{name: "missing model", args: []string{"tribunal"}, wantError: constants.ErrMissingRequiredField},
		{name: "blank model", args: []string{"tribunal", "--model", "   "}, wantError: constants.ErrMissingRequiredField},
		{name: "quorum below minimum", args: []string{"tribunal", "--model", tribunalTestTag, "--quorum", "2"}, wantError: constants.ErrConsensusQuorumInvalid},
		{name: "quorum above member count", args: []string{"tribunal", "--model", tribunalTestTag, "--quorum", "6"}, wantError: constants.ErrConsensusQuorumInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &cmdtest.MockAPIClient{}
			_, enrolled, err := runEnrollTribunal(t, client, nil, tc.args...)
			require.ErrorIs(t, err, tc.wantError)
			require.Empty(t, client.PostCalls)
			require.Empty(t, enrolled)
		})
	}
}

func TestConsensusEnrollTribunal_UnknownTagIsNotFound(t *testing.T) {
	client := &cmdtest.MockAPIClient{}
	_, enrolled, err := runEnrollTribunal(t, client, nil, "tribunal", "--model", "missing-tag")
	require.ErrorIs(t, err, constants.ErrNotFound)
	require.ErrorContains(t, err, "missing-tag")
	require.Empty(t, client.PostCalls)
	require.Empty(t, enrolled)
}

func TestConsensusEnrollTribunal_ResumeSkipsEnrolledMembers(t *testing.T) {
	members := constants.TribunalConsensusMemberAppIDs()
	for _, tc := range []struct {
		name       string
		enrolled   map[string]bool
		wantEnroll []string
	}{
		{name: "fresh enrolls every member in canonical order", enrolled: nil, wantEnroll: members},
		{name: "re-run skips members with sealed keys", enrolled: map[string]bool{members[0]: true, members[1]: true}, wantEnroll: members[2:]},
		{name: "all keys present enrolls nobody", enrolled: map[string]bool{members[0]: true, members[1]: true, members[2]: true, members[3]: true, members[4]: true}, wantEnroll: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &cmdtest.MockAPIClient{PostResp: tribunalBindingBody(t, constants.TribunalConsensusDefaultQuorum)}
			_, enrolled, err := runEnrollTribunal(t, client, tc.enrolled, "tribunal", "--model", tribunalTestTag)
			require.NoError(t, err)
			require.Equal(t, tc.wantEnroll, enrolled)
			require.Len(t, client.PostCalls, 1)
		})
	}
}

func TestConsensusEnrollTribunal_BindRequestBody(t *testing.T) {
	members := constants.TribunalConsensusMemberAppIDs()
	for _, tc := range []struct {
		name       string
		args       []string
		wantQuorum int
	}{
		{name: "default quorum", args: []string{"tribunal", "--model", tribunalTestTag}, wantQuorum: constants.TribunalConsensusDefaultQuorum},
		{name: "explicit quorum", args: []string{"tribunal", "--model", " " + tribunalTestTag + " ", "--quorum", "4"}, wantQuorum: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &cmdtest.MockAPIClient{PostResp: tribunalBindingBody(t, tc.wantQuorum)}
			_, _, err := runEnrollTribunal(t, client, map[string]bool{members[0]: true}, tc.args...)
			require.NoError(t, err)
			require.Len(t, client.PostCalls, 1)
			require.Equal(t, constants.APIPaths.AuthPlatformEnrollmentConsensus, client.PostCalls[0].Path)
			require.Equal(t, models.PlatformEnrollmentBindConsensusRequest{
				AppID:        members[0],
				MemberAppIDs: members,
				Quorum:       tc.wantQuorum,
				Provider:     string(constants.ConsensusProviderTribunal),
				ModelTag:     tribunalTestTag,
				ModelDigest:  tribunalTestDigest,
			}, client.PostCalls[0].Body)
		})
	}
}

func TestConsensusEnrollTribunal_PrintsPostureSuspension(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			suspension := models.PlatformEnrollmentL3SuspensionResponse{Status: models.PlatformEnrollmentStatusL3Required, Posture: posture, TxHash: "tribunal-binding-hash", ApprovalURL: "https://gateway.example/approve/tribunal-binding-hash", ExpiresAt: time.Now().Add(time.Minute)}
			body, err := json.Marshal(suspension)
			require.NoError(t, err)
			client := &cmdtest.MockAPIClient{PostResp: body}
			output, enrolled, err := runEnrollTribunal(t, client, nil, "tribunal", "--model", tribunalTestTag)
			require.NoError(t, err)
			require.Len(t, enrolled, len(constants.TribunalConsensusMemberAppIDs()))
			require.Contains(t, output, posture.HumanApprovalReason())
			require.Contains(t, output, suspension.ApprovalURL)
			require.Contains(t, output, suspension.TxHash)
			require.Len(t, client.PostCalls, 1)
			require.Empty(t, client.GetCalls)
		})
	}
}

func TestConsensusEnrollTribunal_RejectsMismatchedBinding(t *testing.T) {
	policy := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: constants.TribunalConsensusMemberAppIDs()[0], MemberAppIDs: constants.TribunalConsensusMemberAppIDs(), Quorum: constants.TribunalConsensusDefaultQuorum, Provider: string(constants.ConsensusProviderTribunal), ModelTag: tribunalTestTag, ModelDigest: strings.Repeat("cd", 32), Enabled: true}
	body, err := json.Marshal(policy)
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{PostResp: body}
	_, _, err = runEnrollTribunal(t, client, map[string]bool{constants.TribunalConsensusMemberAppIDs()[0]: true}, "tribunal", "--model", tribunalTestTag)
	require.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}
