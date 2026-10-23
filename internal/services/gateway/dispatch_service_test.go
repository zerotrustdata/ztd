// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// --- stubs for Tier 1 unit tests ---

// stubStateRootProvider implements governance.StateRootProvider for unit tests.
type stubStateRootProvider struct {
	root string
	err  error
}

func (s *stubStateRootProvider) GetCurrentStateRoot(context.Context) (string, error) {
	return s.root, s.err
}

// stubOperatorSessionValidator implements operatorSessionValidator for unit tests.
type stubOperatorSessionValidator struct {
	op  *operatorv1.OperatorDocument
	err error
}

func (s *stubOperatorSessionValidator) ValidateOperatorSession(_ context.Context, _ string) (*operatorv1.OperatorDocument, error) {
	return s.op, s.err
}

// --- models.OperatorCommandRequest.Validate tests ---

func TestOperatorCommandRequest_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     models.OperatorCommandRequest
		wantErr error
	}{
		{
			name: "valid request",
			req: models.OperatorCommandRequest{
				TargetOperatorSessionID: "session-123",
				EventType:               string(constants.Event.Operator.FsRead.Requested),
				Payload:                 []byte("payload"),
			},
			wantErr: nil,
		},
		{
			name: "missing operator session id",
			req: models.OperatorCommandRequest{
				EventType: string(constants.Event.Operator.FsRead.Requested),
				Payload:   []byte("payload"),
			},
			wantErr: constants.ErrGatewayOperatorSessionIDRequired,
		},
		{
			name: "missing action type",
			req: models.OperatorCommandRequest{
				TargetOperatorSessionID: "session-123",
				Payload:                 []byte("payload"),
			},
			wantErr: constants.ErrTxUnknownEventType,
		},
		{
			name: "missing payload",
			req: models.OperatorCommandRequest{
				TargetOperatorSessionID: "session-123",
				EventType:               string(constants.Event.Operator.FsRead.Requested),
			},
			wantErr: constants.ErrTxPayloadMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.req.Validate()
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr, "expected %v, got %v", tt.wantErr, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- DispatchResult.GovernedDispatchResult tests ---

func TestDispatchResult_GovernedDispatchResult(t *testing.T) {
	t.Parallel()
	t.Run("carries the result envelope and the final receipt", func(t *testing.T) {
		t.Parallel()
		env := &commonv1.GovernanceEnvelope{
			EventType:  string(constants.Event.Operator.FsRead.Completed),
			ActionType: string(constants.ActionTypeFsRead),
			Payload:    []byte("result-payload-bytes"),
		}
		receipt := &operatorv1.ActionReceipt{TransactionId: "tx-abc-123", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
		result := &DispatchResult{
			TransactionID:  "tx-abc-123",
			ResultEnvelope: env,
			Receipt:        receipt,
		}
		resp := result.GovernedDispatchResult()
		assert.Equal(t, "tx-abc-123", resp.GetTransactionId())
		assert.Equal(t, string(constants.Event.Operator.FsRead.Completed), resp.GetEventType())
		assert.Equal(t, string(constants.ActionTypeFsRead), resp.GetActionType())
		assert.Equal(t, []byte("result-payload-bytes"), resp.GetResultPayload())
		assert.Same(t, receipt, resp.GetReceipt())
		assert.Nil(t, resp.GetInferenceResult())
	})

	t.Run("nil result envelope", func(t *testing.T) {
		t.Parallel()
		result := &DispatchResult{
			TransactionID: "tx-abc-123",
		}
		resp := result.GovernedDispatchResult()
		assert.Equal(t, "tx-abc-123", resp.GetTransactionId())
		assert.Empty(t, resp.GetEventType())
		assert.Empty(t, resp.GetActionType())
		assert.Empty(t, resp.GetResultPayload())
		assert.Nil(t, resp.GetReceipt())
	})
}

// dispatchTestCompletedOutcome returns the terminal completed outcome for a
// governed request event used by dispatch unit-test operator stubs.
func dispatchTestCompletedOutcome(requestEvent constants.EventType) constants.EventType {
	switch requestEvent {
	case constants.Event.Operator.FsRead.Requested:
		return constants.Event.Operator.FsRead.Completed
	case constants.Event.Operator.Command.Requested:
		return constants.Event.Operator.Command.Completed
	case constants.Event.Operator.Inference.Requested:
		return constants.Event.Operator.Inference.Completed
	default:
		return ""
	}
}

func dispatchTestResultEnvelope(cmdEnv *commonv1.GovernanceEnvelope, payload []byte) *commonv1.GovernanceEnvelope {
	return &commonv1.GovernanceEnvelope{
		Id:         cmdEnv.Id,
		EventType:  string(dispatchTestCompletedOutcome(constants.EventType(cmdEnv.GetEventType()))),
		ActionType: cmdEnv.ActionType,
		Payload:    payload,
		Timestamp:  timestamppb.Now(),
	}
}

// fsReadPayloadBytes builds a valid proto-marshaled FsReadRequested payload
// for dispatch tests that need a typed payload the builder can decode.
func fsReadPayloadBytes(t *testing.T) []byte {
	t.Helper()
	b, err := proto.Marshal(&operatorv1.FsReadRequested{Path: "/etc/hostname", ExecutionId: "exec-1"})
	require.NoError(t, err)
	return b
}

// --- DispatchService.Dispatch tests ---
//
// These tests use a real GatewayWebSocketHandler (in-process broker, no DB)
// with stub StateRootProvider and operatorSessionValidator. The "operator" is
// simulated by registering an in-process handler on the cmd channel that
// publishes a result envelope on the results channel.

// dispatchTestActuator returns the Actuator key the simulated outbound Operator
// signs its receipts with. newTestDispatchService binds the public half to the
// Operator's signer store, as a provisioned Operator's bound key is.
func dispatchTestActuator() (ed25519.PublicKey, ed25519.PrivateKey, string) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	pub := priv.Public().(ed25519.PublicKey)
	return pub, priv, hex.EncodeToString(pub)
}

// publishDispatchTestReceipt publishes the simulated Operator's signed final
// receipt for cmdEnv on its receipts channel, as PublishActionReceipt does.
func publishDispatchTestReceipt(t *testing.T, broker *GatewayWebSocketHandler, op *operatorv1.OperatorDocument, cmdEnv *commonv1.GovernanceEnvelope, priv ed25519.PrivateKey, keyID string) {
	t.Helper()
	receipt := &operatorv1.ActionReceipt{
		TransactionId:    cmdEnv.GetId(),
		TransactionHash:  cmdEnv.GetTransactionHash(),
		Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:    "ok",
		StateRootBefore:  "root-abc",
		StateRootAfter:   "root-abc",
		ExecutedAtUnixMs: 1700000000000,
	}
	signTestReceipt(t, receipt, priv, keyID)
	payload, err := proto.Marshal(receipt)
	require.NoError(t, err)
	wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
		EventType:         string(constants.Event.Operator.Receipt.Recorded),
		OperatorId:        op.Id,
		OperatorSessionId: op.OperatorSessionId,
		Payload:           payload,
	})
	require.NoError(t, err)
	broker.Publish(pubsub.ReceiptsChannel(op.Id, op.OperatorSessionId), wire)
}

