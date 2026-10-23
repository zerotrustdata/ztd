// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// TestVerifyL2Quorum proves the single L2 tally: only member votes count, each
// must verify under its trusted key over the transaction hash, a repeated
// member fails where distinct signers are required, and quorum counts
// approvals only.
func TestVerifyL2Quorum(t *testing.T) {
	t.Parallel()

	const txHash = "tx-hash"
	keys := map[string]ed25519.PrivateKey{}
	trusted := map[string]ed25519.PublicKey{}
	for _, id := range []string{"m1", "m2", "m3", "outsider"} {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		keys[id] = priv
		if id != "m3" {
			trusted[id] = pub
		}
	}
	vote := func(id, hash string, decision bool) *commonv1.L2Vote {
		return &commonv1.L2Vote{
			SignerKeyId:        id,
			ConsensusSignature: hex.EncodeToString(ed25519.Sign(keys[id], L2VotePayload(hash, decision))),
			Decision:           decision,
		}
	}
	policy := &L2ConsensusPolicy{MemberKeyIDs: []string{"m1", "m2", "m3"}, Quorum: 2, RequireDistinct: true, Enabled: true}
	signers := &FailClosedSignerStore{Signers: trusted}

	cases := []struct {
		name      string
		policy    *L2ConsensusPolicy
		noSigners bool
		votes     []*commonv1.L2Vote
		want      error
	}{
		{name: "quorum of approvals", votes: []*commonv1.L2Vote{vote("m1", txHash, true), vote("m2", txHash, true)}},
		{name: "a denial does not count toward quorum", votes: []*commonv1.L2Vote{vote("m1", txHash, true), vote("m2", txHash, false)}, want: constants.ErrTxL2QuorumNotMet},
		{name: "non-member votes are ignored", votes: []*commonv1.L2Vote{vote("m1", txHash, true), vote("outsider", txHash, true)}, want: constants.ErrTxL2QuorumNotMet},
		{name: "a vote over another hash is invalid", votes: []*commonv1.L2Vote{vote("m1", txHash, true), vote("m2", "other-hash", true)}, want: constants.ErrTxL2SignatureInvalid},
		{name: "a member without a trusted key is invalid", votes: []*commonv1.L2Vote{vote("m1", txHash, true), vote("m3", txHash, true)}, want: constants.ErrTxL2SignatureInvalid},
		{name: "a repeated member fails distinct signers", votes: []*commonv1.L2Vote{vote("m1", txHash, true), vote("m1", txHash, true)}, want: constants.ErrTxL2DuplicateSigner},
		{name: "no votes", want: constants.ErrTxL2QuorumNotMet},
		{name: "disabled policy", policy: &L2ConsensusPolicy{MemberKeyIDs: []string{"m1"}, Quorum: 1}, votes: []*commonv1.L2Vote{vote("m1", txHash, true)}, want: constants.ErrTxL2ConsensusSetUnknown},
		{name: "zero quorum policy is invalid", policy: &L2ConsensusPolicy{MemberKeyIDs: []string{"m1"}, Quorum: 0, Enabled: true}, want: constants.ErrConsensusQuorumInvalid},
		{name: "negative quorum policy is invalid", policy: &L2ConsensusPolicy{MemberKeyIDs: []string{"m1"}, Quorum: -1, Enabled: true}, want: constants.ErrConsensusQuorumInvalid},
		{name: "quorum above member count is invalid", policy: &L2ConsensusPolicy{MemberKeyIDs: []string{"m1"}, Quorum: 2, Enabled: true}, want: constants.ErrConsensusQuorumInvalid},
		{name: "empty member set is invalid", policy: &L2ConsensusPolicy{Quorum: 1, Enabled: true}, want: constants.ErrConsensusQuorumInvalid},
		{name: "no signer store", noSigners: true, votes: []*commonv1.L2Vote{vote("m1", txHash, true)}, want: constants.ErrTxL2SignerStoreNotConfigured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := tc.policy
			if p == nil {
				p = policy
			}
			var s SignerStore = signers
			if tc.noSigners {
				s = nil
			}
			err := verifyL2Quorum(context.Background(), s, p, &commonv1.L2Metadata{ConsensusSetId: "set", Votes: tc.votes}, txHash)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}
