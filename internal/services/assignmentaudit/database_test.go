// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package assignmentaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const (
	testRunID        = "run-1"
	testAssignmentID = "assignment-1"
)

func testHeader() Header {
	return Header{
		SourceID:         "source-1",
		CampaignID:       "campaign-1",
		CampaignRevision: "revision-1",
		RunID:            testRunID,
		AssignmentID:     testAssignmentID,
	}
}

func newTestFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	return files
}

// liveEvent returns a live event that passes the public feed validator for the
// given identity.
func liveEvent(eventID, runID, assignmentID string) *evalv1.PublicLiveEvent {
	return &evalv1.PublicLiveEvent{
		SchemaVersion:   "1.9.0",
		Kind:            "stage_updated",
		DatasetId:       "dataset-1",
		QualityState:    "live_in_progress",
		ObservedAt:      "2026-10-19T00:00:00Z",
		EventId:         eventID,
		RunId:           runID,
		AssignmentId:    assignmentID,
		LifecycleStatus: "running",
		Completed:       proto.Uint32(1),
		Total:           proto.Uint32(2),
	}
}

// marshalEvent returns the canonical live event body the Gateway encodes.
func marshalEvent(t *testing.T, event *evalv1.PublicLiveEvent) []byte {
	t.Helper()
	body, err := evalv1.MarshalCanonical(event)
	require.NoError(t, err)
	return body
}

// eventBody returns the canonical body of a valid live event for the identity.
func eventBody(t *testing.T, eventID, runID, assignmentID string) []byte {
	t.Helper()
	return marshalEvent(t, liveEvent(eventID, runID, assignmentID))
}

// feedRecord wraps body as a feed record carrying its SHA-256 content hash.
func feedRecord(sequence int64, recordType models.PublicFeedRecordType, body []byte) models.PublicFeedRecord {
	sum := sha256.Sum256(body)
	return models.PublicFeedRecord{
		Sequence:    sequence,
		RecordType:  recordType,
		RecordHash:  hex.EncodeToString(sum[:]),
		RecordBytes: string(body),
	}
}

// eventRecord returns an event record for sequence whose body names runID and
// assignmentID.
func eventRecord(t *testing.T, sequence int64, runID, assignmentID string) models.PublicFeedRecord {
	t.Helper()
	return feedRecord(sequence, models.PublicFeedRecordTypeEvent, eventBody(t, fmt.Sprintf("event-%d", sequence), runID, assignmentID))
}

// validRecords returns two in-scope event records with increasing sequences.
func validRecords(t *testing.T) []models.PublicFeedRecord {
	t.Helper()
	return []models.PublicFeedRecord{
		eventRecord(t, 4, testRunID, testAssignmentID),
		eventRecord(t, 9, testRunID, testAssignmentID),
	}
}

// mutateDatabase stages built bytes under the runtime root, runs one statement
// against that file, and returns the file's bytes after the change. The returned
// bytes are what a verifier would digest and load.
func mutateDatabase(t *testing.T, files fs.RuntimeFileService, data []byte, statement string, args ...any) []byte {
	t.Helper()
	ctx := context.Background()
	rel := filepath.Join("exports", databaseFilename)
	require.NoError(t, files.WriteFile(ctx, rel, data, constants.PermFilePrivate))
	execOnDatabaseFile(t, files.Resolve(rel), statement, args...)
	mutated, err := files.ReadFile(ctx, rel)
	require.NoError(t, err)
	return mutated
}

// execOnDatabaseFile runs one statement against the file through a plain SQLite
// handle, so the file keeps the journal mode the build wrote.
func execOnDatabaseFile(t *testing.T, path, statement string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, execErr := db.Exec(statement, args...)
	closeErr := db.Close()
	require.NoError(t, execErr)
	require.NoError(t, closeErr)
}

// requireScratchRemoved asserts the build left no scratch directory behind.
func requireScratchRemoved(t *testing.T, files fs.RuntimeFileService) {
	t.Helper()
	entries, err := files.ReadDir(context.Background(), constants.TmpDirname)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestBuildAndVerifyDatabaseRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newTestFiles(t)
	header := testHeader()
	records := validRecords(t)

	data, err := BuildDatabase(ctx, files, header, records)
	require.NoError(t, err)
	requireScratchRemoved(t, files)

	gotHeader, gotRecords, err := VerifyDatabase(ctx, data)
	require.NoError(t, err)
	assert.Equal(t, header, gotHeader)
	assert.Equal(t, records, gotRecords)
}

