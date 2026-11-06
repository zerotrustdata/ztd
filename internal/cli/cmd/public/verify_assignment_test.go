// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package public

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/assignmentaudit"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	assignmentTestSourceID  = "src-1"
	assignmentTestDBName    = "assignment-audit.db"
	assignmentTestRunID     = "run-1"
	assignmentTestAssignID  = "assignment-1"
	assignmentTestEventRows = 2
)

func assignmentTestFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	return files
}

// assignmentTestEventBody returns a live event body that passes the public feed
// schema and belongs to the test run and assignment, encoded as the Gateway encodes it.
func assignmentTestEventBody(t *testing.T, eventID string) []byte {
	t.Helper()
	body, err := evaluation.MarshalPublicLiveEvent(&evalv1.PublicLiveEvent{
		SchemaVersion:   "1.5.0",
		Kind:            "stage_updated",
		DatasetId:       "dataset-1",
		QualityState:    "live_in_progress",
		ObservedAt:      "2026-10-01T00:00:00Z",
		EventId:         eventID,
		RunId:           assignmentTestRunID,
		AssignmentId:    assignmentTestAssignID,
		VariantId:       "variant-a",
		Role:            "primary",
		LifecycleStatus: "running",
		Completed:       proto.Uint32(1),
		Total:           proto.Uint32(2),
		StageLabel:      "model role invoked",
	})
	require.NoError(t, err)
	return body
}

// buildAssignmentTestDatabase returns an assignment audit database of two live
// events that belong to the test run and assignment.
func buildAssignmentTestDatabase(t *testing.T, files fs.RuntimeFileService) []byte {
	t.Helper()
	records := make([]models.PublicFeedRecord, 0, assignmentTestEventRows)
	for sequence := int64(1); sequence <= assignmentTestEventRows; sequence++ {
		body := assignmentTestEventBody(t, fmt.Sprintf("event-%d", sequence))
		records = append(records, assignmentTestRecord(sequence, body))
	}
	header := assignmentaudit.Header{
		SourceID:         assignmentTestSourceID,
		CampaignID:       "campaign-1",
		CampaignRevision: "revision-1",
		RunID:            assignmentTestRunID,
		AssignmentID:     assignmentTestAssignID,
	}
	data, err := assignmentaudit.BuildDatabase(context.Background(), files, header, records)
	require.NoError(t, err)
	return data
}

func assignmentTestRecord(sequence int64, body []byte) models.PublicFeedRecord {
	sum := sha256.Sum256(body)
	return models.PublicFeedRecord{
		Sequence:    sequence,
		RecordType:  models.PublicFeedRecordTypeEvent,
		RecordHash:  hex.EncodeToString(sum[:]),
		RecordBytes: string(body),
	}
}

// writeAssignmentTestDatabase stores database bytes under the runtime root and
// returns their absolute path.
func writeAssignmentTestDatabase(t *testing.T, files fs.RuntimeFileService, data []byte) string {
	t.Helper()
	require.NoError(t, files.WriteFile(context.Background(), assignmentTestDBName, data, constants.PermFilePrivate))
	return files.Resolve(assignmentTestDBName)
}

func TestVerifyAssignmentCmd_VerifiesDigestOnlyDatabase(t *testing.T) {
	files := assignmentTestFiles(t)
	data := buildAssignmentTestDatabase(t, files)
	dbPath := writeAssignmentTestDatabase(t, files, data)
	f := newPointerBundleFixture(t, files, &evalv1.PublicAssignmentResultProjection{RunId: assignmentTestRunID, AssignmentId: assignmentTestAssignID, EvidenceBindings: []*evalv1.PublicEvidenceBinding{{Sha256: models.SHA256Hex(data), Kind: evaluation.AssignmentAuditSliceKind, SchemaRef: "assignment-audit/v1"}}})
	cmd := publicVerifyAssignmentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(f.args(), "--db", dbPath))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "Assignment audit verified (run run-1, assignment assignment-1, 2 events")
}
func TestVerifyAssignmentCmd_RejectsUnboundAndWrongAssignmentDatabases(t *testing.T) {
	for _, assignmentID := range []string{assignmentTestAssignID, "another-assignment"} {
		t.Run(assignmentID, func(t *testing.T) {
			files := assignmentTestFiles(t)
			data := buildAssignmentTestDatabase(t, files)
			dbPath := writeAssignmentTestDatabase(t, files, data)
			digest := models.SHA256Hex(data)
			if assignmentID == assignmentTestAssignID {
				digest = models.SHA256Hex([]byte("other database"))
			}
			f := newPointerBundleFixture(t, files, &evalv1.PublicAssignmentResultProjection{RunId: assignmentTestRunID, AssignmentId: assignmentID, EvidenceBindings: []*evalv1.PublicEvidenceBinding{{Sha256: digest, Kind: evaluation.AssignmentAuditSliceKind, SchemaRef: "assignment-audit/v1"}}})
			cmd := publicVerifyAssignmentCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(append(f.args(), "--db", dbPath))
			require.ErrorIs(t, cmd.Execute(), constants.ErrPublicAssignmentAuditNotBound)
		})
	}
}
func TestVerifyAssignmentCmd_RejectsBoundDatabaseWithTamperedEvent(t *testing.T) {
	files := assignmentTestFiles(t)
	dbPath := writeAssignmentTestDatabase(t, files, buildAssignmentTestDatabase(t, files))
	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE events SET record_bytes = ? WHERE sequence = 1`, string(assignmentTestEventBody(t, "event-forged")))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	data, err := files.ReadFile(t.Context(), assignmentTestDBName)
	require.NoError(t, err)
	f := newPointerBundleFixture(t, files, &evalv1.PublicAssignmentResultProjection{RunId: assignmentTestRunID, AssignmentId: assignmentTestAssignID, EvidenceBindings: []*evalv1.PublicEvidenceBinding{{Sha256: models.SHA256Hex(data), Kind: evaluation.AssignmentAuditSliceKind, SchemaRef: "assignment-audit/v1"}}})
	cmd := publicVerifyAssignmentCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append(f.args(), "--db", dbPath))
	require.ErrorIs(t, cmd.Execute(), constants.ErrPublicFeedRecordHashMismatch)
}
