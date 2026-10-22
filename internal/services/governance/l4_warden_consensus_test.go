// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/stretchr/testify/assert"
)

// TestL4Warden_Consensus_ValidNonMutationPasses verifies that a valid
// non-mutation envelope with L2 passes under consensus posture.
func TestL4Warden_Consensus_ValidNonMutationPasses(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "consensus")

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	if err != nil {
		t.Fatalf("expected verification to pass, got %v", err)
	}
	if verified.L2Status != operatorv1.L2Status_L2_STATUS_REQUIRED_VALID {
		t.Fatalf("expected L2 status REQUIRED_VALID under consensus with valid L2, got %s", verified.L2Status)
	}
}

// TestL4Warden_Consensus_ValidMutationPassesWithoutL3 verifies that a
// mutation envelope passes under consensus without L3 proof, since
// consensus enforces L1+L2 but audits L3 only.
func TestL4Warden_Consensus_ValidMutationPassesWithoutL3(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "consensus")

	env.Governance.L3 = nil

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	if err != nil {
		t.Fatalf("expected mutation to pass under consensus without L3, got %v", err)
	}
	if verified.L3Status != operatorv1.L3Status_L3_STATUS_NOT_REQUIRED {
		t.Fatalf("expected L3 status NOT_REQUIRED with no L3 proof under consensus, got %s", verified.L3Status)
	}
}

// TestL4Warden_Consensus_AllActionTypesFromSSOT verifies that every action type
// from the SSOT can be decoded and verified under consensus posture.
func TestL4Warden_Consensus_AllActionTypesFromSSOT(t *testing.T) {
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
			env := signedEnvelope(t, actionType, payload, privKey, "consensus")

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

// TestL4Warden_Consensus_MissingL2Rejects verifies that missing L2 votes
// reject an envelope under consensus (L2 is enforced).
func TestL4Warden_Consensus_MissingL2Rejects(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "consensus")

	env.Governance.L2 = nil

	_, err := verifier.VerifyEnvelope(context.Background(), env)
	if !errors.Is(err, ErrL2SignatureMissing) {
		t.Fatalf("expected ErrL2SignatureMissing under consensus with no L2, got %v", err)
	}
}

// TestL4Warden_Consensus_BootstrapActionPassesWithoutL2 verifies that
// platform enrollment (bootstrap) actions pass under consensus posture
// without L2 votes. Bootstrap actions bring the consensus tribunal into
// existence and predate its ability to sign, so they are exempt from the
// L2 enforcement gate. L1 doctrine and stateful checks still apply.
func TestL4Warden_Consensus_BootstrapActionPassesWithoutL2(t *testing.T) {
	t.Parallel()

	bootstrapActions := []constants.ActionType{
		constants.ActionTypePlatformEnrollmentCreate,
		constants.ActionTypePlatformEnrollmentDecide,
		constants.ActionTypePlatformEnrollmentIssue,
		constants.ActionTypePlatformEnrollmentPersistPolicy,
		constants.ActionTypePlatformEnrollmentCreateSession,
		constants.ActionTypePlatformEnrollmentRevoke,
	}

	for _, actionType := range bootstrapActions {

		t.Run(string(actionType), func(t *testing.T) {
			t.Parallel()
			verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			env := signedEnvelope(t, actionType, typedPayload(t, actionType), privKey, "consensus")

			// Strip L2 votes to simulate bootstrap before the tribunal is available.
			env.Governance.L2 = nil
			// Bootstrap mutation actions also lack L3 proofs at bootstrap time.
			env.Governance.L3 = nil
			rehash(t, env)

			verified, err := verifier.VerifyEnvelope(context.Background(), env)
			if err != nil {
				t.Fatalf("expected bootstrap action %s to pass under consensus without L2, got %v", actionType, err)
			}
			if verified == nil {
				t.Fatalf("verified transaction is nil for bootstrap action %s", actionType)
			}
			if verified.L2Status != operatorv1.L2Status_L2_STATUS_NOT_REQUIRED {
				t.Fatalf("expected L2 status NOT_REQUIRED for bootstrap action %s with no L2 votes, got %s", actionType, verified.L2Status)
			}
		})
	}
}

