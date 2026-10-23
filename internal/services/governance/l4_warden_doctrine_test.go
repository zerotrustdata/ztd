// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TestL4Warden_Doctrine_ValidNonMutationPasses verifies that a valid
// non-mutation envelope passes under doctrine posture.
func TestL4Warden_Doctrine_ValidNonMutationPasses(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "doctrine")

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	if err != nil {
		t.Fatalf("expected verification to pass, got %v", err)
	}
	if verified.DecodedPayload == nil || verified.ActionType != constants.ActionTypeFsList {
		t.Fatalf("verified transaction missing decoded payload or action: %#v", verified)
	}
}

// TestL4Warden_Doctrine_ValidMutationPassesWithoutL2L3 verifies that a
// mutation envelope passes under doctrine even without L2 and L3 proofs,
// since doctrine only enforces L1 (L2/L3 are audited but do not gate).
func TestL4Warden_Doctrine_ValidMutationPassesWithoutL2L3(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "doctrine")

	env.Governance.L3 = nil
	env.Governance.L2 = nil
	rehash(t, env)

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	if err != nil {
		t.Fatalf("expected mutation to pass under doctrine without L2/L3, got %v", err)
	}
	if verified.L2Status != operatorv1.L2Status_L2_STATUS_NOT_REQUIRED {
		t.Fatalf("expected L2 status NOT_REQUIRED under doctrine with no L2 proof, got %s", verified.L2Status)
	}
	if verified.L3Status != operatorv1.L3Status_L3_STATUS_NOT_REQUIRED {
		t.Fatalf("expected L3 status NOT_REQUIRED under doctrine with no L3 proof, got %s", verified.L3Status)
	}
}

// TestL4Warden_Doctrine_AllActionTypesFromSSOT verifies that every action type
// from the SSOT can be decoded and verified under doctrine posture.
func TestL4Warden_Doctrine_AllActionTypesFromSSOT(t *testing.T) {
	t.Parallel()
	allActionTypes := constants.AllActionTypes
	if len(allActionTypes) == 0 {
		t.Fatal("AllActionTypes() returned empty list")
	}

	for _, actionType := range allActionTypes {
		t.Run(string(actionType), func(t *testing.T) {
			t.Parallel()
			if _, err := constants.RequestEventForAction(actionType); err != nil {
				t.Skipf("no governed request event for %s", actionType)
			}
			verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			payload := typedPayload(t, actionType)
			env := signedEnvelope(t, actionType, payload, privKey, "doctrine")

			verified, err := verifier.VerifyEnvelope(context.Background(), env)
			if err != nil {
				t.Fatalf("verification failed for action type %s: %v", actionType, err)
			}
			if verified == nil {
				t.Fatalf("verified transaction is nil for action type %s", actionType)
				return
			}
			if verified.ActionType != actionType {
				t.Fatalf("action type mismatch: expected %s, got %s", actionType, verified.ActionType)
			}
		})
	}
}

// TestL4Warden_Doctrine_MissingL2DoesNotReject verifies that missing L2
// votes do not reject an envelope under doctrine (L2 is audited, not enforced).
func TestL4Warden_Doctrine_MissingL2DoesNotReject(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "doctrine")

	env.Governance.L2 = nil

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	if err != nil {
		t.Fatalf("expected non-mutation to pass under doctrine without L2, got %v", err)
	}
	if verified.L2Status != operatorv1.L2Status_L2_STATUS_NOT_REQUIRED {
		t.Fatalf("expected L2 status NOT_REQUIRED with no L2 proof, got %s", verified.L2Status)
	}
}

// TestL4Warden_Doctrine_MissingL3DoesNotReject verifies that missing L3
// proof does not reject a mutation under doctrine (L3 is audited, not enforced).
func TestL4Warden_Doctrine_MissingL3DoesNotReject(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "doctrine")

	env.Governance.L3 = nil

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	if err != nil {
		t.Fatalf("expected mutation to pass under doctrine without L3, got %v", err)
	}
	if verified.L3Status != operatorv1.L3Status_L3_STATUS_NOT_REQUIRED {
		t.Fatalf("expected L3 status NOT_REQUIRED with no L3 proof, got %s", verified.L3Status)
	}
}

