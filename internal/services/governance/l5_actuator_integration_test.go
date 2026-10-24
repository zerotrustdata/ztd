// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package governance

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newCommitmentTestActuator(t *testing.T) (*L5Actuator, *storage.SQLAuditStore) {
	t.Helper()
	actuator, auditStore, _ := newPersistingActuator(t)
	return actuator, auditStore
}

// newPersistingActuator builds an actuator over a real SQLAuditStore and
// returns the database handle so tests can inject write failures.
func newPersistingActuator(t *testing.T) (*L5Actuator, *storage.SQLAuditStore, *sqliteutil.DB) {
	t.Helper()
	actuator, _ := newTestActuator(t)
	_, auditorPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	actuator.AuditorSigningKey = auditorPriv
	actuator.AuditorKeyID = hex.EncodeToString(auditorPriv.Public().(ed25519.PublicKey))
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, fileSvc.MkdirAll(context.Background(), constants.VaultDirname, constants.PermDirPrivate))
	vHeader, _, err := vault.NewVaultHeader(privKey)
	require.NoError(t, err)
	require.NoError(t, vHeader.Save(fileSvc))
	testVault, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: slog.Default()})
	require.NoError(t, err)
	require.NoError(t, testVault.Unlock(privKey))
	t.Cleanup(func() { require.NoError(t, testVault.Close()) })
	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	auditStore, err := storage.NewSQLAuditStore(&storage.AuditStoreConfig{
		RetentionDays:   1,
		EncryptionVault: testVault,
	}, db, slog.Default(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, auditStore.Close()) })
	actuator.SQLAuditStore = auditStore
	require.NoError(t, auditStore.CreateSession("test-operator-session", "operator", "Test Session", "test-user"))
	// The runtime's own identity: the Operator it executes for and the audit
	// session it started. Rejection receipts are filed under it.
	actuator.Identity = &testutil.MockExecutionTarget{OperatorIDs: []string{"test-operator"}, SessionID: "test-operator-session"}
	return actuator, auditStore, db
}

func TestL5ActuatorExecutePersistsReceiptAndCommitment(t *testing.T) {
	actuator, auditStore := newCommitmentTestActuator(t)

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-1234567890abcdef",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.Event.Operator.Command.Requested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}

	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	// Execute
	startedAt := time.Now().Add(-time.Second)
	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.NoError(t, err)
	require.NotNil(t, receipt)

	// Verify RecordEventReceipt was called by querying the audit store
	persistedReceipt, err := auditStore.GetEventReceipt(envelope.Id)
	require.NoError(t, err)
	require.NotNil(t, persistedReceipt, "Receipt should be persisted in audit store")

	// Verify persisted receipt matches the returned receipt
	require.Equal(t, envelope.Id, persistedReceipt.TransactionID)
	require.Equal(t, envelope.TransactionHash, persistedReceipt.TransactionHash)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, persistedReceipt.Status)
	require.Equal(t, "completed", persistedReceipt.ResultSummary)
	require.NotEmpty(t, persistedReceipt.Signature, "Persisted receipt should have signature")
	require.NotNil(t, persistedReceipt.ActionReceipt)
	require.NotNil(t, persistedReceipt.ActionReceipt.FinalPersistenceAttestation)
	require.NoError(t, VerifyReceiptPersistenceAttestation(persistedReceipt.ActionReceipt, actuator.SigningKey.Public().(ed25519.PublicKey)))
	require.Len(t, persistedReceipt.ActionReceipt.DeterministicStageEvidence, 3)
	receiptPersistenceEvidence := persistedReceipt.ActionReceipt.DeterministicStageEvidence[0]
	commitmentEvidence := persistedReceipt.ActionReceipt.DeterministicStageEvidence[1]
	executionEvidence := persistedReceipt.ActionReceipt.DeterministicStageEvidence[2]
	require.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND, commitmentEvidence.Kind)
	require.Equal(t, executionEvidence.StageId, receiptPersistenceEvidence.ParentStageId)
	require.Equal(t, executionEvidence.StageId, commitmentEvidence.ParentStageId)
	require.NotEmpty(t, commitmentEvidence.CommitmentHash)
	require.Equal(t, actuator.AuditorKeyID, commitmentEvidence.SignerKeyId)

	commitments, err := auditStore.CommitmentLedger().ListCommitments()
	require.NoError(t, err)
	require.Len(t, commitments, 1)
	require.Equal(t, envelope.Id, commitments[0].TransactionID)
	require.Equal(t, envelope.TransactionHash, commitments[0].TransactionHash)
	require.Equal(t, commitments[0].PriorCommitmentHash, commitmentEvidence.PriorCommitmentHash)
	require.Equal(t, commitments[0].Hash, commitmentEvidence.CommitmentHash)
	require.Equal(t, actuator.AuditorKeyID, commitments[0].AuditorKeyID)
	require.NotEmpty(t, commitments[0].WardenIntentSignatureDigest)
	require.NotEmpty(t, commitments[0].Signature)

	storedAttestation := &operatorv1.CommitmentAttestation{}
	require.NoError(t, compliancev1.UnmarshalCanonical(commitments[0].AttestationJSON, storedAttestation))
	canonicalJSON, err := compliancev1.MarshalCanonical(storedAttestation)
	require.NoError(t, err)
	require.Equal(t, canonicalJSON, commitments[0].AttestationJSON) //nolint:testifylint // exact bytes, not JSON equivalence
	require.Equal(t, commitments[0].Hash, storedAttestation.Hash)
	require.Equal(t, commitments[0].PriorCommitmentHash, storedAttestation.PriorCommitmentHash)
	canonicalSigningBytes, err := CanonicalizeCommitmentAttestation(storedAttestation)
	require.NoError(t, err)
	commitmentSignature, err := hex.DecodeString(storedAttestation.Signature)
	require.NoError(t, err)
	require.True(t, ed25519.Verify(actuator.AuditorSigningKey.Public().(ed25519.PublicKey), canonicalSigningBytes, commitmentSignature))

	listedReceipts, err := auditStore.ListEventReceipts(models.AuditScope{OperatorSessionID: envelope.OperatorSessionId}, 10, 0)
	require.NoError(t, err)
	require.Len(t, listedReceipts, 1)
	require.True(t, proto.Equal(receipt, listedReceipts[0].ActionReceipt),
		"ListEventReceipts must return the same canonical ActionReceipt persisted by L5")

	exportedReceipts, err := auditStore.ListEventReceiptsSince(startedAt, 10)
	require.NoError(t, err)
	require.Len(t, exportedReceipts, 1)
	require.True(t, proto.Equal(receipt, exportedReceipts[0].ActionReceipt),
		"ListEventReceiptsSince must return the same canonical ActionReceipt persisted by L5")
}

