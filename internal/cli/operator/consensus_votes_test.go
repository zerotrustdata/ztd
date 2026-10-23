// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operator

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type fakeGetter struct {
	bodies map[string][]byte
	err    error
	paths  []string
}

func (f *fakeGetter) Get(path string) ([]byte, error) {
	f.paths = append(f.paths, path)
	if f.err != nil {
		return nil, f.err
	}
	return f.bodies[path], nil
}

var quorumNotMet = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET.String()

func TestAttachConsensusVotesOnlyForL2QuorumRejections(t *testing.T) {
	getter := &fakeGetter{bodies: map[string][]byte{
		ConsensusVotesPath("tx-l2"): []byte(`{"success":true,"transaction_id":"tx-l2","votes":[{"member_app_id":"tribunal-1","outcome":"deny","reason":"unsafe","quorum":3}]}`),
	}}
	results := []RunResult{
		{TransactionID: "tx-l2", RejectionCode: quorumNotMet},
		{TransactionID: "tx-l4", RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EXPIRED.String()},
		{TransactionID: "tx-ok", Success: true},
		{RejectionCode: quorumNotMet},
	}

	AttachConsensusVotes(getter, results)

	assert.Equal(t, []string{constants.APIPaths.AuditConsensusVotes + "?tx_id=tx-l2"}, getter.paths)
	require.Len(t, results[0].ConsensusVotes, 1)
	assert.Equal(t, models.ConsensusVoteOutcomeDeny, results[0].ConsensusVotes[0].Outcome)
	assert.Equal(t, "unsafe", results[0].ConsensusVotes[0].Reason)
	for _, result := range results[1:] {
		assert.Nil(t, result.ConsensusVotes)
		assert.Empty(t, result.ConsensusVotesError)
	}
}

func TestAttachConsensusVotesRecordsFetchFailure(t *testing.T) {
	getter := &fakeGetter{err: errors.New("forbidden")}
	results := []RunResult{{TransactionID: "tx-l2", RejectionCode: quorumNotMet}}

	AttachConsensusVotes(getter, results)

	assert.Nil(t, results[0].ConsensusVotes)
	assert.Contains(t, results[0].ConsensusVotesError, "forbidden")
}

func TestWriteConsensusVotes(t *testing.T) {
	var buf bytes.Buffer
	WriteConsensusVotes(&buf, "  ", "tx-1", []models.ConsensusVoteRecord{
		{ConsensusSetID: "tribunal", ModelTag: "gemma4:e2b", Quorum: 3, MemberAppID: "tribunal-1", Outcome: models.ConsensusVoteOutcomeApprove, Reason: "read-only"},
		{ConsensusSetID: "tribunal", ModelTag: "gemma4:e2b", Quorum: 3, MemberAppID: "tribunal-2", Outcome: models.ConsensusVoteOutcomeNoVote, Failure: "member inference: timeout"},
	})
	out := buf.String()
	for _, want := range []string{"  Consensus votes for transaction tx-1 (set tribunal, model gemma4:e2b, quorum 3):",
		"tribunal-1", "reason: read-only", "no_vote", "failure: member inference: timeout", "  Approvals: 1 of 2 votes, quorum 3"} {
		assert.Contains(t, out, want)
	}

	buf.Reset()
	WriteConsensusVotes(&buf, "", "tx-2", nil)
	assert.Equal(t, "No consensus votes recorded for transaction tx-2\n", buf.String())
}
