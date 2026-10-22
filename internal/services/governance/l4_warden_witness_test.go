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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// witnessParentHash is the parent INFERENCE transaction a witness binds to.
const witnessParentHash = "parent-inference-hash"

// witnessPayload is an observation command of actionType naming inferenceID.
func witnessPayload(t *testing.T, actionType constants.ActionType, inferenceID string) []byte {
	t.Helper()
	var msg proto.Message
	switch actionType {
	case constants.ActionTypeProviderBoundaryObservation:
		msg = &evalv1.ProviderBoundaryObservationCommand{
			ProviderAttemptId:      "attempt-1",
			InferenceTransactionId: inferenceID,
			Phase:                  evalv1.ProviderBoundaryObservationPhase_PROVIDER_BOUNDARY_OBSERVATION_PHASE_BEGIN,
		}
	case constants.ActionTypeModelProvenanceObservation:
		msg = &evalv1.ModelProvenanceObservationCommand{
			ProviderAttemptId:      "attempt-1",
			InferenceTransactionId: inferenceID,
			Phase:                  evalv1.ModelProvenanceObservationPhase_MODEL_PROVENANCE_OBSERVATION_PHASE_BEGIN,
		}
	default:
		t.Fatalf("no witness payload for %s", actionType)
	}
	payload, err := proto.Marshal(msg)
	require.NoError(t, err)
	return payload
}

// witnessEnvelope is an observation envelope naming inferenceID whose L2 proof
// is priv's vote over the parent hash, bound to that parent.
func witnessEnvelope(t *testing.T, actionType constants.ActionType, inferenceID string, posture constants.Posture, priv ed25519.PrivateKey) *govtypes.GovernanceEnvelope {
	t.Helper()
	env := signedEnvelope(t, actionType, witnessPayload(t, actionType, inferenceID), priv, posture)
	env.Governance.L2 = &commonv1.L2Metadata{
		ConsensusSetId:        deliberationSetID,
		Votes:                 []*commonv1.L2Vote{signL2Vote(priv, deliberationMemberKeyID, witnessParentHash, true)},
		ParentTransactionHash: witnessParentHash,
	}
	return env
}

// witnessWarden is a tribunal warden trusting one member key.
func witnessWarden(t *testing.T) (*L4Warden, ed25519.PrivateKey) {
	t.Helper()
	memberPub, memberPriv := newMemberKey(t)
	return deliberationWarden(t, tribunalPolicy(deliberationMemberKeyID), map[string]ed25519.PublicKey{deliberationMemberKeyID: memberPub}), memberPriv
}

// TestL4Warden_Witness_ParentQuorumPasses verifies that an observation naming
// its parent inference passes on the parent's votes, recorded as required and
// valid, for both witness actions.
func TestL4Warden_Witness_ParentQuorumPasses(t *testing.T) {
	t.Parallel()
	for _, actionType := range []constants.ActionType{constants.ActionTypeProviderBoundaryObservation, constants.ActionTypeModelProvenanceObservation} {
		t.Run(string(actionType), func(t *testing.T) {
			t.Parallel()
			warden, memberPriv := witnessWarden(t)
			env := witnessEnvelope(t, actionType, witnessParentHash, constants.PostureConsensus, memberPriv)

			verified, err := warden.VerifyEnvelope(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, verified.L2Status)
		})
	}
}

// TestL4Warden_Witness_PayloadNamingAnotherInferenceRejects verifies that a
// parent quorum cannot be borrowed by an observation whose signed payload names
// a different inference, or none.
func TestL4Warden_Witness_PayloadNamingAnotherInferenceRejects(t *testing.T) {
	t.Parallel()
	for name, inferenceID := range map[string]string{"another inference": "other-inference-hash", "no inference": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			warden, memberPriv := witnessWarden(t)
			env := witnessEnvelope(t, constants.ActionTypeProviderBoundaryObservation, inferenceID, constants.PostureConsensus, memberPriv)

			_, err := warden.VerifyEnvelope(context.Background(), env)
			assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
		})
	}
}

