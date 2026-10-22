// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package compliance

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	integrationSessionID = "ksi-integration-session"
	integrationTxID      = "ksi-integration-tx-1"
)

// integrationEvaluatorFixture holds real storage backends wired exactly like
// the production openEvaluatorDeps path: SQLAuditStore for audit evidence,
// GitLedgerService for ledger evidence, and CommitmentLedger sharing ztd.db.
type integrationEvalGraderReader struct {
	results []GraderResult
}

func (r *integrationEvalGraderReader) ListGraderResults(context.Context) ([]GraderResult, error) {
	return r.results, nil
}

type integrationEvaluatorFixture struct {
	deps            EvaluatorDeps
	auditStore      *storage.SQLAuditStore
	ledger          *storage.GitLedgerService
	commitments     *storage.CommitmentLedger
	history         *KSIHistoryStore
	eventReceiptKey ed25519.PrivateKey
	fileSvc         fs.RuntimeFileService
	tempDir         string
}

// newIntegrationEvaluatorFixture creates the real storage backends against a
// temp-rooted file service. No mocks: on-disk SQLite, real git ledger.
func newIntegrationEvaluatorFixture(t *testing.T) *integrationEvaluatorFixture {
	t.Helper()

	tempDir := testutil.OperatorDir(t)
	fileSvc := storagetest.NewTestFileSvc(t, tempDir)

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	vaultDir := filepath.Join(tempDir, "vault")
	testVault := storagetest.CreateTestVault(t, vaultDir, privKey)

	logger := testutil.NewTestLogger()

	require.NoError(t, fileSvc.MkdirAll(context.Background(), constants.DataDirname, constants.PermDirStandard))
	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	mainDB, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), logger)
	require.NoError(t, err)
	t.Cleanup(func() { mainDB.Close() })

	auditCfg := storage.DefaultAuditStoreConfig()
	auditCfg.EncryptionVault = testVault
	auditStore, err := storage.NewSQLAuditStore(auditCfg, mainDB, logger, fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { auditStore.Close() })

	commitments := auditStore.CommitmentLedger()

	ledger, err := storage.NewGitLedgerService(&storage.LedgerConfig{
		EncryptionVault: testVault,
	}, logger, fileSvc)
	require.NoError(t, err)
	historyDir := filepath.Join(constants.DataDirname, constants.ComplianceDirname, constants.KSIHistoryDirname)
	history := NewKSIHistoryStore(fileSvc, historyDir)
	now := time.Now().UTC()
	graders := &integrationEvalGraderReader{results: []GraderResult{{
		ArtifactID: "metric:protocol-chain", GraderID: "protocol_chain", GraderVersion: "1.0.0",
		SHA256: strings.Repeat("a", 64), Verified: true, ProducedAt: now,
		Evidence: []*compliancev1.ComplianceEvidenceReference{{ArtifactId: "event_receipt:" + integrationTxID, Sha256: strings.Repeat("b", 64)}},
	}}}

	return &integrationEvaluatorFixture{
		deps: EvaluatorDeps{
			Audit:       auditStore,
			Ledger:      ledger,
			Commitments: commitments,
			History:     history,
			Graders:     graders,
		},
		auditStore:      auditStore,
		ledger:          ledger,
		commitments:     commitments,
		history:         history,
		eventReceiptKey: privKey,
		fileSvc:         fileSvc,
		tempDir:         tempDir,
	}
}

