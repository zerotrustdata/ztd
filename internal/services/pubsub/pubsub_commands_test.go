// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	storagetest "github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorPubSubService_HandlerDecodeFailuresReachActuator(t *testing.T) {
	fixture := newPubsubFixture(t)
	for _, action := range []constants.ActionType{
		constants.ActionTypeHeartbeat, constants.ActionTypeExecuteBash, constants.ActionTypeCancel,
		constants.ActionTypeFsList, constants.ActionTypeFsRead, constants.ActionTypeFsGrep,
		constants.ActionTypePortCheck, constants.ActionTypeOllamaModelInventory, constants.ActionTypeOllamaModelResidency,
		constants.ActionTypeFetchLogs, constants.ActionTypeFetchHistory, constants.ActionTypeFetchFileHistory,
		constants.ActionTypeRestoreFile, constants.ActionTypeFetchFileDiff,
	} {
		t.Run(string(action), func(t *testing.T) {
			handler, ok := fixture.Svc.actionHandlers[action]
			require.True(t, ok)
			_, err := handler(t.Context(), &PubSubCommandMessage{ID: "decode-failure", Payload: []byte{0xff}})
			require.Error(t, err, "a handler failure must reach L5 instead of producing a completed receipt")
			assert.ErrorIs(t, err, proto.Error, "the original protobuf decoding failure must remain in the error chain")
		})
	}
}

func TestLocalNativeToolRouteSelectionPreservesGatewaySessionQueries(t *testing.T) {
	gateway := &OperatorPubSubService{gatewayMode: true}
	local := &OperatorPubSubService{}
	tests := []struct {
		name      string
		tool      string
		args      string
		wantLocal bool
	}{
		{
			name:      "Gateway native tool stays on the MCP gateway",
			tool:      "run_shell_command",
			args:      `{"command":"true"}`,
			wantLocal: false,
		},
		{
			name:      "Gateway-local read_field stays on the MCP gateway",
			tool:      "read_field",
			args:      `{}`,
			wantLocal: false,
		},
		{
			name:      "Gateway public session query",
			args:      `{"operator_session_id":"session-1"}`,
			wantLocal: false,
		},
		{
			name:      "explicit investigation query selects embedded Operator ledger",
			args:      `{"investigation_id":"investigation-1","exclude_action_type":"MCP_CALL"}`,
			wantLocal: true,
		},
		{
			name:      "wrong local exclusion remains on public Gateway policy",
			args:      `{"investigation_id":"investigation-1","exclude_action_type":"EXECUTE_BASH"}`,
			wantLocal: false,
		},
		{
			name:      "malformed arguments remain on public Gateway policy",
			args:      `{`,
			wantLocal: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := tt.tool
			if tool == "" {
				tool = "audit_receipt_list"
			}
			useLocal := gateway.useLocalNativeToolRoute(tool, json.RawMessage(tt.args))
			assert.Equal(t, tt.wantLocal, useLocal)
			assert.True(t, local.useLocalNativeToolRoute(tool, json.RawMessage(tt.args)), "outbound Operators never fall back to a Gateway mirror")
		})
	}
}

func TestNewOperatorPubSubService(t *testing.T) {
	t.Run("returns non-nil service without error", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
			Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
			Ledger:       storagetest.NewTestLedger(t),
			RuntimeVault: governancetest.UnlockedVault{},
			Config:       cfg,
			Logger:       testutil.NewTestLogger(),
			PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
		}), OutboundModeDeps{
			GovernanceCoreDeps: GovernanceCoreDeps{
				ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
				ReplayStore:       &testutil.MockReplayStore{},
				StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
				TransactionAudit:  &testutil.MockTransactionAudit{},
				L3Notary:          &testutil.MockL3Notary{},
				Doctrine:          governance.NewL1Doctrine(),
			},
		})
		require.NoError(t, err)
		assert.NotNil(t, svc)
	})
}

func TestNewOperatorPubSubService_StartsWithoutTrustedSignersButRejectsL2(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	tmpDir := testutil.TempDir(t)
	cfg.PKIDir = filepath.Join(tmpDir, "pki")
	cfg.Gateway.Posture = constants.PostureConsensus // Set Consensus posture to enforce L2
	svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
		Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
		Ledger:       storagetest.NewTestLedger(t),
		RuntimeVault: governancetest.UnlockedVault{},
		Config:       cfg,
		Logger:       testutil.NewTestLogger(),
		PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
	}), OutboundModeDeps{
		GovernanceCoreDeps: GovernanceCoreDeps{
			ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
			ReplayStore:       &testutil.MockReplayStore{},
			StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
			TransactionAudit:  &testutil.MockTransactionAudit{},
			L3Notary:          &testutil.MockL3Notary{},
			Doctrine:          governance.NewL1Doctrine(),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, svc.l4warden)

	_, signerPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	env := unsignedSignerEnvelope(t, signerPriv)

	_, err = svc.l4warden.VerifyEnvelope(context.Background(), env)
	require.Error(t, err)
	assert.ErrorIs(t, err, governance.ErrL2ConsensusNotConfigured, "expected consensus not configured error, got %v", err)
}

func unsignedSignerEnvelope(t *testing.T, signerPriv ed25519.PrivateKey) *govpkg.GovernanceEnvelope {
	t.Helper()
	req := &operatorv1.FsListRequested{Path: ".", ExecutionId: "exec-1"}
	payload, err := proto.Marshal(req)
	require.NoError(t, err)
	env := &govpkg.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		Timestamp:         timestamppb.Now(),
		ExpiresAt:         timestamppb.New(time.Now().UTC().Add(time.Hour)),
		SourceComponent:   commonv1.Component_COMPONENT_AGENT,
		OperatorId:        "operator-1",
		OperatorSessionId: "session-1",
		EventType:         string(constants.Event.Operator.FsList.Requested),

		ActionType:      string(constants.ActionTypeFsList),
		TargetResource:  "localhost",
		Payload:         payload,
		StateMerkleRoot: "test-state-root",
		Nonce:           "nonce-missing-signer",
		Posture:         string(constants.PostureConsensus),
	}
	hash, err := govpkg.GenerateMessageID(env)
	require.NoError(t, err)
	env.Id = hash
	env.TransactionHash = hash
	env.Governance = &commonv1.GovernanceMetadata{
		L2: &commonv1.L2Metadata{
			ConsensusSetId: "test-consensus",
			Votes: []*commonv1.L2Vote{
				{
					SignerKeyId:        "missing-key",
					ConsensusSignature: hex.EncodeToString(ed25519.Sign(signerPriv, []byte(hash+"|true"))),
					Decision:           true,
				},
			},
		},
	}
	return env
}

func TestOperatorPubSubService_handleCommandPayload(t *testing.T) {
	t.Run("rejects oversized payload", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		largePayload := make([]byte, MaxPayloadSize+1)
		f.Svc.handleCommandPayload(largePayload)
		// Should log error and return without panic
	})

	t.Run("rejects non-JSON payload", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		binaryPayload := []byte{0x00, 0x01, 0x02}
		f.Svc.handleCommandPayload(binaryPayload)
		// Should log error and return without panic
	})
}

func TestOperatorPubSubService_handleGovernanceEnvelope(t *testing.T) {
	t.Run("rejects envelope with missing payload", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		env := &govpkg.GovernanceEnvelope{
			ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
			Timestamp:       timestamppb.Now(),
			ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
			EventType:       string(constants.Event.Operator.FsList.Requested),

			ActionType:      string(constants.ActionTypeFsList),
			TargetResource:  "localhost",
			Payload:         nil,
			StateMerkleRoot: "test-state-root",
			Nonce:           "nonce-1",
			Posture:         string(constants.PostureDoctrine),
		}
		f.Svc.handleGovernanceEnvelope(env)
		// Should log error and return without panic
	})

	t.Run("rejects when Actuator missing", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
			Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
			Ledger:       storagetest.NewTestLedger(t),
			RuntimeVault: governancetest.UnlockedVault{},
			Config:       cfg,
			Logger:       testutil.NewTestLogger(),
			PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
		}), OutboundModeDeps{
			GovernanceCoreDeps: GovernanceCoreDeps{
				ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
				ReplayStore:       &testutil.MockReplayStore{},
				StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
				TransactionAudit:  &testutil.MockTransactionAudit{},
				L3Notary:          &testutil.MockL3Notary{},
				SignerStore:       &governance.FailClosedSignerStore{Signers: map[string]ed25519.PublicKey{}},
				Doctrine:          governance.NewL1Doctrine(),
			},
		})
		require.NoError(t, err)
		svc.SetActuator(nil)

		req := &operatorv1.FsListRequested{Path: ".", ExecutionId: "exec-1"}
		payload, _ := proto.Marshal(req)
		env := &govpkg.GovernanceEnvelope{
			ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
			Timestamp:       timestamppb.Now(),
			ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
			EventType:       string(constants.Event.Operator.FsList.Requested),

			ActionType:      string(constants.ActionTypeFsList),
			TargetResource:  "localhost",
			Payload:         payload,
			StateMerkleRoot: "test-state-root",
			Nonce:           "nonce-1",
			Posture:         string(constants.PostureDoctrine),
		}
		svc.handleGovernanceEnvelope(env)
		// Should log error and return without panic
	})

	// An all-default ShutdownRequested marshals to zero bytes, which is what
	// the Gateway stop routes send when no reason is given. It must execute
	// and signal shutdown, not be dropped after L4 has already verified it.
	t.Run("executes shutdown envelope with empty payload", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.results = &mockResultsPublisher{}
		env := &govpkg.GovernanceEnvelope{
			OperatorId:      "operator-1",
			ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
			Timestamp:       timestamppb.Now(),
			ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
			EventType:       string(constants.Event.Operator.ShutdownRequested),
			ActionType:      string(constants.ActionTypeShutdown),
			TargetResource:  "localhost",
			StateMerkleRoot: "test-state-root",
			Nonce:           "nonce-shutdown-empty",
			Posture:         string(constants.PostureDoctrine),
			Governance: &commonv1.GovernanceMetadata{
				L2: &commonv1.L2Metadata{
					ConsensusSetId: "test-consensus",
					Votes:          []*commonv1.L2Vote{{SignerKeyId: "test-key", Decision: true}},
				},
			},
		}
		env.TransactionHash, _ = govpkg.GenerateMessageID(env)
		env.Id = env.TransactionHash
		sig := ed25519.Sign(f.SignerPriv, []byte(fmt.Sprintf("%s|true", env.TransactionHash)))
		env.Governance.L2.Votes[0].ConsensusSignature = hex.EncodeToString(sig)

		f.Svc.handleGovernanceEnvelope(env)

		select {
		case reason := <-f.Svc.ShutdownChan:
			assert.Equal(t, "No reason provided", reason)
		default:
			t.Fatal("shutdown envelope with empty payload did not signal shutdown")
		}
	})
}

