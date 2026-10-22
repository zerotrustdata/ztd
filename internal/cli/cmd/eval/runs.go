// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
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

// normalizeRuntimeEvalPath accepts a runtime-relative path with or without the
// leading runtime directory.
func normalizeRuntimeEvalPath(rawPath string) string {
	return strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rawPath)), constants.RuntimeDirname+"/")
}

func runsEvalCmd(deps nativeEvalDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Start, follow, verify, and publish campaign runs",
		Long: `A run is one execution of a campaign. Runs have a lifecycle: start, resume,
cancel, verify, publish, and export, and they can be archived.`,
	}
	addLeaves(cmd,
		runsListCmd(deps),
		runsShowCmd(deps),
		runsAssignmentsCmd(deps),
		runsTraceCmd(deps),
		runsStartCmd(deps),
		runsResumeCmd(deps),
		runsCancelCmd(deps),
		runsVerifyCmd(deps),
		runsPublishCmd(deps),
		runsExportCmd(deps),
		runsRepairCmd(deps),
		runsCompareCmd(deps),
		runsArchiveCmd(deps),
		runsUnarchiveCmd(deps),
		runsDeleteCmd(deps),
	)
	return cmd
}

type runListJSON struct {
	Runs []runRow `json:"runs"`
}

func runsListCmd(deps nativeEvalDeps) *cobra.Command {
	var campaignFilter, statusFilter string
	var includeArchived bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runs",
		Long: `List runs with their status and progress.

Archived runs are hidden unless --archived is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			control, err := deps.runControl.process(fileSvc)
			if err != nil {
				return err
			}
			runIDs, err := evaluation.NewStore(fileSvc).ListRunIDs(cmd.Context())
			if err != nil {
				return fmt.Errorf("evaluation: runs list: %w", err)
			}
			if includeArchived {
				archivedIDs, err := evaluation.NewArchivedStore(fileSvc).ListRunIDs(cmd.Context())
				if err != nil {
					return fmt.Errorf("evaluation: runs list: %w", err)
				}
				runIDs = append(runIDs, archivedIDs...)
			}
			sort.Strings(runIDs)
			rows, err := campaignRunRows(cmd.Context(), fileSvc, runIDs, leaseLiveness(control), deps)
			if err != nil {
				return fmt.Errorf("evaluation: runs list: %w", err)
			}
			selected := rows[:0:0]
			for _, row := range rows {
				if campaignFilter != "" && row.CampaignID != campaignFilter {
					continue
				}
				if statusFilter != "" && row.Status != statusFilter {
					continue
				}
				selected = append(selected, row)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), runListJSON{Runs: selected})
			}
			if len(selected) == 0 {
				cmd.Println("No runs found")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "RUN\tCAMPAIGN\tSTATUS\tPROGRESS\tLANE\tARCHIVED")
			for _, row := range selected {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%s\t%t\n", row.RunID, row.CampaignID, row.Status, row.Terminal, row.ExpectedAssignments, row.Lane, row.Archived)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&campaignFilter, "campaign", "", "Only runs of this campaign")
	cmd.Flags().StringVar(&statusFilter, "status", "", "Only runs with this status (for example running, completed, verified, interrupted)")
	cmd.Flags().BoolVar(&includeArchived, "archived", false, "Include archived runs")
	return cmd
}

type runShowJSON struct {
	runRow
	ModelCount          int    `json:"model_count"`
	ScenarioCount       uint32 `json:"scenario_count"`
	RepetitionCount     uint32 `json:"repetition_count"`
	ModelRegistryDigest string `json:"model_registry_digest"`
	CatalogDigest       string `json:"catalog_digest"`
	InferenceSession    string `json:"inference_session,omitempty"`
	DataSession         string `json:"data_session,omitempty"`
}

func loadRunShow(ctx context.Context, deps nativeEvalDeps, store *evaluation.Store, archived bool, runID string, live evaluation.LeaseLiveness) (*runShowJSON, error) {
	row, summary, err := summarizeRun(ctx, store, archived, runID, live, deps.now, adaptNewID(deps.newID))
	if err != nil {
		return nil, err
	}
	binding := summary.Run.GetCampaignBinding()
	spec, err := store.LoadCampaignSpec(ctx, binding.GetCampaignId())
	if err != nil {
		return nil, err
	}
	return &runShowJSON{
		runRow:              *row,
		ModelCount:          len(spec.GetModelRegistry()),
		ScenarioCount:       spec.GetScenarioCount(),
		RepetitionCount:     spec.GetRepetitionCount(),
		ModelRegistryDigest: spec.GetModelRegistryDigest(),
		CatalogDigest:       spec.GetCatalogDigest(),
		InferenceSession:    binding.GetInferenceOperatorSessionId(),
		DataSession:         binding.GetDataOperatorSessionId(),
	}, nil
}

func writeRunShow(cmd *cobra.Command, show *runShowJSON) error {
	out := cmd.OutOrStdout()
	_, err := fmt.Fprintf(out, "Run: %s\nCampaign: %s\nStatus: %s\nLane: %s\nStarted: %s\nModels: %d\nScenarios: %d\nRepetitions: %d\nModel registry digest: %s\nCatalog digest: %s\nExpected assignments: %d\nQueued: %d\nRunning: %d\nTerminal: %d\nStopped: %d\nNext assignment: %s\nArchived: %t\n",
		show.RunID, show.CampaignID, show.Status, show.Lane, show.StartedAt, show.ModelCount, show.ScenarioCount, show.RepetitionCount,
		show.ModelRegistryDigest, show.CatalogDigest, show.ExpectedAssignments, show.Queued, show.Running, show.Terminal, show.Stopped, show.NextAssignmentID, show.Archived)
	if err != nil {
		return err
	}
	if show.Holder != nil {
		_, err = fmt.Fprintf(out, "Held by: process %d on %s since %s\n", show.Holder.PID, show.Holder.Host, show.Holder.StartedAt)
	}
	return err
}

func runsShowCmd(deps nativeEvalDeps) *cobra.Command {
	var watch bool
	cmd := &cobra.Command{
		Use:   "show <run>",
		Short: "Show one run's status and progress",
		Long: `Show one run. The status is "running" only while a live process holds the
run's lease. A run left running with no live process reports "interrupted".

With --watch, follow the run's event stream and report the run on each run status
change or stop request until the run reaches a settled status. The stream needs
the CLI identity, as runs cancel does. Assignment counts refresh only when the
run's status changes, because no per-assignment event reaches the CLI.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			control, err := deps.runControl.process(fileSvc)
			if err != nil {
				return err
			}
			live := leaseLiveness(control)
			store, archived, err := evaluation.LocateRun(cmd.Context(), fileSvc, runID)
			if err != nil {
				return fmt.Errorf("evaluation: runs show: %w", err)
			}
			show, err := loadRunShow(cmd.Context(), deps, store, archived, runID, live)
			if err != nil {
				return fmt.Errorf("evaluation: runs show: %w", err)
			}
			if output.JSONEnabled(cmd) && !watch {
				return output.WriteJSON(cmd.OutOrStdout(), show)
			}
			if !output.JSONEnabled(cmd) {
				if err := writeRunShow(cmd, show); err != nil {
					return err
				}
			}
			if !watch {
				return nil
			}
			return watchRun(cmd, deps, cfg, fileSvc, store, archived, runID, live, show)
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "Keep reporting progress until the run settles")
	return cmd
}

