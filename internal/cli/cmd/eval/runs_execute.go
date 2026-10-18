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
	"slices"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
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
	RunID                    string
	Publish                  bool
	Daemon                   bool
	Limit                    uint32
	EnforceProviderResidency bool
	NoAutoBind               bool
	JSONOutput               bool
	ResultOutput             func(*evalv1.EvaluationAssignmentResult)
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
		return 0, fmt.Errorf("evaluation: run execute: %w", constants.ErrEvaluationSystemLaneUnavailable)
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
	var publication *evaluation.CampaignPublicationCoordinator
	defer func() {
		if releaseErr := store.ReleaseRunLease(context.WithoutCancel(cmd.Context()), opts.RunID, control.PID()); releaseErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("evaluation: release run lease: %w", releaseErr))
			return
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

	signals, stopSignals := deps.runControl.interrupts()
	defer stopSignals()
	parent := cmd.Context()
	hardCtx, hardCancel := context.WithCancel(parent)
	defer hardCancel()
	cmd.SetContext(hardCtx)
	defer cmd.SetContext(parent)
	watcher := startRunStopWatcher(hardCtx, store, opts.RunID, deps.runControl.interval(), signals, hardCancel, deps.runControl.onSignalHandled)
	defer watcher.Stop()

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
	binding := summary.Run.GetCampaignBinding()
	if binding == nil {
		return 0, fmt.Errorf("evaluation: run execute: campaign binding: %w", constants.ErrMissingRequiredField)
	}
	spec, err := store.LoadCampaignSpec(cmd.Context(), binding.GetCampaignId())
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	investigation := spec.GetInvestigation()
	if err := evaluation.ValidateCampaignInvestigation(investigation); err != nil {
		return 0, fmt.Errorf("evaluation: run execute: campaign investigation: %w", err)
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
	}
	if err := gwremote.PreflightProviderObservationDelivery(fileSvc, cfg); err != nil {
		return executed, fmt.Errorf("evaluation: run execute: %w", err)
	}
	if opts.EnforceProviderResidency {
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
	waitCtx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	if err := evaluation.WaitForOllamaModelsAbsent(waitCtx, dispatcher, maintenance, residentTags); err != nil {
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
	return evaluation.NewCampaignAgentExecutor(&governedInferenceDispatcher{client: appClient}, evaluation.NewCommandLane(gatewayClient, persona), sentinel, in.Store, in.Deps.now, adaptNewID(in.Deps.newID))
}

func resolveCampaignOllamaEndpoint(operators []*operatorv1.OperatorDocument, inferenceSessionID string) (string, error) {
	endpoint, err := evaluation.GovernedInferenceOllamaEndpoint(operators, inferenceSessionID)
	if err != nil {
		return "", fmt.Errorf("evaluation: run execute: %w", err)
	}
	return endpoint, nil
}