// seedEvidence records real events, a signed event receipt, a file mutation, git
// ledger commits, and an intact two-entry commitment chain.
func (f *integrationEvaluatorFixture) seedEvidence(t *testing.T) {
	t.Helper()

	require.NoError(t, f.auditStore.CreateSession(integrationSessionID, constants.SessionTypeOperator, "KSI integration", "test-user"))

	now := time.Now().UTC()
	eventID, err := f.auditStore.RecordEvent(&storage.Event{
		OperatorSessionID: integrationSessionID,
		Timestamp:         now,
		Type:              constants.EventAppCaseCreated,
		ContentText:       "integration test event",
	})
	require.NoError(t, err)
	require.Positive(t, eventID)

	signerKeyID := hex.EncodeToString(f.eventReceiptKey.Public().(ed25519.PublicKey))
	eventReceipt := &operatorv1.EventReceipt{TransactionId: integrationTxID, TransactionHash: "tx-hash-1", StateRootBefore: "state-before", StateRootAfter: "state-after", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ExecutedAtUnixMs: now.UnixMilli(), SignerKeyId: signerKeyID}
	payload, err := governance.CanonicalizeEventReceipt(eventReceipt)
	require.NoError(t, err)
	eventReceipt.Signature = hex.EncodeToString(ed25519.Sign(f.eventReceiptKey, payload))
	attestation := &operatorv1.EventReceiptPersistenceAttestation{TransactionId: integrationTxID, EventReceiptSignatureDigest: governance.SignatureDigest([]string{eventReceipt.Signature}), PersistedAtUnixMs: now.Add(time.Millisecond).UnixMilli(), AuditRecordId: integrationTxID, SignerKeyId: signerKeyID}
	payload, err = governance.CanonicalizeEventReceiptPersistenceAttestation(attestation)
	require.NoError(t, err)
	attestation.Signature = hex.EncodeToString(ed25519.Sign(f.eventReceiptKey, payload))
	eventReceipt.FinalPersistenceAttestation = attestation
	require.NoError(t, f.auditStore.RecordEventReceipt(&models.EventReceiptRecord{
		TransactionID:     integrationTxID,
		TransactionHash:   eventReceipt.TransactionHash,
		OperatorID:        "operator-1",
		OperatorSessionID: integrationSessionID,
		ActionType:        constants.ActionTypeExecuteBash,
		TargetResource:    "localhost",
		Status:            eventReceipt.Status,
		StateRootBefore:   eventReceipt.StateRootBefore,
		StateRootAfter:    eventReceipt.StateRootAfter,
		ExecutedAt:        now,
		SignerKeyID:       signerKeyID,
		Signature:         eventReceipt.Signature,
		Timestamp:         now,
		EventReceipt:      eventReceipt,
	}))

	require.NoError(t, f.auditStore.RecordFileMutation(&storage.FileMutationLog{
		EventID:   eventID,
		Filepath:  "integration-target.txt",
		Operation: storage.FileMutationCreate,
	}))

	targetPath := filepath.Join(f.tempDir, "integration-target.txt")
	require.NoError(t, os.WriteFile(targetPath, []byte("integration evidence"), constants.PermFilePrivate))
	target, err := f.fileSvc.OpenFileEditTarget(context.Background(), targetPath, f.tempDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })
	mirror, err := f.ledger.MirrorFileCreate("", target)
	require.NoError(t, err)
	require.NotNil(t, mirror)
	require.NoError(t, f.ledger.CompleteMirrorCreate(mirror, ""))

	appendCommitment := func(txID string, committedAt time.Time, priorHash string) string {
		attestation := &operatorv1.CommitmentAttestation{
			TransactionId: txID, TransactionHash: "tx-hash", PriorCommitmentHash: priorHash,
			StateRootAtCommit: eventReceipt.StateRootBefore, ActionType: string(constants.ActionTypeExecuteBash),
			TargetResource: "localhost", CommittedAtUnixMs: committedAt.UnixMilli(), AuditorKeyId: signerKeyID,
		}
		canonical, err := governance.CanonicalizeCommitmentAttestation(attestation)
		require.NoError(t, err)
		digest := sha256.Sum256(canonical)
		attestation.Hash = hex.EncodeToString(digest[:])
		attestation.Signature = hex.EncodeToString(ed25519.Sign(f.eventReceiptKey, canonical))
		body, err := json.Marshal(attestation)
		require.NoError(t, err)
		require.NoError(t, f.commitments.AppendCommitmentJSON(body, priorHash, attestation.Hash))
		return attestation.Hash
	}
	firstHash := appendCommitment("tx-1", now.Add(-time.Second), "")
	appendCommitment("tx-2", now, firstHash)
	require.NoError(t, f.history.SaveSnapshot(context.Background(), &KSIResultSet{
		Class: ClassC, EvaluatedAtMs: now.UnixMilli(), Results: []KSIResult{
			{ID: "KSI-CMT-01", Status: KSIStatusSatisfied},
			{ID: "KSI-MLA-08", Status: KSIStatusSatisfied},
		},
	}))
}

