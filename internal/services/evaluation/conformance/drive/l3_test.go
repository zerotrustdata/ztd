// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// l3Faults are the faults L3Mutators owns, rows 20 and 21.
var l3Faults = []cases.Fault{cases.FaultL3Missing, cases.FaultL3Invalid, cases.FaultL3Garbage}

// withoutL3 returns a copy of env with governance.l3 cleared. A governance message
// left empty by the clear is removed, so a baseline with no governance compares equal.
func withoutL3(env *commonv1.GovernanceEnvelope) *commonv1.GovernanceEnvelope {
	got := proto.Clone(env).(*commonv1.GovernanceEnvelope)
	if got.Governance == nil {
		return got
	}
	got.Governance.L3 = nil
	if got.Governance.L1 == nil && got.Governance.L2 == nil {
		got.Governance = nil
	}
	return got
}

// TestL3MutatorsChangeOnlyGovernanceL3 applies each L3 mutator to a fresh baseline.
// Once governance.l3 is cleared, the envelope must equal the baseline: nothing else
// moved, and the transaction hash and id are the baseline's.
func TestL3MutatorsChangeOnlyGovernanceL3(t *testing.T) {
	set := L3Mutators()
	require.Len(t, set, len(l3Faults), "L3Mutators must own exactly the three L3 faults")

	for _, fault := range l3Faults {
		t.Run(string(fault), func(t *testing.T) {
			m, ok := set[fault]
			require.True(t, ok, "L3Mutators has no mutator for %s", fault)

			a := testAttempt(fault, cases.ActionMutation)
			base, err := Baseline(a, testIdentity(), "root-1", time.Now())
			require.NoError(t, err)
			mutated := proto.Clone(base).(*commonv1.GovernanceEnvelope)
			require.NoError(t, m.Apply(context.Background(), mutated, a))

			assert.Equal(t, base.TransactionHash, mutated.TransactionHash, "%s must not change the transaction hash", fault)
			assert.Equal(t, base.Id, mutated.Id, "%s must not change the id", fault)
			assert.True(t, proto.Equal(withoutL3(base), withoutL3(mutated)), "%s changed more than governance.l3", fault)
		})
	}
}

// TestL3MutatorsLeaveTheHashValid proves L3 is outside the hash: after each mutator
// the recomputed hash still equals the transaction hash, so L4 reaches the L3 check.
func TestL3MutatorsLeaveTheHashValid(t *testing.T) {
	set := L3Mutators()
	for _, fault := range l3Faults {
		t.Run(string(fault), func(t *testing.T) {
			a := testAttempt(fault, cases.ActionMutation)
			env, err := Baseline(a, testIdentity(), "root-1", time.Now())
			require.NoError(t, err)
			require.NoError(t, set[fault].Apply(context.Background(), env, a))

			computed, err := governance.GenerateMessageID(env)
			require.NoError(t, err)
			assert.Equal(t, computed, env.TransactionHash, "%s must leave a hash that verifies", fault)
		})
	}
}

// TestL3MissingRemovesProofAndKeepsL2 is the negative side of row 20: the missing
// fault must leave no L3 proof, and an L2 proof already on the envelope survives.
func TestL3MissingRemovesProofAndKeepsL2(t *testing.T) {
	a := testAttempt(cases.FaultL3Missing, cases.ActionMutation)
	env, err := Baseline(a, testIdentity(), "root-1", time.Now())
	require.NoError(t, err)
	env.Governance = &commonv1.GovernanceMetadata{
		L2: &commonv1.L2Metadata{},
		L3: &commonv1.L3Metadata{Proof: &commonv1.L3Proof{CredentialId: "present"}},
	}

	require.NoError(t, L3Mutators()[cases.FaultL3Missing].Apply(context.Background(), env, a))

	assert.Nil(t, env.Governance.L3, "the missing fault must leave governance.l3 unset")
	assert.NotNil(t, env.Governance.L2, "the missing fault must not touch governance.l2")
}

// TestL3InvalidProofIsPresentAndForged checks the invalid proof field by field. It
// must be present (governance.l3.proof non-nil, which verifyL3Posture reads as a
// proof), carry a credential, an authenticator data blob and a 64-byte signature,
// and name the envelope's transaction hash in its client data challenge.
func TestL3InvalidProofIsPresentAndForged(t *testing.T) {
	for _, fault := range []cases.Fault{cases.FaultL3Invalid, cases.FaultL3Garbage} {
		t.Run(string(fault), func(t *testing.T) {
			a := testAttempt(fault, cases.ActionMutation)
			env, err := Baseline(a, testIdentity(), "root-1", time.Now())
			require.NoError(t, err)
			require.NoError(t, L3Mutators()[fault].Apply(context.Background(), env, a))

			proof := env.GetGovernance().GetL3().GetProof()
			require.NotNil(t, proof, "the invalid proof must be present to reach the notary verify step")
			assert.Empty(t, proof.MtlsCertFingerprint, "the forged proof is a passkey proof, not a CLI proof")

			credential, err := base64.RawURLEncoding.DecodeString(proof.CredentialId)
			require.NoError(t, err)
			assert.Len(t, credential, invalidL3CredentialBytes)

			authenticatorData, err := base64.RawURLEncoding.DecodeString(proof.AuthenticatorData)
			require.NoError(t, err)
			assert.Len(t, authenticatorData, invalidL3AuthenticatorDataBytes)

			signature, err := base64.RawURLEncoding.DecodeString(proof.Signature)
			require.NoError(t, err)
			assert.Len(t, signature, invalidL3SignatureBytes)

			var clientData webAuthnClientData
			require.NoError(t, json.Unmarshal([]byte(proof.ClientDataJson), &clientData))
			assert.Equal(t, "webauthn.get", clientData.Type)
			assert.Equal(t, conformanceL3Origin, clientData.Origin)
			challenge, err := base64.RawURLEncoding.DecodeString(clientData.Challenge)
			require.NoError(t, err)
			assert.Equal(t, env.TransactionHash, string(challenge), "the challenge must name the envelope's transaction hash")
		})
	}
}

// TestL3InvalidProofIsFreshPerAttempt is the negative side of the forgery: two
// attempts never share credential material, so no run can replay a proof.
func TestL3InvalidProofIsFreshPerAttempt(t *testing.T) {
	first, err := invalidL3Proof("tx-hash-1")
	require.NoError(t, err)
	second, err := invalidL3Proof("tx-hash-1")
	require.NoError(t, err)
	assert.NotEqual(t, first.CredentialId, second.CredentialId)
	assert.NotEqual(t, first.Signature, second.Signature)
}

// TestL3InvalidProofNamesTheTransactionHash is the negative side of the challenge:
// a proof built for one transaction must not name another transaction's hash.
func TestL3InvalidProofNamesTheTransactionHash(t *testing.T) {
	proof, err := invalidL3Proof("tx-hash-A")
	require.NoError(t, err)
	var clientData webAuthnClientData
	require.NoError(t, json.Unmarshal([]byte(proof.ClientDataJson), &clientData))
	challenge, err := base64.RawURLEncoding.DecodeString(clientData.Challenge)
	require.NoError(t, err)
	assert.Equal(t, "tx-hash-A", string(challenge))
	assert.NotEqual(t, "tx-hash-B", string(challenge))
}
