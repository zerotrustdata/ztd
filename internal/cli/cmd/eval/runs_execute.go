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
	"log/slog"
	"slices"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	assignmentTimeout      = 8 * time.Minute
	campaignAgentPersonaID = "ztd-eval-campaign"
)

type runExecuteOptions struct {
	RunID        string
	Publish      bool
	Daemon       bool
	Limit        uint32
	NoAutoBind   bool
	JSONOutput   bool
	ResultOutput func(*evalv1.EvaluationAssignmentResult)
}

// executeRun executes queued assignments of one run while holding the run's
// lease. The lease is the durable handle that lets status, cancel, and archive
// tell a live run from an interrupted one. Command output goes to the caller's
// streams only: it quotes grade detail and model-derived text, so it is never
// written to a file under the runtime root.
func executeRun(cmd *cobra.Command, deps nativeEvalDeps, opts runExecuteOptions) (executed int, runErr error) {
	cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
	if err != nil {
		return 0, err
	}
	if err := evaluation.RejectArchivedRun(cmd.Context(), fileSvc, opts.RunID); err != nil {
		return 0, err
	}
	store := evaluation.NewStore(fileSvc)
	// The lease is written under the run's directory, so a run that does not
	// exist must fail before one is acquired.
	run, err := store.LoadRun(cmd.Context(), opts.RunID)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	switch run.GetLane() {
	case evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE:
	case evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM:
	default:
		return 0, fmt.Errorf("evaluation: run execute: lane %s: %w", run.GetLane(), constants.ErrEvaluationTargetKindMismatch)
	}
	control, err := deps.runControl.process(fileSvc)
	if err != nil {
		return 0, err
	}
	host, err := control.Hostname()
	if err != nil {
		return 0, fmt.Errorf("evaluation: run lease: read hostname: %w", err)
	}
	startedAt := deps.now().UTC()
	stale, err := store.AcquireRunLease(cmd.Context(), evaluation.RunLease{
		RunID:     opts.RunID,
		PID:       control.PID(),
		Host:      host,
		StartedAt: startedAt,
	}, leaseLiveness(control))
	if err != nil {
		return 0, err
	}
	var (
		// lifecycle is set once the ztd-eval identity and the CLI session are
		// resolved. A run that fails before then has nothing to report.
		lifecycle   *runLifecycle
		publication *evaluation.CampaignPublicationCoordinator
		watcher     *runStopWatcher
	)
	defer func() {
		if releaseErr := store.ReleaseRunLease(context.WithoutCancel(cmd.Context()), opts.RunID, control.PID()); releaseErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("evaluation: release run lease: %w", releaseErr))
			return
		}
		if lifecycle != nil {
			if err := lifecycle.reportTerminal(context.WithoutCancel(cmd.Context()), watcher.Requested()); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
		if publication != nil {
			publishCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), constants.DefaultHTTPTimeout)
			defer cancel()
			_, publishErr := publication.PublishRunAggregates(publishCtx, opts.RunID, deps.now().UTC())
			if publishErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("evaluation: publish released run status: %w", publishErr))
			}
		}
	}()
	if stale != nil && !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Cleared stale lease held by process %d on %s\n", stale.PID, stale.Host)
	}
	if deps.runControl.onLeaseAcquired != nil {
		deps.runControl.onLeaseAcquired()
	}

	authContext, err := deps.authLoader(fileSvc, cfg)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: load CLI identity: %w", err)
	}
	appClient, err := inferenceEvalAppClient(cmd.Context(), cmd.ErrOrStderr(), deps, fileSvc, cfg, authContext)
	if err != nil {
		return 0, err
	}
	lifecycle = &runLifecycle{
		client:       appClient,
		live:         leaseLiveness(control),
		deps:         deps,
		store:        store,
		cliSessionID: authContext.CLISessionID,
		runID:        opts.RunID,
		startedAt:    startedAt,
	}

	signals, stopSignals := deps.runControl.interrupts()
	defer stopSignals()
	parent := cmd.Context()
	hardCtx, hardCancel := context.WithCancel(parent)
	defer hardCancel()
	cmd.SetContext(hardCtx)
	defer cmd.SetContext(parent)
	// The watcher exists before the stream subscribes, so the stream's connect
	// reconcile can mark it requested.
	events := make(chan runEvent)
	watcher = startRunStopWatcher(hardCtx, events, signals, hardCancel, deps.runControl.onSignalHandled, deps.runControl.onStopRequested)
	defer watcher.Stop()
	httpClient, err := auth.BuildMTLSClient(fileSvc, cfg, 0)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: build stream client: %w", err)
	}
	stream, err := subscribeRunEvents(hardCtx, httpClient, cfg.OperatorPublicURL(), authContext.CLISessionID, opts.RunID, events, func() {
		reconcileStopRequest(hardCtx, store, opts.RunID, watcher)
	})
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	defer stream.Close()
	// A stream that stops on its own can no longer deliver a stop request, so the
	// run stops before its next assignment.
	go func() {
		<-stream.Done()
		if err := stream.Err(); err != nil {
			slog.Default().Warn("evaluation: run execute: run event stream stopped; stopping before the next assignment", "run_id", opts.RunID, "error", err)
			watcher.requestStop()
		}
	}()
	if err := lifecycle.reportRunning(cmd.Context()); err != nil {
		return 0, err
	}

	if opts.Publish {
		publication, err = NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
		if err != nil {
			return 0, err
		}
		publication.WithLeaseLiveness(leaseLiveness(control))
		if _, err := publication.PublishRunAggregates(cmd.Context(), opts.RunID, deps.now().UTC()); err != nil {
			return 0, fmt.Errorf("evaluation: publish acquired run status: %w", err)
		}
	}

	return executeAssignments(cmd, deps, opts, watcher, publication)
}

