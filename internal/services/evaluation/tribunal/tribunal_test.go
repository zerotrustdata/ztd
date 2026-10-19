// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestNormaliseCommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty", input: "", expected: ""},
		{name: "clean command", input: "cat /etc/hosts", expected: "cat /etc/hosts"},
		{name: "markdown fence bash", input: "```bash\ncat /etc/hosts\n```", expected: "cat /etc/hosts"},
		{name: "markdown fence with comment", input: "```sh\n# check host file\ncat /etc/hosts;\n```", expected: "cat /etc/hosts"},
		{name: "prefix Command:", input: "Command: grep -rn 'error' /var/log", expected: "grep -rn 'error' /var/log"},
		{name: "dollar prefix", input: "$ ss -tlpn", expected: "ss -tlpn"},
		{name: "trailing semicolon and whitespace", input: "   curl -s http://localhost:8080/health ;  ", expected: "curl -s http://localhost:8080/health"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NormaliseCommand(tt.input))
		})
	}
}

// fakeInferer answers each call from a per-stage script and records every
// call in order.
type fakeInferer struct {
	generate func(round uint32, member Member) string
	marshal  string
	audit    []string
	fail     Stage
	calls    []Call
}

var errFakeInference = errors.New("fake inference failure")

func (f *fakeInferer) Infer(_ context.Context, call Call) (string, error) {
	f.calls = append(f.calls, call)
	if call.Stage == f.fail {
		return "", errFakeInference
	}
	switch call.Stage {
	case StageGenerate:
		return f.generate(call.Round, Member(call.Persona)), nil
	case StageMarshal:
		return f.marshal, nil
	case StageAudit:
		return f.audit[min(int(call.Round), len(f.audit))-1], nil
	}
	return "", nil
}

func proposals(commands map[Member]string) func(uint32, Member) string {
	return func(_ uint32, member Member) string { return commands[member] }
}

func testConfig(inferer Inferer) Config {
	return Config{
		Inferer:     inferer,
		Tier:        evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
		ModelTag:    "model:tag",
		ModelDigest: "sha256:digest",
	}
}

const (
	lowRisk     = `{"risk": "LOW", "rationale": "read-only", "feedback": ""}`
	approve     = `{"verdict": "APPROVE", "rationale": "correct"}`
	testRequest = "list listening TCP ports"
)

func candidatesFor(commands map[Member]string, round uint32) []Candidate {
	var out []Candidate
	for _, member := range AllMembers {
		out = append(out, Candidate{Member: member, Round: round, Command: commands[member]})
	}
	return out
}

func TestVote(t *testing.T) {
	tests := []struct {
		name     string
		commands map[Member]string
		winner   string
	}{
		{
			name:     "largest cluster wins",
			commands: map[Member]string{MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "netstat -tlpn", MemberPragma: "ss -tlpn", MemberNemesis: "lsof -i"},
			winner:   "ss -tlpn",
		},
		{
			name:     "no cluster reaches the minimum consensus",
			commands: map[Member]string{MemberAxiom: "a", MemberConcord: "b", MemberVariance: "c", MemberPragma: "d", MemberNemesis: "e"},
		},
		{
			name:     "tie broken by the shortest command",
			commands: map[Member]string{MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "netstat -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "lsof -i"},
			winner:   "ss -tlpn",
		},
		{
			name:     "equal-length tie broken against Nemesis",
			commands: map[Member]string{MemberAxiom: "ls -a", MemberConcord: "ls -a", MemberVariance: "ls -l", MemberPragma: "pwd", MemberNemesis: "ls -l"},
			winner:   "ls -a",
		},
		{
			name:     "equal-length tie without Nemesis has no winner",
			commands: map[Member]string{MemberAxiom: "ls -a", MemberConcord: "ls -a", MemberVariance: "ls -l", MemberPragma: "ls -l", MemberNemesis: "pwd"},
		},
		{
			name:     "empty proposals join no cluster",
			commands: map[Member]string{MemberAxiom: "", MemberConcord: "", MemberVariance: "", MemberPragma: "pwd", MemberNemesis: "pwd"},
			winner:   "pwd",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := vote(1, candidatesFor(tt.commands, 1))
			assert.Equal(t, tt.winner, v.Winner)
			for i, cluster := range v.Clusters {
				assert.Equal(t, clusterID(i), cluster.ID)
				assert.NotEmpty(t, cluster.Command)
			}
		})
	}
}

