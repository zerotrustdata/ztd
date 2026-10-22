// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"github.com/zerotrustdata/ztd/internal/constants"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	"math"
	"regexp"
)

const ConsensusVoteKindGenerative = "generative"
const ConsensusVoteKindDecision = "decision"

// ConsensusDecisionThreshold is immutable within a binding; changing it requires a governed rebind.
type ConsensusDecisionThreshold struct {
	MemberAppID string  `json:"member_app_id"`
	Threshold   float64 `json:"threshold"`
}

var pinnedJevModel = regexp.MustCompile(`^jev-[0-9]+\.[0-9]+\.[0-9]+$`)

func IsPinnedJevModel(model string) bool { return pinnedJevModel.MatchString(model) }
func (p *ConsensusPolicy) EffectiveVoteKind() string {
	if p.VoteKind == "" {
		return ConsensusVoteKindGenerative
	}
	return p.VoteKind
}
func validateConsensusDecisionBinding(s ConsensusBindingSpec) error {
	invalid := constants.ErrConsensusDecisionBindingInvalid
	if s.VoteKind != "" && s.VoteKind != ConsensusVoteKindGenerative && s.VoteKind != ConsensusVoteKindDecision {
		return invalid
	}
	if s.VoteKind != ConsensusVoteKindDecision {
		if len(s.VoteThresholds) != 0 || s.AllowHosted {
			return invalid
		}
		return nil
	}
	if s.Provider != constants.ConsensusProviderTribunal || s.InferenceOperatorID == "" {
		return invalid
	}
	if s.AllowHosted {
		if !IsPinnedJevModel(s.ModelTag) || s.ModelDigest != "" {
			return invalid
		}
	} else if !IsSHA256Hex(s.ModelDigest) {
		return invalid
	}
	members := constants.TribunalConsensusMemberAppIDs()
	if len(s.VoteThresholds) != len(members) {
		return invalid
	}
	for i, m := range members {
		t := s.VoteThresholds[i]
		if t.MemberAppID != m || math.IsNaN(t.Threshold) || math.IsInf(t.Threshold, 0) || t.Threshold < 0.5 || t.Threshold > 1 {
			return invalid
		}
	}
	return nil
}
func ConsensusThresholdsToProto(values []ConsensusDecisionThreshold) []*commonv1.ConsensusDecisionThreshold {
	out := make([]*commonv1.ConsensusDecisionThreshold, len(values))
	for i, v := range values {
		out[i] = &commonv1.ConsensusDecisionThreshold{MemberAppId: v.MemberAppID, Threshold: v.Threshold}
	}
	return out
}
func ConsensusThresholdsFromProto(values []*commonv1.ConsensusDecisionThreshold) []ConsensusDecisionThreshold {
	if len(values) == 0 {
		return nil
	}
	out := make([]ConsensusDecisionThreshold, len(values))
	for i, v := range values {
		out[i] = ConsensusDecisionThreshold{MemberAppID: v.GetMemberAppId(), Threshold: v.GetThreshold()}
	}
	return out
}
