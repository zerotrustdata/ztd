// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package assignmentaudit builds and verifies the plaintext public
// assignment-audit database: a standalone SQLite file holding the public feed
// live-event records of exactly one assignment, exactly as the Gateway stored
// them. The file has no encryption, no vault header, and no internal chain.
// Its authenticity comes from the Gateway's signed proof segment, which covers
// the file's SHA-256.
package assignmentaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// assignmentDatabaseSchema is the canonical schema, embedded from schema.sql.
//
//go:embed schema.sql
var assignmentDatabaseSchema string

const (
	databaseFilename = "assignment-audit.db"
	scratchPattern   = "assignment-database-*"
)

// Header identifies the single assignment a database covers.
type Header struct {
	SourceID, CampaignID, CampaignRevision, RunID, AssignmentID string
}

// BuildDatabase writes records into a fresh SQLite file and returns its bytes.
// Every record must be a public feed event whose record_hash matches its
// record_bytes, that passes the public feed validator, and that belongs to the
// header's assignment. Sequences must strictly increase.
// The file is assembled in a runtime scratch directory that is removed before
// returning.
func BuildDatabase(ctx context.Context, fileSvc fs.RuntimeFileService, header Header, records []models.PublicFeedRecord) ([]byte, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("assignmentaudit: build database: file service: %w", constants.ErrMissingRequiredField)
	}
	if err := validateHeader(header); err != nil {
		return nil, fmt.Errorf("assignmentaudit: build database: %w", err)
	}
	if err := validateRecords(header, records); err != nil {
		return nil, fmt.Errorf("assignmentaudit: build database: %w", err)
	}

	scratchDir, err := fileSvc.MkdirTemp(ctx, scratchPattern)
	if err != nil {
		return nil, fmt.Errorf("assignmentaudit: build database: scratch directory: %w", err)
	}
	defer removeScratch(fileSvc, scratchDir)

	dbRelPath := filepath.Join(scratchDir, databaseFilename)
	if err := writeDatabase(ctx, fileSvc, fileSvc.Resolve(dbRelPath), header, records); err != nil {
		return nil, err
	}
	data, err := fileSvc.ReadFile(ctx, dbRelPath)
	if err != nil {
		return nil, fmt.Errorf("assignmentaudit: build database: read database: %w", err)
	}
	return data, nil
}

// VerifyDatabase checks a database image fully: the assignment row, then every
// event against the rules BuildDatabase enforces. It returns the header and the
// records ordered by sequence. The image is loaded into a private in-memory
// database, so the caller passes the same bytes it digested and the verified
// records always come from those bytes, never from a second read of a file.
func VerifyDatabase(ctx context.Context, image []byte) (Header, []models.PublicFeedRecord, error) {
	db, err := sqliteutil.OpenReadOnlyDBFromBytes(ctx, image, slog.Default())
	if err != nil {
		return Header{}, nil, fmt.Errorf("assignmentaudit: verify database: open: %w", err)
	}
	defer db.Close()

	header, err := readHeader(ctx, db)
	if err != nil {
		return Header{}, nil, fmt.Errorf("assignmentaudit: verify database: %w", err)
	}
	if err := validateHeader(header); err != nil {
		return Header{}, nil, fmt.Errorf("assignmentaudit: verify database: %w", err)
	}
	records, err := readRecords(ctx, db)
	if err != nil {
		return Header{}, nil, fmt.Errorf("assignmentaudit: verify database: %w", err)
	}
	if err := validateRecords(header, records); err != nil {
		return Header{}, nil, fmt.Errorf("assignmentaudit: verify database: %w", err)
	}
	return header, records, nil
}

// writeDatabase creates the database at path with its schema, header row, and
// event rows in one transaction. It then switches the file to rollback-journal
// mode so the file alone holds every committed page, with no WAL or SHM
// sidecar left to be read alongside it.
func writeDatabase(ctx context.Context, fileSvc fs.RuntimeFileService, path string, header Header, records []models.PublicFeedRecord) error {
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(path), slog.Default())
	if err != nil {
		return fmt.Errorf("assignmentaudit: build database: open: %w", err)
	}
	defer db.Close()

	if err := db.ExecInTxWithRetry(ctx, func(tx *sql.Tx) error {
		return insertDatabaseRows(ctx, tx, header, records)
	}); err != nil {
		return fmt.Errorf("assignmentaudit: build database: write rows: %w", err)
	}

	var journalMode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode = DELETE`).Scan(&journalMode); err != nil {
		return fmt.Errorf("assignmentaudit: build database: set journal mode: %w", err)
	}
	if journalMode != "delete" {
		return fmt.Errorf("assignmentaudit: build database: journal mode is %q, want delete", journalMode)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("assignmentaudit: build database: close: %w", err)
	}
	return nil
}

func insertDatabaseRows(ctx context.Context, tx *sql.Tx, header Header, records []models.PublicFeedRecord) error {
	if _, err := tx.ExecContext(ctx, assignmentDatabaseSchema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO assignment (id, source_id, campaign_id, campaign_revision, run_id, assignment_id)
		VALUES (1, ?, ?, ?, ?, ?)
	`, header.SourceID, header.CampaignID, header.CampaignRevision, header.RunID, header.AssignmentID); err != nil {
		return fmt.Errorf("write assignment row: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (sequence, record_type, record_hash, record_bytes)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare event insert: %w", err)
	}
	defer stmt.Close()
	for _, record := range records {
		if _, err := stmt.ExecContext(ctx, record.Sequence, string(record.RecordType), record.RecordHash, record.RecordBytes); err != nil {
			return fmt.Errorf("write event %d: %w", record.Sequence, err)
		}
	}
	return nil
}