// TestL4Warden_Consensus_BootstrapActionWithInvalidL2StillPasses verifies
// that a bootstrap action with invalid L2 votes (e.g., unconfigured consensus
// policy) still passes under consensus posture. L2 votes on bootstrap actions
// are audit evidence, not an enforcement gate.
func TestL4Warden_Consensus_BootstrapActionWithInvalidL2StillPasses(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypePlatformEnrollmentDecide, typedPayload(t, constants.ActionTypePlatformEnrollmentDecide), privKey, "consensus")

	// Point L2 at a nonexistent consensus set so verification would fail
	// for a non-bootstrap action.
	env.Governance.L2.ConsensusSetId = "nonexistent-consensus"
	env.Governance.L3 = nil
	rehash(t, env)

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	if err != nil {
		t.Fatalf("expected bootstrap action to pass even with invalid L2, got %v", err)
	}
	if verified == nil {
		t.Fatalf("verified transaction is nil")
	}
	if verified.L2Status != operatorv1.L2Status_L2_STATUS_NOT_REQUIRED {
		t.Fatalf("expected L2 status NOT_REQUIRED for bootstrap action with invalid L2 votes, got %s", verified.L2Status)
	}
}

// TestL4Warden_Consensus_MissingL3DoesNotReject verifies that missing L3
// proof does not reject a mutation under consensus (L3 is audited, not enforced).
func TestL4Warden_Consensus_MissingL3DoesNotReject(t *testing.T) {
	t.Parallel()
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "consensus")

	env.Governance.L3 = nil

	_, err := verifier.VerifyEnvelope(context.Background(), env)
	if err != nil {
		t.Fatalf("expected mutation to pass under consensus without L3, got %v", err)
	}
}

// TestL4Warden_Consensus_ReplayAndStateRootReject verifies that replay
// attacks and state root mismatches are rejected under consensus posture.
func TestL4Warden_Consensus_ReplayAndStateRootReject(t *testing.T) {
	t.Parallel()
	t.Run("replayed nonce", func(t *testing.T) {
		t.Parallel()
		replayStore := testutil.NewStatefulMockReplayStore()
		verifier, privKey := createStrictVerifier(t, replayStore, testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "consensus")
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
		env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "consensus")
		_, err := verifier.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrStateRootMismatch) {
			t.Fatalf("expected state root mismatch, got %v", err)
		}
	})
}

// TestL4Warden_Consensus_MissingVerifierDependenciesReject verifies that
// missing critical verifier dependencies are rejected under consensus posture.
func TestL4Warden_Consensus_MissingVerifierDependenciesReject(t *testing.T) {
	t.Parallel()
	t.Run("missing replay store", func(t *testing.T) {
		t.Parallel()
		verifier, privKey := createStrictVerifier(t, nil, testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "consensus")
		_, err := verifier.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrReplayStoreMissing) {
			t.Fatalf("expected replay store rejection, got %v", err)
		}
	})

	t.Run("missing state root provider", func(t *testing.T) {
		t.Parallel()
		verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), nil, testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "consensus")
		_, err := verifier.VerifyEnvelope(context.Background(), env)
		if !errors.Is(err, ErrStateRootMissing) {
			t.Fatalf("expected state root provider rejection, got %v", err)
		}
	})
}

// TestL4Warden_L2VerifiesAgainstEnrollmentBundleTrust verifies the outbound
// Operator wiring: one L2TrustStore built from the enrollment bundle serves
// both the signer keys and the consensus policy, and L2 quorum is enforced
// against exactly what the bundle delivered.
func TestL4Warden_L2VerifiesAgainstEnrollmentBundleTrust(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	bundleTrust := func(signers []models.OperatorSigner) *models.OperatorL2Trust {
		return &models.OperatorL2Trust{
			ConsensusSetID:  "test-consensus",
			MemberKeyIDs:    []string{"test-key"},
			Quorum:          1,
			RequireDistinct: true,
			Enabled:         true,
			Signers:         signers,
		}
	}

	tests := []struct {
		name    string
		trust   *models.OperatorL2Trust
		wantErr error
	}{
		{name: "bundle signer meets quorum", trust: bundleTrust([]models.OperatorSigner{{KeyID: "test-key", PublicKeyHex: hex.EncodeToString(pub)}})},
		{name: "member key absent from bundle", trust: bundleTrust(nil), wantErr: ErrL2SignatureInvalid},
		{name: "no consensus set in bundle", trust: nil, wantErr: ErrL2ConsensusSetUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := NewL2TrustStore(tc.trust)
			if err != nil {
				t.Fatalf("NewL2TrustStore: %v", err)
			}
			warden := NewL4Warden(
				testutil.NewTestLogger(),
				testutil.NewStatefulMockReplayStore(),
				testutil.NewMockStateRootProvider("root-1"),
				store,
				store,
				testutil.NewConfigurableMockL3Notary(true),
				NewL1Doctrine(),
				constants.AllActionTypes,
				nil,
				&testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "operator-session-1"},
				&governancetest.UnlockedVault{},
			)
			env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), priv, "consensus")

			verified, err := warden.VerifyEnvelope(context.Background(), env)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected verification to pass, got %v", err)
			}
			assert.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, verified.L2Status)
		})
	}
}

