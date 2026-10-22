// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operator

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Getter fetches a Gateway API path and returns the raw 2xx body.
type Getter interface {
	Get(path string) ([]byte, error)
}

// ConsensusVotesPath is the Gateway audit path for txID's recorded L2 votes.
func ConsensusVotesPath(txID string) string {
	query := url.Values{}
	query.Set("tx_id", txID)
	return constants.APIPaths.AuditConsensusVotes + "?" + query.Encode()
}

// FetchConsensusVotes reads every recorded L2 member vote on txID.
func FetchConsensusVotes(client Getter, txID string) (models.ConsensusVotesResponse, error) {
	raw, err := client.Get(ConsensusVotesPath(txID))
	if err != nil {
		return models.ConsensusVotesResponse{}, fmt.Errorf("fetch consensus votes: %w", err)
	}
	var resp models.ConsensusVotesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return models.ConsensusVotesResponse{}, fmt.Errorf("fetch consensus votes: %w: %w", constants.ErrInvalidJSONResponse, err)
	}
	return resp, nil
}

// AttachConsensusVotes fetches the recorded member votes for every result
// rejected because L2 quorum was not met, so the caller sees why.
func AttachConsensusVotes(client Getter, results []RunResult) {
	for i := range results {
		result := &results[i]
		if result.TransactionID == "" || result.RejectionCode != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET.String() {
			continue
		}
		resp, err := FetchConsensusVotes(client, result.TransactionID)
		if err != nil {
			result.ConsensusVotesError = err.Error()
			continue
		}
		result.ConsensusVotes = resp.Votes
	}
}

// WriteConsensusVotes prints one block per member vote, in deliberation order,
// each line prefixed by indent.
func WriteConsensusVotes(w io.Writer, indent string, txID string, votes []models.ConsensusVoteRecord) {
	if len(votes) == 0 {
		fmt.Fprintf(w, "%sNo consensus votes recorded for transaction %s\n", indent, txID)
		return
	}
	first := votes[0]
	fmt.Fprintf(w, "%sConsensus votes for transaction %s (set %s, model %s, quorum %d):\n", indent, txID, first.ConsensusSetID, first.ModelTag, first.Quorum)
	approvals := 0
	for _, vote := range votes {
		if vote.Outcome == models.ConsensusVoteOutcomeApprove {
			approvals++
		}
		fmt.Fprintf(w, "%s  %-9s %s  %s\n", indent, vote.Outcome, vote.RecordedAt.Format(time.RFC3339), vote.MemberAppID)
		if vote.Reason != "" {
			fmt.Fprintf(w, "%s            reason: %s\n", indent, vote.Reason)
		}
		if vote.Failure != "" {
			fmt.Fprintf(w, "%s            failure: %s\n", indent, vote.Failure)
		}
		if vote.InferenceTransactionID != "" {
			fmt.Fprintf(w, "%s            inference: %s\n", indent, vote.InferenceTransactionID)
		}
	}
	fmt.Fprintf(w, "%sApprovals: %d of %d votes, quorum %d\n", indent, approvals, len(votes), first.Quorum)
}
