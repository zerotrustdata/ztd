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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
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
