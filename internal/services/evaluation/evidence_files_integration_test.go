// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	rtfs "github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const evidenceMarker = "ZTD-EVIDENCE-MARKER-7f3a9c41"

// sealedRuntime is one runtime root whose evaluation records are encrypted with
// a real runtime vault opened through the real key store.
type sealedRuntime struct {
	raw   rtfs.RuntimeFileService
	files rtfs.RuntimeFileService
}

func newSealedRuntime(t *testing.T) *sealedRuntime {
	t.Helper()
	return newSealedRuntimeWithKeyring(t, keystoretest.NewMemoryKeyring())
}

// newSealedRuntimeWithKeyring is newSealedRuntime over a given OS key store, so
// several runtimes share one as the runtimes on a host do.
func newSealedRuntimeWithKeyring(t *testing.T, keyring keystore.Keyring) *sealedRuntime {
	t.Helper()
	raw, err := rtfs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, raw.CreateRuntimeTree(context.Background()))
	ks, err := keystore.NewWithFS(raw, testutil.NewTestLogger(), keystore.Options{Keyring: keyring})
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())
	files, err := NewEvidenceFileService(raw, func(keys rtfs.RuntimeFileService) (EvidenceVault, error) {
		keyStore, err := keystore.NewWithFS(keys, testutil.NewTestLogger(), keystore.Options{Keyring: keyring})
		if err != nil {
			return nil, err
		}
		return keyStore.OpenVault()
	})
	require.NoError(t, err)
	return &sealedRuntime{raw: raw, files: files}
}

// lockedVault is a real vault that was never unlocked.
func lockedVault(t *testing.T, files rtfs.RuntimeFileService) *vault.Vault {
	t.Helper()
	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: files, Logger: testutil.NewTestLogger()})
	require.NoError(t, err)
	return v
}

// assertNoPlaintextAnywhere walks every file under the runtime root and fails
// when any holds one of the needles. It returns the number of files scanned so
// a caller can prove the walk was not vacuous.
func assertNoPlaintextAnywhere(t *testing.T, root string, needles ...string) int {
	t.Helper()
	scanned := 0
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		scanned++
		for _, needle := range needles {
			assert.Falsef(t, bytes.Contains(body, []byte(needle)), "plaintext content %q found at rest in %s", needle, path)
		}
		return nil
	}))
	return scanned
}

