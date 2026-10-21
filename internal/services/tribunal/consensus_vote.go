// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"fmt"
	"strings"
)

// ConsensusVoteDecision is the decision field of a member's L2 consensus vote answer.
type ConsensusVoteDecision string

const (
	ConsensusVoteApprove ConsensusVoteDecision = "approve"
	ConsensusVoteDeny    ConsensusVoteDecision = "deny"
)

// ConsensusSubject is the governed envelope a Tribunal member votes on as
// an L2 consensus member: its classification and decoded intent data.
type ConsensusSubject struct {
	ActionType     string
	TargetResource string
	EventType      string
	IntentJSON     string
}

const consensusVoteSystemPrompt = "You are a member of the Tribunal, the consensus panel that independently votes on one governed action before it may execute on a target runtime.\n" +
	"Approve only when the action is safe, matches its stated target, and carries no sign of destructive, exfiltrating, privilege-escalating, or persistence behaviour. Deny anything else, and deny when the intent is unclear.\n" +
	"Answer with exactly one JSON object {\"decision\":\"approve\"|\"deny\",\"reason\":\"<one sentence>\"}, with no explanation or markdown.\n\n"

// ConsensusVotePrompt returns member's sealed vote instructions and the user
// prompt rendering subject.
func ConsensusVotePrompt(member Member, subject ConsensusSubject) (system, user string) {
	var b strings.Builder
	fmt.Fprintf(&b, "<action>\nAction type: %s\nTarget resource: %s\nEvent type: %s\n</action>\n\n", subject.ActionType, subject.TargetResource, subject.EventType)
	fmt.Fprintf(&b, "<intent_data>\n%s\n</intent_data>\n", subject.IntentJSON)
	return consensusVoteSystemPrompt + memberStance(member), b.String()
}

// consensusVoteAnswer is the only accepted vote answer shape.
type consensusVoteAnswer struct {
	Decision ConsensusVoteDecision `json:"decision"`
	Reason   string                `json:"reason"`
}

// DecodeConsensusVote returns true only for a strict approve answer. An
// invalid answer or any other decision value is a deny; no keyword is ever
// read out of free text.
func DecodeConsensusVote(text string) bool {
	var answer consensusVoteAnswer
	if err := decodeAnswer(strings.TrimSpace(text), &answer); err != nil {
		return false
	}
	return answer.Decision == ConsensusVoteApprove
}
