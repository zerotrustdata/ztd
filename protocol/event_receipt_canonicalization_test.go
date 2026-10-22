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

type actionEventReceiptCanonicalizationVector struct {
	EventReceipt  json.RawMessage `json:"event_receipt"`
	CanonicalUTF8 string          `json:"canonical_utf8"`
	PublicKeyHex  string          `json:"public_key_hex"`
}

type eventReceiptPersistenceCanonicalizationVector struct {
	Attestation  json.RawMessage `json:"attestation"`
	CanonicalHex string          `json:"canonical_hex"`
	PublicKeyHex string          `json:"public_key_hex"`
}

//go:embed vectors/event_receipt_canonicalization.json
var actionEventReceiptCanonicalizationVectorJSON []byte

//go:embed vectors/event_receipt_persistence_attestation_canonicalization.json
var eventReceiptPersistenceCanonicalizationVectorJSON []byte

//go:embed vectors/event_receipt_stage_evidence_canonicalization.json
var actionEventReceiptStageEvidenceCanonicalizationVectorJSON []byte

//go:embed vectors/event_receipt_failure_code_canonicalization.json
var actionEventReceiptFailureCodeCanonicalizationVectorJSON []byte

//go:embed vectors/event_receipt_rejection_code_canonicalization.json
var actionEventReceiptRejectionCodeCanonicalizationVectorJSON []byte

func TestEventReceiptCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var vector actionEventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(actionEventReceiptCanonicalizationVectorJSON, &vector))

	eventReceipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(vector.EventReceipt, eventReceipt))
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, eventReceipt.L2Status)
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_FAILED, eventReceipt.L3Status)

	canonical, err := governance.CanonicalizeEventReceipt(eventReceipt)
	require.NoError(t, err)
	assert.Equal(t, vector.CanonicalUTF8, string(canonical))

	publicKey, err := hex.DecodeString(vector.PublicKeyHex)
	require.NoError(t, err)
	signature, err := hex.DecodeString(eventReceipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))
}

func TestEventReceiptStageEvidenceCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var vector actionEventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(actionEventReceiptStageEvidenceCanonicalizationVectorJSON, &vector))
	eventReceipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(vector.EventReceipt, eventReceipt))
	require.Len(t, eventReceipt.DeterministicStageEvidence, 2)

	canonical, err := governance.CanonicalizeEventReceipt(eventReceipt)
	require.NoError(t, err)
	assert.Equal(t, vector.CanonicalUTF8, string(canonical))
	publicKey, err := hex.DecodeString(vector.PublicKeyHex)
	require.NoError(t, err)
	signature, err := hex.DecodeString(eventReceipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))

	t.Run("stage order", func(t *testing.T) {
		tampered := proto.Clone(eventReceipt).(*operatorv1.EventReceipt)
		tampered.DeterministicStageEvidence[0], tampered.DeterministicStageEvidence[1] = tampered.DeterministicStageEvidence[1], tampered.DeterministicStageEvidence[0]
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})

	t.Run("stage field", func(t *testing.T) {
		tampered := proto.Clone(eventReceipt).(*operatorv1.EventReceipt)
		tampered.DeterministicStageEvidence[0].DoctrineBundleHash = "sha256:tampered"
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})
}

func TestActionEventReceiptFailureCodeCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var vector actionEventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(actionEventReceiptFailureCodeCanonicalizationVectorJSON, &vector))
	eventReceipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(vector.EventReceipt, eventReceipt))
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, eventReceipt.Status)
	assert.Equal(t, operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, eventReceipt.FailureCode)

	canonical, err := governance.CanonicalizeEventReceipt(eventReceipt)
	require.NoError(t, err)
	assert.Equal(t, vector.CanonicalUTF8, string(canonical))
	assert.Contains(t, string(canonical), `"failure_code":1`)

	publicKey, err := hex.DecodeString(vector.PublicKeyHex)
	require.NoError(t, err)
	signature, err := hex.DecodeString(eventReceipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))

	t.Run("failure code tampering", func(t *testing.T) {
		tampered := proto.Clone(eventReceipt).(*operatorv1.EventReceipt)
		tampered.FailureCode = operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})

	t.Run("failure code removal", func(t *testing.T) {
		tampered := proto.Clone(eventReceipt).(*operatorv1.EventReceipt)
		tampered.FailureCode = operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_UNSPECIFIED
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.NotContains(t, string(payload), "failure_code")
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})
}

func TestEventReceiptRejectionCodeCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var vector actionEventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(actionEventReceiptRejectionCodeCanonicalizationVectorJSON, &vector))
	eventReceipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(vector.EventReceipt, eventReceipt))
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, eventReceipt.Status)
	assert.Equal(t, operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, eventReceipt.FailureCode)
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET, eventReceipt.RejectionCode)

	canonical, err := governance.CanonicalizeEventReceipt(eventReceipt)
	require.NoError(t, err)
	assert.Equal(t, vector.CanonicalUTF8, string(canonical))
	assert.Contains(t, string(canonical), `"rejection_code":19`)

	publicKey, err := hex.DecodeString(vector.PublicKeyHex)
	require.NoError(t, err)
	signature, err := hex.DecodeString(eventReceipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))

	t.Run("rejection code tampering", func(t *testing.T) {
		tampered := proto.Clone(eventReceipt).(*operatorv1.EventReceipt)
		tampered.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})

	t.Run("rejection code removal", func(t *testing.T) {
		tampered := proto.Clone(eventReceipt).(*operatorv1.EventReceipt)
		tampered.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED
		payload, canonicalErr := governance.CanonicalizeEventReceipt(tampered)
		require.NoError(t, canonicalErr)
		assert.NotContains(t, string(payload), "rejection_code")
		assert.False(t, ed25519.Verify(publicKey, payload, signature))
	})
}

func TestEventReceiptPersistenceAttestationCanonicalizationMatchesCrossLanguageVector(t *testing.T) {
	var eventReceiptVector actionEventReceiptCanonicalizationVector
	require.NoError(t, json.Unmarshal(actionEventReceiptCanonicalizationVectorJSON, &eventReceiptVector))
	var persistenceVector eventReceiptPersistenceCanonicalizationVector
	require.NoError(t, json.Unmarshal(eventReceiptPersistenceCanonicalizationVectorJSON, &persistenceVector))

	eventReceipt := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(eventReceiptVector.EventReceipt, eventReceipt))
	attestation := &operatorv1.EventReceiptPersistenceAttestation{}
	require.NoError(t, protojson.Unmarshal(persistenceVector.Attestation, attestation))
	eventReceipt.FinalPersistenceAttestation = attestation

	canonical, err := governance.CanonicalizeEventReceiptPersistenceAttestation(attestation)
	require.NoError(t, err)
	assert.Equal(t, persistenceVector.CanonicalHex, hex.EncodeToString(canonical))

	publicKey, err := hex.DecodeString(persistenceVector.PublicKeyHex)
	require.NoError(t, err)
	assert.NoError(t, governance.VerifyEventReceiptPersistenceAttestation(eventReceipt, publicKey))
}