func newTestDispatchService(t *testing.T, _ string, op *operatorv1.OperatorDocument) (*DispatchService, *GatewayWebSocketHandler) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	broker := NewGatewayWebSocketHandler(logger)
	pub, _, keyID := dispatchTestActuator()
	svc := NewDispatchService(
		logger,
		broker,
		&stubStateRootProvider{root: "root-abc"},
		&stubOperatorSessionValidator{op: op},
		"doctrine",
		governance.NewL1Doctrine(),
		nil, // no L2 deliberator under doctrine posture
		&stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}},
		nil, // no embedded processor in remote dispatch tests
		nil,
	)
	return svc, broker
}

func TestDispatchService_Dispatch_Success(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{
		Id:                "op-001",
		OperatorSessionId: "sess-001",
	}
	svc, broker := newTestDispatchService(t, "root-abc", op)

	// Simulate the operator: register a handler on the cmd channel that
	// unmarshals the command, builds a result envelope with the same Id,
	// and publishes it on the results channel.
	cmdChannel := pubsub.CmdChannel(op.Id, op.OperatorSessionId)
	resultsChannel := pubsub.ResultsChannel(op.Id, op.OperatorSessionId)

	unregisterOperator := broker.RegisterHandler(cmdChannel, func(channel string, data []byte) {
		cmdEnv := &commonv1.GovernanceEnvelope{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
			t.Errorf("operator: unmarshal command: %v", err)
			return
		}
		// Verify the command envelope is well-formed.
		assert.Equal(t, "root-abc", cmdEnv.StateMerkleRoot, "envelope must carry gateway state root")
		assert.Equal(t, op.Id, cmdEnv.OperatorId, "envelope must target the correct operator")
		assert.Equal(t, op.OperatorSessionId, cmdEnv.OperatorSessionId, "envelope must target the correct session")
		assert.NotEmpty(t, cmdEnv.Nonce, "envelope must have a nonce")
		assert.NotEmpty(t, cmdEnv.Id, "envelope must have an Id (transaction hash)")
		assert.Equal(t, cmdEnv.Id, cmdEnv.TransactionHash, "Id must equal TransactionHash")

		// Build and publish the result envelope.
		resultEnv := dispatchTestResultEnvelope(cmdEnv, nil)
		resultWire, err := protojson.Marshal(resultEnv)
		require.NoError(t, err)
		broker.Publish(resultsChannel, resultWire)
		_, priv, keyID := dispatchTestActuator()
		publishDispatchTestReceipt(t, broker, op, cmdEnv, priv, keyID)
	})
	defer unregisterOperator()

	result, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: "sess-001",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 fsReadPayloadBytes(t),
		TargetResource:          "/etc/hostname",
		RequestorUserID:         "user-001",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.TransactionID)
	require.NotNil(t, result.ResultEnvelope)
	assert.Equal(t, result.TransactionID, result.ResultEnvelope.Id)
	require.NotNil(t, result.Receipt, "a successful dispatch carries the verified final receipt")
	assert.Equal(t, result.TransactionID, result.Receipt.GetTransactionId())
}