func executeAssignments(cmd *cobra.Command, deps nativeEvalDeps, opts runExecuteOptions, watcher *runStopWatcher, publication *evaluation.CampaignPublicationCoordinator) (executed int, runErr error) {
	if opts.Daemon {
		opts.Limit = ^uint32(0)
	} else if opts.Limit == 0 {
		opts.Limit = 1
	}
	gatewayDeps := deps.gatewayDeps()
	cfg, fileSvc, authContext, err := gatewayEvalEnvironment(cmd, gatewayDeps)
	if err != nil {
		return 0, err
	}
	store := evaluation.NewStore(fileSvc)
	controllerFactory := func(executor evaluation.CampaignAssignmentExecutor) *evaluation.CampaignController {
		return evaluation.NewCampaignController(store, executor, deps.now, adaptNewID(deps.newID))
	}
	summary, err := controllerFactory(nil).RunSummary(cmd.Context(), opts.RunID)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	strictObservation, strictProvenance, err := resolveRunWitnessPolicies(summary.Run, false, false)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute witness policies: %w", err)
	}
	binding := summary.Run.GetCampaignBinding()
	if binding == nil {
		return 0, fmt.Errorf("evaluation: run execute: campaign binding: %w", constants.ErrMissingRequiredField)
	}
	spec, err := store.LoadCampaignSpec(cmd.Context(), binding.GetCampaignId())
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	if err := evaluation.ValidateCampaignSpecDigest(spec); err != nil {
		return 0, fmt.Errorf("evaluation: run execute: campaign spec: %w", err)
	}
	if summary.Run.GetLane() == evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM {
		stackSet, err := store.LoadHeterogeneousStackSet(cmd.Context(), binding.GetCampaignId())
		if err != nil {
			return 0, fmt.Errorf("evaluation: run execute: frozen formations: %w", err)
		}
		if err := evaluation.ValidateFormationStackSetBinding(stackSet, binding.GetCampaignId(), spec.GetModelRegistry()); err != nil {
			return 0, fmt.Errorf("evaluation: run execute: frozen formations: %w", err)
		}
	}
	investigation := spec.GetInvestigation()
	if err := evaluation.ValidateCampaignInvestigation(investigation); err != nil {
		return 0, fmt.Errorf("evaluation: run execute: campaign investigation: %w", err)
	}
	if err := evalPreflightGatewayPosture(cmd, gatewayDeps, cfg, authContext, spec); err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	operators, err := evalListOperators(cmd, gatewayDeps, cfg, authContext)
	if err != nil {
		return 0, err
	}
	topology, err := evaluation.ResolveCampaignOperatorTopologyForDataOperator(operators, "", investigation.GetDataOperatorId())
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	// A model drives every non-platform lane's Data Operator actions, shell
	// included, so the operator must run in a disposable directory, and every
	// governed receipt is verified against its runtime's enrollment-bound key.
	// The workspace is checked first because its refusal is the actionable
	// error. A platform run reads no receipts, so it needs no anchors.
	if summary.Run.GetLane() != evalv1.EvaluationLane_EVALUATION_LANE_PLATFORM {
		if err := evaluation.ValidateAgentDataOperator(topology.DataWorkingDirectory, cfg.ProjectRoot); err != nil {
			return 0, fmt.Errorf("evaluation: run execute: %w", err)
		}
		if topology, err = topology.WithAnchors(operators); err != nil {
			return 0, fmt.Errorf("evaluation: run execute: %w", err)
		}
	}
	authContext, err = evalBindDataOperator(cmd, gatewayDeps, cfg, fileSvc, authContext, operators, topology.Data.OperatorSessionID, !opts.NoAutoBind)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	catalog, err := store.LoadScenarioCatalog(cmd.Context(), binding.GetCampaignId())
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	artifacts, err := store.LoadScenarioArtifacts(cmd.Context(), binding.GetCampaignId(), catalog)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	executor, err := deps.agentExecutorFactory(campaignAgentExecutorInputs{
		Cmd:         cmd,
		Deps:        deps,
		Config:      cfg,
		FileSvc:     fileSvc,
		AuthContext: authContext,
		Topology:    topology,
		Store:       store,
	})
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	controller := controllerFactory(executor)
	if publication != nil {
		controller = controller.WithPublication(publication)
	}
	executionBinding := evaluation.CampaignExecutionBinding{
		Topology:            topology,
		ModelRegistryDigest: spec.GetModelRegistryDigest(),
		ModelRegistry:       evaluation.InferenceVariantsFromEvalRegistry(spec.GetModelRegistry()),
		ModelVariants:       spec.GetModelRegistry(),
	}
	if err := gwremote.PreflightProviderObservationDelivery(fileSvc, cfg); err != nil {
		return executed, fmt.Errorf("evaluation: run execute: %w", err)
	}
	witness, err := gwremote.NewCampaignWitnessCapture(fileSvc, store, cfg)
	if err != nil {
		return executed, fmt.Errorf("evaluation: run execute: witness capture: %w", err)
	}
	if strictObservation {
		defer func() {
			if runErr == nil {
				return
			}
			cleanupErr := releaseRunModels(cmd, deps, opts, cfg, authContext, operators, topology.Inference.OperatorSessionID, spec)
			if cleanupErr == nil {
				return
			}
			runErr = errors.Join(runErr, fmt.Errorf("evaluation: run execute: release provider models: %w", cleanupErr))
		}()
		endpoint, err := resolveCampaignOllamaEndpoint(operators, topology.Inference.OperatorSessionID)
		if err != nil {
			return executed, fmt.Errorf("evaluation: run execute: %w", err)
		}
		if err := releaseResidentProviderModels(cmd, deps, opts, cfg, authContext, topology.Inference.OperatorSessionID, endpoint); err != nil {
			return executed, fmt.Errorf("evaluation: run execute: %w", err)
		}
	}
	iterations := int64(opts.Limit)
	stopped := false
	for i := int64(0); i < iterations; i++ {
		if watcher.Requested() {
			stopped = true
			break
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), assignmentTimeout)
		result, ok, err := controller.ExecuteNextAssignment(ctx, opts.RunID, executionBinding, artifacts)
		cancel()
		if err != nil {
			return executed, fmt.Errorf("evaluation: run execute: %w", err)
		}
		if !ok {
			break
		}
		executed++
		if err := witness.CaptureAssignmentEvidence(cmd.Context(), result); err != nil {
			return executed, fmt.Errorf("evaluation: run execute: capture witness evidence: %w", err)
		}
		if opts.ResultOutput != nil {
			opts.ResultOutput(result)
		}
		if !opts.JSONOutput {
			executedAssignment, err := store.LoadAssignment(cmd.Context(), opts.RunID, result.GetAssignmentId())
			if err != nil {
				return executed, fmt.Errorf("evaluation: run execute: load executed assignment: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), executedAssignmentLine(executedAssignment, result))
		}
	}

	summary, err = controller.RunSummary(cmd.Context(), opts.RunID)
	if err != nil {
		return executed, fmt.Errorf("evaluation: run execute: %w", err)
	}
	// A stop is not completion: a caller that verifies or promotes on success
	// would otherwise accept the executed subset as the run.
	if stopped && summary.QueuedCount > 0 {
		return executed, fmt.Errorf("evaluation: run execute: run %s stopped after %d assignment(s) with %d queued: %w", opts.RunID, executed, summary.QueuedCount, constants.ErrEvaluationRunStopped)
	}
	if summary.QueuedCount == 0 && summary.RunningCount == 0 {
		// Windows recorded after their assignment completed are sealed here, while
		// the run's models are still loaded. A strict run then waits, once, for the
		// windows its scored attempts have not delivered yet.
		if err := witness.CaptureRunProviderObservationEvidence(cmd.Context(), opts.RunID); err != nil {
			return executed, fmt.Errorf("evaluation: run execute: %w", err)
		}
		if err := witness.CaptureRunModelProvenanceEvidence(cmd.Context(), opts.RunID); err != nil {
			return executed, fmt.Errorf("evaluation: run execute: %w", err)
		}
		if strictProvenance {
			if err := waitForRunEvidence(cmd, opts.RunID, "model provenance", constants.ModelProvenanceCompletionTimeout, opts.JSONOutput, func(ctx context.Context) error {
				return witness.WaitForRunModelProvenanceEvidence(ctx, opts.RunID)
			}); err != nil {
				return executed, fmt.Errorf("evaluation: run execute: %w", err)
			}
		}
		if strictObservation {
			if err := waitForRunEvidence(cmd, opts.RunID, "provider observation", constants.ProviderObservationCompletionTimeout, opts.JSONOutput, func(ctx context.Context) error {
				return witness.WaitForRunProviderObservationEvidence(ctx, opts.RunID)
			}); err != nil {
				return executed, fmt.Errorf("evaluation: run execute: %w", err)
			}
		}
		if err := releaseRunModels(cmd, deps, opts, cfg, authContext, operators, topology.Inference.OperatorSessionID, spec); err != nil {
			return executed, fmt.Errorf("evaluation: run execute: %w", err)
		}
		if publication != nil {
			completionCount, publishErr := publication.PublishRunCompletion(cmd.Context(), opts.RunID, deps.now().UTC())
			if publishErr != nil {
				return executed, fmt.Errorf("evaluation: run execute: %w", publishErr)
			}
			if completionCount > 0 && !opts.JSONOutput {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Published %d completion projection record(s) for run %s\n", completionCount, opts.RunID)
			}
		}
	}
	return executed, nil
}

