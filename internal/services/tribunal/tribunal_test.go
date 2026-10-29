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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/agentpersona"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCandidateDecodeIsStrictAndDoesNotRewrite(t *testing.T) {
	for _, answer := range []string{`not json`, `{"command":"ss","args":[],"extra":1}`, `{"command":"ss -l","args":[]}`, `{"command":"ss\u00a0-l","args":[]}`, `{"command":"","args":[]}`, `null`, "{\"command\":\"ss\",\"args\":[]} trailing", "Command: {\"command\":\"ss\",\"args\":[]}", "```json\n{\"command\":\"ss\",\"args\":[]}\n```"} {
		inferer := &fakeInferer{generate: func(uint32, Member) string { return answer }}
		got, err := generate(context.Background(), testConfig(inferer), Request{}, 1, nil)
		require.NoError(t, err)
		require.Len(t, got, len(AllMembers))
		assert.Empty(t, got[0].Arguments.Command, answer)
	}
	inferer := &fakeInferer{generate: func(uint32, Member) string { return `{"command":"ss","args":["-l"]}` }}
	got, err := generate(context.Background(), testConfig(inferer), Request{}, 1, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"-l"}, got[0].Arguments.Args)
}

func TestVoteUsesCanonicalTypedArguments(t *testing.T) {
	candidates := []Candidate{
		{Member: MemberAxiom, Arguments: models.RunShellCommandRequest{Command: "tool", Args: []string{"a", "b"}}},
		{Member: MemberConcord, Arguments: models.RunShellCommandRequest{Command: "tool", Args: []string{"a", "b"}}},
		{Member: MemberVariance, Arguments: models.RunShellCommandRequest{Command: "tool", Args: []string{"b", "a"}}},
	}
	v := vote(1, candidates)
	require.Len(t, v.Clusters, 2)
	assert.Equal(t, []Member{MemberAxiom, MemberConcord}, v.Clusters[0].Members)
	assert.Equal(t, []string{"a", "b"}, v.Clusters[0].Arguments.Args)
}

// fakeInferer answers each call from a per-stage script and records every
// call in order.
type fakeInferer struct {
	generate func(round uint32, member Member) string
	marshal  string
	// marshalFinal, when set, answers every Marshal call after the first.
	marshalFinal string
	audit        []string
	fail         Stage
	calls        []Call
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
		marshalCalls := 0
		for _, c := range f.calls {
			if c.Stage == StageMarshal {
				marshalCalls++
			}
		}
		if f.marshalFinal != "" && marshalCalls > 1 {
			return f.marshalFinal, nil
		}
		return f.marshal, nil
	case StageAudit:
		return f.audit[min(int(call.Round), len(f.audit))-1], nil
	}
	return "", nil
}

func proposals(commands map[Member]string) func(uint32, Member) string {
	return func(_ uint32, member Member) string {
		return proposalAnswer(commands[member])
	}
}

func proposalAnswer(command string) string {
	if command == "" || strings.HasPrefix(command, "{") || strings.HasPrefix(command, "```") {
		return command
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return command
	}
	b, _ := json.Marshal(struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{Command: fields[0], Args: fields[1:]})
	return string(b)
}

// testPersonaPrompt is the registry persona prompt a stage renders.
func testPersonaPrompt(id string) string {
	personas, err := agentpersona.NewRegistry()
	if err != nil {
		panic(err)
	}
	prompt, err := personaSystemPrompt(personas, id)
	if err != nil {
		panic(err)
	}
	return prompt
}

func testConfig(inferer Inferer) Config {
	personas, err := agentpersona.NewRegistry()
	if err != nil {
		panic(err)
	}
	return Config{
		Inferer:     inferer,
		Personas:    personas,
		Doctrine:    governance.NewL1Doctrine(),
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

func candidatesFor(commands map[Member]string) []Candidate {
	var out []Candidate
	for _, member := range AllMembers {
		out = append(out, Candidate{Member: member, Round: 1, Arguments: argsFor(commands[member])})
	}
	return out
}

func argsFor(command string) models.RunShellCommandRequest {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return models.RunShellCommandRequest{}
	}
	return models.RunShellCommandRequest{Command: fields[0], Args: fields[1:]}
}

func TestVoteTalliesNemesisLikeAnyMember(t *testing.T) {
	candidates := []Candidate{
		{Member: MemberAxiom, Arguments: argsFor("ss -tlpn")},
		{Member: MemberConcord, Arguments: argsFor("ss -tlpn")},
		{Member: MemberVariance, Arguments: argsFor("ss -tlpn")},
		{Member: MemberPragma, Arguments: argsFor("netstat -tlpn")},
		{Member: MemberNemesis, Arguments: argsFor("ss -tlpn")},
	}
	v := vote(1, candidates)
	// An honest Nemesis abstention no longer knocks out the matching cluster.
	assert.Equal(t, "ss -tlpn", v.Winner.CommandLine())
	require.Len(t, v.Clusters, 2)
	assert.Equal(t, []Member{MemberAxiom, MemberConcord, MemberVariance, MemberNemesis}, v.Clusters[0].Members)
	assert.False(t, v.Unanimous(), "Pragma dissented")
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
			name:     "equal-length tie broken by first cluster",
			commands: map[Member]string{MemberAxiom: "ls -a", MemberConcord: "ls -a", MemberVariance: "ls -l", MemberPragma: "ls -l", MemberNemesis: "pwd"},
			winner:   "",
		},
		{
			name:     "empty proposals join no cluster",
			commands: map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberVariance: "", MemberPragma: "", MemberNemesis: "ls"},
			winner:   "pwd",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := vote(1, candidatesFor(tt.commands))
			assert.Equal(t, tt.winner, v.Winner.CommandLine())
			for i, cluster := range v.Clusters {
				assert.Equal(t, clusterID(i), cluster.ID)
				assert.NotEmpty(t, cluster.Arguments.Command)
			}
		})
	}
}