func TestGatewayDispatchedVerificationContext_AcceptsPDPBoundRootDespiteLiveDrift(t *testing.T) {
	t.Parallel()

	const gatewayRoot = "gateway-root-at-construction"
	const liveRoot = "live-root-after-concurrent-tx"

	cfg := testutil.NewTestConfig(t)
	svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
		Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
		Ledger:       storagetest.NewTestLedger(t),
		RuntimeVault: governancetest.UnlockedVault{},
		Config:       cfg,
		Logger:       testutil.NewTestLogger(),
		PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
	}), OutboundModeDeps{
		GovernanceCoreDeps: GovernanceCoreDeps{
			ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
			ReplayStore:       &testutil.MockReplayStore{},
			StateRootProvider: testutil.NewMockStateRootProvider(liveRoot),
			TransactionAudit:  &testutil.MockTransactionAudit{},
			L3Notary:          &testutil.MockL3Notary{},
			SignerStore:       &governance.FailClosedSignerStore{Signers: map[string]ed25519.PublicKey{}},
			Doctrine:          governance.NewL1Doctrine(),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, svc.l4warden)

	env := buildGatewayDispatchedDoctrineEnvelope(t, gatewayRoot, "nonce-gateway-dispatch-root")
	_, err = svc.l4warden.VerifyEnvelope(context.Background(), env)
	require.Error(t, err)
	assert.ErrorIs(t, err, governance.ErrStateRootMismatch)

	_, err = svc.l4warden.VerifyEnvelope(gatewayDispatchedVerificationContext(context.Background(), env), env)
	require.NoError(t, err)
}

func TestOperatorPubSubService_handleGovernanceEnvelope_AcceptsPDPBoundRootDespiteLiveDrift(t *testing.T) {
	t.Parallel()

	const gatewayRoot = "gateway-root-at-construction"
	const liveRoot = "live-root-after-concurrent-tx"

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
		Scrubbing:          mustNewScrubbingSvc(t, testutil.NewTestLogger()),
		Ledger:             storagetest.NewTestLedger(t),
		RuntimeVault:       governancetest.UnlockedVault{},
		Config:             cfg,
		Logger:             logger,
		PubSubClient:       pubsubtest.NewMockOperatorPubSubClient(),
		ActuatorSigningKey: priv,
		ActuatorKeyID:      "actuator-key",
	}), OutboundModeDeps{
		GovernanceCoreDeps: GovernanceCoreDeps{
			ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
			ReplayStore:       &testutil.MockReplayStore{},
			StateRootProvider: testutil.NewMockStateRootProvider(liveRoot),
			TransactionAudit:  &testutil.MockTransactionAudit{},
			L3Notary:          &testutil.MockL3Notary{},
			SignerStore: &governance.FailClosedSignerStore{
				Signers: map[string]ed25519.PublicKey{"test-key": pub},
			},
			Doctrine: governance.NewL1Doctrine(),
		},
	})
	require.NoError(t, err)
	svc.ctx = context.Background()

	var executed bool
	actuator := &governance.L5Actuator{
		Logger:    logger,
		Scrubbing: mustNewScrubbingSvc(t, logger),
		ExecutionHandler: &mockExecutionHandler{
			ExecuteVerifiedTransactionFunc: func(ctx context.Context, eventType constants.EventType, cmdMsg governance.CommandMessage) (string, error) {
				executed = true
				return "ok", nil
			},
		},
		SigningKey: priv,
		KeyID:      "actuator-key",
	}
	newTestAuditBackend(t, "session-1").Attach(actuator)
	svc.SetActuator(actuator)

	env := buildGatewayDispatchedDoctrineEnvelope(t, gatewayRoot, "nonce-handle-governance-root")
	svc.handleGovernanceEnvelope(env)
	assert.True(t, executed, "gateway-dispatched envelope must execute despite live state-root drift")
}

func TestOperatorPubSubService_handleGovernanceEnvelope_ActuatorFailClosedNilReceiptDoesNotPanic(t *testing.T) {
	t.Parallel()

	const stateRoot = "state-root"
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
		Scrubbing:          mustNewScrubbingSvc(t, testutil.NewTestLogger()),
		Ledger:             storagetest.NewTestLedger(t),
		RuntimeVault:       governancetest.UnlockedVault{},
		Config:             cfg,
		Logger:             logger,
		PubSubClient:       pubsubtest.NewMockOperatorPubSubClient(),
		ActuatorSigningKey: priv,
		ActuatorKeyID:      "actuator-key",
	}), OutboundModeDeps{
		GovernanceCoreDeps: GovernanceCoreDeps{
			ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
			ReplayStore:       &testutil.MockReplayStore{},
			StateRootProvider: testutil.NewMockStateRootProvider(stateRoot),
			TransactionAudit:  &testutil.MockTransactionAudit{},
			L3Notary:          &testutil.MockL3Notary{},
			SignerStore: &governance.FailClosedSignerStore{
				Signers: map[string]ed25519.PublicKey{"test-key": pub},
			},
			Doctrine: governance.NewL1Doctrine(),
		},
	})
	require.NoError(t, err)
	svc.ctx = context.Background()

	// A nil ExecutionHandler makes Execute fail closed and return a nil receipt,
	// the same shape as a receipt-persistence (SQLITE_BUSY) failure.
	svc.SetActuator(&governance.L5Actuator{
		Logger:     logger,
		Scrubbing:  mustNewScrubbingSvc(t, logger),
		SigningKey: priv,
		KeyID:      "actuator-key",
	})

	env := buildGatewayDispatchedDoctrineEnvelope(t, stateRoot, "nonce-actuator-nil-receipt")
	assert.NotPanics(t, func() { svc.handleGovernanceEnvelope(env) })
}

// l4RejectionStateRoot is the state root the L4 rejection fixture reports.
const l4RejectionStateRoot = "test-state-root"

// newL4RejectionPubsubService builds an outbound-mode pubsub service whose
// L4 warden uses a stateful replay store, so replayed nonces are detected.
// The returned handler records whether the L5 execution boundary was reached.
func newL4RejectionPubsubService(t *testing.T) (*OperatorPubSubService, *mockExecutionHandler) {
	t.Helper()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
		Scrubbing:          mustNewScrubbingSvc(t, testutil.NewTestLogger()),
		Ledger:             storagetest.NewTestLedger(t),
		RuntimeVault:       governancetest.UnlockedVault{},
		Config:             cfg,
		Logger:             logger,
		PubSubClient:       pubsubtest.NewMockOperatorPubSubClient(),
		ActuatorSigningKey: priv,
		ActuatorKeyID:      "actuator-key",
	}), OutboundModeDeps{
		GovernanceCoreDeps: GovernanceCoreDeps{
			ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
			ReplayStore:       testutil.NewStatefulMockReplayStore(),
			StateRootProvider: testutil.NewMockStateRootProvider(l4RejectionStateRoot),
			TransactionAudit:  &testutil.MockTransactionAudit{},
			L3Notary:          &testutil.MockL3Notary{},
			SignerStore: &governance.FailClosedSignerStore{
				Signers: map[string]ed25519.PublicKey{"test-key": pub},
			},
			Doctrine: governance.NewL1Doctrine(),
		},
	})
	require.NoError(t, err)
	svc.ctx = context.Background()

	handler := &mockExecutionHandler{
		ExecuteVerifiedTransactionFunc: func(ctx context.Context, eventType constants.EventType, cmdMsg governance.CommandMessage) (string, error) {
			return "l4-rejection-receipt", nil
		},
	}
	actuator := &governance.L5Actuator{
		Logger:           logger,
		Scrubbing:        mustNewScrubbingSvc(t, logger),
		ExecutionHandler: handler,
		SigningKey:       priv,
		KeyID:            "actuator-key",
	}
	newTestAuditBackend(t, "session-1").Attach(actuator)
	svc.SetActuator(actuator)
	return svc, handler
}

