// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type embeddedEnvelopeProcessorFunc func(context.Context, []byte) (*operatorv1.ActionReceipt, error)

func (f embeddedEnvelopeProcessorFunc) ProcessEnvelope(ctx context.Context, wire []byte) (*operatorv1.ActionReceipt, error) {
	return f(ctx, wire)
}

func TestEmbeddedDispatchUsesGovernanceProcessorAndCorrelatesResult(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	actuatorPub, actuatorPriv, actuatorKeyID := dispatchTestActuator()
	broker := NewGatewayWebSocketHandler(logger)
	op := &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: "embedded-session", OperatorType: string(constants.OperatorTypeEmbedded), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityObserver})}}
	called := false
	processor := embeddedEnvelopeProcessorFunc(func(ctx context.Context, wire []byte) (*operatorv1.ActionReceipt, error) {
		called = true
		env := &commonv1.GovernanceEnvelope{}
		require.NoError(t, protojson.Unmarshal(wire, env))
		require.Equal(t, op.Id, env.OperatorId)
		require.Equal(t, op.OperatorSessionId, env.OperatorSessionId)
		result, err := proto.Marshal(&operatorv1.CommandResult{Stdout: "hello"})
		require.NoError(t, err)
		completion := &commonv1.GovernanceEnvelope{Id: env.Id, EventType: string(constants.Event.Operator.Command.Completed), ActionType: string(constants.ActionTypeExecuteBash), Payload: result}
		payload, err := protojson.Marshal(completion)
		require.NoError(t, err)
		broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), payload)
		receipt := &operatorv1.ActionReceipt{TransactionId: env.Id, TransactionHash: env.TransactionHash, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
		signTestReceipt(t, receipt, actuatorPriv, actuatorKeyID)
		return receipt, nil
	})
	signers := &stubSignerStore{keys: map[string]ed25519.PublicKey{actuatorKeyID: actuatorPub}}
	svc := NewDispatchService(logger, broker, &stubStateRootProvider{root: "root"}, &stubOperatorSessionValidator{op: op}, constants.PostureDoctrine, governance.NewL1Doctrine(), nil, signers, processor, nil)
	payload, err := proto.Marshal(&operatorv1.CommandRequested{Command: "echo hello", ExecutionId: "execution"})
	require.NoError(t, err)
	result, err := svc.Dispatch(t.Context(), DispatchRequest{TargetOperatorSessionID: op.OperatorSessionId, RequestorUserID: "user", EventType: string(constants.Event.Operator.Command.Requested), Payload: payload})
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, result)
}