func TestDispatchService_Dispatch_InvalidOperatorSession(t *testing.T) {
	t.Parallel()

	svc, _ := newTestDispatchService(t, "root-abc", nil)

	// Override the validator to return an error.
	svc.auth = &stubOperatorSessionValidator{err: fmt.Errorf("session not found")}

	_, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: "invalid-session",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 []byte("payload"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatch: validate operator session")
}

func TestDispatchErrorCode_RendersRefusalNotChain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		err     error
		status  int
		code    operatorv1.IngressRefusalCode
		message string
	}{
		{
			name:    "unresolved target session",
			err:     fmt.Errorf("dispatch: validate operator session: %w", &AuthError{Message: constants.ErrGatewayOperatorSessionInvalid.Error(), Status: http.StatusUnauthorized}),
			status:  http.StatusNotFound,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND,
			message: constants.ErrGatewayOperatorSessionInvalid.Error(),
		},
		{
			name:    "witness capability",
			err:     fmt.Errorf("dispatch: %w: rm -rf /srv", constants.ErrWitnessCommandNotCapable),
			status:  http.StatusUnprocessableEntity,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_WITNESS_NOT_CAPABLE,
			message: constants.ErrWitnessCommandNotCapable.Error(),
		},
		{
			name:    "governance rejected after envelope",
			err:     fmt.Errorf("dispatch: %w (transaction tx-1): cat /etc/shadow", constants.ErrDispatchGovernanceRejected),
			status:  http.StatusForbidden,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED,
			message: constants.ErrDispatchGovernanceRejected.Error(),
		},
		{
			name:    "result timeout after envelope",
			err:     fmt.Errorf("dispatch: %w after 30s (transaction tx-1)", constants.ErrDispatchResultTimeout),
			status:  http.StatusGatewayTimeout,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED,
			message: constants.ErrDispatchResultTimeout.Error(),
		},
		{
			name:    "no delivery after envelope",
			err:     fmt.Errorf("dispatch: %w", constants.ErrDispatchNoDelivery),
			status:  http.StatusServiceUnavailable,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED,
			message: constants.ErrDispatchNoDelivery.Error(),
		},
		{
			name:    "build-time doctrine",
			err:     fmt.Errorf("dispatch: %w: matched rm -rf /", constants.ErrTxL1ValidationFailed),
			status:  http.StatusForbidden,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE,
			message: constants.ErrTxL1ValidationFailed.Error(),
		},
		{
			name:    "state root unavailable",
			err:     fmt.Errorf("dispatch: state root: %w: %w", constants.ErrIngressStateRootUnavailable, errors.New("sqlite: disk I/O error at /var/lib/ztd")),
			status:  http.StatusServiceUnavailable,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_STATE_ROOT_UNAVAILABLE,
			message: constants.ErrIngressStateRootUnavailable.Error(),
		},
		{
			name:    "l2 vote quorum not met",
			err:     fmt.Errorf("dispatch: l2 deliberation: %w: %w", constants.ErrIngressL2DeliberationFailed, constants.ErrTxL2QuorumNotMet),
			status:  http.StatusForbidden,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L2_DELIBERATION_FAILED,
			message: constants.ErrIngressL2DeliberationFailed.Error(),
		},
		{
			name:    "l2 signer store not configured is a server condition",
			err:     fmt.Errorf("dispatch: l2 deliberation: %w: %w", constants.ErrIngressL2DeliberationFailed, constants.ErrTxL2SignerStoreNotConfigured),
			status:  http.StatusServiceUnavailable,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L2_DELIBERATION_FAILED,
			message: constants.ErrIngressL2DeliberationFailed.Error(),
		},
		{
			name:    "l2 deliberator unavailable",
			err:     fmt.Errorf("dispatch: l2 deliberation: %w: %w", constants.ErrIngressL2DeliberationFailed, errors.New("consensus unreachable at 10.0.0.5")),
			status:  http.StatusServiceUnavailable,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L2_DELIBERATION_FAILED,
			message: constants.ErrIngressL2DeliberationFailed.Error(),
		},
		{
			name:    "vault locked without a receipt is gateway unavailable",
			err:     fmt.Errorf("dispatch: %w", constants.ErrWardenVaultLocked),
			status:  http.StatusServiceUnavailable,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE,
			message: constants.ErrWardenVaultLocked.Error(),
		},
		{
			name:    "unclassified fault",
			err:     fmt.Errorf("dispatch: get state root: %w", errors.New("sqlite: disk I/O error at /var/lib/ztd")),
			status:  http.StatusInternalServerError,
			code:    operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED,
			message: constants.ErrInternal.Error(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status, refusal := dispatchErrorCode(tc.err)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.code, refusal.GetCode())
			assert.Equal(t, tc.message, refusal.GetError())
		})
	}
}

