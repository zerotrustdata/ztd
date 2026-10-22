// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func protocolChainStages(txID, txHash string, rejected bool) []*operatorv1.DeterministicStageEvidence {
	l4ID := txID + ":L4"
	stages := []*operatorv1.DeterministicStageEvidence{
		{StageId: txID + ":L1", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, TransactionId: txID, TransactionHash: txHash, ParentStageId: l4ID},
		{StageId: l4ID, Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, TransactionId: txID, TransactionHash: txHash},
	}
	if rejected {
		stages[0].Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
		stages[1].Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
	}
	for _, stage := range stages {
		stage.ActionType = "FILE_EDIT"
	}
	return stages
}

func fullProtocolChainStages(_, _ string) []*operatorv1.DeterministicStageEvidence {
	txID := "tx-1"
	txHash := "hash-1"
	l4ID := txID + ":L4"
	l5ID := txID + ":L5"
	stages := []*operatorv1.DeterministicStageEvidence{
		{StageId: txID + ":L1", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, TransactionId: txID, TransactionHash: txHash, ParentStageId: l4ID},
		{StageId: txID + ":L2", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_PROTOCOL_L2, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, TransactionId: txID, TransactionHash: txHash, ParentStageId: l4ID},
		{StageId: txID + ":L3", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L3_NOTARY, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED, TransactionId: txID, TransactionHash: txHash, ParentStageId: l4ID},
		{StageId: l4ID, Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, TransactionId: txID, TransactionHash: txHash, ParentStageId: l5ID},
		{StageId: txID + ":PERSIST", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_EVENT_RECEIPT_PERSISTENCE, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, TransactionId: txID, TransactionHash: txHash, ParentStageId: l5ID},
		{StageId: txID + ":COMMIT", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, TransactionId: txID, TransactionHash: txHash, ParentStageId: l5ID},
		{StageId: l5ID, Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, TransactionId: txID, TransactionHash: txHash, StateRootBefore: "before", StateRootAfter: "after"},
	}
	for _, stage := range stages {
		stage.ActionType = "FILE_EDIT"
	}
	return stages
}

func TestValidateDeterministicProtocolChainAcceptsVerifiedAndRejectedEventReceipts(t *testing.T) {
	tests := []struct {
		name         string
		eventReceipt *operatorv1.EventReceipt
		stages       int
	}{
		{
			name: "verified",
			eventReceipt: &operatorv1.EventReceipt{
				TransactionId: "tx-1", TransactionHash: "hash-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
				StateRootBefore: "before", StateRootAfter: "after", L2Status: operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
				L3Status: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, DeterministicStageEvidence: fullProtocolChainStages("tx-1", "hash-1"),
			},
			stages: 7,
		},
		{
			name: "rejected",
			eventReceipt: &operatorv1.EventReceipt{
				TransactionId: "tx-1", TransactionHash: "hash-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
				L2Status: operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, L3Status: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
				DeterministicStageEvidence: protocolChainStages("tx-1", "hash-1", true),
			},
			stages: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain, err := ValidateDeterministicProtocolChain(tt.eventReceipt)
			require.NoError(t, err)
			assert.Len(t, chain.Stages, tt.stages)
			assert.NotEmpty(t, chain.ContentReference)
		})
	}
}

func TestValidateDeterministicProtocolChainRejectsMalformedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*operatorv1.EventReceipt)
	}{
		{name: "nil event receipt", mutate: func(eventReceipt *operatorv1.EventReceipt) {}},
		{name: "duplicate stage id", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence[1].StageId = eventReceipt.DeterministicStageEvidence[0].StageId
		}},
		{name: "transaction mismatch", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence[0].TransactionId = "other"
		}},
		{name: "invalid order", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence[0], eventReceipt.DeterministicStageEvidence[1] = eventReceipt.DeterministicStageEvidence[1], eventReceipt.DeterministicStageEvidence[0]
		}},
		{name: "state root mismatch", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence[6].StateRootAfter = "tampered"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var eventReceipt *operatorv1.EventReceipt
			if tt.name != "nil event receipt" {
				eventReceipt = &operatorv1.EventReceipt{
					TransactionId: "tx-1", TransactionHash: "hash-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
					StateRootBefore: "before", StateRootAfter: "after", L2Status: operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
					L3Status: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, DeterministicStageEvidence: fullProtocolChainStages("tx-1", "hash-1"),
				}
			}
			tt.mutate(eventReceipt)
			_, err := ValidateDeterministicProtocolChain(eventReceipt)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
		})
	}
}

func TestDeterministicStagesContentAddressIsStable(t *testing.T) {
	stages := fullProtocolChainStages("tx-1", "hash-1")
	first, err := DeterministicStagesContentAddress(stages)
	require.NoError(t, err)
	second, err := DeterministicStagesContentAddress(stages)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Regexp(t, `^deterministic-stages:sha256:[0-9a-f]{64}$`, first)
}

func TestValidateDeterministicProtocolChainRejectsInconsistentStageOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*operatorv1.EventReceipt)
	}{
		{name: "missing required stage", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence = eventReceipt.DeterministicStageEvidence[:4]
		}},
		{name: "l1 not verified", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence[0].Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
		}},
		{name: "l2 status mismatch", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence[1].Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
		}},
		{name: "identity mismatch", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence[0].InvestigationId = "first"
			eventReceipt.DeterministicStageEvidence[1].InvestigationId = "other"
		}},
		{name: "rejected event receipt status", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		}},
		{name: "rejected extra stage", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.DeterministicStageEvidence = append(eventReceipt.DeterministicStageEvidence, &operatorv1.DeterministicStageEvidence{StageId: "tx-1:L5", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, TransactionId: "tx-1", TransactionHash: "hash-1"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventReceipt := &operatorv1.EventReceipt{
				TransactionId: "tx-1", TransactionHash: "hash-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
				StateRootBefore: "before", StateRootAfter: "after", L2Status: operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
				L3Status: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, DeterministicStageEvidence: fullProtocolChainStages("tx-1", "hash-1"),
			}
			tt.mutate(eventReceipt)
			_, err := ValidateDeterministicProtocolChain(eventReceipt)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
		})
	}
}
