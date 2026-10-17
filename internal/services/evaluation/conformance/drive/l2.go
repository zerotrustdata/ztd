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
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// syntheticSetID is the consensus set of SyntheticL2 proofs. No Gateway knows it.
const syntheticSetID = "conformance-synthetic-set"

// unknownSetID is the consensus set that row 18 names as unknown to the Gateway.
const unknownSetID = "conformance-unknown-set"

// nonMemberSignerID signs the row 18 non-member vote. It belongs to no set.
const nonMemberSignerID = "conformance-non-member"

// Deliberator is an L2Source that gets a valid L2 proof for a final envelope
// from the Gateway's bound consensus application's HTTPS protobuf endpoint.
type Deliberator struct {
	client  *client.Client
	persona client.Persona
}

// NewDeliberator returns a deliberator that posts as persona p.
func NewDeliberator(c *client.Client, p client.Persona) *Deliberator {
	return &Deliberator{client: c, persona: p}
}

// Proof returns the L2 proof the bound application attaches to env.
func (d *Deliberator) Proof(ctx context.Context, env *commonv1.GovernanceEnvelope) (*commonv1.L2Metadata, error) {
	if d.client == nil || env == nil {
		return nil, fmt.Errorf("deliberator proof: %w", constants.ErrMissingRequiredField)
	}
	proof, err := d.client.Deliberate(ctx, d.persona, env)
	if err != nil {
		return nil, fmt.Errorf("deliberator proof: %w", err)
	}
	return proof, nil
}

// SyntheticL2 is an L2Source for postures without a deliberation endpoint. It
// signs k affirmative votes with fresh ed25519 keys under a set id no Gateway
// knows. The votes are well formed, so the proof reaches the same checks as a
// real one.
type SyntheticL2 struct {
	setID   string
	signers []syntheticSigner
}

// syntheticSigner is one synthetic member: its key id and private key.
type syntheticSigner struct {
	keyID string
	priv  ed25519.PrivateKey
}

// NewSyntheticL2 returns a source of proofs with k votes, one per fresh key.
func NewSyntheticL2(k int) (*SyntheticL2, error) {
	if k < 1 {
		return nil, fmt.Errorf("synthetic l2: votes %d: %w", k, constants.ErrMissingRequiredField)
	}
	signers := make([]syntheticSigner, 0, k)
	for i := range k {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("synthetic l2: generate key %d: %w", i, err)
		}
		signers = append(signers, syntheticSigner{keyID: fmt.Sprintf("%s-member-%d", syntheticSetID, i), priv: priv})
	}
	return &SyntheticL2{setID: syntheticSetID, signers: signers}, nil
}

// Proof signs every synthetic vote affirmatively over the canonical message id of env.
func (s *SyntheticL2) Proof(_ context.Context, env *commonv1.GovernanceEnvelope) (*commonv1.L2Metadata, error) {
	if env == nil {
		return nil, fmt.Errorf("synthetic l2 proof: %w", constants.ErrMissingRequiredField)
	}
	txHash, err := messageID(env)
	if err != nil {
		return nil, fmt.Errorf("synthetic l2 proof: %w", err)
	}
	votes := make([]*commonv1.L2Vote, 0, len(s.signers))
	for _, sg := range s.signers {
		votes = append(votes, signVote(sg.keyID, sg.priv, txHash, true))
	}
	return &commonv1.L2Metadata{ConsensusSetId: s.setID, Votes: votes}, nil
}

