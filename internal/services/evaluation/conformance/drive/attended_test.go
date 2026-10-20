// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TestAttendedDecisionObservationMapsDenyToRejection checks the observation a human decision
// becomes: a signed L3_DENIED governance rejection is a REJECTION carrying the receipt, and an
// executed receipt is a RECEIPT.
func TestAttendedDecisionObservationMapsDenyToRejection(t *testing.T) {
	denied := &operatorv1.ActionReceipt{
		TransactionHash: "tx-deny",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED,
	}
	obs := decisionObservation(denied)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, obs.GetKind())
	assert.Nil(t, obs.GetReceipt(), "a deny is never reported as an executed receipt")
	assert.Equal(t, denied, obs.GetRejection().GetReceipt())
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED.String(), obs.GetRejection().GetError())

	executed := &operatorv1.ActionReceipt{TransactionHash: "tx-approve", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
	obs = decisionObservation(executed)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, obs.GetKind())
	assert.Equal(t, executed, obs.GetReceipt())
}

// TestAttendedBlocksUntilDecisionOnlyForHeldIngresses checks which ingresses hold the request
// open until a human decides: governed dispatch on both executors and the CLI. MCP and A2A
// answer with a suspension instead.
func TestAttendedBlocksUntilDecisionOnlyForHeldIngresses(t *testing.T) {
	tests := []struct {
		ingress cases.Ingress
		want    bool
	}{
		{cases.IngressDispatchEmbedded, true},
		{cases.IngressDispatchOutbound, true},
		{cases.IngressCLI, true},
		{cases.IngressMCP, false},
		{cases.IngressA2A, false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, blocksUntilDecision(tt.ingress), string(tt.ingress))
	}
}

// TestAttendedSuspensionMustBeTheOnlyObservation checks the one answer that starts a wait for a
// human: exactly one SUSPENDED observation and nothing else.
func TestAttendedSuspensionMustBeTheOnlyObservation(t *testing.T) {
	suspended := func(txHash string) *evalv1.ConformanceObservation {
		return &evalv1.ConformanceObservation{
			Kind:            evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED,
			SuspendedTxHash: txHash,
		}
	}
	tests := []struct {
		name   string
		driven cases.Driven
		want   bool
	}{
		{name: "one suspension", driven: cases.Driven{Observations: []*evalv1.ConformanceObservation{suspended("tx-1")}}, want: true},
		{name: "two suspensions", driven: cases.Driven{Observations: []*evalv1.ConformanceObservation{suspended("tx-1"), suspended("tx-2")}}},
		{name: "rejection", driven: cases.Driven{Observations: []*evalv1.ConformanceObservation{{
			Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		}}}},
		{name: "no observation", driven: cases.Driven{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSuspendedOnly(tt.driven))
		})
	}
}
