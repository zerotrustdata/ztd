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
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
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
	JSONOutput         bool
	// ProviderFailureLimit bounds consecutive generation failures in rollout
	// runs. Zero keeps explicit campaign runs exhaustive.
	ProviderFailureLimit int
	AssignmentTimeout    time.Duration
}

type runStartFlowResult struct {
	CampaignID string
	RunID      string
	Lane       string
	Cells      uint64
	Models     []string
	// Topology is populated only when the flow resolved operator sessions; a
	// dry run does not.
	Topology         evaluation.CampaignOperatorTopology
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
		InferenceSession:    result.Topology.Inference.OperatorSessionID,
		DataSession:         result.Topology.Data.OperatorSessionID,
	}
}

func writeRunStartPlan(cmd *cobra.Command, plan runStartPlanJSON, jsonOutput bool) error {
	if jsonOutput {
		return output.WriteJSON(cmd.OutOrStdout(), plan)
	}
	sessionsResolvedLabel := "skipped (dry run)"
	if plan.SessionsResolved {
		sessionsResolvedLabel = "true"
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Run start plan\nCampaign: %s\nRun: %s\nLane: %s\nModels: %v\nCells: %d\nModel registry digest: %s\nSessions resolved: %s\n",
		plan.CampaignID, plan.RunID, plan.Lane, plan.ModelTags, plan.CellCount, plan.ModelRegistryDigest, sessionsResolvedLabel)
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
	if opts.AssignmentTimeout < 0 {
		return nil, fmt.Errorf("evaluation: assignment timeout must be nonnegative: %w", constants.ErrEvaluationFlagsInvalid)
	}
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
	var stackSet *evaluation.HeterogeneousStackSet
	if laneName == campaignLaneSystem {
		runLane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
		stackSet, err = store.LoadHeterogeneousStackSet(cmd.Context(), opts.CampaignID)
		if err != nil {
			return nil, fmt.Errorf("evaluation: runs start: frozen formations: %w", err)
		}
		if err := evaluation.ValidateFormationStackSetBinding(stackSet, opts.CampaignID, spec.GetModelRegistry()); err != nil {
			return nil, fmt.Errorf("evaluation: runs start: frozen formations: %w", err)
		}
	}
	catalog, err := store.LoadScenarioCatalog(cmd.Context(), opts.CampaignID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: runs start: %w", err)
	}
	var topology evaluation.CampaignOperatorTopology
	var governance evaluation.RunGovernance
	if !opts.DryRun {
		investigation := spec.GetInvestigation()
		if err := evaluation.ValidateCampaignInvestigation(investigation); err != nil {
			return nil, fmt.Errorf("evaluation: runs start: campaign investigation: %w", err)
		}
		gatewayDeps := deps.gatewayDeps()
		gatewayCfg, _, gatewayAuth, err := gatewayEvalEnvironment(cmd, gatewayDeps)
		if err != nil {
			return nil, fmt.Errorf("evaluation: runs start: %w", err)
		}
		if governance, err = evalPreflightGatewayGovernance(cmd, gatewayDeps, gatewayCfg, gatewayAuth, spec, nil); err != nil {
			return nil, fmt.Errorf("evaluation: runs start: %w", err)
		}
		topology, err = resolveOperatorTopologyForDataOperator(cmd, deps, investigation.GetDataOperatorId())
		if err != nil {
			return nil, fmt.Errorf("evaluation: runs start: %w", err)
		}
	}
	cellPosture := governance.Posture
	if cellPosture == evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED {
		// Dry runs do not query live Gateway governance; retain the legacy
		// posture-neutral (doctrine-sized) planning count.
		cellPosture = evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE
	}
	runID := opts.RunID
	if runID == "" {
		runID = fmt.Sprintf("%s-%d", opts.CampaignID, deps.now().UTC().Unix())
	}
	result := &runStartFlowResult{
		CampaignID: opts.CampaignID,
		RunID:      runID,
		Lane:       laneName,
		Cells:      campaignCells(catalog, spec, laneName, stacks, cellPosture),
		Topology:   topology,

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
	started, err := controller.StartRun(cmd.Context(), evaluation.RunStartRequest{
		CampaignID:                 opts.CampaignID,
		RunID:                      runID,
		InferenceOperatorSessionID: topology.Inference.OperatorSessionID,
		DataOperatorSessionID:      topology.Data.OperatorSessionID,
		Deployment:                 nativeEvalDeployment(cfg, authContext, runID, ""),
		Lane:                       runLane,
		PlatformRelease:            platformIdentity(cmd).Release,
		ProviderObservationPolicy:  witnessPolicy(opts.RequireObservation),
		ModelProvenancePolicy:      witnessPolicy(opts.RequireProvenance),
		Governance:                 governance,
	})
	if err != nil {
		return nil, fmt.Errorf("evaluation: runs start: %w", err)
	}
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Started run %s of campaign %s\n", runID, opts.CampaignID)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Governance: %s\n", evaluation.DescribeRunGovernance(started))
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
	executed, err := executeRun(cmd, deps, runExecuteOptions{
		RunID:                runID,
		Publish:              opts.Publish,
		Daemon:               opts.Daemon,
		NoAutoBind:           opts.NoAutoBind,
		JSONOutput:           opts.JSONOutput,
		ProviderFailureLimit: opts.ProviderFailureLimit,
		AssignmentTimeout:    opts.AssignmentTimeout,
	})
	// A publication failure leaves every result persisted, so the run still
	// verifies; the failure is returned once verification has run.
	result.Executed = executed
	publicationErr := err
	if err != nil && (errors.Is(err, constants.ErrInferenceGenerateFailed) || !errors.Is(err, constants.ErrEvaluationPublicationFailed)) {
		return result, err
	}
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Executed %d assignment(s) for run %s\n", executed, runID)
	}
	if !opts.Verify {
		return result, publicationErr
	}
	report, err := verifyRun(cmd, deps, store, runID, opts.RequireObservation, opts.RequireProvenance, opts.Publish, opts.JSONOutput)
	if err != nil {
		return result, errors.Join(err, publicationErr)
	}
	result.Report = report
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Run %s verification: %s (%d failure(s))\n", runID, report.GetStatus().String(), report.GetFailureCount())
		for _, reason := range report.GetFailureReasons() {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", reason)
		}
	}
	if report.GetFailureCount() > 0 {
		return result, errors.Join(constants.ErrEvalRunVerificationFailed, publicationErr)
	}
	return result, publicationErr
}

