// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"path/filepath"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

// EvidenceScope binds a persisted evidence artifact to its run, scenario,
// attempt, and transaction so a returned reference can never describe a body
// outside its declared scope.
type EvidenceScope struct {
	RunID         string
	ScenarioID    string
	AttemptID     string
	TransactionID string
}

// Store persists evaluation records beneath one evaluation layout.
type Store struct {
	files  fs.RuntimeFileService
	layout evalLayout
}

// NewStore returns a store over the active evaluation layout.
func NewStore(files fs.RuntimeFileService) *Store {
	return &Store{files: files, layout: activeEvalLayout()}
}

// NewArchivedStore returns a store over the archive layout for archived
// campaigns and the runs archived with them. Archived campaigns and runs keep
// their internal structure, so every read works against this store exactly as
// it does against the active one.
func NewArchivedStore(files fs.RuntimeFileService) *Store {
	return &Store{files: files, layout: archivedEvalLayout()}
}

// NewArchivedRunStore returns a store for runs that were archived on their own:
// run records come from the archive while their campaign is still active.
func NewArchivedRunStore(files fs.RuntimeFileService) *Store {
	return &Store{files: files, layout: archivedRunLayout()}
}

// SaveProtoArtifact persists one canonical protobuf evidence body beneath the
// run's evidence directory and returns a scope-bound reference. The body is
// durably written before the reference is returned.
func (s *Store) SaveProtoArtifact(ctx context.Context, scope EvidenceScope, artifactType complianceevidence.ArtifactType, message proto.Message) (*compliancev1.ComplianceEvidenceReference, error) {
	if s == nil || s.files == nil || message == nil || !validEvidenceScope(scope) || !complianceevidence.ContainsArtifactType(complianceevidence.SupportedArtifactTypes(), artifactType) {
		return nil, fmt.Errorf("%w: evidence scope, supported artifact type, and protocol message are required", constants.ErrEvaluationReportPersistFailed)
	}
	artifact, err := complianceevidence.PersistCanonicalProtoArtifact(ctx, s.files, s.layout.evidenceDir(scope.RunID), artifactType, message)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", constants.ErrEvaluationReportPersistFailed, err)
	}
	return scopedReference(artifact.Reference, scope), nil
}

// SaveJSONArtifact persists one already-canonical JSON evidence body, such as
// a recorded exchange or admission response, beneath the run's evidence
// directory and returns a scope-bound reference.
func (s *Store) SaveJSONArtifact(ctx context.Context, scope EvidenceScope, artifactType complianceevidence.ArtifactType, body []byte) (*compliancev1.ComplianceEvidenceReference, error) {
	if s == nil || s.files == nil || len(body) == 0 || !validEvidenceScope(scope) || !complianceevidence.ContainsArtifactType(complianceevidence.SupportedArtifactTypes(), artifactType) {
		return nil, fmt.Errorf("%w: evidence scope, supported artifact type, and canonical body are required", constants.ErrEvaluationReportPersistFailed)
	}
	if err := complianceevidence.ValidateCanonicalJSON(body); err != nil {
		return nil, fmt.Errorf("%w: canonical evidence body: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	artifactID := complianceevidence.ContentAddress(artifactType, body)
	_, digest, ok := complianceevidence.ParseContentAddress(artifactID)
	if !ok {
		return nil, fmt.Errorf("%w: evidence content address is invalid", constants.ErrEvaluationReportPersistFailed)
	}
	directory := s.layout.evidenceDir(scope.RunID)
	if err := s.files.MkdirAll(ctx, directory, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("%w: create evidence directory: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	if err := s.files.WriteFile(ctx, filepath.Join(directory, digest+constants.FileExtJSON), body, constants.PermFileReadOnly); err != nil {
		return nil, fmt.Errorf("%w: write evidence artifact: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	return scopedReference(&compliancev1.ComplianceEvidenceReference{
		ArtifactId: artifactID, ArtifactType: string(artifactType), Sha256: digest, MediaType: constants.MediaTypeJSON,
	}, scope), nil
}

func validEvidenceScope(scope EvidenceScope) bool {
	if !complianceevidence.ValidPathElement(scope.RunID) {
		return false
	}
	if scope.ScenarioID != "" && !complianceevidence.ValidPathElement(scope.ScenarioID) {
		return false
	}
	if scope.AttemptID != "" && !complianceevidence.ValidPathElement(scope.AttemptID) {
		return false
	}
	return true
}

func scopedReference(reference *compliancev1.ComplianceEvidenceReference, scope EvidenceScope) *compliancev1.ComplianceEvidenceReference {
	reference.RunId = scope.RunID
	reference.ScenarioId = scope.ScenarioID
	reference.AttemptId = scope.AttemptID
	reference.TransactionId = scope.TransactionID
	return reference
}

// loadProtoArtifact reads the sealed body a reference names in the run's
// evidence directory. The body must hash to the reference's content address and
// decode as canonical protobuf JSON; a missing body reports constants.ErrNotFound.
func (s *Store) loadProtoArtifact(ctx context.Context, runID string, reference *compliancev1.ComplianceEvidenceReference, message proto.Message) error {
	if s == nil || s.files == nil || !complianceevidence.ValidPathElement(runID) || reference == nil || message == nil {
		return fmt.Errorf("%w: file service, run ID, evidence reference, and protocol message are required", constants.ErrEvidenceArtifactMalformed)
	}
	artifactType, digest, ok := complianceevidence.ParseContentAddress(reference.GetArtifactId())
	if !ok || string(artifactType) != reference.GetArtifactType() || digest != reference.GetSha256() || reference.GetRunId() != runID || !complianceevidence.ContainsArtifactType(complianceevidence.SupportedArtifactTypes(), artifactType) {
		return fmt.Errorf("%w: evidence reference binding is invalid", constants.ErrEvidenceArtifactMalformed)
	}
	body, err := s.files.ReadFile(ctx, filepath.Join(s.layout.evidenceDir(runID), digest+constants.FileExtJSON))
	if err != nil {
		return fmt.Errorf("evaluation: read evidence artifact %s: %w", reference.GetArtifactId(), err)
	}
	if complianceevidence.ContentAddress(artifactType, body) != reference.GetArtifactId() {
		return fmt.Errorf("%w: evidence body does not match its content address", constants.ErrEvidenceArtifactMalformed)
	}
	if err := compliancev1.UnmarshalCanonical(body, message); err != nil {
		return fmt.Errorf("%w: canonical evidence body: %v", constants.ErrEvidenceArtifactMalformed, err)
	}
	return nil
}

func evaluationRunDir(runID string) string {
	return filepath.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, runID)
}
