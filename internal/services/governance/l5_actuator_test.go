// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/uuid"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newTestActuator(t *testing.T) (*L5Actuator, ed25519.PublicKey) {
	t.Helper()

	// Generate L5Actuator signing key
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	// Create mock dependencies
	mockHandler := &mockExecutionHandler{err: nil}
	mockStateRoot := testutil.NewMockStateRootProvider("test-state-root-123")

	logger := slog.Default()

	actuator := &L5Actuator{
		Logger:            logger,
		SigningKey:        privKey,
		KeyID:             "test-Actuator-key",
		ExecutionHandler:  mockHandler,
		StateRootProvider: mockStateRoot,
		Scrubbing:         newL5TestScrubber(t),
	}

	return actuator, pubKey
}

// newL5TestScrubber returns an enabled scrubber, as the production Operator wires one. The actuator
// scrubs the target resource and free-text summaries with it before signing.
func newL5TestScrubber(t *testing.T) *scrubbing.ScrubbingService {
	t.Helper()
	svc, err := scrubbing.NewScrubbingService(context.Background(), scrubbing.DefaultConfig(), testutil.NewTestLogger(), storagetest.NewTestTokenStore())
	require.NoError(t, err)
	return svc
}

// mustUUID returns a fresh UUID string, failing the test on the
// (practically impossible) failure of the OS random source.
func mustUUID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewString()
	require.NoError(t, err)
	return id
}

