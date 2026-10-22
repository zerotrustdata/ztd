// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestResolveConsensusMembership(t *testing.T) {
	cases := []struct {
		name        string
		members     []string
		quorum      int
		wantMembers []string
		wantQuorum  int
		wantErr     error
	}{
		{name: "no members is the primary alone at 1-of-1", wantMembers: []string{"app-a"}, wantQuorum: 1},
		{name: "explicit primary alone defaults to 1-of-1", members: []string{" app-a "}, wantMembers: []string{"app-a"}, wantQuorum: 1},
		{name: "explicit K-of-N", members: []string{"app-a", "app-b", "app-c"}, quorum: 2, wantMembers: []string{"app-a", "app-b", "app-c"}, wantQuorum: 2},
		{name: "multi-member set must state its quorum", members: []string{"app-a", "app-b"}, wantErr: constants.ErrConsensusQuorumInvalid},
		{name: "quorum above member count", members: []string{"app-a", "app-b"}, quorum: 3, wantErr: constants.ErrConsensusQuorumInvalid},
		{name: "negative quorum", quorum: -1, wantErr: constants.ErrConsensusQuorumInvalid},
		{name: "empty member", members: []string{"app-a", " "}, quorum: 1, wantErr: constants.ErrConsensusAppNotEnrolled},
		{name: "duplicate member after trimming", members: []string{"app-a", "app-a "}, quorum: 1, wantErr: constants.ErrConsensusMemberDuplicate},
		{name: "primary not a member", members: []string{"app-b"}, quorum: 1, wantErr: constants.ErrConsensusAppNotEnrolled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			members, quorum, err := ResolveConsensusMembership("app-a", tc.members, tc.quorum)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantMembers, members)
			require.Equal(t, tc.wantQuorum, quorum)
		})
	}
}

const (
	testAppEndpoint    = "https://app.example.com/deliberate"
	testTribunalTag    = "tribunal-vote-model"
	testTribunalDigest = "sha256:0123456789abcdef"
)

func TestResolveConsensusBinding(t *testing.T) {
	canonical := constants.TribunalConsensusMemberAppIDs()
	validTribunal := ConsensusBindingSpec{
		Provider:    constants.ConsensusProviderTribunal,
		AppID:       canonical[0],
		ModelTag:    testTribunalTag,
		ModelDigest: testTribunalDigest,
	}
	cases := []struct {
		name        string
		spec        ConsensusBindingSpec
		wantErr     error
		wantMembers []string
		wantQuorum  int
	}{
		{
			name:        "application happy path defaults to primary alone at 1-of-1",
			spec:        ConsensusBindingSpec{Provider: constants.ConsensusProviderApplication, AppID: "app-a", EndpointURL: testAppEndpoint},
			wantMembers: []string{"app-a"},
			wantQuorum:  1,
		},
		{
			name:    "empty provider is rejected",
			spec:    ConsensusBindingSpec{AppID: "app-a", EndpointURL: testAppEndpoint},
			wantErr: constants.ErrConsensusProviderInvalid,
		},
		{
			name:    "application with model tag is rejected",
			spec:    ConsensusBindingSpec{Provider: constants.ConsensusProviderApplication, AppID: "app-a", EndpointURL: testAppEndpoint, ModelTag: testTribunalTag},
			wantErr: constants.ErrConsensusModelUnexpected,
		},
		{
			name:    "application missing endpoint is rejected",
			spec:    ConsensusBindingSpec{Provider: constants.ConsensusProviderApplication, AppID: "app-a"},
			wantErr: constants.ErrMissingRequiredField,
		},
		{
			name:        "tribunal happy path with empty members normalizes to canonical list at quorum 3",
			spec:        validTribunal,
			wantMembers: canonical,
			wantQuorum:  constants.TribunalConsensusDefaultQuorum,
		},
		{
			name:        "tribunal explicit quorum 5 is accepted",
			spec:        withQuorum(validTribunal, 5),
			wantMembers: canonical,
			wantQuorum:  5,
		},
		{
			name:    "tribunal quorum 2 is below minimum",
			spec:    withQuorum(validTribunal, 2),
			wantErr: constants.ErrConsensusQuorumInvalid,
		},
		{
			name:    "tribunal quorum 6 is above member count",
			spec:    withQuorum(validTribunal, 6),
			wantErr: constants.ErrConsensusQuorumInvalid,
		},
		{
			name: "tribunal with endpoint is rejected",
			spec: func() ConsensusBindingSpec {
				s := validTribunal
				s.EndpointURL = testAppEndpoint
				return s
			}(),
			wantErr: constants.ErrConsensusTribunalEndpointSet,
		},
		{
			name: "tribunal with wrong primary app is rejected",
			spec: func() ConsensusBindingSpec {
				s := validTribunal
				s.AppID = "tribunal-concord"
				return s
			}(),
			wantErr: constants.ErrConsensusTribunalMembersInvalid,
		},
		{
			name: "tribunal with members out of order is rejected",
			spec: func() ConsensusBindingSpec {
				s := validTribunal
				s.MemberAppIDs = []string{canonical[1], canonical[0], canonical[2], canonical[3], canonical[4]}
				return s
			}(),
			wantErr: constants.ErrConsensusTribunalMembersInvalid,
		},
		{
			name: "tribunal with four members is rejected",
			spec: func() ConsensusBindingSpec {
				s := validTribunal
				s.MemberAppIDs = canonical[:4]
				return s
			}(),
			wantErr: constants.ErrConsensusTribunalMembersInvalid,
		},
		{
			name: "tribunal missing model digest is rejected",
			spec: func() ConsensusBindingSpec {
				s := validTribunal
				s.ModelDigest = ""
				return s
			}(),
			wantErr: constants.ErrConsensusModelRequired,
		},
		{
			name:    "unknown provider is rejected",
			spec:    ConsensusBindingSpec{Provider: "bogus", AppID: "app-a", EndpointURL: testAppEndpoint},
			wantErr: constants.ErrConsensusProviderInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveConsensusBinding(tc.spec)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantMembers, got.MemberAppIDs)
			require.Equal(t, tc.wantQuorum, got.Quorum)
		})
	}
}

func TestConsensusPolicyProviderRequiresExplicitProvider(t *testing.T) {
	var policy ConsensusPolicy
	legacy := `{"app_id":"app-a","member_app_ids":["app-a"],"quorum":1,"require_distinct":true,"endpoint_url":"https://app.example.com/deliberate","enabled":true}`
	require.NoError(t, json.Unmarshal([]byte(legacy), &policy))
	require.Empty(t, policy.Provider)

	_, err := policy.ConsensusProvider()
	require.ErrorIs(t, err, constants.ErrConsensusProviderInvalid)
}

func withQuorum(spec ConsensusBindingSpec, quorum int) ConsensusBindingSpec {
	spec.Quorum = quorum
	return spec
}