// watchRun follows a run until it settles. A run that has already settled is
// reported once, with no stream. Otherwise the run's event stream is subscribed,
// and the run is reloaded on each connect and on each run event. The connect
// reload is the reconcile, so a change made while the stream was down is shown.
func watchRun(cmd *cobra.Command, deps nativeEvalDeps, cfg *config.Config, fileSvc fs.RuntimeFileService, store *evaluation.Store, archived bool, runID string, live evaluation.LeaseLiveness, last *runShowJSON) error {
	emit := func(show *runShowJSON) error {
		if output.JSONEnabled(cmd) {
			body, err := json.Marshal(show)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %d/%d terminal  %d queued  %d running\n",
			deps.now().UTC().Format("15:04:05"), show.Status, show.Terminal, show.ExpectedAssignments, show.Queued, show.Running)
		return err
	}
	if terminalRunStatus(last.Status) {
		if output.JSONEnabled(cmd) {
			return emit(last)
		}
		return nil
	}

	authContext, err := deps.authLoader(fileSvc, cfg)
	if err != nil {
		return fmt.Errorf("load CLI identity: %w", err)
	}
	httpClient, err := auth.BuildMTLSClient(fileSvc, cfg, 0)
	if err != nil {
		return fmt.Errorf("build stream client: %w", err)
	}
	ctx := cmd.Context()
	events := make(chan runEvent)
	// reconnects holds one pending reload. A reload reads the run's current state,
	// so one pending connect covers every connect that lands before it is read.
	reconnects := make(chan struct{}, 1)
	stream, err := subscribeRunEvents(ctx, httpClient, cfg.OperatorPublicURL(), authContext.CLISessionID, runID, events, func() {
		select {
		case reconnects <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return err
	}
	defer stream.Close()
	return followRun(ctx, stream, reconnects, events, func() (*runShowJSON, error) {
		return loadRunShow(ctx, deps, store, archived, runID, live)
	}, emit)
}

// followRun reports the run after each reconnect and each run event. It returns
// when the run settles, when ctx ends, or with the error the stream stopped on.
func followRun(ctx context.Context, stream *runEventStream, reconnects <-chan struct{}, events <-chan runEvent, reload func() (*runShowJSON, error), emit func(*runShowJSON) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-stream.Done():
			// Ending ctx also stops the stream; that stop is the ctx case.
			select {
			case <-ctx.Done():
				return nil
			default:
				return stream.StoppedError()
			}
		case <-reconnects:
		case <-events:
		}
		show, err := reload()
		if err != nil {
			return fmt.Errorf("evaluation: runs show: %w", err)
		}
		if err := emit(show); err != nil {
			return err
		}
		if terminalRunStatus(show.Status) {
			return nil
		}
	}
}

