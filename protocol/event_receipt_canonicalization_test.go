// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package protocol_test

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type eventReceiptCanonicalizationVector struct {
	Receipt       json.RawMessage `json:"receipt"`
	CanonicalUTF8 string          `json:"canonical_utf8"`
	PublicKeyHex  string          `json:"public_key_hex"`
}

type receiptPersistenceCanonicalizationVector struct {
	Attestation  json.RawMessage `json:"attestation"`
	CanonicalHex string          `json:"canonical_hex"`
	PublicKeyHex string          `json:"public_key_hex"`
}

//go:embed vectors/event_receipt_canonicalization.json
var eventReceiptCanonicalizationVectorJSON []byte

//go:embed vectors/receipt_persistence_attestation_canonicalization.json
var receiptPersistenceCanonicalizationVectorJSON []byte

//go:embed vectors/event_receipt_stage_evidence_canonicalization.json
var eventReceiptStageEvidenceCanonicalizationVectorJSON []byte

//go:embed vectors/event_receipt_failure_code_canonicalization.json
var eventReceiptFailureCodeCanonicalizationVectorJSON []byte

//go:embed vectors/event_receipt_rejection_code_canonicalization.json
var eventReceiptRejectionCodeCanonicalizationVectorJSON []byte

func TestEventReceiptCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var vector eventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(eventReceiptCanonicalizationVectorJSON, &vector))

	receipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(vector.Receipt, receipt))
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, receipt.L2Status)
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_FAILED, receipt.L3Status)

	canonical, err := governance.CanonicalizeEventReceipt(receipt)
	require.NoError(t, err)
	assert.Equal(t, vector.CanonicalUTF8, string(canonical))

	publicKey, err := hex.DecodeString(vector.PublicKeyHex)
	require.NoError(t, err)
	signature, err := hex.DecodeString(receipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))
}

func TestEventReceiptStageEvidenceCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var vector eventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(eventReceiptStageEvidenceCanonicalizationVectorJSON, &vector))
	receipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(vector.Receipt, receipt))
	require.Len(t, receipt.DeterministicStageEvidence, 2)

	canonical, err := governance.CanonicalizeEventReceipt(receipt)
	require.NoError(t, err)
	assert.Equal(t, vector.CanonicalUTF8, string(canonical))
	publicKey, err := hex.DecodeString(vector.PublicKeyHex)
	require.NoError(t, err)
	signature, err := hex.DecodeString(receipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))

	t.Run("stage order", func(t *testing.T) {
		tampered := proto.Clone(receipt).(*operatorv1.EventReceipt)
		tampered.DeterministicStageEvidence[0], tampered.DeterministicStageEvidence[1] = tampered.DeterministicStageEvidence[1], tampered.DeterministicStageEvidence[0]
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})

	t.Run("stage field", func(t *testing.T) {
		tampered := proto.Clone(receipt).(*operatorv1.EventReceipt)
		tampered.DeterministicStageEvidence[0].DoctrineBundleHash = "sha256:tampered"
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})
}

func TestEventReceiptFailureCodeCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var vector eventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(eventReceiptFailureCodeCanonicalizationVectorJSON, &vector))
	receipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(vector.Receipt, receipt))
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, receipt.Status)
	assert.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, receipt.FailureCode)

	canonical, err := governance.CanonicalizeEventReceipt(receipt)
	require.NoError(t, err)
	assert.Equal(t, vector.CanonicalUTF8, string(canonical))
	assert.Contains(t, string(canonical), `"failure_code":1`)

	publicKey, err := hex.DecodeString(vector.PublicKeyHex)
	require.NoError(t, err)
	signature, err := hex.DecodeString(receipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))

	t.Run("failure code tampering", func(t *testing.T) {
		tampered := proto.Clone(receipt).(*operatorv1.EventReceipt)
		tampered.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})

	t.Run("failure code removal", func(t *testing.T) {
		tampered := proto.Clone(receipt).(*operatorv1.EventReceipt)
		tampered.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_UNSPECIFIED
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.NotContains(t, string(payload), "failure_code")
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})
}

func TestEventReceiptRejectionCodeCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var vector eventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(eventReceiptRejectionCodeCanonicalizationVectorJSON, &vector))
	receipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(vector.Receipt, receipt))
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, receipt.Status)
	assert.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, receipt.FailureCode)
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET, receipt.RejectionCode)

	canonical, err := governance.CanonicalizeEventReceipt(receipt)
	require.NoError(t, err)
	assert.Equal(t, vector.CanonicalUTF8, string(canonical))
	assert.Contains(t, string(canonical), `"rejection_code":19`)

	publicKey, err := hex.DecodeString(vector.PublicKeyHex)
	require.NoError(t, err)
	signature, err := hex.DecodeString(receipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))

	t.Run("rejection code tampering", func(t *testing.T) {
		tampered := proto.Clone(receipt).(*operatorv1.EventReceipt)
		tampered.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})

	t.Run("rejection code removal", func(t *testing.T) {
		tampered := proto.Clone(receipt).(*operatorv1.EventReceipt)
		tampered.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.NotContains(t, string(payload), "rejection_code")
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})
}

func TestReceiptPersistenceAttestationCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var receiptVector eventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(eventReceiptCanonicalizationVectorJSON, &receiptVector))
	var persistenceVector receiptPersistenceCanonicalizationVector
	require.NoError(t, json.Unmarshal(receiptPersistenceCanonicalizationVectorJSON, &persistenceVector))

	receipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(receiptVector.Receipt, receipt))
	attestation := &operatorv1.ReceiptPersistenceAttestation{}
	require.NoError(t, protojson.Unmarshal(persistenceVector.Attestation, attestation))
	receipt.FinalPersistenceAttestation = attestation

	canonical, err := governance.CanonicalizeReceiptPersistenceAttestation(attestation)
	require.NoError(t, err)
	assert.Equal(t, persistenceVector.CanonicalHex, hex.EncodeToString(canonical))

	publicKey, err := hex.DecodeString(persistenceVector.PublicKeyHex)
	require.NoError(t, err)
	assert.NoError(t, governance.VerifyReceiptPersistenceAttestation(receipt, publicKey))
}
