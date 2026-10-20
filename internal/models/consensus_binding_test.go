// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
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