func TestVerifyActionReceiptSignatureFailsClosedOnInvalidEvidence(t *testing.T) {
	actuator, publicKey := newTestActuator(t)
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
	wrongPublicKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	tests := []struct {
		name      string
		receipt   *operatorv1.ActionReceipt
		publicKey ed25519.PublicKey
		wantErr   error
	}{
		{name: "valid signature", receipt: receipt, publicKey: publicKey},
		{name: "missing receipt", publicKey: publicKey, wantErr: constants.ErrActionReceiptMissing},
		{name: "missing signature", receipt: func() *operatorv1.ActionReceipt {
			candidate := proto.Clone(receipt).(*operatorv1.ActionReceipt)
			candidate.Signature = ""
			return candidate
		}(), publicKey: publicKey, wantErr: constants.ErrActionReceiptSignatureInvalid},
		{name: "malformed signature", receipt: func() *operatorv1.ActionReceipt {
			candidate := proto.Clone(receipt).(*operatorv1.ActionReceipt)
			candidate.Signature = "not-hex"
			return candidate
		}(), publicKey: publicKey, wantErr: constants.ErrActionReceiptSignatureInvalid},
		{name: "tampered receipt", receipt: func() *operatorv1.ActionReceipt {
			candidate := proto.Clone(receipt).(*operatorv1.ActionReceipt)
			candidate.ResultSummary = "tampered"
			return candidate
		}(), publicKey: publicKey, wantErr: constants.ErrActionReceiptSignatureInvalid},
		{name: "wrong public key", receipt: receipt, publicKey: wrongPublicKey, wantErr: constants.ErrActionReceiptSignatureInvalid},
		{name: "malformed public key", receipt: receipt, publicKey: ed25519.PublicKey("short"), wantErr: constants.ErrActionReceiptSignatureInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyActionReceiptSignature(tt.receipt, tt.publicKey)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

type operatorAuditSummaryCommand struct {
	result *models.OperatorAuditReceiptListResult
}

func (*operatorAuditSummaryCommand) GetPayload() []byte            { return nil }
func (*operatorAuditSummaryCommand) SetPayload([]byte)             {}
func (*operatorAuditSummaryCommand) InferenceResultDigest() string { return "" }
func (c *operatorAuditSummaryCommand) AuditReceiptListSummary() *models.OperatorAuditReceiptListResult {
	return c.result
}

type operatorAuditSummaryCommandWithoutTypedResult struct{}

func (*operatorAuditSummaryCommandWithoutTypedResult) GetPayload() []byte            { return nil }
func (*operatorAuditSummaryCommandWithoutTypedResult) SetPayload([]byte)             {}
func (*operatorAuditSummaryCommandWithoutTypedResult) InferenceResultDigest() string { return "" }

func TestL5AuditReceiptListSummaryPreservesOnlyExactTypedLocalEvidence(t *testing.T) {
	actuator, publicKey := newTestActuator(t)
	request, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: "audit_receipt_list"})
	require.NoError(t, err)
	receipt := &operatorv1.ActionReceipt{
		TransactionId:   "tx-command",
		TransactionHash: "hash-command",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		SignerKeyId:     actuator.KeyID,
	}
	receipt.Signature, err = actuator.signReceipt(receipt)
	require.NoError(t, err)
	require.NoError(t, VerifyActionReceiptSignature(receipt, publicKey))
	result := &models.OperatorAuditReceiptListResult{
		Receipts:        []*operatorv1.ActionReceipt{receipt},
		Count:           1,
		OperatorID:      "op-1",
		Authority:       "operator_local_ledger",
		InvestigationID: "investigation-1",
	}
	summaryBytes, err := json.Marshal(result)
	require.NoError(t, err)
	summary := string(summaryBytes)
	vt := &VerifiedTransaction{
		Envelope: &govtypes.GovernanceEnvelope{
			OperatorId:      "op-1",
			InvestigationId: "investigation-1",
			Payload:         request,
		},
		ActionType: constants.ActionTypeMcpCall,
	}
	command := &operatorAuditSummaryCommand{result: result}

	assert.Equal(t, summary, actuator.auditReceiptListSummary(vt, command, summary))
	assert.Contains(t, summary, receipt.GetSignature())
	assert.Empty(t, actuator.auditReceiptListSummary(vt, command, `{"authority":"operator_local_ledger"}`))
	assert.Empty(t, actuator.auditReceiptListSummary(vt, &operatorAuditSummaryCommandWithoutTypedResult{}, summary))

	tests := []struct {
		name   string
		mutate func(*VerifiedTransaction, *models.OperatorAuditReceiptListResult)
	}{
		{
			name: "wrong action",
			mutate: func(v *VerifiedTransaction, _ *models.OperatorAuditReceiptListResult) {
				v.ActionType = constants.ActionTypeExecuteBash
			},
		},
		{
			name: "wrong tool",
			mutate: func(v *VerifiedTransaction, _ *models.OperatorAuditReceiptListResult) {
				v.Envelope.Payload, _ = proto.Marshal(&operatorv1.McpCallRequested{ToolName: "audit_receipt_get"})
			},
		},
		{
			name: "wrong authority",
			mutate: func(_ *VerifiedTransaction, r *models.OperatorAuditReceiptListResult) {
				r.Authority = "gateway_mirror"
			},
		},
		{
			name: "wrong operator",
			mutate: func(_ *VerifiedTransaction, r *models.OperatorAuditReceiptListResult) {
				r.OperatorID = "op-2"
			},
		},
		{
			name: "wrong investigation",
			mutate: func(_ *VerifiedTransaction, r *models.OperatorAuditReceiptListResult) {
				r.InvestigationID = "investigation-2"
			},
		},
		{
			name: "count mismatch",
			mutate: func(_ *VerifiedTransaction, r *models.OperatorAuditReceiptListResult) {
				r.Count = 2
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vtCopy := proto.Clone(vt.Envelope).(*govtypes.GovernanceEnvelope)
			verified := *vt
			verified.Envelope = vtCopy
			resultCopy := proto.Clone(receipt).(*operatorv1.ActionReceipt)
			copied := *result
			copied.Receipts = []*operatorv1.ActionReceipt{resultCopy}
			commandCopy := &operatorAuditSummaryCommand{result: &copied}
			tt.mutate(&verified, &copied)
			mutatedSummary, marshalErr := json.Marshal(&copied)
			require.NoError(t, marshalErr)
			assert.Empty(t, actuator.auditReceiptListSummary(&verified, commandCopy, string(mutatedSummary)))
		})
	}
}

func TestVerifyReceiptPersistenceAttestationRejectsSemanticallyUnboundFields(t *testing.T) {
	t.Parallel()
	actuator, publicKey := newTestActuator(t)
	receipt := &operatorv1.ActionReceipt{
		TransactionId: "test-transaction",
		SignerKeyId:   actuator.KeyID,
		Signature:     "abcd",
	}
	tests := []struct {
		name   string
		mutate func(*operatorv1.ReceiptPersistenceAttestation)
	}{
		{name: "transaction mismatch", mutate: func(attestation *operatorv1.ReceiptPersistenceAttestation) {
			attestation.TransactionId = "other-transaction"
		}},
		{name: "audit record mismatch", mutate: func(attestation *operatorv1.ReceiptPersistenceAttestation) {
			attestation.AuditRecordId = "other-record"
		}},
		{name: "signer mismatch", mutate: func(attestation *operatorv1.ReceiptPersistenceAttestation) {
			attestation.SignerKeyId = "other-signer"
		}},
		{name: "missing persistence timestamp", mutate: func(attestation *operatorv1.ReceiptPersistenceAttestation) {
			attestation.PersistedAtUnixMs = 0
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attestation, err := actuator.signReceiptPersistenceAttestation(receipt)
			require.NoError(t, err)
			tt.mutate(attestation)
			canonical, err := CanonicalizeReceiptPersistenceAttestation(attestation)
			require.NoError(t, err)
			attestation.Signature = hex.EncodeToString(ed25519.Sign(actuator.SigningKey, canonical))
			receipt.FinalPersistenceAttestation = attestation

			assert.ErrorIs(t, VerifyReceiptPersistenceAttestation(receipt, publicKey), constants.ErrReceiptPersistenceAttestationInvalid)
		})
	}
}

func TestL5ActuatorExecuteMissingSigningKey(t *testing.T) {
	t.Parallel()
	actuator, _ := newTestActuator(t)
	actuator.SigningKey = nil

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-1234567890abcdef",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}

	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
	}

	// Execute - should fail immediately
	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "L5Actuator: signing key missing")
	require.Nil(t, receipt)
}