// TestL4Warden_L2QuorumVerification verifies L2 quorum mechanics under
// consensus posture (L2 is enforced).
func TestL4Warden_L2QuorumVerification(t *testing.T) {
	t.Parallel()

	pub1, priv1, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	pub2, priv2, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	allSigners := map[string]ed25519.PublicKey{
		"member-1": pub1,
		"member-2": pub2,
	}
	partialSigners := map[string]ed25519.PublicKey{
		"member-1": pub1,
	}

	enabledConsensus2of2 := &models.ConsensusPolicy{
		ID:              "consensus-1",
		MemberAppIDs:    []string{"member-1", "member-2"},
		Quorum:          2,
		RequireDistinct: true,
		Enabled:         true,
	}
	enabledConsensus1of2 := &models.ConsensusPolicy{
		ID:              "consensus-2",
		MemberAppIDs:    []string{"member-1", "member-2"},
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
	}
	disabledConsensus := &models.ConsensusPolicy{
		ID:              "consensus-disabled",
		MemberAppIDs:    []string{"member-1", "member-2"},
		Quorum:          2,
		RequireDistinct: true,
		Enabled:         false,
	}

	payload := typedPayload(t, constants.ActionTypeExecuteBash)

	buildEnv := func(nonceTag, consensusID string, votes []*commonv1.L2Vote) *govtypes.GovernanceEnvelope {
		nonceSuffix := hex.EncodeToString(payload)
		if len(nonceSuffix) > 8 {
			nonceSuffix = nonceSuffix[:8]
		}
		eventType, err := constants.RequestEventForAction(constants.ActionTypeExecuteBash)
		if err != nil {
			t.Fatalf("failed to resolve request event: %v", err)
		}
		env := &govtypes.GovernanceEnvelope{
			ProtocolVersion:   govtypes.GovernanceProtocolVersionV2,
			Timestamp:         timestamppb.Now(),
			ExpiresAt:         timestamppb.New(time.Now().UTC().Add(time.Hour)),
			SourceComponent:   commonv1.Component_COMPONENT_CLIENT,
			OperatorId:        "operator-1",
			OperatorSessionId: "operator-session-1",
			EventType:         string(eventType),
			ActionType:        string(constants.ActionTypeExecuteBash),
			TargetResource:    "localhost",
			Payload:           payload,
			StateMerkleRoot:   "root-1",
			Nonce:             "nonce-quorum-" + nonceTag + "-" + nonceSuffix,
			Posture:           "consensus",
		}
		hash, err := govtypes.GenerateMessageID(env)
		if err != nil {
			t.Fatalf("failed to generate hash: %v", err)
		}
		env.Id = hash
		env.TransactionHash = hash
		env.Governance = &commonv1.GovernanceMetadata{
			L2: &commonv1.L2Metadata{
				ConsensusSetId: consensusID,
				Votes:          votes,
			},
		}
		return env
	}

	makeVerifier := func(signers map[string]ed25519.PublicKey, consensus map[string]*models.ConsensusPolicy) *L4Warden {
		return NewL4Warden(
			testutil.NewTestLogger(),
			testutil.NewStatefulMockReplayStore(),
			testutil.NewMockStateRootProvider("root-1"),
			&FailClosedSignerStore{Signers: signers},
			&consensusStoreTestAdapter{Inner: &governancetest.SimpleConsensusStore{Consensus: consensus}},
			testutil.NewConfigurableMockL3Notary(true),
			NewL1Doctrine(),
			constants.AllActionTypes,
			nil,
			&testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "operator-session-1"},
			&governancetest.UnlockedVault{},
		)
	}

	tests := []struct {
		name     string
		verifier *L4Warden
		env      *govtypes.GovernanceEnvelope
		wantErr  error
		wantL2   operatorv1.L2Status
	}{
		{
			name:     "2-of-2 quorum pass",
			verifier: makeVerifier(allSigners, map[string]*models.ConsensusPolicy{"consensus-1": enabledConsensus2of2}),
			env: func() *govtypes.GovernanceEnvelope {
				env := buildEnv("2of2pass", "consensus-1", nil)
				hash := env.TransactionHash
				env.Governance.L2.Votes = []*commonv1.L2Vote{
					signL2Vote(priv1, "member-1", hash, true),
					signL2Vote(priv2, "member-2", hash, true),
				}
				return env
			}(),
			wantErr: nil,
			wantL2:  operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
		},
		{
			name:     "1 valid of quorum-2 fails",
			verifier: makeVerifier(allSigners, map[string]*models.ConsensusPolicy{"consensus-1": enabledConsensus2of2}),
			env: func() *govtypes.GovernanceEnvelope {
				env := buildEnv("1of2fail", "consensus-1", nil)
				hash := env.TransactionHash
				env.Governance.L2.Votes = []*commonv1.L2Vote{
					signL2Vote(priv1, "member-1", hash, true),
				}
				return env
			}(),
			wantErr: ErrL2QuorumNotMet,
			wantL2:  operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		},
		{
			name:     "duplicate signer with require_distinct",
			verifier: makeVerifier(allSigners, map[string]*models.ConsensusPolicy{"consensus-1": enabledConsensus2of2}),
			env: func() *govtypes.GovernanceEnvelope {
				env := buildEnv("dupsign", "consensus-1", nil)
				hash := env.TransactionHash
				env.Governance.L2.Votes = []*commonv1.L2Vote{
					signL2Vote(priv1, "member-1", hash, true),
					signL2Vote(priv1, "member-1", hash, true),
				}
				return env
			}(),
			wantErr: ErrL2DuplicateSigner,
			wantL2:  operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		},
		{
			name:     "false vote does not count toward quorum",
			verifier: makeVerifier(allSigners, map[string]*models.ConsensusPolicy{"consensus-1": enabledConsensus2of2}),
			env: func() *govtypes.GovernanceEnvelope {
				env := buildEnv("falsevote", "consensus-1", nil)
				hash := env.TransactionHash
				env.Governance.L2.Votes = []*commonv1.L2Vote{
					signL2Vote(priv1, "member-1", hash, true),
					signL2Vote(priv2, "member-2", hash, false),
				}
				return env
			}(),
			wantErr: ErrL2QuorumNotMet,
			wantL2:  operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		},
		{
			// INV-CON-WARD-06: an enrolled member whose trusted signer cannot be
			// resolved fails closed even though member-1 alone meets quorum.
			name:     "member with unresolvable signer rejects despite quorum",
			verifier: makeVerifier(partialSigners, map[string]*models.ConsensusPolicy{"consensus-2": enabledConsensus1of2}),
			env: func() *govtypes.GovernanceEnvelope {
				env := buildEnv("unknownsigner", "consensus-2", nil)
				hash := env.TransactionHash
				env.Governance.L2.Votes = []*commonv1.L2Vote{
					signL2Vote(priv1, "member-1", hash, true),
					signL2Vote(priv2, "member-2", hash, true),
				}
				return env
			}(),
			wantErr: ErrL2SignatureInvalid,
			wantL2:  operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		},
		{
			name:     "empty votes for a known set cannot meet quorum",
			verifier: makeVerifier(allSigners, map[string]*models.ConsensusPolicy{"consensus-1": enabledConsensus2of2}),
			env:      buildEnv("emptyvotes", "consensus-1", []*commonv1.L2Vote{}),
			wantErr:  ErrL2QuorumNotMet,
			wantL2:   operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		},
		{
			name:     "empty L2 metadata is a missing proof",
			verifier: makeVerifier(allSigners, map[string]*models.ConsensusPolicy{"consensus-1": enabledConsensus2of2}),
			env:      buildEnv("emptymetadata", "", nil),
			wantErr:  ErrL2SignatureMissing,
			wantL2:   operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		},
		{
			name:     "disabled consensus policy",
			verifier: makeVerifier(allSigners, map[string]*models.ConsensusPolicy{"consensus-disabled": disabledConsensus}),
			env: func() *govtypes.GovernanceEnvelope {
				env := buildEnv("disabled-consensus", "consensus-disabled", nil)
				hash := env.TransactionHash
				env.Governance.L2.Votes = []*commonv1.L2Vote{
					signL2Vote(priv1, "member-1", hash, true),
					signL2Vote(priv2, "member-2", hash, true),
				}
				return env
			}(),
			wantErr: ErrL2ConsensusSetUnknown,
			wantL2:  operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		},
		{
			name:     "unknown consensus ID",
			verifier: makeVerifier(allSigners, map[string]*models.ConsensusPolicy{"consensus-1": enabledConsensus2of2}),
			env: func() *govtypes.GovernanceEnvelope {
				env := buildEnv("unknown-consensus", "nonexistent-consensus", nil)
				hash := env.TransactionHash
				env.Governance.L2.Votes = []*commonv1.L2Vote{
					signL2Vote(priv1, "member-1", hash, true),
					signL2Vote(priv2, "member-2", hash, true),
				}
				return env
			}(),
			wantErr: ErrL2ConsensusSetUnknown,
			wantL2:  operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verified, err := tc.verifier.VerifyEnvelope(context.Background(), tc.env)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			assert.NoError(t, err)
			if verified != nil {
				assert.Equal(t, tc.wantL2, verified.L2Status)
			}
		})
	}
}

