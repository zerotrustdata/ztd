// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// The ordinary vector tests verify signatures, but regenerating both the
// schema and vectors can hide a break in already-sealed evidence. These pins
// identify the original signed examples; a naming cleanup must still verify
// those examples rather than replace them with newly signed evidence.
func TestReceiptHistoryKeepsOriginalSignedVectors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      []byte
		signature string
	}{
		{"receipt", actionReceiptCanonicalizationVectorJSON, "01d60a4fc5d5b2f33ab0208b14efde29fb37f8e8dc5db0a99b4962c3b28676c432b887b9ce3d1d94a31a345c3d5773dd37dd1e2da5a6cb9e89e2d00a029ef603"},
		{"staged receipt", actionReceiptStageEvidenceCanonicalizationVectorJSON, "5e940060f02f72fee66b1ac0714ad44b8c97fa8775dedcf979794870b955b1d70b063974570767f01e9fb178ed81b47b44757e070628dd3af16477dd4be72309"},
		{"persistence", receiptPersistenceCanonicalizationVectorJSON, "8fee4aa18c1beb8fc52b7ee371ff82a727f75430c672e694cf3d4cafc0f5dfb33a4a28ba79d405c7b3a4b6c61876f0a05a90f9a197b96039fd34d47c69ba9c03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var vector struct {
				Receipt struct {
					Signature string `json:"signature"`
				} `json:"receipt"`
				Attestation struct {
					Signature string `json:"signature"`
				} `json:"attestation"`
			}
			require.NoError(t, json.Unmarshal(tc.body, &vector))
			require.Contains(t, []string{vector.Receipt.Signature, vector.Attestation.Signature}, tc.signature)
		})
	}
}

func TestReceiptHistoryKeepsWireFieldNumbersAndNames(t *testing.T) {
	for _, tc := range []struct {
		message protoreflect.MessageDescriptor
		name    protoreflect.Name
		number  protoreflect.FieldNumber
	}{
		{(&operatorv1.ActionReceipt{}).ProtoReflect().Descriptor(), "l2_status", 11},
		{(&operatorv1.ActionReceipt{}).ProtoReflect().Descriptor(), "l3_status", 12},
		{(&operatorv1.ActionReceipt{}).ProtoReflect().Descriptor(), "deterministic_stage_evidence", 13},
		{(&operatorv1.ActionReceipt{}).ProtoReflect().Descriptor(), "final_persistence_attestation", 14},
		{(&operatorv1.ActionReceipt{}).ProtoReflect().Descriptor(), "failure_code", 15},
		{(&operatorv1.ActionReceipt{}).ProtoReflect().Descriptor(), "event_type", 16},
		{(&operatorv1.ActionReceipt{}).ProtoReflect().Descriptor(), "action_type", 17},
		{(&operatorv1.ActionReceipt{}).ProtoReflect().Descriptor(), "rejection_code", 18},
		{(&operatorv1.DeterministicStageEvidence{}).ProtoReflect().Descriptor(), "receipt_signature_digest", 21},
		{(&operatorv1.ReceiptPersistenceAttestation{}).ProtoReflect().Descriptor(), "receipt_signature_digest", 2},
		{(&evalv1.GovernedActionBinding{}).ProtoReflect().Descriptor(), "receipt_ref", 6},
		{(&evalv1.ModelInferenceRecord{}).ProtoReflect().Descriptor(), "governed_receipt_ref", 30},
		{(&evalv1.PublicGovernedActionActivityRecord{}).ProtoReflect().Descriptor(), "receipt_status", 3},
	} {
		t.Run(string(tc.message.FullName())+"/"+string(tc.name), func(t *testing.T) {
			field := tc.message.Fields().ByName(tc.name)
			require.NotNil(t, field, "sealed evidence uses this field name")
			require.Equal(t, tc.number, field.Number())
		})
	}
}
