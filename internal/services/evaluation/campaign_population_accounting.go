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
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// CampaignPopulationReport records matrix coverage and lifecycle accounting for
// one homogeneous campaign run.
type CampaignPopulationReport struct {
	RunID                 string
	ExpectedCells         uint64
	ScheduledAssignments  uint32
	QueuedCount           uint32
	RunningCount          uint32
	TerminalCount         uint32
	StoppedCount          uint32
	DispositionCounts     map[string]uint32
	MissingCells          []string
	DuplicateIdentities   []string
	ExtraAssignments      []string
	TerminalWithoutResult []string
	ResultWithoutTerminal []string
	Complete              bool
	FailureReasons        []string
	AccountedAt           time.Time
}

// CampaignPopulationAccountant independently verifies that one homogeneous run
// scheduled the full expected matrix and that lifecycle counts reconcile.
type CampaignPopulationAccountant struct {
	now func() time.Time
}

func NewCampaignPopulationAccountant(now func() time.Time) *CampaignPopulationAccountant {
	if now == nil {
		now = time.Now
	}
	return &CampaignPopulationAccountant{now: now}
}

// AccountRun recomputes population coverage from canonical assignment records.
func (a *CampaignPopulationAccountant) AccountRun(
	ctx context.Context,
	store CampaignStore,
	runID string,
	catalog *evalv1.EvaluationScenarioCatalog,
) (*CampaignPopulationReport, error) {
	if a == nil || store == nil || runID == "" || catalog == nil {
		return nil, fmt.Errorf("evaluation: account campaign population: %w", constants.ErrMissingRequiredField)
	}
	run, err := store.LoadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	binding := run.GetCampaignBinding()
	if binding == nil || binding.GetCampaignId() == "" {
		return nil, fmt.Errorf("evaluation: account campaign population: run binding: %w", constants.ErrMissingRequiredField)
	}
	spec, err := store.LoadCampaignSpec(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return nil, err
	}
	if err := ValidateCampaignSpecDigest(spec); err != nil {
		return nil, fmt.Errorf("evaluation: account campaign population: campaign spec: %w", err)
	}
	if binding.GetCampaignDigest() != spec.GetCampaignDigest() ||
		binding.GetCatalogDigest() != spec.GetCatalogDigest() ||
		binding.GetModelRegistryDigest() != spec.GetModelRegistryDigest() {
		return nil, fmt.Errorf("evaluation: account campaign population: run differs from frozen campaign: %w", constants.ErrEvidenceScopeMismatch)
	}
	if err := ValidateScenarioCatalogDigest(catalog); err != nil {
		return nil, fmt.Errorf("evaluation: account campaign population: catalog: %w", err)
	}
	if catalog.GetCatalogDigest() != spec.GetCatalogDigest() || !sameReference(catalog.GetCatalogRef(), spec.GetCatalogRef()) {
		return nil, fmt.Errorf("evaluation: account campaign population: catalog differs from frozen campaign: %w", constants.ErrEvidenceScopeMismatch)
	}
	assignments, err := store.ListAssignments(ctx, runID)
	if err != nil {
		return nil, err
	}
	var expectedMatrix []*evalv1.EvaluationAssignment
	switch run.GetLane() {
	case evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM:
		if spec.GetRepetitionCount() != 1 || spec.GetHoldContext() || spec.GetFormationStackSetDigest() == "" {
			return nil, fmt.Errorf("evaluation: account campaign population: invalid frozen system campaign: %w", constants.ErrEvaluationTargetKindMismatch)
		}
		stackSet, err := store.LoadHeterogeneousStackSet(ctx, spec.GetCampaignId())
		if err != nil {
			return nil, fmt.Errorf("evaluation: account campaign population: frozen formations: %w", err)
		}
		if err := validateCampaignFormationDigest(spec, stackSet); err != nil {
			return nil, fmt.Errorf("evaluation: account campaign population: frozen formations: %w", err)
		}
		expectedMatrix, err = BuildHeterogeneousAssignmentMatrix(HeterogeneousScheduleRequest{
			CampaignID: spec.GetCampaignId(), RunID: runID, Catalog: catalog, ActivePosture: run.GetActivePosture(), StackSet: stackSet,
		})
		if err != nil {
			return nil, fmt.Errorf("evaluation: account campaign population: expected system matrix: %w", err)
		}
	case evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE:
		if spec.GetFormationStackSetDigest() != "" || len(spec.GetModelRegistry()) != 1 {
			return nil, fmt.Errorf("evaluation: account campaign population: frozen campaign does not match model-role lane: %w", constants.ErrEvaluationTargetKindMismatch)
		}
		repetition := spec.GetRepetitionCount()
		if repetition == 0 {
			repetition = 1
		}
		expectedMatrix, err = BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
			CampaignID:      spec.GetCampaignId(),
			RunID:           runID,
			Catalog:         catalog,
			ActivePosture:   run.GetActivePosture(),
			Variants:        spec.GetModelRegistry(),
			RepetitionCount: repetition,
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("evaluation: account campaign population: run lane %s: %w", run.GetLane(), constants.ErrEvaluationTargetKindMismatch)
	}
	expected := uint64(len(expectedMatrix))
	report := &CampaignPopulationReport{
		RunID:             runID,
		ExpectedCells:     expected,
		DispositionCounts: make(map[string]uint32),
		AccountedAt:       a.now().UTC(),
	}
	expectedIdentities := make(map[string]string, len(expectedMatrix))
	for _, assignment := range expectedMatrix {
		cell := homogeneousAssignmentCellLabel(assignment)
		expectedIdentities[assignment.GetDeterministicIdentity()] = cell
	}
	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		if assignment == nil {
			report.FailureReasons = append(report.FailureReasons, "encountered nil assignment record")
			continue
		}
		report.ScheduledAssignments++
		identity := assignment.GetDeterministicIdentity()
		if identity == "" {
			report.FailureReasons = append(report.FailureReasons, fmt.Sprintf("assignment %s is missing deterministic identity", assignment.GetAssignmentId()))
			continue
		}
		recomputedIdentity, err := ComputeAssignmentDeterministicIdentity(assignment)
		if err != nil || recomputedIdentity != identity {
			report.FailureReasons = append(report.FailureReasons, fmt.Sprintf("assignment %s deterministic identity does not match its target", assignment.GetAssignmentId()))
		}
		if _, exists := seen[identity]; exists {
			report.DuplicateIdentities = append(report.DuplicateIdentities, identity)
		}
		seen[identity] = struct{}{}
		if _, ok := expectedIdentities[identity]; !ok {
			report.ExtraAssignments = append(report.ExtraAssignments, assignment.GetAssignmentId())
		}
		status := assignment.GetLifecycleStatus()
		report.DispositionCounts[status.String()]++
		switch status {
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED:
			report.QueuedCount++
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING:
			report.RunningCount++
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED:
			report.StoppedCount++
			report.TerminalCount++
		default:
			if isTerminalAssignmentLifecycle(status) {
				report.TerminalCount++
			}
		}
		hasResult, err := store.AssignmentResultExists(ctx, runID, assignment.GetAssignmentId())
		if err != nil {
			return nil, err
		}
		if isTerminalAssignmentLifecycle(status) && !hasResult {
			report.TerminalWithoutResult = append(report.TerminalWithoutResult, assignment.GetAssignmentId())
		}
		if hasResult && (status == evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED ||
			status == evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING) {
			report.ResultWithoutTerminal = append(report.ResultWithoutTerminal, assignment.GetAssignmentId())
		}
	}
	for identity, cell := range expectedIdentities {
		if _, ok := seen[identity]; !ok {
			report.MissingCells = append(report.MissingCells, cell)
		}
	}
	if uint64(report.ScheduledAssignments) != expected {
		report.FailureReasons = append(report.FailureReasons,
			fmt.Sprintf("scheduled assignments %d do not match expected matrix size %d", report.ScheduledAssignments, expected))
	}
	if len(report.MissingCells) > 0 {
		report.FailureReasons = append(report.FailureReasons,
			fmt.Sprintf("%d expected matrix cell(s) are not scheduled", len(report.MissingCells)))
	}
	if len(report.DuplicateIdentities) > 0 {
		report.FailureReasons = append(report.FailureReasons,
			fmt.Sprintf("%d duplicate deterministic identity(ies) detected", len(report.DuplicateIdentities)))
	}
	if len(report.ExtraAssignments) > 0 {
		report.FailureReasons = append(report.FailureReasons,
			fmt.Sprintf("%d assignment(s) fall outside the expected matrix", len(report.ExtraAssignments)))
	}
	if len(report.TerminalWithoutResult) > 0 {
		report.FailureReasons = append(report.FailureReasons,
			fmt.Sprintf("%d terminal assignment(s) lack persisted results", len(report.TerminalWithoutResult)))
	}
	if len(report.ResultWithoutTerminal) > 0 {
		report.FailureReasons = append(report.FailureReasons,
			fmt.Sprintf("%d assignment(s) have persisted results but non-terminal lifecycle", len(report.ResultWithoutTerminal)))
	}
	accounted := uint64(report.QueuedCount) + uint64(report.RunningCount) + uint64(report.TerminalCount)
	if accounted != uint64(report.ScheduledAssignments) {
		report.FailureReasons = append(report.FailureReasons,
			fmt.Sprintf("lifecycle accounting mismatch: queued(%d)+running(%d)+terminal(%d)=%d, scheduled=%d",
				report.QueuedCount, report.RunningCount, report.TerminalCount, accounted, report.ScheduledAssignments))
	}
	report.Complete = len(report.FailureReasons) == 0 &&
		report.TerminalCount == uint32(expected) &&
		uint64(report.ScheduledAssignments) == expected
	return report, nil
}

func homogeneousAssignmentCellLabel(assignment *evalv1.EvaluationAssignment) string {
	if assignment == nil {
		return ""
	}
	homogeneous, ok := assignment.GetTarget().(*evalv1.EvaluationAssignment_Homogeneous)
	if !ok || homogeneous.Homogeneous == nil || homogeneous.Homogeneous.GetCandidateVariant() == nil {
		return assignment.GetAssignmentId()
	}
	role := strings.TrimPrefix(homogeneous.Homogeneous.GetDesignatedRole().String(), "MODEL_CAMPAIGN_ROLE_")
	return fmt.Sprintf("%s/%s/rep-%d/%s",
		homogeneous.Homogeneous.GetCandidateVariant().GetServedModelTag(),
		role,
		assignment.GetRepetition(),
		assignment.GetScenarioId(),
	)
}