// divergentMirrorPublisher models a Gateway receipt mirror that diverges from
// the executing runtime. At publish time it snapshots what it was handed and
// what the local store already holds, then tampers with the handed receipt and
// reports a publish failure.
type divergentMirrorPublisher struct {
	auditStore *storage.SQLAuditStore

	calls                int
	publishedCopy        *operatorv1.ActionReceipt
	localAtPublish       *models.EventReceiptRecord
	localLookupErr       error
	commitmentsAtPublish []*storage.CommitmentRow
	commitmentLookupErr  error
}

func (p *divergentMirrorPublisher) PublishActionReceipt(_ context.Context, _ *govtypes.GovernanceEnvelope, receipt *operatorv1.ActionReceipt) error {
	p.calls++
	p.publishedCopy = proto.Clone(receipt).(*operatorv1.ActionReceipt)
	p.localAtPublish, p.localLookupErr = p.auditStore.GetEventReceipt(receipt.TransactionId)
	p.commitmentsAtPublish, p.commitmentLookupErr = p.auditStore.CommitmentLedger().ListCommitments()

	receipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	receipt.ResultSummary = "forged by gateway mirror"
	receipt.TransactionHash = "forged-transaction-hash"
	return errors.New("gateway receipts channel unavailable")
}

// A divergent or failing Gateway mirror must not alter the executing runtime's
// local receipt projection, audit facts, or commitment ledger. The mirror is
// handed exactly the locally persisted receipt, and its tampering affects only
// its own copy.
func TestL5ActuatorDivergentGatewayMirrorLeavesLocalRecordsIntact(t *testing.T) {
	actuator, auditStore := newCommitmentTestActuator(t)
	publisher := &divergentMirrorPublisher{auditStore: auditStore}
	actuator.ReceiptPublisher = publisher

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-divergent-mirror",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.Event.Operator.Command.Requested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}
	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.NoError(t, err, "a failing gateway mirror is best-effort and must not fail execution")
	require.NotNil(t, receipt)
	require.Equal(t, 1, publisher.calls)

	// The mirror was handed exactly the locally persisted receipt.
	require.NoError(t, publisher.localLookupErr)
	require.NoError(t, publisher.commitmentLookupErr)
	require.NotNil(t, publisher.localAtPublish)
	require.True(t, proto.Equal(publisher.publishedCopy, publisher.localAtPublish.ActionReceipt),
		"publisher must be handed the receipt already persisted locally")

	// The mirror's tampering must not reach the local record.
	persisted, err := auditStore.GetEventReceipt(envelope.Id)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, persisted.Status)
	require.Equal(t, "completed", persisted.ResultSummary)
	require.Equal(t, envelope.TransactionHash, persisted.TransactionHash)
	require.Equal(t, publisher.localAtPublish.Signature, persisted.Signature)
	require.True(t, proto.Equal(publisher.localAtPublish.ActionReceipt, persisted.ActionReceipt),
		"local receipt must be unchanged by the mirror")
	require.NoError(t, VerifyReceiptPersistenceAttestation(persisted.ActionReceipt, actuator.SigningKey.Public().(ed25519.PublicKey)))

	listedReceipts, err := auditStore.ListEventReceipts(models.AuditScope{OperatorSessionID: envelope.OperatorSessionId}, 10, 0)
	require.NoError(t, err)
	require.Len(t, listedReceipts, 1)
	require.True(t, proto.Equal(publisher.publishedCopy, listedReceipts[0].ActionReceipt),
		"ListEventReceipts must still return the locally persisted receipt")
	require.Equal(t, 2, receiptFactCount(t, auditStore, envelope.OperatorSessionId),
		"mirror divergence must not append receipt audit facts")

	// The commitment ledger (and therefore its head, the commitment root) is unchanged.
	commitments, err := auditStore.CommitmentLedger().ListCommitments()
	require.NoError(t, err)
	require.Len(t, commitments, 1)
	require.Equal(t, publisher.commitmentsAtPublish, commitments,
		"commitment ledger must be unchanged by the mirror")
	require.Equal(t, envelope.Id, commitments[0].TransactionID)
	require.Equal(t, envelope.TransactionHash, commitments[0].TransactionHash)
}