func TestDispatchService_Dispatch_StateRootError(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	broker := NewGatewayWebSocketHandler(logger)
	svc := NewDispatchService(
		logger,
		broker,
		&stubStateRootProvider{err: fmt.Errorf("state root unavailable")},
		&stubOperatorSessionValidator{op: op},
		"doctrine",
		governance.NewL1Doctrine(),
		nil,
		nil,
		nil,
		nil,
	)

	_, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: "sess-001",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 []byte("payload"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatch: state root")
	assert.ErrorIs(t, err, constants.ErrIngressStateRootUnavailable)
}

func TestDispatchService_Dispatch_ZeroDeliveryFailsClosed(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, _ := newTestDispatchService(t, "root-abc", op)

	// No operator handler registered — the publish delivers to zero
	// subscribers, which is a terminal transport failure.
	_, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: "sess-001",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 fsReadPayloadBytes(t),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrDispatchNoDelivery)
}

func TestDispatchService_Dispatch_TimeoutNoResult(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "root-abc", op)

	// Operator handler is registered (so delivery succeeds) but never
	// publishes a result.
	unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) {})
	defer unreg()

	// Use a context with a short timeout so the test doesn't wait 30s.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := svc.Dispatch(ctx, DispatchRequest{
		TargetOperatorSessionID: "sess-001",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 fsReadPayloadBytes(t),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDispatchService_Dispatch_RequestTimeoutOverridesDefault(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "root-abc", op)

	unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) {})
	defer unreg()

	start := time.Now()
	_, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: "sess-001",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 fsReadPayloadBytes(t),
		Timeout:                 100 * time.Millisecond,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrDispatchResultTimeout)
	assert.Less(t, time.Since(start), 5*time.Second, "request timeout must override the default dispatch timeout")
}

func TestDispatchService_Dispatch_CallerCancelReturnsCtxErr(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "root-abc", op)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The caller cancels once the operator has received the command, so the
	// dispatch is provably in flight, awaiting a result that never comes.
	unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) { cancel() })
	defer unreg()

	_, err := svc.Dispatch(ctx, DispatchRequest{
		TargetOperatorSessionID: "sess-001",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 fsReadPayloadBytes(t),
		Timeout:                 30 * time.Second,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrInferenceCanceled)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, constants.ErrDispatchResultTimeout,
		"caller cancellation must not be reported as a dispatch timeout")
}

func TestDispatchService_Dispatch_StreamingProgressOverflowFailsClosed(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "root-abc", op)

	unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, data []byte) {
		cmdEnv := &commonv1.GovernanceEnvelope{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
			return
		}
		resultsChannel := pubsub.ResultsChannel(op.Id, op.OperatorSessionId)
		for i := 0; i < InferenceProgressResultBuffer+1; i++ {
			progress, err := proto.Marshal(&operatorv1.InferenceProgressEvent{
				ProviderAttemptId: "attempt-1",
				Sequence:          uint32(i + 1),
				Parts: []*operatorv1.InferenceResponsePart{
					{Part: &operatorv1.InferenceResponsePart_Text{Text: "x"}},
				},
			})
			if err != nil {
				return
			}
			wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
				Id:         cmdEnv.Id,
				EventType:  string(constants.Event.Operator.Inference.ProgressUpdated),
				ActionType: cmdEnv.ActionType,
				Payload:    progress,
			})
			if err != nil {
				return
			}
			broker.Publish(resultsChannel, wire)
		}
	})
	defer unreg()

	_, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: op.OperatorSessionId,
		EventType:               string(constants.Event.Operator.Inference.Requested),
		Payload:                 inferencePayload(t),
		Timeout:                 2 * time.Second,
		OnInferenceProgress:     func(*operatorv1.InferenceProgressEvent) error { return nil },
	})
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrInferenceProgressBackpressure)
	assert.NotErrorIs(t, err, constants.ErrDispatchResultTimeout)
}

// resultHandlerCount returns the number of registered in-process handlers on
// the operator's results channel, for leak assertions.
func resultHandlerCount(b *GatewayWebSocketHandler, op *operatorv1.OperatorDocument) int {
	b.handlersMu.RLock()
	defer b.handlersMu.RUnlock()
	return len(b.handlers[pubsub.ResultsChannel(op.Id, op.OperatorSessionId)])
}