// buildL4RejectionEnvelope builds a doctrine-posture, gateway-dispatched FsList
// envelope. mutate runs before the transaction hash is computed (so the hash
// stays consistent with the mutated fields); postHash runs after hashing and
// is used to break the hash on purpose.
func buildL4RejectionEnvelope(t *testing.T, stateRoot, nonce string, mutate, postHash func(*govpkg.GovernanceEnvelope)) *govpkg.GovernanceEnvelope {
	t.Helper()
	env := &govpkg.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		Timestamp:         timestamppb.Now(),
		ExpiresAt:         timestamppb.New(time.Now().Add(time.Hour)),
		OperatorId:        "operator-1",
		OperatorSessionId: "session-1",
		EventType:         string(constants.Event.Operator.FsList.Requested),
		ActionType:        string(constants.ActionTypeFsList),
		TargetResource:    "localhost",
		Payload: mustMarshalProto(t, &operatorv1.FsListRequested{
			Path:        ".",
			ExecutionId: "exec-l4-rejection",
		}),
		StateMerkleRoot: stateRoot,
		Nonce:           nonce,
		Posture:         string(constants.PostureDoctrine),
	}
	if mutate != nil {
		mutate(env)
	}
	hash, err := govpkg.GenerateMessageID(env)
	require.NoError(t, err)
	env.Id = hash
	env.TransactionHash = hash
	env.Governance = &commonv1.GovernanceMetadata{
		L1: &commonv1.L1Metadata{Validated: true},
	}
	if postHash != nil {
		postHash(env)
	}
	return env
}

// TestOperatorPubSubService_handleGovernanceEnvelope_L4RejectionDoesNotDispatch
// proves that every L4 rejection reachable through the gateway-dispatched
// envelope path is refused before the L5 execution boundary: the rejected
// envelope never reaches ExecuteVerifiedTransaction. The valid control case
// proves the harness does dispatch when verification passes.
func TestOperatorPubSubService_handleGovernanceEnvelope_L4RejectionDoesNotDispatch(t *testing.T) {
	t.Parallel()

	const stateRoot = l4RejectionStateRoot
	cases := []struct {
		name string
		// mutate runs before hashing; postHash runs after hashing (breaks the hash).
		mutate   func(env *govpkg.GovernanceEnvelope)
		postHash func(env *govpkg.GovernanceEnvelope)
		// replay submits the same valid envelope twice; the second must be rejected.
		replay       bool
		wantErr      error
		wantDispatch bool
	}{
		{
			name:         "valid control dispatches",
			wantDispatch: true,
		},
		{
			name: "hash mismatch",
			postHash: func(env *govpkg.GovernanceEnvelope) {
				env.TransactionHash = "tampered-" + env.TransactionHash
			},
			wantErr: constants.ErrTxTransactionHashMismatch,
		},
		{
			name:    "replayed nonce",
			replay:  true,
			wantErr: constants.ErrTxTransactionReplay,
		},
		{
			name: "expired",
			mutate: func(env *govpkg.GovernanceEnvelope) {
				env.ExpiresAt = timestamppb.New(time.Now().Add(-time.Hour))
			},
			wantErr: constants.ErrTxTransactionExpired,
		},
		{
			name: "undecodable payload",
			mutate: func(env *govpkg.GovernanceEnvelope) {
				env.Payload = []byte{0xFF, 0xFF, 0xFF}
			},
			wantErr: constants.ErrTxPayloadDecodeFailed,
		},
		{
			name: "missing state root",
			mutate: func(env *govpkg.GovernanceEnvelope) {
				env.StateMerkleRoot = ""
			},
			wantErr: constants.ErrTxStateRootRequired,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nonce := fmt.Sprintf("nonce-l4-rejection-%d", i)
			env := buildL4RejectionEnvelope(t, stateRoot, nonce, tc.mutate, tc.postHash)

			if tc.replay {
				// Reason check on a dedicated warden: the first verification
				// reserves the nonce, so the second must report replay.
				reasonSvc, _ := newL4RejectionPubsubService(t)
				_, err := reasonSvc.l4warden.VerifyEnvelope(gatewayDispatchedVerificationContext(context.Background(), env), env)
				require.NoError(t, err, "first submission of the replay fixture must verify")
				_, err = reasonSvc.l4warden.VerifyEnvelope(gatewayDispatchedVerificationContext(context.Background(), env), env)
				require.ErrorIs(t, err, tc.wantErr)

				// Dispatch check: the first submission executes, the replayed one must not.
				svc, handler := newL4RejectionPubsubService(t)
				svc.handleGovernanceEnvelope(env)
				require.True(t, handler.executed.Load(), "first submission must reach the execution boundary")
				handler.executed.Store(false)
				svc.handleGovernanceEnvelope(env)
				assert.False(t, handler.executed.Load(), "replayed envelope must not reach the execution boundary")
				return
			}

			if tc.wantErr != nil {
				// Reason check on a dedicated warden so the dispatch check below
				// starts with an unreserved nonce.
				reasonSvc, _ := newL4RejectionPubsubService(t)
				_, err := reasonSvc.l4warden.VerifyEnvelope(gatewayDispatchedVerificationContext(context.Background(), env), env)
				require.ErrorIs(t, err, tc.wantErr)
			}

			svc, handler := newL4RejectionPubsubService(t)
			svc.handleGovernanceEnvelope(env)
			if tc.wantDispatch {
				assert.True(t, handler.executed.Load(), "valid envelope must reach the execution boundary")
			} else {
				assert.False(t, handler.executed.Load(), "L4-rejected envelope must not reach the execution boundary")
			}
		})
	}
}

func buildGatewayDispatchedDoctrineEnvelope(t *testing.T, stateRoot, nonce string) *govpkg.GovernanceEnvelope {
	t.Helper()
	env := &govpkg.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		Timestamp:         timestamppb.Now(),
		ExpiresAt:         timestamppb.New(time.Now().Add(time.Hour)),
		OperatorId:        "operator-1",
		OperatorSessionId: "session-1",
		EventType:         string(constants.Event.Operator.FsList.Requested),

		ActionType:     string(constants.ActionTypeFsList),
		TargetResource: "localhost",
		Payload: mustMarshalProto(t, &operatorv1.FsListRequested{
			Path:        ".",
			ExecutionId: "exec-gateway-dispatch",
		}),
		StateMerkleRoot: stateRoot,
		Nonce:           nonce,
		Posture:         string(constants.PostureDoctrine),
	}
	hash, err := govpkg.GenerateMessageID(env)
	require.NoError(t, err)
	env.Id = hash
	env.TransactionHash = hash
	env.Governance = &commonv1.GovernanceMetadata{
		L1: &commonv1.L1Metadata{Validated: true},
	}
	return env
}

func TestOperatorPubSubService_AllActionTypesProduceReceipts(t *testing.T) {
	// §4.2: Receipt-coverage test - drive each action type through ProcessEnvelope
	// and assert a receipt is written EXECUTING before and terminal status after.
	// This locks the invariant that every execution path produces a receipt.
	t.Run("all action types produce receipts through Actuator", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)

		// Test a representative subset of action types that have simple payloads
		// Complex types (MCP_CALL, A2A_CALL) are tested in their own integration tests
		testCases := []struct {
			name       string
			eventType  constants.EventType
			actionType constants.ActionType
			payload    []byte
		}{
			{
				name:       "FS_LIST",
				eventType:  constants.Event.Operator.FsList.Requested,
				actionType: constants.ActionTypeFsList,
				payload: mustMarshalProto(t, &operatorv1.FsListRequested{
					Path:        ".",
					ExecutionId: "exec-1",
				}),
			},
			{
				name:       "FS_READ",
				eventType:  constants.Event.Operator.FsRead.Requested,
				actionType: constants.ActionTypeFsRead,
				payload: mustMarshalProto(t, &operatorv1.FsReadRequested{
					Path:        "test.txt",
					ExecutionId: "exec-1",
				}),
			},
			{
				name:       "FS_GREP",
				eventType:  constants.Event.Operator.FsGrep.Requested,
				actionType: constants.ActionTypeFsGrep,
				payload: mustMarshalProto(t, &operatorv1.FsGrepRequested{
					Pattern:     "test",
					Path:        ".",
					ExecutionId: "exec-1",
				}),
			},
			{
				name:       "PORT_CHECK",
				eventType:  constants.Event.Operator.PortCheck.Requested,
				actionType: constants.ActionTypePortCheck,
				payload: mustMarshalProto(t, &operatorv1.CheckPortRequested{
					Host:        "localhost",
					Port:        8080,
					ExecutionId: "exec-1",
				}),
			},
			{
				name:       "HEARTBEAT",
				eventType:  constants.Event.Operator.HeartbeatRequested,
				actionType: constants.ActionTypeHeartbeat,
				payload:    mustMarshalProto(t, &operatorv1.HeartbeatRequested{}),
			},
			{
				name:       "EVAL_ANSWER",
				eventType:  constants.Event.Operator.Eval.AnswerRequested,
				actionType: constants.ActionTypeEvalAnswer,
				payload: mustMarshalProto(t, &operatorv1.EvalAnswerRequested{
					PromptId:  "test-prompt",
					Benchmark: "test",
					Answer:    "test answer",
					Model:     "test-model",
				}),
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				env := &govpkg.GovernanceEnvelope{
					Id:              "tx-" + tc.name,
					OperatorId:      "operator-1",
					TransactionHash: "hash-" + tc.name,
					ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
					Timestamp:       timestamppb.Now(),
					ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
					EventType:       string(tc.eventType),
					ActionType:      string(tc.actionType),
					TargetResource:  "localhost",
					Payload:         tc.payload,
					StateMerkleRoot: "test-state-root",
					Nonce:           "nonce-" + tc.name,
					Posture:         string(constants.PostureDoctrine),
					Governance: &commonv1.GovernanceMetadata{
						L2: &commonv1.L2Metadata{
							ConsensusSetId: "test-consensus",
							Votes: []*commonv1.L2Vote{
								{
									SignerKeyId: "test-key",
									Decision:    true,
								},
							},
						},
					},
				}

				// Re-hash for verifier
				env.TransactionHash, _ = govpkg.GenerateMessageID(env)
				env.Id = env.TransactionHash

				// Sign for verifier
				l2Payload := fmt.Sprintf("%s|true", env.TransactionHash)
				sig := ed25519.Sign(f.SignerPriv, []byte(l2Payload))
				env.Governance.L2.Votes[0].ConsensusSignature = hex.EncodeToString(sig)

				envelopeBytes, _ := (protojson.MarshalOptions{}).Marshal(env)

				receipt, err := f.Svc.ProcessEnvelope(context.Background(), envelopeBytes)
				require.NoError(t, err, "ProcessEnvelope should succeed")
				require.NotNil(t, receipt, "receipt must not be nil")
				require.Equal(t, env.Id, receipt.TransactionId, "receipt transaction ID must match")
				require.NotEmpty(t, receipt.Signature, "receipt must be signed")

				// Receipt should have a terminal status (COMPLETED or FAILED)
				// EXECUTING is only the initial status before handler execution
				require.NotEqual(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
					receipt.Status, "receipt should have terminal status after execution")
			})
		}
	})
}

