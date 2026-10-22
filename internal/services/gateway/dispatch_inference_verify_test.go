// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// --- helpers for verifyInferenceCompletion tests ---

// signTestEventReceipt signs an EventReceipt's canonical form and attaches a
// signed EventReceiptPersistenceAttestation, mirroring L5Actuator.signEventReceipt and
// signEventReceiptPersistenceAttestation.
func signTestEventReceipt(t *testing.T, eventReceipt *operatorv1.EventReceipt, signerPriv ed25519.PrivateKey, keyID string) {
	t.Helper()
	eventReceipt.SignerKeyId = keyID
	canonical, err := governance.CanonicalizeEventReceipt(eventReceipt)
	require.NoError(t, err)
	eventReceipt.Signature = hex.EncodeToString(ed25519.Sign(signerPriv, canonical))

	attestation := &operatorv1.EventReceiptPersistenceAttestation{
		TransactionId:               eventReceipt.TransactionId,
		EventReceiptSignatureDigest: governance.SignatureDigest([]string{eventReceipt.Signature}),
		PersistedAtUnixMs:           time.Now().UnixMilli(),
		AuditRecordId:               eventReceipt.TransactionId,
		SignerKeyId:                 keyID,
	}
	attestationPayload, err := governance.CanonicalizeEventReceiptPersistenceAttestation(attestation)
	require.NoError(t, err)
	attestation.Signature = hex.EncodeToString(ed25519.Sign(signerPriv, attestationPayload))
	eventReceipt.FinalPersistenceAttestation = attestation
}

// inferenceTestResult returns a complete InferenceResult with its canonical
// digest stamped, mirroring handleInferenceRequestSync.
func inferenceTestResult(t *testing.T) *operatorv1.InferenceResult {
	t.Helper()
	result := &operatorv1.InferenceResult{
		Parts:                 []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: "generated output"}}},
		PromptTokens:          7,
		CompletionTokens:      11,
		TotalTokens:           18,
		UsageReported:         true,
		FinishReason:          "stop",
		Model:                 "gemma3:4b",
		RequestedModel:        "gemma3:4b",
		ProviderAttemptId:     "provider-attempt-1",
		NormalizedRequestHash: strings.Repeat("1", 64),
		OutputHash:            strings.Repeat("2", 64),
		RetryClassification:   models.ClassifyRetry(0),
		LoadState:             models.ClassifyLoadState(nil),
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	return result
}

// newInferenceDispatchService builds a DispatchService whose signer store is
// the given store; other dependencies are stubs that are not exercised by
// verifyInferenceCompletion.
func newInferenceDispatchService(t *testing.T, signerStore EventReceiptSignerResolver) *DispatchService {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return NewDispatchService(
		logger,
		NewGatewayWebSocketHandler(logger),
		&stubStateRootProvider{root: "root-abc"},
		&stubOperatorSessionValidator{op: &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}},
		"doctrine",
		governance.NewL1Doctrine(),
		nil,
		signerStore,
		nil,
		nil,
	)
}

// --- verifyInferenceCompletion table-driven tests ---

