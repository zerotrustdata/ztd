// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// runStartFlowOptions describes one start of a campaign.
type runStartFlowOptions struct {
	CampaignID string
	// RunID names the run. When empty the run is named after the campaign and
	// the start time.
	RunID              string
	DryRun             bool
	PrepareOnly        bool
	Publish            bool
	Daemon             bool
	Verify             bool
	RequireObservation bool
	RequireProvenance  bool
	NoAutoBind         bool
	NoBackup           bool
	JSONOutput         bool
}

type runStartFlowResult struct {
	CampaignID string
	RunID      string
	Lane       string
	Cells      uint64
	Models     []string
	// Sessions is populated only when the flow resolved operator sessions; a
	// dry run does not.
	Sessions         operatorSessions
	SessionsResolved bool
	Prepared         bool
	Executed         int
	Report           *evalv1.EvaluationVerificationReport
}

type runStartPlanJSON struct {
	CampaignID          string   `json:"campaign_id"`
	RunID               string   `json:"run_id"`
	Lane                string   `json:"lane"`
	ModelTags           []string `json:"model_tags"`
	ModelRegistryDigest string   `json:"model_registry_digest"`
	CellCount           uint64   `json:"cell_count"`
	SessionsResolved    bool     `json:"sessions_resolved"`
	InferenceSession    string   `json:"inference_session,omitempty"`
	DataSession         string   `json:"data_session,omitempty"`
}

type runStartResultJSON struct {
	CampaignID     string   `json:"campaign_id"`
	RunID          string   `json:"run_id"`
	Executed       int      `json:"executed"`
	Verified       bool     `json:"verified"`
	VerifyStatus   string   `json:"verify_status"`
	FailureCount   int      `json:"failure_count"`
	FailureReasons []string `json:"failure_reasons"`
	Prepared       bool     `json:"prepared"`
}

func runStartPlan(result *runStartFlowResult, digest string) runStartPlanJSON {
	return runStartPlanJSON{
		CampaignID:          result.CampaignID,
		RunID:               result.RunID,
		Lane:                result.Lane,
		ModelTags:           result.Models,
		ModelRegistryDigest: digest,
		CellCount:           result.Cells,
		SessionsResolved:    result.SessionsResolved,
		InferenceSession:    result.Sessions.InferenceSessionID,
		DataSession:         result.Sessions.DataSessionID,
	}
}