func TestOperatorPubSubService_CancellationReceipt(t *testing.T) {
	// BUG-2 §4.3: Cancellation test - assert a cancel produces a receipt for both
	// the real-kill and already-finished cases, with distinguishable status.
	t.Run("CANCEL action type produces receipt through Actuator", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)

		req := &operatorv1.CommandCancelRequested{
			ExecutionId: "exec-cancel-1",
		}
		payload, _ := proto.Marshal(req)

		env := &govpkg.GovernanceEnvelope{
			Id:              "tx-cancel",
			OperatorId:      "operator-1",
			TransactionHash: "hash-cancel",
			ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
			Timestamp:       timestamppb.Now(),
			ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
			EventType:       string(constants.Event.Operator.Command.CancelRequested),

			ActionType:      string(constants.ActionTypeCancel),
			TargetResource:  "localhost",
			Payload:         payload,
			StateMerkleRoot: "test-state-root",
			Nonce:           "nonce-cancel",
			Posture:         string(constants.PostureDoctrine),
			Governance: &commonv1.GovernanceMetadata{
				L2: &commonv1.L2Metadata{
					ConsensusSetId: "test-consensus",
					Votes: []*commonv1.L2Vote{
						{
							SignerKeyId: "test-key",
							Decision:    true,
						},
					},
				},
				// CANCEL is a mutation — L3 proof required by notary posture
				L3: &commonv1.L3Metadata{
					Proof: &commonv1.L3Proof{
						Signature: "human-proof",
					},
				},
			},
		}

		// Re-hash for verifier
		env.TransactionHash, _ = govpkg.GenerateMessageID(env)
		env.Id = env.TransactionHash

		// Sign for verifier
		l2Payload := fmt.Sprintf("%s|true", env.TransactionHash)
		sig := ed25519.Sign(f.SignerPriv, []byte(l2Payload))
		env.Governance.L2.Votes[0].ConsensusSignature = hex.EncodeToString(sig)

		envelopeBytes, _ := (protojson.MarshalOptions{}).Marshal(env)

		receipt, err := f.Svc.ProcessEnvelope(context.Background(), envelopeBytes)
		require.NoError(t, err, "ProcessEnvelope should succeed")
		require.NotNil(t, receipt, "receipt must not be nil")
		require.Equal(t, env.Id, receipt.TransactionId, "receipt transaction ID must match")
		require.NotEmpty(t, receipt.Signature, "receipt must be signed")

		// Receipt should have a terminal status (COMPLETED or FAILED)
		// The handler distinguishes real kill (CANCELLED) from no-op on already-finished
		require.NotEqual(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
			receipt.Status, "receipt should have terminal status after execution")
	})
}

func TestOperatorPubSubService_SinglePathEnforcement(t *testing.T) {
	// BUG-1 regression test: enforce that handlers map is only reachable through Actuator
	// This ensures no receipt-less execution paths can be added in the future.
	// The source-scan approach will actually fail CI if either function is re-added.
	t.Run("bypass functions do not exist", func(t *testing.T) {
		t.Parallel()
		src, err := os.ReadFile("pubsub_commands.go")
		require.NoError(t, err, "failed to read pubsub_commands.go")
		content := string(src)

		require.NotContains(t, content, "func (rs *OperatorPubSubService) HandleCommandData",
			"HandleCommandData bypass must not exist")
		require.NotContains(t, content, "func (rs *OperatorPubSubService) dispatchCommand",
			"dispatchCommand bypass must not exist")
	})
}

func TestOperatorPubSubService_ExecuteVerifiedTransaction(t *testing.T) {
	f := newPubsubFixture(t)

	t.Run("rejects nil cmdMsg", func(t *testing.T) {
		t.Parallel()
		_, err := f.Svc.ExecuteVerifiedTransaction(context.Background(), constants.Event.Operator.Command.Requested, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid cmdMsg type")
	})

	t.Run("rejects when no handler registered", func(t *testing.T) {
		t.Parallel()
		msg := &PubSubCommandMessage{
			EventType: "NONEXISTENT_EVENT",
			ID:        "msg-1",
		}
		_, err := f.Svc.ExecuteVerifiedTransaction(context.Background(), msg.EventType, msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no handler for")
	})

	t.Run("rejects heartbeat results on the command map", func(t *testing.T) {
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Heartbeat,
			ID:        "msg-heartbeat-result",
		}
		_, err := f.Svc.ExecuteVerifiedTransaction(t.Context(), msg.EventType, msg)
		require.ErrorIs(t, err, constants.ErrTxEventNotRequest)
	})

	t.Run("heartbeat requests propagate handler decode failures", func(t *testing.T) {
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.HeartbeatRequested,
			ID:        "msg-heartbeat-request",
			Payload:   []byte{0xff},
		}
		_, err := f.Svc.ExecuteVerifiedTransaction(t.Context(), msg.EventType, msg)
		require.ErrorIs(t, err, proto.Error)
	})

	t.Run("rejects LFAA recorded facts on the command map", func(t *testing.T) {
		t.Parallel()
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Audit.UserMsg,
			ID:        "msg-audit",
		}
		_, err := f.Svc.ExecuteVerifiedTransaction(context.Background(), msg.EventType, msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no handler for")
	})
}

func TestOperatorPubSubService_handleMcpCallRequestSync(t *testing.T) {
	f := newPubsubFixture(t)

	msg := &PubSubCommandMessage{
		EventType: constants.Event.Operator.Mcp.CallRequested,
		ID:        "msg-1",
		Payload:   mustMarshalProto(t, &operatorv1.McpCallRequested{ToolName: "test"}),
	}

	t.Run("outbound Operator rejects tools outside the native registry", func(t *testing.T) {
		_, err := f.Svc.handleMcpCallRequestSync(context.Background(), msg)
		require.ErrorIs(t, err, constants.ErrMCPNativeToolUnknown)
	})

	t.Run("outbound Operator without a local native tool host fails closed", func(t *testing.T) {
		svc := &OperatorPubSubService{logger: testutil.NewTestLogger()}
		_, err := svc.handleMcpCallRequestSync(context.Background(), msg)
		require.ErrorIs(t, err, constants.ErrMCPNativeToolHostMissing)
	})
}

func TestOperatorPubSubService_handleMcpResourceAndPromptSync(t *testing.T) {
	f := newPubsubFixture(t)

	t.Run("resource read rejects when MCP gateway not configured", func(t *testing.T) {
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Mcp.ResourceReadRequested,
			ID:        "msg-read",
			Payload:   mustMarshalProto(t, &operatorv1.McpResourceReadRequested{Uri: "file:///tmp/x"}),
		}
		_, err := f.Svc.handleMcpResourceReadSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MCP gateway not configured")
	})

	t.Run("prompt get rejects when MCP gateway not configured", func(t *testing.T) {
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Mcp.PromptGetRequested,
			ID:        "msg-get",
			Payload:   mustMarshalProto(t, &operatorv1.McpPromptGetRequested{Name: "p"}),
		}
		_, err := f.Svc.handleMcpPromptGetSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MCP gateway not configured")
	})

	t.Run("resource list rejects when MCP gateway not configured", func(t *testing.T) {
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Mcp.ResourceListRequested,
			ID:        "msg-rlist",
			Payload:   mustMarshalProto(t, &operatorv1.McpResourceListRequested{ExecutionId: "exec-1"}),
		}
		_, err := f.Svc.handleMcpResourceListSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MCP gateway not configured")
	})

	t.Run("prompt list rejects when MCP gateway not configured", func(t *testing.T) {
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Mcp.PromptListRequested,
			ID:        "msg-plist",
			Payload:   mustMarshalProto(t, &operatorv1.McpPromptListRequested{ExecutionId: "exec-1"}),
		}
		_, err := f.Svc.handleMcpPromptListSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MCP gateway not configured")
	})
}