func TestDispatchService_Dispatch_ResultHandlerRemovedAfterReturn(t *testing.T) {
	t.Parallel()

	newOp := func() *operatorv1.OperatorDocument {
		return &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	}

	publishResult := func(t *testing.T, broker *GatewayWebSocketHandler, op *operatorv1.OperatorDocument) func(string, []byte) {
		t.Helper()
		return func(_ string, data []byte) {
			cmdEnv := &commonv1.GovernanceEnvelope{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
				return
			}
			resultEnv := dispatchTestResultEnvelope(cmdEnv, nil)
			wire, err := protojson.Marshal(resultEnv)
			if err != nil {
				return
			}
			broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), wire)
			_, priv, keyID := dispatchTestActuator()
			publishDispatchTestReceipt(t, broker, op, cmdEnv, priv, keyID)
		}
	}

	t.Run("after success", func(t *testing.T) {
		t.Parallel()

		op := newOp()
		svc, broker := newTestDispatchService(t, "root-abc", op)
		unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), publishResult(t, broker, op))
		defer unreg()

		_, err := svc.Dispatch(context.Background(), DispatchRequest{
			TargetOperatorSessionID: op.OperatorSessionId,
			EventType:               string(constants.Event.Operator.FsRead.Requested),
			Payload:                 fsReadPayloadBytes(t),
		})
		require.NoError(t, err)
		assert.Equal(t, 0, resultHandlerCount(broker, op), "result handler must be unregistered after success")
	})

	t.Run("after timeout", func(t *testing.T) {
		t.Parallel()

		op := newOp()
		svc, broker := newTestDispatchService(t, "root-abc", op)
		unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) {})
		defer unreg()

		_, err := svc.Dispatch(context.Background(), DispatchRequest{
			TargetOperatorSessionID: op.OperatorSessionId,
			EventType:               string(constants.Event.Operator.FsRead.Requested),
			Payload:                 fsReadPayloadBytes(t),
			Timeout:                 50 * time.Millisecond,
		})
		require.Error(t, err)
		assert.Equal(t, 0, resultHandlerCount(broker, op), "result handler must be unregistered after timeout")
	})

	t.Run("after caller cancellation", func(t *testing.T) {
		t.Parallel()

		op := newOp()
		svc, broker := newTestDispatchService(t, "root-abc", op)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) { cancel() })
		defer unreg()

		_, err := svc.Dispatch(ctx, DispatchRequest{
			TargetOperatorSessionID: op.OperatorSessionId,
			EventType:               string(constants.Event.Operator.FsRead.Requested),
			Payload:                 fsReadPayloadBytes(t),
		})
		require.Error(t, err)
		assert.Equal(t, 0, resultHandlerCount(broker, op), "result handler must be unregistered after cancellation")
	})

	t.Run("after zero delivery", func(t *testing.T) {
		t.Parallel()

		op := newOp()
		svc, broker := newTestDispatchService(t, "root-abc", op)

		_, err := svc.Dispatch(context.Background(), DispatchRequest{
			TargetOperatorSessionID: op.OperatorSessionId,
			EventType:               string(constants.Event.Operator.FsRead.Requested),
			Payload:                 fsReadPayloadBytes(t),
		})
		require.Error(t, err)
		assert.Equal(t, 0, resultHandlerCount(broker, op), "result handler must be unregistered after zero delivery")
	})
}

func TestDispatchService_Dispatch_LateResultDiscarded(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "root-abc", op)

	var cmdEnvID string
	unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, data []byte) {
		cmdEnv := &commonv1.GovernanceEnvelope{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err == nil {
			cmdEnvID = cmdEnv.Id
		}
	})
	defer unreg()

	_, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: op.OperatorSessionId,
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 fsReadPayloadBytes(t),
		Timeout:                 50 * time.Millisecond,
	})
	require.Error(t, err)
	require.NotEmpty(t, cmdEnvID)

	// The result arrives after the dispatch deadline: the handler is gone
	// and the publication is a no-op.
	resultEnv := &commonv1.GovernanceEnvelope{Id: cmdEnvID}
	wire, merr := protojson.Marshal(resultEnv)
	require.NoError(t, merr)
	broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), wire)
	assert.Equal(t, 0, resultHandlerCount(broker, op))
}

func TestDispatchService_Dispatch_DuplicateResultDropped(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "root-abc", op)

	unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, data []byte) {
		cmdEnv := &commonv1.GovernanceEnvelope{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
			return
		}
		// Publish two result envelopes for the same transaction in one
		// handler invocation: the first fills the buffered result channel,
		// the second must be dropped, not block or corrupt the outcome.
		for i := 0; i < 2; i++ {
			resultEnv := dispatchTestResultEnvelope(cmdEnv, []byte{byte('a' + i)})
			wire, err := protojson.Marshal(resultEnv)
			if err != nil {
				return
			}
			broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), wire)
		}
		_, priv, keyID := dispatchTestActuator()
		publishDispatchTestReceipt(t, broker, op, cmdEnv, priv, keyID)
	})
	defer unreg()

	result, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: op.OperatorSessionId,
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 fsReadPayloadBytes(t),
		Timeout:                 5 * time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, result.ResultEnvelope)
	assert.Equal(t, []byte("a"), result.ResultEnvelope.Payload, "the first correlated result wins; the duplicate is dropped")
	assert.Equal(t, 0, resultHandlerCount(broker, op))
}