// loadRealKSICatalog loads the shipped docs-as-code KSI catalog.
func loadRealKSICatalog(t *testing.T) *KSICatalog {
	t.Helper()
	catalog, err := LoadKSICatalog(filepath.Join("..", "..", "..", "docs", "reference", "ksi-catalog.json"))
	require.NoError(t, err)
	return catalog
}

// integrationBinding returns a valid EvaluationBinding for integration tests.
func integrationBinding(t *testing.T) EvaluationBinding {
	t.Helper()
	now := time.Now()
	return EvaluationBinding{
		ScopeID:            "integration-scope",
		RunID:              "integration-run",
		WindowStartUnixMs:  now.Add(-time.Minute).UnixMilli(),
		WindowEndUnixMs:    now.Add(time.Second).UnixMilli(),
		EvaluatorID:        constants.KSIEvaluatorID,
		EvaluatorVersion:   constants.KSIEvaluatorVersion,
		MethodDefinitionID: constants.KSIMethodDefinitionVersion,
		AssertionAssessments: AssertionAssessmentScope{
			AssessmentIDs: []string{"assessment-1"},
		},
	}
}

// automatableKSIIDs mirrors the KSI IDs bound in DefaultMethods.
var automatableKSIIDs = []string{
	"KSI-CMT-01", "KSI-CMT-03", "KSI-CNA-01",
	"KSI-IAM-05", "KSI-IAM-07",
	"KSI-MLA-03", "KSI-MLA-07", "KSI-MLA-08",
	"KSI-SVC-04", "KSI-SVC-05",
}