type runPublishJSON struct {
	RunID            string `json:"run_id"`
	PublishedRecords int    `json:"published_records"`
	Force            bool   `json:"force"`
}

type runPublishSkipJSON struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason"`
}

type runPublishFailureJSON struct {
	RunID string `json:"run_id"`
	Error string `json:"error"`
}

type runPublishAllJSON struct {
	Published []runPublishJSON        `json:"published"`
	Skipped   []runPublishSkipJSON    `json:"skipped"`
	Failed    []runPublishFailureJSON `json:"failed"`
}

func runsPublishCmd(deps nativeEvalDeps) *cobra.Command {
	var force, all bool
	cmd := &cobra.Command{
		Use:   "publish [<run>]",
		Short: "Publish missing public lifecycle projections for one run, or republish every run",
		Long: `Publish the public lifecycle projections one run is missing.

With --all, republish every run that is not archived. Each run's host
publication idempotency is cleared first, as --force does, so a run that was
already published is published again. A run a live process holds is skipped,
because that process publishes it. A run that fails to publish does not stop
the others; the command reports each failure and exits with an error.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if !all {
				return cobra.ExactArgs(1)(cmd, args)
			}
			if len(args) != 0 {
				return fmt.Errorf("evaluation: runs publish: --all cannot be combined with a run: %w", constants.ErrEvaluationFlagsInvalid)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			if all {
				publication, err := NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
				if err != nil {
					return fmt.Errorf("evaluation: runs publish: %w", err)
				}
				return publishAllRuns(cmd, deps, fileSvc, publication)
			}
			runID := args[0]
			if err := evaluation.RejectArchivedRun(cmd.Context(), fileSvc, runID); err != nil {
				return fmt.Errorf("evaluation: runs publish: %w", err)
			}
			publication, err := NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
			if err != nil {
				return fmt.Errorf("evaluation: runs publish: %w", err)
			}
			count, err := publishRun(cmd.Context(), fileSvc, publication, runID, force)
			if err != nil {
				return fmt.Errorf("evaluation: runs publish: %w", err)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), runPublishJSON{RunID: runID, PublishedRecords: count, Force: force})
			}
			return writeRunPublished(cmd, runID, count, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Clear host publication idempotency and republish all projections (use after a gateway mirror volume wipe)")
	cmd.Flags().BoolVar(&all, "all", false, "Republish every run that is not archived, clearing publication idempotency as --force does")
	return cmd
}

// publishRun publishes one run's missing projections and its bound
// verification. With force, the run's host publication idempotency is cleared
// first, so every projection is published again.
func publishRun(ctx context.Context, fileSvc fs.RuntimeFileService, publication *evaluation.CampaignPublicationCoordinator, runID string, force bool) (int, error) {
	report, err := evaluation.NewStore(fileSvc).LoadCampaignVerification(ctx, runID)
	if err != nil && !isMissingRecord(err) {
		return 0, fmt.Errorf("load verification report: %w", err)
	}
	if force {
		if err := publication.ResetPublicationIdempotency(ctx, runID); err != nil {
			return 0, err
		}
	}
	return publication.PublishRunCatchUpWithVerification(ctx, runID, report)
}

func writeRunPublished(cmd *cobra.Command, runID string, count int, force bool) error {
	suffix := ""
	if force {
		suffix = " (forced republish)"
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Published %d public projection record(s) for run %s%s\n", count, runID, suffix)
	return err
}

// publishAllRuns force-republishes every run in the active store. A run a live
// process holds is skipped, because that process publishes it. A failed run is
// recorded and the remaining runs still publish.
func publishAllRuns(cmd *cobra.Command, deps nativeEvalDeps, fileSvc fs.RuntimeFileService, publication *evaluation.CampaignPublicationCoordinator) error {
	ctx := cmd.Context()
	control, err := deps.runControl.process(fileSvc)
	if err != nil {
		return err
	}
	live := leaseLiveness(control)
	store := evaluation.NewStore(fileSvc)
	runIDs, err := store.ListRunIDs(ctx)
	if err != nil {
		return fmt.Errorf("evaluation: runs publish: %w", err)
	}
	jsonOut := output.JSONEnabled(cmd)
	payload := runPublishAllJSON{Published: []runPublishJSON{}, Skipped: []runPublishSkipJSON{}, Failed: []runPublishFailureJSON{}}
	var failures []error
	fail := func(runID string, err error) {
		failures = append(failures, fmt.Errorf("run %s: %w", runID, err))
		payload.Failed = append(payload.Failed, runPublishFailureJSON{RunID: runID, Error: err.Error()})
	}
	for _, runID := range runIDs {
		lease, err := store.LoadRunLease(ctx, runID)
		switch {
		case err == nil && live(*lease):
			skip := runPublishSkipJSON{RunID: runID, Reason: fmt.Sprintf("held by live process %d on %s", lease.PID, lease.Host)}
			payload.Skipped = append(payload.Skipped, skip)
			if !jsonOut {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Skipped run %s: %s\n", runID, skip.Reason); err != nil {
					return err
				}
			}
			continue
		case err != nil && !isMissingRecord(err):
			fail(runID, err)
			continue
		}
		count, err := publishRun(ctx, fileSvc, publication, runID, true)
		if err != nil {
			fail(runID, err)
			continue
		}
		payload.Published = append(payload.Published, runPublishJSON{RunID: runID, PublishedRecords: count, Force: true})
		if !jsonOut {
			if err := writeRunPublished(cmd, runID, count, true); err != nil {
				return err
			}
		}
	}
	if jsonOut {
		if err := output.WriteJSON(cmd.OutOrStdout(), payload); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Republished %d run(s), skipped %d, failed %d\n", len(payload.Published), len(payload.Skipped), len(payload.Failed)); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("evaluation: runs publish: %d of %d run(s) failed: %w", len(failures), len(runIDs), errors.Join(failures...))
	}
	return nil
}

type runExportJSON struct {
	RunID               string                          `json:"run_id"`
	CampaignID          string                          `json:"campaign_id"`
	OutputDir           string                          `json:"output_dir"`
	ExportedAt          string                          `json:"exported_at"`
	AssignmentCount     uint32                          `json:"assignment_count"`
	TerminalResultCount uint32                          `json:"terminal_result_count"`
	Files               []evaluation.CampaignExportFile `json:"files"`
}

func runsExportCmd(deps nativeEvalDeps) *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "export <run>",
		Short: "Generate disclosure-safe JSONL, CSV, and SQLite exports for one run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			store, _, err := evaluation.LocateRun(cmd.Context(), fileSvc, runID)
			if err != nil {
				return fmt.Errorf("evaluation: runs export: %w", err)
			}
			report, err := evaluation.NewCampaignExporter(deps.now).ExportRun(cmd.Context(), store, fileSvc, runID, normalizeRuntimeEvalPath(outputDir))
			if err != nil {
				return fmt.Errorf("evaluation: runs export: %w", err)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), runExportJSON{
					RunID:               report.RunID,
					CampaignID:          report.CampaignID,
					OutputDir:           report.OutputDir,
					ExportedAt:          report.ExportedAt.Format(time.RFC3339),
					AssignmentCount:     report.AssignmentCount,
					TerminalResultCount: report.TerminalResultCount,
					Files:               report.Files,
				})
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Exported run %s to %s\nAssignments: %d scheduled, %d terminal results\n",
				report.RunID, report.OutputDir, report.AssignmentCount, report.TerminalResultCount)
			for _, file := range report.Files {
				_, _ = fmt.Fprintf(out, "- %s (%s, %d record(s))\n", file.Name, file.Format, file.RecordCount)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Runtime-relative directory to write the export into, outside data/eval and reports (default export/<run>)")
	_ = cmd.MarkFlagRequired("output-dir")
	return cmd
}

type runRepairJSON struct {
	RunID   string `json:"run_id"`
	Results int    `json:"repaired_results"`
	// Regraded counts the results --regrade re-imported from their sealed
	// agent traces.
	Regraded int `json:"regraded_results,omitempty"`
	// ProjectionReopened is the terminal Gateway run status --projection
	// replaced, empty when the projection was not reopened.
	ProjectionReopened models.RunLifecycleStatus `json:"projection_reopened,omitempty"`
}

func runsRepairCmd(deps nativeEvalDeps) *cobra.Command {
	var projection, regrade bool
	cmd := &cobra.Command{
		Use:   "repair <run>",
		Short: "Backfill or regrade terminal assignment results",
		Long: `Repair one run's persisted records: every assignment that reached a terminal
lifecycle without a persisted result gets one, recorded as a recovered failure.
A running assignment with no result and no agent trace, whose frozen scenario
tools this harness rejects, is recorded as a scenario_contract_invalid failure:
the executor rejects that contract before it acts, so the attempt never acted.
Any other running assignment stays unresolved.

With --projection, repair also corrects the Gateway's run projection when it
holds a terminal status the local store contradicts, such as a paused run
projected as cancelled. The local store is authoritative: a run with assignments
queued and none cancelled is reported as waiting, and the Gateway reopens the
terminal projection only while it still holds that exact status.

With --regrade, repair re-imports each terminal assignment result from its
sealed agent trace when the trace now imports to a different lifecycle, such as
an INVALID_EVIDENCE an import check since fixed recorded for valid evidence.
The sealed trace is never changed. Run ztd eval runs verify and then
ztd eval runs publish --force to republish the corrected projections.

Repair holds the run lease throughout, so it refuses a run a live process holds.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			if err := evaluation.RejectArchivedRun(cmd.Context(), fileSvc, runID); err != nil {
				return fmt.Errorf("evaluation: runs repair: %w", err)
			}
			store := evaluation.NewStore(fileSvc)
			controller := evaluation.NewCampaignController(store, nil, deps.now, adaptNewID(deps.newID))
			payload := runRepairJSON{RunID: runID}
			if err := withRepairLease(cmd.Context(), deps, fileSvc, store, runID, func(lease repairLease) error {
				if payload.Results, err = controller.RepairAssignmentsWithoutResults(cmd.Context(), runID); err != nil {
					return err
				}
				if regrade {
					if payload.Regraded, err = controller.RegradeAssignmentsFromAgentTraces(cmd.Context(), runID); err != nil {
						return err
					}
				}
				if projection {
					payload.ProjectionReopened, err = repairRunProjection(cmd, deps, cfg, fileSvc, store, runID, lease)
				}
				return err
			}); err != nil {
				return fmt.Errorf("evaluation: runs repair: %w", err)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), payload)
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "Repaired %d terminal assignment result(s) for run %s\n", payload.Results, runID); err != nil {
				return err
			}
			if regrade {
				if _, err := fmt.Fprintf(out, "Regraded %d assignment result(s) from sealed agent traces for run %s\n", payload.Regraded, runID); err != nil {
					return err
				}
			}
			switch {
			case !projection:
				return nil
			case payload.ProjectionReopened != "":
				_, err = fmt.Fprintf(out, "Reopened Gateway run projection for run %s: %s -> %s\n", runID, payload.ProjectionReopened, models.RunLifecycleStatusWaiting)
			default:
				_, err = fmt.Fprintf(out, "Gateway run projection for run %s needed no reopen\n", runID)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&projection, "projection", false, "Reopen a terminal Gateway run projection the local store contradicts")
	cmd.Flags().BoolVar(&regrade, "regrade", false, "Re-import terminal results whose sealed agent trace imports to a different lifecycle")
	return cmd
}