// TestL4Warden_Witness_NonWitnessActionRejects verifies that a parent hash on
// any action other than an observation command is rejected.
func TestL4Warden_Witness_NonWitnessActionRejects(t *testing.T) {
	t.Parallel()
	for _, actionType := range []constants.ActionType{constants.ActionTypeInference, constants.ActionTypeFsList} {
		t.Run(string(actionType), func(t *testing.T) {
			t.Parallel()
			warden, memberPriv := witnessWarden(t)
			env := signedEnvelope(t, actionType, typedPayload(t, actionType), memberPriv, constants.PostureConsensus)
			env.Governance.L2 = &commonv1.L2Metadata{
				ConsensusSetId:        deliberationSetID,
				Votes:                 []*commonv1.L2Vote{signL2Vote(memberPriv, deliberationMemberKeyID, witnessParentHash, true)},
				ParentTransactionHash: witnessParentHash,
			}

			_, err := warden.VerifyEnvelope(context.Background(), env)
			assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
		})
	}
}

// TestL4Warden_Witness_WithDeliberationRejects verifies that a witness binding
// and a deliberation attestation are mutually exclusive.
func TestL4Warden_Witness_WithDeliberationRejects(t *testing.T) {
	t.Parallel()
	warden, memberPriv := witnessWarden(t)
	env := witnessEnvelope(t, constants.ActionTypeProviderBoundaryObservation, witnessParentHash, constants.PostureConsensus, memberPriv)
	env.Governance.L2.Deliberation = &commonv1.L2DeliberationAttestation{SignerKeyId: deliberationMemberKeyID}

	_, err := warden.VerifyEnvelope(context.Background(), env)
	assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
}

// TestL4Warden_Witness_VotesOverOwnHashNotCounted verifies that a bound
// witness's votes are checked against the parent hash only: votes over the
// observation's own hash do not satisfy the parent quorum. An observation is
// not a state change, so L2 does not gate it and the receipt records the
// uncounted votes as NOT_REQUIRED rather than REQUIRED_VALID.
func TestL4Warden_Witness_VotesOverOwnHashNotCounted(t *testing.T) {
	t.Parallel()
	warden, memberPriv := witnessWarden(t)
	env := witnessEnvelope(t, constants.ActionTypeProviderBoundaryObservation, witnessParentHash, constants.PostureConsensus, memberPriv)
	env.Governance.L2.Votes = []*commonv1.L2Vote{signL2Vote(memberPriv, deliberationMemberKeyID, env.TransactionHash, true)}

	verified, err := warden.VerifyEnvelope(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, verified.L2Status)
}

// TestL4Warden_Witness_DoctrinePostureStillVerifies verifies that a binding
// naming another inference is rejected under the doctrine posture, where L2 is
// not enforced: a witness binding is never audit-only.
func TestL4Warden_Witness_DoctrinePostureStillVerifies(t *testing.T) {
	t.Parallel()
	warden, memberPriv := witnessWarden(t)
	env := witnessEnvelope(t, constants.ActionTypeProviderBoundaryObservation, "other-inference-hash", constants.PostureDoctrine, memberPriv)

	_, err := warden.VerifyEnvelope(context.Background(), env)
	assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
}

// TestL2WitnessBound verifies the single owner of the witness claim: an
// observation command carrying a parent hash and no deliberation attestation.
func TestL2WitnessBound(t *testing.T) {
	t.Parallel()
	bound := func(actionType constants.ActionType, parent string, deliberation *commonv1.L2DeliberationAttestation) *govtypes.GovernanceEnvelope {
		return &govtypes.GovernanceEnvelope{
			ActionType: string(actionType),
			Governance: &commonv1.GovernanceMetadata{
				L2: &commonv1.L2Metadata{ParentTransactionHash: parent, Deliberation: deliberation},
			},
		}
	}
	tests := []struct {
		name string
		env  *govtypes.GovernanceEnvelope
		want bool
	}{
		{name: "provider boundary observation", env: bound(constants.ActionTypeProviderBoundaryObservation, witnessParentHash, nil), want: true},
		{name: "model provenance observation", env: bound(constants.ActionTypeModelProvenanceObservation, witnessParentHash, nil), want: true},
		{name: "observation without parent", env: bound(constants.ActionTypeProviderBoundaryObservation, "", nil), want: false},
		{name: "observation with deliberation", env: bound(constants.ActionTypeProviderBoundaryObservation, witnessParentHash, &commonv1.L2DeliberationAttestation{SignerKeyId: deliberationMemberKeyID}), want: false},
		{name: "inference with parent", env: bound(constants.ActionTypeInference, witnessParentHash, nil), want: false},
		{name: "nil envelope", env: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, L2WitnessBound(tc.env))
		})
	}
}
