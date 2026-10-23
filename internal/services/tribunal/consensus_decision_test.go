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