// repairLease is the run lease repair holds: its owner and the liveness rule
// it was acquired under.
type repairLease struct {
	pid  int
	host string
	live evaluation.LeaseLiveness
}

// withRepairLease runs repair while holding the run lease, so no live process
// executes or reports the run concurrently, and releases it afterwards.
func withRepairLease(ctx context.Context, deps nativeEvalDeps, fileSvc fs.RuntimeFileService, store *evaluation.Store, runID string, repair func(repairLease) error) (repairErr error) {
	control, err := deps.runControl.process(fileSvc)
	if err != nil {
		return err
	}
	host, err := control.Hostname()
	if err != nil {
		return fmt.Errorf("run lease: read hostname: %w", err)
	}
	lease := repairLease{pid: control.PID(), host: host, live: leaseLiveness(control)}
	if _, err := store.AcquireRunLease(ctx, evaluation.RunLease{
		RunID:     runID,
		PID:       lease.pid,
		Host:      host,
		StartedAt: deps.now().UTC(),
	}, lease.live); err != nil {
		return err
	}
	defer func() {
		if releaseErr := store.ReleaseRunLease(context.WithoutCancel(ctx), runID, lease.pid); releaseErr != nil {
			repairErr = errors.Join(repairErr, fmt.Errorf("release run lease: %w", releaseErr))
		}
	}()
	return repair(lease)
}