// TestL4Warden_Doctrine_ReplayAndStateRootReject verifies that replay
// attacks and state root mismatches are rejected under doctrine posture.
func TestL4Warden_Doctrine_ReplayAndStateRootReject(t *testing.T) {
	t.Parallel()
	t.Run("replayed nonce", func(t *testing.T) {
		t.Parallel()
		replayStore := testutil.NewStatefulMockReplayStore()
		verifier, privKey := createStrictVerifier(t, replayStore, testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "doctrine")
		if _, err := verifier.VerifyEnvelope(context.Background(), env); err != nil {
			t.Fatalf("first verification failed: %v", err)
		}
		_, err := verifier.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrTransactionReplay) {
			t.Fatalf("expected replay rejection, got %v", err)
		}
	})

	t.Run("state root mismatch", func(t *testing.T) {
		t.Parallel()
		verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("other-root"), testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "doctrine")
		rejected, err := verifier.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrStateRootMismatch) {
			t.Fatalf("expected state root mismatch, got %v", err)
		}
		require.NotNil(t, rejected)
		require.Len(t, rejected.DeterministicStageEvidence, 2)
		assert.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE, rejected.DeterministicStageEvidence[0].Kind)
		assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, rejected.DeterministicStageEvidence[0].Outcome)
		assert.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION, rejected.DeterministicStageEvidence[1].Kind)
		assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED, rejected.DeterministicStageEvidence[1].Outcome)
	})
}

// TestL4Warden_Doctrine_MissingVerifierDependenciesReject verifies that
// missing critical verifier dependencies are rejected under doctrine posture.
func TestL4Warden_Doctrine_MissingVerifierDependenciesReject(t *testing.T) {
	t.Parallel()
	t.Run("missing replay store", func(t *testing.T) {
		t.Parallel()
		verifier, privKey := createStrictVerifier(t, nil, testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "doctrine")
		_, err := verifier.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrReplayStoreMissing) {
			t.Fatalf("expected replay store rejection, got %v", err)
		}
	})

	t.Run("missing state root provider", func(t *testing.T) {
		t.Parallel()
		verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), nil, testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "doctrine")
		_, err := verifier.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrStateRootMissing) {
			t.Fatalf("expected state root provider rejection, got %v", err)
		}
	})
}

func TestL4WardenVerifyEnvelopeProducesAuthoritativeDeterministicStageEvidence(t *testing.T) {
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	envelope := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "doctrine")
	envelope.Governance.L2 = nil
	envelope.Governance.L3 = nil
	envelope.CaseId = "case-1"
	envelope.InvestigationId = "investigation-1"
	envelope.TaskId = "task-1"
	rehash(t, envelope)

	verified, err := verifier.VerifyEnvelope(context.Background(), envelope)
	require.NoError(t, err)
	require.Len(t, verified.DeterministicStageEvidence, 4)

	expected := []struct {
		kind    operatorv1.DeterministicStageKind
		outcome operatorv1.DeterministicStageOutcome
	}{
		{operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED},
		{operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_PROTOCOL_L2, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED},
		{operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L3_NOTARY, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED},
		{operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED},
	}
	l1Stage := verified.DeterministicStageEvidence[0]
	assert.NotEmpty(t, l1Stage.DoctrineBundleHash)
	assert.NotEmpty(t, l1Stage.DoctrineBundleVersion)

	l4StageID := verified.DeterministicStageEvidence[3].StageId
	for index, want := range expected {
		stage := verified.DeterministicStageEvidence[index]
		assert.Equal(t, want.kind, stage.Kind)
		assert.Equal(t, want.outcome, stage.Outcome)
		assert.Equal(t, envelope.Id, stage.TransactionId)
		assert.Equal(t, envelope.TransactionHash, stage.TransactionHash)
		assert.Equal(t, envelope.CaseId, stage.CaseId)
		assert.Equal(t, envelope.InvestigationId, stage.InvestigationId)
		assert.Equal(t, envelope.TaskId, stage.TaskId)
		assert.Equal(t, "ztd-operator-process", stage.ClockDomain)
		assert.Equal(t, "go_monotonic", stage.TimingSource)
		assert.GreaterOrEqual(t, stage.MonotonicEndNs, stage.MonotonicStartNs)
		if want.kind != operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION {
			assert.Equal(t, l4StageID, stage.ParentStageId)
		}
	}
}

