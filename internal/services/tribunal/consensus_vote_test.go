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

	"github.com/zerotrustdata/ztd/internal/constants"
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
		PayloadJSON:    `{"command":"ls -l"}`,
		IntentJSON:     `{"command":"ls","args":["-l"]}`,
	}
	for _, member := range AllMembers {
		_, user := ConsensusVotePrompt(member, subject)
		require.Equal(t, "<action>\nAction type: run_shell_command\nTarget resource: host-7\nEvent type: EXECUTION_REQUESTED\n</action>\n\n"+
			"<payload>\n"+subject.PayloadJSON+"\n</payload>\n\n"+
			"<intent_data>\n"+subject.IntentJSON+"\n</intent_data>\n", user, "member %s", member)
	}
}

func TestParseConsensusVote(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		want       bool
		wantReason string
		wantErr    bool
	}{
		{name: "approve", text: `{"decision":"approve","reason":"ok"}`, want: true, wantReason: "ok"},
		{name: "approve with surrounding whitespace", text: "  \n{\"decision\":\"approve\",\"reason\":\"ok\"}\t\n", want: true, wantReason: "ok"},
		{name: "deny keeps its reason", text: `{"decision":"deny","reason":"destructive"}`, wantReason: "destructive"},
		{name: "uppercase approve", text: `{"decision":"APPROVE","reason":"ok"}`, wantReason: "ok", wantErr: true},
		{name: "unknown decision", text: `{"decision":"maybe","reason":"unsure"}`, wantReason: "unsure", wantErr: true},
		{name: "empty string", text: "", wantErr: true},
		{name: "free text approve", text: "approve", wantErr: true},
		{name: "invalid JSON", text: `{"decision":"approve",`, wantErr: true},
		{name: "unknown field rejected", text: `{"decision":"approve","reason":"ok","extra":true}`, wantErr: true},
		{name: "markdown fenced rejected", text: "```json\n{\"decision\":\"approve\",\"reason\":\"ok\"}\n```", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answer := ParseConsensusVote(tt.text)
			require.Equal(t, tt.want, answer.Approve())
			require.Equal(t, tt.wantReason, answer.Reason)
			if tt.wantErr {
				require.ErrorIs(t, answer.Err, constants.ErrTribunalAnswerInvalid)
				require.Equal(t, ConsensusVoteDeny, answer.Decision)
				return
			}
			require.NoError(t, answer.Err)
		})
	}
}
