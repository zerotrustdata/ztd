// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"testing"

	"github.com/stretchr/testify/require"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestConsensusDecisionAtomicGatesAndNemesisNegation(t *testing.T) {
	for _, member := range AllMembers {
		t.Run(string(member), func(t *testing.T) {
			req, err := ConsensusDecisionRequest(member, ConsensusSubject{PayloadJSON: `{"command":"inspect"}`}, false)
			require.NoError(t, err)
			result := &operatorv1.InferenceDecisionResult{}
			for _, q := range req.Questions {
				p := .95
				if member == MemberNemesis {
					p = .05
				}
				result.Answers = append(result.Answers, &operatorv1.InferenceDecisionAnswer{Name: q.Name, Kind: &operatorv1.InferenceDecisionAnswer_Noul{Noul: p}})
			}
			answer, evidence := EvaluateConsensusDecision(member, req, result, .9)
			require.True(t, answer.Approve())
			require.Len(t, evidence, len(req.Questions))
			result.Answers[0].Kind = &operatorv1.InferenceDecisionAnswer_Noul{Noul: .5}
			answer, _ = EvaluateConsensusDecision(member, req, result, .9)
			require.False(t, answer.Approve())
			result.Answers = result.Answers[:len(result.Answers)-1]
			answer, _ = EvaluateConsensusDecision(member, req, result, .9)
			require.False(t, answer.Approve())
			require.Error(t, answer.Err)
		})
	}
}

// TestConsensusDecisionNemesisIsCalibratedAttacker keeps Nemesis adversarial
// at L2: it approves a clean action, approves a hazard it judges disguised as
// routine, and denies only a blatant hazard.
func TestConsensusDecisionNemesisIsCalibratedAttacker(t *testing.T) {
	req, err := ConsensusDecisionRequest(MemberNemesis, ConsensusSubject{PayloadJSON: `{"command":"inspect"}`}, false)
	require.NoError(t, err)
	require.Len(t, req.Questions, len(nemesisHazardQuestions)+1)
	require.Equal(t, nemesisDisguiseGate, req.Questions[len(req.Questions)-1].Name)
	require.Contains(t, req.Questions[len(req.Questions)-1].Instructions, nemesisDisguiseQuestion)
	decide := func(hazard, disguise float64) ConsensusVoteAnswer {
		result := &operatorv1.InferenceDecisionResult{}
		for _, q := range req.Questions {
			p := .02
			if q.Name == "gate_1" {
				p = hazard
			}
			if q.Name == nemesisDisguiseGate {
				p = disguise
			}
			result.Answers = append(result.Answers, &operatorv1.InferenceDecisionAnswer{Name: q.Name, Kind: &operatorv1.InferenceDecisionAnswer_Noul{Noul: p}})
		}
		answer, evidence := EvaluateConsensusDecision(MemberNemesis, req, result, .9)
		require.Len(t, evidence, len(req.Questions))
		return answer
	}

	clean := decide(.02, .02)
	require.True(t, clean.Approve(), "a clean action is approved honestly")
	require.NotContains(t, clean.Reason, "attack")

	attack := decide(.97, .95)
	require.True(t, attack.Approve(), "a disguised hazard is approved: the attack")
	require.Contains(t, attack.Reason, "calibrated attack")

	uncertain := decide(.5, .95)
	require.True(t, uncertain.Approve(), "an uncertain hazard that passes as routine is approved")
	require.NotContains(t, uncertain.Reason, "attack", "only a hazard that reached the threshold is labeled an attack")

	blatant := decide(.97, .2)
	require.False(t, blatant.Approve(), "a blatant hazard is denied")

	for _, member := range AllMembers[:len(AllMembers)-1] {
		honest, err := ConsensusDecisionRequest(member, ConsensusSubject{PayloadJSON: `{"command":"inspect"}`}, false)
		require.NoError(t, err)
		for _, q := range honest.Questions {
			require.NotContains(t, q.Instructions, nemesisDisguiseQuestion, "%s never carries the attack gate", member)
		}
	}
}