// deliberationSetID names the Tribunal consensus set the deliberation tests bind.
const deliberationSetID = "tribunal-set"

// deliberationMemberKeyID is the enrolled Tribunal member that signs attestations.
const deliberationMemberKeyID = "member-a"

// deliberationPolicyStore serves one consensus set, as the outbound Operator's
// L2TrustStore does, with the policy the test configures.
type deliberationPolicyStore struct {
	policy *L2ConsensusPolicy
}

func (s deliberationPolicyStore) GetConsensusPolicy(_ context.Context, id string) (*L2ConsensusPolicy, error) {
	if s.policy == nil || id != deliberationSetID {
		return nil, nil
	}
	policy := *s.policy
	return &policy, nil
}

// tribunalPolicy is an enabled, tribunal-bound policy over the given members.
func tribunalPolicy(members ...string) *L2ConsensusPolicy {
	return &L2ConsensusPolicy{
		MemberKeyIDs:    members,
		Quorum:          1,
		RequireDistinct: true,
		Enabled:         true,
		Provider:        constants.ConsensusProviderTribunal,
	}
}

// deliberationWarden builds a warden whose signer keys and consensus policy are
// the given ones.
func deliberationWarden(t *testing.T, policy *L2ConsensusPolicy, signers map[string]ed25519.PublicKey) *L4Warden {
	t.Helper()
	return NewL4Warden(
		testutil.NewTestLogger(),
		testutil.NewStatefulMockReplayStore(),
		testutil.NewMockStateRootProvider("root-1"),
		&FailClosedSignerStore{Signers: signers},
		deliberationPolicyStore{policy: policy},
		testutil.NewConfigurableMockL3Notary(true),
		NewL1Doctrine(),
		constants.AllActionTypes,
		nil,
		&testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "operator-session-1"},
		&governancetest.UnlockedVault{},
	)
}

