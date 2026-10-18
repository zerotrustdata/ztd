// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestNormaliseCommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
		{
			name:     "clean command",
			input:    "cat /etc/hosts",
			expected: "cat /etc/hosts",
		},
		{
			name:     "markdown fence bash",
			input:    "```bash\ncat /etc/hosts\n```",
			expected: "cat /etc/hosts",
		},
		{
			name:     "markdown fence with comment",
			input:    "```sh\n# check host file\ncat /etc/hosts;\n```",
			expected: "cat /etc/hosts",
		},
		{
			name:     "prefix Command:",
			input:    "Command: grep -rn 'error' /var/log",
			expected: "grep -rn 'error' /var/log",
		},
		{
			name:     "dollar prefix",
			input:    "$ ss -tlpn",
			expected: "ss -tlpn",
		},
		{
			name:     "trailing semicolon and whitespace",
			input:    "   curl -s http://localhost:8080/health ;  ",
			expected: "curl -s http://localhost:8080/health",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := NormaliseCommand(tt.input)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

type testDispatcher struct {
	handler func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error)
}

func (d *testDispatcher) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	return d.handler(req)
}

func testConfig(dispatcher Dispatcher) PipelineConfig {
	return PipelineConfig{
		Dispatcher:          dispatcher,
		InferenceSessionID:  "sess-inf-1",
		Variant:             &evalv1.ModelVariant{ServedModelTag: "model:test", ModelDigest: strings.Repeat("a", 64)},
		CampaignID:          "campaign-test",
		ModelRegistryDigest: strings.Repeat("b", 64),
		NewID: func(prefix string) string {
			return prefix + "-id"
		},
		Now: func() time.Time {
			return time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC)
		},
	}
}

func TestRunVoting_ConsensusRound1(t *testing.T) {
	candidates := []Candidate{
		{Member: MemberAxiom, Command: "cat /var/log/syslog | grep error"},
		{Member: MemberConcord, Command: "grep 'error' /var/log/syslog"},
		{Member: MemberVariance, Command: "grep 'error' /var/log/syslog"},
		{Member: MemberPragma, Command: "grep 'error' /var/log/syslog"},
		{Member: MemberNemesis, Command: "grep -a 'error' /var/log/syslog"},
	}

	vote, err := RunVoting(context.Background(), testConfig(nil), PipelineRequest{}, candidates)
	require.NoError(t, err)
	assert.True(t, vote.ConsensusReached)
	assert.Equal(t, uint32(1), vote.Rounds)
	assert.Equal(t, "grep 'error' /var/log/syslog", vote.WinnerCommand)
	assert.Len(t, vote.Votes, 5)
}

func TestRunVoting_TieBreakingShortestAndNonNemesis(t *testing.T) {
	// Axiom and Concord vote for short cmd (2 votes)
	// Pragma and Nemesis vote for longer cmd (2 votes)
	// Variance votes for something else (1 vote)
	candidates := []Candidate{
		{Member: MemberAxiom, Command: "ss -tlpn"},
		{Member: MemberConcord, Command: "ss -tlpn"},
		{Member: MemberPragma, Command: "netstat -tulpen"},
		{Member: MemberNemesis, Command: "netstat -tulpen"},
		{Member: MemberVariance, Command: "lsof -i -P -n"},
	}

	vote, err := RunVoting(context.Background(), testConfig(nil), PipelineRequest{}, candidates)
	require.NoError(t, err)
	assert.True(t, vote.ConsensusReached)
	assert.Equal(t, uint32(1), vote.Rounds)
	// "ss -tlpn" is shorter and has 2 votes vs "netstat -tulpen" with 2 votes
	assert.Equal(t, "ss -tlpn", vote.WinnerCommand)
}