// DispatchObservation waits for the observer's verified final receipt: an
// observation publishes no result envelope, so the receipt alone completes it.
// A failed observation, a non-observation event, and an undeliverable command
// all fail closed.
func TestDispatchObservationReturnsEmbeddedObserverReceipt(t *testing.T) {
	t.Parallel()

	logger := testutil.NewTestLogger()
	actuatorPub, actuatorPriv, actuatorKeyID := dispatchTestActuator()
	broker := NewGatewayWebSocketHandler(logger)
	op := &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: "embedded-session", OperatorType: string(constants.OperatorTypeEmbedded)}
	signers := &stubSignerStore{keys: map[string]ed25519.PublicKey{actuatorKeyID: actuatorPub}}
	payload, err := proto.Marshal(&evalv1.ProviderBoundaryObservationCommand{
		ProviderAttemptId:      "attempt-1",
		Phase:                  evalv1.ProviderBoundaryObservationPhase_PROVIDER_BOUNDARY_OBSERVATION_PHASE_BEGIN,
		AttemptStartedAtUnixMs: 1,
	})
	require.NoError(t, err)
	request := ObservationDispatchRequest{
		TargetOperatorSessionID: op.OperatorSessionId,
		EventType:               string(constants.Event.Operator.ProviderBoundaryObservation.Requested),
		Payload:                 payload,
	}
	serviceWithStatus := func(status operatorv1.ExecutionStatus) *DispatchService {
		processor := embeddedEnvelopeProcessorFunc(func(ctx context.Context, wire []byte) (*operatorv1.ActionReceipt, error) {
			env := &commonv1.GovernanceEnvelope{}
			require.NoError(t, protojson.Unmarshal(wire, env))
			receipt := &operatorv1.ActionReceipt{TransactionId: env.Id, TransactionHash: env.TransactionHash, Status: status, ActionType: env.ActionType}
			if status == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
				receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
				receipt.DeterministicStageEvidence = []*operatorv1.DeterministicStageEvidence{{
					Kind:    operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION,
					Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED,
				}}
			}
			signTestReceipt(t, receipt, actuatorPriv, actuatorKeyID)
			return receipt, nil
		})
		return NewDispatchService(logger, broker, &stubStateRootProvider{root: "root"}, &stubOperatorSessionValidator{op: op}, constants.PostureDoctrine, governance.NewL1Doctrine(), nil, signers, processor, nil)
	}

	receipt, err := serviceWithStatus(operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED).DispatchObservation(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, string(constants.ActionTypeProviderBoundaryObservation), receipt.GetActionType())

	_, err = serviceWithStatus(operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED).DispatchObservation(t.Context(), request)
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)

	commandPayload, err := proto.Marshal(&operatorv1.CommandRequested{Command: "echo hello", ExecutionId: "execution"})
	require.NoError(t, err)
	_, err = serviceWithStatus(operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED).DispatchObservation(t.Context(), ObservationDispatchRequest{
		TargetOperatorSessionID: op.OperatorSessionId,
		EventType:               string(constants.Event.Operator.Command.Requested),
		Payload:                 commandPayload,
	})
	require.ErrorIs(t, err, constants.ErrTxEventActionMismatch)

	noProcessor := NewDispatchService(logger, broker, &stubStateRootProvider{root: "root"}, &stubOperatorSessionValidator{op: op}, constants.PostureDoctrine, governance.NewL1Doctrine(), nil, signers, nil, nil)
	_, err = noProcessor.DispatchObservation(t.Context(), request)
	require.ErrorIs(t, err, constants.ErrDispatchNoDelivery)
}

func TestEmbeddedInferenceDispatchDrainsProgressWhileProcessing(t *testing.T) {
	t.Parallel()

	errProcessing := errors.New("embedded processing failed")
	for _, tc := range []struct {
		name            string
		processingError error
		want            error
	}{
		{name: "processing failure", processingError: errProcessing, want: errProcessing},
		{name: "completion verification", want: constants.ErrInferenceCompletionNoReceipt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				logger := testutil.NewTestLogger()
				broker := NewGatewayWebSocketHandler(logger)
				op := &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: "embedded-session", OperatorType: string(constants.OperatorTypeEmbedded)}
				processor := embeddedEnvelopeProcessorFunc(func(ctx context.Context, wire []byte) (*operatorv1.ActionReceipt, error) {
					env := &commonv1.GovernanceEnvelope{}
					require.NoError(t, protojson.Unmarshal(wire, env))
					// More events than the result buffer holds, all published before
					// ProcessEnvelope returns, as an in-process inference stream does.
					for i := 0; i < InferenceProgressResultBuffer*2; i++ {
						progress, err := proto.Marshal(&operatorv1.InferenceProgressEvent{
							ProviderAttemptId: "attempt-1",
							Sequence:          uint32(i + 1),
							Parts:             []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: "x"}}},
						})
						require.NoError(t, err)
						out, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: env.Id, EventType: string(constants.Event.Operator.Inference.ProgressUpdated), ActionType: env.ActionType, Payload: progress})
						require.NoError(t, err)
						broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), out)
					}

					if tc.processingError == nil {
						// An unsigned completion must still reach receipt verification after
						// every queued progress event has been delivered in order.
						out, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: env.Id, EventType: string(constants.Event.Operator.Inference.Completed), ActionType: env.ActionType})
						require.NoError(t, err)
						broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), out)
					}
					return nil, tc.processingError
				})
				svc := NewDispatchService(logger, broker, &stubStateRootProvider{root: "root"}, &stubOperatorSessionValidator{op: op}, constants.PostureDoctrine, governance.NewL1Doctrine(), nil, nil, processor, nil)

				var seen int
				_, err := svc.Dispatch(t.Context(), DispatchRequest{
					TargetOperatorSessionID: op.OperatorSessionId,
					RequestorUserID:         "user",
					EventType:               string(constants.Event.Operator.Inference.Requested),
					Payload:                 inferencePayload(t),
					Timeout:                 5 * time.Second,
					OnInferenceProgress: func(progress *operatorv1.InferenceProgressEvent) error {
						// Give the producer time to fill the bounded queue while the
						// consumer is busy. Virtual time makes this deterministic.
						time.Sleep(time.Millisecond)
						seen++
						require.Equal(t, uint32(seen), progress.GetSequence())
						return nil
					},
				})
				require.ErrorIs(t, err, tc.want)
				require.NotErrorIs(t, err, constants.ErrInferenceProgressBackpressure)
				require.Positive(t, seen)
				if tc.processingError == nil {
					require.Equal(t, InferenceProgressResultBuffer*2, seen)
				}
			})
		})
	}
}