// repairRunProjection reconciles the Gateway run projection with the local
// store while repair holds the run lease.
func repairRunProjection(cmd *cobra.Command, deps nativeEvalDeps, cfg *config.Config, fileSvc fs.RuntimeFileService, store *evaluation.Store, runID string, held repairLease) (models.RunLifecycleStatus, error) {
	ctx := cmd.Context()
	run, err := store.LoadRun(ctx, runID)
	if err != nil {
		return "", err
	}
	authContext, err := deps.authLoader(fileSvc, cfg)
	if err != nil {
		return "", fmt.Errorf("load CLI identity: %w", err)
	}
	appClient, err := inferenceEvalAppClient(ctx, cmd.ErrOrStderr(), deps, fileSvc, cfg, authContext)
	if err != nil {
		return "", err
	}
	// The repair's own lease only excludes other processes; the run is read as
	// it stands released, which is what the projection must show.
	released := func(lease evaluation.RunLease) bool {
		return (lease.PID != held.pid || lease.Host != held.host) && held.live(lease)
	}
	reporter := &runLifecycle{
		client:       appClient,
		live:         released,
		deps:         deps,
		store:        store,
		cliSessionID: authContext.CLISessionID,
		runID:        runID,
		startedAt:    run.GetStartedAt().AsTime().UTC(),
	}
	return reporter.reconcileProjection(ctx)
}