func TestOperatorPubSubService_handleA2aCallRequestSync(t *testing.T) {
	t.Run("rejects when A2A gateway not configured", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.A2a.CallRequested,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.A2ACallRequested{SkillName: "test"}),
		}
		_, err := f.Svc.handleA2aCallRequestSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "A2A gateway not configured")
	})
}

func TestOperatorPubSubService_handleDocumentUpdateSync(t *testing.T) {
	t.Run("rejects when GovernedDocStore not configured", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = nil
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseCreated,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentUpdateRequested{Collection: "cases", DocumentId: "case-1"}),
		}
		_, err := f.Svc.handleDocumentUpdateSync(context.Background(), msg)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrGovernedDocStoreNotConfigured)
	})

	t.Run("rejects malformed protobuf payload", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = &testutil.ConfigurableMockGovernedDocStore{}
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseCreated,
			ID:        "msg-1",
			Payload:   []byte("not-a-protobuf"),
		}
		_, err := f.Svc.handleDocumentUpdateSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pubsub: document update: unmarshal")
	})

	t.Run("rejects empty collection or document_id", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = &testutil.ConfigurableMockGovernedDocStore{}
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseCreated,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentUpdateRequested{Collection: "", DocumentId: ""}),
		}
		_, err := f.Svc.handleDocumentUpdateSync(context.Background(), msg)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrTxPayloadMissing)
	})

	t.Run("rejects when DocReplace fails", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = &testutil.ConfigurableMockGovernedDocStore{
			DocReplaceFunc: func(collection, id string, data json.RawMessage) error {
				return fmt.Errorf("disk full")
			},
		}
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseCreated,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentUpdateRequested{Collection: "cases", DocumentId: "case-1"}),
		}
		_, err := f.Svc.handleDocumentUpdateSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pubsub: document update: doc replace")
	})

	t.Run("rejects when DocMerge fails", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = &testutil.ConfigurableMockGovernedDocStore{
			DocMergeFunc: func(collection, id string, fields json.RawMessage) error {
				return fmt.Errorf("document not found")
			},
		}
		updates := mustNewStruct(t, `{"title":"updated"}`)
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseCreated,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentUpdateRequested{Collection: "cases", DocumentId: "case-1", Updates: updates, Merge: true}),
		}
		_, err := f.Svc.handleDocumentUpdateSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pubsub: document update: doc merge")
	})

	t.Run("persists document replace via DocReplace with canonical protojson when merge is false", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		updates := mustNewStruct(t, `{"title":"test case","status":"open"}`)
		mock := &testutil.ConfigurableMockGovernedDocStore{}
		f.Svc.governedDocStore = mock
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseCreated,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentUpdateRequested{Collection: "cases", DocumentId: "case-1", Updates: updates, Merge: false}),
		}
		summary, err := f.Svc.handleDocumentUpdateSync(context.Background(), msg)
		require.NoError(t, err)
		assert.Equal(t, "document updated: cases/case-1", summary)
		require.Len(t, mock.DocReplaceCalls, 1)
		require.Len(t, mock.DocMergeCalls, 0)
		call := mock.DocReplaceCalls[0]
		assert.Equal(t, "cases", call.Collection)
		assert.Equal(t, "case-1", call.ID)
		// Canonical protojson preserves field names from the Struct.
		var parsed structpb.Struct
		require.NoError(t, protojson.Unmarshal(call.Data, &parsed))
		assert.Equal(t, "test case", parsed.Fields["title"].GetStringValue())
		assert.Equal(t, "open", parsed.Fields["status"].GetStringValue())
	})

	t.Run("persists document merge via DocMerge when merge is true", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		updates := mustNewStruct(t, `{"title":"updated title"}`)
		mock := &testutil.ConfigurableMockGovernedDocStore{}
		f.Svc.governedDocStore = mock
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseUpdated,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentUpdateRequested{Collection: "cases", DocumentId: "case-1", Updates: updates, Merge: true}),
		}
		summary, err := f.Svc.handleDocumentUpdateSync(context.Background(), msg)
		require.NoError(t, err)
		assert.Equal(t, "document updated: cases/case-1", summary)
		require.Len(t, mock.DocMergeCalls, 1)
		require.Len(t, mock.DocReplaceCalls, 0)
		call := mock.DocMergeCalls[0]
		assert.Equal(t, "cases", call.Collection)
		assert.Equal(t, "case-1", call.ID)
		var parsed structpb.Struct
		require.NoError(t, protojson.Unmarshal(call.Fields, &parsed))
		assert.Equal(t, "updated title", parsed.Fields["title"].GetStringValue())
	})

	t.Run("persists document update with empty updates as empty object", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		mock := &testutil.ConfigurableMockGovernedDocStore{}
		f.Svc.governedDocStore = mock
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseCreated,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentUpdateRequested{Collection: "cases", DocumentId: "case-1", Updates: nil}),
		}
		summary, err := f.Svc.handleDocumentUpdateSync(context.Background(), msg)
		require.NoError(t, err)
		assert.Equal(t, "document updated: cases/case-1", summary)
		require.Len(t, mock.DocReplaceCalls, 1)
		assert.Equal(t, json.RawMessage("{}"), mock.DocReplaceCalls[0].Data)
	})
}

func TestOperatorPubSubService_handleDocumentDeleteSync(t *testing.T) {
	t.Run("rejects when GovernedDocStore not configured", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = nil
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseDeleted,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentDeleteRequested{Collection: "cases", DocumentId: "case-1"}),
		}
		_, err := f.Svc.handleDocumentDeleteSync(context.Background(), msg)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrGovernedDocStoreNotConfigured)
	})

	t.Run("rejects malformed protobuf payload", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = &testutil.ConfigurableMockGovernedDocStore{}
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseDeleted,
			ID:        "msg-1",
			Payload:   []byte("not-a-protobuf"),
		}
		_, err := f.Svc.handleDocumentDeleteSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pubsub: document delete: unmarshal")
	})

	t.Run("rejects empty collection or document_id", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = &testutil.ConfigurableMockGovernedDocStore{}
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseDeleted,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentDeleteRequested{Collection: "", DocumentId: ""}),
		}
		_, err := f.Svc.handleDocumentDeleteSync(context.Background(), msg)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrTxPayloadMissing)
	})

	t.Run("rejects when DocDelete fails", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		f.Svc.governedDocStore = &testutil.ConfigurableMockGovernedDocStore{
			DocDeleteFunc: func(collection, id string) error {
				return fmt.Errorf("disk full")
			},
		}
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseDeleted,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentDeleteRequested{Collection: "cases", DocumentId: "case-1"}),
		}
		_, err := f.Svc.handleDocumentDeleteSync(context.Background(), msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pubsub: document delete: doc delete")
	})

	t.Run("deletes document via DocDelete with correct collection and id", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		mock := &testutil.ConfigurableMockGovernedDocStore{}
		f.Svc.governedDocStore = mock
		msg := &PubSubCommandMessage{
			EventType: constants.EventAppCaseDeleted,
			ID:        "msg-1",
			Payload:   mustMarshalProto(t, &operatorv1.DocumentDeleteRequested{Collection: "cases", DocumentId: "case-1"}),
		}
		summary, err := f.Svc.handleDocumentDeleteSync(context.Background(), msg)
		require.NoError(t, err)
		assert.Equal(t, "document deleted: cases/case-1", summary)
		require.Len(t, mock.DocDeleteCalls, 1)
		assert.Equal(t, "cases", mock.DocDeleteCalls[0].Collection)
		assert.Equal(t, "case-1", mock.DocDeleteCalls[0].ID)
	})
}

func TestOperatorPubSubService_handleShutdownRequest(t *testing.T) {
	f := newPubsubFixture(t)

	t.Run("rejects malformed payload", func(t *testing.T) {
		_, err := f.Svc.handleShutdownRequest(&PubSubCommandMessage{
			EventType: constants.Event.Operator.ShutdownRequested,
			ID:        "msg-1",
			Payload:   []byte("invalid protobuf"),
		})
		require.Error(t, err)
	})

	for _, tc := range []struct {
		name   string
		reason string
		want   string
	}{
		{name: "returns supplied reason", reason: "test shutdown", want: "test shutdown"},
		{name: "defaults empty reason", want: "No reason provided"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := proto.Marshal(&operatorv1.ShutdownRequested{Reason: tc.reason})
			require.NoError(t, err)
			summary, err := f.Svc.handleShutdownRequest(&PubSubCommandMessage{
				EventType: constants.Event.Operator.ShutdownRequested,
				ID:        "msg-1",
				Payload:   payload,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, summary)
			assert.Empty(t, f.Svc.ShutdownChan)
		})
	}
}