func TestVoteUnanimous(t *testing.T) {
	all := map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberVariance: "pwd", MemberPragma: "pwd", MemberNemesis: "pwd"}
	assert.True(t, vote(1, candidatesFor(all, 1)).Unanimous())
	all[MemberNemesis] = "ls"
	assert.False(t, vote(1, candidatesFor(all, 1)).Unanimous())
}

func TestAssessRisk(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		level   RiskLevel
		invalid bool
	}{
		{name: "low", answer: lowRisk, level: RiskLow},
		{name: "fenced medium", answer: "```json\n{\"risk\": \"medium\", \"rationale\": \"edit\", \"feedback\": \"\"}\n```", level: RiskMedium},
		{name: "high", answer: `{"risk": "HIGH", "rationale": "deletes /", "feedback": "scope the path"}`, level: RiskHigh},
		{name: "free text fails closed", answer: "This is LOW risk.", level: RiskHigh, invalid: true},
		{name: "unknown field fails closed", answer: `{"risk": "LOW", "score": 1}`, level: RiskHigh, invalid: true},
		{name: "unknown level fails closed", answer: `{"risk": "NONE"}`, level: RiskHigh, invalid: true},
		{name: "trailing content fails closed", answer: lowRisk + " LOW", level: RiskHigh, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inferer := &fakeInferer{marshal: tt.answer}
			risk, err := assessRisk(context.Background(), testConfig(inferer), Request{Request: testRequest}, "rm -rf /tmp/x")
			require.NoError(t, err)
			assert.Equal(t, tt.level, risk.Level)
			assert.Equal(t, tt.invalid, risk.Invalid)
			require.Len(t, inferer.calls, 1)
			assert.Contains(t, inferer.calls[0].User, "rm -rf /tmp/x")
		})
	}
}

func TestDecodeAnswerSentinel(t *testing.T) {
	var answer marshalAnswer
	assert.ErrorIs(t, decodeAnswer("LOW", &answer), constants.ErrTribunalAnswerInvalid)
}

func TestAudit(t *testing.T) {
	split := vote(1, candidatesFor(map[Member]string{
		MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "lsof -i",
	}, 1))
	unanimous := vote(1, candidatesFor(map[Member]string{
		MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "ss -tlpn", MemberNemesis: "ss -tlpn",
	}, 1))
	tests := []struct {
		name     string
		vote     Vote
		answers  []string
		verdict  evalv1.AgentReviewVerdict
		command  string
		failed   bool
		attempts int
	}{
		{name: "approve", vote: split, answers: []string{approve}, verdict: evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, command: "ss -tlpn", attempts: 1},
		{
			name: "revise", vote: unanimous,
			answers: []string{`{"verdict": "REVISE", "revised_command": "ss -tlpn;", "rationale": "x"}`},
			verdict: evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_REVISE, command: "ss -tlpn", attempts: 1,
		},
		{
			name: "swap to an alternative cluster", vote: split,
			answers: []string{`{"verdict": "SWAP", "swap_to_cluster": "cluster_c", "rationale": "x"}`},
			verdict: evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_SWAP, command: "lsof -i", attempts: 1,
		},
		{
			name: "invalid swap retried once", vote: split,
			answers: []string{`{"verdict": "SWAP", "swap_to_cluster": "cluster_a"}`, approve},
			verdict: evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, command: "ss -tlpn", attempts: 2,
		},
		{
			name: "swap on a unanimous panel fails closed", vote: unanimous,
			answers: []string{`{"verdict": "SWAP", "swap_to_cluster": "cluster_b"}`},
			failed:  true, attempts: 2,
		},
		{
			name: "revise without a command fails closed", vote: split,
			answers: []string{`{"verdict": "REVISE", "revised_command": "  "}`},
			failed:  true, attempts: 2,
		},
		{name: "free text fails closed", vote: split, answers: []string{"APPROVE"}, failed: true, attempts: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inferer := &fakeInferer{audit: tt.answers}
			result, err := audit(context.Background(), testConfig(inferer), Request{Request: testRequest}, tt.vote)
			require.NoError(t, err)
			assert.Len(t, inferer.calls, tt.attempts)
			if tt.failed {
				assert.NotEmpty(t, result.Failure)
				assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_UNSPECIFIED, result.Verdict)
				assert.Empty(t, result.Command)
				return
			}
			assert.Empty(t, result.Failure)
			assert.Equal(t, tt.verdict, result.Verdict)
			assert.Equal(t, tt.command, result.Command)
		})
	}
}