func witnessPolicy(strict bool) evalv1.EvaluationWitnessPolicy {
	if strict {
		return evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT
	}
	return evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM
}

func runsStartCmd(deps nativeEvalDeps) *cobra.Command {
	var opts runStartFlowOptions
	var requireWitness bool
	cmd := &cobra.Command{
		Use:   "start <campaign>",
		Short: "Start a run of a campaign: bind sessions, schedule, execute, and verify",
		Long: `Start one run of an existing campaign. The run binds the operator sessions,
persists its full assignment matrix, and then executes it. Progress is
resumable (ztd eval runs resume) and cancellable (ztd eval runs cancel).
Verification runs after execution by default; use --verify=false to skip it.

Operator sessions are resolved from the active Inference Operator and the
campaign's Data Operator. A persistent campaign resolves its frozen Data
Operator ID to the current active session on every run. The selected Data
Operator session is bound to the CLI session unless --no-auto-bind is set.

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
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Prepared run %s. Execute it with:\n./ztd eval runs resume %s --publish --daemon\n", result.RunID, result.RunID)
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print the resolved plan without starting")
	cmd.Flags().BoolVar(&opts.PrepareOnly, "prepare-only", false, "Persist the run and its assignments, then stop before executing")
	cmd.Flags().BoolVar(&opts.Publish, "publish", true, "Publish lifecycle projections to the public mirror")
	cmd.Flags().BoolVar(&opts.Daemon, "daemon", true, "Execute continuously until the assignment matrix is exhausted")
	cmd.Flags().BoolVar(&opts.Verify, "verify", true, "Verify the run after it completes")
	cmd.Flags().BoolVar(&requireWitness, "require-witness", false, "Require provider-boundary observation and model-provenance witness evidence (implies --verify)")
	cmd.Flags().BoolVar(&opts.RequireProvenance, "require-provenance", false, "Fail verification when model provenance attestation windows are missing")
	cmd.Flags().BoolVar(&opts.RequireObservation, "require-observation", false, "Fail verification when provider-boundary observation windows are missing")
	cmd.Flags().BoolVar(&opts.NoAutoBind, "no-auto-bind", false, "Do not bind the data-operator to the CLI session when it is not bound")
	cmd.Flags().DurationVar(&opts.AssignmentTimeout, "assignment-timeout", 0, "Optional per-assignment wall-clock limit (0 adds no assignment deadline)")
	return cmd
}

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
	RunID          string            `json:"run_id"`
	Executed       int               `json:"executed"`
	Remaining      int64             `json:"remaining"`
	Results        []runResumeResult `json:"results"`
	Verified       bool              `json:"verified"`
	VerifyStatus   string            `json:"verify_status"`
	FailureCount   int               `json:"failure_count"`
	FailureReasons []string          `json:"failure_reasons"`
}

func runsResumeCmd(deps nativeEvalDeps) *cobra.Command {
	var limit uint32
	var noAutoBind, publish, daemon, verify bool
	var assignmentTimeout time.Duration
	cmd := &cobra.Command{
		Use:     "resume <run>",
		Aliases: []string{"execute"},
		Short:   "Execute the remaining queued assignments of a run",
		Long: `Execute queued assignments of an existing run. A model-role assignment runs
as a tool-calling agent (each turn an Inference Operator dispatch, each tool
call a governed Data Operator action). By default one assignment runs; --limit raises
that and --daemon runs until the matrix is exhausted. A run left with an
unresolved running assignment cannot resume; cancel it and start a new run.
An archived run cannot be resumed.
Verification runs after execution by default; use --verify=false to skip it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			jsonOutput := output.JSONEnabled(cmd)
			results := make([]runResumeResult, 0)
			executed, err := executeRun(cmd, deps, runExecuteOptions{
				RunID:             runID,
				Publish:           publish,
				Daemon:            daemon,
				Limit:             limit,
				NoAutoBind:        noAutoBind,
				JSONOutput:        jsonOutput,
				AssignmentTimeout: assignmentTimeout,
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
			publicationErr := err
			if err != nil && !errors.Is(err, constants.ErrEvaluationPublicationFailed) {
				return err
			}
			var report *evalv1.EvaluationVerificationReport
			if verify {
				_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
				if err != nil {
					return errors.Join(err, publicationErr)
				}
				report, err = verifyRun(cmd, deps, evaluation.NewStore(fileSvc), runID, false, false, publish, jsonOutput)
				if err != nil {
					return errors.Join(err, publicationErr)
				}
			}
			var verificationErr error
			if report.GetFailureCount() > 0 {
				verificationErr = constants.ErrEvalRunVerificationFailed
			}
			if !jsonOutput {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Executed %d assignment(s) for run %s\n", executed, runID)
				if report != nil {
					_, reportErr := fmt.Fprintf(cmd.OutOrStdout(), "Run %s verification: %s (%d failure(s))\n", runID, report.GetStatus().String(), report.GetFailureCount())
					err = errors.Join(err, reportErr)
					for _, reason := range report.GetFailureReasons() {
						_, reasonErr := fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", reason)
						err = errors.Join(err, reasonErr)
					}
				}
				return errors.Join(err, verificationErr, publicationErr)
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
			return errors.Join(output.WriteJSON(cmd.OutOrStdout(), runResumeJSON{
				RunID: runID, Executed: executed, Remaining: remaining, Results: results,
				Verified: report != nil, VerifyStatus: verificationStatusString(report),
				FailureCount: verificationFailureCount(report), FailureReasons: verificationFailureReasons(report),
			}), verificationErr, publicationErr)
		},
	}
	cmd.Flags().Uint32Var(&limit, "limit", 1, "Maximum queued assignments to execute in this invocation")
	cmd.Flags().BoolVar(&daemon, "daemon", false, "Run continuously until the queued matrix is exhausted")
	cmd.Flags().BoolVar(&verify, "verify", true, "Verify the run after execution")
	cmd.Flags().BoolVar(&publish, "publish", false, "Publish assignment lifecycle and terminal result projections to the public mirror")
	cmd.Flags().BoolVar(&noAutoBind, "no-auto-bind", false, "Do not bind the data-operator to the CLI session when it is not bound")
	cmd.Flags().DurationVar(&assignmentTimeout, "assignment-timeout", 0, "Optional per-assignment wall-clock limit (0 adds no assignment deadline)")
	return cmd
}

type runCancelJSON struct {
	RunID        string `json:"run_id"`
	Stopped      int    `json:"stopped"`
	ClearedStale bool   `json:"cleared_stale_lease"`
	WasRunning   bool   `json:"was_running"`
	Published    int    `json:"published"`
}

func runsCancelCmd(deps nativeEvalDeps) *cobra.Command {
	var publish bool
	cmd := &cobra.Command{
		Use:   "cancel <run>",
		Short: "Cancel a run: stop after the current assignment and stop the rest",
		Long: `Cancel a run. A run whose process is alive is asked to stop; cancel waits for
its current assignment to reach a terminal record and then stops every remaining
assignment. A lease whose process is gone is cleared and reported. With --publish
the stopped assignments and the run's stopped summary are published to the
public mirror, so the explorer stops reporting the run as running.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
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
				cleared, err := awaitRunStop(cmd, deps, cfg, fileSvc, store, control, live, lease, jsonOutput)
				if err != nil {
					return fmt.Errorf("evaluation: runs cancel: %w", err)
				}
				// A holder that exited without releasing left its lease stale, and cancel cleared it.
				result.ClearedStale = cleared
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
			if result.Stopped > 0 {
				// Only cancel stops assignments, so only cancel reports the cancelled
				// status. A holder released before this point and projected the run
				// as waiting. A settled run stops nothing and posts nothing.
				authContext, err := deps.authLoader(fileSvc, cfg)
				if err != nil {
					return fmt.Errorf("evaluation: runs cancel: load CLI identity: %w", err)
				}
				appClient, err := inferenceEvalAppClient(cmd.Context(), cmd.ErrOrStderr(), deps, fileSvc, cfg, authContext)
				if err != nil {
					return fmt.Errorf("evaluation: runs cancel: %w", err)
				}
				run, err := store.LoadRun(cmd.Context(), runID)
				if err != nil {
					return fmt.Errorf("evaluation: runs cancel: %w", err)
				}
				reporter := &runLifecycle{
					client:       appClient,
					live:         live,
					deps:         deps,
					store:        store,
					cliSessionID: authContext.CLISessionID,
					runID:        runID,
					startedAt:    run.GetStartedAt().AsTime().UTC(),
				}
				if err := reporter.reportTerminal(cmd.Context(), true); err != nil {
					return fmt.Errorf("evaluation: runs cancel: %w", err)
				}
			}
			if publish {
				publication, err := NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
				if err != nil {
					return fmt.Errorf("evaluation: runs cancel: %w", err)
				}
				if result.Published, err = publication.PublishRunCatchUp(cmd.Context(), runID); err != nil {
					return fmt.Errorf("evaluation: runs cancel: %w", err)
				}
			}
			if jsonOutput {
				return output.WriteJSON(out, result)
			}
			_, err = fmt.Fprintf(out, "Cancelled run %s: stopped %d assignment(s)\n", runID, result.Stopped)
			if err == nil && publish {
				_, err = fmt.Fprintf(out, "Published %d projection record(s) for run %s\n", result.Published, runID)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&publish, "publish", false, "Publish the stopped assignments and run summary to the public mirror")
	return cmd
}

// awaitRunStop asks the process holding a run's lease to stop and waits until
// that process releases the run, which it does after its current assignment
// reaches a terminal record and it has reported the released status.
//
// The stop reaches the process as an event on the owner's CLI session stream,
// and the release arrives on the same stream. The stream is subscribed first,
// and its connect reconciles the lease, so a release that happened before the
// subscription, or while the stream was down, is not missed.
//
// The holder's exit is watched on the operating system. A process that dies
// without releasing the run does not leave cancel waiting for an event that never
// comes: its lease is settled locally, and cleared reports that cancel cleared it.
func awaitRunStop(cmd *cobra.Command, deps nativeEvalDeps, cfg *config.Config, fileSvc fs.RuntimeFileService, store *evaluation.Store, control processControl, live evaluation.LeaseLiveness, lease *evaluation.RunLease, jsonOutput bool) (bool, error) {
	ctx := cmd.Context()
	runID := lease.RunID
	authContext, err := deps.authLoader(fileSvc, cfg)
	if err != nil {
		return false, fmt.Errorf("load CLI identity: %w", err)
	}
	appClient, err := inferenceEvalAppClient(ctx, cmd.ErrOrStderr(), deps, fileSvc, cfg, authContext)
	if err != nil {
		return false, err
	}
	run, err := store.LoadRun(ctx, runID)
	if err != nil {
		return false, err
	}
	httpClient, err := auth.BuildMTLSClient(fileSvc, cfg, 0)
	if err != nil {
		return false, fmt.Errorf("build stream client: %w", err)
	}

	events := make(chan runEvent)
	// released receives the first settled outcome of a connect reconcile.
	released := make(chan runRelease, 1)
	var releaseOnce sync.Once
	release := func(r runRelease) { releaseOnce.Do(func() { released <- r }) }
	stream, err := subscribeRunEvents(ctx, httpClient, cfg.OperatorPublicURL(), authContext.CLISessionID, runID, events, func() {
		done, cleared, err := settleRunLease(ctx, store, live, runID)
		switch {
		case err != nil:
			release(runRelease{err: err})
		case done:
			release(runRelease{cleared: cleared})
		}
	})
	if err != nil {
		return false, err
	}
	defer stream.Close()
	select {
	case r := <-released:
		// The lease was already gone or stale when the stream connected.
		return r.cleared, r.err
	default:
	}

	// The exit is watched from before the stop request, so an exit that lands
	// during the request is still seen.
	exitCtx, stopExitWatch := context.WithCancel(ctx)
	defer stopExitWatch()
	holderExit := watchHolderExit(exitCtx, control, lease.PID)

	if _, err := store.RequestRunCancel(ctx, runID, deps.now()); err != nil {
		return false, err
	}
	// Delivery is best effort: the cancel request in the lease reaches a process
	// that cannot be signalled.
	_ = control.Interrupt(lease.PID)
	if err := appClient.RequestRunStop(ctx, models.ObserveProducerStopRequest{
		SchemaVersion: constants.ObserveEventPayloadSchemaVersion,
		CampaignID:    run.GetCampaignBinding().GetCampaignId(),
		RunID:         runID,
		StopReason:    models.StopReasonGraceful,
		StopScope:     models.StopScopeCycle,
		CLISessionID:  authContext.CLISessionID,
	}); err != nil {
		return false, fmt.Errorf("request stop: %w", err)
	}
	if !jsonOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Waiting for process %d to finish its current assignment\n", lease.PID)
	}
	cleared, err := awaitRunRelease(ctx, released, events, stream, holderExit, func() (bool, bool, error) {
		return settleRunLease(ctx, store, live, runID)
	})
	if err != nil {
		return false, err
	}
	if cleared && !jsonOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Process %d exited; cleared its lease on run %s\n", lease.PID, runID)
	}
	return cleared, nil
}

// runRelease is the outcome of a connect reconcile that settled a run's lease:
// cleared reports that the reconcile cleared a stale lease, and err is the error
// that kept the answer from being known.
type runRelease struct {
	cleared bool
	err     error
}

// awaitRunRelease waits for the holder of a run to release it. It returns when
// the stream reports a settled status, when released delivers the connect
// reconcile's outcome, or when the holder's process exits and settle finds its
// lease gone or stale. The reconcile and exit cases report whether the lease was
// cleared. The wait also ends when the stream stops on its own, with the stream's
// error, or when ctx ends.
func awaitRunRelease(ctx context.Context, released <-chan runRelease, events <-chan runEvent, stream *runEventStream, holderExit <-chan error, settle func() (bool, bool, error)) (bool, error) {
	for {
		select {
		case r := <-released:
			return r.cleared, r.err
		case ev := <-events:
			if ev.Status != nil && runStatusSettled(ev.Status.Status) {
				return false, nil
			}
		case err := <-holderExit:
			if err != nil {
				return false, fmt.Errorf("wait for run holder: %w", err)
			}
			done, cleared, err := settle()
			if err != nil {
				return false, err
			}
			if done {
				return cleared, nil
			}
			// The PID now names another live process, so the lease still looks held.
			// The stream remains the release signal, and the exit is not awaited again.
			holderExit = nil
		case <-stream.Done():
			return false, stream.StoppedError()
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// settleRunLease reports whether a run's process has released the run: its lease
// is missing, or it names a process that is no longer alive. A stale lease is
// cleared, as cancel always has, and cleared reports that this call cleared it. A
// lease the process released itself, or that a concurrent settle already cleared,
// counts as released and is not reported as cleared.
func settleRunLease(ctx context.Context, store *evaluation.Store, live evaluation.LeaseLiveness, runID string) (released, cleared bool, err error) {
	lease, err := store.LoadRunLease(ctx, runID)
	switch {
	case isMissingRecord(err):
		return true, false, nil
	case err != nil:
		return false, false, err
	case live(*lease):
		return false, false, nil
	}
	err = store.ClearRunLease(ctx, runID)
	switch {
	case isMissingRecord(err):
		return true, false, nil
	case err != nil:
		return false, false, err
	}
	return true, true, nil
}

// runStatusSettled reports whether a run status means its process has finished
// with the run. The process reports running before it executes, and every other
// status it reports comes after it has released the lease.
func runStatusSettled(status models.RunLifecycleStatus) bool {
	return status != models.RunLifecycleStatusQueued && status != models.RunLifecycleStatusRunning
}
