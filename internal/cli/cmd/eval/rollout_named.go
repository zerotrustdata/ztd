// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func rolloutDefineCmd(deps nativeEvalDeps, update bool) *cobra.Command {
	verb := "create"
	short := "Create a named rollout from existing campaign IDs in order"
	if update {
		verb = "update"
		short = "Replace the campaign list of a named rollout"
	}
	var continueOnFailure bool
	cmd := &cobra.Command{
		Use:   verb + " <name> <campaign-id>...",
		Short: short,
		Long: short + `.

The first argument is the rollout name. Every following argument is an existing
campaign ID, in the order it should run. Find IDs with: ztd eval campaigns list

Create requires a new name. Update replaces the saved list and keeps execution
history. Repeating a campaign ID runs that campaign again at that position.
--continue-on-failure allows the next campaign to run after a failure.

Examples (campaign-1 and campaign-2 are placeholders for your campaign IDs):
  ztd eval rollout ` + verb + ` my-rollout campaign-1 campaign-2`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, files, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			held, err := evaluation.LockRolloutQueue(cmd.Context(), files)
			if err != nil {
				return err
			}
			defer func() { _ = held.Close() }()
			rollout, err := evaluation.LoadNamedRollout(cmd.Context(), files, args[0])
			if update {
				if err != nil {
					return err
				}
			} else {
				if err == nil {
					return fmt.Errorf("evaluation: rollout %q exists; use rollout update: %w", args[0], constants.ErrAlreadyExists)
				}
				if !errors.Is(err, constants.ErrNotFound) {
					return err
				}
				rollout = &evaluation.NamedRollout{RolloutDefinition: evaluation.RolloutDefinition{Name: args[0]}, Executions: []evaluation.RolloutExecution{}}
			}
			if err := validateRolloutCampaigns(cmd, deps, args[1:]); err != nil {
				return err
			}
			rollout.Campaigns = append([]string{}, args[1:]...)
			if !update || cmd.Flags().Changed("continue-on-failure") {
				rollout.ContinueOnFailure = continueOnFailure
			}
			if err := evaluation.SaveNamedRollout(cmd.Context(), files, rollout); err != nil {
				return err
			}
			return writeNamedRollout(cmd, rollout)
		},
	}
	cmd.Flags().BoolVar(&continueOnFailure, "continue-on-failure", false, "Continue with later campaigns after a failure")
	return cmd
}

func rolloutShowCmd(deps nativeEvalDeps) *cobra.Command {
	return &cobra.Command{
		Use: "show <name>", Short: "Show a rollout definition and its execution history", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, files, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			rollout, err := evaluation.LoadNamedRollout(cmd.Context(), files, args[0])
			if err != nil {
				return err
			}
			return writeNamedRollout(cmd, rollout)
		},
	}
}

type rolloutDeleteJSON struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

func rolloutDeleteCmd(deps nativeEvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a named rollout",
		Long:  "Delete the saved rollout and its execution history. Campaigns and their runs are retained.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, files, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			held, err := evaluation.LockRolloutQueue(cmd.Context(), files)
			if err != nil {
				return err
			}
			defer func() { _ = held.Close() }()
			if err := evaluation.DeleteNamedRollout(cmd.Context(), files, args[0]); err != nil {
				return err
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), rolloutDeleteJSON{Name: args[0], Deleted: true})
			}
			cmd.Printf("Deleted rollout %s\n", args[0])
			return nil
		},
	}
}

func writeNamedRollout(cmd *cobra.Command, rollout *evaluation.NamedRollout) error {
	if output.JSONEnabled(cmd) {
		return output.WriteJSON(cmd.OutOrStdout(), rollout)
	}
	cmd.Printf("Rollout: %s\nContinue on failure: %t\n", rollout.Name, rollout.ContinueOnFailure)
	for i, id := range rollout.Campaigns {
		cmd.Printf("  %d. %s\n", i+1, id)
	}
	for _, execution := range rollout.Executions {
		cmd.Printf("Execution: %s (%s)\n", execution.ID, execution.StartedAt.Format("2006-01-02T15:04:05Z07:00"))
		for i, step := range execution.Steps {
			cmd.Printf("  %d. %s: %s\n", i+1, step.CampaignID, step.Status)
			for _, attempt := range step.Attempts {
				cmd.Printf("     run=%s failure=%s\n", attempt.RunID, attempt.FailureClass)
			}
		}
	}
	return nil
}

