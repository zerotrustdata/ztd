// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthoringAcceptsSurroundingJSONWhitespace(t *testing.T) {
	inferer := &fakeInferer{
		generate: func(_ uint32, member Member) string {
			command := "pwd"
			if member == MemberNemesis {
				command = "ls"
			}
			return " \n" + proposalAnswer(command) + "\t\n"
		},
		marshal: "\n" + lowRisk + "\n",
		audit:   []string{"\t" + approve + "\n"},
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: "show working directory"}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeApproved, result.Outcome)
	assert.Equal(t, "pwd", result.Arguments.Command)
	assert.Len(t, inferer.calls, len(AllMembers)+2)
}

func TestGenerationRepairsInvalidAnswerWithFeedback(t *testing.T) {
	attempts := make(map[Member]int)
	inferer := &fakeInferer{generate: func(_ uint32, member Member) string {
		attempts[member]++
		if attempts[member] == 1 {
			return `{"command":"pwd","args":[],"timeout":30}`
		}
		if attempts[member] == 2 {
			return `{"command":"pwd -L","args":[]}`
		}
		return `{"command":"pwd","args":[]}`
	}}
	candidates, err := generate(context.Background(), testConfig(inferer), Request{Request: "show working directory"}, 1, nil)
	require.NoError(t, err)
	for i, candidate := range candidates {
		assert.Equal(t, "pwd", candidate.Arguments.Command)
		require.Equal(t, candidateMaxAttempts, attempts[candidate.Member])
		assert.Contains(t, inferer.calls[i*candidateMaxAttempts+1].User, "timeout")
		assert.Contains(t, inferer.calls[i*candidateMaxAttempts+2].User, "executable")
	}
}

func TestGenerationRejectsAnswersOutsideDeclaredSchema(t *testing.T) {
	for name, answer := range map[string]string{
		"missing argv":  `{"command":"pwd"}`,
		"null argv":     `{"command":"pwd","args":null}`,
		"null argument": `{"command":"printf","args":[null]}`,
		"wrong casing":  `{"Command":"pwd","args":[]}`,
		"duplicate key": `{"command":"ls","command":"pwd","args":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			inferer := &fakeInferer{generate: func(uint32, Member) string { return answer }}
			candidates, err := generate(context.Background(), testConfig(inferer), Request{}, 1, nil)
			require.NoError(t, err)
			for _, candidate := range candidates {
				assert.Empty(t, candidate.Arguments.Command)
			}
			assert.Len(t, inferer.calls, len(AllMembers)*candidateMaxAttempts)
		})
	}
}

func TestAuditorRepairsInvalidAnswerWithFeedback(t *testing.T) {
	v := vote(1, candidatesFor(map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberNemesis: "ls"}))
	inferer := &fakeInferer{audit: []string{
		`{"verdict":"REVISE","revised_arguments":{"command":"pwd","args":[],"timeout":30},"rationale":"x"}`,
		approve,
	}}
	result, err := audit(context.Background(), testConfig(inferer), Request{Request: "show working directory"}, v)
	require.NoError(t, err)
	assert.Empty(t, result.Failure)
	require.Len(t, inferer.calls, auditorMaxAttempts)
	assert.Contains(t, inferer.calls[1].User, `unknown field "timeout"`)
}

func TestAuditorRejectsAnswersOutsideDeclaredSchema(t *testing.T) {
	v := vote(1, candidatesFor(map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberNemesis: "ls"}))
	for name, answer := range map[string]string{
		"missing argv":   `{"verdict":"REVISE","revised_arguments":{"command":"pwd"},"rationale":"x"}`,
		"null argument":  `{"verdict":"REVISE","revised_arguments":{"command":"printf","args":[null]},"rationale":"x"}`,
		"null revision":  `{"verdict":"APPROVE","revised_arguments":null,"rationale":"x"}`,
		"missing reason": `{"verdict":"APPROVE"}`,
	} {
		t.Run(name, func(t *testing.T) {
			inferer := &fakeInferer{audit: []string{answer}}
			result, err := audit(context.Background(), testConfig(inferer), Request{}, v)
			require.NoError(t, err)
			assert.NotEmpty(t, result.Failure)
			assert.Empty(t, result.Arguments.Command)
		})
	}
}

func TestMarshalRejectsAssessmentWithoutRationale(t *testing.T) {
	inferer := &fakeInferer{marshal: `{"risk":"LOW"}`}
	risk, err := assessRisk(context.Background(), testConfig(inferer), Request{}, argsFor("pwd"))
	require.NoError(t, err)
	assert.True(t, risk.Invalid)
	assert.True(t, risk.Blocked())
}

func TestAuthoringPreservesLiteralUtilityArguments(t *testing.T) {
	// The final semicolon belongs to find, and the pattern belongs to grep.
	// Neither argument is shell syntax when passed directly as argv.
	for _, answer := range []string{
		`{"command":"find","args":["/var/tmp","-type","f","-exec","ls","-lh","{}",";"]}`,
		`{"command":"grep","args":["-E","error|warning","a file with spaces.log"]}`,
	} {
		inferer := &fakeInferer{generate: func(uint32, Member) string { return answer }}
		candidates, err := generate(context.Background(), testConfig(inferer), Request{}, 1, nil)
		require.NoError(t, err)
		for _, candidate := range candidates {
			assert.JSONEq(t, answer, candidate.Arguments.CanonicalJSON())
		}
		v := Vote{Winner: candidates[0].Arguments}
		inferer.audit = []string{`{"verdict":"REVISE","revised_arguments":` + answer + `,"rationale":"preserve literal utility syntax"}`}
		result, err := audit(context.Background(), testConfig(inferer), Request{}, v)
		require.NoError(t, err)
		require.Empty(t, result.Failure)
		assert.JSONEq(t, answer, result.Arguments.CanonicalJSON())
	}
}

func TestNemesisAuthoringIsCunningNotAlwaysAggressive(t *testing.T) {
	generate := testPersonaPrompt(string(MemberNemesis))
	assert.Contains(t, generate, "always cunning, not always aggressive")
	assert.Contains(t, generate, "propose the honest correct argument set")
	assert.NotContains(t, generate, "excluded from voting")
	vote, _ := ConsensusVotePrompt(MemberNemesis, ConsensusSubject{})
	assert.Contains(t, vote, "privilege escalation")
}