func TestL5ActuatorExecuteMissingExecutionHandler(t *testing.T) {
	t.Parallel()
	actuator, _ := newTestActuator(t)
	actuator.ExecutionHandler = nil

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-1234567890abcdef",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}

	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
	}

	// Execute - should fail immediately
	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "L5Actuator: ExecutionHandler not set")
	require.Nil(t, receipt)
}

// rehydrationCommandMessage is a CommandMessage whose payload carries UEI placeholders.
type rehydrationCommandMessage struct{ payload []byte }

func (m *rehydrationCommandMessage) GetPayload() []byte          { return m.payload }
func (m *rehydrationCommandMessage) SetPayload(p []byte)         { m.payload = p }
func (*rehydrationCommandMessage) InferenceResultDigest() string { return "" }

func rehydrationTestVT() *VerifiedTransaction {
	return &VerifiedTransaction{Envelope: &govtypes.GovernanceEnvelope{Id: "test-rehydrate-message"}}
}

func TestL5RehydratePayloadRestoresResolvableTokens(t *testing.T) {
	t.Parallel()
	actuator, _ := newTestActuator(t)
	token, err := actuator.Scrubbing.GetTokenForValue(context.Background(), "s3cret-value")
	require.NoError(t, err)

	msg := &rehydrationCommandMessage{payload: []byte(`{"command":"login --password ` + token + `"}`)}
	require.NoError(t, actuator.rehydratePayload(context.Background(), rehydrationTestVT(), msg))
	assert.JSONEq(t, `{"command":"login --password s3cret-value"}`, string(msg.GetPayload()))
}

// A payload with a placeholder that cannot be resolved fails rehydration and is left untouched,
// so the caller has nothing to dispatch.
func TestL5RehydratePayloadFailsClosedOnUnresolvableToken(t *testing.T) {
	t.Parallel()
	actuator, _ := newTestActuator(t)
	original := []byte(`{"command":"login --password {{UEI_999999}}"}`)

	msg := &rehydrationCommandMessage{payload: original}
	err := actuator.rehydratePayload(context.Background(), rehydrationTestVT(), msg)
	require.ErrorIs(t, err, constants.ErrL5ActuatorRehydrate)
	require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
	assert.Equal(t, original, msg.GetPayload(), "an unrehydrated payload is never handed back as dispatchable")
}

// An Actuator with no scrubbing service cannot rehydrate a token, so a payload that holds one is
// refused; a payload with no token passes unchanged.
func TestL5RehydratePayloadWithoutScrubberRefusesTokens(t *testing.T) {
	t.Parallel()
	actuator, _ := newTestActuator(t)
	actuator.Scrubbing = nil

	tokenized := []byte(`{"command":"login --password {{UEI_1}}"}`)
	msg := &rehydrationCommandMessage{payload: tokenized}
	err := actuator.rehydratePayload(context.Background(), rehydrationTestVT(), msg)
	require.ErrorIs(t, err, constants.ErrL5ActuatorRehydrate)
	require.ErrorIs(t, err, constants.ErrScrubbingTokenUnresolved)
	assert.Equal(t, tokenized, msg.GetPayload())

	plain := []byte(`{"command":"uptime"}`)
	msg = &rehydrationCommandMessage{payload: plain}
	require.NoError(t, actuator.rehydratePayload(context.Background(), rehydrationTestVT(), msg))
	assert.Equal(t, plain, msg.GetPayload())
}

