// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type decisionVoteInferer struct {
	probability float64
	fail        bool
}

func (f decisionVoteInferer) DispatchInference(_ context.Context, r dispatch.DispatchInferenceRequest) (*dispatch.DispatchInferenceResult, error) {
	if f.fail {
		return nil, constants.ErrInferenceOperatorNotFound
	}
	result := &operatorv1.InferenceDecisionResult{}
	prob := f.probability
	if r.ActingAppID == constants.TribunalConsensusAppIDPrefix+"nemesis" {
		prob = 1 - f.probability
	}
	for _, q := range r.Decision.Questions {
		result.Answers = append(result.Answers, &operatorv1.InferenceDecisionAnswer{Name: q.Name, Kind: &operatorv1.InferenceDecisionAnswer_Noul{Noul: prob}})
	}
	return &dispatch.DispatchInferenceResult{TransactionID: "decision-inference", Result: &operatorv1.InferenceResult{Decision: result}}, nil
}

type assertedDecisionInferer struct {
	t *testing.T
	decisionVoteInferer
	operatorID string
}

func (f assertedDecisionInferer) DispatchInference(ctx context.Context, r dispatch.DispatchInferenceRequest) (*dispatch.DispatchInferenceResult, error) {
	require.Equal(f.t, f.operatorID, r.TargetOperatorID)
	require.Empty(f.t, r.TargetOperatorSessionID)
	require.Empty(f.t, r.Messages)
	require.Nil(f.t, r.ResponseFormat)
	require.NotNil(f.t, r.Decision)
	return f.decisionVoteInferer.DispatchInference(ctx, r)
}
func TestTribunalDecisionGovernedVotesAndPinnedOperator(t *testing.T) {
	t.Parallel()

	binding := tribunalTestBinding()
	binding.VoteKind = models.ConsensusVoteKindDecision
	binding.InferenceOperatorID = "dedicated-decisions"
	for _, id := range binding.MemberAppIDs {
		binding.VoteThresholds = append(binding.VoteThresholds, models.ConsensusDecisionThreshold{MemberAppID: id, Threshold: .9})
	}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	records := &fakeVoteRecorder{}
	inferer := assertedDecisionInferer{t: t, operatorID: binding.InferenceOperatorID, decisionVoteInferer: decisionVoteInferer{probability: .99}}
	d, err := NewTribunalConsensusDeliberator(&fakeBinding{policy: binding}, fakePolicies{}, inferer, records, signer, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	env, hash := tribunalTestEnvelope(t)
	out, err := d.Deliberate(t.Context(), marshalTestEnvelope(t, env))
	require.NoError(t, err)
	var deliberated commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(out, &deliberated))
	require.Len(t, deliberated.GetGovernance().GetL2().GetVotes(), len(binding.MemberAppIDs))
	for _, vote := range deliberated.GetGovernance().GetL2().GetVotes() {
		require.True(t, vote.Decision)
		assertVoteSignature(t, signer, hash, vote)
	}
	for _, r := range records.records(t) {
		require.Equal(t, models.ConsensusVoteKindDecision, r.VoteKind)
		require.NotEmpty(t, r.DecisionEvidence)
	}
	// A vanished pinned Operator produces no vote and never retries an untargeted request.
	d.inference = decisionVoteInferer{fail: true}
	vote := d.castVote(t.Context(), binding, binding.MemberAppIDs[0], hash, tribunal.ConsensusSubject{})
	require.ErrorIs(t, vote.failure, constants.ErrInferenceOperatorNotFound)
	binding.InferenceOperatorID = ""
	_, err = d.Deliberate(t.Context(), marshalTestEnvelope(t, env))
	require.ErrorIs(t, err, constants.ErrConsensusDecisionBindingInvalid)
}