func TestEmbeddedInferenceDispatchCancelsAndJoinsProcessor(t *testing.T) {
	t.Parallel()

	errProgress := errors.New("progress callback failed")
	for _, tc := range []struct {
		name string
		stop func(context.CancelFunc) error
		want error
	}{
		{name: "caller cancellation", stop: func(cancel context.CancelFunc) error { cancel(); return nil }, want: constants.ErrInferenceCanceled},
		{name: "callback failure", stop: func(context.CancelFunc) error { return errProgress }, want: errProgress},
		{name: "deadline", stop: func(context.CancelFunc) error { time.Sleep(2 * time.Second); return nil }, want: constants.ErrDispatchResultTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				logger := testutil.NewTestLogger()
				broker := NewGatewayWebSocketHandler(logger)
				op := &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: "embedded-session", OperatorType: string(constants.OperatorTypeEmbedded)}
				joined := false
				processor := embeddedEnvelopeProcessorFunc(func(ctx context.Context, wire []byte) (*operatorv1.ActionReceipt, error) {
					defer func() { joined = true }()
					env := &commonv1.GovernanceEnvelope{}
					if err := protojson.Unmarshal(wire, env); err != nil {
						return nil, err
					}
					progress, err := proto.Marshal(&operatorv1.InferenceProgressEvent{ProviderAttemptId: "attempt-1", Sequence: 1})
					if err != nil {
						return nil, err
					}
					out, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: env.Id, EventType: string(constants.Event.Operator.Inference.ProgressUpdated), ActionType: env.ActionType, Payload: progress})
					if err != nil {
						return nil, err
					}
					for ctx.Err() == nil {
						broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), out)
					}
					return nil, ctx.Err()
				})
				svc := NewDispatchService(logger, broker, &stubStateRootProvider{root: "root"}, &stubOperatorSessionValidator{op: op}, constants.PostureDoctrine, governance.NewL1Doctrine(), nil, nil, processor, nil)
				_, err := svc.Dispatch(ctx, DispatchRequest{
					TargetOperatorSessionID: op.OperatorSessionId,
					RequestorUserID:         "user",
					EventType:               string(constants.Event.Operator.Inference.Requested),
					Payload:                 inferencePayload(t),
					Timeout:                 time.Second,
					OnInferenceProgress: func(*operatorv1.InferenceProgressEvent) error {
						// Wait until the producer is blocked on the full queue.
						synctest.Wait()
						return tc.stop(cancel)
					},
				})
				require.ErrorIs(t, err, tc.want)
				require.NotErrorIs(t, err, constants.ErrInferenceProgressBackpressure)
				require.True(t, joined, "dispatch must join the canceled processor before returning")
				require.Zero(t, resultHandlerCount(broker, op))
			})
		})
	}
}

