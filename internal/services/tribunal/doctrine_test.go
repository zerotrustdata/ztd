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

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestGenerationReplacesL1BlockedCandidatesBeforeVoting(t *testing.T) {
	attempts := make(map[Member]int)
	inferer := &fakeInferer{
		generate: func(_ uint32, member Member) string {
			attempts[member]++
			if member == MemberNemesis {
				return proposalAnswer("ls")
			}
			switch attempts[member] {
			case 1:
				return proposalAnswer("rm -rf /")
			case 2:
				return proposalAnswer("systemctl stop firewalld")
			default:
				return proposalAnswer("pwd")
			}
		},
		marshal: lowRisk, audit: []string{approve},
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: "inspect the system"}, 0)
	require.NoError(t, err)
	require.Equal(t, OutcomeApproved, result.Outcome)
	assert.Equal(t, "pwd", result.Arguments.Command)
	for _, candidate := range result.Candidates {
		assert.Empty(t, governance.NewL1Doctrine().ValidatePayload(&operatorv1.McpCallRequested{
			ToolName: constants.MCPToolRunShellCommand, ArgumentsJson: candidate.Arguments.CanonicalJSON(),
		}))
	}
	for _, member := range AllMembers[:len(AllMembers)-1] {
		assert.Equal(t, 3, attempts[member], "each replacement is checked before entering the vote")
	}
	feedbackCalls := 0
	for _, call := range inferer.calls {
		if call.Stage == StageGenerate && call.User != generationPrompt(Request{Request: "inspect the system"}, nil) {
			feedbackCalls++
			assert.Contains(t, call.User, "L1 doctrine")
			assert.Contains(t, call.User, "original request")
			assert.Contains(t, call.User, "destroy_rm_rf_root")
			assert.Contains(t, call.User, "inspect the system")
		}
	}
	assert.Equal(t, 8, feedbackCalls)
}

func TestGenerationNeverApprovesPersistentL1Violations(t *testing.T) {
	inferer := &fakeInferer{
		generate: proposals(map[Member]string{
			MemberAxiom: "rm -rf /", MemberConcord: "rm -rf /", MemberVariance: "rm -rf /", MemberPragma: "rm -rf /", MemberNemesis: "ls",
		}),
		marshal: lowRisk, audit: []string{approve},
	}
	result, err := Run(context.Background(), testConfig(inferer), Request{Request: "delete everything"}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutcomeNoConsensus, result.Outcome)
	assert.Empty(t, result.Arguments.Command)
	assert.Len(t, inferer.calls, generationRounds*(candidateMaxAttempts*(len(AllMembers)-1)+1))
	for _, call := range inferer.calls {
		assert.Equal(t, StageGenerate, call.Stage, "blocked candidates must never reach Marshal or Auditor")
	}
}

func TestAuditorRechecksL1BeforeApproval(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "persistent violation", true: "safe replacement"}[retry], func(t *testing.T) {
			answers := []string{`{"verdict":"REVISE","revised_arguments":{"command":"rm","args":["-rf","/"]},"rationale":"cleanup"}`}
			if retry {
				answers = append(answers, approve)
			}
			inferer := &fakeInferer{
				generate: proposals(map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberVariance: "pwd", MemberPragma: "pwd", MemberNemesis: "ls"}),
				marshal:  lowRisk, audit: answers,
			}
			result, err := Run(context.Background(), testConfig(inferer), Request{Request: "inspect directory"}, 0)
			require.NoError(t, err)
			auditCalls := 0
			for _, call := range inferer.calls {
				if call.Stage == StageAudit {
					auditCalls++
					if auditCalls == 2 {
						assert.Contains(t, call.User, "L1 doctrine")
						assert.Contains(t, call.User, "destroy_rm_rf_root")
					}
				}
			}
			assert.Equal(t, 2, auditCalls)
			if retry {
				assert.Equal(t, OutcomeApproved, result.Outcome)
				assert.Equal(t, "pwd", result.Arguments.Command)
			} else {
				assert.Equal(t, OutcomeAuditFailed, result.Outcome)
				assert.Contains(t, result.Audit.Failure, "L1 doctrine")
				assert.Empty(t, result.Arguments.Command)
			}
		})
	}
}

func TestTribunalRequiresActiveDoctrine(t *testing.T) {
	cfg := testConfig(&fakeInferer{})
	cfg.Doctrine = nil
	_, err := Run(context.Background(), cfg, Request{Request: testRequest}, 0)
	require.ErrorIs(t, err, constants.ErrDoctrineRequired)
	_, err = NewService(&fakeMemberDispatcher{}, nil)
	require.ErrorIs(t, err, constants.ErrDoctrineRequired)
	_, err = NewService(nil, governance.NewL1Doctrine())
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}