// waitForRunEvidence holds a strict run's completion until the Gateway records the
// run's windows of one kind, event-driven through the capture. A window that does
// not arrive by the deadline is a warning, not a failure: the run completes, and
// verification fails closed on the missing window.
func waitForRunEvidence(cmd *cobra.Command, runID, kind string, timeout time.Duration, jsonOutput bool, wait func(context.Context) error) error {
	if !jsonOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Waiting for %s completion (up to %s)\n", kind, timeout)
	}
	waitCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	err := wait(waitCtx)
	if err == nil {
		return nil
	}
	if cmd.Context().Err() == nil && errors.Is(err, constants.ErrEvaluationObservationUnavailable) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: run %s: %v\n", runID, err)
		return nil
	}
	return err
}

// releaseResidentProviderModels unloads every model resident on the provider
// before scored work, so a run never starts against a warm, foreign model.
func releaseResidentProviderModels(cmd *cobra.Command, deps nativeEvalDeps, opts runExecuteOptions, cfg *config.Config, authContext *auth.ClientAuthContext, inferenceSessionID, endpoint string) error {
	return releaseProviderModels(cmd, deps, opts, cfg, authContext, inferenceSessionID, endpoint, "resident", nil)
}

// releaseRunModels unloads the campaign's own models once the run is done.
func releaseRunModels(cmd *cobra.Command, deps nativeEvalDeps, opts runExecuteOptions, cfg *config.Config, authContext *auth.ClientAuthContext, operators []*operatorv1.OperatorDocument, inferenceSessionID string, spec *evalv1.EvaluationCampaignSpec) error {
	if inferenceSessionID == "" || spec == nil || len(spec.GetModelRegistry()) == 0 {
		return nil
	}
	endpoint, err := resolveCampaignOllamaEndpoint(operators, inferenceSessionID)
	if err != nil {
		return err
	}
	modelTags := make([]string, 0, len(spec.GetModelRegistry()))
	for _, variant := range spec.GetModelRegistry() {
		if variant != nil && variant.GetServedModelTag() != "" {
			modelTags = append(modelTags, variant.GetServedModelTag())
		}
	}
	if len(modelTags) == 0 {
		return nil
	}
	return releaseProviderModels(cmd, deps, opts, cfg, authContext, inferenceSessionID, endpoint, "campaign", modelTags)
}