// newMemberKey generates a Tribunal member's Ed25519 key pair.
func newMemberKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate member key: %v", err)
	}
	return pub, priv
}

// setDeliberation replaces env's L2 proof with a deliberation attestation by
// keyID carrying sig, under the tribunal consensus set and with no votes.
func setDeliberation(env *govtypes.GovernanceEnvelope, keyID string, sig []byte) {
	env.Governance.L2 = &commonv1.L2Metadata{
		ConsensusSetId: deliberationSetID,
		Deliberation: &commonv1.L2DeliberationAttestation{
			SignerKeyId: keyID,
			Signature:   hex.EncodeToString(sig),
		},
	}
}

// deliberationEnvelope is a signed envelope whose L2 proof is a valid attestation
// by priv over L2DeliberationPayload of its hash.
func deliberationEnvelope(t *testing.T, actionType constants.ActionType, posture constants.Posture, priv ed25519.PrivateKey) *govtypes.GovernanceEnvelope {
	t.Helper()
	env := signedEnvelope(t, actionType, typedPayload(t, actionType), priv, posture)
	setDeliberation(env, deliberationMemberKeyID, ed25519.Sign(priv, L2DeliberationPayload(env.TransactionHash)))
	return env
}

// protocolL2Stage returns the PROTOCOL_L2 stage of a verified envelope.
func protocolL2Stage(t *testing.T, verified *VerifiedTransaction) *operatorv1.DeterministicStageEvidence {
	t.Helper()
	for _, stage := range verified.DeterministicStageEvidence {
		if stage.GetKind() == operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_PROTOCOL_L2 {
			return stage
		}
	}
	t.Fatalf("verified envelope carries no PROTOCOL_L2 stage")
	return nil
}

