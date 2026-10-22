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

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ConsensusVoteDecision is the decision field of a member's L2 consensus vote answer.
type ConsensusVoteDecision string

const (
	ConsensusVoteApprove ConsensusVoteDecision = "approve"
	ConsensusVoteDeny    ConsensusVoteDecision = "deny"
)

// ConsensusSubject is the governed envelope a Tribunal member votes on as
// an L2 consensus member: its classification, typed action payload, and
// decoded intent data.
type ConsensusSubject struct {
	ActionType     string
	TargetResource string
	EventType      string
	PayloadJSON    string
	IntentJSON     string
}

const consensusVoteSystemPrompt = "You are a member of the Tribunal, the consensus panel that independently votes on one governed action before it may execute on a target runtime.\n" +
	"Approve only when the action is safe, matches its stated target, and carries no sign of destructive, exfiltrating, privilege-escalating, or persistence behaviour. Deny anything else, and deny when the intent is unclear.\n" +
	"Answer with exactly one JSON object {\"decision\":\"approve\"|\"deny\",\"reason\":\"<one sentence>\"}, with no explanation or markdown.\n\n"

// ConsensusVoteJSONSchema constrains the provider's vote answer to the shape
// ParseConsensusVote accepts.
const ConsensusVoteJSONSchema = `{"additionalProperties":false,"properties":{"decision":{"enum":["approve","deny"],"type":"string"},` +
	`"reason":{"type":"string"}},"required":["decision","reason"],"type":"object"}`

// ConsensusVotePrompt returns member's sealed vote instructions and the user
// prompt rendering subject.
func ConsensusVotePrompt(member Member, subject ConsensusSubject) (system, user string) {
	var b strings.Builder
	fmt.Fprintf(&b, "<action>\nAction type: %s\nTarget resource: %s\nEvent type: %s\n</action>\n\n", subject.ActionType, subject.TargetResource, subject.EventType)
	fmt.Fprintf(&b, "<payload>\n%s\n</payload>\n\n", subject.PayloadJSON)
	fmt.Fprintf(&b, "<intent_data>\n%s\n</intent_data>\n", subject.IntentJSON)
	return consensusVoteSystemPrompt + memberStance(member), b.String()
}

// consensusVoteAnswer is the only accepted vote answer shape.
type consensusVoteAnswer struct {
	Decision ConsensusVoteDecision `json:"decision"`
	Reason   string                `json:"reason"`
}

// ConsensusVoteAnswer is a member's decoded vote answer. Err is set when the
// answer is not the strict vote shape; the vote then counts as a deny.
type ConsensusVoteAnswer struct {
	Decision ConsensusVoteDecision
	Reason   string
	Err      error
}

// Approve reports whether the answer is a strict approve.
func (a ConsensusVoteAnswer) Approve() bool {
	return a.Err == nil && a.Decision == ConsensusVoteApprove
}

// ParseConsensusVote decodes a member's vote answer. An invalid answer or any
// other decision value is a deny; no keyword is ever read out of free text.
func ParseConsensusVote(text string) ConsensusVoteAnswer {
	var answer consensusVoteAnswer
	if err := decodeAnswer(strings.TrimSpace(text), &answer); err != nil {
		return ConsensusVoteAnswer{Decision: ConsensusVoteDeny, Err: err}
	}
	if answer.Decision != ConsensusVoteApprove && answer.Decision != ConsensusVoteDeny {
		return ConsensusVoteAnswer{Decision: ConsensusVoteDeny, Reason: answer.Reason,
			Err: fmt.Errorf("decision %q: %w", answer.Decision, constants.ErrTribunalAnswerInvalid)}
	}
	return ConsensusVoteAnswer{Decision: answer.Decision, Reason: answer.Reason}
}
