// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func newAssignmentAuditTestFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	return files
}

// requireAssignmentAuditScratchRemoved asserts the build or verify left no
// scratch directory, WAL, or SHM file behind under the runtime tmp directory.
func requireAssignmentAuditScratchRemoved(t *testing.T, files fs.RuntimeFileService) {
	t.Helper()
	entries, err := files.ReadDir(context.Background(), constants.TmpDirname)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func sampleAssignmentAuditEvent(t *testing.T) AssignmentAuditSliceEvent {
	payload, err := ProjectModelRoleInvocationEvent(PublicModelRoleInvocationSignal{
		RunID:        "run-1",
		AssignmentID: "assignment-1",
		VariantID:    "variant-a",
		Role:         "primary",
		TaskID:       "scenario-1",
		ObservedAt:   "2026-09-17T00:00:00Z",
		EventID:      "event-1",
		Completed:    1,
		Total:        2,
	})
	require.NoError(t, err)
	body, err := MarshalPublicLiveEvent(payload)
	require.NoError(t, err)
	return AssignmentAuditSliceEvent{
		Type:      "stage_updated",
		Timestamp: "2026-09-17T00:00:00Z",
		Payload:   body,
	}
}

func TestBuildAssignmentAuditSlice_VaultKeyIsDeterministic(t *testing.T) {
	events := []AssignmentAuditSliceEvent{sampleAssignmentAuditEvent(t)}
	first, err := BuildAssignmentAuditSlice(context.Background(), newAssignmentAuditTestFiles(t), "assignment-1", events)
	require.NoError(t, err)
	second, err := BuildAssignmentAuditSlice(context.Background(), newAssignmentAuditTestFiles(t), "assignment-1", events)
	require.NoError(t, err)
	assert.Equal(t, first.VaultKeySHA256, second.VaultKeySHA256)
}

func TestBuildAssignmentAuditSlice_RoundTripAndBindings(t *testing.T) {
	files := newAssignmentAuditTestFiles(t)
	artifacts, err := BuildAssignmentAuditSlice(context.Background(), files, "assignment-1", []AssignmentAuditSliceEvent{sampleAssignmentAuditEvent(t)})
	require.NoError(t, err)
	requireAssignmentAuditScratchRemoved(t, files)
	require.NotEmpty(t, artifacts.Database)
	require.NotEmpty(t, artifacts.VaultKey)
	assert.Equal(t, hashBytes(artifacts.Database), artifacts.DatabaseSHA256)
	assert.Equal(t, hashBytes(artifacts.VaultKey), artifacts.VaultKeySHA256)
	require.NoError(t, VerifyAssignmentAuditSlice(context.Background(), files, artifacts.Database, artifacts.VaultKey))
	requireAssignmentAuditScratchRemoved(t, files)

	bindings := AssignmentAuditEvidenceBindings(artifacts)
	require.Len(t, bindings, 2)
	assert.Equal(t, AssignmentAuditSliceKind, bindings[0].GetKind())
	assert.Equal(t, AssignmentAuditVaultKeyKind, bindings[1].GetKind())

	_, bindingsFromEvidence, err := BuildPublicAssignmentEvidence(PublicAssignmentEvidenceInput{
		Result:       &evalv1.EvaluationAssignmentResult{},
		PublicProofs: bindings,
	})
	require.NoError(t, err)
	assert.Len(t, bindingsFromEvidence, 2)
}

// saveAgentTraceForAuditProof seals and persists one trace and returns the
// result that references it alongside the exact persisted body.
func saveAgentTraceForAuditProof(t *testing.T, store *Store) (*evalv1.EvaluationAssignmentResult, []byte) {
	t.Helper()
	trace := &AgentTrace{RunID: "run-1", AssignmentID: "assignment-1", EvaluationAttemptID: "attempt-1", DesignatedRole: "primary", Input: testAgentTraceInput(), FinalOutput: "Checkout fails on payment timeouts."}
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(context.Background(), "run-1", "assignment-1", body))
	ref, err := BuildAssignmentAgentTraceEvidenceReference("run-1", "attempt-1", body, time.Now().UTC())
	require.NoError(t, err)
	return &evalv1.EvaluationAssignmentResult{RunId: "run-1", AssignmentId: "assignment-1", EvidenceRefs: []*compliancev1.ComplianceEvidenceReference{ref}}, body
}

