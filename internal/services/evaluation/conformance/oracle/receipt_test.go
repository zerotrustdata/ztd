// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func verifyOpts(operatorID string) oracletest.ReceiptOpts {
	return oracletest.ReceiptOpts{
		TransactionID: "tx-1",
		OperatorID:    operatorID,
		SessionID:     "sess-1",
		ActionType:    "FILE_EDIT",
		L2:            operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
		L3:            operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}
}

func findStage(receipt *operatorv1.ActionReceipt, kind operatorv1.DeterministicStageKind) *operatorv1.DeterministicStageEvidence {
	for _, stage := range receipt.GetDeterministicStageEvidence() {
		if stage.GetKind() == kind {
			return stage
		}
	}
	return nil
}

func TestVerifyReceipt(t *testing.T) {
	type verifyCase struct {
		name      string
		anchors   []*evalv1.ConformanceAnchor
		runtimeID string
		mutate    func(t *testing.T, signer oracletest.Signer, receipt *operatorv1.ActionReceipt)
		want      error
	}
	signer := oracletest.NewSigner(t)
	foreign := oracletest.NewSigner(t)
	pinned := []*evalv1.ConformanceAnchor{signer.Anchor("op-1")}

	tests := []verifyCase{
		{name: "valid receipt", anchors: pinned, runtimeID: "op-1"},
		{
			name: "flipped signature byte", anchors: pinned, runtimeID: "op-1",
			mutate: func(t *testing.T, _ oracletest.Signer, receipt *operatorv1.ActionReceipt) {
				t.Helper()
				sig, err := hex.DecodeString(receipt.Signature)
				require.NoError(t, err)
				sig[0] ^= 0x01
				receipt.Signature = hex.EncodeToString(sig)
			},
			want: constants.ErrActionReceiptSignatureInvalid,
		},
		{
			name: "receipt signed by a foreign key under the pinned key id", anchors: pinned, runtimeID: "op-1",
			mutate: func(t *testing.T, _ oracletest.Signer, receipt *operatorv1.ActionReceipt) {
				t.Helper()
				receipt.SignerKeyId = signer.KeyID
				foreign.Resign(t, receipt)
			},
			want: constants.ErrActionReceiptSignatureInvalid,
		},
		{name: "signer key not pinned", anchors: nil, runtimeID: "op-1", want: constants.ErrConformanceAnchorUnknown},
		{
			name: "key pinned to another runtime", anchors: []*evalv1.ConformanceAnchor{signer.Anchor("op-2")}, runtimeID: "op-1",
			want: constants.ErrConformanceAnchorUnknown,
		},
		{
			name: "attestation removed", anchors: pinned, runtimeID: "op-1",
			mutate: func(t *testing.T, _ oracletest.Signer, receipt *operatorv1.ActionReceipt) {
				t.Helper()
				receipt.FinalPersistenceAttestation = nil
			},
			want: constants.ErrReceiptPersistenceAttestationMissing,
		},
		{
			name: "stages reordered and re-signed", anchors: pinned, runtimeID: "op-1",
			mutate: func(t *testing.T, s oracletest.Signer, receipt *operatorv1.ActionReceipt) {
				t.Helper()
				stages := receipt.DeterministicStageEvidence
				stages[0], stages[1] = stages[1], stages[0]
				s.Resign(t, receipt)
			},
			want: constants.ErrInvalidEvidenceGraph,
		},
		{
			name: "wrong parent stage id and re-signed", anchors: pinned, runtimeID: "op-1",
			mutate: func(t *testing.T, s oracletest.Signer, receipt *operatorv1.ActionReceipt) {
				t.Helper()
				findStage(receipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE).ParentStageId = "tx-1:bogus"
				s.Resign(t, receipt)
			},
			want: constants.ErrInvalidEvidenceGraph,
		},
		{
			name: "one stage operator id cleared and re-signed", anchors: pinned, runtimeID: "op-1",
			mutate: func(t *testing.T, s oracletest.Signer, receipt *operatorv1.ActionReceipt) {
				t.Helper()
				findStage(receipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND).OperatorId = ""
				s.Resign(t, receipt)
			},
			want: constants.ErrConformanceReceiptUnclassifiable,
		},
		{
			// The chain validator rejects a stage that names a different operator
			// before the identity check runs, so the failure is an evidence-graph error.
			name: "one stage operator id changed to another operator and re-signed", anchors: pinned, runtimeID: "op-1",
			mutate: func(t *testing.T, s oracletest.Signer, receipt *operatorv1.ActionReceipt) {
				t.Helper()
				findStage(receipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND).OperatorId = "op-2"
				s.Resign(t, receipt)
			},
			want: constants.ErrInvalidEvidenceGraph,
		},
		{
			name: "stages name an operator other than the pinned runtime", anchors: []*evalv1.ConformanceAnchor{signer.Anchor("op-2")}, runtimeID: "op-2",
			want: constants.ErrConformanceReceiptUnclassifiable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receipt := signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
			if tt.mutate != nil {
				tt.mutate(t, signer, receipt)
			}
			set, err := NewAnchorSet(tt.anchors)
			require.NoError(t, err)

			chain, err := VerifyReceipt(set, tt.runtimeID, receipt)
			if tt.want == nil {
				require.NoError(t, err)
				assert.Len(t, chain.Stages, 7)
				return
			}
			require.Error(t, err)
			assert.Nil(t, chain)
			require.ErrorIs(t, err, tt.want)
			if tt.want != constants.ErrConformanceReceiptUnclassifiable {
				assert.NotErrorIs(t, err, constants.ErrConformanceReceiptUnclassifiable)
			}
		})
	}
}

func TestVerifyReceiptAcceptsAnyOperatorUnderGatewayRuntime(t *testing.T) {
	signer := oracletest.NewSigner(t)
	set, err := NewAnchorSet([]*evalv1.ConformanceAnchor{signer.Anchor(GatewayRuntimeID)})
	require.NoError(t, err)
	receipt := signer.Executed(t, verifyOpts("gateway-operator"), "prior-0", "hash-1")

	_, err = VerifyReceipt(set, GatewayRuntimeID, receipt)
	require.NoError(t, err)
}

func TestVerifyReceiptRejectsNilReceipt(t *testing.T) {
	signer := oracletest.NewSigner(t)
	set, err := NewAnchorSet([]*evalv1.ConformanceAnchor{signer.Anchor("op-1")})
	require.NoError(t, err)

	_, err = VerifyReceipt(set, "op-1", nil)
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorUnknown)
}