func readHeader(ctx context.Context, db *sqliteutil.DB) (Header, error) {
	var header Header
	err := db.QueryRowContext(ctx, `
		SELECT source_id, campaign_id, campaign_revision, run_id, assignment_id
		FROM assignment
		WHERE id = 1
	`).Scan(&header.SourceID, &header.CampaignID, &header.CampaignRevision, &header.RunID, &header.AssignmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return Header{}, fmt.Errorf("assignment row: %w", constants.ErrEvidenceArtifactMalformed)
	}
	if err != nil {
		return Header{}, fmt.Errorf("read assignment row: %w", err)
	}
	return header, nil
}

func readRecords(ctx context.Context, db *sqliteutil.DB) ([]models.PublicFeedRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT sequence, record_type, record_hash, record_bytes
		FROM events
		ORDER BY sequence ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	records := make([]models.PublicFeedRecord, 0)
	for rows.Next() {
		var record models.PublicFeedRecord
		var recordType string
		if err := rows.Scan(&record.Sequence, &recordType, &record.RecordHash, &record.RecordBytes); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		record.RecordType = models.PublicFeedRecordType(recordType)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return records, nil
}

func validateHeader(header Header) error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "source_id", value: header.SourceID},
		{name: "campaign_id", value: header.CampaignID},
		{name: "campaign_revision", value: header.CampaignRevision},
		{name: "run_id", value: header.RunID},
		{name: "assignment_id", value: header.AssignmentID},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("header %s: %w", field.name, constants.ErrMissingRequiredField)
		}
	}
	return nil
}

// validateRecords applies the build rules to an ordered record list. BuildDatabase
// runs it on its input and VerifyDatabase runs it on the rows read back, so both
// paths enforce the same rules.
func validateRecords(header Header, records []models.PublicFeedRecord) error {
	if len(records) == 0 {
		return fmt.Errorf("no records: %w", constants.ErrMissingRequiredField)
	}
	for index, record := range records {
		if record.RecordType != models.PublicFeedRecordTypeEvent {
			return fmt.Errorf("record %d: type %q: %w", record.Sequence, record.RecordType, constants.ErrPublicFeedRecordTypeInvalid)
		}
		sum := sha256.Sum256([]byte(record.RecordBytes))
		if hex.EncodeToString(sum[:]) != record.RecordHash {
			return fmt.Errorf("record %d: %w", record.Sequence, constants.ErrPublicFeedRecordHashMismatch)
		}
		if err := publicdisclosure.ValidatePublicFeedRecord(record.RecordType, []byte(record.RecordBytes)); err != nil {
			return fmt.Errorf("record %d: %w", record.Sequence, err)
		}
		event := &evalv1.PublicLiveEvent{}
		if err := evalv1.UnmarshalCanonical([]byte(record.RecordBytes), event); err != nil {
			return fmt.Errorf("record %d: decode live event: %w: %w", record.Sequence, constants.ErrPublicFeedRecordSchemaInvalid, err)
		}
		if event.GetRunId() != header.RunID || event.GetAssignmentId() != header.AssignmentID {
			return fmt.Errorf("record %d: run %q assignment %q: %w", record.Sequence, event.GetRunId(), event.GetAssignmentId(), constants.ErrEvidenceScopeMismatch)
		}
		if index > 0 && record.Sequence <= records[index-1].Sequence {
			return fmt.Errorf("record %d after record %d: %w", record.Sequence, records[index-1].Sequence, constants.ErrPublicFeedSequenceOutOfOrder)
		}
	}
	return nil
}

// removeScratch runs on a background context so a cancelled caller still cleans up.
func removeScratch(files fs.RuntimeFileService, relDir string) {
	if err := files.RemoveAll(context.Background(), relDir); err != nil {
		slog.Warn("assignmentaudit: remove scratch directory", "path", relDir, "error", err)
	}
}
