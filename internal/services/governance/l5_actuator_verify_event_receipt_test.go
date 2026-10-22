// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// newSignedPersistedEventReceipt returns an event receipt carrying a valid actuator signature and a valid
// final-persistence attestation, signed by the test actuator.
func newSignedPersistedEventReceipt(t *testing.T, actuator *L5Actuator) *operatorv1.EventReceipt {
	t.Helper()
	eventReceipt := &operatorv1.EventReceipt{
		TransactionId:    "test-transaction",
		TransactionHash:  "test-transaction-hash",
		Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:    "completed",
		ExecutedAtUnixMs: 1,
		SignerKeyId:      actuator.KeyID,
	}
	signature, err := actuator.signEventReceipt(eventReceipt)
	require.NoError(t, err)
	eventReceipt.Signature = signature
	attestation, err := actuator.signEventReceiptPersistenceAttestation(eventReceipt)
	require.NoError(t, err)
	eventReceipt.FinalPersistenceAttestation = attestation
	return eventReceipt
}

func TestVerifyEventReceipt(t *testing.T) {
	t.Parallel()
	actuator, publicKey := newTestActuator(t)
	wrongPublicKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	tests := []struct {
		name         string
		eventReceipt func(*testing.T) *operatorv1.EventReceipt
		publicKey    ed25519.PublicKey
		wantErr      error
	}{
		{
			name: "valid signature and persistence attestation",
			eventReceipt: func(t *testing.T) *operatorv1.EventReceipt {
				return newSignedPersistedEventReceipt(t, actuator)
			},
			publicKey: publicKey,
		},
		{
			name: "flipped signature byte",
			eventReceipt: func(t *testing.T) *operatorv1.EventReceipt {
				eventReceipt := newSignedPersistedEventReceipt(t, actuator)
				signature, err := hex.DecodeString(eventReceipt.Signature)
				require.NoError(t, err)
				signature[0] ^= 0x01
				eventReceipt.Signature = hex.EncodeToString(signature)
				return eventReceipt
			},
			publicKey: publicKey,
			wantErr:   constants.ErrEventReceiptSignatureInvalid,
		},
		{
			name: "missing persistence attestation",
			eventReceipt: func(t *testing.T) *operatorv1.EventReceipt {
				eventReceipt := newSignedPersistedEventReceipt(t, actuator)
				eventReceipt.FinalPersistenceAttestation = nil
				return eventReceipt
			},
			publicKey: publicKey,
			wantErr:   constants.ErrEventReceiptPersistenceAttestationMissing,
		},
		{
			name: "wrong public key",
			eventReceipt: func(t *testing.T) *operatorv1.EventReceipt {
				return newSignedPersistedEventReceipt(t, actuator)
			},
			publicKey: wrongPublicKey,
			wantErr:   constants.ErrEventReceiptSignatureInvalid,
		},
		{
			name: "attestation signed by a key other than the actuator",
			eventReceipt: func(t *testing.T) *operatorv1.EventReceipt {
				eventReceipt := newSignedPersistedEventReceipt(t, actuator)
				attestation := proto.Clone(eventReceipt.FinalPersistenceAttestation).(*operatorv1.EventReceiptPersistenceAttestation)
				canonical, err := CanonicalizeEventReceiptPersistenceAttestation(attestation)
				require.NoError(t, err)
				_, forgedKey, err := ed25519.GenerateKey(nil)
				require.NoError(t, err)
				attestation.Signature = hex.EncodeToString(ed25519.Sign(forgedKey, canonical))
				eventReceipt.FinalPersistenceAttestation = attestation
				return eventReceipt
			},
			publicKey: publicKey,
			wantErr:   constants.ErrEventReceiptPersistenceAttestationInvalid,
		},
		{
			name: "missing event receipt",
			eventReceipt: func(t *testing.T) *operatorv1.EventReceipt {
				return nil
			},
			publicKey: publicKey,
			wantErr:   constants.ErrEventReceiptMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyEventReceipt(tt.eventReceipt(t), tt.publicKey)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}