// Both rehydration sentinels, wrapped the way rehydratePayload wraps them, classify as
// REHYDRATION_FAILED so the signed receipt names the cause.
func TestClassifyReceiptFailureRehydration(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []error{constants.ErrScrubbingTokenUnresolved, constants.ErrScrubbingKeyCollision} {
		wrapped := fmt.Errorf("%w: %w", constants.ErrL5ActuatorRehydrate, sentinel)
		assert.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_REHYDRATION_FAILED, classifyReceiptFailure(wrapped), sentinel.Error())
	}
}

func TestCanonicalizeActionReceipt(t *testing.T) {
	t.Parallel()
	receipt := &operatorv1.ActionReceipt{
		TransactionId:    "test-tx-id",
		TransactionHash:  "test-hash",
		Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:    "test summary",
		StateRootBefore:  "root-before",
		StateRootAfter:   "root-after",
		ExecutedAtUnixMs: 1234567890,
		SignerKeyId:      "test-key-id",
	}

	// Canonicalize twice and verify results are identical
	bytes1, err := CanonicalizeActionReceipt(receipt)
	require.NoError(t, err)

	bytes2, err := CanonicalizeActionReceipt(receipt)
	require.NoError(t, err)

	require.Equal(t, bytes1, bytes2, "Canonicalization should be deterministic")

	// Verify it's valid JSON
	var parsed map[string]interface{}
	err = json.Unmarshal(bytes1, &parsed)
	require.NoError(t, err)

	// Verify all expected fields are present
	require.Equal(t, "test-tx-id", parsed["transaction_id"])
	require.Equal(t, "test-hash", parsed["transaction_hash"])
	require.InEpsilon(t, float64(2), parsed["status"], 0.0) // EXECUTION_STATUS_COMPLETED = 2 (JSON marshals enums as float64)
	require.Equal(t, "test summary", parsed["result_summary"])
	require.Equal(t, "root-before", parsed["state_root_before"])
	require.Equal(t, "root-after", parsed["state_root_after"])
	require.InEpsilon(t, float64(1234567890), parsed["executed_at_unix_ms"], 0.0)
	require.Equal(t, "test-key-id", parsed["signer_key_id"])
}

func TestCanonicalizeCommitmentAttestation_ProtocolVector(t *testing.T) {
	t.Parallel()
	attestation := &operatorv1.CommitmentAttestation{
		TransactionId:               "tx-1",
		TransactionHash:             "abc123",
		StateRootAtCommit:           "state-1",
		L2SignatureDigest:           "l2",
		WardenIntentSignatureDigest: "warden",
		HumanSignatureDigest:        "human",
		ActionType:                  "FILE_EDIT",
		TargetResource:              "/tmp/example",
		CommittedAtUnixMs:           1700000000123,
		AuditorKeyId:                "0123",
		Signature:                   "excluded",
		Hash:                        "excluded",
	}

	canonical, err := CanonicalizeCommitmentAttestation(attestation)
	require.NoError(t, err)
	assert.Equal(t, "0000000474782d3100000006616263313233000000000000000773746174652d31000000026c320000000677617264656e0000000568756d616e0000000946494c455f454449540000000c2f746d702f6578616d706c650000018bcfe5687b0000000430313233", hex.EncodeToString(canonical))
}

func TestSignatureDigest_ProtocolVectors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		signatures []string
		expected   string
	}{
		{name: "no signatures", signatures: nil, expected: ""},
		{name: "single signature", signatures: []string{"aa"}, expected: "e9e797104fd871e7e240d637b2bba89f78211d032586b6ce20080025db015bec"},
		{name: "sorted nonempty signatures", signatures: []string{"bb", "", "aa"}, expected: "ee23674743e070fa0543cbf985228c7e34e18e6f64652da9cb1bec62887d9518"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := append([]string(nil), tt.signatures...)
			assert.Equal(t, tt.expected, SignatureDigest(input))
			assert.Equal(t, tt.signatures, input)
		})
	}
}

