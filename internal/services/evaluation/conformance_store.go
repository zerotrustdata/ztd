// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// SaveConformanceRecord writes the canonical record and the lower-case hex
// sha256 of those bytes next to it. It refuses to overwrite an existing record.
func (s *Store) SaveConformanceRecord(ctx context.Context, record *evalv1.ConformanceRunRecord) error {
	if s == nil || s.files == nil || record == nil || !complianceevidence.ValidPathElement(record.GetRunId()) {
		return fmt.Errorf("%w: file service and canonical conformance run ID are required", constants.ErrEvaluationReportPersistFailed)
	}
	body, err := evalv1.MarshalCanonical(record)
	if err != nil {
		return fmt.Errorf("%w: canonicalize conformance record: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	digest := sha256.Sum256(body)
	runDir := evaluationRunDir(record.GetRunId())
	recordPath := filepath.Join(runDir, constants.ConformanceRecordFilename)
	digestPath := filepath.Join(runDir, constants.ConformanceRecordDigestFilename)

	for _, relPath := range []string{recordPath, digestPath} {
		exists, err := s.files.FileExists(ctx, relPath)
		if err != nil {
			return fmt.Errorf("%w: check conformance record: %w", constants.ErrEvaluationReportPersistFailed, err)
		}
		if exists {
			return fmt.Errorf("%w: conformance record exists for run %s", constants.ErrEvaluationReportPersistFailed, record.GetRunId())
		}
	}
	if err := s.files.MkdirAll(ctx, runDir, constants.PermDirPrivate); err != nil {
		return fmt.Errorf("%w: create run directory: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	if err := s.files.WriteFile(ctx, recordPath, body, constants.PermFileReadOnly); err != nil {
		return fmt.Errorf("%w: write conformance record: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	if err := s.files.WriteFile(ctx, digestPath, []byte(hex.EncodeToString(digest[:])), constants.PermFileReadOnly); err != nil {
		return fmt.Errorf("%w: write conformance record digest: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	return nil
}

// LoadConformanceRecordRaw returns the persisted bytes and digest unparsed, for
// verify.VerifyPersisted.
func (s *Store) LoadConformanceRecordRaw(ctx context.Context, runID string) (raw []byte, digestHex string, err error) {
	if s == nil {
		return nil, "", fmt.Errorf("%w: store is required", constants.ErrConformanceRecordInvalid)
	}
	return readConformanceRecordRaw(ctx, s.files, runID)
}