func TestVoteUnanimous(t *testing.T) {
	all := map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberVariance: "pwd", MemberPragma: "pwd", MemberNemesis: "pwd"}
	assert.True(t, vote(1, candidatesFor(all)).Unanimous())
	all[MemberNemesis] = "ls"
	assert.False(t, vote(1, candidatesFor(all)).Unanimous(), "Nemesis is a full panel member")
}

func TestAssessRisk(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		level   RiskLevel
		invalid bool
	}{
		{name: "low", answer: lowRisk, level: RiskLow},
		{name: "fenced answer fails closed", answer: "```json\n{\"risk\": \"medium\"}\n```", level: RiskHigh, invalid: true},
		{name: "high", answer: `{"risk": "HIGH", "rationale": "deletes /", "feedback": "scope the path"}`, level: RiskHigh},
		{name: "free text fails closed", answer: "This is LOW risk.", level: RiskHigh, invalid: true},
		{name: "unknown field fails closed", answer: `{"risk": "LOW", "score": 1}`, level: RiskHigh, invalid: true},
		{name: "unknown level fails closed", answer: `{"risk": "NONE"}`, level: RiskHigh, invalid: true},
		{name: "trailing content fails closed", answer: lowRisk + " LOW", level: RiskHigh, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inferer := &fakeInferer{marshal: tt.answer}
			risk, err := assessRisk(context.Background(), testConfig(inferer), Request{Request: testRequest}, argsFor("rm -rf /tmp/x"))
			require.NoError(t, err)
			assert.Equal(t, tt.level, risk.Level)
			assert.Equal(t, tt.invalid, risk.Invalid)
			require.Len(t, inferer.calls, 1)
			assert.Contains(t, inferer.calls[0].User, `"command":"rm"`)
		})
	}
}

func TestDecodeAnswerSentinel(t *testing.T) {
	var answer marshalAnswer
	assert.ErrorIs(t, decodeAnswer("LOW", &answer), constants.ErrTribunalAnswerInvalid)
}

func TestAudit(t *testing.T) {
	split := vote(1, candidatesFor(map[Member]string{
		MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "netstat -tlpn", MemberPragma: "lsof -i", MemberNemesis: "ps",
	}))
	unanimous := vote(1, candidatesFor(map[Member]string{
		MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "ss -tlpn", MemberNemesis: "ps",
	}))
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
			answers: []string{`{"verdict": "REVISE", "revised_arguments": {"command":"ss","args":["-tlpn"]}, "rationale": "x"}`},
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
			answers: []string{`{"verdict": "REVISE", "revised_arguments": {"command":" ","args":[]}}`},
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
				assert.Empty(t, result.Arguments.Command)
				return
			}
			assert.Empty(t, result.Failure)
			assert.Equal(t, tt.verdict, result.Verdict)
			assert.Equal(t, tt.command, result.Arguments.CommandLine())
		})
	}
}

func TestAuditClustersLabelWinnerFirst(t *testing.T) {
	v := vote(1, candidatesFor(map[Member]string{
		MemberAxiom: "lsof -i", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "netstat", MemberNemesis: "ps",
	}))
	clusters := auditClusters(v)
	require.Len(t, clusters, 4, "Nemesis's ps is a cluster like any other")
	assert.Equal(t, Cluster{ID: "cluster_a", Arguments: argsFor("ss -tlpn"), Members: []Member{MemberConcord, MemberVariance}}, clusters[0])
	assert.Equal(t, "cluster_b", clusters[1].ID)
	assert.Equal(t, "lsof -i", clusters[1].Arguments.CommandLine())
	assert.Equal(t, "cluster_c", clusters[2].ID)
	assert.Equal(t, "cluster_d", clusters[3].ID)
}

