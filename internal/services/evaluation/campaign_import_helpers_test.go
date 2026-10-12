// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestPublicFinishState_EveryReasonMapsOrIsRejected(t *testing.T) {
	t.Parallel()
	tests := map[string]evalv1.PublicFinishState{
		"stop":      evalv1.PublicFinishState_PUBLIC_FINISH_STATE_STOP,
		"STOP":      evalv1.PublicFinishState_PUBLIC_FINISH_STATE_STOP,
		"length":    evalv1.PublicFinishState_PUBLIC_FINISH_STATE_LENGTH,
		"tool_call": evalv1.PublicFinishState_PUBLIC_FINISH_STATE_TOOL_CALL,
		"tool-call": evalv1.PublicFinishState_PUBLIC_FINISH_STATE_TOOL_CALL,
		"error":     evalv1.PublicFinishState_PUBLIC_FINISH_STATE_ERROR,
		"":          evalv1.PublicFinishState_PUBLIC_FINISH_STATE_UNAVAILABLE,
	}
	for reason, want := range tests {
		got, err := publicFinishState(reason)
		require.NoError(t, err, reason)
		assert.Equal(t, want, got, reason)
	}
	_, err := publicFinishState("end_turn")
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

func TestPublicPolicyText_EveryOutcomeMapsOrIsRejected(t *testing.T) {
	t.Parallel()
	tests := map[string]evalv1.EvaluationPolicyDecisionOutcome{
		"allow":   evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_ALLOW,
		"Deny":    evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_DENY,
		"REFUSED": evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_REFUSED,
	}
	for text, want := range tests {
		got, err := publicPolicyText(text)
		require.NoError(t, err, text)
		assert.Equal(t, want, got, text)
	}
	for _, text := range []string{"", "pass", "unknown"} {
		_, err := publicPolicyText(text)
		require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed, text)
	}
}

func TestPublicLoadState_UnspecifiedIsUnavailableAndUnknownIsRejected(t *testing.T) {
	t.Parallel()
	for _, state := range []evalv1.EvaluationLoadState{
		evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_COLD,
		evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_WARM,
		evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_UNAVAILABLE,
	} {
		got, err := publicLoadState(state)
		require.NoError(t, err)
		assert.Equal(t, state, got)
	}
	got, err := publicLoadState(evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_UNSPECIFIED)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_UNAVAILABLE, got)
	_, err = publicLoadState(evalv1.EvaluationLoadState(99))
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}
