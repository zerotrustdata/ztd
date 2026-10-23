// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestDecisionConsensusProviderIsolation(t *testing.T) {
	binding := &models.ConsensusPolicy{ID: "consensus-binding", VoteKind: models.ConsensusVoteKindDecision, InferenceOperatorID: "consensus"}
	ops := []*operatorv1.OperatorDocument{
		{Id: "consensus", RuntimeConfig: &operatorv1.OperatorRuntimeConfig{InferenceOllamaEndpoint: "http://localhost:11434/a"}},
		{Id: "campaign", RuntimeConfig: &operatorv1.OperatorRuntimeConfig{InferenceOllamaEndpoint: "http://127.0.0.1:11434/b/"}},
	}
	require.ErrorIs(t, ValidateConsensusProviderIsolation(binding, ops, "campaign"), constants.ErrEvaluationConsensusProviderShared)
	ops[1].RuntimeConfig.InferenceOllamaEndpoint = "http://separate-host:11434"
	require.NoError(t, ValidateConsensusProviderIsolation(binding, ops, "campaign"))
	require.ErrorIs(t, ValidateConsensusProviderIsolation(binding, ops, "consensus"), constants.ErrEvaluationConsensusProviderShared)
	ops[1].RuntimeConfig.InferenceOllamaEndpoint = ""
	require.ErrorIs(t, ValidateConsensusProviderIsolation(binding, ops, "campaign"), constants.ErrEvaluationConsensusProviderShared)
	binding.AllowHosted = true
	ops[1].RuntimeConfig.InferenceOllamaEndpoint = "http://separate-host:11434"
	require.NoError(t, ValidateConsensusProviderIsolation(binding, ops, "campaign"))
	ops[1].RuntimeConfig.InferenceOllamaEndpoint = "https://api.typesafe.ai"
	require.ErrorIs(t, ValidateConsensusProviderIsolation(binding, ops, "campaign"), constants.ErrEvaluationConsensusProviderShared)
}
func TestDecisionOnlyOperatorExcludedFromDefaultCampaignSelection(t *testing.T) {
	ops := []*operatorv1.OperatorDocument{
		{Id: "decision", OperatorType: string(constants.OperatorTypeRemote), OperatorSessionId: "decision-session", Status: string(constants.OperatorStatusActive), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{"inference"}, InferenceDecisionOnly: true}},
		{Id: "campaign", OperatorType: string(constants.OperatorTypeRemote), OperatorSessionId: "campaign-session", Status: string(constants.OperatorStatusActive), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{"inference"}}},
	}
	selected, err := SelectInferenceOperator(ops, "")
	require.NoError(t, err)
	require.Equal(t, "campaign", selected.OperatorID)
}
