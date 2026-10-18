// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const minConsensusThreshold = 2

// RunVoting performs clustering, weighted voting, tie-breaking, and optional
// Round 2 anonymized peer review.
func RunVoting(ctx context.Context, cfg PipelineConfig, req PipelineRequest, candidates []Candidate) (*VoteResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("tribunal: voting: no candidates provided")
	}

	// 1. Group candidates into clusters by normalized command
	clusters := make(map[string][]Member)
	for _, c := range candidates {
		cmd := NormaliseCommand(c.Command)
		if cmd == "" {
			continue
		}
		clusters[cmd] = append(clusters[cmd], c.Member)
	}

	var round1Votes []CandidateVote
	for _, c := range candidates {
		cmd := NormaliseCommand(c.Command)
		round1Votes = append(round1Votes, CandidateVote{
			VoterMember:    string(c.Member),
			CandidateLabel: cmd,
		})
	}

	winner, tied := evaluateClusters(clusters)
	if winner != "" && !tied {
		return &VoteResult{
			WinnerCommand:    winner,
			Votes:            round1Votes,
			Rounds:           1,
			ConsensusReached: true,
			Clusters:         clusters,
		}, nil
	}

	// 2. If consensus < 2 or tie cannot be resolved, execute Round 2: Anonymized Peer Review
	r2Result, err := runPeerReviewRound(ctx, cfg, req, candidates, clusters)
	if err != nil {
		// If peer review fails, fallback to best-effort round 1 top candidate if available
		if winner != "" {
			return &VoteResult{
				WinnerCommand:    winner,
				Votes:            round1Votes,
				Rounds:           1,
				ConsensusReached: true,
				Clusters:         clusters,
			}, nil
		}
		return &VoteResult{
			Votes:            round1Votes,
			Rounds:           1,
			ConsensusReached: false,
			Clusters:         clusters,
		}, nil
	}

	return r2Result, nil
}

// evaluateClusters selects the winning command or reports an unresolved tie.
func evaluateClusters(clusters map[string][]Member) (winner string, tied bool) {
	if len(clusters) == 0 {
		return "", false
	}

	type clusterInfo struct {
		cmd     string
		members []Member
		votes   int
	}

	var list []clusterInfo
	for cmd, members := range clusters {
		list = append(list, clusterInfo{
			cmd:     cmd,
			members: members,
			votes:   len(members),
		})
	}

	// Sort by votes descending
	sort.Slice(list, func(i, j int) bool {
		return list[i].votes > list[j].votes
	})

	maxVotes := list[0].votes
	if maxVotes < minConsensusThreshold {
		return "", false
	}

	// Check for ties at maxVotes
	var top []clusterInfo
	for _, item := range list {
		if item.votes == maxVotes {
			top = append(top, item)
		}
	}

	if len(top) == 1 {
		return top[0].cmd, false
	}

	// Deterministic tie-breaking ladder:
	// Step 1: Shortest command string
	shortestLen := len(top[0].cmd)
	for _, item := range top[1:] {
		if len(item.cmd) < shortestLen {
			shortestLen = len(item.cmd)
		}
	}
	var shortest []clusterInfo
	for _, item := range top {
		if len(item.cmd) == shortestLen {
			shortest = append(shortest, item)
		}
	}
	if len(shortest) == 1 {
		return shortest[0].cmd, false
	}

	// Step 2: Cluster that does NOT include Nemesis
	var nonNemesis []clusterInfo
	for _, item := range shortest {
		hasNemesis := false
		for _, m := range item.members {
			if m == MemberNemesis {
				hasNemesis = true
				break
			}
		}
		if !hasNemesis {
			nonNemesis = append(nonNemesis, item)
		}
	}
	if len(nonNemesis) == 1 {
		return nonNemesis[0].cmd, false
	}

	// Still tied: trigger Round 2
	return "", true
}