// assertAttestationExempt asserts a deliberation-attested envelope passes with
// its L2 recorded as NOT_REQUIRED and the attesting member on the L2 stage.
func assertAttestationExempt(t *testing.T, verified *VerifiedTransaction, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected deliberation attestation to pass, got %v", err)
	}
	if verified.L2Status != operatorv1.L2Status_L2_STATUS_NOT_REQUIRED {
		t.Fatalf("expected L2 status NOT_REQUIRED for attested envelope, got %s", verified.L2Status)
	}
	stage := protocolL2Stage(t, verified)
	assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED, stage.GetOutcome())
	assert.Equal(t, deliberationMemberKeyID, stage.GetSignerKeyId())
}

// TestL4Warden_Deliberation_ValidAttestationOnInferencePasses verifies that a
// valid attestation by an enrolled tribunal member exempts a vote-free INFERENCE
// from L2 under the notary posture, and that its L2 stage names the member.
func TestL4Warden_Deliberation_ValidAttestationOnInferencePasses(t *testing.T) {
	t.Parallel()
	memberPub, memberPriv := newMemberKey(t)
	warden := deliberationWarden(t, tribunalPolicy(deliberationMemberKeyID), map[string]ed25519.PublicKey{deliberationMemberKeyID: memberPub})
	env := deliberationEnvelope(t, constants.ActionTypeInference, "notary", memberPriv)

	verified, err := warden.VerifyEnvelope(context.Background(), env)
	assertAttestationExempt(t, verified, err)
}

// TestL4Warden_Deliberation_ValidAttestationOnObservationPasses verifies the
// same exemption for the provider-boundary observation that a Tribunal vote's
// inference attempt emits.
func TestL4Warden_Deliberation_ValidAttestationOnObservationPasses(t *testing.T) {
	t.Parallel()
	memberPub, memberPriv := newMemberKey(t)
	warden := deliberationWarden(t, tribunalPolicy(deliberationMemberKeyID), map[string]ed25519.PublicKey{deliberationMemberKeyID: memberPub})
	env := deliberationEnvelope(t, constants.ActionTypeProviderBoundaryObservation, "notary", memberPriv)

	verified, err := warden.VerifyEnvelope(context.Background(), env)
	assertAttestationExempt(t, verified, err)
}

// TestL4Warden_Deliberation_NonDeliberationActionRejects verifies that an
// attestation on an action outside the deliberation set is rejected, even with
// a valid signature by an enrolled member.
func TestL4Warden_Deliberation_NonDeliberationActionRejects(t *testing.T) {
	t.Parallel()
	for _, actionType := range []constants.ActionType{constants.ActionTypeMcpCall, constants.ActionTypeFsList} {
		t.Run(string(actionType), func(t *testing.T) {
			t.Parallel()
			memberPub, memberPriv := newMemberKey(t)
			warden := deliberationWarden(t, tribunalPolicy(deliberationMemberKeyID), map[string]ed25519.PublicKey{deliberationMemberKeyID: memberPub})
			env := deliberationEnvelope(t, actionType, "notary", memberPriv)

			_, err := warden.VerifyEnvelope(context.Background(), env)
			assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
		})
	}
}