// Use the same local plan validation as runs start, including frozen formation
// bindings and archived-campaign rejection. Dry planning needs no live stack.
func validateRolloutCampaigns(cmd *cobra.Command, deps nativeEvalDeps, ids []string) error {
	seen := make(map[string]bool, len(ids))
	sub := *cmd
	sub.SetOut(io.Discard)
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := runStartFlow(&sub, deps, runStartFlowOptions{CampaignID: id, DryRun: true}); err != nil {
			return fmt.Errorf("evaluation: rollout campaign %q: %w", id, err)
		}
	}
	return nil
}

type namedRolloutOptions struct {
	Resume            bool
	DryRun            bool
	Until             int
	Repeat            int
	ContinueOnFailure *bool
}

// namedRolloutRunner is the governed campaign execution boundary. A reserved
// run ID is persisted before calling it, allowing a stopped step to resume the
// same run without losing the reference if the process exits unexpectedly.
type namedRolloutRunner func(cmd *cobra.Command, campaignID, runID string, resume bool) error

func runNamedRollout(cmd *cobra.Command, deps nativeEvalDeps, files fs.RuntimeFileService, name string, opts namedRolloutOptions, run namedRolloutRunner) error {
	if opts.Until < 0 || opts.Repeat < 1 {
		return fmt.Errorf("evaluation: rollout: --until must be nonnegative and --repeat positive: %w", constants.ErrEvaluationFlagsInvalid)
	}
	if !opts.DryRun {
		held, err := evaluation.LockRolloutQueue(cmd.Context(), files)
		if err != nil {
			return err
		}
		defer func() { _ = held.Close() }()
	}
	rollout, err := evaluation.LoadNamedRollout(cmd.Context(), files, name)
	if err != nil {
		return err
	}
	var execution *evaluation.RolloutExecution
	if opts.Resume {
		if len(rollout.Executions) == 0 {
			return fmt.Errorf("evaluation: rollout %q has no execution to resume; use rollout run: %w", name, constants.ErrEvaluationSelectionEmpty)
		}
		execution = &rollout.Executions[len(rollout.Executions)-1]
	} else {
		next := evaluation.RolloutExecution{StartedAt: deps.now().UTC(), ContinueOnFailure: rollout.ContinueOnFailure}
		for range opts.Repeat {
			for _, id := range rollout.Campaigns {
				next.Steps = append(next.Steps, evaluation.RolloutStep{CampaignID: id, Status: evaluation.RolloutStepPending})
			}
		}
		rollout.Executions = append(rollout.Executions, next)
		execution = &rollout.Executions[len(rollout.Executions)-1]
	}
	if opts.ContinueOnFailure != nil {
		execution.ContinueOnFailure = *opts.ContinueOnFailure
	}
	indices := make([]int, 0, len(execution.Steps))
	ids := make([]string, 0, len(execution.Steps))
	for i, step := range execution.Steps {
		if step.Status == evaluation.RolloutStepSucceeded {
			continue
		}
		indices = append(indices, i)
		ids = append(ids, step.CampaignID)
	}
	// Validate the entire remaining sequence before applying the invocation's
	// limit, so a missing or archived later campaign cannot start a partial batch.
	if err := validateRolloutCampaigns(cmd, deps, ids); err != nil {
		return err
	}
	if opts.Until > 0 && len(indices) > opts.Until {
		indices = indices[:opts.Until]
	}
	if opts.DryRun {
		plan := namedRolloutPlan{Name: name, Resume: opts.Resume, ContinueOnFailure: execution.ContinueOnFailure, Campaigns: []string{}}
		for _, i := range indices {
			plan.Campaigns = append(plan.Campaigns, execution.Steps[i].CampaignID)
		}
		if output.JSONEnabled(cmd) {
			return output.WriteJSON(cmd.OutOrStdout(), plan)
		}
		cmd.Printf("Rollout plan: %s (continue on failure: %t)\n", name, plan.ContinueOnFailure)
		for i, id := range plan.Campaigns {
			cmd.Printf("  %d. %s\n", i+1, id)
		}
		return nil
	}
	if !opts.Resume {
		execution.ID, err = deps.newID()
		if err != nil {
			return fmt.Errorf("evaluation: rollout execution ID: %w", err)
		}
	}
	if err := evaluation.SaveNamedRollout(cmd.Context(), files, rollout); err != nil {
		return err
	}
	sub := *cmd
	if output.JSONEnabled(cmd) {
		// Keep stdout as one JSON document, including when a campaign fails.
		sub.SetOut(cmd.ErrOrStderr())
	}
	var batchErr error
	for _, i := range indices {
		if err := cmd.Context().Err(); err != nil {
			batchErr = errors.Join(batchErr, err)
			break
		}
		step := &execution.Steps[i]
		resume := (step.Status == evaluation.RolloutStepRunning || step.Status == evaluation.RolloutStepStopped) && len(step.Attempts) > 0
		if !resume {
			id, err := deps.newID()
			if err != nil {
				return fmt.Errorf("evaluation: rollout run ID: %w", err)
			}
			step.Attempts = append(step.Attempts, evaluation.RolloutAttempt{RunID: step.CampaignID + "-" + id})
		}
		attempt := &step.Attempts[len(step.Attempts)-1]
		step.Status = evaluation.RolloutStepRunning
		attempt.FailureClass = ""
		if err := evaluation.SaveNamedRollout(cmd.Context(), files, rollout); err != nil {
			return err
		}
		sub.Printf("Rollout %s: campaign %d/%d: %s\n", name, i+1, len(execution.Steps), step.CampaignID)
		runErr := run(&sub, step.CampaignID, attempt.RunID, resume)
		step.Status = evaluation.RolloutStepSucceeded
		stopped := errors.Is(runErr, constants.ErrEvaluationRunStopped) || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)
		// A resume guard is not a new campaign outcome. Keep its existing run
		// reference so another resume cannot silently replace an ambiguous or
		// still-active run, or evade that run's frozen governance binding.
		resumeGuard := resume && (errors.Is(runErr, constants.ErrEvaluationRunLeaseHeld) ||
			errors.Is(runErr, constants.ErrEvaluationAssignmentUnresolved) ||
			errors.Is(runErr, constants.ErrEvaluationRunGovernanceChanged) ||
			errors.Is(runErr, constants.ErrGatewayPostureMismatch) ||
			errors.Is(runErr, constants.ErrEvaluationArchived))
		if runErr != nil {
			step.Status = evaluation.RolloutStepFailed
			attempt.FailureClass = evaluation.ClassifyExecutionFailure(runErr)
			if errors.Is(runErr, constants.ErrEvaluationRunStopped) {
				attempt.FailureClass = evaluation.ExecutionFailureCanceled
			}
			var canceled *rolloutCanceledCampaignError
			// Cancellation terminally stops assignments, so the batch ends but
			// this campaign needs a fresh attempt on the next rollout resume.
			if (stopped && !errors.As(runErr, &canceled)) || resumeGuard {
				step.Status = evaluation.RolloutStepStopped
			}
			batchErr = errors.Join(batchErr, fmt.Errorf("campaign %q: %w", step.CampaignID, runErr))
		}
		// A cancellation must not prevent checkpointing the stopped step.
		if err := evaluation.SaveNamedRollout(context.WithoutCancel(cmd.Context()), files, rollout); err != nil {
			return errors.Join(batchErr, err)
		}
		if stopped || resumeGuard || (runErr != nil && !execution.ContinueOnFailure) {
			break
		}
	}
	return errors.Join(batchErr, writeNamedRollout(cmd, rollout))
}

