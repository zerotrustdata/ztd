// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// HeterogeneousScheduleRequest carries frozen inputs for one deterministic
// heterogeneous system-lane assignment matrix.
type HeterogeneousScheduleRequest struct {
	CampaignID    string
	RunID         string
	Catalog       *evalv1.EvaluationScenarioCatalog
	ActivePosture evalv1.EvaluationGovernancePosture
	StackSet      *HeterogeneousStackSet
	QueuedAt      time.Time
}

// BuildHeterogeneousAssignmentMatrix materializes stacks × (scenario, eligible
// role) cells: every stack runs every scenario once per role the scenario
// declares eligible, the same cell rule the model-role lane uses.
func BuildHeterogeneousAssignmentMatrix(req HeterogeneousScheduleRequest) ([]*evalv1.EvaluationAssignment, error) {
	if req.CampaignID == "" || req.RunID == "" || req.Catalog == nil || req.StackSet == nil || len(req.StackSet.Stacks) == 0 {
		return nil, fmt.Errorf("evaluation: build heterogeneous assignment matrix: %w", constants.ErrMissingRequiredField)
	}
	if err := ValidateHeterogeneousStackSet(req.StackSet); err != nil {
		return nil, fmt.Errorf("evaluation: build heterogeneous assignment matrix: %w", err)
	}
	scenarios := append([]*evalv1.EvaluationScenarioDefinition(nil), req.Catalog.GetScenarios()...)
	stacks := append([]*evalv1.HeterogeneousStackDefinition(nil), req.StackSet.Stacks...)
	sort.Slice(stacks, func(i, j int) bool {
		return stacks[i].GetStackId() < stacks[j].GetStackId()
	})
	queuedAt := req.QueuedAt
	if queuedAt.IsZero() {
		queuedAt = time.Now().UTC()
	}
	assignments := make([]*evalv1.EvaluationAssignment, 0)
	for _, stack := range stacks {
		for _, scenario := range scenarios {
			if !scenarioIncludedForPosture(scenario, req.ActivePosture) {
				continue
			}
			roles, err := scenarioEligibleRoles(scenario)
			if err != nil {
				return nil, fmt.Errorf("evaluation: build heterogeneous assignment matrix: %w", err)
			}
			for _, role := range roles {
				assignment, err := buildHeterogeneousAssignment(req.CampaignID, req.RunID, scenario, stack, role, queuedAt)
				if err != nil {
					return nil, err
				}
				assignments = append(assignments, assignment)
			}
		}
	}
	sortAssignmentsDeterministic(assignments, req.Catalog)
	return assignments, nil
}

func buildHeterogeneousAssignment(campaignID, runID string, scenario *evalv1.EvaluationScenarioDefinition, stack *evalv1.HeterogeneousStackDefinition, role evalv1.ModelCampaignRole, queuedAt time.Time) (*evalv1.EvaluationAssignment, error) {
	if scenario == nil || stack == nil {
		return nil, fmt.Errorf("evaluation: build heterogeneous assignment: %w", constants.ErrMissingRequiredField)
	}
	assignment := &evalv1.EvaluationAssignment{
		SchemaVersion: CampaignSchemaVersion,
		CampaignId:    campaignID,
		RunId:         runID,
		ScenarioRef: &compliancev1.VersionedReference{
			Id:      scenario.GetScenarioId(),
			Version: scenario.GetScenarioVersion(),
		},
		ScenarioId:      scenario.GetScenarioId(),
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED,
		Repetition:      1,
		QueuedAt:        timestamppb.New(queuedAt),
		Target: &evalv1.EvaluationAssignment_Heterogeneous{
			Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{
				Stack:          stack,
				DesignatedRole: role,
			},
		},
	}
	identity, err := ComputeAssignmentDeterministicIdentity(assignment)
	if err != nil {
		return nil, fmt.Errorf("evaluation: build heterogeneous assignment: %w", err)
	}
	assignment.DeterministicIdentity = identity
	assignment.AssignmentId = identity
	return assignment, nil
}

// ValidateHeterogeneousAssignmentMatrix verifies the heterogeneous scheduler gate.
func ValidateHeterogeneousAssignmentMatrix(catalog *evalv1.EvaluationScenarioCatalog, stackSet *HeterogeneousStackSet, assignments []*evalv1.EvaluationAssignment, posture evalv1.EvaluationGovernancePosture) error {
	if catalog == nil || stackSet == nil {
		return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: %w", constants.ErrMissingRequiredField)
	}
	expected := FormationMatrixSize(catalog, uint64(len(stackSet.Stacks)), posture)
	if uint64(len(assignments)) != expected {
		return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: expected %d assignments, got %d", expected, len(assignments))
	}
	eligible := make(map[string]map[evalv1.ModelCampaignRole]struct{}, len(catalog.GetScenarios()))
	for _, scenario := range catalog.GetScenarios() {
		if !scenarioIncludedForPosture(scenario, posture) {
			continue
		}
		roles, err := scenarioEligibleRoles(scenario)
		if err != nil {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: %w", err)
		}
		set := make(map[evalv1.ModelCampaignRole]struct{}, len(roles))
		for _, role := range roles {
			set[role] = struct{}{}
		}
		eligible[scenario.GetScenarioId()] = set
	}
	seen := make(map[string]struct{}, len(assignments))
	cells := make(map[heterogeneousCell]struct{}, len(assignments))
	for _, assignment := range assignments {
		if assignment == nil || assignment.GetAssignmentId() == "" {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: %w", constants.ErrMissingRequiredField)
		}
		if assignment.GetLane() != evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: assignment %s has unexpected lane", assignment.GetAssignmentId())
		}
		if assignment.GetLifecycleStatus() != evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: assignment %s must start queued", assignment.GetAssignmentId())
		}
		identity, err := ComputeAssignmentDeterministicIdentity(assignment)
		if err != nil {
			return err
		}
		if assignment.GetDeterministicIdentity() != identity || assignment.GetAssignmentId() != identity {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: assignment %s identity mismatch", assignment.GetAssignmentId())
		}
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: duplicate assignment identity %s", identity)
		}
		seen[identity] = struct{}{}
		role := assignment.GetHeterogeneous().GetDesignatedRole()
		roles, known := eligible[assignment.GetScenarioId()]
		if !known {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: assignment %s scenario %s not in catalog: %w", assignment.GetAssignmentId(), assignment.GetScenarioId(), constants.ErrEvaluationScenarioRolesUnassigned)
		}
		if _, ok := roles[role]; !ok {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: assignment %s role %s not eligible for scenario %s: %w", assignment.GetAssignmentId(), role.String(), assignment.GetScenarioId(), constants.ErrEvaluationScenarioRolesUnassigned)
		}
		cell := heterogeneousCell{stackID: assignment.GetHeterogeneous().GetStack().GetStackId(), scenarioID: assignment.GetScenarioId(), role: role}
		if _, exists := cells[cell]; exists {
			return fmt.Errorf("evaluation: validate heterogeneous assignment matrix: stack %s scenario %s role %s: %w", cell.stackID, cell.scenarioID, role.String(), constants.ErrEvaluationAssignmentCellDuplicate)
		}
		cells[cell] = struct{}{}
	}
	return nil
}

// heterogeneousCell is the typed (stack, scenario, role) key a heterogeneous
// matrix must hold exactly once.
type heterogeneousCell struct {
	stackID    string
	scenarioID string
	role       evalv1.ModelCampaignRole
}