// TestL4Warden_Deliberation_AttestationWithVoteRejects verifies that an
// attestation carried alongside a valid vote is rejected: the exemption holds
// only for a vote-free envelope.
func TestL4Warden_Deliberation_AttestationWithVoteRejects(t *testing.T) {
	t.Parallel()
	memberPub, memberPriv := newMemberKey(t)
	warden := deliberationWarden(t, tribunalPolicy(deliberationMemberKeyID), map[string]ed25519.PublicKey{deliberationMemberKeyID: memberPub})
	env := deliberationEnvelope(t, constants.ActionTypeInference, "notary", memberPriv)
	env.Governance.L2.Votes = []*commonv1.L2Vote{signL2Vote(memberPriv, deliberationMemberKeyID, env.TransactionHash, true)}

	_, err := warden.VerifyEnvelope(context.Background(), env)
	assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
}

// TestL4Warden_Deliberation_SignerTrustRejects verifies that an attestation
// fails closed when its signer is not a policy member, or is a member with no
// trusted signer key.
func TestL4Warden_Deliberation_SignerTrustRejects(t *testing.T) {
	t.Parallel()
	t.Run("signer not a member", func(t *testing.T) {
		t.Parallel()
		outsiderPub, outsiderPriv := newMemberKey(t)
		warden := deliberationWarden(t, tribunalPolicy("member-b"), map[string]ed25519.PublicKey{"outsider": outsiderPub})
		env := deliberationEnvelope(t, constants.ActionTypeInference, "notary", outsiderPriv)
		setDeliberation(env, "outsider", ed25519.Sign(outsiderPriv, L2DeliberationPayload(env.TransactionHash)))

		_, err := warden.VerifyEnvelope(context.Background(), env)
		assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
	})
	t.Run("member without a trusted signer key", func(t *testing.T) {
		t.Parallel()
		_, memberPriv := newMemberKey(t)
		warden := deliberationWarden(t, tribunalPolicy(deliberationMemberKeyID), map[string]ed25519.PublicKey{})
		env := deliberationEnvelope(t, constants.ActionTypeInference, "notary", memberPriv)

		_, err := warden.VerifyEnvelope(context.Background(), env)
		assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
	})
}

// TestL4Warden_Deliberation_NonTribunalPolicyRejects verifies that an attestation
// is rejected under a consensus set whose provider is not tribunal: an
// application set, or a policy with no provider decoded.
func TestL4Warden_Deliberation_NonTribunalPolicyRejects(t *testing.T) {
	t.Parallel()
	for name, provider := range map[string]constants.ConsensusProvider{
		"application provider": constants.ConsensusProviderApplication,
		"empty provider":       "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			memberPub, memberPriv := newMemberKey(t)
			policy := tribunalPolicy(deliberationMemberKeyID)
			policy.Provider = provider
			warden := deliberationWarden(t, policy, map[string]ed25519.PublicKey{deliberationMemberKeyID: memberPub})
			env := deliberationEnvelope(t, constants.ActionTypeInference, "notary", memberPriv)

			_, err := warden.VerifyEnvelope(context.Background(), env)
			assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
		})
	}
}

// TestL4Warden_Deliberation_VoteSignatureReplayRejects verifies that a vote
// signature over L2VotePayload cannot be replayed as an attestation, since the
// attestation signs a different payload.
func TestL4Warden_Deliberation_VoteSignatureReplayRejects(t *testing.T) {
	t.Parallel()
	memberPub, memberPriv := newMemberKey(t)
	warden := deliberationWarden(t, tribunalPolicy(deliberationMemberKeyID), map[string]ed25519.PublicKey{deliberationMemberKeyID: memberPub})
	env := deliberationEnvelope(t, constants.ActionTypeInference, "notary", memberPriv)
	setDeliberation(env, deliberationMemberKeyID, ed25519.Sign(memberPriv, L2VotePayload(env.TransactionHash, true)))

	_, err := warden.VerifyEnvelope(context.Background(), env)
	assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
}