func TestAssignmentAgentTraceArtifact_PublishesExactPersistedBodyAndBindsIt(t *testing.T) {
	store := NewStore(newCampaignRuntimeFileService(t))
	result, persisted := saveAgentTraceForAuditProof(t, store)

	body, digest, err := loadAssignmentAgentTraceArtifact(context.Background(), store, result)

	require.NoError(t, err)
	assert.Equal(t, persisted, body, "the published artifact is the sealed body byte for byte")
	assert.Equal(t, result.GetEvidenceRefs()[0].GetSha256(), digest)
	bindings := AssignmentAuditEvidenceBindingsFromHashes(CampaignPublishedProofArtifacts{DatabaseSHA256: strings.Repeat("a", 64), VaultKeySHA256: strings.Repeat("b", 64), AgentTraceSHA256: digest})
	require.Len(t, bindings, 3)
	assert.Equal(t, AssignmentAgentTraceKind, bindings[2].GetKind())
	assert.Len(t, AssignmentAuditEvidenceBindingsFromHashes(CampaignPublishedProofArtifacts{DatabaseSHA256: strings.Repeat("a", 64), VaultKeySHA256: strings.Repeat("b", 64)}), 2, "no trace, no trace binding")
}

// testScenarioFixtures returns one frozen built-in scenario's fixture bodies
// keyed under scenarioID.
func testScenarioFixtures(t *testing.T, scenarioID string) map[string]ScenarioArtifacts {
	t.Helper()
	catalog, artifacts, err := BuildSmokeGateScenarioCatalog()
	require.NoError(t, err)
	require.NotEmpty(t, catalog.GetScenarios())
	return map[string]ScenarioArtifacts{scenarioID: artifacts[catalog.GetScenarios()[0].GetScenarioId()]}
}

func TestAttachScenarioFixture_PublishesFrozenBodiesAndBindsThem(t *testing.T) {
	fixtures := testScenarioFixtures(t, "scenario-1")
	artifacts := AssignmentAuditSliceArtifacts{DatabaseSHA256: strings.Repeat("a", 64), VaultKeySHA256: strings.Repeat("b", 64)}

	require.NoError(t, artifacts.attachScenarioFixture(&evalv1.EvaluationAssignment{ScenarioId: "scenario-1"}, fixtures))

	assert.Equal(t, "scenario-1", artifacts.ScenarioID)
	assert.Equal(t, fixtures["scenario-1"].Input.Body, artifacts.ScenarioInput, "input fixture is published byte for byte")
	assert.Equal(t, fixtures["scenario-1"].Gold.Body, artifacts.ScenarioGold, "gold criteria are published byte for byte")
	assert.Equal(t, hashBytes(artifacts.ScenarioInput), artifacts.ScenarioInputSHA256)
	assert.Equal(t, hashBytes(artifacts.ScenarioGold), artifacts.ScenarioGoldSHA256)
	bindings := AssignmentAuditEvidenceBindings(artifacts)
	require.Len(t, bindings, 4)
	assert.Equal(t, ScenarioInputFixtureKind, bindings[2].GetKind())
	assert.Equal(t, artifacts.ScenarioInputSHA256, bindings[2].GetSha256())
	assert.Equal(t, ScenarioGoldCriteriaKind, bindings[3].GetKind())
	assert.Equal(t, artifacts.ScenarioGoldSHA256, bindings[3].GetSha256())
	_, publicBindings, err := BuildPublicAssignmentEvidence(PublicAssignmentEvidenceInput{Result: &evalv1.EvaluationAssignmentResult{}, PublicProofs: bindings})
	require.NoError(t, err)
	assert.Len(t, publicBindings, 4)
}

func TestAttachScenarioFixture_RejectsMissingOrTamperedFixture(t *testing.T) {
	fixtures := testScenarioFixtures(t, "scenario-1")
	var artifacts AssignmentAuditSliceArtifacts
	require.ErrorIs(t, artifacts.attachScenarioFixture(&evalv1.EvaluationAssignment{ScenarioId: "scenario-unknown"}, fixtures), constants.ErrEvidenceArtifactMalformed)

	tampered := fixtures["scenario-1"]
	tampered.Gold.Body = append(append([]byte(nil), tampered.Gold.Body...), ' ')
	require.ErrorIs(t, artifacts.attachScenarioFixture(&evalv1.EvaluationAssignment{ScenarioId: "scenario-1"}, map[string]ScenarioArtifacts{"scenario-1": tampered}), constants.ErrEvidenceArtifactMalformed)
	assert.Empty(t, artifacts.ScenarioInput, "a rejected fixture attaches nothing")
}

