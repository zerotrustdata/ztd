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
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// routedClient answers GETs per path so status sees the binding and health
// bodies independently. Any POST, PUT, or DELETE is a test failure.
type routedClient struct {
	bodies map[string][]byte
	errs   map[string]error
	calls  []string
}

func (c *routedClient) Get(path string) ([]byte, error) {
	c.calls = append(c.calls, path)
	if err := c.errs[path]; err != nil {
		return nil, err
	}
	return c.bodies[path], nil
}

func (c *routedClient) Post(path string, _ interface{}) ([]byte, error) {
	return nil, errors.New("unexpected POST " + path)
}

func (c *routedClient) Put(path string, _ interface{}) ([]byte, error) {
	return nil, errors.New("unexpected PUT " + path)
}

func (c *routedClient) Delete(path string) ([]byte, error) {
	return nil, errors.New("unexpected DELETE " + path)
}

func healthBody(t *testing.T, health models.HealthResponse) []byte {
	t.Helper()
	body, err := json.Marshal(health)
	require.NoError(t, err)
	return body
}

func bindingBody(t *testing.T, policy models.ConsensusPolicy) []byte {
	t.Helper()
	body, err := json.Marshal(policy)
	require.NoError(t, err)
	return body
}

func TestConsensusStatus_BoundApplicationJSON(t *testing.T) {
	policy := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: "reviewer", MemberAppIDs: []string{"reviewer", "peer"}, Quorum: 2, RequireDistinct: true, Provider: string(constants.ConsensusProviderApplication), Enabled: true, EndpointURL: "https://reviewer.example/deliberate"}
	client := &routedClient{bodies: map[string][]byte{
		constants.APIPaths.AuthPlatformEnrollmentConsensus: bindingBody(t, policy),
		constants.APIPaths.Health: healthBody(t, models.HealthResponse{Posture: constants.PostureNotary, GovernanceReady: true,
			Consensus: models.HealthConsensus{Bound: true, Provider: "application", Quorum: 2, Members: 2, Usable: 2, Ready: true}}),
	}}
	cmd := testCmd(t, client)
	cmd.PersistentFlags().Bool("json", false, "JSON output")
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"status", "--json"})
	require.NoError(t, cmd.Execute())

	var actual consensusStatus
	require.NoError(t, json.Unmarshal(output.Bytes(), &actual))
	require.Equal(t, consensusStatus{
		Bound:           true,
		Provider:        "application",
		Quorum:          2,
		Members:         []string{"reviewer", "peer"},
		Usable:          2,
		Ready:           true,
		Posture:         constants.PostureNotary,
		GovernanceReady: true,
		L2Enforced:      true,
	}, actual)
	require.Equal(t, []string{constants.APIPaths.AuthPlatformEnrollmentConsensus, constants.APIPaths.Health}, client.calls)
}

func TestConsensusStatus_BoundTribunalShowsModelText(t *testing.T) {
	policy := models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: constants.TribunalConsensusMemberAppIDs()[0], MemberAppIDs: constants.TribunalConsensusMemberAppIDs(), Quorum: constants.TribunalConsensusDefaultQuorum, Provider: string(constants.ConsensusProviderTribunal), ModelTag: "vote-model", ModelDigest: "sha256:abc", Enabled: true}
	client := &routedClient{bodies: map[string][]byte{
		constants.APIPaths.AuthPlatformEnrollmentConsensus: bindingBody(t, policy),
		constants.APIPaths.Health: healthBody(t, models.HealthResponse{Posture: constants.PostureNotary, GovernanceReady: true,
			Consensus: models.HealthConsensus{Bound: true, Provider: "tribunal", Quorum: 3, Members: 5, Usable: 2, Ready: false}}),
	}}
	cmd := testCmd(t, client)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"status"})
	require.NoError(t, cmd.Execute())
	text := output.String()
	require.Contains(t, text, "Provider   tribunal")
	require.Contains(t, text, "Quorum     3-of-5")
	require.Contains(t, text, "Model      vote-model")
	require.Contains(t, text, "Signers    2/5 usable (not ready)")
	require.Contains(t, text, "Posture    notary")
	require.Contains(t, text, "L2         enforced")
}

func TestConsensusStatus_UnboundWithL2PostureFails(t *testing.T) {
	client := &routedClient{
		errs: map[string]error{constants.APIPaths.AuthPlatformEnrollmentConsensus: &api.StatusError{StatusCode: http.StatusNotFound, Body: "not bound"}},
		bodies: map[string][]byte{
			constants.APIPaths.Health: healthBody(t, models.HealthResponse{Posture: constants.PostureNotary}),
		},
	}
	cmd := testCmd(t, client)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"status"})
	err := cmd.Execute()
	require.ErrorIs(t, err, constants.ErrTxL2ConsensusNotConfigured)
	require.Contains(t, output.String(), "Consensus  not bound")
	require.Contains(t, output.String(), "L2         enforced")
}

func TestConsensusStatus_UnboundDoctrinePostureSucceeds(t *testing.T) {
	client := &routedClient{
		errs: map[string]error{constants.APIPaths.AuthPlatformEnrollmentConsensus: &api.StatusError{StatusCode: http.StatusNotFound, Body: "not bound"}},
		bodies: map[string][]byte{
			constants.APIPaths.Health: healthBody(t, models.HealthResponse{Posture: constants.PostureDoctrine}),
		},
	}
	cmd := testCmd(t, client)
	cmd.PersistentFlags().Bool("json", false, "JSON output")
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"status", "--json"})
	require.NoError(t, cmd.Execute())
	var actual consensusStatus
	require.NoError(t, json.Unmarshal(output.Bytes(), &actual))
	require.False(t, actual.Bound)
	require.False(t, actual.L2Enforced)
	require.Equal(t, constants.PostureDoctrine, actual.Posture)
}

func TestConsensusStatus_BindingFetchErrorIsNotUnbound(t *testing.T) {
	errBoom := errors.New("gateway unreachable")
	client := &routedClient{
		errs: map[string]error{constants.APIPaths.AuthPlatformEnrollmentConsensus: errBoom},
		bodies: map[string][]byte{
			constants.APIPaths.Health: healthBody(t, models.HealthResponse{Posture: constants.PostureRatify}),
		},
	}
	cmd := testCmd(t, client)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"status"})
	err := cmd.Execute()
	require.ErrorIs(t, err, errBoom)
	require.NotErrorIs(t, err, constants.ErrTxL2ConsensusNotConfigured)
}

func TestConsensusStatus_HealthFailureReturnsError(t *testing.T) {
	errBoom := errors.New("health down")
	client := &routedClient{
		bodies: map[string][]byte{constants.APIPaths.AuthPlatformEnrollmentConsensus: bindingBody(t, models.ConsensusPolicy{ID: string(constants.DocIDConsensusBinding), AppID: "reviewer", MemberAppIDs: []string{"reviewer"}, Quorum: 1, Provider: string(constants.ConsensusProviderApplication), Enabled: true, EndpointURL: "https://reviewer.example/deliberate"})},
		errs:   map[string]error{constants.APIPaths.Health: errBoom},
	}
	cmd := testCmd(t, client)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"status"})
	require.ErrorIs(t, cmd.Execute(), errBoom)
}