// TestL4Warden_Deliberation_DoctrinePostureStillVerifies verifies that a bad
// attestation is rejected under the doctrine posture, where L2 is not enforced:
// an attestation is never audit-only.
func TestL4Warden_Deliberation_DoctrinePostureStillVerifies(t *testing.T) {
	t.Parallel()
	memberPub, memberPriv := newMemberKey(t)
	_, otherPriv := newMemberKey(t)
	warden := deliberationWarden(t, tribunalPolicy(deliberationMemberKeyID), map[string]ed25519.PublicKey{deliberationMemberKeyID: memberPub})
	env := deliberationEnvelope(t, constants.ActionTypeInference, constants.PostureDoctrine, memberPriv)
	setDeliberation(env, deliberationMemberKeyID, ed25519.Sign(otherPriv, L2DeliberationPayload(env.TransactionHash)))

	_, err := warden.VerifyEnvelope(context.Background(), env)
	assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
}

// TestL4Warden_Deliberation_OutboundTrustStoreVerifies verifies the outbound
// Operator wiring: one L2TrustStore built from a tribunal enrollment bundle is
// both the signer store and the consensus policy store, and it accepts a valid
// attestation.
func TestL4Warden_Deliberation_OutboundTrustStoreVerifies(t *testing.T) {
	t.Parallel()
	memberPub, memberPriv := newMemberKey(t)
	store, err := NewL2TrustStore(&models.OperatorL2Trust{
		ConsensusSetID:  deliberationSetID,
		MemberKeyIDs:    []string{deliberationMemberKeyID, "member-b", "member-c"},
		Quorum:          3,
		RequireDistinct: true,
		Enabled:         true,
		Signers:         []models.OperatorSigner{{KeyID: deliberationMemberKeyID, PublicKeyHex: hex.EncodeToString(memberPub)}},
		Provider:        string(constants.ConsensusProviderTribunal),
	})
	if err != nil {
		t.Fatalf("NewL2TrustStore: %v", err)
	}
	warden := NewL4Warden(
		testutil.NewTestLogger(),
		testutil.NewStatefulMockReplayStore(),
		testutil.NewMockStateRootProvider("root-1"),
		store,
		store,
		testutil.NewConfigurableMockL3Notary(true),
		NewL1Doctrine(),
		constants.AllActionTypes,
		nil,
		&testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "operator-session-1"},
		&governancetest.UnlockedVault{},
	)
	env := deliberationEnvelope(t, constants.ActionTypeInference, "notary", memberPriv)

	verified, err := warden.VerifyEnvelope(context.Background(), env)
	assertAttestationExempt(t, verified, err)
}

// TestL2DeliberationExempt verifies the single owner of the deliberation claim:
// a deliberation action carrying an attestation and no votes.
func TestL2DeliberationExempt(t *testing.T) {
	t.Parallel()
	attested := func(actionType constants.ActionType, votes []*commonv1.L2Vote) *govtypes.GovernanceEnvelope {
		return &govtypes.GovernanceEnvelope{
			ActionType: string(actionType),
			Governance: &commonv1.GovernanceMetadata{
				L2: &commonv1.L2Metadata{
					Votes:        votes,
					Deliberation: &commonv1.L2DeliberationAttestation{SignerKeyId: deliberationMemberKeyID},
				},
			},
		}
	}
	tests := []struct {
		name string
		env  *govtypes.GovernanceEnvelope
		want bool
	}{
		{name: "inference attestation without votes", env: attested(constants.ActionTypeInference, nil), want: true},
		{name: "provider boundary observation without votes", env: attested(constants.ActionTypeProviderBoundaryObservation, nil), want: true},
		{name: "inference attestation with a vote", env: attested(constants.ActionTypeInference, []*commonv1.L2Vote{{SignerKeyId: deliberationMemberKeyID, Decision: true}}), want: false},
		{name: "mcp call attestation without votes", env: attested(constants.ActionTypeMcpCall, nil), want: false},
		{name: "observation attestation with a parent hash", env: func() *govtypes.GovernanceEnvelope {
			env := attested(constants.ActionTypeProviderBoundaryObservation, nil)
			env.Governance.L2.ParentTransactionHash = "parent-inference-hash"
			return env
		}(), want: false},
		{name: "nil envelope", env: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, L2DeliberationExempt(tc.env))
		})
	}
}
