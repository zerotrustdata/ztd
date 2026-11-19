// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import "fmt"

// ConsensusProvider names the vote producer of a bound L2 consensus. It is the
// single owner of provider names; every stored binding must specify one.
type ConsensusProvider string

// Consensus providers.
const (
	// ConsensusProviderApplication: an enrolled application's HTTPS
	// deliberation endpoint returns signed votes.
	ConsensusProviderApplication ConsensusProvider = "application"
	// ConsensusProviderTribunal: the Gateway-hosted Tribunal panel votes with
	// its members' sealed app signing keys over a pinned model.
	ConsensusProviderTribunal ConsensusProvider = "tribunal"
)

// ConsensusNotReadyReason names why a bound consensus is not ready. It is a
// closed code, so the unauthenticated /health summary never carries member
// IDs, the pinned model, or an Operator ID.
type ConsensusNotReadyReason string

// Consensus not-ready reasons. Readiness reports the owner or vote-route
// reason of the first member, in member order, whose vote cannot route, and
// otherwise signers.
const (
	// ConsensusNotReadyMemberOwner: a Tribunal member with a usable key has no
	// enrolled owner, so its vote has no requestor.
	ConsensusNotReadyMemberOwner ConsensusNotReadyReason = "member_owner"
	// ConsensusNotReadyVoteOperator: no active Inference Operator of the
	// member owner serves the binding's model provider (the hosted class, or
	// the local provider for an empty class), or the pinned decision Operator
	// does not serve the decision vote.
	ConsensusNotReadyVoteOperator ConsensusNotReadyReason = "vote_operator"
	// ConsensusNotReadyVoteOperatorAmbiguous: more than one active Inference
	// Operator could serve the vote and none is pinned.
	ConsensusNotReadyVoteOperatorAmbiguous ConsensusNotReadyReason = "vote_operator_ambiguous"
	// ConsensusNotReadySigners: fewer members than the quorum have an enabled
	// signer (and, for the Tribunal, a sealed key on this host).
	ConsensusNotReadySigners ConsensusNotReadyReason = "signers"
)

// ParseConsensusProvider decodes a stored or requested provider. The provider
// is required; empty is invalid.
func ParseConsensusProvider(value string) (ConsensusProvider, error) {
	switch ConsensusProvider(value) {
	case ConsensusProviderApplication:
		return ConsensusProviderApplication, nil
	case ConsensusProviderTribunal:
		return ConsensusProviderTribunal, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrConsensusProviderInvalid, value)
	}
}

// Tribunal consensus membership. The member app IDs are the canonical
// tribunal panel (tribunal.AllMembers) prefixed with TribunalConsensusAppIDPrefix,
// in canonical order. tribunal.ConsensusMemberAppIDs derives from the panel
// and is asserted equal to this list.
const (
	TribunalConsensusAppIDPrefix = "tribunal-"
	// TribunalConsensusDefaultQuorum is the default K of the 3-of-5 Tribunal consensus.
	TribunalConsensusDefaultQuorum = 3
	// TribunalConsensusMinQuorum is the lowest K a Tribunal binding accepts.
	TribunalConsensusMinQuorum = 3
)

// TribunalConsensusMemberAppIDs returns the canonical Tribunal consensus member
// app IDs in canonical order. The first is the binding's primary app.
func TribunalConsensusMemberAppIDs() []string {
	return []string{
		TribunalConsensusAppIDPrefix + "axiom",
		TribunalConsensusAppIDPrefix + "concord",
		TribunalConsensusAppIDPrefix + "variance",
		TribunalConsensusAppIDPrefix + "pragma",
		TribunalConsensusAppIDPrefix + "nemesis",
	}
}