func TestEvidenceFiles_EncryptEveryRecordUnderDataEval(t *testing.T) {
	rt := newSealedRuntime(t)
	ctx := context.Background()
	sealedPath := filepath.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, "run-1", constants.ConformanceRecordFilename)
	plainPath := filepath.Join(constants.EvaluationDirname, "queue.json")
	body := []byte(`{"prompt":"` + evidenceMarker + `"}`)

	require.NoError(t, rt.files.WriteFile(ctx, sealedPath, body, constants.PermFilePrivate))
	require.NoError(t, rt.files.WriteFile(ctx, plainPath, []byte(`{"status":"pending"}`), constants.PermFilePrivate))

	onDisk, err := rt.raw.ReadFile(ctx, sealedPath)
	require.NoError(t, err)
	assert.NotContains(t, string(onDisk), evidenceMarker, "records under data/eval/ are ciphertext at rest")
	assert.NotEqual(t, body, onDisk)

	roundTrip, err := rt.files.ReadFile(ctx, sealedPath)
	require.NoError(t, err)
	assert.Equal(t, body, roundTrip)

	// Identifier-only records outside data/eval/ pass through unchanged.
	queue, err := rt.raw.ReadFile(ctx, plainPath)
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"pending"}`, string(queue))

	// The sealed view is what backup uses: it reads the ciphertext as stored.
	sealed, err := SealedFiles(rt.files).ReadFile(ctx, sealedPath)
	require.NoError(t, err)
	assert.Equal(t, onDisk, sealed)
}

func TestEvidenceFiles_SealComplianceReportBundlesUnderTheirOwnDomain(t *testing.T) {
	rt := newSealedRuntime(t)
	ctx := context.Background()
	reportPath := path.Join(constants.ComplianceBundlesDirname, "report-1", constants.ComplianceBundleManifestPath)
	evalPath := path.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, "run-1", constants.ConformanceRecordFilename)
	body := []byte(`{"source":"` + evidenceMarker + `"}`)

	assert.True(t, SealsPath(rt.files, reportPath))
	assert.False(t, SealsPath(rt.raw, reportPath), "a file service that does not seal never claims to")
	require.NoError(t, rt.files.WriteFile(ctx, reportPath, body, constants.PermFilePrivate))
	assert.Positive(t, assertNoPlaintextAnywhere(t, rt.raw.Resolve(constants.ComplianceBundlesDirname), evidenceMarker))

	roundTrip, err := rt.files.ReadFile(ctx, reportPath)
	require.NoError(t, err)
	assert.Equal(t, body, roundTrip)

	// A report ciphertext moved under data/eval/ does not authenticate there.
	stored, err := rt.raw.ReadFile(ctx, reportPath)
	require.NoError(t, err)
	require.NoError(t, rt.raw.MkdirAll(ctx, path.Dir(evalPath), constants.PermDirPrivate))
	require.NoError(t, rt.raw.WriteFile(ctx, evalPath, stored, constants.PermFilePrivate))
	_, err = rt.files.ReadFile(ctx, evalPath)
	require.ErrorIs(t, err, constants.ErrEvaluationEvidenceDecryptFailed)
}

func TestEvidenceFiles_FailClosed(t *testing.T) {
	ctx := context.Background()
	sealedPath := filepath.Join(constants.DataDirname, constants.EvaluationDirname, "campaigns", "c-1", "campaign-spec.json")

	t.Run("a locked vault refuses to write and writes nothing", func(t *testing.T) {
		rt := newSealedRuntime(t)
		locked, err := NewEvidenceFileService(rt.raw, func(rtfs.RuntimeFileService) (EvidenceVault, error) { return lockedVault(t, rt.raw), nil })
		require.NoError(t, err)

		err = locked.WriteFile(ctx, sealedPath, []byte(evidenceMarker), constants.PermFilePrivate)
		require.ErrorIs(t, err, constants.ErrVaultLocked)
		exists, existsErr := rt.raw.FileExists(ctx, sealedPath)
		require.NoError(t, existsErr)
		assert.False(t, exists, "a refused write must not leave a plaintext or partial file")
		assertNoPlaintextAnywhere(t, rt.raw.Resolve(""), evidenceMarker)
	})

	t.Run("a locked vault refuses to read", func(t *testing.T) {
		rt := newSealedRuntime(t)
		require.NoError(t, rt.files.WriteFile(ctx, sealedPath, []byte(evidenceMarker), constants.PermFilePrivate))
		locked, err := NewEvidenceFileService(rt.raw, func(rtfs.RuntimeFileService) (EvidenceVault, error) { return lockedVault(t, rt.raw), nil })
		require.NoError(t, err)

		body, err := locked.ReadFile(ctx, sealedPath)
		require.ErrorIs(t, err, constants.ErrVaultLocked)
		assert.NotErrorIs(t, err, constants.ErrEvaluationEvidenceDecryptFailed, "a locked vault is not an authentication failure")
		assert.Empty(t, body)
	})

	t.Run("an unavailable vault is a typed error, never a plaintext write", func(t *testing.T) {
		rt := newSealedRuntime(t)
		unavailable, err := NewEvidenceFileService(rt.raw, func(rtfs.RuntimeFileService) (EvidenceVault, error) { return nil, constants.ErrVaultNotInitialized })
		require.NoError(t, err)

		err = unavailable.WriteFile(ctx, sealedPath, []byte(evidenceMarker), constants.PermFilePrivate)
		require.ErrorIs(t, err, constants.ErrEvaluationEvidenceVaultRequired)
		require.ErrorIs(t, err, constants.ErrVaultNotInitialized)
		exists, existsErr := rt.raw.FileExists(ctx, sealedPath)
		require.NoError(t, existsErr)
		assert.False(t, exists)
	})

	t.Run("a plaintext file planted under data/eval is never returned as a record", func(t *testing.T) {
		rt := newSealedRuntime(t)
		require.NoError(t, rt.raw.WriteFile(ctx, sealedPath, []byte(`{"prompt":"planted"}`), constants.PermFilePrivate))

		body, err := rt.files.ReadFile(ctx, sealedPath)
		require.ErrorIs(t, err, constants.ErrEvaluationEvidenceDecryptFailed)
		assert.Empty(t, body)
	})

	t.Run("a record sealed under another runtime vault does not decrypt", func(t *testing.T) {
		writer := newSealedRuntime(t)
		reader := newSealedRuntime(t)
		require.NoError(t, writer.files.WriteFile(ctx, sealedPath, []byte(evidenceMarker), constants.PermFilePrivate))
		ciphertext, err := writer.raw.ReadFile(ctx, sealedPath)
		require.NoError(t, err)
		require.NoError(t, reader.raw.WriteFile(ctx, sealedPath, ciphertext, constants.PermFilePrivate))

		_, err = reader.files.ReadFile(ctx, sealedPath)
		require.ErrorIs(t, err, constants.ErrEvaluationEvidenceDecryptFailed)
	})

	t.Run("a tampered record does not decrypt", func(t *testing.T) {
		rt := newSealedRuntime(t)
		require.NoError(t, rt.files.WriteFile(ctx, sealedPath, []byte(evidenceMarker), constants.PermFilePrivate))
		ciphertext, err := rt.raw.ReadFile(ctx, sealedPath)
		require.NoError(t, err)
		ciphertext[len(ciphertext)-1] ^= 0xff
		require.NoError(t, rt.raw.WriteFile(ctx, sealedPath, ciphertext, constants.PermFilePrivate))

		_, err = rt.files.ReadFile(ctx, sealedPath)
		require.ErrorIs(t, err, constants.ErrEvaluationEvidenceDecryptFailed)
	})

	t.Run("a raw stream handle onto a record is refused", func(t *testing.T) {
		rt := newSealedRuntime(t)
		_, err := rt.files.OpenForAppend(ctx, sealedPath, constants.PermFilePrivate)
		require.ErrorIs(t, err, constants.ErrEvaluationEvidenceStreamUnsupported)
		_, err = rt.files.OpenForRead(ctx, sealedPath)
		require.ErrorIs(t, err, constants.ErrEvaluationEvidenceStreamUnsupported)
	})
}

// markerExecutor returns a terminal result whose private grade detail quotes
// the marker on its first call and fails with an error that echoes the marker
// on later calls.
type markerExecutor struct {
	calls int
}

func (m *markerExecutor) ExecuteAssignment(_ context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	m.calls++
	if m.calls > 1 {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment", fmt.Errorf("model echoed prompt %s", evidenceMarker))
	}
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    req.Assignment.GetAssignmentId(),
		RunId:           req.Assignment.GetRunId(),
		CampaignId:      req.Assignment.GetCampaignId(),
		Lane:            req.Assignment.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		CompletedAt:     timestamppb.Now(),
		ModelInferences: StubHomogeneousAssignmentModelInferences(req.Assignment),
		FailureReason:   "model answered " + evidenceMarker,
		DeterministicGrades: []*evalv1.DeterministicGrade{{
			GradeId:     req.Assignment.GetAssignmentId() + ":scenario-content",
			CriterionId: "scenario-content",
			Status:      evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
			Detail:      "expected hint not found in " + evidenceMarker,
			Basis:       basisObservation,
		}},
	}
	digest, err := ComputeAssignmentResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return result, nil
}

// TestEvalWriters_PersistNoContentInPlaintext drives the real evaluation writers
// (campaign freeze, run, assignment results, failure results, agent trace,
// exchange evidence, run export) with a marker in the prompt, model output,
// grade detail, tool arguments, and error text, then walks the runtime root and
// requires that no file holds any of them. The readers must still round-trip.
func TestEvalWriters_PersistNoContentInPlaintext(t *testing.T) {
	rt := newSealedRuntime(t)
	ctx := context.Background()
	store := NewStore(rt.files)
	executor := &markerExecutor{}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })

	req := testCampaignInitRequest(t)
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: req.Catalog.GetSchemaVersion(),
		CatalogRef:    req.Catalog.GetCatalogRef(),
		Scenarios:     req.Catalog.GetScenarios()[:3],
	}
	truncatedDigest, err := ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)
	truncated.CatalogDigest = truncatedDigest
	req.Catalog = truncated

	// The frozen scenario prompt is content too: the model is shown it.
	var prompt string
	for _, scenario := range truncated.GetScenarios() {
		var input ScenarioInputFixture
		require.NoError(t, json.Unmarshal(req.ScenarioArtifacts[scenario.GetScenarioId()].Input.Body, &input))
		if len(input.UserPrompt) > len(prompt) {
			prompt = input.UserPrompt
		}
	}
	require.GreaterOrEqual(t, len(prompt), 24, "the built-in fixtures carry a distinctive prompt to track")

	_, err = controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = scheduleModelRoleAssignments(ctx, controller, req.RunID)
	require.NoError(t, err)

	binding := CampaignExecutionBinding{
		InferenceOperatorSessionID: "inf-session",
		DataOperatorID:             "data-op",
		DataOperatorSessionID:      "data-session",
		ModelRegistryDigest:        req.Inventory.RegistryDigest,
		ModelRegistry:              req.Inventory.ToModelRegistryFreeze().Variants,
	}
	completed, executedFirst, err := controller.ExecuteNextAssignment(ctx, req.RunID, binding, req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, executedFirst)
	failed, executedSecond, err := controller.ExecuteNextAssignment(ctx, req.RunID, binding, req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, executedSecond)

	// A failed executor persists its failure class, never its error text.
	require.Len(t, failed.GetDeterministicGrades(), 1)
	assert.Equal(t, "assignment execution failed: "+string(ExecutionFailureInternal), failed.GetDeterministicGrades()[0].GetDetail())
	loadedFailure, err := store.LoadAssignmentResult(ctx, req.RunID, failed.GetAssignmentId())
	require.NoError(t, err)
	assert.NotContains(t, loadedFailure.String(), evidenceMarker)

	// The completed result keeps its private grade detail, encrypted and readable.
	loadedCompleted, err := store.LoadAssignmentResult(ctx, req.RunID, completed.GetAssignmentId())
	require.NoError(t, err)
	assert.Contains(t, loadedCompleted.GetFailureReason(), evidenceMarker)
	assert.Contains(t, loadedCompleted.GetDeterministicGrades()[0].GetDetail(), evidenceMarker)

	// The frozen fixtures decrypt for the readers that grade against them.
	run, err := store.LoadRun(ctx, req.RunID)
	require.NoError(t, err)
	campaignID := run.GetCampaignBinding().GetCampaignId()
	catalog, err := store.LoadScenarioCatalog(ctx, campaignID)
	require.NoError(t, err)
	frozen, err := store.LoadScenarioArtifacts(ctx, campaignID, catalog)
	require.NoError(t, err)
	foundPrompt := false
	for _, artifact := range frozen {
		var input ScenarioInputFixture
		require.NoError(t, json.Unmarshal(artifact.Input.Body, &input))
		foundPrompt = foundPrompt || input.UserPrompt == prompt
	}
	assert.True(t, foundPrompt, "the frozen prompt round-trips through the encrypted store")

	// Agent trace: model text, tool arguments, command, tool result, final answer.
	trace := &AgentTrace{
		RunID: req.RunID, AssignmentID: completed.GetAssignmentId(), EvaluationAttemptID: "attempt-1", CampaignID: campaignID,
		ScenarioID: "scenario-1", DesignatedRole: "primary", ServedModelTag: "qwen3:4b",
		ModelCalls: []AgentModelCall{{ProviderAttemptID: "pa-1", TransactionID: "tx-1", Purpose: agentModelCallPurposeTurn, LoopTurn: 1, Text: "thinking about " + evidenceMarker}},
		ToolCalls: []AgentToolCall{{
			CallID: "tx-2", ModelCallID: "pa-1", ToolName: "run_commands_with_operator", ArgumentsJSON: `{"command":"cat ` + evidenceMarker + `"}`,
			Command: "cat " + evidenceMarker, ResultJSON: `{"stdout":"` + evidenceMarker + `"}`, Error: "no such file " + evidenceMarker, IsOperatorTool: true, LoopTurn: 1,
		}},
		FinalOutput:     "the answer is " + evidenceMarker,
		ProviderFailure: string(ClassifyExecutionFailure(fmt.Errorf("model echoed %s", evidenceMarker))),
	}
	traceBody, err := SealAgentTrace(trace)
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, req.RunID, completed.GetAssignmentId(), traceBody))
	loadedTrace, err := store.LoadAssignmentAgentTrace(ctx, req.RunID, completed.GetAssignmentId())
	require.NoError(t, err)
	assert.Equal(t, trace.FinalOutput, loadedTrace.FinalOutput)
	assert.Equal(t, trace.ToolCalls[0].ArgumentsJSON, loadedTrace.ToolCalls[0].ArgumentsJSON)
	assert.Equal(t, string(ExecutionFailureInternal), loadedTrace.ProviderFailure)

	// Exchange evidence: the recorded Gateway request and response bodies.
	exchanges, err := json.Marshal([]map[string]any{{"req_body": map[string]string{"command": "cat " + evidenceMarker}, "resp_body": map[string]string{"output": evidenceMarker}}})
	require.NoError(t, err)
	exchangeRef, err := store.SaveJSONArtifact(ctx, EvidenceScope{RunID: req.RunID, ScenarioID: "scenario-1", AttemptID: "attempt-1"}, complianceevidence.ArtifactTypeEvalExchange, exchanges)
	require.NoError(t, err)
	exchangeBody, err := rt.files.ReadFile(ctx, filepath.Join(evaluationEvidenceDir(req.RunID), exchangeRef.GetSha256()+constants.FileExtJSON))
	require.NoError(t, err)
	assert.Contains(t, string(exchangeBody), evidenceMarker, "the exchange evidence round-trips through the encrypted store")

	// The export bundle is public by design and must carry none of it.
	exportReport, err := NewCampaignExporter(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() }).ExportRun(ctx, store, rt.files, req.RunID, constants.EvaluationCampaignExportDirname)
	require.NoError(t, err)
	require.NotEmpty(t, exportReport.Files)
	exportedAssignments, err := rt.raw.ReadFile(ctx, filepath.Join(constants.EvaluationCampaignExportDirname, constants.EvaluationAssignmentsJSONLFilename))
	require.NoError(t, err)
	assert.Contains(t, string(exportedAssignments), completed.GetAssignmentId(), "the export is a readable bundle, not ciphertext")

	scanned := assertNoPlaintextAnywhere(t, rt.raw.Resolve(""), evidenceMarker, prompt)
	assert.Greater(t, scanned, 10, "the walk covered the evaluation records, vault, and export")
}

// TestEvalBackup_CarriesCiphertextOnly proves a backup leaves the runtime in
// its encrypted form, key chain included, and restores into any runtime whose
// OS key store holds the master key that sealed that key chain.
func TestEvalBackup_CarriesCiphertextOnly(t *testing.T) {
	keyring := keystoretest.NewMemoryKeyring()
	rt := newSealedRuntimeWithKeyring(t, keyring)
	ctx := context.Background()
	recordPath := filepath.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, "run-1", constants.ConformanceRecordFilename)
	require.NoError(t, rt.files.WriteFile(ctx, recordPath, []byte(`{"detail":"`+evidenceMarker+`"}`), constants.PermFilePrivate))

	backupDir := filepath.Join(testutil.TempDir(t), "backups")
	report, err := NewEvalBackup(rt.files, time.Now).Create(ctx, backupDir)
	require.NoError(t, err)
	require.NotEmpty(t, report.Files)
	assertNoPlaintextAnywhere(t, backupDir, evidenceMarker)

	// Restoring into the same runtime after the record is lost makes it readable again.
	require.NoError(t, rt.raw.Remove(ctx, recordPath))
	_, err = NewEvalBackup(rt.files, time.Now).Restore(ctx, report.SnapshotDir, false)
	require.NoError(t, err)
	restored, err := rt.files.ReadFile(ctx, recordPath)
	require.NoError(t, err)
	assert.Contains(t, string(restored), evidenceMarker)

	// A reinstalled runtime has a new vault on the same host key store: the
	// restore opens the snapshot's vault and reseals the evidence under its own.
	reinstalled := newSealedRuntimeWithKeyring(t, keyring)
	_, err = NewEvalBackup(reinstalled.files, time.Now).Restore(ctx, report.SnapshotDir, false)
	require.NoError(t, err)
	restored, err = reinstalled.files.ReadFile(ctx, recordPath)
	require.NoError(t, err)
	assert.Contains(t, string(restored), evidenceMarker)
	assertNoPlaintextAnywhere(t, reinstalled.raw.Resolve(""), evidenceMarker)

	// A host whose key store never held the master key cannot open the snapshot.
	other := newSealedRuntime(t)
	_, err = NewEvalBackup(other.files, time.Now).Restore(ctx, report.SnapshotDir, false)
	require.True(t, errors.Is(err, constants.ErrEvaluationBackupKeyChainInvalid), "a key chain the key store cannot open restores nothing: %v", err)
	exists, err := other.files.FileExists(ctx, recordPath)
	require.NoError(t, err)
	assert.False(t, exists)
}
