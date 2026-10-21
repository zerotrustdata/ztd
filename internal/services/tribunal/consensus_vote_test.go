// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsensusVotePromptSystemEndsWithMemberStance(t *testing.T) {
	for _, member := range AllMembers {
		system, _ := ConsensusVotePrompt(member, ConsensusSubject{})
		require.True(t, strings.HasPrefix(system, consensusVoteSystemPrompt), "member %s", member)
		require.True(t, strings.HasSuffix(system, memberStance(member)), "member %s", member)
	}
}

func TestConsensusVotePromptUserRendersSubject(t *testing.T) {
	subject := ConsensusSubject{
		ActionType:     "run_shell_command",
		TargetResource: "host-7",
		EventType:      "EXECUTION_REQUESTED",
		IntentJSON:     `{"command":"ls","args":["-l"]}`,
	}
	for _, member := range AllMembers {
		_, user := ConsensusVotePrompt(member, subject)
		require.Equal(t, "<action>\nAction type: run_shell_command\nTarget resource: host-7\nEvent type: EXECUTION_REQUESTED\n</action>\n\n"+
			"<intent_data>\n"+subject.IntentJSON+"\n</intent_data>\n", user, "member %s", member)
	}
}

func TestDecodeConsensusVote(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "approve", text: `{"decision":"approve","reason":"ok"}`, want: true},
		{name: "approve with surrounding whitespace", text: "  \n{\"decision\":\"approve\",\"reason\":\"ok\"}\t\n", want: true},
		{name: "deny", text: `{"decision":"deny","reason":"destructive"}`, want: false},
		{name: "uppercase approve", text: `{"decision":"APPROVE","reason":"ok"}`, want: false},
		{name: "unknown decision", text: `{"decision":"maybe","reason":"unsure"}`, want: false},
		{name: "empty string", text: "", want: false},
		{name: "free text approve", text: "approve", want: false},
		{name: "invalid JSON", text: `{"decision":"approve",`, want: false},
		{name: "unknown field rejected", text: `{"decision":"approve","reason":"ok","extra":true}`, want: false},
		{name: "markdown fenced rejected", text: "```json\n{\"decision\":\"approve\",\"reason\":\"ok\"}\n```", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, DecodeConsensusVote(tt.text))
		})
	}
}