func TestDispatchService_Dispatch_FinalReceiptGatesSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		receiptFirst   bool
		publishReceipt bool
		boundKey       bool
		wantErr        error
	}{
		{name: "verified receipt after the result succeeds", publishReceipt: true, boundKey: true},
		{name: "verified receipt before the result succeeds", receiptFirst: true, publishReceipt: true, boundKey: true},
		{name: "result without a receipt times out", wantErr: constants.ErrDispatchResultTimeout},
		{name: "receipt signed by an unbound key does not end the wait", publishReceipt: true, wantErr: constants.ErrDispatchResultTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
			svc, broker := newTestDispatchService(t, "root-abc", op)
			boundPub, signPriv, signKeyID := dispatchTestActuator()
			if !tt.boundKey {
				unbound := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x99}, ed25519.SeedSize))
				signPriv = unbound
				signKeyID = hex.EncodeToString(unbound.Public().(ed25519.PublicKey))
			}

			unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, data []byte) {
				cmdEnv := &commonv1.GovernanceEnvelope{}
				if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
					t.Errorf("operator: unmarshal command: %v", err)
					return
				}
				resultWire, err := protojson.Marshal(dispatchTestResultEnvelope(cmdEnv, []byte("ok")))
				require.NoError(t, err)
				publish := func() {
					if tt.publishReceipt {
						publishDispatchTestReceipt(t, broker, op, cmdEnv, signPriv, signKeyID)
					}
				}
				if tt.receiptFirst {
					publish()
				}
				broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), resultWire)
				if !tt.receiptFirst {
					publish()
				}
			})
			defer unreg()

			timeout := 5 * time.Second
			if tt.wantErr != nil {
				timeout = 200 * time.Millisecond
			}
			result, err := svc.Dispatch(context.Background(), DispatchRequest{
				TargetOperatorSessionID: "sess-001",
				EventType:               string(constants.Event.Operator.FsRead.Requested),
				Payload:                 fsReadPayloadBytes(t),
				Timeout:                 timeout,
			})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, result, "an unproven outcome must never be returned as a success")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result.Receipt)
			assert.NoError(t, governance.VerifyActionReceiptSignature(result.Receipt, boundPub))
			assert.NoError(t, governance.VerifyReceiptPersistenceAttestation(result.Receipt, boundPub))
		})
	}
}

// TestDispatchService_Dispatch_McpCallCompletesOnFinalReceipt pins the MCP_CALL
// exception: the Operator publishes no result envelope for an MCP_CALL, so the
// verified final receipt alone completes the dispatch. Every other action still
// needs its result envelope and times out on a receipt alone.
func TestDispatchService_Dispatch_McpCallCompletesOnFinalReceipt(t *testing.T) {
	t.Parallel()

	mcpPayload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolRunShellCommand, ArgumentsJson: `{"command":"pwd"}`, ExecutionId: "exec-mcp"})
	require.NoError(t, err)
	tests := []struct {
		name      string
		eventType constants.EventType
		payload   []byte
		wantErr   error
	}{
		{name: "MCP_CALL completes on its verified final receipt", eventType: constants.Event.Operator.Mcp.CallRequested, payload: mcpPayload},
		{name: "FS_READ still waits for its result envelope", eventType: constants.Event.Operator.FsRead.Requested, payload: fsReadPayloadBytes(t), wantErr: constants.ErrDispatchResultTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
			svc, broker := newTestDispatchService(t, "root-abc", op)
			boundPub, signPriv, signKeyID := dispatchTestActuator()

			unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, data []byte) {
				cmdEnv := &commonv1.GovernanceEnvelope{}
				if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
					t.Errorf("operator: unmarshal command: %v", err)
					return
				}
				publishDispatchTestReceipt(t, broker, op, cmdEnv, signPriv, signKeyID)
			})
			defer unreg()

			timeout := 5 * time.Second
			if tt.wantErr != nil {
				timeout = 200 * time.Millisecond
			}
			result, err := svc.Dispatch(context.Background(), DispatchRequest{
				TargetOperatorSessionID: "sess-001",
				EventType:               string(tt.eventType),
				Payload:                 tt.payload,
				Timeout:                 timeout,
			})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, result, "an unproven outcome must never be returned as a success")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result.Receipt)
			assert.Nil(t, result.ResultEnvelope, "MCP_CALL publishes no result envelope")
			assert.NoError(t, governance.VerifyActionReceiptSignature(result.Receipt, boundPub))
		})
	}
}

// --- DispatchController.HandleDispatch tests ---