func TestVerifyInferenceCompletion(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"

	const txID = "tx-inference-001"
	const txHash = "hash-inference-001"

	requestPayload, err := proto.Marshal(&operatorv1.InferenceRequested{
		Model:             "gemma3:4b",
		ProviderAttemptId: "provider-attempt-1",
	})
	require.NoError(t, err)
	cmdEnv := &commonv1.GovernanceEnvelope{Id: txID, TransactionHash: txHash, Payload: requestPayload}

	// resultEnvelopeFor marshals a completion into a result envelope payload.
	resultEnvelopeFor := func(t *testing.T, completion *operatorv1.InferenceCompletion) *commonv1.GovernanceEnvelope {
		t.Helper()
		payload, err := proto.Marshal(completion)
		require.NoError(t, err)
		return &commonv1.GovernanceEnvelope{Id: txID, Payload: payload}
	}

	// completedEventReceipt returns a signed COMPLETED event receipt whose
	// ResultSummary binds the given result's canonical digest.
	completedEventReceipt := func(t *testing.T, result *operatorv1.InferenceResult) *operatorv1.EventReceipt {
		t.Helper()
		digest, err := models.ComputeInferenceResultDigest(result)
		require.NoError(t, err)
		eventReceipt := &operatorv1.EventReceipt{
			TransactionId:    txID,
			TransactionHash:  txHash,
			Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ResultSummary:    digest,
			StateRootBefore:  "root-before",
			StateRootAfter:   "root-after",
			ExecutedAtUnixMs: 1700000000000,
		}
		signTestEventReceipt(t, eventReceipt, priv, keyID)
		return eventReceipt
	}

	t.Run("success returns verified event receipt and result", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		out, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.NoError(t, err)
		assert.Equal(t, txID, out.TransactionID)
		assert.Same(t, resultEnv, out.ResultEnvelope)
		require.NotNil(t, out.EventReceipt)
		assert.Equal(t, eventReceipt.TransactionId, out.EventReceipt.TransactionId)
		assert.Equal(t, eventReceipt.Signature, out.EventReceipt.Signature)
		assert.Equal(t, eventReceipt.ResultSummary, out.EventReceipt.ResultSummary)
		require.NotNil(t, out.InferenceResult)
		assert.True(t, proto.Equal(result, out.InferenceResult))
		assert.Equal(t, result.ResultDigest, out.InferenceResult.ResultDigest)
	})

	t.Run("malformed payload fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		resultEnv := &commonv1.GovernanceEnvelope{Id: txID, Payload: []byte{0xff, 0xff, 0xff, 0xff}}

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceResultDecode)
	})

	t.Run("missing event receipt fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{Result: inferenceTestResult(t)})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceCompletionNoEventReceipt)
	})

	t.Run("event receipt transaction identity mismatch fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		eventReceipt.TransactionId = "tx-other"
		signTestEventReceipt(t, eventReceipt, priv, keyID)
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceEventReceiptVerify)
	})

	t.Run("nil signer store fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, nil)
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceEventReceiptVerify)
	})

	t.Run("unknown signer key id fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		eventReceipt.SignerKeyId = "unregistered-key"
		signTestEventReceipt(t, eventReceipt, priv, "unregistered-key")
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceEventReceiptVerify)
	})

	t.Run("signer store error fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{err: constants.ErrTrustedSignerKeyNotFound})
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceEventReceiptVerify)
	})

	t.Run("event receipt signature from wrong key fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		_, otherPriv, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		signTestEventReceipt(t, eventReceipt, otherPriv, keyID)
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		_, err = svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceEventReceiptVerify)
	})

	t.Run("missing persistence attestation fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		eventReceipt.FinalPersistenceAttestation = nil
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceEventReceiptVerify)
	})

	t.Run("failed event receipt terminates as typed failure", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		eventReceipt := &operatorv1.EventReceipt{
			TransactionId:    txID,
			TransactionHash:  txHash,
			Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			ResultSummary:    "backend unavailable",
			ExecutedAtUnixMs: 1700000000000,
		}
		signTestEventReceipt(t, eventReceipt, priv, keyID)
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceEventReceiptFailed)
		failed := governance.FailedEventReceipt(err)
		require.NotNil(t, failed)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, failed.Status)
		assert.Equal(t, txID, failed.TransactionId)
	})

	t.Run("completed event receipt without result fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		eventReceipt := &operatorv1.EventReceipt{
			TransactionId:    txID,
			TransactionHash:  txHash,
			Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ResultSummary:    "digest",
			ExecutedAtUnixMs: 1700000000000,
		}
		signTestEventReceipt(t, eventReceipt, priv, keyID)
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceCompletionNoResult)
	})

	t.Run("result part substitution after digest fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		// Substitute the text after the digest was computed: the recomputed
		// digest no longer matches the stamped digest or the event receipt summary.
		result.Parts[0].Part = &operatorv1.InferenceResponsePart_Text{Text: "substituted output"}
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceResultDigestMismatch)
	})

	t.Run("event receipt summary substitution fails closed", func(t *testing.T) {
		svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
		result := inferenceTestResult(t)
		eventReceipt := completedEventReceipt(t, result)
		eventReceipt.ResultSummary = "truncated text summary"
		signTestEventReceipt(t, eventReceipt, priv, keyID)
		resultEnv := resultEnvelopeFor(t, &operatorv1.InferenceCompletion{EventReceipt: eventReceipt, Result: result})

		_, err := svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceResultDigestMismatch)
	})
}