// runCell is one cell of a run's matrix reduced to what compare reports.
type runCell struct {
	Status  string  `json:"status"`
	Verdict string  `json:"verdict,omitempty"`
	Passed  bool    `json:"passed"`
	Score   float64 `json:"score"`
	// ToolRejected marks a cell whose provider refused the tool declaration. The
	// cell still scores as a FAIL (INV-EVAL-CAMP-07); the flag only lets reports
	// separate "model cannot call tools" from "model called tools badly".
	ToolRejected bool `json:"tool_rejected,omitempty"`
}

// invalidEvidence reports a cell whose grades measure the harness, not the
// model. It is shown, never counted for or against the model.
func (c runCell) invalidEvidence() bool { return c.Verdict == invalidEvidenceVerdict }

const (
	verdictStatusPrefix    = "EVALUATION_VERDICT_STATUS_"
	invalidEvidenceVerdict = "invalid_evidence"
)

// cellFromResult reads one terminal result's outcome from the same verdict and
// pass rate every other surface publishes; it never re-tallies grades.
func cellFromResult(result *evalv1.EvaluationAssignmentResult) runCell {
	verdict := evaluation.DerivePublicSummaryStatus(result)
	cell := runCell{
		Status:  strings.ToLower(strings.TrimPrefix(result.GetLifecycleStatus().String(), "EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_")),
		Verdict: strings.ToLower(strings.TrimPrefix(verdict.String(), verdictStatusPrefix)),
		Passed:  verdict == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,

		ToolRejected: result.GetTrajectoryOutcome() == evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_PROVIDER_REJECTED_TOOL_DECLARATION,
	}
	if rate, ok := evaluation.DeterministicPassRate(result); ok {
		cell.Score = rate
	}
	return cell
}

