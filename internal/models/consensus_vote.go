// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import "time"

// ConsensusVoteOutcome is what one consensus member contributed to an
// envelope's L2 deliberation.
type ConsensusVoteOutcome string

const (
	// ConsensusVoteOutcomeApprove is a signed approve vote.
	ConsensusVoteOutcomeApprove ConsensusVoteOutcome = "approve"
	// ConsensusVoteOutcomeDeny is a signed deny vote, including an answer
	// that was not a strict vote.
	ConsensusVoteOutcomeDeny ConsensusVoteOutcome = "deny"
	// ConsensusVoteOutcomeNoVote means the member cast no vote: it could not
	// run its inference, sign, or have its vote recorded.
	ConsensusVoteOutcomeNoVote ConsensusVoteOutcome = "no_vote"
)

// ConsensusVoteRecord is one member's vote on one governed transaction, as the
// Gateway records it in its audit log (event ztd.v1.consensus.vote.recorded)
// before the deliberated envelope reaches L4.
type ConsensusVoteRecord struct {
	// TransactionID is the envelope ID, the ID its receipts carry.
	TransactionID   string               `json:"transaction_id"`
	TransactionHash string               `json:"transaction_hash"`
	ConsensusSetID  string               `json:"consensus_set_id"`
	MemberAppID     string               `json:"member_app_id"`
	Outcome         ConsensusVoteOutcome `json:"outcome"`
	// Reason is the member's own stated reason for its decision.
	Reason string `json:"reason,omitempty"`
	// Failure explains a deny read from an invalid answer, or why no vote was cast.
	Failure string `json:"failure,omitempty"`
	// InferenceTransactionID is the member's governed INFERENCE transaction.
	InferenceTransactionID string    `json:"inference_transaction_id,omitempty"`
	ProviderAttemptID      string    `json:"provider_attempt_id,omitempty"`
	ModelTag               string    `json:"model_tag,omitempty"`
	Quorum                 int       `json:"quorum"`
	RecordedAt             time.Time `json:"recorded_at"`
}

// ConsensusVotesResponse is the typed response for GET /api/v1/audit/consensus-votes.
type ConsensusVotesResponse struct {
	Success       bool                  `json:"success"`
	TransactionID string                `json:"transaction_id"`
	Votes         []ConsensusVoteRecord `json:"votes"`
}