// TestKSIEvaluator_Integration_SeededEvidenceSatisfiesAutomatableKSIs runs the
// evaluator with default methods against real seeded storage and verifies that
// automatable KSIs are satisfied with evidence anchors that reference real
// event receipt IDs, commit hashes, and commitment hashes from the test stores.
func TestKSIEvaluator_Integration_SeededEvidenceSatisfiesAutomatableKSIs(t *testing.T) {
	fixture := newIntegrationEvaluatorFixture(t)
	fixture.seedEvidence(t)

	catalog := loadRealKSICatalog(t)
	evaluator := NewKSIEvaluator(catalog)
	require.NoError(t, evaluator.RegisterDefaultMethods(fixture.deps))

	resultSet, err := evaluator.Evaluate(context.Background(), ClassC, integrationBinding(t))
	require.NoError(t, err)
	require.NoError(t, resultSet.Validate(catalog))
	require.Len(t, resultSet.Results, len(catalog.KSIsForClass(ClassC)))

	merkleRoot, err := fixture.ledger.GetStateMerkleRoot()
	require.NoError(t, err)
	require.NotEmpty(t, merkleRoot)

	resultsByID := make(map[string]KSIResult, len(resultSet.Results))
	for _, res := range resultSet.Results {
		resultsByID[res.ID] = res
	}

	for _, ksiID := range automatableKSIIDs {
		res, ok := resultsByID[ksiID]
		require.True(t, ok, "result set missing %s", ksiID)
		assert.Equal(t, KSIStatusSatisfied, res.Status, "%s should be satisfied against seeded evidence", ksiID)
		assert.Equal(t, KSIOutcomeSatisfied, res.Outcome)
		assert.GreaterOrEqual(t, res.MethodCount, MinimumMethodsForClass(ClassC))
		assert.NotEmpty(t, res.Evidence, "%s should carry evidence anchors", ksiID)
	}

	// Evidence anchors resolve to real event receipt IDs from the audit store.
	assert.Equal(t, integrationTxID, firstEvidenceRef(t, resultsByID["KSI-MLA-08"], EvidenceTypeEventReceiptID))

	// Evidence anchors resolve to real commitment hashes and cryptographic verification.
	assert.NotEmpty(t, firstEvidenceRef(t, resultsByID["KSI-MLA-07"], EvidenceTypeLedgerCommit))
	assert.NotEmpty(t, firstEvidenceRef(t, resultsByID["KSI-MLA-07"], EvidenceTypeCommitmentSignature))
	assert.NotEmpty(t, firstEvidenceRef(t, resultsByID["KSI-SVC-05"], EvidenceTypeLedgerCommit))

	// Merkle state observations match the real git ledger HEAD.
	assert.Equal(t, merkleRoot, firstEvidenceRef(t, resultsByID["KSI-CMT-03"], EvidenceTypeStateObservation))
	assert.Equal(t, merkleRoot, firstEvidenceRef(t, resultsByID["KSI-SVC-05"], EvidenceTypeStateObservation))

	// Historical and grader-backed methods retain typed evidence anchors.
	assert.NotEmpty(t, firstEvidenceRef(t, resultsByID["KSI-CMT-01"], EvidenceTypeHistoricalFreshness))
	assert.NotEmpty(t, firstEvidenceRef(t, resultsByID["KSI-IAM-05"], EvidenceTypeGraderResult))

	// Non-automatable KSIs remain unverifiable even with seeded evidence.
	assert.Equal(t, KSIStatusUnverifiable, resultsByID["KSI-CED-01"].Status)
	assert.Equal(t, KSIOutcomeUnsupportedAutomation, resultsByID["KSI-CED-01"].Outcome)
	assert.Positive(t, resultSet.SatisfiedCount())
	assert.Positive(t, resultSet.UnverifiableCount())
}

// TestKSIEvaluator_Integration_EmptyStoresFailClosed verifies that the
// evaluator marks every KSI not_satisfied when the real stores hold no
// evidence: no events, no event receipts, no mutations, no operational ledger commits,
// and no commitments.
func TestKSIEvaluator_Integration_EmptyStoresFailClosed(t *testing.T) {
	fixture := newIntegrationEvaluatorFixture(t)

	catalog := loadRealKSICatalog(t)
	evaluator := NewKSIEvaluator(catalog)
	require.NoError(t, evaluator.RegisterDefaultMethods(fixture.deps))

	resultSet, err := evaluator.Evaluate(context.Background(), ClassC, integrationBinding(t))
	require.NoError(t, err)
	require.NoError(t, resultSet.Validate(catalog))
	require.NotEmpty(t, resultSet.Results)

	for _, res := range resultSet.Results {
		assert.Equal(t, KSIStatusUnverifiable, res.Status, "%s must be unverifiable with empty stores", res.ID)
	}
	assert.Zero(t, resultSet.SatisfiedCount())
	assert.Equal(t, len(resultSet.Results), resultSet.UnverifiableCount())
	assert.False(t, resultSet.HasFailures())
}

// firstEvidenceRef returns the reference of the first evidence anchor of the
// given type, failing the test if none exists.
func firstEvidenceRef(t *testing.T, res KSIResult, evidenceType EvidenceType) string {
	t.Helper()
	for _, ev := range res.Evidence {
		if ev.GetArtifactType() == string(evidenceType) {
			return ev.GetArtifactId()
		}
	}
	t.Fatalf("no evidence of type %s in result %s", evidenceType, res.ID)
	return ""
}
