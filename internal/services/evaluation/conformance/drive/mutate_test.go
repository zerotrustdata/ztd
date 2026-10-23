// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.
package drive

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// mutatorCase describes one mutator: the fields it may change, and whether it
// rehashes. resetChanged copies the expected fields from base onto got so that
// only the mutator's own change can differ.
type mutatorCase struct {
	fault       cases.Fault
	rehash      bool
	resetFields func(got, base *commonv1.GovernanceEnvelope)
}

func resetHash(got, base *commonv1.GovernanceEnvelope) {
	got.Id = base.Id
	got.TransactionHash = base.TransactionHash
}

var mutatorCases = []mutatorCase{
	{fault: cases.FaultNone},
	{fault: cases.FaultReadBaseline},
	{fault: cases.FaultReplay},
	{fault: cases.FaultDoctrine},
	{fault: cases.FaultUngovernedCollection},
	{fault: cases.FaultPayloadTampered, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.Payload = base.Payload
	}},
	{fault: cases.FaultIDMismatch, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.Id = base.Id
	}},
	{fault: cases.FaultExpired, rehash: true, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.ExpiresAt = base.ExpiresAt
		resetHash(got, base)
	}},
	{fault: cases.FaultProtocolVersion, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.ProtocolVersion = base.ProtocolVersion
	}},
	{fault: cases.FaultEventActionMismatch, rehash: true, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.EventType = base.EventType
		resetHash(got, base)
	}},
	{fault: cases.FaultWrongTarget, rehash: true, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.OperatorSessionId = base.OperatorSessionId
		resetHash(got, base)
	}},
	{fault: cases.FaultStaleStateRoot, rehash: true, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.StateMerkleRoot = base.StateMerkleRoot
		resetHash(got, base)
	}},
	{fault: cases.FaultCallerPosture, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.Posture = base.Posture
	}},
	{fault: cases.FaultRequestorMismatch, rehash: true, resetFields: func(got, base *commonv1.GovernanceEnvelope) {
		got.RequestorUserId = base.RequestorUserId
		resetHash(got, base)
	}},
}

// TestBaseMutatorsChangeExactlyOneProperty applies each mutator to a fresh
// baseline. After the expected fields are reset, the envelope must equal the
// baseline, which proves nothing else moved.
func TestBaseMutatorsChangeExactlyOneProperty(t *testing.T) {
	set := BaseMutators()
	require.Len(t, set, len(mutatorCases), "every BaseMutators fault needs a case here")

	for _, mc := range mutatorCases {
		t.Run(string(mc.fault), func(t *testing.T) {
			m, ok := set[mc.fault]
			require.True(t, ok, "BaseMutators has no mutator for %s", mc.fault)

			a := testAttempt(mc.fault, cases.ActionMutation)
			base, err := Baseline(a, testIdentity(), "root-1", time.Now())
			require.NoError(t, err)
			mutated := proto.Clone(base).(*commonv1.GovernanceEnvelope)
			require.NoError(t, m.Apply(context.Background(), mutated, a))

			if mc.resetFields == nil {
				assert.True(t, proto.Equal(base, mutated), "%s must not change the envelope", mc.fault)
				return
			}
			assert.False(t, proto.Equal(base, mutated), "%s must change the envelope", mc.fault)

			got := proto.Clone(mutated).(*commonv1.GovernanceEnvelope)
			mc.resetFields(got, base)
			assert.True(t, proto.Equal(base, got), "%s changed more than one property", mc.fault)

			if mc.rehash {
				want, err := governance.GenerateMessageID(mutated)
				require.NoError(t, err)
				assert.Equal(t, want, mutated.TransactionHash, "%s must leave a valid hash", mc.fault)
			}
		})
	}
}

// TestMutatorsFailHashCheckWhenNotRehashed is the negative side: a mutator that
// does not rehash must leave a hash that L4 refuses. L4 compares transaction_hash
// and id against the recomputed hash separately, so each case checks the field
// it breaks.
func TestMutatorsFailHashCheckWhenNotRehashed(t *testing.T) {
	set := BaseMutators()
	tests := []struct {
		fault     cases.Fault
		wantBreak string // "transaction_hash" or "id"
	}{
		{cases.FaultPayloadTampered, "transaction_hash"},
		{cases.FaultIDMismatch, "id"},
	}
	for _, tt := range tests {
		t.Run(string(tt.fault), func(t *testing.T) {
			a := testAttempt(tt.fault, cases.ActionMutation)
			env, err := Baseline(a, testIdentity(), "root-1", time.Now())
			require.NoError(t, err)
			require.NoError(t, set[tt.fault].Apply(context.Background(), env, a))

			computed, err := governance.GenerateMessageID(env)
			require.NoError(t, err)
			switch tt.wantBreak {
			case "transaction_hash":
				assert.NotEqual(t, computed, env.TransactionHash)
				assert.NotEqual(t, computed, env.Id)
				require.Error(t, governance.VerifyTransactionHash(env, env.TransactionHash))
			case "id":
				assert.Equal(t, computed, env.TransactionHash)
				assert.NotEqual(t, computed, env.Id)
			}
		})
	}
}

// TestProtocolVersionMutatorIsRejectedByHasher proves the "0.0" version cannot
// be hashed, so the L4 unsupported-version check is what refuses it.
func TestProtocolVersionMutatorIsRejectedByHasher(t *testing.T) {
	a := testAttempt(cases.FaultProtocolVersion, cases.ActionMutation)
	env, err := Baseline(a, testIdentity(), "root-1", time.Now())
	require.NoError(t, err)
	require.NoError(t, BaseMutators()[cases.FaultProtocolVersion].Apply(context.Background(), env, a))

	_, err = governance.GenerateMessageID(env)
	assert.ErrorIs(t, err, constants.ErrTxProtocolVersionUnsupported)
}

// TestPayloadTamperRefusesMissingPayload proves the tamper mutator fails closed
// on an envelope that carries no payload to tamper with.
func TestPayloadTamperRefusesMissingPayload(t *testing.T) {
	a := testAttempt(cases.FaultPayloadTampered, cases.ActionMutation)
	env := &commonv1.GovernanceEnvelope{}
	err := BaseMutators()[cases.FaultPayloadTampered].Apply(context.Background(), env, a)
	assert.ErrorIs(t, err, constants.ErrTxPayloadMissing)
}