func TestRunApproved(t *testing.T) {
	inferer := &fakeInferer{
		generate: proposals(map[Member]string{
			MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "lsof -i",
		}),
		marshal: lowRisk,
		audit:   []string{approve},
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 3)
	require.NoError(t, err)
	assert.Equal(t, OutcomeApproved, result.Outcome)
	assert.Equal(t, "ss -tlpn", result.Arguments.CommandLine())
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
	assert.Contains(t, voter.GetVote().GetSelectedCommand(), `"command":"ss"`)
	assert.Len(t, voter.GetVote().GetVotes(), len(AllMembers))
	risk := result.Steps[len(AllMembers)+1]
	assert.Equal(t, evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_LOW, risk.GetRisk().GetRisk())
	assert.Equal(t, "command", risk.GetRisk().GetSubjectKind())
	review := result.Steps[len(AllMembers)+2]
	assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, review.GetReview().GetVerdict())
	assert.Equal(t, "ss -tlpn", review.GetReview().GetArguments().GetCommand()+" "+strings.Join(review.GetReview().GetArguments().GetArgs(), " "))
}

func TestRunRegeneratesOnceWithAnonymizedClusters(t *testing.T) {
	distinct := map[Member]string{MemberAxiom: "a", MemberConcord: "b", MemberVariance: "c", MemberPragma: "d", MemberNemesis: "e"}
	inferer := &fakeInferer{
		generate: func(round uint32, member Member) string {
			if round == 2 {
				if member == MemberNemesis {
					return proposalAnswer("other")
				}
				return proposalAnswer("pwd")
			}
			return proposalAnswer(distinct[member])
		},
		marshal: lowRisk,
		audit:   []string{approve},
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeApproved, result.Outcome)
	assert.Equal(t, "pwd", result.Arguments.Command)
	assert.Len(t, result.Candidates, 2*len(AllMembers))
	round2 := inferer.calls[len(AllMembers)]
	assert.Equal(t, uint32(2), round2.Round)
	assert.Contains(t, round2.User, "cluster_a")
	assert.NotContains(t, round2.User, string(MemberAxiom))
	assert.Equal(t, uint32(2), result.Steps[2*len(AllMembers)].GetVote().GetRounds())
}

func TestRunNoConsensus(t *testing.T) {
	distinct := map[Member]string{MemberAxiom: "a", MemberConcord: "b", MemberVariance: "c", MemberPragma: "d", MemberNemesis: "e"}
	inferer := &fakeInferer{generate: proposals(distinct)}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeNoConsensus, result.Outcome)
	assert.Empty(t, result.Arguments.Command)
	assert.Len(t, inferer.calls, 2*len(AllMembers))
	voter := result.Steps[len(result.Steps)-1]
	assert.False(t, voter.GetVote().GetConsensusReached())
	assert.Equal(t, uint32(2), voter.GetVote().GetRounds())
}

func TestRunBlocked(t *testing.T) {
	inferer := &fakeInferer{
		generate: proposals(map[Member]string{MemberAxiom: "rm ./build/file", MemberConcord: "rm ./build/file", MemberVariance: "x", MemberPragma: "y", MemberNemesis: "z"}),
		marshal:  "not json",
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeBlocked, result.Outcome)
	assert.Empty(t, result.Arguments.Command)
	assert.True(t, result.Risk.Invalid)
	risk := result.Steps[len(result.Steps)-1]
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_RISK_ANALYST, risk.GetRole())
	assert.True(t, risk.GetRisk().GetBlocked())
	assert.Contains(t, risk.GetRisk().GetSubject(), `"command":"rm"`)
}

