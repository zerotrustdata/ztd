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
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const rolloutSmokeCampaignSuffix = "-smoke"

type rolloutRunResult struct {
	Planned   int                 `json:"planned"`
	Succeeded int                 `json:"succeeded"`
	Failed    int                 `json:"failed"`
	Failures  []rolloutRunFailure `json:"failures,omitempty"`
	Runs      []rolloutRunSuccess `json:"runs,omitempty"`
}

type rolloutRunFailure struct {
	VariantID string `json:"variant_id"`
	Tag       string `json:"served_model_tag"`
	Error     string `json:"error"`
}

type rolloutRunSuccess struct {
	VariantID string `json:"variant_id"`
	Tag       string `json:"served_model_tag"`
	// CampaignID is the campaign the run executed under, which differs from the
	// queue entry's when the catalog changed after that campaign was frozen.
	CampaignID string `json:"campaign_id,omitempty"`
	RunID      string `json:"run_id"`
	Gate       string `json:"gate"`
}

const (
	rolloutGateSmoke = "smoke"
	rolloutGateFull  = "full"
)

type rolloutRunOptions struct {
	Until              int
	SkipVerified       bool
	DryRun             bool
	GateSmoke          bool
	PromoteOnPass      bool
	MirrorBootstrapURL string
}

func rolloutRunCmd(deps nativeEvalDeps) *cobra.Command {
	opts := rolloutRunOptions{SkipVerified: true}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Qualify queued models with strict-witness campaign runs",
		Long: `Qualify each selected queue entry: freeze a single-model campaign over the
built-in suite, run every model-role assignment through the governed Inference
and Data Operators, and verify the run with provider observation and model
provenance required. A verified run marks the entry verified; a failed entry is
marked failed and the batch continues. A run in which the provider failed every
assignment, as when it rejects tool declarations for the model, fails. Stopping
a run (Ctrl-C or 'ztd eval runs cancel') ends the batch and leaves its entry
pending.

--gate-smoke screens each model first with a fast smoke campaign
(<campaign>-smoke). A smoke pass does not verify the entry; add
--promote-on-pass to follow it with the full run.

Examples:
  ztd eval rollout run --until 1
  ztd eval rollout run --gate-smoke --promote-on-pass
  ztd eval rollout run --dry-run
  ztd eval rollout run --skip-verified=false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Until < 0 {
				return fmt.Errorf("evaluation: rollout run: --until must not be negative: %w", constants.ErrEvaluationFlagsInvalid)
			}
			if opts.PromoteOnPass && !opts.GateSmoke {
				return fmt.Errorf("evaluation: rollout run: --promote-on-pass requires --gate-smoke: %w", constants.ErrEvaluationFlagsInvalid)
			}
			return runRollout(cmd, deps, opts)
		},
	}
	cmd.Flags().IntVar(&opts.Until, "until", 0, "Run at most this many models (0 runs every pending model)")
	cmd.Flags().BoolVar(&opts.SkipVerified, "skip-verified", true, "Skip queue entries already verified on the current scenario catalog")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print the rollout plan without executing")
	cmd.Flags().BoolVar(&opts.GateSmoke, "gate-smoke", false, "Screen each model with the fast smoke campaign before any full run")
	cmd.Flags().BoolVar(&opts.PromoteOnPass, "promote-on-pass", false, "Run the full campaign for models that pass the smoke gate")
	cmd.Flags().StringVar(&opts.MirrorBootstrapURL, "mirror-bootstrap-url", "http://127.0.0.1:8082/bootstrap", "Preflight public mirror bootstrap URL")
	return cmd
}

// rolloutGateRunner runs one gate ("smoke" or "full") of one queue entry and
// returns the run it started. The run is non-nil whenever one started,
// including when it then failed.
type rolloutGateRunner func(cmd *cobra.Command, entry evaluation.CampaignQueueModel, gate string) (*rolloutRunSuccess, error)

// rolloutGateFactory builds the gate runner once the registry is loaded.
type rolloutGateFactory func(fileSvc fs.RuntimeFileService, registry []*evalv1.ModelVariant) rolloutGateRunner

// productionRolloutGates creates each gate's campaign from the entry's registry
// model and starts a run of it with strict witness verification.
func productionRolloutGates(deps nativeEvalDeps) rolloutGateFactory {
	return func(fileSvc fs.RuntimeFileService, registry []*evalv1.ModelVariant) rolloutGateRunner {
		return func(cmd *cobra.Command, entry evaluation.CampaignQueueModel, gate string) (*rolloutRunSuccess, error) {
			variant := findVariant(registry, entry.VariantID)
			if variant == nil {
				return nil, fmt.Errorf("model %q is not in the registry\nImport it with:\nztd eval models import %s\n%w", entry.VariantID, entry.ServedModelTag, constants.ErrInferenceModelNotFound)
			}
			return runRolloutGate(cmd, deps, fileSvc, variant, entry, gate)
		}
	}
}

func runRollout(cmd *cobra.Command, deps nativeEvalDeps, opts rolloutRunOptions) error {
	return runRolloutWith(cmd, deps, opts, productionRolloutGates(deps))
}

func runRolloutWith(cmd *cobra.Command, deps nativeEvalDeps, opts rolloutRunOptions, gates rolloutGateFactory) error {
	cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
	if err != nil {
		return err
	}
	queue, err := evaluation.LoadRolloutQueue(cmd.Context(), fileSvc)
	if err != nil {
		return fmt.Errorf("evaluation: rollout run: %w", err)
	}
	planRequest := evaluation.CampaignQueueBatchPlanRequest{SkipVerified: opts.SkipVerified, Until: opts.Until}
	if opts.SkipVerified {
		store := evaluation.NewStore(fileSvc)
		catalog, _, err := store.LoadSuiteCatalog(cmd.Context(), evaluation.DefaultSuiteID)
		if err != nil {
			return fmt.Errorf("evaluation: rollout run: %w", err)
		}
		stale, err := evaluation.StaleVerifiedVariants(cmd.Context(), store, queue, catalog.GetCatalogDigest())
		if err != nil {
			return fmt.Errorf("evaluation: rollout run: %w", err)
		}
		planRequest.VerifiedIsStale = func(entry evaluation.CampaignQueueModel) bool {
			_, isStale := stale[entry.VariantID]
			return isStale
		}
	}
	plan := queue.BuildBatchPlan(planRequest)
	if len(plan) == 0 {
		cmd.Println("No queue entries selected")
		return nil
	}
	if opts.DryRun {
		return writeRolloutRunPlan(cmd, plan, output.JSONEnabled(cmd))
	}
	if err := preflightRollout(cmd, opts.MirrorBootstrapURL); err != nil {
		return fmt.Errorf("evaluation: rollout run: %w", err)
	}
	registry, err := newModelInventories(fileSvc, cfg.ProjectRoot).variants(cmd.Context(), modelScopeRegistry)
	if err != nil {
		return fmt.Errorf("evaluation: rollout run: %w", err)
	}
	runGate := gates(fileSvc, registry)
	jsonOutput := output.JSONEnabled(cmd)
	result := rolloutRunResult{Planned: len(plan)}
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	// Structured output stays one document on stdout, so progress moves to stderr.
	progress := stdout
	if jsonOutput {
		progress = stderr
	}
	var stopErr error
	for _, entry := range plan {
		_, _ = fmt.Fprintf(progress, "=== START %s (%s) ===\n", entry.VariantID, entry.ServedModelTag)
		// Per-model output goes to the caller's streams only. It quotes grade
		// detail and model-derived text, so it is never written under the
		// runtime root.
		entryOut := stdout
		if jsonOutput {
			// Structured output stays a single document.
			entryOut = io.Discard
		}
		entryErr := stderr
		sub := *cmd
		sub.SetOut(entryOut)
		sub.SetErr(entryErr)

		runs, runErr := qualifyRolloutEntry(&sub, fileSvc, runGate, entry, opts)
		result.Runs = append(result.Runs, runs...)
		if errors.Is(runErr, constants.ErrEvaluationRunStopped) {
			// An operator stop is not a model outcome: the entry stays
			// pending and no later entry starts.
			_, _ = fmt.Fprintf(progress, "STOPPED %s (%s)\n", entry.VariantID, entry.ServedModelTag)
			stopErr = fmt.Errorf("evaluation: rollout run: %s: %w", entry.VariantID, runErr)
			break
		}
		if runErr != nil {
			result.Failed++
			result.Failures = append(result.Failures, rolloutRunFailure{VariantID: entry.VariantID, Tag: entry.ServedModelTag, Error: runErr.Error()})
			_, _ = fmt.Fprintf(entryErr, "Error: %v\n", runErr)
			_, _ = fmt.Fprintf(progress, "FAIL %s (%s)\n", entry.VariantID, entry.ServedModelTag)
			if markErr := markRolloutFailure(cmd.Context(), fileSvc, entry, runs, runErr); markErr != nil {
				_, _ = fmt.Fprintf(stderr, "warning: update queue entry %s: %v\n", entry.VariantID, markErr)
			}
			continue
		}
		result.Succeeded++
		if len(runs) > 0 {
			last := runs[len(runs)-1]
			_, _ = fmt.Fprintf(progress, "PASS %s %s -> %s\n", entry.VariantID, last.Gate, last.RunID)
		}
	}
	return errors.Join(stopErr, finishRolloutRun(cmd, result))
}

// qualifyRolloutEntry runs the gates the options ask for against one queue
// entry and records the outcome in the queue. It returns every run that
// completed, in order, even when a later gate failed.
func qualifyRolloutEntry(cmd *cobra.Command, fileSvc fs.RuntimeFileService, runGate rolloutGateRunner, entry evaluation.CampaignQueueModel, opts rolloutRunOptions) ([]rolloutRunSuccess, error) {
	var runs []rolloutRunSuccess
	if opts.GateSmoke {
		run, err := runGate(cmd, entry, rolloutGateSmoke)
		if run != nil {
			runs = append(runs, *run)
		}
		if err != nil {
			return runs, err
		}
		if !opts.PromoteOnPass {
			// A smoke pass screens the model. Only a full run verifies it.
			_, err := evaluation.MarkCampaignQueueEntry(evaluation.MarkCampaignQueueEntryRequest{
				Context:        cmd.Context(),
				FileService:    fileSvc,
				VariantID:      entry.VariantID,
				ServedModelTag: entry.ServedModelTag,
				Status:         evaluation.QueueStatusPending,
				Notes:          "smoke gate passed: run " + run.RunID,
			})
			return runs, err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "PROMOTING %s (%s) from the smoke gate to full qualification\n", entry.VariantID, entry.ServedModelTag)
	}
	run, err := runGate(cmd, entry, rolloutGateFull)
	if run != nil {
		runs = append(runs, *run)
	}
	if err != nil {
		return runs, err
	}
	_, err = evaluation.MarkCampaignQueueEntry(evaluation.MarkCampaignQueueEntryRequest{
		Context:        cmd.Context(),
		FileService:    fileSvc,
		VariantID:      entry.VariantID,
		ServedModelTag: entry.ServedModelTag,
		Status:         evaluation.QueueStatusVerified,
		VerifiedRunID:  run.RunID,
		Notes:          evaluation.StrictWitnessVerifyNotes(run.RunID),
	})
	return runs, err
}

// runRolloutGate creates the campaign for one gate of one model and starts a
// run of it with strict witness verification. The returned run is non-nil
// whenever a run was started, including when it then failed.
func runRolloutGate(cmd *cobra.Command, deps nativeEvalDeps, fileSvc fs.RuntimeFileService, variant *evalv1.ModelVariant, entry evaluation.CampaignQueueModel, gate string) (*rolloutRunSuccess, error) {
	campaignID := entry.CampaignID
	suiteID := evaluation.DefaultSuiteID
	if gate == rolloutGateSmoke {
		campaignID += rolloutSmokeCampaignSuffix
		suiteID = evaluation.SmokeSuiteID
	}
	created, err := createCampaign(cmd.Context(), deps, fileSvc, campaignCreateSpec{
		CampaignID:             campaignID,
		Variants:               []*evalv1.ModelVariant{variant},
		Repetitions:            1,
		SuiteID:                suiteID,
		VersionOnCatalogChange: true,
		Platform:               platformIdentity(cmd),
	})
	if err != nil {
		return nil, err
	}
	if frozenID := created.Spec.GetCampaignId(); frozenID != campaignID {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Catalog changed since campaign %s was frozen; qualifying under %s\n", campaignID, frozenID)
		campaignID = frozenID
	}
	result, err := runStartFlow(cmd, deps, runStartFlowOptions{
		CampaignID:         campaignID,
		Publish:            true,
		Daemon:             true,
		Verify:             true,
		RequireObservation: true,
		RequireProvenance:  true,
	})
	var run *rolloutRunSuccess
	if result != nil && result.RunID != "" {
		run = &rolloutRunSuccess{VariantID: entry.VariantID, Tag: entry.ServedModelTag, CampaignID: campaignID, RunID: result.RunID, Gate: gate}
	}
	if err == nil && run != nil {
		err = requireRunServed(cmd.Context(), fileSvc, gate, run.RunID)
	}
	return run, err
}

// requireRunServed fails a gate when the provider failed every assignment
// before the model answered, as when the provider rejects tool declarations
// for the model. Such a run verifies, since its evidence is intact, but it
// holds no inference, so it neither promotes nor verifies the model.
func requireRunServed(ctx context.Context, fileSvc fs.RuntimeFileService, gate, runID string) error {
	store := evaluation.NewStore(fileSvc)
	assignments, err := store.ListAssignments(ctx, runID)
	if err != nil {
		return fmt.Errorf("%s gate: run %s: %w", gate, runID, err)
	}
	results, err := store.LoadAssignmentResults(ctx, runID, assignments)
	if err != nil {
		return fmt.Errorf("%s gate: run %s: %w", gate, runID, err)
	}
	for _, result := range results {
		if result.GetLifecycleStatus() != evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED {
			return nil
		}
	}
	class, err := runProviderFailureClass(ctx, store, runID, assignments)
	if err != nil {
		return fmt.Errorf("%s gate: run %s: %w", gate, runID, err)
	}
	return &rolloutUnservedError{Gate: gate, RunID: runID, Class: class}
}

// rolloutUnservedError is a gate run the provider served no assignment of.
// Class is the inference attempt failure class most of its traces record
// (capability_unsupported for a model without tool support), or "" when none
// does.
type rolloutUnservedError struct {
	Gate  string
	RunID string
	Class string
}

func (e *rolloutUnservedError) Error() string {
	reason := "every assignment failed at the provider"
	if e.Class != "" {
		reason += " (" + e.Class + ")"
	}
	return fmt.Sprintf("%s gate: run %s: %s: %v", e.Gate, e.RunID, reason, constants.ErrEvaluationModelUnserved)
}

func (e *rolloutUnservedError) Unwrap() error { return constants.ErrEvaluationModelUnserved }

// runProviderFailureClass returns the attempt failure class most agent traces
// of a run record. An assignment whose executor failed before a trace was
// sealed has none and is skipped.
func runProviderFailureClass(ctx context.Context, store *evaluation.Store, runID string, assignments []*evalv1.EvaluationAssignment) (string, error) {
	counts := make(map[string]int)
	for _, assignment := range assignments {
		trace, err := store.LoadAssignmentAgentTrace(ctx, runID, assignment.GetAssignmentId())
		if errors.Is(err, constants.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		if trace.ProviderFailureClass != "" {
			counts[trace.ProviderFailureClass]++
		}
	}
	best := ""
	for class, count := range counts {
		if count > counts[best] || (count == counts[best] && class < best) {
			best = class
		}
	}
	return best, nil
}

func findVariant(variants []*evalv1.ModelVariant, variantID string) *evalv1.ModelVariant {
	for _, variant := range variants {
		if variant != nil && variant.GetVariantId() == variantID {
			return variant
		}
	}
	return nil
}

// markRolloutFailure records a failed entry, naming the run that failed when
// one started.
func markRolloutFailure(ctx context.Context, fileSvc fs.RuntimeFileService, entry evaluation.CampaignQueueModel, runs []rolloutRunSuccess, runErr error) error {
	// The queue is plaintext, so it records the failure class, never the error
	// text, which can echo model, provider, or target content.
	class := string(evaluation.ClassifyExecutionFailure(runErr))
	var unserved *rolloutUnservedError
	if errors.As(runErr, &unserved) && unserved.Class != "" {
		class += " (" + unserved.Class + ")"
	}
	notes := "failed: " + class
	runID := ""
	if len(runs) > 0 {
		runID = runs[len(runs)-1].RunID
		notes = fmt.Sprintf("run %s: failed: %s", runID, class)
	}
	if _, err := evaluation.MarkCampaignQueueEntry(evaluation.MarkCampaignQueueEntryRequest{
		Context:       ctx,
		FileService:   fileSvc,
		VariantID:     entry.VariantID,
		Status:        evaluation.QueueStatusFailed,
		VerifiedRunID: runID,
		Notes:         notes,
	}); err != nil {
		return fmt.Errorf("evaluation: update rollout queue: %w", err)
	}
	return nil
}

type rolloutRunPlanJSON struct {
	Models []evaluation.CampaignQueueModel `json:"models"`
}

func writeRolloutRunPlan(cmd *cobra.Command, plan []evaluation.CampaignQueueModel, jsonOutput bool) error {
	if jsonOutput {
		return output.WriteJSON(cmd.OutOrStdout(), rolloutRunPlanJSON{Models: plan})
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Rollout plan (%d model(s))\n", len(plan)); err != nil {
		return err
	}
	for _, entry := range plan {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "- %s (%s) status=%s\n", entry.VariantID, entry.ServedModelTag, entry.Status); err != nil {
			return err
		}
	}
	return nil
}

func finishRolloutRun(cmd *cobra.Command, result rolloutRunResult) error {
	if output.JSONEnabled(cmd) {
		if err := output.WriteJSON(cmd.OutOrStdout(), result); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Rollout finished: planned=%d succeeded=%d failed=%d\n", result.Planned, result.Succeeded, result.Failed)
		for _, failure := range result.Failures {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Failed: %s (%s): %s\n", failure.VariantID, failure.Tag, failure.Error)
		}
	}
	if result.Failed > 0 {
		return fmt.Errorf("evaluation: rollout run: %d model(s) failed", result.Failed)
	}
	return nil
}

func preflightRollout(cmd *cobra.Command, mirrorBootstrapURL string) error {
	if err := checkHTTPReachable(cmd.Context(), mirrorBootstrapURL); err != nil {
		return fmt.Errorf("preflight mirror bootstrap: %w", err)
	}
	if !output.JSONEnabled(cmd) {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Preflight ok (mirror reachable)")
	}
	return nil
}

func checkHTTPReachable(ctx context.Context, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("evaluation: check HTTP reachable: %w", constants.ErrEndpointRequired)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	return nil
}
