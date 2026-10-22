// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package governance

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestGovernanceFlow tests the full governance flow from envelope creation
// through L5Actuator execution.
func TestGovernanceFlow(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	nodeID := "test-node-1"

	actuator, _ := newCommitmentTestActuator(t)

	env := &govtypes.GovernanceEnvelope{
		ProtocolVersion:   govtypes.GovernanceProtocolVersionV2,
		OperatorId:        "agent-1",
		OperatorSessionId: "test-operator-session",
		Timestamp:         timestamppb.Now(),
		EventType:         string(constants.Event.Operator.FetchLogs.Requested),
		ActionType:        string(constants.ActionTypeFetchLogs),
		TargetResource:    "localhost",
		Payload:           []byte("fetch logs"),
	}

	// 1. Generate Message ID
	id, _ := govtypes.GenerateMessageID(env)
	env.Id = id

	// 2. Set governance metadata (L1 validated, L2 vote pre-populated)
	env.Governance = &commonv1.GovernanceMetadata{
		L1: &commonv1.L1Metadata{Validated: true},
		L2: &commonv1.L2Metadata{
			ConsensusSetId: "test-consensus",
			Votes: []*commonv1.L2Vote{
				{
					SignerKeyId:        nodeID,
					ConsensusSignature: "test-sig",
					Decision:           true,
				},
			},
		},
	}

	handler := &mockExecutionHandler{}
	actuator.ExecutionHandler = handler
	actuator.SigningKey = priv
	actuator.KeyID = nodeID

	vt := &VerifiedTransaction{
		Envelope:   env,
		ActionType: constants.ActionTypeFetchLogs,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	// 3. L5Actuator Execution
	eventReceipt, err := actuator.Execute(context.Background(), vt, nil)
	if err != nil {
		t.Fatalf("L5Actuator execution failed: %v", err)
	}

	if !handler.executed {
		t.Error("Expected handler to be executed")
	}

	if eventReceipt.TransactionId != env.Id {
		t.Errorf("Expected event receipt tx id %s, got %s", env.Id, eventReceipt.TransactionId)
	}
}

// TestGovernanceFailClosed tests that the L4 Warden fails closed when
// critical components are missing or misconfigured. Each subtest verifies
// a specific fail-closed path by calling VerifyEnvelope with a valid
// envelope and asserting the correct typed error is returned.
func TestGovernanceFailClosed(t *testing.T) {
	buildValidEnvelope := func(t *testing.T, actionType constants.ActionType, posture constants.Posture) *govtypes.GovernanceEnvelope {
		t.Helper()
		_, privKey, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatalf("failed to generate key: %v", err)
		}
		return signedEnvelope(t, actionType, typedPayload(t, actionType), privKey, posture)
	}

	makeWarden := func(replayStore ReplayStore, stateRootProvider StateRootProvider, consensusPolicyStore L2ConsensusPolicyStore, doctrine *L1Doctrine) *L4Warden {
		return NewL4Warden(
			testutil.NewTestLogger(),
			replayStore,
			stateRootProvider,
			&FailClosedSignerStore{Signers: map[string]ed25519.PublicKey{}},
			consensusPolicyStore,
			nil, // L3Notary
			doctrine,
			constants.AllActionTypes,
			nil, // Clock defaults to RealClock
			&testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "operator-session-1"},
			&governancetest.UnlockedVault{},
		)
	}

	t.Run("NilReplayStore_FailClosed", func(t *testing.T) {
		warden := makeWarden(nil, &governancetest.SimpleStateRootProvider{Root: "root-1"}, &NoopConsensusPolicyStore{}, NewL1Doctrine())
		env := buildValidEnvelope(t, constants.ActionTypeFsList, "doctrine")
		_, err := warden.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrReplayStoreMissing) {
			t.Fatalf("expected ErrReplayStoreMissing, got %v", err)
		}
	})

	t.Run("NilStateRootProvider_FailClosed", func(t *testing.T) {
		warden := makeWarden(testutil.NewStatefulMockReplayStore(), nil, &NoopConsensusPolicyStore{}, NewL1Doctrine())
		env := buildValidEnvelope(t, constants.ActionTypeFsList, "doctrine")
		_, err := warden.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrStateRootMissing) {
			t.Fatalf("expected ErrStateRootMissing, got %v", err)
		}
	})

	t.Run("EmptyStateRoot_FailClosed", func(t *testing.T) {
		warden := makeWarden(testutil.NewStatefulMockReplayStore(), &governancetest.SimpleStateRootProvider{Root: ""}, &NoopConsensusPolicyStore{}, NewL1Doctrine())
		env := buildValidEnvelope(t, constants.ActionTypeFsList, "doctrine")
		_, err := warden.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, constants.ErrTxProviderMisconfigured) {
			t.Fatalf("expected ErrTxProviderMisconfigured, got %v", err)
		}
	})

	t.Run("NilDoctrine_FailClosed", func(t *testing.T) {
		warden := makeWarden(testutil.NewStatefulMockReplayStore(), &governancetest.SimpleStateRootProvider{Root: "root-1"}, &NoopConsensusPolicyStore{}, nil)
		env := buildValidEnvelope(t, constants.ActionTypeFsList, "doctrine")
		_, err := warden.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrDoctrineMissing) {
			t.Fatalf("expected ErrDoctrineMissing, got %v", err)
		}
	})

	t.Run("NoopConsensusStore_ConsensusFailClosed", func(t *testing.T) {
		warden := makeWarden(testutil.NewStatefulMockReplayStore(), &governancetest.SimpleStateRootProvider{Root: "root-1"}, &NoopConsensusPolicyStore{}, NewL1Doctrine())
		env := buildValidEnvelope(t, constants.ActionTypeFileEdit, "consensus")
		_, err := warden.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrL2ConsensusSetUnknown) {
			t.Fatalf("expected ErrL2ConsensusSetUnknown, got %v", err)
		}
	})
}