func TestRunVoting_Round2PeerReview(t *testing.T) {
	// 5 distinct candidates in Round 1: no consensus in Round 1!
	candidates := []Candidate{
		{Member: MemberAxiom, Command: "cat /proc/cpuinfo"},
		{Member: MemberConcord, Command: "lscpu"},
		{Member: MemberVariance, Command: "nproc"},
		{Member: MemberPragma, Command: "uname -a"},
		{Member: MemberNemesis, Command: "top -b -n 1"},
	}

	// Dispatcher returns "cluster_b" ("lscpu") for Round 2 peer review
	dispatcher := &testDispatcher{
		handler: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return &operatorv1.InferenceDispatchResponse{
				TransactionId: "tx-r2",
				Receipt:       &operatorv1.ActionReceipt{ActionType: "inference"},
				Result: &operatorv1.InferenceResult{
					Parts: []*operatorv1.InferenceResponsePart{
						{Part: &operatorv1.InferenceResponsePart_Text{Text: `{"cluster": "cluster_b", "rationale": "lscpu is standard"}`}},
					},
				},
			}, nil
		},
	}

	vote, err := RunVoting(context.Background(), testConfig(dispatcher), PipelineRequest{}, candidates)
	require.NoError(t, err)
	assert.True(t, vote.ConsensusReached)
	assert.Equal(t, uint32(2), vote.Rounds)
	assert.Equal(t, "lscpu", vote.WinnerCommand)
}

func TestAnalyzeRisk_LowMediumHigh(t *testing.T) {
	t.Run("low risk", func(t *testing.T) {
		dispatcher := &testDispatcher{
			handler: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return &operatorv1.InferenceDispatchResponse{
					TransactionId: "tx-low",
					Result: &operatorv1.InferenceResult{
						Parts: []*operatorv1.InferenceResponsePart{
							{Part: &operatorv1.InferenceResponsePart_Text{Text: `{"risk": "LOW", "rationale": "read-only status"}`}},
						},
					},
				}, nil
			},
		}

		risk, err := AnalyzeRisk(context.Background(), testConfig(dispatcher), PipelineRequest{Request: "Check status"}, "cat /status/health.txt")
		require.NoError(t, err)
		assert.Equal(t, RiskLow, risk.Risk)
		assert.False(t, risk.Blocked)
	})

	t.Run("high risk blocked", func(t *testing.T) {
		dispatcher := &testDispatcher{
			handler: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return &operatorv1.InferenceDispatchResponse{
					TransactionId: "tx-high",
					Result: &operatorv1.InferenceResult{
						Parts: []*operatorv1.InferenceResponsePart{
							{Part: &operatorv1.InferenceResponsePart_Text{Text: `{"risk": "HIGH", "rationale": "destructive deletion", "feedback": "Avoid rm -rf /"}`}},
						},
					},
				}, nil
			},
		}

		risk, err := AnalyzeRisk(context.Background(), testConfig(dispatcher), PipelineRequest{Request: "Clean all"}, "rm -rf /")
		require.NoError(t, err)
		assert.Equal(t, RiskHigh, risk.Risk)
		assert.True(t, risk.Blocked)
		assert.Equal(t, "Avoid rm -rf /", risk.Feedback)
	})
}

func TestAuditCommand_ApproveReviseSwap(t *testing.T) {
	vote := &VoteResult{
		WinnerCommand: "curl http://localhost:8080",
		Clusters: map[string][]Member{
			"curl http://localhost:8080":    {MemberAxiom, MemberConcord},
			"curl -s http://localhost:8080": {MemberVariance},
		},
	}

	t.Run("approve", func(t *testing.T) {
		dispatcher := &testDispatcher{
			handler: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return &operatorv1.InferenceDispatchResponse{
					TransactionId: "tx-app",
					Result: &operatorv1.InferenceResult{
						Parts: []*operatorv1.InferenceResponsePart{
							{Part: &operatorv1.InferenceResponsePart_Text{Text: `{"verdict": "APPROVE", "command": "curl http://localhost:8080"}`}},
						},
					},
				}, nil
			},
		}
		audit, err := AuditCommand(context.Background(), testConfig(dispatcher), PipelineRequest{}, vote)
		require.NoError(t, err)
		assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, audit.Verdict)
		assert.Equal(t, "curl http://localhost:8080", audit.FinalCommand)
	})

	t.Run("revise", func(t *testing.T) {
		dispatcher := &testDispatcher{
			handler: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
				return &operatorv1.InferenceDispatchResponse{
					TransactionId: "tx-rev",
					Result: &operatorv1.InferenceResult{
						Parts: []*operatorv1.InferenceResponsePart{
							{Part: &operatorv1.InferenceResponsePart_Text{Text: `{"verdict": "REVISE", "command": "curl -fsSL http://localhost:8080"}`}},
						},
					},
				}, nil
			},
		}
		audit, err := AuditCommand(context.Background(), testConfig(dispatcher), PipelineRequest{}, vote)
		require.NoError(t, err)
		assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_REVISE, audit.Verdict)
		assert.Equal(t, "curl -fsSL http://localhost:8080", audit.FinalCommand)
	})
}

