// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestConsensusVoteAttemptID(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("ab", 32)
	member := constants.TribunalConsensusAppIDPrefix + "axiom"

	// The first attempt keeps the ID every vote has always had, so recorded
	// evidence and its fixtures stay valid.
	assert.Equal(t, "l2-"+strings.Repeat("ab", 16)+"-axiom", ConsensusVoteAttemptID(hash, member, 0))
	assert.Equal(t, "l2-"+strings.Repeat("ab", 16)+"-axiom-r1", ConsensusVoteAttemptID(hash, member, 1))
	assert.Equal(t, "l2-"+strings.Repeat("ab", 16)+"-axiom-r12", ConsensusVoteAttemptID(hash, member, 12))
	assert.Equal(t, "l2-short-axiom", ConsensusVoteAttemptID("short", member, 0))
}

func TestConsensusVoteAttemptID_IsDistinctPerMemberSubjectAndRetry(t *testing.T) {
	t.Parallel()
	seen := make(map[string]string)
	for _, hash := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		for _, member := range constants.TribunalConsensusMemberAppIDs() {
			for retry := range uint32(6) {
				id := ConsensusVoteAttemptID(hash, member, retry)
				label := hash[:1] + "/" + member
				previous, duplicate := seen[id]
				require.False(t, duplicate, "attempt ID %q repeats %s", id, previous)
				seen[id] = label
			}
		}
	}
}

func TestConsensusVoteAttempts_WalkSupersededAttemptsThenTheVote(t *testing.T) {
	t.Parallel()
	first := &operatorv1.ConsensusVoteInference{RetryCount: 0}
	second := &operatorv1.ConsensusVoteInference{RetryCount: 1}
	vote := &operatorv1.ConsensusVoteInference{RetryCount: 2, SupersededAttempts: []*operatorv1.ConsensusVoteInference{first, second}}

	attempts := ConsensusVoteAttempts(vote)
	require.Len(t, attempts, 3)
	assert.Same(t, first, attempts[0])
	assert.Same(t, second, attempts[1])
	assert.Same(t, vote, attempts[2])

	plain := &operatorv1.ConsensusVoteInference{}
	assert.Equal(t, []*operatorv1.ConsensusVoteInference{plain}, ConsensusVoteAttempts(plain))
	assert.Nil(t, ConsensusVoteAttempts(nil))
	assert.Len(t, vote.GetSupersededAttempts(), 2, "walking never edits the record")
}