// TestL4Warden_Doctrine_ForbiddenPayloadRejectedByL1 verifies that a payload
// matching a doctrine forbidden pattern is rejected at L1 with
// ErrTxL1ValidationFailed even though hash, nonce, expiry, and posture proofs
// are otherwise valid. The L4 warden has no execution handler wired, and a
// rejected verification must not yield a decoded payload for dispatch.
func TestL4Warden_Doctrine_ForbiddenPayloadRejectedByL1(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	forbidden, err := proto.Marshal(&operatorv1.CommandRequested{Command: "sudo rm -rf /", ExecutionId: "exec-1", Justification: "test"})
	require.NoError(t, err)
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, forbidden, privKey, "doctrine")

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrTxL1ValidationFailed)
	require.NotNil(t, verified)
	assert.Nil(t, verified.DecodedPayload, "rejected envelope must not carry a decoded payload for dispatch")
	require.NotEmpty(t, verified.DeterministicStageEvidence)
	last := verified.DeterministicStageEvidence[len(verified.DeterministicStageEvidence)-1]
	assert.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION, last.Kind)
	assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED, last.Outcome)
}

// requireL1StageOutcome asserts the L1_DOCTRINE stage exists at index 0, carries
// the doctrine bundle identity, and binds to the L4 stage as its parent.
func requireL1StageOutcome(t *testing.T, stages []*operatorv1.DeterministicStageEvidence, want operatorv1.DeterministicStageOutcome) {
	t.Helper()
	require.NotEmpty(t, stages)
	l1 := stages[0]
	assert.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE, l1.Kind)
	assert.Equal(t, want, l1.Outcome)
	assert.NotEmpty(t, l1.DoctrineBundleHash, "L1 stage must carry the doctrine bundle hash")
	assert.NotEmpty(t, l1.DoctrineBundleVersion, "L1 stage must carry the doctrine bundle version")
}

// requireL4FailedLast asserts the final stage is the L4 FAILED stage and that
// every prior stage declares it as parent.
func requireL4FailedLast(t *testing.T, stages []*operatorv1.DeterministicStageEvidence) {
	t.Helper()
	require.NotEmpty(t, stages)
	l4 := stages[len(stages)-1]
	assert.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION, l4.Kind)
	assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED, l4.Outcome)
	for _, stage := range stages[:len(stages)-1] {
		assert.Equal(t, l4.StageId, stage.ParentStageId, "stage %s must bind to the L4 stage", stage.Kind)
	}
}

// TestL4Warden_Doctrine_L1ViolationEmitsFailedL1Stage verifies that a payload
// doctrine actually rejects produces an L1_DOCTRINE FAILED stage followed by the
// L4 FAILED stage.
func TestL4Warden_Doctrine_L1ViolationEmitsFailedL1Stage(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	forbidden, err := proto.Marshal(&operatorv1.CommandRequested{Command: "sudo rm -rf /", ExecutionId: "exec-1", Justification: "test"})
	require.NoError(t, err)
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, forbidden, privKey, "doctrine")

	rejected, err := verifier.VerifyEnvelope(context.Background(), env)
	require.ErrorIs(t, err, constants.ErrTxL1ValidationFailed)
	require.NotNil(t, rejected)
	require.Len(t, rejected.DeterministicStageEvidence, 2)
	requireL1StageOutcome(t, rejected.DeterministicStageEvidence, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED)
	requireL4FailedLast(t, rejected.DeterministicStageEvidence)
}

// TestL4Warden_Doctrine_IdentityFailureAfterDoctrinePassEmitsVerifiedL1Stage
// verifies that when doctrine passes and the hash/id binding then fails, the
// L1 stage truthfully records VERIFIED and the L4 stage records FAILED.
func TestL4Warden_Doctrine_IdentityFailureAfterDoctrinePassEmitsVerifiedL1Stage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(env *govtypes.GovernanceEnvelope)
		wantErr error
	}{
		{
			name:    "hash mismatch",
			mutate:  func(env *govtypes.GovernanceEnvelope) { env.TransactionHash = "tampered-hash" },
			wantErr: constants.ErrTxTransactionHashMismatch,
		},
		{
			name:    "id mismatch",
			mutate:  func(env *govtypes.GovernanceEnvelope) { env.Id = "tampered-id" },
			wantErr: constants.ErrTxTransactionIDMismatch,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "doctrine")
			tc.mutate(env)

			rejected, err := verifier.VerifyEnvelope(context.Background(), env)
			require.ErrorIs(t, err, tc.wantErr)
			require.NotNil(t, rejected)
			require.Len(t, rejected.DeterministicStageEvidence, 2)
			requireL1StageOutcome(t, rejected.DeterministicStageEvidence, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED)
			requireL4FailedLast(t, rejected.DeterministicStageEvidence)
		})
	}
}