func writeRunStartPlan(cmd *cobra.Command, plan runStartPlanJSON, jsonOutput bool) error {
	if jsonOutput {
		return output.WriteJSON(cmd.OutOrStdout(), plan)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Run start plan\nCampaign: %s\nRun: %s\nLane: %s\nModels: %v\nCells: %d\nModel registry digest: %s\nSessions resolved: %t\n",
		plan.CampaignID, plan.RunID, plan.Lane, plan.ModelTags, plan.CellCount, plan.ModelRegistryDigest, plan.SessionsResolved)
	if err != nil || !plan.SessionsResolved {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Inference session: %s\nData session: %s\n", plan.InferenceSession, plan.DataSession)
	return err
}

// runStartFlow starts one run of an existing campaign: it binds operator
// sessions, persists the run and its assignment matrix, and unless prepared
// only, executes and optionally verifies it.
func runStartFlow(cmd *cobra.Command, deps nativeEvalDeps, opts runStartFlowOptions) (*runStartFlowResult, error) {
	cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
	if err != nil {
		return nil, err
	}
	store, archived, err := evaluation.LocateCampaign(cmd.Context(), fileSvc, opts.CampaignID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: runs start: %w", err)
	}
	if archived {
		return nil, fmt.Errorf("evaluation: runs start: campaign %q: %w", opts.CampaignID, constants.ErrEvaluationArchived)
	}
	spec, err := store.LoadCampaignSpec(cmd.Context(), opts.CampaignID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: runs start: %w", err)
	}
	laneName, stacks, err := campaignLane(cmd.Context(), store, opts.CampaignID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: runs start: %w", err)
	}
	runLane := evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE
	if laneName == campaignLaneSystem {
		runLane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	}
	catalog, err := store.LoadScenarioCatalog(cmd.Context(), opts.CampaignID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: runs start: %w", err)
	}
	var sessions operatorSessions
	if !opts.DryRun {
		sessions, err = resolveOperatorSessions(cmd, deps, constants.OperatorCapabilityInference, constants.OperatorCapabilityData)
		if err != nil {
			return nil, fmt.Errorf("evaluation: runs start: %w", err)
		}
	}
	runID := opts.RunID
	if runID == "" {
		runID = fmt.Sprintf("%s-%d", opts.CampaignID, deps.now().UTC().Unix())
	}
	result := &runStartFlowResult{
		CampaignID: opts.CampaignID,
		RunID:      runID,
		Lane:       laneName,
		Cells:      campaignCells(catalog, spec, laneName, stacks),
		Sessions:   sessions,

		SessionsResolved: !opts.DryRun,
	}
	for _, variant := range spec.GetModelRegistry() {
		result.Models = append(result.Models, variant.GetServedModelTag())
	}
	plan := runStartPlan(result, spec.GetModelRegistryDigest())
	if opts.DryRun {
		return result, writeRunStartPlan(cmd, plan, opts.JSONOutput)
	}
	if !opts.JSONOutput {
		if err := writeRunStartPlan(cmd, plan, false); err != nil {
			return nil, err
		}
	}
	authContext, err := deps.authLoader(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("evaluation: runs start: load CLI identity: %w", err)
	}
	controller := evaluation.NewCampaignController(store, nil, deps.now, adaptNewID(deps.newID))
	if _, err := controller.StartRun(cmd.Context(), evaluation.RunStartRequest{
		CampaignID:                 opts.CampaignID,
		RunID:                      runID,
		InferenceOperatorSessionID: sessions.InferenceSessionID,
		DataOperatorSessionID:      sessions.DataSessionID,
		Deployment:                 nativeEvalDeployment(cfg, authContext, runID, ""),
		Lane:                       runLane,
		PlatformRelease:            platformIdentity(cmd).Release,
	}); err != nil {
		return nil, fmt.Errorf("evaluation: runs start: %w", err)
	}
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Started run %s of campaign %s\n", runID, opts.CampaignID)
	}
	if opts.Publish {
		publication, err := NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
		if err != nil {
			return nil, fmt.Errorf("evaluation: runs start: %w", err)
		}
		controller = controller.WithPublication(publication)
	}
	assignments, err := controller.ScheduleRun(cmd.Context(), runID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: runs start: schedule: %w", err)
	}
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Scheduled %d assignments for run %s\n", assignments, runID)
	}
	if opts.PrepareOnly {
		result.Prepared = true
		return result, nil
	}
	if !opts.NoBackup {
		// Deferred so the snapshot also captures verification output and the
		// evidence of a run that failed or was cancelled.
		defer autoBackupEval(cmd, deps, opts.JSONOutput)
	}
	executed, err := executeRun(cmd, deps, runExecuteOptions{
		RunID:                    runID,
		Publish:                  opts.Publish,
		Daemon:                   opts.Daemon,
		EnforceProviderResidency: opts.RequireObservation,
		NoAutoBind:               opts.NoAutoBind,
		JSONOutput:               opts.JSONOutput,
	})
	if err != nil {
		return result, err
	}
	result.Executed = executed
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Executed %d assignment(s) for run %s\n", executed, runID)
	}
	if !opts.Verify {
		return result, nil
	}
	report, err := verifyRun(cmd, deps, store, runID, opts.RequireObservation, opts.RequireProvenance, opts.Publish, opts.JSONOutput)
	if err != nil {
		return result, err
	}
	result.Report = report
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Run %s verification: %s (%d failure(s))\n", runID, report.GetStatus().String(), report.GetFailureCount())
		for _, reason := range report.GetFailureReasons() {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", reason)
		}
	}
	if report.GetFailureCount() > 0 {
		return result, constants.ErrEvalRunVerificationFailed
	}
	return result, nil
}

