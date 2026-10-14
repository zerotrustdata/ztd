// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.
package drive

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// l2Faults6 is the six row 18 faults, in catalogue order.
var l2Faults6 = []cases.Fault{
	cases.FaultL2Missing, cases.FaultL2BelowQuorum, cases.FaultL2DuplicateSigner,
	cases.FaultL2SignatureFlipped, cases.FaultL2NonMember, cases.FaultL2UnknownSet,
}

// failingL2 is an L2Source whose deliberation always fails.
type failingL2 struct{}

func (failingL2) Proof(context.Context, *commonv1.GovernanceEnvelope) (*commonv1.L2Metadata, error) {
	return nil, constants.ErrIngressL2DeliberationFailed
}

// emptyL2 is an L2Source that returns a proof with no votes.
type emptyL2 struct{}

func (emptyL2) Proof(context.Context, *commonv1.GovernanceEnvelope) (*commonv1.L2Metadata, error) {
	return &commonv1.L2Metadata{ConsensusSetId: syntheticSetID}, nil
}

// l2Envelope is a hashed baseline for one L2 fault.
func l2Envelope(t *testing.T, fault cases.Fault) (*commonv1.GovernanceEnvelope, cases.Attempt) {
	t.Helper()
	a := testAttempt(fault, cases.ActionMutation)
	env, err := Baseline(a, testIdentity(), "root-1", time.Now())
	require.NoError(t, err)
	return env, a
}

// withoutL2 returns a copy of env with governance.l2 removed. A governance
// block left empty by that removal is dropped, so a mutator that creates the
// block compares equal to one that did not.
func withoutL2(env *commonv1.GovernanceEnvelope) *commonv1.GovernanceEnvelope {
	c := proto.Clone(env).(*commonv1.GovernanceEnvelope)
	if c.Governance != nil {
		c.Governance.L2 = nil
		if proto.Size(c.Governance) == 0 {
			c.Governance = nil
		}
	}
	return c
}

// verifyVote checks one vote the way the L2 verifier does: ed25519 over "<tx>|<decision>".
func verifyVote(pub ed25519.PublicKey, txHash string, vote *commonv1.L2Vote) bool {
	sig, err := hex.DecodeString(vote.GetConsensusSignature())
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, []byte(fmt.Sprintf("%s|%v", txHash, vote.GetDecision())), sig)
}

// memberPublicKeys returns the public key of each synthetic member, in order.
func memberPublicKeys(s *SyntheticL2) []ed25519.PublicKey {
	pubs := make([]ed25519.PublicKey, 0, len(s.signers))
	for _, sg := range s.signers {
		pubs = append(pubs, sg.priv.Public().(ed25519.PublicKey))
	}
	return pubs
}

func TestL2Mutators_ChangeOnlyGovernanceL2AndLeaveHashUnchanged(t *testing.T) {
	for _, fault := range l2Faults6 {
		t.Run(string(fault), func(t *testing.T) {
			src, err := NewSyntheticL2(2)
			require.NoError(t, err)
			env, a := l2Envelope(t, fault)
			before := proto.Clone(env).(*commonv1.GovernanceEnvelope)
			hashBefore, err := governance.GenerateMessageID(before)
			require.NoError(t, err)

			m, ok := L2Mutators(src)[fault]
			require.True(t, ok, "no mutator for %s", fault)
			assert.True(t, m.SetsL2, "mutator for %s must mark that it sets L2", fault)
			require.NoError(t, m.Apply(context.Background(), env, a))

			hashAfter, err := governance.GenerateMessageID(env)
			require.NoError(t, err)
			assert.Equal(t, hashBefore, hashAfter, "a row 18 fault must not change a hashed field")
			assert.True(t, proto.Equal(withoutL2(before), withoutL2(env)), "a row 18 fault must change governance.l2 only")
		})
	}
}