func TestAuditClustersLabelWinnerFirst(t *testing.T) {
	v := vote(1, candidatesFor(map[Member]string{
		MemberAxiom: "lsof -i", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "ss -tlpn", MemberNemesis: "netstat",
	}, 1))
	clusters := auditClusters(v)
	require.Len(t, clusters, 3)
	assert.Equal(t, Cluster{ID: "cluster_a", Command: "ss -tlpn", Members: []Member{MemberConcord, MemberVariance, MemberPragma}}, clusters[0])
	assert.Equal(t, "cluster_b", clusters[1].ID)
	assert.Equal(t, "lsof -i", clusters[1].Command)
	assert.Equal(t, "cluster_c", clusters[2].ID)
}

func TestRunApproved(t *testing.T) {
	inferer := &fakeInferer{
		generate: proposals(map[Member]string{
			MemberAxiom: "ss -tlpn", MemberConcord: "```bash\nss -tlpn\n```", MemberVariance: "ss -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "lsof -i",
		}),
		marshal: lowRisk,
		audit:   []string{approve},
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 3)
	require.NoError(t, err)
	assert.Equal(t, OutcomeApproved, result.Outcome)
	assert.Equal(t, "ss -tlpn", result.Command)
	assert.Len(t, inferer.calls, len(AllMembers)+2)

	require.Len(t, result.Steps, len(AllMembers)+3)
	for i, step := range result.Steps {
		assert.Equal(t, uint32(4+i), step.GetIndex())
		assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, step.GetResolvedTier())
		assert.Equal(t, "model:tag", step.GetModelTag())
		assert.Equal(t, "sha256:digest", step.GetModelDigest())
		assert.Equal(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN, step.GetCallClass())
	}
	voter := result.Steps[len(AllMembers)]
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_CANDIDATE_VOTER, voter.GetRole())
	assert.True(t, voter.GetVote().GetConsensusReached())
	assert.Equal(t, uint32(1), voter.GetVote().GetRounds())
	assert.Equal(t, "ss -tlpn", voter.GetVote().GetSelectedCommand())
	assert.Len(t, voter.GetVote().GetVotes(), len(AllMembers))
	risk := result.Steps[len(AllMembers)+1]
	assert.Equal(t, evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_LOW, risk.GetRisk().GetRisk())
	assert.Equal(t, "command", risk.GetRisk().GetSubjectKind())
	review := result.Steps[len(AllMembers)+2]
	assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, review.GetReview().GetVerdict())
	assert.Equal(t, "ss -tlpn", review.GetReview().GetCommand())
}

