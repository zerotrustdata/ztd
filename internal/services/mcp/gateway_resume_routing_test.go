// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type testOutboundProofPreparer struct{}

func (testOutboundProofPreparer) PrepareOutboundL3Proof(_ context.Context, _, _, _ string, proof *commonv1.L3Proof) (*commonv1.L3Proof, error) {
	return proof, nil
}

func TestResubmitSuspended_OutboundTargetNeverUsesEmbeddedProcessor(t *testing.T) {
	local := &fakeEnvelopeProcessor{receipt: &operatorv1.ActionReceipt{}}
	g := newTestGatewayService(t, withEnvProc(local))
	wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
		Id: "tx-remote", TransactionHash: "tx-remote", OperatorId: "remote-operator", OperatorSessionId: "remote-session",
	})
	require.NoError(t, err)
	receipt, err := g.resubmitSuspended(t.Context(), &models.SuspendedTransaction{TransactionHash: "tx-remote", Envelope: wire}, &commonv1.L3Metadata{Denied: true})
	require.ErrorIs(t, err, constants.ErrGatewayNotReady)
	require.Nil(t, receipt)
	require.Empty(t, local.gotPayload, "an outbound target must never execute on the Gateway")
}

func TestSuspendedDecision_RoutesToEnvelopeExecutionTarget(t *testing.T) {
	for _, embedded := range []bool{true, false} {
		for _, deny := range []bool{true, false} {
			name := "outbound approval"
			target := "remote-operator"
			if embedded {
				name, target = "embedded approval", string(constants.DocIDEmbeddedOperator)
			}
			if deny {
				name += " denied"
			}
			t.Run(name, func(t *testing.T) {
				local, outbound := &fakeEnvelopeProcessor{}, &fakeEnvelopeProcessor{}
				selected, unused := outbound, local
				if embedded {
					selected, unused = local, outbound
				}
				selected.receipt = &operatorv1.ActionReceipt{TransactionId: "tx-target", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
				if deny {
					selected.receipt, selected.err = l3DeniedRejection()
				}
				store := &fakeSuspendedStore{}
				g := newTestGatewayService(t, withEnvProc(local), withSuspendedStore(store))
				g.outboundEnvProc = outbound
				g.outboundProofPreparer = testOutboundProofPreparer{}
				env := &commonv1.GovernanceEnvelope{
					Id: "tx-target", TransactionHash: "tx-target", OperatorId: target, OperatorSessionId: "target-session",
					RequestorUserId: "user-1", StateMerkleRoot: "admission-root", Nonce: "original-nonce",
					Governance: &commonv1.GovernanceMetadata{L2: &commonv1.L2Metadata{ConsensusSetId: "original-consensus-set"}},
				}
				_, err := g.SuspendGovernanceEnvelope(t.Context(), env)
				require.NoError(t, err)
				proof := &commonv1.L3Proof{CredentialId: "credential-1"}
				var receipt *operatorv1.ActionReceipt
				if deny {
					receipt, err = g.DenySuspendedTransaction(t.Context(), env.Id, "user-1")
				} else {
					receipt, err = g.ResumeWithL3Proof(t.Context(), env.Id, "user-1", proof)
				}
				require.NoError(t, err)
				require.Same(t, selected.receipt, receipt)
				require.Empty(t, unused.gotPayload)
				require.Empty(t, store.txs, "a completed decision must retire its suspension")
				resumed := &commonv1.GovernanceEnvelope{}
				require.NoError(t, protojson.Unmarshal(selected.gotPayload, resumed))
				if deny {
					require.True(t, resumed.GetGovernance().GetL3().GetDenied())
					require.Nil(t, resumed.GetGovernance().GetL3().GetProof())
				} else {
					require.True(t, proto.Equal(proof, resumed.GetGovernance().GetL3().GetProof()))
				}
				resumed.Governance.L3 = nil
				require.True(t, proto.Equal(env, resumed), "only L3 metadata may change during resume")
			})
		}
	}
}

func TestResubmitSuspended_MissingTargetFailsClosed(t *testing.T) {
	local, outbound := &fakeEnvelopeProcessor{}, &fakeEnvelopeProcessor{}
	g := newTestGatewayService(t, withEnvProc(local))
	g.outboundEnvProc = outbound
	wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: "tx-target"})
	require.NoError(t, err)
	_, err = g.resubmitSuspended(t.Context(), &models.SuspendedTransaction{TransactionHash: "tx-target", Envelope: wire}, &commonv1.L3Metadata{Denied: true})
	require.ErrorIs(t, err, constants.ErrTxTargetOperatorMismatch)
	require.Empty(t, local.gotPayload)
	require.Empty(t, outbound.gotPayload)
}
