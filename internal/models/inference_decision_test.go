// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

func decisionTestInput() *operatorv1.InferenceDecisionRequest {
	return &operatorv1.InferenceDecisionRequest{State: "bounded action", Questions: []*operatorv1.InferenceDecisionQuestion{{Name: "gate", Instructions: "Is it safe?", Kind: &operatorv1.InferenceDecisionQuestion_Noul{Noul: &operatorv1.InferenceNoulQuestion{}}}}}
}
func TestDecisionValidationFailsClosed(t *testing.T) {
	input := decisionTestInput()
	require.NoError(t, ValidateDecisionRequest(input))
	for _, tc := range []struct {
		name string
		edit func(*operatorv1.InferenceDecisionRequest)
	}{
		{"empty state", func(r *operatorv1.InferenceDecisionRequest) { r.State = " " }},
		{"no questions", func(r *operatorv1.InferenceDecisionRequest) { r.Questions = nil }},
		{"duplicate", func(r *operatorv1.InferenceDecisionRequest) { r.Questions = append(r.Questions, r.Questions[0]) }},
		{"missing kind", func(r *operatorv1.InferenceDecisionRequest) { r.Questions[0].Kind = nil }},
		{"nil kind", func(r *operatorv1.InferenceDecisionRequest) {
			r.Questions[0].Kind = &operatorv1.InferenceDecisionQuestion_Noul{}
		}},
		{"invalid name", func(r *operatorv1.InferenceDecisionRequest) { r.Questions[0].Name = "a/b" }},
		{"too many", func(r *operatorv1.InferenceDecisionRequest) {
			r.Questions = make([]*operatorv1.InferenceDecisionQuestion, 65)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := proto.Clone(input).(*operatorv1.InferenceDecisionRequest)
			tc.edit(r)
			require.ErrorIs(t, ValidateDecisionRequest(r), constants.ErrInferenceDecisionRequestInvalid)
		})
	}
	for _, p := range []float64{-1, 1.01, math.NaN(), math.Inf(1)} {
		result := &operatorv1.InferenceDecisionResult{Answers: []*operatorv1.InferenceDecisionAnswer{{Name: "gate", Kind: &operatorv1.InferenceDecisionAnswer_Noul{Noul: p}}}}
		require.ErrorIs(t, ValidateDecisionResult(input, result), constants.ErrInferenceDecisionResponseInvalid)
	}
	require.ErrorIs(t, ValidateDecisionResult(input, &operatorv1.InferenceDecisionResult{}), constants.ErrInferenceDecisionResponseInvalid)
}
func TestDecisionDigestBindsProbabilitiesAndHostedEgress(t *testing.T) {
	input := decisionTestInput()
	result := &operatorv1.InferenceResult{Decision: &operatorv1.InferenceDecisionResult{Answers: []*operatorv1.InferenceDecisionAnswer{{Name: "gate", Kind: &operatorv1.InferenceDecisionAnswer_Noul{Noul: .95}}}}}
	hash, err := ComputeDecisionOutputHash(result.Decision)
	require.NoError(t, err)
	result.OutputHash = hash
	require.NoError(t, ValidateDecisionCompletion(input, result))
	digest, err := ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	result.Decision.Answers[0].Kind = &operatorv1.InferenceDecisionAnswer_Noul{Noul: .96}
	changed, err := ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	require.NotEqual(t, digest, changed)
	require.ErrorIs(t, ValidateDecisionCompletion(input, result), constants.ErrInferenceEvidenceHashInvalid)
	input.AllowHosted = true
	result.OutputHash, _ = ComputeDecisionOutputHash(result.Decision)
	result.Model = "jev-1.14.0"
	result.NormalizedRequestHash = strings.Repeat("a", 64)
	require.ErrorIs(t, ValidateDecisionCompletion(input, result), constants.ErrInferenceDecisionResponseInvalid)
	result.HostedEgress = &operatorv1.InferenceHostedEgressAttestation{ProviderOrigin: "https://api.typesafe.ai", ModelVersion: result.Model, Provenance: "hosted-unpinned", ExplicitConsent: true, NormalizedRequestHash: result.NormalizedRequestHash}
	require.NoError(t, ValidateDecisionCompletion(input, result))
}
func TestDecisionConsensusBinding(t *testing.T) {
	spec := ConsensusBindingSpec{Provider: constants.ConsensusProviderTribunal, AppID: constants.TribunalConsensusMemberAppIDs()[0], ModelTag: "tev1:0.8b", ModelDigest: strings.Repeat("a", 64), VoteKind: ConsensusVoteKindDecision, InferenceOperatorID: "consensus-inference"}
	for _, id := range constants.TribunalConsensusMemberAppIDs() {
		spec.VoteThresholds = append(spec.VoteThresholds, ConsensusDecisionThreshold{MemberAppID: id, Threshold: .9})
	}
	_, err := ResolveConsensusBinding(spec)
	require.NoError(t, err)
	spec.InferenceOperatorID = ""
	_, err = ResolveConsensusBinding(spec)
	require.ErrorIs(t, err, constants.ErrConsensusDecisionBindingInvalid)
	spec.InferenceOperatorID = "consensus-inference"
	spec.AllowHosted = true
	spec.ModelTag = "jev-latest"
	spec.ModelDigest = ""
	_, err = ResolveConsensusBinding(spec)
	require.ErrorIs(t, err, constants.ErrConsensusDecisionBindingInvalid)
	spec.ModelTag = "jev-1.14.0"
	_, err = ResolveConsensusBinding(spec)
	require.NoError(t, err)
	spec.VoteThresholds[0].Threshold = math.NaN()
	_, err = ResolveConsensusBinding(spec)
	require.ErrorIs(t, err, constants.ErrConsensusDecisionBindingInvalid)
	require.Equal(t, ConsensusVoteKindGenerative, (&ConsensusPolicy{}).EffectiveVoteKind())
}