// TestVerifyInferenceCompletion_FailureCodeMapping proves that a signed
// FAILED event receipt's typed failure_code maps back to the corresponding
// typed sentinel on the gateway side, so the controller can distinguish
// governance rejections, client faults, and provider failures without
// string-matching the event receipt summary.
func TestVerifyInferenceCompletion_FailureCodeMapping(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"

	const txID = "tx-inference-001"
	const txHash = "hash-inference-001"
	cmdEnv := &commonv1.GovernanceEnvelope{Id: txID, TransactionHash: txHash}

	failedEventReceiptWithCode := func(t *testing.T, code operatorv1.EventReceiptFailureCode) *operatorv1.EventReceipt {
		t.Helper()
		eventReceipt := &operatorv1.EventReceipt{
			TransactionId:    txID,
			TransactionHash:  txHash,
			Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			ResultSummary:    "rejected upstream",
			ExecutedAtUnixMs: 1700000000000,
			FailureCode:      code,
		}
		signTestEventReceipt(t, eventReceipt, priv, keyID)
		return eventReceipt
	}

	tests := []struct {
		name string
		code operatorv1.EventReceiptFailureCode
		want error
	}{
		{name: "governance rejected", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, want: constants.ErrInferenceGovernanceRejected},
		{name: "model override denied", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_OVERRIDE_DENIED, want: constants.ErrInferenceModelOverrideDenied},
		{name: "role invalid", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_ROLE_INVALID, want: constants.ErrInferenceRoleInvalid},
		{name: "model ref invalid", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_REF_INVALID, want: constants.ErrInferenceModelRefInvalid},
		{name: "backend unavailable", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE, want: constants.ErrInferenceBackendUnavailable},
		{name: "backend timeout", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_BACKEND_TIMEOUT, want: constants.ErrInferenceBackendTimeout},
		{name: "generate failed", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GENERATE_FAILED, want: constants.ErrInferenceGenerateFailed},
		{name: "model not found", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_NOT_FOUND, want: constants.ErrInferenceModelNotFound},
		{name: "provider response invalid", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_PROVIDER_RESPONSE_INVALID, want: constants.ErrInferenceProviderResponseInvalid},
		{name: "generation options invalid", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GENERATION_OPTIONS_INVALID, want: constants.ErrInferenceGenerationOptionsInvalid},
		{name: "capability unsupported", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_CAPABILITY_UNSUPPORTED, want: constants.ErrInferenceCapabilityUnsupported},
		{name: "tools unsupported", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_TOOLS_UNSUPPORTED, want: constants.ErrInferenceToolsUnsupported},
		{name: "context overflow", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_CONTEXT_OVERFLOW, want: constants.ErrInferenceContextOverflow},
		{name: "provider attempt required", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_PROVIDER_ATTEMPT_REQUIRED, want: constants.ErrInferenceProviderAttemptRequired},
		{name: "identity mismatch", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_IDENTITY_MISMATCH, want: constants.ErrInferenceIdentityMismatch},
		{name: "model digest mismatch", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_DIGEST_MISMATCH, want: constants.ErrInferenceModelDigestMismatch},
		{name: "evidence hash invalid", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_EVIDENCE_HASH_INVALID, want: constants.ErrInferenceEvidenceHashInvalid},
		{name: "model registry invalid", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_REGISTRY_INVALID, want: constants.ErrInferenceModelRegistryInvalid},
		{name: "campaign binding invalid", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_CAMPAIGN_BINDING_INVALID, want: constants.ErrInferenceCampaignBindingInvalid},
		{name: "generic execution failure", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_EXECUTION_FAILED, want: constants.ErrInferenceEventReceiptFailed},
		{name: "unspecified code falls back to generic failure", code: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_UNSPECIFIED, want: constants.ErrInferenceEventReceiptFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newInferenceDispatchService(t, &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}})
			eventReceipt := failedEventReceiptWithCode(t, tt.code)
			payload, err := proto.Marshal(&operatorv1.InferenceCompletion{EventReceipt: eventReceipt})
			require.NoError(t, err)
			resultEnv := &commonv1.GovernanceEnvelope{Id: txID, Payload: payload}

			_, err = svc.verifyInferenceCompletion(context.Background(), cmdEnv, resultEnv)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want, "failure_code %v must map to the typed sentinel", tt.code)
		})
	}
}