func TestDispatchController_HandleDispatch_SuccessReturnsGovernedDispatchResult(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	svc, broker := newTestDispatchService(t, "root-abc", op)
	pub, priv, keyID := dispatchTestActuator()
	unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, data []byte) {
		cmdEnv := &commonv1.GovernanceEnvelope{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
			t.Errorf("operator: unmarshal command: %v", err)
			return
		}
		resultWire, err := protojson.Marshal(dispatchTestResultEnvelope(cmdEnv, []byte("ok")))
		require.NoError(t, err)
		broker.Publish(pubsub.ResultsChannel(op.Id, op.OperatorSessionId), resultWire)
		publishDispatchTestReceipt(t, broker, op, cmdEnv, priv, keyID)
	})
	defer unreg()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	ctrl := newDispatchController(DispatchControllerDeps{
		DispatchSvc: svc,
		Responder:   response.NewWriter(logger),
		Logger:      logger,
	})
	body, err := json.Marshal(models.OperatorCommandRequest{
		TargetOperatorSessionID: "sess-001",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 fsReadPayloadBytes(t),
	})
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	ctrl.HandleDispatch(rr, httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	got := &operatorv1.GovernedDispatchResult{}
	require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), got))
	assert.Equal(t, string(constants.Event.Operator.FsRead.Completed), got.GetEventType())
	require.NotNil(t, got.GetReceipt(), "the 200 body carries the final receipt")
	assert.NoError(t, governance.VerifyActionReceiptSignature(got.GetReceipt(), pub))
	assert.NoError(t, governance.VerifyReceiptPersistenceAttestation(got.GetReceipt(), pub))
}

// --- DispatchController.HandleDispatch tests ---

func TestDispatchController_HandleDispatch_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	resp := response.NewWriter(logger)
	ctrl := newDispatchController(DispatchControllerDeps{
		DispatchSvc: nil, // not reached
		Responder:   resp,
		Logger:      logger,
	})

	req := httptest.NewRequest(http.MethodGet, constants.APIPaths.OperatorsCommands, nil)
	rr := httptest.NewRecorder()
	ctrl.HandleDispatch(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	refusal := &operatorv1.IngressRefusal{}
	require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), refusal))
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED, refusal.GetCode())
}

func TestDispatch_InvalidJSON_ReturnsIngressRefusal(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	resp := response.NewWriter(logger)
	ctrl := newDispatchController(DispatchControllerDeps{
		DispatchSvc: nil,
		Responder:   resp,
		Logger:      logger,
	})

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader([]byte("{invalid")))
	rr := httptest.NewRecorder()
	ctrl.HandleDispatch(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	refusal := &operatorv1.IngressRefusal{}
	require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), refusal))
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST, refusal.GetCode())
	assert.Equal(t, constants.ErrInvalidJSONBody.Error(), refusal.GetError())
}

func TestDispatchController_HandleDispatch_ValidationFails(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	resp := response.NewWriter(logger)
	ctrl := newDispatchController(DispatchControllerDeps{
		DispatchSvc: nil,
		Responder:   resp,
		Logger:      logger,
	})

	// Missing target_operator_session_id and event_type.
	body := `{"payload":"dGVzdA=="}`
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader([]byte(body)))
	rr := httptest.NewRecorder()
	ctrl.HandleDispatch(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	refusal := &operatorv1.IngressRefusal{}
	require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), refusal))
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD, refusal.GetCode())
}