// releaseProviderModels releases the resident provider models, limited to
// only when it is non-nil, through the exact Inference Operator session and
// waits until the provider reports them absent.
func releaseProviderModels(cmd *cobra.Command, deps nativeEvalDeps, opts runExecuteOptions, cfg *config.Config, authContext *auth.ClientAuthContext, inferenceSessionID, endpoint, label string, only []string) error {
	dispatcher, err := newHarnessOllamaModelCommandDispatcher(cfg, authContext, deps.gatewayDeps())
	if err != nil {
		return err
	}
	maintenance := evaluation.OllamaModelMaintenanceContext{
		TargetOperatorSessionID: inferenceSessionID,
		Environment:             modelCommandEnvironment(endpoint),
		CaseID:                  opts.RunID,
		NewID:                   adaptNewID(deps.newID),
	}
	residency, err := evaluation.ReadOllamaProviderResidency(cmd.Context(), dispatcher, maintenance)
	if err != nil {
		return fmt.Errorf("evaluation: read provider residency: %w", err)
	}
	residentTags := make([]string, 0, len(residency.Models))
	for _, resident := range residency.Models {
		if only == nil || slices.Contains(only, resident.Name) {
			residentTags = append(residentTags, resident.Name)
		}
	}
	if len(residentTags) == 0 {
		return nil
	}
	if err := evaluation.ReleaseOllamaModels(cmd.Context(), dispatcher, inferenceSessionID, opts.RunID, residentTags, maintenance.Environment, maintenance.NewID); err != nil {
		return err
	}
	if err := evaluation.VerifyOllamaModelsAbsent(cmd.Context(), dispatcher, maintenance, residentTags); err != nil {
		return err
	}
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Released %d %s model(s) through inference session %s\n", len(residentTags), label, inferenceSessionID)
	}
	return nil
}

