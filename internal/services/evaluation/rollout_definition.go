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
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// RolloutDefinition is an ordered sequence of frozen campaigns. Duplicates
// intentionally mean another run of that campaign at that point in the sequence.
type RolloutDefinition struct {
	Name              string   `json:"name"`
	Campaigns         []string `json:"campaigns"`
	ContinueOnFailure bool     `json:"continue_on_failure"`
}

// NamedRollout keeps the editable definition and the history of its executions.
// Each execution snapshots its sequence so editing a definition cannot change
// an interrupted execution or the meaning of past results.
type NamedRollout struct {
	RolloutDefinition
	Executions []RolloutExecution `json:"executions"`
}

type RolloutExecution struct {
	ID                string        `json:"id"`
	StartedAt         time.Time     `json:"started_at"`
	ContinueOnFailure bool          `json:"continue_on_failure"`
	Steps             []RolloutStep `json:"steps"`
}

const (
	RolloutStepPending   = "pending"
	RolloutStepRunning   = "running"
	RolloutStepSucceeded = "succeeded"
	RolloutStepFailed    = "failed"
	RolloutStepStopped   = "stopped"
)

type RolloutStep struct {
	CampaignID string           `json:"campaign_id"`
	Status     string           `json:"status"`
	Attempts   []RolloutAttempt `json:"attempts,omitempty"`
}

// RolloutAttempt stores references and fixed failure classes only. Execution
// content and error text belong to neither the definition nor its history.
type RolloutAttempt struct {
	RunID        string                `json:"run_id"`
	FailureClass ExecutionFailureClass `json:"failure_class,omitempty"`
}

func rolloutDefinitionsDir() string {
	return filepath.Join(evalRoot(), constants.EvaluationRolloutsDirname)
}

func rolloutDefinitionPath(name string) string {
	return filepath.Join(rolloutDefinitionsDir(), name+constants.FileExtJSON)
}

func validateRolloutDefinition(def RolloutDefinition) error {
	if err := validatePathElements(pathElement{name: "rollout name", value: def.Name}); err != nil {
		return err
	}
	if len(def.Campaigns) == 0 {
		return fmt.Errorf("rollout %q needs at least one campaign: %w", def.Name, constants.ErrEvaluationSelectionEmpty)
	}
	for _, id := range def.Campaigns {
		if err := validatePathElements(campaignIDElement(id)); err != nil {
			return err
		}
	}
	return nil
}

func validateNamedRollout(rollout *NamedRollout) error {
	if err := validateRolloutDefinition(rollout.RolloutDefinition); err != nil {
		return err
	}
	seenExecutions, seenRuns := make(map[string]bool), make(map[string]bool)
	for _, execution := range rollout.Executions {
		if err := validatePathElements(pathElement{name: "rollout execution ID", value: execution.ID}); err != nil {
			return err
		}
		if seenExecutions[execution.ID] || len(execution.Steps) == 0 {
			return fmt.Errorf("evaluation: duplicate or empty rollout execution: %w", constants.ErrEvaluationFlagsInvalid)
		}
		seenExecutions[execution.ID] = true
		for _, step := range execution.Steps {
			if err := validatePathElements(campaignIDElement(step.CampaignID)); err != nil {
				return err
			}
			switch step.Status {
			case RolloutStepPending:
			case RolloutStepRunning, RolloutStepSucceeded, RolloutStepFailed, RolloutStepStopped:
				if len(step.Attempts) == 0 {
					return fmt.Errorf("evaluation: rollout step has no run reference: %w", constants.ErrEvaluationFlagsInvalid)
				}
			default:
				return fmt.Errorf("evaluation: unknown rollout step status: %w", constants.ErrEvaluationFlagsInvalid)
			}
			for _, attempt := range step.Attempts {
				if err := validatePathElements(runIDElement(attempt.RunID)); err != nil {
					return err
				}
				if seenRuns[attempt.RunID] {
					return fmt.Errorf("evaluation: duplicate rollout run reference: %w", constants.ErrEvaluationFlagsInvalid)
				}
				seenRuns[attempt.RunID] = true
			}
		}
	}
	return nil
}

func LoadNamedRollout(ctx context.Context, files fs.RuntimeFileService, name string) (*NamedRollout, error) {
	if files == nil {
		return nil, fmt.Errorf("evaluation: load named rollout: %w", constants.ErrMissingRequiredField)
	}
	if err := validatePathElements(pathElement{name: "rollout name", value: name}); err != nil {
		return nil, err
	}
	body, err := files.ReadFile(ctx, rolloutDefinitionPath(name))
	if err != nil {
		return nil, fmt.Errorf("evaluation: load rollout %q: %w", name, err)
	}
	var rollout NamedRollout
	if err := json.Unmarshal(body, &rollout); err != nil {
		return nil, fmt.Errorf("evaluation: decode rollout %q: %w", name, err)
	}
	if rollout.Name != name {
		return nil, fmt.Errorf("evaluation: rollout name mismatch: %w", constants.ErrEvaluationFlagsInvalid)
	}
	if err := validateNamedRollout(&rollout); err != nil {
		return nil, err
	}
	return &rollout, nil
}

// SaveNamedRollout atomically replaces the record. Callers hold LockRolloutQueue
// across load/modify/save, and across an entire execution, including step writes.
func SaveNamedRollout(ctx context.Context, files fs.RuntimeFileService, rollout *NamedRollout) error {
	if files == nil || rollout == nil {
		return fmt.Errorf("evaluation: save named rollout: %w", constants.ErrMissingRequiredField)
	}
	if err := validateNamedRollout(rollout); err != nil {
		return err
	}
	body, err := json.MarshalIndent(rollout, "", "  ")
	if err != nil {
		return fmt.Errorf("evaluation: encode rollout: %w", err)
	}
	if err := files.WriteFile(ctx, rolloutDefinitionPath(rollout.Name), body, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("evaluation: save rollout %q: %w", rollout.Name, err)
	}
	return nil
}

// DeleteNamedRollout removes the definition and execution references. Campaign
// evidence is stored separately and retained. Callers hold LockRolloutQueue.
func DeleteNamedRollout(ctx context.Context, files fs.RuntimeFileService, name string) error {
	if _, err := LoadNamedRollout(ctx, files, name); err != nil {
		return err
	}
	if err := files.Remove(ctx, rolloutDefinitionPath(name)); err != nil {
		return fmt.Errorf("evaluation: delete rollout %q: %w", name, err)
	}
	return nil
}

func ListNamedRollouts(ctx context.Context, files fs.RuntimeFileService) ([]RolloutDefinition, error) {
	if files == nil {
		return nil, fmt.Errorf("evaluation: list named rollouts: %w", constants.ErrMissingRequiredField)
	}
	entries, err := files.ReadDir(ctx, rolloutDefinitionsDir())
	if errors.Is(err, constants.ErrNotFound) {
		return []RolloutDefinition{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("evaluation: list named rollouts: %w", err)
	}
	definitions := make([]RolloutDefinition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), constants.FileExtJSON) {
			continue
		}
		rollout, err := LoadNamedRollout(ctx, files, strings.TrimSuffix(entry.Name(), constants.FileExtJSON))
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, rollout.RolloutDefinition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions, nil
}