func TestAssignmentAgentTraceArtifact_AbsentTraceWithoutReferencePublishesNothing(t *testing.T) {
	store := NewStore(newCampaignRuntimeFileService(t))

	body, digest, err := loadAssignmentAgentTraceArtifact(context.Background(), store, &evalv1.EvaluationAssignmentResult{RunId: "run-1", AssignmentId: "assignment-1"})

	require.NoError(t, err)
	assert.Nil(t, body)
	assert.Empty(t, digest)
}

func TestAssignmentAgentTraceArtifact_FailsClosed(t *testing.T) {
	t.Run("referenced trace missing", func(t *testing.T) {
		store := NewStore(newCampaignRuntimeFileService(t))
		result := &evalv1.EvaluationAssignmentResult{RunId: "run-1", AssignmentId: "assignment-1", EvidenceRefs: []*compliancev1.ComplianceEvidenceReference{{ArtifactType: string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace), Sha256: strings.Repeat("c", 64)}}}
		_, _, err := loadAssignmentAgentTraceArtifact(context.Background(), store, result)
		assert.ErrorIs(t, err, constants.ErrNotFound)
	})
	t.Run("reference hash differs from persisted body", func(t *testing.T) {
		store := NewStore(newCampaignRuntimeFileService(t))
		result, _ := saveAgentTraceForAuditProof(t, store)
		result.EvidenceRefs[0].Sha256 = strings.Repeat("c", 64)
		_, _, err := loadAssignmentAgentTraceArtifact(context.Background(), store, result)
		assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	})
}

func TestVerifyAssignmentAuditSlice_WrongKeyFailsDecrypt(t *testing.T) {
	artifacts, err := BuildAssignmentAuditSlice(context.Background(), newAssignmentAuditTestFiles(t), "assignment-1", []AssignmentAuditSliceEvent{sampleAssignmentAuditEvent(t)})
	require.NoError(t, err)

	wrongKey := []byte(strings.Repeat("a", 64) + "\n")
	err = VerifyAssignmentAuditSlice(context.Background(), newAssignmentAuditTestFiles(t), artifacts.Database, wrongKey)
	require.Error(t, err)
}

func TestBuildAssignmentAuditSlice_ProhibitedFieldFailsClosed(t *testing.T) {
	event := sampleAssignmentAuditEvent(t)
	fields := map[string]any{}
	require.NoError(t, json.Unmarshal(event.Payload, &fields))
	fields["prompt"] = "secret"
	payload, err := json.Marshal(fields)
	require.NoError(t, err)
	event.Payload = payload

	_, err = BuildAssignmentAuditSlice(context.Background(), newAssignmentAuditTestFiles(t), "assignment-1", []AssignmentAuditSliceEvent{event})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt")
}

func TestVerifyAssignmentAuditSlice_FlippedEventByteBreaksChain(t *testing.T) {
	ctx := context.Background()
	files := newAssignmentAuditTestFiles(t)
	artifacts, err := BuildAssignmentAuditSlice(ctx, files, "assignment-1", []AssignmentAuditSliceEvent{sampleAssignmentAuditEvent(t)})
	require.NoError(t, err)

	tamperDir, err := files.MkdirTemp(ctx, "assignment-audit-tamper-*")
	require.NoError(t, err)
	dbRelPath := filepath.Join(tamperDir, "tamper.db")
	require.NoError(t, files.WriteFile(ctx, dbRelPath, artifacts.Database, constants.PermFilePrivate))

	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(files.Resolve(dbRelPath)), slog.Default())
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE commitment_ledger SET hash = ? WHERE id = 1`, strings.Repeat("0", 64))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	tampered, err := files.ReadFile(ctx, dbRelPath)
	require.NoError(t, err)

	err = VerifyAssignmentAuditSlice(ctx, files, tampered, artifacts.VaultKey)
	require.Error(t, err)
}
