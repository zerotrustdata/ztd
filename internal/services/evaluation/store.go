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
	"path"

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
	if err := s.validateArtifactWrite(scope, artifactType, message != nil); err != nil {
		return nil, err
	}
	artifact, err := complianceevidence.PersistCanonicalProtoArtifact(ctx, s.files, s.layout.evidenceDir(scope.RunID), artifactType, message)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	return scopedReference(artifact.Reference, scope), nil
}

// SaveJSONArtifact persists one already-canonical JSON evidence body, such as
// a recorded exchange or admission response, beneath the run's evidence
// directory and returns a scope-bound reference.
func (s *Store) SaveJSONArtifact(ctx context.Context, scope EvidenceScope, artifactType complianceevidence.ArtifactType, body []byte) (*compliancev1.ComplianceEvidenceReference, error) {
	if err := s.validateArtifactWrite(scope, artifactType, len(body) > 0); err != nil {
		return nil, err
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
	if err := s.files.WriteFile(ctx, path.Join(directory, digest+constants.FileExtJSON), body, constants.PermFileReadOnly); err != nil {
		return nil, fmt.Errorf("%w: write evidence artifact: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	return scopedReference(&compliancev1.ComplianceEvidenceReference{
		ArtifactId: artifactID, ArtifactType: string(artifactType), Sha256: digest, MediaType: constants.MediaTypeJSON,
	}, scope), nil
}

// validateArtifactWrite reports, under constants.ErrEvaluationReportPersistFailed,
// the first unmet precondition of a run evidence write: the artifact write
// itself, then each scope identifier that names a path element.
func (s *Store) validateArtifactWrite(scope EvidenceScope, artifactType complianceevidence.ArtifactType, hasBody bool) error {
	if s == nil {
		return missingField(constants.ErrEvaluationReportPersistFailed, "store")
	}
	if err := complianceevidence.ValidateArtifactWrite(s.files, artifactType, hasBody); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	elements := []pathElement{runIDElement(scope.RunID)}
	if scope.ScenarioID != "" {
		elements = append(elements, scenarioIDElement(scope.ScenarioID))
	}
	if scope.AttemptID != "" {
		elements = append(elements, attemptIDElement(scope.AttemptID))
	}
	return s.validateAccess(constants.ErrEvaluationReportPersistFailed, elements...)
}

// pathElement is one named identifier a Store call joins into a runtime path.
type pathElement struct {
	name  string
	value string
}

func campaignIDElement(id string) pathElement   { return pathElement{name: "campaign ID", value: id} }
func scenarioIDElement(id string) pathElement   { return pathElement{name: "scenario ID", value: id} }
func runIDElement(id string) pathElement        { return pathElement{name: "run ID", value: id} }
func assignmentIDElement(id string) pathElement { return pathElement{name: "assignment ID", value: id} }
func attemptIDElement(id string) pathElement    { return pathElement{name: "attempt ID", value: id} }

// validatePathElements reports the first element that is not a single safe
// path element, wrapping constants.ErrEvidencePathElementInvalid.
func validatePathElements(elements ...pathElement) error {
	for _, element := range elements {
		if err := complianceevidence.ValidatePathElement(element.value); err != nil {
			return fmt.Errorf("%s: %w", element.name, err)
		}
	}
	return nil
}

// validateAccess reports, under outer, the first unmet precondition of a Store
// call: the file service, then each identifier the call joins into a path.
func (s *Store) validateAccess(outer error, elements ...pathElement) error {
	if s == nil || s.files == nil {
		return missingField(outer, "file service")
	}
	if err := validatePathElements(elements...); err != nil {
		return fmt.Errorf("%w: %w", outer, err)
	}
	return nil
}

// missingField reports, under outer, that the named argument was not supplied.
func missingField(outer error, name string) error {
	return fmt.Errorf("%w: %s: %w", outer, name, constants.ErrMissingRequiredField)
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
	if reference == nil {
		return missingField(constants.ErrEvidenceArtifactMalformed, "evidence reference")
	}
	if message == nil {
		return missingField(constants.ErrEvidenceArtifactMalformed, "protocol message")
	}
	if err := s.validateAccess(constants.ErrEvidenceArtifactMalformed, runIDElement(runID)); err != nil {
		return err
	}
	artifactType, digest, ok := complianceevidence.ParseContentAddress(reference.GetArtifactId())
	if !ok || string(artifactType) != reference.GetArtifactType() || digest != reference.GetSha256() || reference.GetRunId() != runID || !complianceevidence.ContainsArtifactType(complianceevidence.SupportedArtifactTypes(), artifactType) {
		return fmt.Errorf("%w: evidence reference binding is invalid", constants.ErrEvidenceArtifactMalformed)
	}
	body, err := s.files.ReadFile(ctx, path.Join(s.layout.evidenceDir(runID), digest+constants.FileExtJSON))
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
	return path.Join(constants.EvaluationRunsPath, runID)
}