// campaignAgentExecutorInputs is everything a run resolves before it builds the
// executor that runs each model-role assignment.
type campaignAgentExecutorInputs struct {
	Cmd         *cobra.Command
	Deps        nativeEvalDeps
	Config      *config.Config
	FileSvc     fs.RuntimeFileService
	AuthContext *auth.ClientAuthContext
	Topology    evaluation.CampaignOperatorTopology
	Store       *evaluation.Store
}

// campaignAgentExecutorFactory builds the executor campaign execution runs
// every model-role assignment through.
type campaignAgentExecutorFactory func(campaignAgentExecutorInputs) (evaluation.CampaignAssignmentExecutor, error)

// newProductionCampaignAgentExecutor runs a model-role assignment as a
// tool-calling agent: each turn is dispatched to the Inference Operator under
// the ztd-eval application identity, and each tool call is a governed Data
// Operator action under the bound CLI session.
func newProductionCampaignAgentExecutor(in campaignAgentExecutorInputs) (evaluation.CampaignAssignmentExecutor, error) {
	appClient, err := inferenceEvalAppClient(in.Cmd.Context(), in.Cmd.ErrOrStderr(), in.Deps, in.FileSvc, in.Config, in.AuthContext)
	if err != nil {
		return nil, err
	}
	gatewayClient, err := in.Deps.clientFactory(nativeEvalClientConfig(in.Config, in.AuthContext))
	if err != nil {
		return nil, fmt.Errorf("evaluation: initialize gateway client: %w", err)
	}
	persona := harnessclient.Persona{ID: campaignAgentPersonaID, CLISessionID: in.AuthContext.CLISessionID, UserID: in.AuthContext.UserID}
	sentinel, err := drive.NewSentinel(gatewayClient, persona, map[cases.Executor]cliop.Target{
		cases.ExecutorOutbound: {OperatorID: in.Topology.Data.OperatorID, OperatorSessionID: in.Topology.Data.OperatorSessionID},
	})
	if err != nil {
		return nil, fmt.Errorf("evaluation: initialize ledger sentinel: %w", err)
	}
	return evaluation.NewCampaignAgentExecutor(&governedInferenceDispatcher{client: appClient, cliSessionID: persona.CLISessionID}, evaluation.NewCommandLane(gatewayClient, persona), sentinel, in.Store, in.Deps.now, adaptNewID(in.Deps.newID))
}

