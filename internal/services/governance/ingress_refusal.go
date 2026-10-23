// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"errors"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ingressRefusalCodes maps each pre-envelope sentinel to the refusal it names.
// First match wins, so list the most specific sentinel first.
var ingressRefusalCodes = []struct {
	sentinel error
	code     operatorv1.IngressRefusalCode
}{
	{constants.ErrMethodNotAllowed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED},
	{constants.ErrJSONRPCMethodNotFound, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED},

	{constants.ErrPayloadExceedsLimit, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_PAYLOAD_TOO_LARGE},

	{constants.ErrRequestBodyRead, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{constants.ErrInvalidJSONBody, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{constants.ErrPubSubEmptyPayload, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{constants.ErrTxInvalidEnvelope, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{constants.ErrGatewayInvalidToolArguments, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{constants.ErrJSONRPCBatchUnsupported, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{constants.ErrJSONRPCVersionInvalid, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{constants.ErrJSONRPCMethodRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},
	{constants.ErrTribunalTemplateInvalid, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST},

	{constants.ErrMissingRequiredField, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{constants.ErrGatewayToolNameRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{constants.ErrGatewaySkillNameRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{constants.ErrGatewayOperatorSessionIDRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{constants.ErrGatewayCollectionRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{constants.ErrGatewayDocumentIDRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{constants.ErrGatewayFieldPathRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{constants.ErrGatewayURIRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},
	{constants.ErrGatewayNameRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD},

	{constants.ErrTxUnknownEventType, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE},
	{constants.ErrTxEventNotRequest, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE},
	{constants.ErrTxEventNotGoverned, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE},
	{constants.ErrTxOutcomeNotRegistered, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE},

	{constants.ErrTxEventActionMismatch, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_EVENT_ACTION_MISMATCH},
	{constants.ErrTxOutcomeNotAllowed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_EVENT_ACTION_MISMATCH},

	{constants.ErrTxPayloadDecodeFailed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_PAYLOAD_DECODE_FAILED},

	{constants.ErrMTLSRequired, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MTLS_REQUIRED},
	{constants.ErrCertURISANMissing, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MTLS_REQUIRED},

	{constants.ErrURISANMismatch, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_IDENTITY_BINDING_FAILED},
	{constants.ErrIdentityBindingFailed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_IDENTITY_BINDING_FAILED},

	{constants.ErrEnvelopePostureCallerSupplied, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_CALLER_SUPPLIED_POSTURE},

	{constants.ErrGatewayOperatorSessionInvalid, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND},

	{constants.ErrWitnessCommandNotCapable, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_WITNESS_NOT_CAPABLE},

	{constants.ErrTxL1ValidationFailed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE},
	{constants.ErrGatewayForbiddenPattern, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE},

	{constants.ErrTxL3ProofUnmintable, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L3_PROOF_UNMINTABLE},

	{constants.ErrGatewayNotReady, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE},
	{constants.ErrGatewayDownstreamUnavailable, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE},
	{constants.ErrWardenVaultLocked, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE},

	{constants.ErrEnvelopePostureMissing, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrInvalidPosture, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrTxDoctrineMissing, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrTxPayloadDecoderMissing, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrGatewayNoA2ADownstreamConfigured, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrGatewayNoDownstreamConfigured, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrGatewayFieldPathRegistryNotInit, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrGatewayDatabaseServiceNotConfigured, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrEnvelopeProcessorNotInit, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},
	{constants.ErrTxL3NotaryNotConfigured, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED},

	{constants.ErrEmbeddedOperatorUnclaimed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_EMBEDDED_OPERATOR_UNCLAIMED},

	{constants.ErrIngressL2DeliberationFailed, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L2_DELIBERATION_FAILED},

	{constants.ErrIngressStateRootUnavailable, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_STATE_ROOT_UNAVAILABLE},
}

// ClassifyIngressRefusal names the pre-envelope check behind err. An unmapped
// error is UNCLASSIFIED, never a guessed check; a nil error is UNSPECIFIED.
func ClassifyIngressRefusal(err error) operatorv1.IngressRefusalCode {
	code, _ := IngressRefusalSentinel(err)
	return code
}

// IngressRefusalSentinel returns the code of the sentinel err wraps, and that sentinel. Ingresses
// render the sentinel text, never the wrapped chain. An unmapped error returns
// a nil sentinel and UNCLASSIFIED; a nil error returns UNSPECIFIED.
func IngressRefusalSentinel(err error) (operatorv1.IngressRefusalCode, error) {
	if err == nil {
		return operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED, nil
	}
	for _, entry := range ingressRefusalCodes {
		if errors.Is(err, entry.sentinel) {
			return entry.code, entry.sentinel
		}
	}
	return operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED, nil
}

// NewIngressRefusal builds the wire body for a pre-envelope refusal. Error is
// the sentinel's text; an unmapped error renders constants.ErrInternal.
func NewIngressRefusal(err error) (refusal *operatorv1.IngressRefusal) {
	defer func() {
		if refusal != nil {
			refusal.ConsensusInferences = ConsensusInferences(err)
		}
	}()
	code, sentinel := IngressRefusalSentinel(err)
	text := constants.ErrInternal.Error()
	if sentinel != nil {
		text = sentinel.Error()
	}
	return &operatorv1.IngressRefusal{Code: code, Error: text}
}