func TestRunPipeline_FullFlow(t *testing.T) {
	dispatcher := &testDispatcher{
		handler: func(req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
			attempt := req.GetProviderAttemptId()
			txID := "tx-" + attempt
			var text string

			switch {
			case strings.Contains(attempt, "tribunal-gen-axiom"):
				text = "cat /srv/health.txt"
			case strings.Contains(attempt, "tribunal-gen-concord"):
				text = "cat /srv/health.txt"
			case strings.Contains(attempt, "tribunal-gen-variance"):
				text = "cat /srv/health.txt"
			case strings.Contains(attempt, "tribunal-gen-pragma"):
				text = "head -n 1 /srv/health.txt"
			case strings.Contains(attempt, "tribunal-gen-nemesis"):
				text = "cat /srv/health.txt"
			case strings.Contains(attempt, "tribunal-marshal"):
				text = `{"risk": "LOW", "rationale": "read-only health inspection"}`
			case strings.Contains(attempt, "tribunal-audit"):
				text = `{"verdict": "APPROVE", "command": "cat /srv/health.txt", "rationale": "Safe and direct"}`
			default:
				text = "echo ok"
			}

			return &operatorv1.InferenceDispatchResponse{
				TransactionId: txID,
				Receipt: &operatorv1.ActionReceipt{
					ActionType:    "inference",
					ResultSummary: txID,
				},
				Result: &operatorv1.InferenceResult{
					Parts: []*operatorv1.InferenceResponsePart{
						{Part: &operatorv1.InferenceResponsePart_Text{Text: text}},
					},
				},
			}, nil
		},
	}

	cfg := testConfig(dispatcher)
	req := PipelineRequest{
		Request:          "Read the health status file",
		WorkingDirectory: "/srv",
		Turn:             1,
	}

	result, err := RunPipeline(context.Background(), cfg, req, 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "cat /srv/health.txt", result.FinalCommand)

	// Check discrete steps created:
	// 5 candidate steps + 1 vote step + 1 risk step + 1 review step = 8 steps!
	require.Len(t, result.Steps, 8)

	// Verify step indices and roles
	assert.Equal(t, uint32(1), result.Steps[0].GetIndex())
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR, result.Steps[0].GetRole())
	assert.Equal(t, "axiom", result.Steps[0].GetPersonaLabel())

	assert.Equal(t, uint32(5), result.Steps[4].GetIndex())
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR, result.Steps[4].GetRole())
	assert.Equal(t, "nemesis", result.Steps[4].GetPersonaLabel())

	assert.Equal(t, uint32(6), result.Steps[5].GetIndex())
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_CANDIDATE_VOTER, result.Steps[5].GetRole())
	assert.Equal(t, "tribunal-voter", result.Steps[5].GetPersonaLabel())
	assert.True(t, result.Steps[5].GetVote().GetConsensusReached())
	assert.Equal(t, "cat /srv/health.txt", result.Steps[5].GetVote().GetSelectedCommand())

	assert.Equal(t, uint32(7), result.Steps[6].GetIndex())
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_RISK_ANALYST, result.Steps[6].GetRole())
	assert.Equal(t, "marshal", result.Steps[6].GetPersonaLabel())
	assert.Equal(t, evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_LOW, result.Steps[6].GetRisk().GetRisk())
	assert.False(t, result.Steps[6].GetRisk().GetBlocked())

	assert.Equal(t, uint32(8), result.Steps[7].GetIndex())
	assert.Equal(t, evalv1.AgentRole_AGENT_ROLE_REVIEWER, result.Steps[7].GetRole())
	assert.Equal(t, "auditor", result.Steps[7].GetPersonaLabel())
	assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, result.Steps[7].GetReview().GetVerdict())
	assert.Equal(t, "cat /srv/health.txt", result.Steps[7].GetReview().GetCommand())

	// Receipts and transaction IDs
	assert.NotEmpty(t, result.Receipts)
	assert.NotEmpty(t, result.TransactionIDs)
}