func TestDeterministicStageActionType_RequiresOneConsistentActionType(t *testing.T) {
	tests := []struct {
		name       string
		receipt    *operatorv1.ActionReceipt
		expected   string
		expectsErr bool
	}{
		{name: "consistent action type", receipt: &operatorv1.ActionReceipt{DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{{ActionType: "FILE_EDIT"}, {}, {ActionType: "FILE_EDIT"}}}, expected: "FILE_EDIT"},
		{name: "nil receipt", expectsErr: true},
		{name: "missing stages", receipt: &operatorv1.ActionReceipt{}, expectsErr: true},
		{name: "empty action types", receipt: &operatorv1.ActionReceipt{DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{{}, {}}}, expectsErr: true},
		{name: "conflicting action types", receipt: &operatorv1.ActionReceipt{DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{{ActionType: "FILE_EDIT"}, {ActionType: "FILE_READ"}}}, expectsErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actionType, err := DeterministicStageActionType(test.receipt)
			if test.expectsErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
				assert.Empty(t, actionType)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.expected, actionType)
		})
	}
}

func TestClassifyReceiptFailure_MapsTypedSentinelsToCodes(t *testing.T) {
	tests := []struct {
		name    string
		execErr error
		want    operatorv1.ReceiptFailureCode
	}{
		{name: "nil error is unspecified", execErr: nil, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_UNSPECIFIED},
		{name: "model override denied", execErr: constants.ErrInferenceModelOverrideDenied, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_OVERRIDE_DENIED},
		{name: "role invalid", execErr: constants.ErrInferenceRoleInvalid, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_ROLE_INVALID},
		{name: "model ref invalid", execErr: constants.ErrInferenceModelRefInvalid, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_REF_INVALID},
		{name: "backend unavailable", execErr: constants.ErrInferenceBackendUnavailable, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE},
		{name: "backend not registered", execErr: constants.ErrInferenceBackendNotRegistered, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE},
		{name: "backend timeout", execErr: constants.ErrInferenceBackendTimeout, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TIMEOUT},
		{name: "generate failed", execErr: constants.ErrInferenceGenerateFailed, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GENERATE_FAILED},
		{name: "model not found", execErr: constants.ErrInferenceModelNotFound, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_NOT_FOUND},
		{name: "provider response invalid", execErr: constants.ErrInferenceProviderResponseInvalid, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_PROVIDER_RESPONSE_INVALID},
		{name: "generation options invalid", execErr: constants.ErrInferenceGenerationOptionsInvalid, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GENERATION_OPTIONS_INVALID},
		{name: "capability unsupported", execErr: constants.ErrInferenceCapabilityUnsupported, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CAPABILITY_UNSUPPORTED},
		{name: "tools unsupported", execErr: constants.ErrInferenceToolsUnsupported, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_TOOLS_UNSUPPORTED},
		{name: "context overflow", execErr: constants.ErrInferenceContextOverflow, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CONTEXT_OVERFLOW},
		{name: "provider attempt required", execErr: constants.ErrInferenceProviderAttemptRequired, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_PROVIDER_ATTEMPT_REQUIRED},
		{name: "identity mismatch", execErr: constants.ErrInferenceIdentityMismatch, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_IDENTITY_MISMATCH},
		{name: "model digest mismatch", execErr: constants.ErrInferenceModelDigestMismatch, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_DIGEST_MISMATCH},
		{name: "evidence hash invalid", execErr: constants.ErrInferenceEvidenceHashInvalid, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EVIDENCE_HASH_INVALID},
		{name: "model registry invalid", execErr: constants.ErrInferenceModelRegistryInvalid, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_REGISTRY_INVALID},
		{name: "campaign binding invalid", execErr: constants.ErrInferenceCampaignBindingInvalid, want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CAMPAIGN_BINDING_INVALID},
		{name: "untyped error is generic execution failure", execErr: errors.New("boom"), want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED},
		{name: "wrapped sentinel still classifies", execErr: fmt.Errorf("inference handler: %w", constants.ErrInferenceModelNotFound), want: operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_NOT_FOUND},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, classifyReceiptFailure(test.execErr))
		})
	}
}
