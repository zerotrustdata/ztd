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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
)

func TestMintCapability_BindsEnvelopeFields(t *testing.T) {
	t.Parallel()

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	expiresAt := time.Now().Add(5 * time.Minute)
	vt := &VerifiedTransaction{
		Envelope: &govtypes.GovernanceEnvelope{
			TransactionHash:   "abc123hash",
			OperatorId:        "op-1",
			OperatorSessionId: "sess-1",
			TargetResource:    "localhost",
		},
		ActionType: constants.ActionTypeExecuteBash,
		ExpiresAt:  expiresAt,
	}

	cap, err := MintCapability(vt, privKey, "l5-key-1")
	require.NoError(t, err)
	require.NotNil(t, cap)

	assert.Equal(t, "abc123hash", cap.TransactionHash)
	assert.Equal(t, constants.ActionTypeExecuteBash, cap.ActionType)
	assert.Equal(t, "localhost", cap.TargetResource)
	assert.Equal(t, "op-1", cap.OperatorID)
	assert.Equal(t, "sess-1", cap.OperatorSession)
	assert.Equal(t, "l5-key-1", cap.KeyID)
	assert.Equal(t, expiresAt.Unix(), cap.ExpiresAt.Unix())
	assert.NotEmpty(t, cap.Token)
	assert.False(t, cap.IsDissolved(), "freshly minted capability should not be dissolved")
}

func TestMintCapability_NilVT(t *testing.T) {
	t.Parallel()

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	_, err = MintCapability(nil, privKey, "l5-key-1")
	require.Error(t, err)
}

func TestCapability_Dissolve(t *testing.T) {
	t.Parallel()

	cap := &Capability{
		TransactionHash: "hash-1",
		ActionType:      constants.ActionTypeExecuteBash,
		ExpiresAt:       time.Now().Add(5 * time.Minute),
	}

	require.False(t, cap.IsDissolved())
	require.True(t, cap.IsValid(time.Now()))

	cap.Dissolve()

	require.True(t, cap.IsDissolved())
	require.False(t, cap.IsValid(time.Now()))
}

func TestCapability_Expired(t *testing.T) {
	t.Parallel()

	cap := &Capability{
		TransactionHash: "hash-1",
		ActionType:      constants.ActionTypeExecuteBash,
		ExpiresAt:       time.Now().Add(-1 * time.Minute), // already expired
	}

	require.True(t, cap.IsExpired(time.Now()))
	require.False(t, cap.IsValid(time.Now()))
}

func TestCapability_Verify_Success(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cap := &Capability{
		TransactionHash: "hash-1",
		ActionType:      constants.ActionTypeExecuteBash,
		ExpiresAt:       now.Add(5 * time.Minute),
	}

	err := cap.Verify(constants.ActionTypeExecuteBash, "hash-1", now)
	require.NoError(t, err)
}

func TestCapability_Verify_ActionMismatch(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cap := &Capability{
		TransactionHash: "hash-1",
		ActionType:      constants.ActionTypeExecuteBash,
		ExpiresAt:       now.Add(5 * time.Minute),
	}

	err := cap.Verify(constants.ActionTypeFileEdit, "hash-1", now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "action type mismatch")
}

func TestCapability_Verify_HashMismatch(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cap := &Capability{
		TransactionHash: "hash-1",
		ActionType:      constants.ActionTypeExecuteBash,
		ExpiresAt:       now.Add(5 * time.Minute),
	}

	err := cap.Verify(constants.ActionTypeExecuteBash, "wrong-hash", now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction hash mismatch")
}

func TestCapability_Verify_Dissolved(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cap := &Capability{
		TransactionHash: "hash-1",
		ActionType:      constants.ActionTypeExecuteBash,
		ExpiresAt:       now.Add(5 * time.Minute),
	}
	cap.Dissolve()

	err := cap.Verify(constants.ActionTypeExecuteBash, "hash-1", now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dissolved")
}

func TestCapability_Verify_Expired(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cap := &Capability{
		TransactionHash: "hash-1",
		ActionType:      constants.ActionTypeExecuteBash,
		ExpiresAt:       now.Add(-1 * time.Minute),
	}

	err := cap.Verify(constants.ActionTypeExecuteBash, "hash-1", now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestCapability_VerifyRejectsNilAndInvalidContextValues(t *testing.T) {
	t.Parallel()

	now := time.Now()
	valid := &Capability{
		TransactionHash: "hash-1",
		ActionType:      constants.ActionTypeExecuteBash,
		ExpiresAt:       now.Add(time.Minute),
	}
	for _, tt := range []struct {
		name string
		cap  *Capability
		want string
	}{
		{name: "nil capability", cap: nil, want: "nil capability"},
		{name: "dissolved capability", cap: func() *Capability {
			valid.Dissolve()
			return valid
		}(), want: "dissolved"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cap.Verify(constants.ActionTypeExecuteBash, "hash-1", now)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	ctx := context.WithValue(context.Background(), constants.ContextKeyCapability, "not-a-capability")
	assert.Nil(t, CapabilityFromContext(ctx))
}

func TestCapabilityFromContext(t *testing.T) {
	t.Parallel()

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		cap := &Capability{TransactionHash: "h1"}
		ctx := ContextWithCapability(context.Background(), cap)

		extracted := CapabilityFromContext(ctx)
		require.NotNil(t, extracted)
		assert.Equal(t, "h1", extracted.TransactionHash)
	})

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		extracted := CapabilityFromContext(context.Background())
		require.Nil(t, extracted)
	})
}