type namedRolloutPlan struct {
	Name              string   `json:"name"`
	Resume            bool     `json:"resume"`
	ContinueOnFailure bool     `json:"continue_on_failure"`
	Campaigns         []string `json:"campaigns"`
}

func productionNamedRolloutRunner(deps nativeEvalDeps, files fs.RuntimeFileService) namedRolloutRunner {
	return func(cmd *cobra.Command, campaignID, runID string, resume bool) error {
		store := evaluation.NewStore(files)
		if resume {
			run, err := store.LoadRun(cmd.Context(), runID)
			if err != nil && !errors.Is(err, constants.ErrNotFound) {
				return err
			}
			if err == nil {
				if run.GetCampaignBinding().GetCampaignId() != campaignID || run.GetProviderObservationPolicy() != evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT || run.GetModelProvenancePolicy() != evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT {
					return fmt.Errorf("evaluation: rollout run binding or witness policies differ: %w", constants.ErrEvaluationFlagsInvalid)
				}
				_, execErr := executeRun(cmd, deps, runExecuteOptions{RunID: runID, Publish: true, Daemon: true, ProviderFailureLimit: rolloutProviderFailureLimit})
				if execErr != nil && !errors.Is(execErr, constants.ErrEvaluationPublicationFailed) {
					return execErr
				}
				report, verifyErr := verifyRun(cmd, deps, store, runID, true, true, true, false)
				if verifyErr == nil && report.GetFailureCount() > 0 {
					verifyErr = constants.ErrEvalRunVerificationFailed
				}
				return errors.Join(execErr, verifyErr, requireNamedRolloutCoverage(cmd, deps, store, runID), requireRunServed(cmd.Context(), files, "campaign", runID))
			}
		}
		_, err := runStartFlow(cmd, deps, runStartFlowOptions{
			CampaignID: campaignID, RunID: runID, Publish: true, Daemon: true, Verify: true,
			RequireObservation: true, RequireProvenance: true, ProviderFailureLimit: rolloutProviderFailureLimit,
		})
		if err != nil {
			return err
		}
		return errors.Join(requireNamedRolloutCoverage(cmd, deps, store, runID), requireRunServed(cmd.Context(), files, "campaign", runID))
	}
}