// L2Mutators returns one mutator per row 18 fault. Each takes the valid proof
// from src for the final envelope, changes exactly one thing in it, and writes
// the result to governance.l2. None changes a hashed field.
func L2Mutators(src L2Source) MutatorSet {
	return MutatorSet{
		cases.FaultL2Missing: l2Mutator(src, func(*commonv1.GovernanceEnvelope, *commonv1.L2Metadata) (*commonv1.L2Metadata, error) {
			return nil, nil
		}),
		cases.FaultL2BelowQuorum: l2Mutator(src, func(_ *commonv1.GovernanceEnvelope, base *commonv1.L2Metadata) (*commonv1.L2Metadata, error) {
			k := len(base.GetVotes())
			if k < 1 {
				return nil, fmt.Errorf("below quorum: proof holds %d votes: %w", k, constants.ErrMissingRequiredField)
			}
			base.Votes = base.Votes[:k-1]
			return base, nil
		}),
		cases.FaultL2DuplicateSigner: l2Mutator(src, func(_ *commonv1.GovernanceEnvelope, base *commonv1.L2Metadata) (*commonv1.L2Metadata, error) {
			if len(base.GetVotes()) == 0 {
				return nil, fmt.Errorf("duplicate signer: proof holds no votes: %w", constants.ErrMissingRequiredField)
			}
			base.Votes = append(base.Votes, proto.Clone(base.Votes[0]).(*commonv1.L2Vote))
			return base, nil
		}),
		cases.FaultL2SignatureFlipped: l2Mutator(src, func(_ *commonv1.GovernanceEnvelope, base *commonv1.L2Metadata) (*commonv1.L2Metadata, error) {
			if len(base.GetVotes()) == 0 || base.Votes[0].GetConsensusSignature() == "" {
				return nil, fmt.Errorf("signature flipped: vote 0 has no signature: %w", constants.ErrMissingRequiredField)
			}
			base.Votes[0].ConsensusSignature = flipFirstHexDigit(base.Votes[0].ConsensusSignature)
			return base, nil
		}),
		cases.FaultL2NonMember: l2Mutator(src, func(env *commonv1.GovernanceEnvelope, base *commonv1.L2Metadata) (*commonv1.L2Metadata, error) {
			if len(base.GetVotes()) == 0 {
				return nil, fmt.Errorf("non-member: proof holds no votes: %w", constants.ErrMissingRequiredField)
			}
			txHash, err := messageID(env)
			if err != nil {
				return nil, fmt.Errorf("non-member: %w", err)
			}
			vote, _, err := nonMemberVote(txHash, base.Votes[0].GetDecision())
			if err != nil {
				return nil, fmt.Errorf("non-member: %w", err)
			}
			base.Votes[0] = vote
			return base, nil
		}),
		cases.FaultL2UnknownSet: l2Mutator(src, func(_ *commonv1.GovernanceEnvelope, base *commonv1.L2Metadata) (*commonv1.L2Metadata, error) {
			base.ConsensusSetId = unknownSetID
			return base, nil
		}),
	}
}

// l2Mutator builds a SetsL2 mutator from change. change receives the final
// envelope and a copy of its valid proof, and returns the L2 metadata to write
// (nil removes it).
func l2Mutator(src L2Source, change func(env *commonv1.GovernanceEnvelope, base *commonv1.L2Metadata) (*commonv1.L2Metadata, error)) Mutator {
	return Mutator{SetsL2: true, Apply: func(ctx context.Context, env *commonv1.GovernanceEnvelope, a cases.Attempt) error {
		base, err := src.Proof(ctx, env)
		if err != nil {
			return fmt.Errorf("l2 mutator %s: base proof: %w", a.Case.Fault, err)
		}
		l2, err := change(env, proto.Clone(base).(*commonv1.L2Metadata))
		if err != nil {
			return fmt.Errorf("l2 mutator %s: %w", a.Case.Fault, err)
		}
		if env.Governance == nil {
			env.Governance = &commonv1.GovernanceMetadata{}
		}
		env.Governance.L2 = l2
		return nil
	}}
}

// messageID is the canonical hash the L2 signatures cover.
func messageID(env *commonv1.GovernanceEnvelope) (string, error) {
	id, err := governance.GenerateMessageID(env)
	if err != nil {
		return "", fmt.Errorf("message id: %w", err)
	}
	return id, nil
}

// signVote signs "<txHash>|<decision>" with priv, as the L2 verifier expects.
func signVote(keyID string, priv ed25519.PrivateKey, txHash string, decision bool) *commonv1.L2Vote {
	sig := ed25519.Sign(priv, []byte(fmt.Sprintf("%s|%v", txHash, decision)))
	return &commonv1.L2Vote{
		SignerKeyId:        keyID,
		ConsensusSignature: hex.EncodeToString(sig),
		Decision:           decision,
	}
}

// nonMemberVote signs the message with a fresh key and returns the vote with
// its public key, so a test can check the signature.
func nonMemberVote(txHash string, decision bool) (*commonv1.L2Vote, ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("non-member vote: generate key: %w", err)
	}
	return signVote(nonMemberSignerID, priv, txHash, decision), pub, nil
}

// flipFirstHexDigit changes the first hex digit of sig to a different digit.
func flipFirstHexDigit(sig string) string {
	b := []byte(sig)
	if b[0] == '0' {
		b[0] = '1'
	} else {
		b[0] = '0'
	}
	return string(b)
}