func resolveCampaignOllamaEndpoint(operators []*operatorv1.OperatorDocument, inferenceSessionID string) (string, error) {
	endpoint, err := evaluation.GovernedInferenceOllamaEndpoint(operators, inferenceSessionID)
	if err != nil {
		return "", fmt.Errorf("evaluation: run execute: %w", err)
	}
	return endpoint, nil
}

// reconcileStopRequest reads the run's lease and marks the watcher requested when
// a cancel is recorded there. The stream runs it on every connection, so a stop
// sent while the stream was down is still seen. A lease that cannot be read may
// hold such a stop, so the watcher is marked requested then too.
func reconcileStopRequest(ctx context.Context, store *evaluation.Store, runID string, watcher *runStopWatcher) {
	lease, err := store.LoadRunLease(ctx, runID)
	if err != nil {
		if !isMissingRecord(err) {
			slog.Default().Warn("evaluation: run execute: read run lease on stream connect; stopping before the next assignment", "run_id", runID, "error", err)
			watcher.requestStop()
		}
		return
	}
	if lease.CancelRequested() {
		watcher.requestStop()
	}
}

// runStateReporter is the Gateway run projection a run reports its lifecycle to.
// The harness client is the production reporter.
type runStateReporter interface {
	UpdateRunState(ctx context.Context, req models.ObserveProducerRunStateRequest) error
}

// runLifecycle reports one execution's lifecycle to the Gateway run projection
// under the ztd-eval identity, bound to the owner's CLI session. The local lease
// and run store are authoritative: the projection mirrors the status runs show
// reports for the run, and a rejected or unreachable update is returned to the
// command, so the run never continues or ends behind a projection it failed to
// write.
type runLifecycle struct {
	client       runStateReporter
	live         evaluation.LeaseLiveness
	deps         nativeEvalDeps
	store        *evaluation.Store
	cliSessionID string
	runID        string
	startedAt    time.Time
	// running is set once RUNNING is attempted. The Gateway's state after an
	// attempt is unknown, so the release reports even when the attempt failed.
	running bool
}