func TestL5ActuatorExecuteCommitmentFailureStopsBeforeHandler(t *testing.T) {
	actuator, auditStore := newCommitmentTestActuator(t)
	handler := actuator.ExecutionHandler.(*mockExecutionHandler)
	actuator.AuditorKeyID = strings.Repeat("0", ed25519.PublicKeySize*2)
	vt := &VerifiedTransaction{
		Envelope: &govtypes.GovernanceEnvelope{
			Id:                mustUUID(t),
			TransactionHash:   "test-hash-commitment-failure",
			OperatorId:        "test-operator",
			OperatorSessionId: "test-operator-session",
			EventType:         string(constants.Event.Operator.Command.Requested),
			ActionType:        string(constants.ActionTypeExecuteBash),
			TargetResource:    "localhost",
		},
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrL5ActuatorCommitmentPersist)
	assert.Nil(t, receipt)
	assert.False(t, handler.executed)
	commitments, listErr := auditStore.CommitmentLedger().ListCommitments()
	require.NoError(t, listErr)
	assert.Empty(t, commitments)
}

func receiptFactCount(t *testing.T, store *storage.SQLAuditStore, sessionID string) int {
	t.Helper()
	events, err := store.ListEvents(sessionID, 100, 0)
	require.NoError(t, err)
	count := 0
	for _, ev := range events {
		if ev.Type == constants.EventOperatorReceiptRecorded {
			count++
		}
	}
	return count
}

func executeSampleTransaction(t *testing.T, actuator *L5Actuator) *govtypes.GovernanceEnvelope {
	t.Helper()
	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-single-write",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.Event.Operator.Command.Requested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}
	_, err := actuator.Execute(context.Background(), &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}, nil)
	require.NoError(t, err)
	return envelope
}

// Each stage (EXECUTING, then COMPLETED) must produce exactly one audit fact:
// the SQLAuditStore is the single owner of receipt records.
func TestL5ActuatorRecordsEachStageOnce(t *testing.T) {
	actuator, auditStore := newCommitmentTestActuator(t)
	envelope := executeSampleTransaction(t, actuator)

	require.Equal(t, 2, receiptFactCount(t, auditStore, envelope.OperatorSessionId),
		"one receipt fact per stage")

	persisted, err := auditStore.GetEventReceipt(envelope.Id)
	require.NoError(t, err)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, persisted.Status)
	require.NotNil(t, persisted.ActionReceipt.FinalPersistenceAttestation)

	commitments, err := auditStore.CommitmentLedger().ListCommitments()
	require.NoError(t, err)
	require.Len(t, commitments, 1)
}