type runCompareSide struct {
	RunID           string  `json:"run_id"`
	CampaignID      string  `json:"campaign_id"`
	Cells           int     `json:"cells"`
	Completed       int     `json:"completed"`
	Passed          int     `json:"passed"`
	InvalidEvidence int     `json:"invalid_evidence"`
	ToolRejected    int     `json:"tool_rejected"`
	MeanScore       float64 `json:"mean_score"`
}

type runCompareChange struct {
	Cell  string  `json:"cell"`
	Kind  string  `json:"kind"`
	Left  runCell `json:"left"`
	Right runCell `json:"right"`
}

type runCompareJSON struct {
	Left         runCompareSide     `json:"left"`
	Right        runCompareSide     `json:"right"`
	OnlyInLeft   []string           `json:"only_in_left"`
	OnlyInRight  []string           `json:"only_in_right"`
	Regressions  int                `json:"regressions"`
	Improvements int                `json:"improvements"`
	Changes      []runCompareChange `json:"changes"`
}

// runCellKey names a matrix cell independently of the run it belongs to.
func runCellKey(assignment *evalv1.EvaluationAssignment) string {
	return fmt.Sprintf("%s | %s | rep %d", assignment.GetScenarioId(), assignmentTarget(assignment), assignment.GetRepetition())
}

// assignmentTarget names what an assignment exercises: `<served model>/<role>`
// for one model in one role, `stack:<id>` for a heterogeneous formation.
func assignmentTarget(assignment *evalv1.EvaluationAssignment) string {
	switch t := assignment.GetTarget().(type) {
	case *evalv1.EvaluationAssignment_Homogeneous:
		return fmt.Sprintf("%s/%s", t.Homogeneous.GetCandidateVariant().GetServedModelTag(), assignmentRoleLabel(t.Homogeneous.GetDesignatedRole()))
	case *evalv1.EvaluationAssignment_Heterogeneous:
		return "stack:" + t.Heterogeneous.GetStack().GetStackId()
	default:
		return "unknown"
	}
}

func assignmentRoleLabel(role evalv1.ModelCampaignRole) string {
	return strings.ToLower(strings.TrimPrefix(role.String(), "MODEL_CAMPAIGN_ROLE_"))
}

// executedAssignmentLine is the execution-log line for one finished assignment:
// what ran (scenario, target, repetition), how long it took, and its outcome.
func executedAssignmentLine(assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) string {
	took := ""
	if elapsed, ok := assignmentDuration(assignment, result); ok {
		took = ", " + elapsed.Round(100*time.Millisecond).String()
	}
	return fmt.Sprintf("Executed %s [%s%s]: %s", result.GetAssignmentId(), runCellKey(assignment), took, evaluation.AssignmentOutcomeSummary(result))
}

// assignmentDuration is the wall-clock time from start to completion, when both
// were recorded.
func assignmentDuration(assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) (time.Duration, bool) {
	started := assignment.GetStartedAt()
	completed := result.GetCompletedAt()
	if started == nil || completed == nil {
		return 0, false
	}
	return completed.AsTime().Sub(started.AsTime()), true
}

func loadRunCells(ctx context.Context, store *evaluation.Store, runID string) (map[string]runCell, error) {
	assignments, err := store.ListAssignments(ctx, runID)
	if err != nil {
		return nil, err
	}
	cells := make(map[string]runCell, len(assignments))
	for _, assignment := range assignments {
		cell := runCell{Status: strings.ToLower(strings.TrimPrefix(assignment.GetLifecycleStatus().String(), "EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_"))}
		exists, err := store.AssignmentResultExists(ctx, runID, assignment.GetAssignmentId())
		if err != nil {
			return nil, err
		}
		if exists {
			result, err := store.LoadAssignmentResult(ctx, runID, assignment.GetAssignmentId())
			if err != nil {
				return nil, err
			}
			cell = cellFromResult(result)
		}
		cells[runCellKey(assignment)] = cell
	}
	return cells, nil
}

func summarizeRunCells(runID, campaignID string, cells map[string]runCell) runCompareSide {
	side := runCompareSide{RunID: runID, CampaignID: campaignID, Cells: len(cells)}
	var total float64
	for _, cell := range cells {
		if cell.Status == "completed" {
			side.Completed++
		}
		if cell.Passed {
			side.Passed++
		}
		if cell.ToolRejected {
			side.ToolRejected++
		}
		if cell.invalidEvidence() {
			side.InvalidEvidence++
			continue
		}
		total += cell.Score
	}
	if measured := side.Cells - side.InvalidEvidence; measured > 0 {
		side.MeanScore = total / float64(measured)
	}
	return side
}