// TestRunAssessesCommandTheAuditorChanged pins CNN-1: a command the Auditor
// changed is assessed by Marshal before it can be approved, and a changed
// command that Marshal blocks is never approved.
func TestRunAssessesCommandTheAuditorChanged(t *testing.T) {
	const highRisk = `{"risk": "HIGH", "rationale": "deletes /", "feedback": "scope the path"}`
	tests := []struct {
		name         string
		audit        string
		marshalFinal string
		outcome      Outcome
		command      string
		subject      string
		riskLevel    RiskLevel
		marshalCalls int
	}{
		{
			name:    "revision back to the winner is not reassessed",
			audit:   `{"verdict":"REVISE","revised_arguments":{"command":"ss","args":["-tlpn"]},"rationale":"correct"}`,
			outcome: OutcomeApproved, command: "ss -tlpn", subject: "ss -tlpn", riskLevel: RiskLow, marshalCalls: 1,
		},
		{
			name:         "revision Marshal rates LOW is approved",
			audit:        `{"verdict":"REVISE","revised_arguments":{"command":"ss","args":["-tlpn","-p"]},"rationale":"correct"}`,
			marshalFinal: lowRisk,
			outcome:      OutcomeApproved, command: "ss -tlpn -p", subject: "ss -tlpn -p", riskLevel: RiskLow, marshalCalls: 2,
		},
		{
			name:         "revision Marshal rates HIGH is blocked",
			audit:        `{"verdict":"REVISE","revised_arguments":{"command":"rm","args":["./build/file"]},"rationale":"cleanup"}`,
			marshalFinal: highRisk,
			outcome:      OutcomeBlocked, subject: "rm ./build/file", riskLevel: RiskHigh, marshalCalls: 2,
		},
		{
			name:         "swap to another cluster Marshal rates HIGH is blocked",
			audit:        `{"verdict": "SWAP", "swap_to_cluster": "cluster_b", "rationale":"alternative"}`,
			marshalFinal: highRisk,
			outcome:      OutcomeBlocked, subject: "netstat -tlpn", riskLevel: RiskHigh, marshalCalls: 2,
		},
		{
			name:    "swap to another cluster Marshal rates LOW is approved",
			audit:   `{"verdict": "SWAP", "swap_to_cluster": "cluster_b", "rationale":"alternative"}`,
			outcome: OutcomeApproved, command: "netstat -tlpn", subject: "netstat -tlpn", riskLevel: RiskLow, marshalCalls: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inferer := &fakeInferer{
				generate: proposals(map[Member]string{
					MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "lsof -i",
				}),
				marshal:      lowRisk,
				marshalFinal: tt.marshalFinal,
				audit:        []string{tt.audit},
			}
			result, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
			require.NoError(t, err)
			assert.Equal(t, tt.outcome, result.Outcome)
			assert.Equal(t, tt.command, result.Arguments.CommandLine())
			require.NotNil(t, result.Risk)
			assert.Equal(t, tt.riskLevel, result.Risk.Level)

			marshalCalls := 0
			var riskSteps []*evalv1.AgentStep
			for _, call := range inferer.calls {
				if call.Stage == StageMarshal {
					marshalCalls++
				}
			}
			for _, step := range result.Steps {
				if step.GetRisk() != nil {
					riskSteps = append(riskSteps, step)
				}
			}
			assert.Equal(t, tt.marshalCalls, marshalCalls)
			require.Len(t, riskSteps, tt.marshalCalls)
			assert.Contains(t, riskSteps[len(riskSteps)-1].GetRisk().GetSubject(), `"command":"`+strings.Fields(tt.subject)[0]+`"`)
			assert.Equal(t, tt.outcome == OutcomeBlocked, riskSteps[len(riskSteps)-1].GetRisk().GetBlocked())
		})
	}
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
	assert.Empty(t, result.Arguments.Command)
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
	assert.Contains(t, block, "OS: linux\nExecution: direct executable argv (no shell)\nWorking directory: /srv")
	assert.Contains(t, block, "<guidelines>\nno sudo\n</guidelines>")
	assert.Contains(t, block, "<request>\n"+testRequest+"\n</request>")
}

func TestGatewayOwnedContextIsComposedBeforeReview(t *testing.T) {
	inferer := &fakeInferer{
		generate: proposals(map[Member]string{MemberAxiom: "ss -l", MemberConcord: "ss -l", MemberVariance: "x", MemberPragma: "y", MemberNemesis: "z"}),
		marshal:  lowRisk,
		audit:    []string{`{"verdict":"REVISE","revised_arguments":{"command":"ss","args":["-l","-p"]},"rationale":"correct"}`},
	}
	req := Request{Request: testRequest, WorkingDirectory: "/srv/work", TimeoutSeconds: 23}
	result, err := Run(context.Background(), testConfig(inferer), req, 0)
	require.NoError(t, err)
	assert.Equal(t, "/srv/work", result.Arguments.WorkingDir)
	assert.Equal(t, 23, result.Arguments.Timeout)
	assert.Equal(t, result.Arguments.CanonicalJSON(), result.ArgumentsJSON)
	var marshalCall, auditorCall string
	for _, call := range inferer.calls {
		if call.Stage == StageMarshal {
			marshalCall = call.User
		}
		if call.Stage == StageAudit {
			auditorCall = call.User
		}
	}
	assert.Contains(t, marshalCall, `"working_dir":"/srv/work"`)
	assert.Contains(t, marshalCall, `"timeout":23`)
	assert.Contains(t, auditorCall, `"working_dir":"/srv/work"`)
	assert.Contains(t, auditorCall, `"timeout":23`)
	assert.Contains(t, auditorCall, `"args":["-l"]`)
	require.Len(t, inferer.calls, len(AllMembers)+3)
	assert.Contains(t, inferer.calls[len(inferer.calls)-1].User, `"args":["-l","-p"]`)
}
