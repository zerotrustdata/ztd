// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func resumeTestEnvelope(t *testing.T, event constants.EventType, payload []byte) *commonv1.GovernanceEnvelope {
	t.Helper()
	env, err := BuildGovernanceEnvelope(baseEnvelopeParams(event, payload, constants.PostureDoctrine, testDoctrine()))
	require.NoError(t, err)
	// Posture and proofs are outside the transaction hash. A suspended
	// ratify envelope already has this identity before human approval.
	env.Posture = string(constants.PostureRatify)
	env.Governance.L2 = &commonv1.L2Metadata{ConsensusSetId: "original-consensus-set"}
	env.Governance.L3 = &commonv1.L3Metadata{Proof: &commonv1.L3Proof{CredentialId: "registered-credential"}}
	return env
}

func TestDispatchService_ProcessEnvelope_PreservesAdmittedEnvelope(t *testing.T) {
	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "new-root-must-not-be-used", op)
	svc.posture = constants.PostureRatify
	svc.stateRootProvider = &stubStateRootProvider{err: constants.ErrIngressStateRootUnavailable}
	deliberator := &stubL2Deliberator{}
	svc.l2Deliberator = deliberator
	svc.embeddedProcessor = embeddedEnvelopeProcessorFunc(func(context.Context, []byte) (*operatorv1.EventReceipt, error) {
		t.Fatal("outbound resume reached the embedded processor")
		return nil, nil
	})
	env := resumeTestEnvelope(t, constants.Event.Operator.FsRead.Requested, fsReadPayload(t))
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)
	_, key, keyID := dispatchTestActuator()
	var delivered int
	unregister := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, data []byte) {
		delivered++
		require.Equal(t, wire, data, "resume must deliver the admitted bytes unchanged")
		resultWire, err := protojson.Marshal(dispatchTestResultEnvelope(env, []byte("observed")))
		require.NoError(t, err)
		broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), resultWire)
		publishDispatchTestReceipt(t, broker, op, env, key, keyID)
	})
	defer unregister()
	receipt, err := svc.ProcessEnvelope(t.Context(), wire)
	require.NoError(t, err)
	require.Equal(t, 1, delivered)
	require.Equal(t, env.Id, receipt.GetTransactionId())
	require.False(t, deliberator.called, "resume must retain the original L2 proofs")
	pub, _, _ := dispatchTestActuator()
	require.NoError(t, governance.VerifyEventReceiptSignature(receipt, pub))
	require.NoError(t, governance.VerifyReceiptPersistenceAttestation(receipt, pub))
}

func TestDispatchService_ProcessEnvelope_ReturnsSignedDenialWithoutResult(t *testing.T) {
	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "root-abc", op)
	svc.posture = constants.PostureRatify
	env := resumeTestEnvelope(t, constants.Event.Operator.FsRead.Requested, fsReadPayload(t))
	env.Governance.L3 = &commonv1.L3Metadata{Denied: true}
	pub, key, keyID := dispatchTestActuator()
	rejection := &operatorv1.EventReceipt{
		TransactionId: env.Id, TransactionHash: env.TransactionHash,
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED,
	}
	signTestReceipt(t, rejection, key, keyID)
	unregister := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) {
		payload, err := proto.Marshal(rejection)
		require.NoError(t, err)
		wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
			EventType: string(constants.Event.Operator.Receipt.Recorded), OperatorId: op.Id, OperatorSessionId: op.OperatorSessionId, Payload: payload,
		})
		require.NoError(t, err)
		broker.Publish(pubsub.ReceiptsChannel(op.Id, op.OperatorSessionId), wire)
	})
	defer unregister()
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)
	receipt, err := svc.ProcessEnvelope(t.Context(), wire)
	require.ErrorIs(t, err, constants.ErrDispatchGovernanceRejected)
	require.True(t, proto.Equal(rejection, receipt))
	require.True(t, proto.Equal(receipt, governance.FailedReceipt(err)))
	require.NoError(t, governance.VerifyEventReceiptSignature(receipt, pub))
	require.NoError(t, governance.VerifyReceiptPersistenceAttestation(receipt, pub))
}

func TestDispatchService_ProcessEnvelope_RejectsChangedIdentityBeforeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*commonv1.GovernanceEnvelope)
		want   error
	}{
		{name: "missing operator", mutate: func(env *commonv1.GovernanceEnvelope) { env.OperatorId = "" }, want: constants.ErrTxTargetOperatorMismatch},
		{name: "different operator", mutate: func(env *commonv1.GovernanceEnvelope) { env.OperatorId = "other-operator" }, want: constants.ErrTxTargetOperatorMismatch},
		{name: "missing session", mutate: func(env *commonv1.GovernanceEnvelope) { env.OperatorSessionId = "" }, want: constants.ErrTxTargetSessionMismatch},
		{name: "rotated session", mutate: func(env *commonv1.GovernanceEnvelope) { env.OperatorSessionId = "old-session" }, want: constants.ErrTxTargetSessionMismatch},
		{name: "different posture", mutate: func(env *commonv1.GovernanceEnvelope) { env.Posture = string(constants.PostureDoctrine) }, want: constants.ErrGatewayPostureMismatch},
		{name: "action event mismatch", mutate: func(env *commonv1.GovernanceEnvelope) { env.ActionType = string(constants.ActionTypeExecuteBash) }, want: constants.ErrTxEventActionMismatch},
		{name: "changed transaction ID", mutate: func(env *commonv1.GovernanceEnvelope) { env.Id = "other-id" }, want: constants.ErrTxTransactionIDMismatch},
		{name: "changed transaction hash", mutate: func(env *commonv1.GovernanceEnvelope) { env.TransactionHash = "other-hash" }, want: constants.ErrTxTransactionHashMismatch},
		{name: "changed state snapshot", mutate: func(env *commonv1.GovernanceEnvelope) { env.StateMerkleRoot = "new-root" }, want: constants.ErrTxTransactionIDMismatch},
		{name: "changed payload", mutate: func(env *commonv1.GovernanceEnvelope) { env.Payload = []byte("other payload") }, want: constants.ErrTxTransactionIDMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
			svc, broker := newTestDispatchService(t, "root-abc", op)
			svc.posture = constants.PostureRatify
			env := resumeTestEnvelope(t, constants.Event.Operator.FsRead.Requested, fsReadPayload(t))
			tc.mutate(env)
			unregister := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) { t.Error("invalid envelope was published") })
			defer unregister()
			wire, err := protojson.Marshal(env)
			require.NoError(t, err)
			receipt, err := svc.ProcessEnvelope(t.Context(), wire)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, receipt)
		})
	}
}

