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
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
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
	_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
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
	if run.GetLane() != evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM && run.GetLane() != evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE {
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
	defer func() {
		if releaseErr := store.ReleaseRunLease(context.WithoutCancel(cmd.Context()), opts.RunID, control.PID()); releaseErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("evaluation: release run lease: %w", releaseErr))
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
	watcher := startRunStopWatcher(hardCtx, store, opts.RunID, deps.runControl.interval(), signals, hardCancel)
	defer watcher.Stop()

	return executeAssignments(cmd, deps, opts, watcher)
}

func executeAssignments(cmd *cobra.Command, deps nativeEvalDeps, opts runExecuteOptions, watcher *runStopWatcher) (executed int, runErr error) {
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
	operators, err := evalListOperators(cmd, gatewayDeps, cfg, authContext)
	if err != nil {
		return 0, err
	}
	sessions, err := resolveOperatorSessionsFrom(operators, constants.OperatorCapabilityInference, constants.OperatorCapabilityData)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	authContext, err = evalBindDataOperator(cmd, gatewayDeps, cfg, fileSvc, authContext, operators, sessions.DataSessionID, !opts.NoAutoBind)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	selected, err := evaluation.SelectInferenceOperator(operators, sessions.InferenceSessionID)
	if err != nil {
		return 0, err
	}
	dataOperator, err := operatorcapability.SelectDataOperator(operators)
	if err != nil {
		return 0, fmt.Errorf("evaluation: run execute: %w", err)
	}
	spec, err := store.LoadCampaignSpec(cmd.Context(), binding.GetCampaignId())
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
	newID := adaptNewID(deps.newID)
	buildFormation := func() (evaluation.CampaignAssignmentExecutor, error) {
		runner, err := deps.formationRunnerFactory(campaignFormationRunnerInputs{
			Deps:        deps,
			Config:      cfg,
			FileSvc:     fileSvc,
			AuthContext: authContext,
			Operators:   operators,
			Spec:        spec,
			Options: campaignFormationProductionOptions{
				InferenceSessionID: selected.OperatorSessionID,
				DataSessionID:      dataOperator.OperatorSessionID,
			},
		})
		if err != nil {
			return nil, err
		}
		observationReader, err := gwremote.NewCampaignProviderObservationReader(fileSvc, cfg)
		if err != nil {
			return nil, err
		}
		provenanceReader, err := gwremote.NewCampaignModelProvenanceReader(fileSvc, cfg)
		if err != nil {
			return nil, err
		}
		return evaluation.NewCampaignFormationExecutorWithWitness(
			spec.GetModelRegistry(),
			runner,
			store,
			evaluation.NewCampaignFormationWitnessReader(observationReader, provenanceReader),
			deps.now,
			newID,
		), nil
	}
	// A model-role assignment runs as a tool-calling agent: each turn is
	// dispatched to the Inference Operator under the ztd-eval application
	// identity, and each tool call is a governed Data Operator action under
	// the bound CLI session.
	buildAgent := func() (evaluation.CampaignAssignmentExecutor, error) {
		appClient, err := inferenceEvalAppClient(fileSvc, cfg, authContext, deps.clientFactory)
		if err != nil {
			return nil, err
		}
		gatewayClient, err := deps.clientFactory(nativeEvalClientConfig(cfg, authContext))
		if err != nil {
			return nil, fmt.Errorf("evaluation: initialize gateway client: %w", err)
		}
		lane := evaluation.NewCommandLane(gatewayClient, store, harnessclient.Persona{ID: campaignAgentPersonaID, CLISessionID: authContext.CLISessionID, UserID: authContext.UserID}, 0, 0)
		return evaluation.NewCampaignAgentExecutor(&harnessFormationInferenceDispatcher{client: appClient}, lane, store, deps.now, newID)
	}
	controller := controllerFactory(evaluation.NewCampaignAssignmentRouter(buildFormation, buildAgent))
	var publication *evaluation.CampaignPublicationCoordinator
	if opts.Publish {
		publication, err = NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
		if err != nil {
			return 0, fmt.Errorf("evaluation: run execute: %w", err)
		}
		controller = controller.WithPublication(publication)
	}
	executionBinding := evaluation.CampaignExecutionBinding{
		InferenceOperatorSessionID:   selected.OperatorSessionID,
		DataOperatorID:               dataOperator.OperatorID,
		DataOperatorSessionID:        dataOperator.OperatorSessionID,
		DataOperatorWorkingDirectory: dataOperator.WorkingDirectory,
		ModelRegistryDigest:          spec.GetModelRegistryDigest(),
		ModelRegistry:                evaluation.InferenceVariantsFromEvalRegistry(spec.GetModelRegistry()),
	}
	if err := gwremote.PreflightProviderObservationDelivery(fileSvc, cfg); err != nil {
		return executed, fmt.Errorf("evaluation: run execute: %w", err)
	}
	if opts.EnforceProviderResidency {
		defer func() {
			if runErr == nil {
				return
			}
			cleanupErr := releaseRunModels(cmd, deps, opts, cfg, authContext, operators, selected.OperatorSessionID, spec)
			if cleanupErr == nil {
				return
			}
			runErr = errors.Join(runErr, fmt.Errorf("evaluation: run execute: release provider models: %w", cleanupErr))
		}()
		endpoint, err := resolveCampaignOllamaEndpoint(operators, selected.OperatorSessionID)
		if err != nil {
			return executed, fmt.Errorf("evaluation: run execute: %w", err)
		}
		if err := releaseResidentProviderModels(cmd, deps, opts, cfg, authContext, selected.OperatorSessionID, endpoint); err != nil {
			return executed, fmt.Errorf("evaluation: run execute: %w", err)
		}
	}
	modelBindings, err := evaluation.CampaignModelBindingsFromSpec(spec)
	if err != nil {
		return executed, fmt.Errorf("evaluation: run execute: %w", err)
	}
	var preflightProgress func(models.ModelProvenancePreflightProgress)
	if !opts.JSONOutput {
		fmt.Fprintf(cmd.OutOrStdout(), "Preflight: model storage attestation (operator acknowledgement: %s; maximum per model: %s)\n", constants.ModelProvenanceCommandAcknowledgementTimeout, constants.ModelProvenanceAttestationPreflightTimeout)
		preflightProgress = func(event models.ModelProvenancePreflightProgress) {
			phase := event.Phase
			switch phase {
			case "awaiting_operator":
				phase = "waiting for operator acknowledgement"
			case "attesting_storage":
				phase = "hashing model storage"
			case "ready":
				phase = "storage attestation ready"
			case "failed":
				phase = "storage attestation failed"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", event.ServedModelTag, phase)
		}
	}
	if err := gwremote.PreflightCampaignModelProvenanceContext(cmd.Context(), fileSvc, cfg, modelBindings, preflightProgress); err != nil {
		return executed, fmt.Errorf("evaluation: run execute: %w", err)
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
	if stopped && !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Stopped after %d assignment(s): cancel requested\n", executed)
	}

	summary, err = controller.RunSummary(cmd.Context(), opts.RunID)
	if err != nil {
		return executed, fmt.Errorf("evaluation: run execute: %w", err)
	}
	if summary.QueuedCount == 0 && summary.RunningCount == 0 {
		if err := releaseRunModels(cmd, deps, opts, cfg, authContext, operators, selected.OperatorSessionID, spec); err != nil {
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

func releaseResidentProviderModels(
	cmd *cobra.Command,
	deps nativeEvalDeps,
	opts runExecuteOptions,
	cfg *config.Config,
	authContext *auth.ClientAuthContext,
	inferenceSessionID string,
	endpoint string,
) error {
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
	if len(residency.Models) == 0 {
		return nil
	}
	residentTags := make([]string, 0, len(residency.Models))
	for _, model := range residency.Models {
		residentTags = append(residentTags, model.Name)
	}
	if err := evaluation.ReleaseOllamaModels(cmd.Context(), dispatcher, inferenceSessionID, opts.RunID, residentTags, modelCommandEnvironment(endpoint), maintenance.NewID); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	if err := evaluation.WaitForOllamaModelsAbsent(waitCtx, dispatcher, maintenance, residentTags); err != nil {
		return err
	}
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Released %d resident model(s) through inference session %s\n", len(residentTags), inferenceSessionID)
	}
	return nil
}

func releaseRunModels(
	cmd *cobra.Command,
	deps nativeEvalDeps,
	opts runExecuteOptions,
	cfg *config.Config,
	authContext *auth.ClientAuthContext,
	operators []*operatorv1.OperatorDocument,
	inferenceSessionID string,
	spec *evalv1.EvaluationCampaignSpec,
) error {
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
		return err
	}
	residentTags := make([]string, 0, len(modelTags))
	for _, tag := range modelTags {
		for _, resident := range residency.Models {
			if resident.Name == tag {
				residentTags = append(residentTags, tag)
				break
			}
		}
	}
	if len(residentTags) == 0 {
		return nil
	}
	if err := evaluation.ReleaseOllamaModels(cmd.Context(), dispatcher, inferenceSessionID, opts.RunID, residentTags, modelCommandEnvironment(endpoint), maintenance.NewID); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	if err := evaluation.WaitForOllamaModelsAbsent(waitCtx, dispatcher, maintenance, residentTags); err != nil {
		return err
	}
	if !opts.JSONOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Released %d campaign model(s) through inference session %s\n", len(residentTags), inferenceSessionID)
	}
	return nil
}

func resolveCampaignOllamaEndpoint(operators []*operatorv1.OperatorDocument, inferenceSessionID string) (string, error) {
	endpoint, err := evaluation.GovernedInferenceOllamaEndpoint(operators, inferenceSessionID)
	if err != nil {
		return "", fmt.Errorf("evaluation: run execute: %w", err)
	}
	return endpoint, nil
}