func runsStartCmd(deps nativeEvalDeps) *cobra.Command {
	var opts runStartFlowOptions
	var requireWitness bool
	cmd := &cobra.Command{
		Use:   "start <campaign>",
		Short: "Start a run of a campaign: bind sessions, schedule, execute, and optionally verify",
		Long: `Start one run of an existing campaign. The run binds the operator sessions,
persists its full assignment matrix, and then executes it. Progress is
resumable (ztd eval runs resume) and cancellable (ztd eval runs cancel).

Operator sessions are resolved from the active Inference Operator and the stack's
data-operator. The data-operator is bound to the CLI session unless --no-auto-bind
is set.

Examples:
  ztd eval runs start eval-qwen
  ztd eval runs start eval-qwen --require-witness
  ztd eval runs start eval-qwen --prepare-only
  ztd eval runs start eval-qwen --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.CampaignID = args[0]
			opts.JSONOutput = output.JSONEnabled(cmd)
			if requireWitness {
				opts.RequireObservation = true
				opts.RequireProvenance = true
				opts.Verify = true
			}
			result, err := runStartFlow(cmd, deps, opts)
			if err != nil {
				return err
			}
			if opts.DryRun {
				return nil
			}
			if opts.JSONOutput {
				return output.WriteJSON(cmd.OutOrStdout(), runStartResultJSON{
					CampaignID:     result.CampaignID,
					RunID:          result.RunID,
					Executed:       result.Executed,
					Verified:       result.Report != nil,
					VerifyStatus:   verificationStatusString(result.Report),
					FailureCount:   verificationFailureCount(result.Report),
					FailureReasons: verificationFailureReasons(result.Report),
					Prepared:       result.Prepared,
				})
			}
			if result.Prepared {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Prepared run %s. Execute it with:\n  ./ztd eval runs resume %s --publish --daemon\n", result.RunID, result.RunID)
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print the resolved plan without starting")
	cmd.Flags().BoolVar(&opts.PrepareOnly, "prepare-only", false, "Persist the run and its assignments, then stop before executing")
	cmd.Flags().BoolVar(&opts.Publish, "publish", true, "Publish lifecycle projections to the public mirror")
	cmd.Flags().BoolVar(&opts.Daemon, "daemon", true, "Execute continuously until the assignment matrix is exhausted")
	cmd.Flags().BoolVar(&opts.Verify, "verify", false, "Verify the run after it completes")
	cmd.Flags().BoolVar(&requireWitness, "require-witness", false, "Require provider-boundary observation and model-provenance witness evidence (implies --verify)")
	cmd.Flags().BoolVar(&opts.RequireProvenance, "require-provenance", false, "Fail verification when model provenance attestation windows are missing")
	cmd.Flags().BoolVar(&opts.RequireObservation, "require-observation", false, "Fail verification when provider-boundary observation windows are missing")
	cmd.Flags().BoolVar(&opts.NoAutoBind, "no-auto-bind", false, "Do not bind the data-operator to the CLI session when it is not bound")
	cmd.Flags().BoolVar(&opts.NoBackup, "no-backup", false, noBackupFlagUsage)
	return cmd
}

const noBackupFlagUsage = "Do not back up evaluation evidence to eval/backups when the run finishes"

func verificationStatusString(report *evalv1.EvaluationVerificationReport) string {
	if report == nil {
		return ""
	}
	return report.GetStatus().String()
}

func verificationFailureCount(report *evalv1.EvaluationVerificationReport) int {
	if report == nil {
		return 0
	}
	return int(report.GetFailureCount())
}

func verificationFailureReasons(report *evalv1.EvaluationVerificationReport) []string {
	if report == nil {
		return nil
	}
	return report.GetFailureReasons()
}

type runResumeResult struct {
	AssignmentID string `json:"assignment_id"`
	Status       string `json:"status"`
	ResultDigest string `json:"result_digest"`
}

type runResumeJSON struct {
	RunID     string            `json:"run_id"`
	Executed  int               `json:"executed"`
	Remaining int64             `json:"remaining"`
	Results   []runResumeResult `json:"results"`
}

func runsResumeCmd(deps nativeEvalDeps) *cobra.Command {
	var limit uint32
	var noAutoBind, noBackup, publish, daemon bool
	cmd := &cobra.Command{
		Use:   "resume <run>",
		Short: "Execute the remaining queued assignments of a run",
		Long: `Execute queued assignments of an existing run through the direct formation
runner, which dispatches each role to the Inference Operator. By default one
assignment runs; --limit raises that and --daemon runs until the matrix is
exhausted. An archived run cannot be resumed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			jsonOutput := output.JSONEnabled(cmd)
			results := make([]runResumeResult, 0)
			if !noBackup {
				defer autoBackupEval(cmd, deps, jsonOutput)
			}
			executed, err := executeRun(cmd, deps, runExecuteOptions{
				RunID:      runID,
				Publish:    publish,
				Daemon:     daemon,
				Limit:      limit,
				NoAutoBind: noAutoBind,
				JSONOutput: jsonOutput,
				ResultOutput: func(result *evalv1.EvaluationAssignmentResult) {
					if jsonOutput && !daemon {
						results = append(results, runResumeResult{
							AssignmentID: result.GetAssignmentId(),
							Status:       result.GetLifecycleStatus().String(),
							ResultDigest: result.GetResultDigest(),
						})
					}
				},
			})
			if err != nil {
				return err
			}
			if !jsonOutput {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Executed %d assignment(s) for run %s\n", executed, runID)
				return err
			}
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			summary, err := evaluation.NewCampaignController(evaluation.NewStore(fileSvc), nil, deps.now, adaptNewID(deps.newID)).RunSummary(cmd.Context(), runID)
			if err != nil {
				return fmt.Errorf("evaluation: runs resume: %w", err)
			}
			remaining := int64(summary.ExpectedAssignment) - int64(summary.TerminalCount)
			if remaining < 0 {
				remaining = 0
			}
			return output.WriteJSON(cmd.OutOrStdout(), runResumeJSON{RunID: runID, Executed: executed, Remaining: remaining, Results: results})
		},
	}
	cmd.Flags().Uint32Var(&limit, "limit", 1, "Maximum queued assignments to execute in this invocation")
	cmd.Flags().BoolVar(&daemon, "daemon", false, "Run continuously until the queued matrix is exhausted")
	cmd.Flags().BoolVar(&publish, "publish", false, "Publish assignment lifecycle and terminal result projections to the public mirror")
	cmd.Flags().BoolVar(&noAutoBind, "no-auto-bind", false, "Do not bind the data-operator to the CLI session when it is not bound")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, noBackupFlagUsage)
	return cmd
}