func TestVerifyDatabaseRequiresImage(t *testing.T) {
	t.Parallel()
	_, _, err := VerifyDatabase(context.Background(), nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestVerifyDatabaseRejectsBytesThatAreNotSQLite(t *testing.T) {
	t.Parallel()
	_, _, err := VerifyDatabase(context.Background(), []byte("audit bytes of another assignment, not a database"))
	require.Error(t, err)
}

// TestVerifyDatabaseReadsOnlyTheGivenImage pins the single-read contract: the
// verified records are the records in the image passed in, whatever else is on
// disk at the database's location.
func TestVerifyDatabaseReadsOnlyTheGivenImage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newTestFiles(t)
	header := testHeader()
	records := validRecords(t)
	data, err := BuildDatabase(ctx, files, header, records)
	require.NoError(t, err)

	other := eventRecord(t, 4, testRunID, "other-assignment")
	otherData := mutateDatabase(t, files, data, `UPDATE events SET record_bytes = ?, record_hash = ? WHERE sequence = ?`, other.RecordBytes, other.RecordHash, 4)
	require.NotEqual(t, data, otherData)

	gotHeader, gotRecords, err := VerifyDatabase(ctx, data)
	require.NoError(t, err)
	assert.Equal(t, header, gotHeader)
	assert.Equal(t, records, gotRecords)
}

func TestBuildDatabaseRequiresFileService(t *testing.T) {
	t.Parallel()
	_, err := BuildDatabase(context.Background(), nil, testHeader(), validRecords(t))
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestBuildDatabaseRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	files := newTestFiles(t)

	badHash := eventRecord(t, 4, testRunID, testAssignmentID)
	badHash.RecordHash = strings.Repeat("0", 64)

	projection := feedRecord(4, models.PublicFeedRecordTypeProjection, eventBody(t, "event-4", testRunID, testAssignmentID))

	notLiveEvent := feedRecord(4, models.PublicFeedRecordTypeEvent, []byte(`{"not_a_live_event":true}`))

	// Decodes as a canonical PublicLiveEvent, so only the feed validator's
	// closed-vocabulary check rejects it.
	unknownKind := liveEvent("event-4", testRunID, testAssignmentID)
	unknownKind.Kind = "not_a_kind"
	closedVocabulary := feedRecord(4, models.PublicFeedRecordTypeEvent, marshalEvent(t, unknownKind))

	tests := []struct {
		name    string
		header  func(h *Header)
		records []models.PublicFeedRecord
		want    error
	}{
		{name: "source id missing", header: func(h *Header) { h.SourceID = "" }, records: validRecords(t), want: constants.ErrMissingRequiredField},
		{name: "campaign id missing", header: func(h *Header) { h.CampaignID = "" }, records: validRecords(t), want: constants.ErrMissingRequiredField},
		{name: "campaign revision blank", header: func(h *Header) { h.CampaignRevision = "   " }, records: validRecords(t), want: constants.ErrMissingRequiredField},
		{name: "run id missing", header: func(h *Header) { h.RunID = "" }, records: validRecords(t), want: constants.ErrMissingRequiredField},
		{name: "assignment id missing", header: func(h *Header) { h.AssignmentID = "" }, records: validRecords(t), want: constants.ErrMissingRequiredField},
		{name: "no records", records: nil, want: constants.ErrMissingRequiredField},
		{name: "projection record type", records: []models.PublicFeedRecord{projection}, want: constants.ErrPublicFeedRecordTypeInvalid},
		{name: "record hash mismatch", records: []models.PublicFeedRecord{badHash}, want: constants.ErrPublicFeedRecordHashMismatch},
		{name: "body is not a live event", records: []models.PublicFeedRecord{notLiveEvent}, want: constants.ErrPublicFeedRecordSchemaInvalid},
		{name: "closed vocabulary rejected by feed validator", records: []models.PublicFeedRecord{closedVocabulary}, want: constants.ErrPublicFeedRecordSchemaInvalid},
		{name: "run id outside header", records: []models.PublicFeedRecord{eventRecord(t, 4, "other-run", testAssignmentID)}, want: constants.ErrEvidenceScopeMismatch},
		{name: "assignment id outside header", records: []models.PublicFeedRecord{eventRecord(t, 4, testRunID, "other-assignment")}, want: constants.ErrEvidenceScopeMismatch},
		{name: "duplicate sequence", records: []models.PublicFeedRecord{eventRecord(t, 5, testRunID, testAssignmentID), eventRecord(t, 5, testRunID, testAssignmentID)}, want: constants.ErrPublicFeedSequenceOutOfOrder},
		{name: "decreasing sequence", records: []models.PublicFeedRecord{eventRecord(t, 9, testRunID, testAssignmentID), eventRecord(t, 4, testRunID, testAssignmentID)}, want: constants.ErrPublicFeedSequenceOutOfOrder},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			header := testHeader()
			if tc.header != nil {
				tc.header(&header)
			}
			_, err := BuildDatabase(context.Background(), files, header, tc.records)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestVerifyDatabaseDetectsTamperedRecordBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newTestFiles(t)
	data, err := BuildDatabase(ctx, files, testHeader(), validRecords(t))
	require.NoError(t, err)

	forged := eventBody(t, "event-forged", testRunID, testAssignmentID)
	tampered := mutateDatabase(t, files, data, `UPDATE events SET record_bytes = ? WHERE sequence = ?`, string(forged), 4)

	_, _, err = VerifyDatabase(ctx, tampered)
	require.ErrorIs(t, err, constants.ErrPublicFeedRecordHashMismatch)
}

func TestVerifyDatabaseRejectsEventOutsideAssignment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newTestFiles(t)
	data, err := BuildDatabase(ctx, files, testHeader(), validRecords(t))
	require.NoError(t, err)

	// Re-hash the replaced body so only the scope check can reject it.
	outside := eventRecord(t, 4, testRunID, "other-assignment")
	tampered := mutateDatabase(t, files, data, `UPDATE events SET record_bytes = ?, record_hash = ? WHERE sequence = ?`, outside.RecordBytes, outside.RecordHash, 4)

	_, _, err = VerifyDatabase(ctx, tampered)
	require.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)
}

func TestVerifyDatabaseRejectsMissingAssignmentRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newTestFiles(t)
	data, err := BuildDatabase(ctx, files, testHeader(), validRecords(t))
	require.NoError(t, err)

	tampered := mutateDatabase(t, files, data, `DELETE FROM assignment`)

	_, _, err = VerifyDatabase(ctx, tampered)
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}