func TestRunRegeneratesOnceWithAnonymizedClusters(t *testing.T) {
	distinct := map[Member]string{MemberAxiom: "a", MemberConcord: "b", MemberVariance: "c", MemberPragma: "d", MemberNemesis: "e"}
	inferer := &fakeInferer{
		generate: func(round uint32, member Member) string {
			if round == 2 {
				return "pwd"
			}
			return distinct[member]
		},
		marshal: lowRisk,
		audit:   []string{approve},
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeApproved, result.Outcome)
	assert.Equal(t, "pwd", result.Command)
	assert.Len(t, result.Candidates, 2*len(AllMembers))
	round2 := inferer.calls[len(AllMembers)]
	assert.Equal(t, uint32(2), round2.Round)
	assert.Contains(t, round2.User, "[cluster_a] (support: 1)\na")
	assert.NotContains(t, round2.User, string(MemberAxiom))
	assert.Equal(t, uint32(2), result.Steps[2*len(AllMembers)].GetVote().GetRounds())
}

func TestRunNoConsensus(t *testing.T) {
	distinct := map[Member]string{MemberAxiom: "a", MemberConcord: "b", MemberVariance: "c", MemberPragma: "d", MemberNemesis: "e"}
	inferer := &fakeInferer{generate: proposals(distinct)}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeNoConsensus, result.Outcome)
	assert.Empty(t, result.Command)
	assert.Len(t, inferer.calls, 2*len(AllMembers))
	voter := result.Steps[len(result.Steps)-1]
	assert.False(t, voter.GetVote().GetConsensusReached())
	assert.Equal(t, uint32(2), voter.GetVote().GetRounds())
}

func TestRunBlocked(t *testing.T) {
	inferer := &fakeInferer{
		generate: proposals(map[Member]string{MemberAxiom: "rm -rf /", MemberConcord: "rm -rf /", MemberVariance: "x", MemberPragma: "y", MemberNemesis: "z"}),
		marshal:  "not json",
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeBlocked, result.Outcome)
	assert.Empty(t, result.Command)
	assert.True(t, result.Risk.Invalid)
	risk := result.Steps[len(result.Steps)-1]
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_RISK_ANALYST, risk.GetRole())
	assert.True(t, risk.GetRisk().GetBlocked())
	assert.Equal(t, "rm -rf /", risk.GetRisk().GetSubject())
}

func TestRunAuditFailed(t *testing.T) {
	inferer := &fakeInferer{
		generate: proposals(map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberVariance: "x", MemberPragma: "y", MemberNemesis: "z"}),
		marshal:  lowRisk,
		audit:    []string{"looks fine"},
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeAuditFailed, result.Outcome)
	assert.Empty(t, result.Command)
	review := result.Steps[len(result.Steps)-1]
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_REVIEWER, review.GetRole())
	assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_UNSPECIFIED, review.GetReview().GetVerdict())
}

func TestRunInferenceFailureStops(t *testing.T) {
	for _, stage := range []Stage{StageGenerate, StageMarshal, StageAudit} {
		t.Run(string(stage), func(t *testing.T) {
			inferer := &fakeInferer{
				generate: proposals(map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd"}),
				marshal:  lowRisk,
				audit:    []string{approve},
				fail:     stage,
			}
			result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
			require.ErrorIs(t, err, errFakeInference)
			assert.Nil(t, result)
			assert.Equal(t, stage, inferer.calls[len(inferer.calls)-1].Stage)
		})
	}
}

func TestRunRequiresInferer(t *testing.T) {
	_, err := Run(context.Background(), Config{}, Request{Request: testRequest}, 0)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestContextBlock(t *testing.T) {
	block := contextBlock(Request{Request: testRequest, Guidelines: "no sudo", WorkingDirectory: "/srv"})
	assert.True(t, strings.Contains(block, "OS: linux\nShell: bash\nWorking directory: /srv"))
	assert.Contains(t, block, "<guidelines>\nno sudo\n</guidelines>")
	assert.Contains(t, block, "<request>\n"+testRequest+"\n</request>")
}