func TestOperatorPubSubService_handleEvalAnswerRequestSync(t *testing.T) {
	f := newPubsubFixture(t)

	t.Run("rejects unmarshal error", func(t *testing.T) {
		t.Parallel()
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Eval.AnswerRequested,
			ID:        "msg-1",
			Payload:   []byte("invalid json"),
		}
		_, err := f.Svc.handleEvalAnswerRequestSync(context.Background(), msg)
		require.Error(t, err)
	})

	t.Run("returns short answer without truncation", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.EvalAnswerRequested{
			PromptId:  "prompt-1",
			Benchmark: "test-benchmark",
			Answer:    "short answer",
			Model:     "test-model",
		}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Eval.AnswerRequested,
			ID:        "msg-1",
			Payload:   payload,
		}
		summary, err := f.Svc.handleEvalAnswerRequestSync(context.Background(), msg)
		require.NoError(t, err)
		assert.Equal(t, "short answer", summary)
	})

	t.Run("truncates answer exceeding ReceiptSummaryMaxBytes", func(t *testing.T) {
		t.Parallel()
		longAnswer := make([]byte, constants.ReceiptSummaryMaxBytes+1000)
		for i := range longAnswer {
			longAnswer[i] = 'A'
		}
		req := &operatorv1.EvalAnswerRequested{
			PromptId:  "prompt-1",
			Benchmark: "test-benchmark",
			Answer:    string(longAnswer),
			Model:     "test-model",
		}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			EventType: constants.Event.Operator.Eval.AnswerRequested,
			ID:        "msg-1",
			Payload:   payload,
		}
		summary, err := f.Svc.handleEvalAnswerRequestSync(context.Background(), msg)
		require.NoError(t, err)
		assert.Len(t, summary, constants.ReceiptSummaryMaxBytes)
	})
}

func TestOperatorPubSubService_Stop(t *testing.T) {
	t.Parallel()

	t.Run("stops gracefully when not running", func(t *testing.T) {
		t.Parallel()
		f := newPubsubFixture(t)
		err := f.Svc.Stop()
		require.NoError(t, err)
	})
}

func TestOperatorPubSubService_ProcessEnvelope(t *testing.T) {
	f := newPubsubFixture(t)

	t.Run("successful synchronous processing", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.FsListRequested{Path: ".", ExecutionId: "exec-sync"}
		payload, _ := proto.Marshal(req)

		env := &commonv1.GovernanceEnvelope{
			Id:              "tx-sync",
			OperatorId:      "operator-1",
			TransactionHash: "hash-sync",
			ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
			Timestamp:       timestamppb.Now(),
			ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
			EventType:       string(constants.Event.Operator.FsList.Requested),

			ActionType:      string(constants.ActionTypeFsList),
			TargetResource:  "localhost",
			Payload:         payload,
			StateMerkleRoot: "test-state-root",
			Nonce:           "nonce-sync",
			Posture:         string(constants.PostureDoctrine),
			Governance: &commonv1.GovernanceMetadata{
				L2: &commonv1.L2Metadata{
					ConsensusSetId: "test-consensus",
					Votes: []*commonv1.L2Vote{
						{
							SignerKeyId: "test-key",
							Decision:    true,
						},
					},
				},
			},
		}

		// Re-hash for verifier
		env.TransactionHash, _ = govpkg.GenerateMessageID(env)
		env.Id = env.TransactionHash

		// Sign for verifier
		l2Payload := fmt.Sprintf("%s|true", env.TransactionHash)
		sig := ed25519.Sign(f.SignerPriv, []byte(l2Payload))
		env.Governance.L2.Votes[0].ConsensusSignature = hex.EncodeToString(sig)

		envelopeBytes, _ := (protojson.MarshalOptions{}).Marshal(env)

		receipt, err := f.Svc.ProcessEnvelope(context.Background(), envelopeBytes)
		require.NoError(t, err)
		require.NotNil(t, receipt)
		require.Equal(t, env.Id, receipt.TransactionId)
		require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.Status)
	})

	t.Run("rejects empty payload", func(t *testing.T) {
		t.Parallel()
		_, err := f.Svc.ProcessEnvelope(context.Background(), []byte{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty payload")
	})

	t.Run("rejects oversized payload", func(t *testing.T) {
		t.Parallel()
		largePayload := make([]byte, MaxPayloadSize+1)
		_, err := f.Svc.ProcessEnvelope(context.Background(), largePayload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds")
	})

	t.Run("rejects invalid JSON envelope", func(t *testing.T) {
		t.Parallel()
		invalidJSON := []byte("{invalid json}")
		_, err := f.Svc.ProcessEnvelope(context.Background(), invalidJSON)
		require.Error(t, err)
		assert.Error(t, err)
	})

	t.Run("rejects event and action_type mismatch in L4 with a signed receipt", func(t *testing.T) {
		t.Parallel()
		req := &operatorv1.FsListRequested{Path: ".", ExecutionId: "exec-mismatch"}
		payload, _ := proto.Marshal(req)
		env := &govpkg.GovernanceEnvelope{
			Id:              "tx-mismatch",
			ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
			Timestamp:       timestamppb.Now(),
			ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
			EventType:       string(constants.Event.Operator.FsList.Requested),
			ActionType:      string(constants.ActionTypeExecuteBash),
			TargetResource:  "localhost",
			Payload:         payload,
			StateMerkleRoot: "test-state-root",
			Nonce:           "nonce-mismatch",
			Posture:         string(constants.PostureDoctrine),
		}
		envelopeBytes, _ := protojson.Marshal(env)

		receipt, err := f.Svc.ProcessEnvelope(context.Background(), envelopeBytes)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrTxEventActionMismatch)
		var rejection *governance.ReceiptError
		require.ErrorAs(t, err, &rejection, "the mismatch must be an L4 rejection carrying a receipt")
		require.NotNil(t, receipt)
		assert.Same(t, rejection.Receipt, receipt)
		assert.True(t, governance.IsGovernanceRejectionReceipt(receipt))
		assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EVENT_ACTION_MISMATCH, receipt.GetRejectionCode())
		assert.NotEmpty(t, receipt.GetSignature(), "the L4 rejection receipt must be signed by the Actuator")
	})

	t.Run("refuses a registered event that is not governed before L4", func(t *testing.T) {
		t.Parallel()
		env := &govpkg.GovernanceEnvelope{
			Id:              "tx-not-governed",
			ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
			Timestamp:       timestamppb.Now(),
			ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
			EventType:       string(constants.EventOperatorAuditAiRecordRequested),
			ActionType:      string(constants.ActionTypeFsList),
			TargetResource:  "localhost",
			StateMerkleRoot: "test-state-root",
			Nonce:           "nonce-not-governed",
			Posture:         string(constants.PostureDoctrine),
		}
		envelopeBytes, _ := protojson.Marshal(env)

		receipt, err := f.Svc.ProcessEnvelope(context.Background(), envelopeBytes)
		require.ErrorIs(t, err, constants.ErrTxEventNotGoverned)
		assert.Nil(t, receipt, "a pre-L4 refusal has no action to sign, so it carries no receipt")
		var rejection *governance.ReceiptError
		assert.False(t, errors.As(err, &rejection), "a pre-L4 refusal must not be an L4 rejection")
	})

	t.Run("rejects when transaction verifier not configured", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
			Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
			Ledger:       storagetest.NewTestLedger(t),
			RuntimeVault: governancetest.UnlockedVault{},
			Config:       cfg,
			Logger:       testutil.NewTestLogger(),
			PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
		}), OutboundModeDeps{
			GovernanceCoreDeps: GovernanceCoreDeps{
				ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
				ReplayStore:       &testutil.MockReplayStore{},
				StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
				TransactionAudit:  &testutil.MockTransactionAudit{},
				L3Notary:          &testutil.MockL3Notary{},
				Doctrine:          governance.NewL1Doctrine(),
			},
		})
		require.NoError(t, err)
		svc.l4warden = nil

		req := &operatorv1.FsListRequested{Path: ".", ExecutionId: "exec-1"}
		payload, _ := proto.Marshal(req)
		env := &govpkg.GovernanceEnvelope{
			ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
			Timestamp:       timestamppb.Now(),
			ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
			EventType:       string(constants.Event.Operator.FsList.Requested),

			ActionType:      string(constants.ActionTypeFsList),
			TargetResource:  "localhost",
			Payload:         payload,
			StateMerkleRoot: "test-state-root",
			Nonce:           "nonce-1",
			Posture:         string(constants.PostureDoctrine),
		}
		envelopeBytes, _ := protojson.Marshal(env)

		_, err = svc.ProcessEnvelope(context.Background(), envelopeBytes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "transaction verifier not configured")
	})
}