func TestEmbeddedDispatchDeadlineWhileAwaitingProcessor(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		logger := testutil.NewTestLogger()
		broker := NewGatewayWebSocketHandler(logger)
		op := &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: "embedded-session", OperatorType: string(constants.OperatorTypeEmbedded)}
		joined := false
		processor := embeddedEnvelopeProcessorFunc(func(ctx context.Context, wire []byte) (*operatorv1.ActionReceipt, error) {
			defer func() { joined = true }()
			env := &commonv1.GovernanceEnvelope{}
			if err := protojson.Unmarshal(wire, env); err != nil {
				return nil, err
			}
			result, err := proto.Marshal(&operatorv1.CommandResult{Stdout: "hello"})
			if err != nil {
				return nil, err
			}
			out, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: env.Id, EventType: string(constants.Event.Operator.Command.Completed), ActionType: env.ActionType, Payload: result})
			if err != nil {
				return nil, err
			}
			broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), out)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		svc := NewDispatchService(logger, broker, &stubStateRootProvider{root: "root"}, &stubOperatorSessionValidator{op: op}, constants.PostureDoctrine, governance.NewL1Doctrine(), nil, nil, processor, nil)
		payload, err := proto.Marshal(&operatorv1.CommandRequested{Command: "echo hello", ExecutionId: "execution"})
		require.NoError(t, err)
		_, err = svc.Dispatch(t.Context(), DispatchRequest{TargetOperatorSessionID: op.OperatorSessionId, RequestorUserID: "user", EventType: string(constants.Event.Operator.Command.Requested), Payload: payload, Timeout: time.Second})
		require.ErrorIs(t, err, constants.ErrDispatchResultTimeout)
		require.True(t, joined)
		require.Zero(t, resultHandlerCount(broker, op))
	})
}

// An embedded handler that runs and fails publishes its typed failure result;
// the dispatch returns that result, as for an outbound Operator. Without an
// executed L5 stage the same error still fails the dispatch.
func TestEmbeddedDispatchReturnsHandlerFailureResult(t *testing.T) {
	t.Parallel()

	actuatorPub, actuatorPriv, actuatorKeyID := dispatchTestActuator()
	for _, tc := range []struct {
		name     string
		executed bool
	}{{name: "handler ran", executed: true}, {name: "refused before handler", executed: false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logger := testutil.NewTestLogger()
			broker := NewGatewayWebSocketHandler(logger)
			op := &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: "embedded-session", OperatorType: string(constants.OperatorTypeEmbedded)}
			processor := embeddedEnvelopeProcessorFunc(func(ctx context.Context, wire []byte) (*operatorv1.ActionReceipt, error) {
				env := &commonv1.GovernanceEnvelope{}
				require.NoError(t, protojson.Unmarshal(wire, env))
				receipt := &operatorv1.ActionReceipt{TransactionId: env.Id, TransactionHash: env.TransactionHash, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, FailureCode: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED}
				if tc.executed {
					result, err := proto.Marshal(&operatorv1.FileEditResult{ExecutionId: "execution", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, ErrorMessage: "content required"})
					require.NoError(t, err)
					out, err := protojson.Marshal(&commonv1.GovernanceEnvelope{Id: env.Id, EventType: string(constants.EventOperatorFileEditFailed), ActionType: env.ActionType, Payload: result})
					require.NoError(t, err)
					broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), out)
					receipt.DeterministicStageEvidence = []*operatorv1.DeterministicStageEvidence{{
						Kind:    operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION,
						Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED,
					}}
				}
				signTestReceipt(t, receipt, actuatorPriv, actuatorKeyID)
				return receipt, constants.ErrFileEditContentRequired
			})
			signers := &stubSignerStore{keys: map[string]ed25519.PublicKey{actuatorKeyID: actuatorPub}}
			svc := NewDispatchService(logger, broker, &stubStateRootProvider{root: "root"}, &stubOperatorSessionValidator{op: op}, constants.PostureDoctrine, governance.NewL1Doctrine(), nil, signers, processor, nil)
			payload, err := proto.Marshal(&operatorv1.FileEditRequested{FilePath: "/tmp/ztd-eval/a.txt", Operation: string(constants.FileOperationWrite), ExecutionId: "execution"})
			require.NoError(t, err)
			result, err := svc.Dispatch(t.Context(), DispatchRequest{TargetOperatorSessionID: op.OperatorSessionId, RequestorUserID: "user", EventType: string(constants.EventOperatorFileEditRequested), Payload: payload, Timeout: 5 * time.Second})
			if !tc.executed {
				require.ErrorIs(t, err, constants.ErrFileEditContentRequired)
				return
			}
			require.NoError(t, err)
			require.Equal(t, string(constants.EventOperatorFileEditFailed), result.ResultEnvelope.GetEventType())
		})
	}
}