// compareRunCells reports what changed from left to right.
func compareRunCells(left, right map[string]runCell) (onlyLeft, onlyRight []string, changes []runCompareChange) {
	for key, l := range left {
		r, ok := right[key]
		if !ok {
			onlyLeft = append(onlyLeft, key)
			continue
		}
		switch {
		case l.invalidEvidence() || r.invalidEvidence():
			if l != r {
				changes = append(changes, runCompareChange{Cell: key, Kind: "changed", Left: l, Right: r})
			}
		case l.Passed && !r.Passed:
			changes = append(changes, runCompareChange{Cell: key, Kind: "regression", Left: l, Right: r})
		case !l.Passed && r.Passed:
			changes = append(changes, runCompareChange{Cell: key, Kind: "improvement", Left: l, Right: r})
		case l.Status != r.Status || l.Score != r.Score:
			changes = append(changes, runCompareChange{Cell: key, Kind: "changed", Left: l, Right: r})
		}
	}
	for key := range right {
		if _, ok := left[key]; !ok {
			onlyRight = append(onlyRight, key)
		}
	}
	sort.Strings(onlyLeft)
	sort.Strings(onlyRight)
	sort.Slice(changes, func(i, j int) bool { return changes[i].Cell < changes[j].Cell })
	return onlyLeft, onlyRight, changes
}

func runsCompareCmd(deps nativeEvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "compare <run> <run>",
		Short: "Compare two runs cell by cell",
		Long: `Compare two runs over the matrix cells they share. Reports per-run totals and
every cell whose outcome or deterministic score differs. A cell that passed in
the first run and did not in the second is a regression. Archived runs compare
like any other.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			sides := make([]runCompareSide, 0, 2)
			cellSets := make([]map[string]runCell, 0, 2)
			for _, runID := range args {
				store, _, err := evaluation.LocateRun(cmd.Context(), fileSvc, runID)
				if err != nil {
					return fmt.Errorf("evaluation: runs compare: %w", err)
				}
				run, err := store.LoadRun(cmd.Context(), runID)
				if err != nil {
					return fmt.Errorf("evaluation: runs compare: %w", err)
				}
				cells, err := loadRunCells(cmd.Context(), store, runID)
				if err != nil {
					return fmt.Errorf("evaluation: runs compare: %w", err)
				}
				cellSets = append(cellSets, cells)
				sides = append(sides, summarizeRunCells(runID, run.GetCampaignBinding().GetCampaignId(), cells))
			}
			onlyLeft, onlyRight, changes := compareRunCells(cellSets[0], cellSets[1])
			payload := runCompareJSON{Left: sides[0], Right: sides[1], OnlyInLeft: onlyLeft, OnlyInRight: onlyRight, Changes: changes}
			if payload.OnlyInLeft == nil {
				payload.OnlyInLeft = []string{}
			}
			if payload.OnlyInRight == nil {
				payload.OnlyInRight = []string{}
			}
			if payload.Changes == nil {
				payload.Changes = []runCompareChange{}
			}
			for _, change := range changes {
				switch change.Kind {
				case "regression":
					payload.Regressions++
				case "improvement":
					payload.Improvements++
				}
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), payload)
			}
			out := cmd.OutOrStdout()
			for _, side := range sides {
				_, _ = fmt.Fprintf(out, "%s (%s): %d cells, %d completed, %d passed, %d invalid evidence, %d tool declaration rejected, mean score %.3f\n", side.RunID, side.CampaignID, side.Cells, side.Completed, side.Passed, side.InvalidEvidence, side.ToolRejected, side.MeanScore)
			}
			_, _ = fmt.Fprintf(out, "Regressions: %d  Improvements: %d  Only in %s: %d  Only in %s: %d\n",
				payload.Regressions, payload.Improvements, args[0], len(onlyLeft), args[1], len(onlyRight))
			if len(changes) == 0 {
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "CELL\tKIND\tLEFT\tRIGHT")
			for _, change := range changes {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s %.3f\t%s %.3f\n", change.Cell, change.Kind, change.Left.Status, change.Left.Score, change.Right.Status, change.Right.Score)
			}
			return w.Flush()
		},
	}
}