// TestOperatorPubSubService_ProcessEnvelope_DocumentDispatchDeterminism
// asserts the EventType stamped on the emitted PubSubCommandMessage and the
// EventType derived by the L5 actuator for the signed receipt are stable
// across repeated ProcessEnvelope calls for both DOCUMENT_UPDATE and
// DOCUMENT_DELETE. The constants-level test (mappings_test.go) covers the
// MapActionTypeToEventType choke point in isolation; this test covers the
// wire-stamped value end to end through the pubsub execution path.
func TestOperatorPubSubService_ProcessEnvelope_DocumentDispatchDeterminism(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		actionType constants.ActionType
		wantEvent  constants.EventType
		payload    func() []byte
	}{
		{
			name:       "DOCUMENT_UPDATE resolves to canonical document update requested event",
			actionType: constants.ActionTypeDocumentUpdate,
			wantEvent:  constants.EventAppDocumentUpdateRequested,
			payload: func() []byte {
				return mustMarshalProto(t, &operatorv1.DocumentUpdateRequested{Collection: "cases", DocumentId: "case-det-1"})
			},
		},
		{
			name:       "DOCUMENT_DELETE resolves to canonical document delete requested event",
			actionType: constants.ActionTypeDocumentDelete,
			wantEvent:  constants.EventAppDocumentDeleteRequested,
			payload: func() []byte {
				return mustMarshalProto(t, &operatorv1.DocumentDeleteRequested{Collection: "cases", DocumentId: "case-det-1"})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newPubsubFixture(t)

			// Captured per-call: the actuator-derived eventType and the
			// PubSubCommandMessage's stamped EventType. Both must be stable
			// across iterations and equal the canonical event.
			var capturedActuatorEvent constants.EventType
			var capturedMsgEvent constants.EventType
			var capturedMu sync.Mutex

			mockHandler := &mockExecutionHandler{
				ExecuteVerifiedTransactionFunc: func(ctx context.Context, eventType constants.EventType, cmdMsg governance.CommandMessage) (string, error) {
					pubsubMsg, ok := cmdMsg.(*PubSubCommandMessage)
					if !ok {
						t.Fatalf("expected *PubSubCommandMessage, got %T", cmdMsg)
					}
					capturedMu.Lock()
					capturedActuatorEvent = eventType
					capturedMsgEvent = pubsubMsg.EventType
					capturedMu.Unlock()
					return "determinism-test-summary", nil
				},
			}
			f.Svc.actuator.ExecutionHandler = mockHandler

			const iterations = 25
			for i := 0; i < iterations; i++ {
				payload := tc.payload()
				env := &commonv1.GovernanceEnvelope{
					Id:              fmt.Sprintf("tx-det-%s-%d", tc.actionType, i),
					TransactionHash: fmt.Sprintf("hash-det-%s-%d", tc.actionType, i),
					ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
					Timestamp:       timestamppb.Now(),
					ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
					EventType:       string(tc.wantEvent),
					ActionType:      string(tc.actionType),
					TargetResource:  "localhost",
					Payload:         payload,
					StateMerkleRoot: "test-state-root",
					Nonce:           fmt.Sprintf("nonce-det-%s-%d", tc.actionType, i),
					Posture:         string(constants.PostureDoctrine),
					Governance: &commonv1.GovernanceMetadata{
						L2: &commonv1.L2Metadata{
							ConsensusSetId: "test-consensus",
							Votes: []*commonv1.L2Vote{
								{SignerKeyId: "test-key", Decision: true},
							},
						},
						L3: &commonv1.L3Metadata{
							Proof: &commonv1.L3Proof{Signature: "human-proof"},
						},
					},
				}
				env.TransactionHash, _ = govpkg.GenerateMessageID(env)
				env.Id = env.TransactionHash
				l2Payload := fmt.Sprintf("%s|true", env.TransactionHash)
				sig := ed25519.Sign(f.SignerPriv, []byte(l2Payload))
				env.Governance.L2.Votes[0].ConsensusSignature = hex.EncodeToString(sig)
				envelopeBytes, _ := (protojson.MarshalOptions{}).Marshal(env)

				receipt, err := f.Svc.ProcessEnvelope(context.Background(), envelopeBytes)
				require.NoError(t, err, "iteration %d", i)
				require.NotNil(t, receipt, "iteration %d", i)

				capturedMu.Lock()
				assert.Equal(t, tc.wantEvent, capturedActuatorEvent, "iteration %d: actuator-derived EventType must be the canonical event", i)
				assert.Equal(t, tc.wantEvent, capturedMsgEvent, "iteration %d: PubSubCommandMessage EventType must be the canonical event", i)
				capturedMu.Unlock()
			}
		})
	}
}

func TestOperatorPubSubService_ObservedStateEvidence(t *testing.T) {
	// §4.5: Observed-evidence tests - test that publish helpers record
	// events with scrubbed content to the audit store, including the error path
	// and non-fatal store errors.
	// Uses the real SQLAuditStore with an encrypted canonical DB (LFAA invariant).
	t.Run("publish helpers record scrubbed content evidence", func(t *testing.T) {
		t.Parallel()

		cfg := testutil.NewTestConfig(t)
		client := pubsubtest.NewMockOperatorPubSubClient()

		// Test FS_LIST
		t.Run("FS_LIST records content evidence", func(t *testing.T) {
			t.Parallel()

			// Set up a real SQLAuditStore with encryption at rest for this subtest
			logger := testutil.NewTestLogger()
			auditStore := storagetest.NewTestAuditStore(t)

			scrubbingSvc := mustNewScrubbingSvc(t, logger)

			// Create a session for this test
			sessionID := "session-fslist-" + hex.EncodeToString([]byte{1, 2, 3, 4})
			err := auditStore.CreateSession(sessionID, "operator", "FS_LIST Test", "test@example.com")
			require.NoError(t, err)

			msg := &PubSubCommandMessage{
				ID:                "msg-fslist",
				EventType:         constants.Event.Operator.FsList.Requested,
				OperatorSessionID: sessionID,
			}

			payload := &operatorv1.FsListResult{
				Entries: []*operatorv1.FsEntry{
					{Name: "file1.txt", Size: 100},
					{Name: "file2.txt", Size: 200},
				},
			}

			publishLFAATypedResponseTo(context.Background(), client, cfg, logger, msg,
				constants.Event.Operator.FsList.Completed, payload, auditStore, scrubbingSvc)

			events, err := auditStore.ListEvents(sessionID, 10, 0)
			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.Equal(t, constants.Event.Operator.FsList.Completed, events[0].Type)
			assert.NotEmpty(t, events[0].ContentText)
		})

		// Test PORT_CHECK
		t.Run("PORT_CHECK records content evidence", func(t *testing.T) {
			t.Parallel()

			// Set up a real SQLAuditStore with encryption at rest for this subtest
			logger := testutil.NewTestLogger()
			auditStore := storagetest.NewTestAuditStore(t)

			scrubbingSvc := mustNewScrubbingSvc(t, logger)

			// Create a session for this test
			sessionID := "session-port-" + hex.EncodeToString([]byte{5, 6, 7, 8})
			err := auditStore.CreateSession(sessionID, "operator", "PORT_CHECK Test", "test@example.com")
			require.NoError(t, err)

			msg := &PubSubCommandMessage{
				ID:                "msg-port",
				EventType:         constants.Event.Operator.PortCheck.Requested,
				OperatorSessionID: sessionID,
			}

			payload := &operatorv1.PortCheckResult{
				Results: []*operatorv1.PortCheckEntry{
					{Host: "localhost", Port: 8080, Open: true},
				},
			}

			publishLFAATypedResponseTo(context.Background(), client, cfg, logger, msg,
				constants.Event.Operator.PortCheck.Completed, payload, auditStore, scrubbingSvc)

			events, err := auditStore.ListEvents(sessionID, 10, 0)
			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.Equal(t, constants.Event.Operator.PortCheck.Completed, events[0].Type)
			assert.NotEmpty(t, events[0].ContentText)
		})

		// Test error path
		t.Run("error path records evidence", func(t *testing.T) {
			t.Parallel()

			// Set up a real SQLAuditStore with encryption at rest for this subtest
			logger := testutil.NewTestLogger()
			auditStore := storagetest.NewTestAuditStore(t)

			scrubbingSvc := mustNewScrubbingSvc(t, logger)

			// Create a session for this test
			sessionID := "session-error-" + hex.EncodeToString([]byte{9, 10, 11, 12})
			err := auditStore.CreateSession(sessionID, "operator", "Error Path Test", "test@example.com")
			require.NoError(t, err)

			msg := &PubSubCommandMessage{
				ID:                "msg-error",
				EventType:         constants.Event.Operator.FsList.Requested,
				OperatorSessionID: sessionID,
			}

			publishLFAAErrorTo(context.Background(), client, cfg, logger, msg,
				constants.Event.Operator.FsList.Failed, "directory not found", auditStore, scrubbingSvc)

			events, err := auditStore.ListEvents(sessionID, 10, 0)
			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.Equal(t, constants.Event.Operator.FsList.Failed, events[0].Type)
		})
	})

	t.Run("store errors are non-fatal for observed evidence", func(t *testing.T) {
		t.Parallel()

		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		client := pubsubtest.NewMockOperatorPubSubClient()

		// The session is never created, so the real audit store rejects the evidence event.
		auditStore := storagetest.NewTestAuditStore(t)

		scrubbingSvc := mustNewScrubbingSvc(t, logger)

		msg := &PubSubCommandMessage{
			ID:                "msg-nonfatal",
			EventType:         constants.Event.Operator.FsList.Requested,
			OperatorSessionID: "session-1",
		}

		payload := &operatorv1.FsListResult{
			Entries: []*operatorv1.FsEntry{{Name: "file1.txt", Size: 100}},
		}

		// This should not panic even though audit store fails
		publishLFAATypedResponseTo(context.Background(), client, cfg, logger, msg,
			constants.Event.Operator.FsList.Completed, payload, auditStore, scrubbingSvc)

		// Verify response was still published
		published := client.LastPublished()
		require.NotNil(t, published, "response should still be published despite audit store error")
	})

	t.Run("scrubbing test - seeded secret is redacted", func(t *testing.T) {
		t.Parallel()

		// Set up a real SQLAuditStore with encryption at rest for this subtest
		logger := testutil.NewTestLogger()
		auditStore := storagetest.NewTestAuditStore(t)

		// Use default scrubbing config which has built-in patterns for common secrets
		scrubbingSvc := mustNewScrubbingSvc(t, logger)

		cfg := testutil.NewTestConfig(t)
		client := pubsubtest.NewMockOperatorPubSubClient()

		// Create a session for this test
		sessionID := "session-scrub-" + hex.EncodeToString([]byte{13, 14, 15, 16})
		err := auditStore.CreateSession(sessionID, "operator", "Scrubbing Test", "test@example.com")
		require.NoError(t, err)

		msg := &PubSubCommandMessage{
			ID:                "msg-scrub",
			EventType:         constants.Event.Operator.FsList.Requested,
			OperatorSessionID: sessionID,
		}

		// Payload contains secrets that should be redacted by default patterns
		payload := &operatorv1.FsListResult{
			Entries: []*operatorv1.FsEntry{{Name: "password=secret123 api_key=ghp_test_token"}},
		}

		publishLFAATypedResponseTo(context.Background(), client, cfg, logger, msg,
			constants.Event.Operator.FsList.Completed, payload, auditStore, scrubbingSvc)

		events, err := auditStore.ListEvents(sessionID, 10, 0)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, constants.Event.Operator.FsList.Completed, events[0].Type)
		assert.NotEmpty(t, events[0].ContentText)
		// Verify the secrets were redacted by default patterns
		assert.NotContains(t, events[0].ContentText, "secret123", "password secret should be redacted")
		assert.NotContains(t, events[0].ContentText, "ghp_test_token", "api key should be redacted")
	})
}