func TestDispatchController_HandleDispatch_WitnessCommandRejectedAsUnprocessableEntity(t *testing.T) {
	t.Parallel()

	observer := &operatorv1.OperatorDocument{
		Id:                "observer-op",
		OperatorSessionId: "observer-session",
		RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
	}
	dispatchSvc, _ := newTestDispatchService(t, "root-abc", observer)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	ctrl := newDispatchController(DispatchControllerDeps{
		DispatchSvc: dispatchSvc,
		Responder:   response.NewWriter(logger),
		Logger:      logger,
	})

	payload, err := proto.Marshal(&operatorv1.CommandRequested{
		Command:     "ollama stop qwen3:0.6b",
		ExecutionId: "exec-1",
	})
	require.NoError(t, err)
	body, err := json.Marshal(models.OperatorCommandRequest{
		TargetOperatorSessionID: observer.OperatorSessionId,
		EventType:               string(constants.Event.Operator.Command.Requested),
		Payload:                 payload,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader(body))
	rr := httptest.NewRecorder()
	ctrl.HandleDispatch(rr, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
	assert.Contains(t, rr.Body.String(), constants.ErrWitnessCommandNotCapable.Error())
}

func TestOperatorCommandResultHelpers(t *testing.T) {
	t.Parallel()
	t.Run("terminal command events", func(t *testing.T) {
		t.Parallel()
		assert.True(t, isOperatorCommandTerminalResult(&commonv1.GovernanceEnvelope{
			EventType: string(constants.Event.Operator.Command.Completed),
		}))
		assert.True(t, isOperatorCommandTerminalResult(&commonv1.GovernanceEnvelope{
			EventType: string(constants.Event.Operator.Command.CancelFailed),
		}), "a withheld cancellation is a terminal command result")
		assert.False(t, isOperatorCommandTerminalResult(&commonv1.GovernanceEnvelope{
			EventType: string(constants.Event.Operator.Command.StatusUpdated.Running),
		}))
	})

	t.Run("payload from envelope bytes", func(t *testing.T) {
		t.Parallel()
		cmdResult := &operatorv1.CommandResult{Stdout: "hello\n", ReturnCode: 0}
		wire, err := proto.Marshal(cmdResult)
		require.NoError(t, err)
		assert.Equal(t, wire, operatorCommandResultPayload(&commonv1.GovernanceEnvelope{Payload: wire}))
	})

	t.Run("ToResponse uses command result payload for completed events", func(t *testing.T) {
		t.Parallel()
		cmdResult := &operatorv1.CommandResult{Stdout: "hello\n", ReturnCode: 0}
		wire, err := proto.Marshal(cmdResult)
		require.NoError(t, err)
		resp := (&DispatchResult{
			TransactionID: "tx-1",
			ResultEnvelope: &commonv1.GovernanceEnvelope{
				EventType:  string(constants.Event.Operator.Command.Completed),
				ActionType: string(constants.ActionTypeExecuteBash),
				Payload:    wire,
			},
		}).GovernedDispatchResult()
		assert.Equal(t, wire, resp.GetResultPayload())
	})
}

func TestDispatchService_Dispatch_ExecuteBash_WaitsForTerminalResult(t *testing.T) {
	t.Parallel()

	op := &operatorv1.OperatorDocument{
		Id:                "op-001",
		OperatorSessionId: "sess-001",
	}
	svc, broker := newTestDispatchService(t, "root-abc", op)

	cmdChannel := pubsub.CmdChannel(op.Id, op.OperatorSessionId)
	resultsChannel := pubsub.ResultsChannel(op.Id, op.OperatorSessionId)

	unregisterOperator := broker.RegisterHandler(cmdChannel, func(channel string, data []byte) {
		cmdEnv := &commonv1.GovernanceEnvelope{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
			t.Errorf("operator: unmarshal command: %v", err)
			return
		}

		statusEnv := &commonv1.GovernanceEnvelope{
			Id:         cmdEnv.Id,
			EventType:  string(constants.Event.Operator.Command.StatusUpdated.Running),
			ActionType: string(constants.ActionTypeExecuteBash),
			Timestamp:  timestamppb.Now(),
		}
		statusWire, err := protojson.Marshal(statusEnv)
		require.NoError(t, err)
		broker.Publish(resultsChannel, statusWire)

		resultPayload, err := proto.Marshal(&operatorv1.CommandResult{
			Stdout:     "hello\n",
			ReturnCode: 0,
		})
		require.NoError(t, err)
		completedEnv := &commonv1.GovernanceEnvelope{
			Id:         cmdEnv.Id,
			EventType:  string(constants.Event.Operator.Command.Completed),
			ActionType: string(constants.ActionTypeExecuteBash),
			Payload:    resultPayload,
			Timestamp:  timestamppb.Now(),
		}
		completedWire, err := protojson.Marshal(completedEnv)
		require.NoError(t, err)
		broker.Publish(resultsChannel, completedWire)
		_, priv, keyID := dispatchTestActuator()
		publishDispatchTestReceipt(t, broker, op, cmdEnv, priv, keyID)
	})
	defer unregisterOperator()

	execPayload, err := proto.Marshal(&operatorv1.CommandRequested{
		Command:     "echo hello",
		ExecutionId: "exec-1",
	})
	require.NoError(t, err)

	result, err := svc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: "sess-001",
		EventType:               string(constants.Event.Operator.Command.Requested),
		Payload:                 execPayload,
		TargetResource:          "cli",
		RequestorUserID:         "user-001",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ResultEnvelope)
	assert.Equal(t, string(constants.Event.Operator.Command.Completed), result.ResultEnvelope.EventType)

	decoded := &operatorv1.CommandResult{}
	require.NoError(t, proto.Unmarshal(result.GovernedDispatchResult().GetResultPayload(), decoded))
	assert.Equal(t, "hello\n", decoded.Stdout)
}

func TestDecodeInferenceProgressEnvelope(t *testing.T) {
	t.Parallel()

	progress := &operatorv1.InferenceProgressEvent{
		ProviderAttemptId: "attempt-1",
		Sequence:          2,
	}
	payload, err := proto.Marshal(progress)
	require.NoError(t, err)

	decoded, ok := decodeInferenceProgressEnvelope(&commonv1.GovernanceEnvelope{
		EventType: string(constants.Event.Operator.Inference.ProgressUpdated),
		Payload:   payload,
	})
	require.True(t, ok)
	assert.Equal(t, "attempt-1", decoded.GetProviderAttemptId())
	assert.Equal(t, uint32(2), decoded.GetSequence())

	_, ok = decodeInferenceProgressEnvelope(&commonv1.GovernanceEnvelope{
		EventType: string(constants.Event.Operator.Inference.Completed),
		Payload:   payload,
	})
	assert.False(t, ok)
}
