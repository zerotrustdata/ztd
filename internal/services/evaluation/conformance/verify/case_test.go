// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package verify

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestVerifyErrorClassMapsEachVerificationSentinel(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantClass evalv1.ConformanceFailureClass
	}{
		{"anchor unknown", constants.ErrConformanceAnchorUnknown, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_ANCHOR_UNKNOWN},
		{"receipt unclassifiable", constants.ErrConformanceReceiptUnclassifiable, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_RECEIPT_UNCLASSIFIABLE},
		{"stage chain invalid", constants.ErrInvalidEvidenceGraph, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_STAGE_CHAIN_INVALID},
		{"action receipt signature invalid", constants.ErrActionReceiptSignatureInvalid, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
		{"attestation signature invalid", constants.ErrReceiptPersistenceAttestationInvalid, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
		{"attestation digest mismatch", constants.ErrReceiptPersistenceSignatureMismatch, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
		{"wrapped anchor unknown", fmt.Errorf("oracle: resolve: %w", constants.ErrConformanceAnchorUnknown), evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_ANCHOR_UNKNOWN},
		{"unmapped error fails closed", errors.New("unrelated failure"), evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantClass, verifyErrorClass(tt.err))
		})
	}
}

func TestInvalidEvidenceClassSeparatesForgeryFromBehaviour(t *testing.T) {
	invalid := []evalv1.ConformanceFailureClass{
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_ANCHOR_UNKNOWN,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_STAGE_CHAIN_INVALID,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_RECEIPT_UNCLASSIFIABLE,
	}
	for _, class := range invalid {
		assert.True(t, invalidEvidenceClass(class), class.String())
	}
	behaviour := []evalv1.ConformanceFailureClass{
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OUTCOME_MISMATCH,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CODE_MISMATCH,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_LEDGER_GAP,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNCLAIMED_EXECUTION,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_EFFECT_MISSING,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNTYPED_OUTCOME,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING,
	}
	for _, class := range behaviour {
		assert.False(t, invalidEvidenceClass(class), class.String())
	}
}

func TestEffectOfReadsTheLastExecutedExpectation(t *testing.T) {
	tests := []struct {
		name   string
		expect []cases.Expect
		want   cases.Effect
	}{
		{"no executed expectation", []cases.Expect{{Kind: cases.ExpectRejected, Effect: cases.EffectTargetMarker}}, cases.EffectNone},
		{"single executed expectation", []cases.Expect{{Kind: cases.ExpectExecuted, Effect: cases.EffectTargetMarker}}, cases.EffectTargetMarker},
		{"last executed expectation wins", []cases.Expect{
			{Kind: cases.ExpectExecuted, Effect: cases.EffectNone},
			{Kind: cases.ExpectRejected, Effect: cases.EffectNone},
			{Kind: cases.ExpectExecuted, Effect: cases.EffectA2AStubCall},
		}, cases.EffectA2AStubCall},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, effectOf(cases.Case{Expect: tt.expect}))
		})
	}
}

func TestRejectionCodeMatchesNeverAcceptsUnspecifiedOrUnclassified(t *testing.T) {
	l1 := operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE
	unclassified := operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED
	unspecified := operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED

	assert.True(t, rejectionCodeMatches(l1, l1))
	assert.False(t, rejectionCodeMatches(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY, l1))
	assert.False(t, rejectionCodeMatches(unclassified, unclassified))
	assert.False(t, rejectionCodeMatches(unspecified, unspecified))
}

func TestRefusalCodeMatchesNeverAcceptsUnspecifiedOrUnclassified(t *testing.T) {
	missing := operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD
	unclassified := operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED
	unspecified := operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED

	assert.True(t, refusalCodeMatches(missing, missing))
	assert.False(t, refusalCodeMatches(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST, missing))
	assert.False(t, refusalCodeMatches(unclassified, unclassified))
	assert.False(t, refusalCodeMatches(unspecified, unspecified))
}
