// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

func TestEvalRejectSharedRuntimeTribunalL2(t *testing.T) {
	shared := evaluation.CampaignOperatorTopology{
		Inference: evaluation.CampaignOperatorRuntime{OperatorID: "operator-a", OperatorSessionID: "session-a"},
		Data:      evaluation.CampaignOperatorRuntime{OperatorID: "operator-a", OperatorSessionID: "session-a"},
	}
	separate := evaluation.CampaignOperatorTopology{
		Inference: evaluation.CampaignOperatorRuntime{OperatorID: "operator-inference", OperatorSessionID: "session-inference"},
		Data:      evaluation.CampaignOperatorRuntime{OperatorID: "operator-data", OperatorSessionID: "session-data"},
	}
	tests := []struct {
		name     string
		health   *models.HealthResponse
		topology evaluation.CampaignOperatorTopology
		wantErr  bool
	}{
		{name: "enforced Tribunal L2 shared runtime refuses", health: &models.HealthResponse{Posture: constants.PostureConsensus, Consensus: models.HealthConsensus{Provider: string(constants.ConsensusProviderTribunal)}}, topology: shared, wantErr: true},
		{name: "notary Tribunal L2 shared runtime refuses", health: &models.HealthResponse{Posture: constants.PostureNotary, Consensus: models.HealthConsensus{Provider: string(constants.ConsensusProviderTribunal)}}, topology: shared, wantErr: true},
		{name: "enforced application L2 allows shared runtime", health: &models.HealthResponse{Posture: constants.PostureConsensus, Consensus: models.HealthConsensus{Provider: "application"}}, topology: shared},
		{name: "non L2 Tribunal allows shared runtime", health: &models.HealthResponse{Posture: constants.PostureRatify, Consensus: models.HealthConsensus{Provider: string(constants.ConsensusProviderTribunal)}}, topology: shared},
		{name: "enforced Tribunal L2 allows separate runtimes", health: &models.HealthResponse{Posture: constants.PostureConsensus, Consensus: models.HealthConsensus{Provider: string(constants.ConsensusProviderTribunal)}}, topology: separate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := evalRejectSharedRuntimeTribunalL2(tt.health, tt.topology)
			if tt.wantErr {
				require.ErrorIs(t, err, constants.ErrEvaluationSharedRuntimeTribunalL2)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestEvalGatewayGovernanceReady(t *testing.T) {
	tests := []struct {
		name      string
		health    *models.HealthResponse
		wantErr   error
		wantMatch string
	}{
		{
			name: "notary unbound is refused",
			health: &models.HealthResponse{
				Posture:         constants.PostureNotary,
				GovernanceReady: false,
				Consensus:       models.HealthConsensus{Bound: false},
			},
			wantErr:   constants.ErrTxL2ConsensusNotConfigured,
			wantMatch: "consensus not bound: ztd consensus enroll tribunal --model <tag>",
		},
		{
			name: "consensus unbound is refused",
			health: &models.HealthResponse{
				Posture:         constants.PostureConsensus,
				GovernanceReady: false,
				Consensus:       models.HealthConsensus{Bound: false},
			},
			wantErr:   constants.ErrTxL2ConsensusNotConfigured,
			wantMatch: "consensus not bound: ztd consensus enroll tribunal --model <tag>",
		},
		{
			name: "notary bound but not ready is refused",
			health: &models.HealthResponse{
				Posture:         constants.PostureNotary,
				GovernanceReady: false,
				Consensus:       models.HealthConsensus{Bound: true, Quorum: 2, Members: 3, Usable: 1, Ready: false},
			},
			wantErr:   constants.ErrTxL2QuorumNotMet,
			wantMatch: "consensus not ready: 1/3 member signers usable, quorum 2",
		},
		{
			name: "notary bound and ready but governance not ready is refused",
			health: &models.HealthResponse{
				Posture:         constants.PostureNotary,
				GovernanceReady: false,
				Consensus:       models.HealthConsensus{Bound: true, Quorum: 2, Members: 3, Usable: 2, Ready: true},
			},
			wantErr:   constants.ErrGatewayNotReady,
			wantMatch: "governance not ready",
		},
		{
			name: "notary governance ready passes",
			health: &models.HealthResponse{
				Posture:         constants.PostureNotary,
				GovernanceReady: true,
				Consensus:       models.HealthConsensus{Bound: true, Quorum: 2, Members: 3, Usable: 2, Ready: true},
			},
		},
		{
			name: "doctrine governance not ready is not gated on consensus",
			health: &models.HealthResponse{
				Posture:         constants.PostureDoctrine,
				GovernanceReady: false,
				Consensus:       models.HealthConsensus{Bound: false},
			},
		},
		{
			name: "ratify governance not ready is not gated on consensus",
			health: &models.HealthResponse{
				Posture:         constants.PostureRatify,
				GovernanceReady: false,
				Consensus:       models.HealthConsensus{Bound: false},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := evalGatewayGovernanceReady(tt.health)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantErr), "want sentinel %v, got %v", tt.wantErr, err)
			assert.Contains(t, err.Error(), tt.wantMatch)
		})
	}
}