// A valid evidence report alone does not prove the whole campaign ran. Account
// for the frozen matrix as well, including after resuming a partially prepared
// run, and never accept canceled assignments as completed campaign coverage.
func requireNamedRolloutCoverage(cmd *cobra.Command, deps nativeEvalDeps, store *evaluation.Store, runID string) error {
	run, err := store.LoadRun(cmd.Context(), runID)
	if err != nil {
		return err
	}
	catalog, err := store.LoadScenarioCatalog(cmd.Context(), run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return err
	}
	report, err := evaluation.NewCampaignPopulationAccountant(deps.now).AccountRun(cmd.Context(), store, runID, catalog)
	if err != nil {
		return err
	}
	if report.StoppedCount > 0 {
		return &rolloutCanceledCampaignError{RunID: runID}
	}
	if !report.Complete {
		return fmt.Errorf("evaluation: rollout run %s has incomplete campaign coverage: %w", runID, constants.ErrEvalRunVerificationFailed)
	}
	return nil
}

type rolloutCanceledCampaignError struct{ RunID string }

func (e *rolloutCanceledCampaignError) Error() string {
	return fmt.Sprintf("evaluation: rollout run %s contains canceled assignments; resume the rollout to retry this campaign: %v", e.RunID, constants.ErrEvaluationRunStopped)
}

func (e *rolloutCanceledCampaignError) Unwrap() error { return constants.ErrEvaluationRunStopped }
