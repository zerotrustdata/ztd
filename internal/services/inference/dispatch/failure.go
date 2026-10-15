// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package dispatch

import (
	"errors"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// failureCodes is the single mapping between the public sentinels
// POST /api/v1/inference/dispatch returns and their wire codes. The Gateway
// stamps the code on a failure body; a caller maps it back to the sentinel.
var failureCodes = []struct {
	sentinel error
	code     operatorv1.InferenceDispatchFailureCode
}{
	{constants.ErrInternal, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_INTERNAL},
	{constants.ErrInferenceOutcomeUnknown, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_OUTCOME_UNKNOWN},
	{constants.ErrDispatchResultTimeout, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_RESULT_TIMEOUT},
	{constants.ErrInferenceOperatorNotFound, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_OPERATOR_NOT_FOUND},
	{constants.ErrInferenceOperatorAmbiguous, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_OPERATOR_AMBIGUOUS},
	{constants.ErrInferenceOperatorNotCapable, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_OPERATOR_NOT_CAPABLE},
	{constants.ErrInferenceRoleInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_ROLE_INVALID},
	{constants.ErrInferenceMessagesRequired, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_MESSAGES_REQUIRED},
	{constants.ErrInferenceProviderAttemptRequired, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_PROVIDER_ATTEMPT_REQUIRED},
	{constants.ErrInferenceMessageInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_MESSAGE_INVALID},
	{constants.ErrInferenceJSONInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_JSON_INVALID},
	{constants.ErrInferenceJSONNonCanonical, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_JSON_NON_CANONICAL},
	{constants.ErrInferenceToolSchemaInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_TOOL_SCHEMA_INVALID},
	{constants.ErrInferenceGenerationOptionsInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GENERATION_OPTIONS_INVALID},
	{constants.ErrInferenceCapabilityUnsupported, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_CAPABILITY_UNSUPPORTED},
	{constants.ErrInferenceToolsUnsupported, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_TOOLS_UNSUPPORTED},
	{constants.ErrInferenceContextOverflow, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_CONTEXT_OVERFLOW},
	{constants.ErrInferenceModelRefInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_MODEL_REF_INVALID},
	{constants.ErrInferenceModelOverrideDenied, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_MODEL_OVERRIDE_DENIED},
	{constants.ErrInferenceModelRegistryInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_MODEL_REGISTRY_INVALID},
	{constants.ErrInferenceCampaignBindingInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_CAMPAIGN_BINDING_INVALID},
	{constants.ErrInferenceGovernanceRejected, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED},
	{constants.ErrDispatchNoDelivery, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_NO_DELIVERY},
	{constants.ErrInferenceReceiptFailed, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_RECEIPT_FAILED},
	{constants.ErrInferenceReceiptVerify, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_RECEIPT_VERIFY},
	{constants.ErrInferenceResultDigestMismatch, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_RESULT_DIGEST_MISMATCH},
	{constants.ErrInferenceResultDigest, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_RESULT_DIGEST},
	{constants.ErrInferenceIdentityMismatch, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_IDENTITY_MISMATCH},
	{constants.ErrInferenceModelDigestMismatch, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_MODEL_DIGEST_MISMATCH},
	{constants.ErrInferenceEvidenceHashInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_EVIDENCE_HASH_INVALID},
	{constants.ErrInferenceResultDecode, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_RESULT_DECODE},
	{constants.ErrInferenceCompletionNoReceipt, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_COMPLETION_NO_RECEIPT},
	{constants.ErrInferenceCompletionNoResult, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_COMPLETION_NO_RESULT},
	{constants.ErrInferenceBackendUnavailable, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_UNAVAILABLE},
	{constants.ErrInferenceBackendNotRegistered, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_NOT_REGISTERED},
	{constants.ErrInferenceBackendTimeout, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_TIMEOUT},
	{constants.ErrInferenceGenerateFailed, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GENERATE_FAILED},
	{constants.ErrInferenceModelNotFound, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_MODEL_NOT_FOUND},
	{constants.ErrInferenceProviderResponseInvalid, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_PROVIDER_RESPONSE_INVALID},
	{constants.ErrInferenceCanceled, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_CANCELED},
	{constants.ErrInferenceCallerDisconnected, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_CALLER_DISCONNECTED},
	{constants.ErrInferenceProgressBackpressure, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_PROGRESS_BACKPRESSURE},
	{constants.ErrInferenceProgressHashMismatch, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_PROGRESS_HASH_MISMATCH},
}

// FailureCode returns the wire code for a public dispatch sentinel, or
// UNSPECIFIED when the sentinel has none (a request fault rejected before
// dispatch).
func FailureCode(sentinel error) operatorv1.InferenceDispatchFailureCode {
	for _, entry := range failureCodes {
		if errors.Is(sentinel, entry.sentinel) {
			return entry.code
		}
	}
	return operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_UNSPECIFIED
}

// FailureSentinel returns the public sentinel a wire code names, or nil for
// UNSPECIFIED and unknown codes.
func FailureSentinel(code operatorv1.InferenceDispatchFailureCode) error {
	for _, entry := range failureCodes {
		if entry.code == code {
			return entry.sentinel
		}
	}
	return nil
}
