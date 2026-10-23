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

// newSignedPersistedReceipt returns a receipt carrying a valid actuator signature and a valid
// final-persistence attestation, signed by the test actuator.
func newSignedPersistedReceipt(t *testing.T, actuator *L5Actuator) *operatorv1.ActionReceipt {
	t.Helper()
	receipt := &operatorv1.ActionReceipt{
		TransactionId:    "test-transaction",
		TransactionHash:  "test-transaction-hash",
		Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:    "completed",
		ExecutedAtUnixMs: 1,
		SignerKeyId:      actuator.KeyID,
	}
	signature, err := actuator.signReceipt(receipt)
	require.NoError(t, err)
	receipt.Signature = signature
	attestation, err := actuator.signReceiptPersistenceAttestation(receipt)
	require.NoError(t, err)
	receipt.FinalPersistenceAttestation = attestation
	return receipt
}

func TestVerifyActionReceipt(t *testing.T) {
	t.Parallel()

	actuator, publicKey := newTestActuator(t)
	wrongPublicKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	tests := []struct {
		name      string
		receipt   func(*testing.T) *operatorv1.ActionReceipt
		publicKey ed25519.PublicKey
		wantErr   error
	}{
		{
			name: "valid signature and persistence attestation",
			receipt: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				return newSignedPersistedReceipt(t, actuator)
			},
			publicKey: publicKey,
		},
		{
			name: "flipped signature byte",
			receipt: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := newSignedPersistedReceipt(t, actuator)
				signature, err := hex.DecodeString(receipt.Signature)
				require.NoError(t, err)
				signature[0] ^= 0x01
				receipt.Signature = hex.EncodeToString(signature)
				return receipt
			},
			publicKey: publicKey,
			wantErr:   constants.ErrActionReceiptSignatureInvalid,
		},
		{
			name: "missing persistence attestation",
			receipt: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := newSignedPersistedReceipt(t, actuator)
				receipt.FinalPersistenceAttestation = nil
				return receipt
			},
			publicKey: publicKey,
			wantErr:   constants.ErrReceiptPersistenceAttestationMissing,
		},
		{
			name: "wrong public key",
			receipt: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				return newSignedPersistedReceipt(t, actuator)
			},
			publicKey: wrongPublicKey,
			wantErr:   constants.ErrActionReceiptSignatureInvalid,
		},
		{
			name: "attestation signed by a key other than the actuator",
			receipt: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := newSignedPersistedReceipt(t, actuator)
				attestation := proto.Clone(receipt.FinalPersistenceAttestation).(*operatorv1.ReceiptPersistenceAttestation)
				canonical, err := CanonicalizeReceiptPersistenceAttestation(attestation)
				require.NoError(t, err)
				_, forgedKey, err := ed25519.GenerateKey(nil)
				require.NoError(t, err)
				attestation.Signature = hex.EncodeToString(ed25519.Sign(forgedKey, canonical))
				receipt.FinalPersistenceAttestation = attestation
				return receipt
			},
			publicKey: publicKey,
			wantErr:   constants.ErrReceiptPersistenceAttestationInvalid,
		},
		{
			name: "missing receipt",
			receipt: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				return nil
			},
			publicKey: publicKey,
			wantErr:   constants.ErrActionReceiptMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := VerifyActionReceipt(tt.receipt(t), tt.publicKey)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}
