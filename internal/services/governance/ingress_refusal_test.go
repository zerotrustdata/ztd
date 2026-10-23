// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ingressRefusalCases covers every entry in ingressRefusalCodes, one row per
// sentinel, in the same order as the classifier table.
var ingressRefusalCases = []struct {
	name     string
	sentinel error
	code     operatorv1.IngressRefusalCode
}{
	{"method not allowed", constants.ErrMethodNotAllowed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED},
	{"json-rpc method not found", constants.ErrJSONRPCMethodNotFound, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED},

	{"payload exceeds limit", constants.ErrPayloadExceedsLimit, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_PAYLOAD_TOO_LARGE},

	{"request body read", constants.ErrRequestBodyRead, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{"invalid JSON body", constants.ErrInvalidJSONBody, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{"pubsub empty payload", constants.ErrPubSubEmptyPayload, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{"tribunal template invalid", constants.ErrTribunalTemplateInvalid, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{"tx invalid envelope", constants.ErrTxInvalidEnvelope, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{"gateway invalid tool arguments", constants.ErrGatewayInvalidToolArguments, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{"json-rpc batch unsupported", constants.ErrJSONRPCBatchUnsupported, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{"json-rpc version invalid", constants.ErrJSONRPCVersionInvalid, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{"json-rpc method required", constants.ErrJSONRPCMethodRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},

	{"missing required field", constants.ErrMissingRequiredField, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{"tool name required", constants.ErrGatewayToolNameRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{"skill name required", constants.ErrGatewaySkillNameRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{"operator session id required", constants.ErrGatewayOperatorSessionIDRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{"collection required", constants.ErrGatewayCollectionRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{"document id required", constants.ErrGatewayDocumentIDRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{"field path required", constants.ErrGatewayFieldPathRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{"URI required", constants.ErrGatewayURIRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{"name required", constants.ErrGatewayNameRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},

	{"tx unknown event type", constants.ErrTxUnknownEventType, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE},
	{"tx event not request", constants.ErrTxEventNotRequest, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE},
	{"tx event not governed", constants.ErrTxEventNotGoverned, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE},
	{"tx outcome not registered", constants.ErrTxOutcomeNotRegistered, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE},

	{"tx event action mismatch", constants.ErrTxEventActionMismatch, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_EVENT_ACTION_MISMATCH},
	{"tx outcome not allowed", constants.ErrTxOutcomeNotAllowed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_EVENT_ACTION_MISMATCH},

	{"tx payload decode failed", constants.ErrTxPayloadDecodeFailed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_PAYLOAD_DECODE_FAILED},

	{"mTLS required", constants.ErrMTLSRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MTLS_REQUIRED},
	{"cert URI SAN missing", constants.ErrCertURISANMissing, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MTLS_REQUIRED},

	{"URI SAN mismatch", constants.ErrURISANMismatch, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_IDENTITY_BINDING_FAILED},
	{"identity binding failed", constants.ErrIdentityBindingFailed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_IDENTITY_BINDING_FAILED},

	{"caller supplied posture", constants.ErrEnvelopePostureCallerSupplied, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_CALLER_SUPPLIED_POSTURE},

	{"gateway operator session invalid", constants.ErrGatewayOperatorSessionInvalid, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND},

	{"witness command not capable", constants.ErrWitnessCommandNotCapable, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_WITNESS_NOT_CAPABLE},

	{"L1 validation failed", constants.ErrTxL1ValidationFailed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE},
	{"gateway forbidden pattern", constants.ErrGatewayForbiddenPattern, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE},

	{"L3 proof unmintable", constants.ErrTxL3ProofUnmintable, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L3_PROOF_UNMINTABLE},

	{"gateway not ready", constants.ErrGatewayNotReady, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE},
	{"warden vault locked", constants.ErrWardenVaultLocked, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE},
	{"gateway downstream unavailable", constants.ErrGatewayDownstreamUnavailable, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE},

	{"posture missing", constants.ErrEnvelopePostureMissing, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"invalid posture", constants.ErrInvalidPosture, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"doctrine missing", constants.ErrTxDoctrineMissing, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"l3 notary not configured", constants.ErrTxL3NotaryNotConfigured, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"payload decoder missing", constants.ErrTxPayloadDecoderMissing, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"A2A downstream not configured", constants.ErrGatewayNoA2ADownstreamConfigured, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"downstream not configured", constants.ErrGatewayNoDownstreamConfigured, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"field path registry not initialized", constants.ErrGatewayFieldPathRegistryNotInit, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"database service not configured", constants.ErrGatewayDatabaseServiceNotConfigured, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{"envelope processor not initialized", constants.ErrEnvelopeProcessorNotInit, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},

	{"embedded operator unclaimed", constants.ErrEmbeddedOperatorUnclaimed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_EMBEDDED_OPERATOR_UNCLAIMED},

	{"ingress L2 deliberation failed", constants.ErrIngressL2DeliberationFailed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L2_DELIBERATION_FAILED},

	{"ingress state root unavailable", constants.ErrIngressStateRootUnavailable, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_STATE_ROOT_UNAVAILABLE},
}

// ingressRefusalRejectionOverlaps lists the sentinels that appear in both
// ingressRefusalCodes and governanceRejectionCodes. Each one fires at a
// different time: pre-envelope on dispatch, or at L4 on direct submission.
// A new overlap must be added here as a reviewed change.
var ingressRefusalRejectionOverlaps = []error{
	constants.ErrTxL1ValidationFailed,
	constants.ErrTxEventActionMismatch,
	constants.ErrTxUnknownEventType,
	constants.ErrTxEventNotRequest,
	constants.ErrTxEventNotGoverned,
	constants.ErrTxPayloadDecodeFailed,
	constants.ErrTxInvalidEnvelope,
	constants.ErrEnvelopePostureMissing,
	constants.ErrTxDoctrineMissing,
	constants.ErrTxPayloadDecoderMissing,
	constants.ErrWardenVaultLocked,
	constants.ErrTxL3NotaryNotConfigured,
}

func TestClassifyIngressRefusal(t *testing.T) {
	t.Run("nil error is UNSPECIFIED", func(t *testing.T) {
		assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED, ClassifyIngressRefusal(nil))
	})

	for _, tc := range ingressRefusalCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.code, ClassifyIngressRefusal(tc.sentinel), "bare sentinel")
			assert.Equal(t, tc.code, ClassifyIngressRefusal(fmt.Errorf("ctx: %w", tc.sentinel)), "single-wrapped sentinel")
			assert.Equal(t, tc.code, ClassifyIngressRefusal(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", tc.sentinel))), "double-wrapped sentinel")
		})
	}
}

// TestClassifyIngressRefusalTableCoversEveryEntry fails when a classifier
// entry has no row in the explicit case table, so the table cannot drift.
func TestClassifyIngressRefusalTableCoversEveryEntry(t *testing.T) {
	require.Len(t, ingressRefusalCases, len(ingressRefusalCodes), "table rows must match classifier entries one-to-one")
	for _, entry := range ingressRefusalCodes {
		found := false
		for _, tc := range ingressRefusalCases {
			if errors.Is(entry.sentinel, tc.sentinel) && tc.code == entry.code {
				found = true
				break
			}
		}
		assert.True(t, found, "classifier entry %v -> %v has no table row", entry.sentinel, entry.code)
	}
}

func TestClassifyIngressRefusalUnmapped(t *testing.T) {
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED, ClassifyIngressRefusal(errors.New("x")))
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED, ClassifyIngressRefusal(fmt.Errorf("store: %w", errors.New("x"))))
}

func TestNewIngressRefusalRendersSentinelOnly(t *testing.T) {
	refusal := NewIngressRefusal(fmt.Errorf("store write failed /secret/path: %w", constants.ErrMethodNotAllowed))
	assert.Equal(t, constants.ErrMethodNotAllowed.Error(), refusal.GetError(), "only the sentinel renders, never the chain")
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED, refusal.GetCode())

	unmapped := NewIngressRefusal(fmt.Errorf("store write failed /secret/path: %w", errors.New("disk full")))
	assert.Equal(t, constants.ErrInternal.Error(), unmapped.GetError(), "an unmapped error renders ErrInternal, never the chain")
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED, unmapped.GetCode())

	none := NewIngressRefusal(nil)
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED, none.GetCode())
}

// TestIngressRefusalCodesDisjointFromRejection proves the pre-envelope and L4
// tables share only the reviewed overlaps. Any other shared sentinel must map
// to the same meaning in both, and the test fails until the overlap list is
// updated on purpose.
func TestIngressRefusalCodesDisjointFromRejection(t *testing.T) {
	var overlaps []error
	for _, ingress := range ingressRefusalCodes {
		for _, rejection := range governanceRejectionCodes {
			if errors.Is(ingress.sentinel, rejection.sentinel) {
				overlaps = append(overlaps, ingress.sentinel)
				break
			}
		}
	}
	require.Len(t, overlaps, len(ingressRefusalRejectionOverlaps), "overlap set changed: %v", overlaps)
	for _, want := range ingressRefusalRejectionOverlaps {
		found := false
		for _, got := range overlaps {
			if errors.Is(got, want) {
				found = true
				break
			}
		}
		assert.True(t, found, "expected overlap %v is missing", want)
	}
}