func runPeerReviewRound(
	ctx context.Context,
	cfg PipelineConfig,
	req PipelineRequest,
	candidates []Candidate,
	clusters map[string][]Member,
) (*VoteResult, error) {
	// Label clusters anonymously: cluster_a, cluster_b, ...
	clusterLabels := make(map[string]string) // cluster_label -> command
	revLabels := make(map[string]string)     // command -> cluster_label

	var clusterList []string
	for cmd := range clusters {
		clusterList = append(clusterList, cmd)
	}
	sort.Strings(clusterList)

	for i, cmd := range clusterList {
		label := fmt.Sprintf("cluster_%c", 'a'+i)
		clusterLabels[label] = cmd
		revLabels[cmd] = label
	}

	var clusterPrompt strings.Builder
	clusterPrompt.WriteString("The panel proposed the following candidate command clusters:\n")
	for _, cmd := range clusterList {
		label := revLabels[cmd]
		count := len(clusters[cmd])
		fmt.Fprintf(&clusterPrompt, "[%s] (supported by %d members):\n%s\n\n", label, count, cmd)
	}
	clusterPrompt.WriteString("Select the best cluster that accomplishes the user request safely and accurately.\n" +
		`Respond ONLY with JSON: {"cluster": "<cluster_label>", "rationale": "<short rationale>"}`)

	r2Votes := make([]CandidateVote, 0, len(AllMembers))
	r2Clusters := make(map[string][]Member)
	var txIDs []string
	var receipts []*operatorv1.ActionReceipt

	for _, member := range AllMembers {
		attemptID := cfg.NewID(fmt.Sprintf("tribunal-r2-%s-%d", member, req.Turn))
		dispatchReq := buildDispatchRequest(cfg, operatorv1.ModelRole_MODEL_ROLE_LITE, attemptID, "You are participating in Round 2 Peer Review for the Tribunal.", formatUserPrompt(req)+"\n\n"+clusterPrompt.String())

		resp, err := cfg.Dispatcher.DispatchInference(ctx, dispatchReq)
		if err != nil {
			continue
		}
		if resp.GetTransactionId() != "" {
			txIDs = append(txIDs, resp.GetTransactionId())
		}
		if resp.GetReceipt() != nil {
			receipts = append(receipts, resp.GetReceipt())
		}

		text := collectTextParts(resp.GetResult())
		selectedLabel := parseSelectedCluster(text)
		chosenCmd := clusterLabels[selectedLabel]
		if chosenCmd == "" {
			// If invalid response, default to member's original round 1 choice
			for _, c := range candidates {
				if c.Member == member {
					chosenCmd = NormaliseCommand(c.Command)
					break
				}
			}
		}

		if chosenCmd != "" {
			r2Votes = append(r2Votes, CandidateVote{
				VoterMember:    string(member),
				CandidateLabel: chosenCmd,
			})
			r2Clusters[chosenCmd] = append(r2Clusters[chosenCmd], member)
		}
	}

	winner, _ := evaluateClusters(r2Clusters)
	if winner == "" {
		// Pick cluster with most votes or first
		for cmd := range r2Clusters {
			winner = cmd
			break
		}
	}

	return &VoteResult{
		WinnerCommand:    winner,
		Votes:            r2Votes,
		Rounds:           2,
		ConsensusReached: len(r2Clusters[winner]) >= minConsensusThreshold,
		Clusters:         r2Clusters,
		TransactionIDs:   txIDs,
		Receipts:         receipts,
	}, nil
}

type clusterSelectionJSON struct {
	Cluster string `json:"cluster"`
}

func parseSelectedCluster(text string) string {
	cleaned := strings.TrimSpace(text)
	if idx := strings.Index(cleaned, "{"); idx != -1 {
		cleaned = cleaned[idx:]
	}
	if idx := strings.LastIndex(cleaned, "}"); idx != -1 {
		cleaned = cleaned[:idx+1]
	}
	var res clusterSelectionJSON
	if err := json.Unmarshal([]byte(cleaned), &res); err == nil && res.Cluster != "" {
		return strings.ToLower(strings.TrimSpace(res.Cluster))
	}
	// Fallback to substring search
	for _, opt := range []string{"cluster_a", "cluster_b", "cluster_c", "cluster_d", "cluster_e"} {
		if strings.Contains(strings.ToLower(text), opt) {
			return opt
		}
	}
	return ""
}