type runCancelJSON struct {
	RunID        string `json:"run_id"`
	Stopped      int    `json:"stopped"`
	ClearedStale bool   `json:"cleared_stale_lease"`
	WasRunning   bool   `json:"was_running"`
}

func runsCancelCmd(deps nativeEvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run>",
		Short: "Cancel a run: stop after the current assignment and stop the rest",
		Long: `Cancel a run. A run whose process is alive is asked to stop; cancel waits for
its current assignment to reach a terminal record and then stops every remaining
assignment. A lease whose process is gone is cleared and reported.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			store, archived, err := evaluation.LocateRun(cmd.Context(), fileSvc, runID)
			if err != nil {
				return fmt.Errorf("evaluation: runs cancel: %w", err)
			}
			if archived {
				return fmt.Errorf("evaluation: runs cancel: run %q: %w", runID, constants.ErrEvaluationArchived)
			}
			control, err := deps.runControl.process(fileSvc)
			if err != nil {
				return err
			}
			result := runCancelJSON{RunID: runID}
			jsonOutput := output.JSONEnabled(cmd)
			out := cmd.OutOrStdout()
			live := leaseLiveness(control)
			lease, err := store.LoadRunLease(cmd.Context(), runID)
			switch {
			case err == nil && live(*lease):
				result.WasRunning = true
				if err := awaitRunStop(cmd, deps, store, control, live, lease, jsonOutput); err != nil {
					return fmt.Errorf("evaluation: runs cancel: %w", err)
				}
			case err == nil:
				result.ClearedStale = true
				if err := store.ClearRunLease(cmd.Context(), runID); err != nil {
					return fmt.Errorf("evaluation: runs cancel: %w", err)
				}
				if !jsonOutput {
					_, _ = fmt.Fprintf(out, "Cleared stale lease held by process %d on %s\n", lease.PID, lease.Host)
				}
			case !isMissingRecord(err):
				return fmt.Errorf("evaluation: runs cancel: %w", err)
			}
			controller := evaluation.NewCampaignController(store, nil, deps.now, adaptNewID(deps.newID))
			if result.Stopped, err = controller.CancelRun(cmd.Context(), runID); err != nil {
				return fmt.Errorf("evaluation: runs cancel: %w", err)
			}
			if jsonOutput {
				return output.WriteJSON(out, result)
			}
			_, err = fmt.Fprintf(out, "Cancelled run %s: stopped %d assignment(s)\n", runID, result.Stopped)
			return err
		},
	}
}

// awaitRunStop asks the process holding a run's lease to stop and waits until
// it has released the lease, which happens once its current assignment reaches
// a terminal record.
func awaitRunStop(cmd *cobra.Command, deps nativeEvalDeps, store *evaluation.Store, control processControl, live evaluation.LeaseLiveness, lease *evaluation.RunLease, jsonOutput bool) error {
	if _, err := store.RequestRunCancel(cmd.Context(), lease.RunID, deps.now()); err != nil {
		return err
	}
	// Delivery is best effort: the cancel request in the lease reaches a process
	// that cannot be signalled.
	_ = control.Interrupt(lease.PID)
	if !jsonOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Waiting for process %d to finish its current assignment\n", lease.PID)
	}
	ticker := time.NewTicker(deps.runControl.interval())
	defer ticker.Stop()
	for {
		current, err := store.LoadRunLease(cmd.Context(), lease.RunID)
		if err != nil {
			if isMissingRecord(err) {
				return nil
			}
			return err
		}
		if !live(*current) {
			return store.ClearRunLease(cmd.Context(), lease.RunID)
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-ticker.C:
		}
	}
}