func TestDispatchService_ProcessEnvelope_UnsignedResultCannotResumeApproval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
		svc, broker := newTestDispatchService(t, "root-abc", op)
		svc.posture = constants.PostureRatify
		env := resumeTestEnvelope(t, constants.Event.Operator.FsRead.Requested, fsReadPayload(t))
		unregister := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) {
			wire, err := protojson.Marshal(dispatchTestResultEnvelope(env, []byte("unproven result")))
			require.NoError(t, err)
			broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), wire)
		})
		defer unregister()
		wire, err := protojson.Marshal(env)
		require.NoError(t, err)
		receipt, err := svc.ProcessEnvelope(t.Context(), wire)
		require.ErrorIs(t, err, constants.ErrDispatchResultTimeout)
		require.Nil(t, receipt)
	})
}

func TestDispatchService_ProcessEnvelope_InferenceCompletionIsVerified(t *testing.T) {
	for _, outcome := range []string{"completed", "denied", "changed result"} {
		t.Run(outcome, func(t *testing.T) {
			op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
			svc, broker := newTestDispatchService(t, "root-abc", op)
			svc.posture = constants.PostureRatify
			request := &operatorv1.InferenceRequested{Model: "gemma3:4b", ProviderAttemptId: "provider-attempt-1"}
			payload, err := proto.Marshal(request)
			require.NoError(t, err)
			env := resumeTestEnvelope(t, constants.Event.Operator.Inference.Requested, payload)
			result := inferenceTestResult(t)
			_, key, keyID := dispatchTestActuator()
			receipt := &operatorv1.EventReceipt{
				TransactionId: env.Id, TransactionHash: env.TransactionHash,
				Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ResultSummary: result.ResultDigest,
			}
			if outcome == "denied" {
				env.Governance.L3 = &commonv1.L3Metadata{Denied: true}
				receipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
				receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED
				receipt.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED
				result = nil
			}
			signTestReceipt(t, receipt, key, keyID)
			if outcome == "changed result" {
				result.CompletionTokens++
			}
			unregister := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) {
				payload, err := proto.Marshal(&operatorv1.InferenceCompletion{Receipt: receipt, Result: result})
				require.NoError(t, err)
				wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
					Id: env.Id, ActionType: env.ActionType, EventType: string(constants.Event.Operator.Inference.Completed), Payload: payload,
				})
				require.NoError(t, err)
				broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), wire)
			})
			defer unregister()
			wire, err := protojson.Marshal(env)
			require.NoError(t, err)
			got, err := svc.ProcessEnvelope(t.Context(), wire)
			switch outcome {
			case "completed":
				require.NoError(t, err)
				require.True(t, proto.Equal(receipt, got))
			case "denied":
				require.ErrorIs(t, err, constants.ErrInferenceGovernanceRejected)
				require.True(t, proto.Equal(receipt, got))
			case "changed result":
				require.ErrorIs(t, err, constants.ErrInferenceResultDigestMismatch)
				require.Nil(t, got)
			}
		})
	}
}

func TestDispatchService_ProcessEnvelope_ReceiptMustBindBothTransactionIdentifiers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
		svc, broker := newTestDispatchService(t, "root-abc", op)
		svc.posture = constants.PostureRatify
		env := resumeTestEnvelope(t, constants.Event.Operator.FsRead.Requested, fsReadPayload(t))
		_, key, keyID := dispatchTestActuator()
		unregister := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) {
			wire, err := protojson.Marshal(dispatchTestResultEnvelope(env, []byte("observed")))
			require.NoError(t, err)
			broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), wire)
			wrongHash := proto.Clone(env).(*commonv1.GovernanceEnvelope)
			wrongHash.TransactionHash = "another-transaction-hash"
			publishDispatchTestReceipt(t, broker, op, wrongHash, key, keyID)
		})
		defer unregister()
		wire, err := protojson.Marshal(env)
		require.NoError(t, err)
		receipt, err := svc.ProcessEnvelope(t.Context(), wire)
		require.ErrorIs(t, err, constants.ErrDispatchResultTimeout)
		require.Nil(t, receipt, "a signed receipt for another hash is not approval evidence")
	})
}