// reportRunning posts RUNNING once the lease is held and the event stream is
// subscribed. A run with nothing queued or running executes nothing, so it posts
// no RUNNING and its release posts nothing either.
func (l *runLifecycle) reportRunning(ctx context.Context) error {
	row, err := l.snapshot(ctx)
	if err != nil {
		return err
	}
	if row.Queued == 0 && row.Running == 0 {
		return nil
	}
	l.running = true
	return l.post(ctx, models.RunLifecycleStatusRunning, row, nil)
}

// reportTerminal posts the status a released run ends in. It must run after the
// lease is released, because a cancel waiting on the Gateway returns on this
// status and then reads the released run. A release that executed nothing posts
// nothing, so a finished run resumed again makes no transition, and a
// verification verdict recorded after the run settled is not projected. A stop
// request always posts, because cancel waiters return on its cancelled status.
func (l *runLifecycle) reportTerminal(ctx context.Context, stopRequested bool) error {
	if !l.running && !stopRequested {
		return nil
	}
	row, err := l.snapshot(ctx)
	if err != nil {
		return err
	}
	endedAt := l.deps.now().UTC()
	return l.post(ctx, releasedRunStatus(stopRequested, row), row, &endedAt)
}

// releasedRunStatus is the projected status of a released run, read from the row
// runs show renders for it. A run asked to stop is cancelled. A run that still
// has queued or running assignments is waiting for its next resume, whatever the
// release reported. Otherwise the run's own classification decides: verified or
// completed runs complete, a run whose verification failed fails, and a run the
// classification does not settle waits.
func releasedRunStatus(stopRequested bool, row *runRow) models.RunLifecycleStatus {
	switch {
	case stopRequested, row.Status == "cancelled":
		return models.RunLifecycleStatusCancelled
	case row.Status == "running":
		return models.RunLifecycleStatusRunning
	case row.Queued > 0 || row.Running > 0:
		return models.RunLifecycleStatusWaiting
	}
	switch row.Status {
	case "verified", "completed":
		return models.RunLifecycleStatusCompleted
	case "verify_failed":
		return models.RunLifecycleStatusFailed
	default:
		return models.RunLifecycleStatusWaiting
	}
}

// snapshot reads the run the way runs show reads it, so the projection and the
// command agree on what the run is. The lifecycle only runs for live, unarchived
// runs, so the archived flag is false.
func (l *runLifecycle) snapshot(ctx context.Context) (*runRow, error) {
	row, _, err := summarizeRun(ctx, l.store, false, l.runID, l.live, l.deps.now, adaptNewID(l.deps.newID))
	if err != nil {
		return nil, fmt.Errorf("evaluation: run lifecycle: %w", err)
	}
	return row, nil
}

func (l *runLifecycle) post(ctx context.Context, status models.RunLifecycleStatus, row *runRow, endedAt *time.Time) error {
	startedAt := l.startedAt
	err := l.client.UpdateRunState(ctx, models.ObserveProducerRunStateRequest{
		SchemaVersion:  constants.ObserveEventPayloadSchemaVersion,
		RunID:          l.runID,
		RunKind:        models.RunKindEval,
		DisplayName:    l.runID,
		Status:         status,
		CompletedTasks: int(row.Terminal),
		TotalTasks:     int(row.ExpectedAssignments),
		StartedAt:      &startedAt,
		EndedAt:        endedAt,
		ObservedAt:     l.deps.now().UTC(),
		CLISessionID:   l.cliSessionID,
	})
	if err != nil {
		return fmt.Errorf("evaluation: report run %s status %s: %w", l.runID, status, err)
	}
	return nil
}