func TestL2Mutators_FaultShapesAgainstValidProof(t *testing.T) {
	ctx := context.Background()
	src, err := NewSyntheticL2(2)
	require.NoError(t, err)
	pubs := memberPublicKeys(src)

	t.Run("missing leaves l2 nil", func(t *testing.T) {
		env, a := l2Envelope(t, cases.FaultL2Missing)
		require.NoError(t, L2Mutators(src)[cases.FaultL2Missing].Apply(ctx, env, a))
		assert.Nil(t, env.GetGovernance().GetL2())
	})

	t.Run("below quorum keeps the first K-1 votes", func(t *testing.T) {
		env, a := l2Envelope(t, cases.FaultL2BelowQuorum)
		valid, err := src.Proof(ctx, env)
		require.NoError(t, err)
		require.NoError(t, L2Mutators(src)[cases.FaultL2BelowQuorum].Apply(ctx, env, a))
		got := env.GetGovernance().GetL2()
		require.Len(t, got.GetVotes(), 1)
		assert.True(t, proto.Equal(valid.Votes[0], got.Votes[0]))
		assert.Equal(t, syntheticSetID, got.GetConsensusSetId())
	})

	t.Run("duplicate signer appends a copy of vote 0 to the full quorum", func(t *testing.T) {
		env, a := l2Envelope(t, cases.FaultL2DuplicateSigner)
		valid, err := src.Proof(ctx, env)
		require.NoError(t, err)
		require.NoError(t, L2Mutators(src)[cases.FaultL2DuplicateSigner].Apply(ctx, env, a))
		got := env.GetGovernance().GetL2().GetVotes()
		require.Len(t, got, 3)
		for i := range valid.Votes {
			assert.True(t, proto.Equal(valid.Votes[i], got[i]), "vote %d changed", i)
		}
		assert.True(t, proto.Equal(valid.Votes[0], got[2]), "last vote must copy vote 0")
	})

	t.Run("signature flipped changes one hex digit of vote 0 and fails verification", func(t *testing.T) {
		env, a := l2Envelope(t, cases.FaultL2SignatureFlipped)
		valid, err := src.Proof(ctx, env)
		require.NoError(t, err)
		require.NoError(t, L2Mutators(src)[cases.FaultL2SignatureFlipped].Apply(ctx, env, a))
		got := env.GetGovernance().GetL2().GetVotes()
		require.Len(t, got, 2)

		was, now := valid.Votes[0].GetConsensusSignature(), got[0].GetConsensusSignature()
		require.Len(t, now, len(was))
		diff := 0
		for i := range was {
			if was[i] != now[i] {
				diff++
			}
		}
		assert.Equal(t, 1, diff, "exactly one hex digit must change")
		assert.True(t, verifyVote(pubs[0], env.GetTransactionHash(), valid.Votes[0]), "valid vote must verify")
		assert.False(t, verifyVote(pubs[0], env.GetTransactionHash(), got[0]), "flipped vote must not verify")
		assert.True(t, proto.Equal(valid.Votes[1], got[1]), "vote 1 must be untouched")
	})

	t.Run("non-member replaces vote 0 with a vote from a key outside the set", func(t *testing.T) {
		env, a := l2Envelope(t, cases.FaultL2NonMember)
		valid, err := src.Proof(ctx, env)
		require.NoError(t, err)
		require.NoError(t, L2Mutators(src)[cases.FaultL2NonMember].Apply(ctx, env, a))
		got := env.GetGovernance().GetL2().GetVotes()
		require.Len(t, got, 2)
		assert.Equal(t, nonMemberSignerID, got[0].GetSignerKeyId())
		assert.Equal(t, valid.Votes[0].GetDecision(), got[0].GetDecision())
		assert.True(t, proto.Equal(valid.Votes[1], got[1]), "vote 1 must be untouched")
		for _, pub := range pubs {
			assert.False(t, verifyVote(pub, env.GetTransactionHash(), got[0]), "non-member vote must not verify under a member key")
		}
	})

	t.Run("unknown set names the unknown consensus set and keeps the votes", func(t *testing.T) {
		env, a := l2Envelope(t, cases.FaultL2UnknownSet)
		valid, err := src.Proof(ctx, env)
		require.NoError(t, err)
		require.NoError(t, L2Mutators(src)[cases.FaultL2UnknownSet].Apply(ctx, env, a))
		got := env.GetGovernance().GetL2()
		assert.Equal(t, unknownSetID, got.GetConsensusSetId())
		assert.Len(t, got.GetVotes(), len(valid.Votes))
	})
}