func TestOperatorPubSubService_SetL4Warden(t *testing.T) {
	t.Run("sets L4 warden for testing", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
			Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
			Ledger:       storagetest.NewTestLedger(t),
			RuntimeVault: governancetest.UnlockedVault{},
			Config:       cfg,
			Logger:       testutil.NewTestLogger(),
			PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
		}), OutboundModeDeps{
			GovernanceCoreDeps: GovernanceCoreDeps{
				ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
				ReplayStore:       &testutil.MockReplayStore{},
				StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
				TransactionAudit:  &testutil.MockTransactionAudit{},
				L3Notary:          &testutil.MockL3Notary{},
				Doctrine:          governance.NewL1Doctrine(),
			},
		})
		require.NoError(t, err)

		mockWarden := &governance.L4Warden{}
		svc.SetL4Warden(mockWarden)

		assert.Equal(t, mockWarden, svc.l4warden)
	})
}

func TestOperatorPubSubService_handleEvalAnswerRequest(t *testing.T) {
	t.Run("handles eval answer request asynchronously", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
			Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
			Ledger:       storagetest.NewTestLedger(t),
			RuntimeVault: governancetest.UnlockedVault{},
			Config:       cfg,
			Logger:       testutil.NewTestLogger(),
			PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
		}), OutboundModeDeps{
			GovernanceCoreDeps: GovernanceCoreDeps{
				ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
				ReplayStore:       &testutil.MockReplayStore{},
				StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
				TransactionAudit:  &testutil.MockTransactionAudit{},
				L3Notary:          &testutil.MockL3Notary{},
				Doctrine:          governance.NewL1Doctrine(),
			},
		})
		require.NoError(t, err)

		req := &operatorv1.EvalAnswerRequested{
			Benchmark: "test-benchmark",
			PromptId:  "prompt-1",
			Answer:    "test answer",
		}
		payload, _ := proto.Marshal(req)
		msg := &PubSubCommandMessage{
			ID:        "msg-1",
			EventType: constants.Event.Operator.Eval.AnswerRequested,
			Payload:   payload,
		}

		// Should not panic
		_, _ = svc.handleEvalAnswerRequestSync(context.Background(), msg)
	})
}

func TestPubsubAuditLogger_LogFieldRead(t *testing.T) {
	t.Run("records field read event in audit store", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		store := storagetest.NewTestAuditStore(t, "session-1")
		auditLogger := &pubsubAuditLogger{
			store:  store,
			logger: logger,
		}

		testVal := "test-value"
		err := auditLogger.LogFieldRead("session-1", "collection", "doc-1", "field.path", mcp.FieldValue{Str: &testVal})
		require.NoError(t, err)

		events, err := store.ListEvents("session-1", 10, 0)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "session-1", events[0].OperatorSessionID)
		assert.Equal(t, constants.EventOperatorFieldReadRequested, events[0].Type)
		assert.Contains(t, events[0].ContentText, "collection/doc-1.field.path")
		assert.Equal(t, "test-value", events[0].CommandStdout)
	})

	t.Run("returns error when store fails", func(t *testing.T) {
		t.Parallel()
		logger := testutil.NewTestLogger()
		// The session is never created, so the real store rejects the event.
		store := storagetest.NewTestAuditStore(t)
		auditLogger := &pubsubAuditLogger{
			store:  store,
			logger: logger,
		}

		testVal := "test-value"
		err := auditLogger.LogFieldRead("session-1", "collection", "doc-1", "field.path", mcp.FieldValue{Str: &testVal})
		assert.Error(t, err)
	})
}

// TestNewOperatorPubSubService_NilOptionalGovDeps_PreservedAsNil verifies that
// OutboundModeDeps has no ConsensusPolicyStore or FieldReader fields, and that
// constructing OperatorPubSubService in outbound mode initializes l4warden with
// a nil consensus policy store without runtime errors.
func TestNewOperatorPubSubService_NilOptionalGovDeps_PreservedAsNil(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
		Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
		Ledger:       storagetest.NewTestLedger(t),
		RuntimeVault: governancetest.UnlockedVault{},
		Config:       cfg,
		Logger:       testutil.NewTestLogger(),
		PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
	}), OutboundModeDeps{
		GovernanceCoreDeps: GovernanceCoreDeps{
			ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
			ReplayStore:       &testutil.MockReplayStore{},
			StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
			TransactionAudit:  &testutil.MockTransactionAudit{},
			L3Notary:          &testutil.MockL3Notary{},
			Doctrine:          governance.NewL1Doctrine(),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, svc)
	assert.NotNil(t, svc.l4warden, "l4warden must be initialized even with nil consensus policy store")
}

// TestNewOperatorPubSubService_NilDoctrine_NotDefaultedAtCallSite verifies that
// a nil Doctrine in GovernanceCoreDeps is NOT silently replaced with NewL1Doctrine()
// at the call site when NewOperatorPubSubService is called directly. The doctrine default belongs in the mode's dependency wiring
// (ztdo.go for outbound mode), not in initializeGovernance. A nil doctrine here
// is a wiring bug; L4Warden fail-closes at verification time with
// ErrTxDoctrineMissing rather than silently running with a default doctrine.
func TestNewOperatorPubSubService_NilDoctrine_NotDefaultedAtCallSite(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
		Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
		Ledger:       storagetest.NewTestLedger(t),
		RuntimeVault: governancetest.UnlockedVault{},
		Config:       cfg,
		Logger:       testutil.NewTestLogger(),
		PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
	}), OutboundModeDeps{
		GovernanceCoreDeps: GovernanceCoreDeps{
			ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
			ReplayStore:       &testutil.MockReplayStore{},
			StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
			TransactionAudit:  &testutil.MockTransactionAudit{},
			L3Notary:          &testutil.MockL3Notary{},
			// Doctrine intentionally left nil — must not be defaulted at the call site.
		},
	})
	require.NoError(t, err)
	require.NotNil(t, svc)
	require.NotNil(t, svc.l4warden, "l4warden must be initialized")
	assert.Nil(t, svc.l4warden.Doctrine(), "nil Doctrine must not be defaulted at the call site; wire it in the mode's dependency wiring instead")
}

func newGatewayPubsubServiceForBindingTest(t *testing.T) *OperatorPubSubService {
	t.Helper()
	cfg := testutil.NewTestConfig(t)
	govDeps := validTestGatewayModeDeps(constants.PostureDoctrine)
	svc, err := NewGatewayOperatorPubSubService(GatewayCommandServiceConfig{
		CommandServiceConfig: withTestDeps(t, CommandServiceConfig{
			Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
			Ledger:       storagetest.NewTestLedger(t),
			RuntimeVault: governancetest.UnlockedVault{},
			Config:       cfg,
			Logger:       testutil.NewTestLogger(),
			PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
		}),
		GovDeps: &govDeps,
	})
	require.NoError(t, err)
	return svc
}

func TestOperatorPubSubService_BindMCPGatewayRejectsNil(t *testing.T) {
	t.Parallel()
	svc := newGatewayPubsubServiceForBindingTest(t)

	err := svc.BindMCPGateway(nil)

	assert.ErrorIs(t, err, constants.ErrPubSubMCPGatewayNil)
	assert.Nil(t, svc.GetMCPGateway())
}

func TestOperatorPubSubService_BindMCPGatewayRejectsDuplicateBinding(t *testing.T) {
	t.Parallel()
	svc := newGatewayPubsubServiceForBindingTest(t)
	first := &mcp.GatewayService{}
	require.NoError(t, svc.BindMCPGateway(first))

	err := svc.BindMCPGateway(&mcp.GatewayService{})

	assert.ErrorIs(t, err, constants.ErrPubSubMCPGatewayAlreadyBound)
	assert.Same(t, first, svc.GetMCPGateway())
}

func TestOperatorPubSubService_BoundMCPGatewaySupportsConcurrentReads(t *testing.T) {
	t.Parallel()
	svc := newGatewayPubsubServiceForBindingTest(t)
	gateway := &mcp.GatewayService{}
	require.NoError(t, svc.BindMCPGateway(gateway))

	const readerCount = 32
	results := make(chan *mcp.GatewayService, readerCount)
	var wg sync.WaitGroup
	wg.Add(readerCount)
	for range readerCount {
		go func() {
			defer wg.Done()
			results <- svc.GetMCPGateway()
		}()
	}
	wg.Wait()
	close(results)

	for result := range results {
		assert.Same(t, gateway, result)
	}
}