func TestNonMemberVote_VerifiesAgainstItsOwnFreshKeyOnly(t *testing.T) {
	src, err := NewSyntheticL2(2)
	require.NoError(t, err)
	env := mustEnvelope(t, cases.FaultL2NonMember)
	txHash, err := governance.GenerateMessageID(env)
	require.NoError(t, err)

	vote, pub, err := nonMemberVote(txHash, true)
	require.NoError(t, err)
	assert.True(t, verifyVote(pub, txHash, vote), "non-member vote must verify against its own key over the same message")
	assert.False(t, verifyVote(pub, "other-tx", vote), "non-member vote must not verify over another message")

	for _, memberPub := range memberPublicKeys(src) {
		assert.False(t, verifyVote(memberPub, txHash, vote), "non-member vote must not verify under a member key")
	}
}

func TestSyntheticL2_ProofVerifiesUnderItsMemberKeys(t *testing.T) {
	ctx := context.Background()
	src, err := NewSyntheticL2(3)
	require.NoError(t, err)
	env := mustEnvelope(t, cases.FaultNone)
	txHash, err := governance.GenerateMessageID(env)
	require.NoError(t, err)

	proof, err := src.Proof(ctx, env)
	require.NoError(t, err)
	assert.Equal(t, syntheticSetID, proof.GetConsensusSetId())
	require.Len(t, proof.GetVotes(), 3)
	for i, vote := range proof.GetVotes() {
		assert.True(t, vote.GetDecision())
		assert.True(t, verifyVote(memberPublicKeys(src)[i], txHash, vote), "vote %d must verify", i)
	}

	env.StateMerkleRoot = "root-2"
	other, err := governance.GenerateMessageID(env)
	require.NoError(t, err)
	assert.False(t, verifyVote(memberPublicKeys(src)[0], other, proof.GetVotes()[0]), "a proof must not verify for another message")
}

func TestL2Mutators_FailWhenTheProofCannotBeCut(t *testing.T) {
	ctx := context.Background()
	one, err := NewSyntheticL2(1)
	require.NoError(t, err)

	tests := []struct {
		name  string
		fault cases.Fault
		src   L2Source
	}{
		{"below quorum with a single vote", cases.FaultL2BelowQuorum, one},
		{"duplicate signer with no votes", cases.FaultL2DuplicateSigner, emptyL2{}},
		{"signature flipped with no votes", cases.FaultL2SignatureFlipped, emptyL2{}},
		{"non-member with no votes", cases.FaultL2NonMember, emptyL2{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, a := l2Envelope(t, tt.fault)
			err := L2Mutators(tt.src)[tt.fault].Apply(ctx, env, a)
			require.ErrorIs(t, err, constants.ErrMissingRequiredField)
			assert.Nil(t, env.GetGovernance().GetL2(), "a failed mutator must not write governance.l2")
		})
	}
}

func TestL2Mutators_PropagateDeliberationFailure(t *testing.T) {
	for _, fault := range l2Faults6 {
		t.Run(string(fault), func(t *testing.T) {
			env, a := l2Envelope(t, fault)
			err := L2Mutators(failingL2{})[fault].Apply(context.Background(), env, a)
			require.Error(t, err)
			assert.True(t, errors.Is(err, constants.ErrIngressL2DeliberationFailed), "error must wrap the deliberation sentinel: %v", err)
		})
	}
}

func TestSyntheticL2_RejectsZeroVotesAndNilEnvelope(t *testing.T) {
	_, err := NewSyntheticL2(0)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)

	src, err := NewSyntheticL2(2)
	require.NoError(t, err)
	_, err = src.Proof(context.Background(), nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

// mustEnvelope is a hashed baseline for fault, for tests that need only the envelope.
func mustEnvelope(t *testing.T, fault cases.Fault) *commonv1.GovernanceEnvelope {
	t.Helper()
	env, _ := l2Envelope(t, fault)
	return env
}
