// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/buildinfo"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	version "github.com/zerotrustdata/ztd/internal/cli/cmd/version"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	"github.com/zerotrustdata/ztd/internal/uuid"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type nativeEvalClientFactory func(harnessconfig.Config) (*harnessclient.Client, error)
type nativeEvalAuthLoader func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error)

type nativeEvalDeps struct {
	configLoader               func(string) (*config.Config, error)
	fileSvcFactory             func(string, *slog.Logger) (fs.RuntimeFileService, error)
	createRuntimeTree          func(context.Context, fs.RuntimeFileService) error
	clientFactory              nativeEvalClientFactory
	authLoader                 nativeEvalAuthLoader
	bindClientFactory          func(*config.Config) evalBindClient
	campaignPublicationFactory campaignVerificationPublicationFactory
	formationRunnerFactory     campaignFormationRunnerFactory
	appEnroller                evalAppEnroller
	runControl                 runControlDeps
	conformance                conformanceDeps
	now                        func() time.Time
	newID                      func() (string, error)
}

// evalAppEnroller enrolls the ztd-eval application identity, writing progress
// to out.
type evalAppEnroller func(ctx context.Context, out io.Writer, fileSvc fs.RuntimeFileService, cfg *config.Config) error

// adaptNewID adapts a fallible ID generator to the evaluation framework's
// infallible func(string) string seams
// (NewCampaignController and friends), which predate error-aware ID
// generation and cannot propagate errors. An OS random-source failure
// (practically impossible) yields a visibly-invalid ID rather than a
// silent empty string or a panic (INV-CODE-06).
func adaptNewID(newID func() (string, error)) func(string) string {
	return func(prefix string) string {
		id, err := newID()
		if err != nil {
			return prefix + "-uuid-generation-failed"
		}
		return prefix + "-" + id
	}
}

// gatewayDeps projects the shared dependencies onto the operator-facing helpers.
func (d nativeEvalDeps) gatewayDeps() gatewayEvalDeps {
	return gatewayEvalDeps{
		configLoader:      d.configLoader,
		fileSvcFactory:    d.fileSvcFactory,
		authLoader:        d.authLoader,
		clientFactory:     d.clientFactory,
		bindClientFactory: d.bindClientFactory,
		now:               d.now,
		newID:             d.newID,
	}
}

func Cmd() *cobra.Command {
	return evalCmdWithConfig(nativeEvalDeps{
		configLoader:      config.Load,
		fileSvcFactory:    shared.NewEvidenceFileSvc,
		createRuntimeTree: func(ctx context.Context, fileSvc fs.RuntimeFileService) error { return fileSvc.CreateRuntimeTree(ctx) },
		clientFactory:     harnessclient.New,
		authLoader:        auth.LoadClientAuthContext,
		bindClientFactory: defaultEvalBindClient,
		campaignPublicationFactory: func(cmd *cobra.Command, fileSvc fs.RuntimeFileService, cfg *config.Config) (campaignVerificationPublication, error) {
			return NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
		},
		formationRunnerFactory: newProductionCampaignFormationRunner,
		appEnroller:            enrollEvalAppIdentity,
		runControl:             defaultRunControlDeps(),
		conformance:            defaultConformanceDeps(),
		now:                    time.Now,
		newID:                  uuid.NewString,
	})
}

func evalCmdWithConfig(deps nativeEvalDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run and verify ztd evaluation programs",
		Long:  `Platform evaluation programs and their supporting workflows.`,
	}
	cmd.PersistentFlags().String("project-root", "", "Override the repository root (defaults to cwd)")
	cmd.AddCommand(
		modelsEvalCmd(deps),
		campaignsEvalCmd(deps),
		runsEvalCmd(deps),
		suitesEvalCmd(deps),
		rolloutEvalCmd(deps),
		formationsEvalCmd(deps),
		gatesEvalCmd(deps),
		conformanceEvalCmd(deps),
		jsonLeaf(backupEvalCmd(deps)),
		jsonLeaf(restoreEvalCmd(deps)),
	)
	return cmd
}

func formationsEvalCmd(deps nativeEvalDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "formations",
		Short: "Heterogeneous model sets",
	}
	cmd.AddCommand(
		formationsListEvalCmd(),
		formationsShowEvalCmd(),
		formationsAddEvalCmd(),
		formationsRemoveEvalCmd(),
		formationsSmokeEvalCmd(deps),
	)
	return cmd
}

func gatesEvalCmd(deps nativeEvalDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gates",
		Short: "Pre-campaign acceptance gates",
	}
	cmd.AddCommand(
		jsonLeaf(gatesInferenceEvalCmd(deps)),
		jsonLeaf(gatesProbeEvalCmd(deps)),
	)
	return cmd
}

func nativeEvalEnvironment(cmd *cobra.Command, deps nativeEvalDeps) (*config.Config, fs.RuntimeFileService, error) {
	projectRoot, err := cmd.Flags().GetString("project-root")
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: read project root: %w", err)
	}
	cfg, err := deps.configLoader(projectRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: load config: %w", err)
	}
	fileSvc, err := deps.fileSvcFactory(cfg.ProjectRoot, slog.Default())
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
	}
	if err := deps.createRuntimeTree(cmd.Context(), fileSvc); err != nil {
		return nil, nil, fmt.Errorf("evaluation: create runtime tree: %w", err)
	}
	return cfg, fileSvc, nil
}

func nativeEvalClientConfig(cfg *config.Config, authContext *auth.ClientAuthContext) harnessconfig.Config {
	credentials := harnessconfig.Auth{ClientCert: authContext.ClientCert, ClientKey: authContext.ClientKey, Certificate: authContext.Certificate, CABundle: cfg.ResolvedTrustBundlePath()}
	return harnessconfig.Config{MTLSBaseURL: cfg.OperatorHTTPURL(), PublicBaseURL: cfg.OperatorDiscoveryURL(), Auth: credentials, CLIAuth: credentials, UseCLIConfig: true, UserID: authContext.UserID, CLISessionID: authContext.CLISessionID, OperatorSessionID: authContext.OperatorSessionID}
}

func nativeEvalDeployment(cfg *config.Config, authContext *auth.ClientAuthContext, runID, target string) *evalv1.EvaluationDeploymentIdentity {
	return &evalv1.EvaluationDeploymentIdentity{
		DeploymentId:        runID,
		TopologyRef:         &compliancev1.VersionedReference{Id: evaluation.TopologyID, Version: evaluation.TopologyVersion},
		ControlledTarget:    target,
		IndependentObserver: constants.EvaluationRuntimeTargetObserverID,
		RuntimeBoundaries: []*evalv1.EvaluationRuntimeBoundary{
			{Component: evalv1.EvaluationRuntimeComponent_EVALUATION_RUNTIME_COMPONENT_EVALUATOR, ProcessIdentity: "host-side ztd eval process", RuntimeNamespace: "host workspace", Endpoint: cfg.OperatorHTTPURL(), AuthenticatedIdentity: authContext.UserID},
			{Component: evalv1.EvaluationRuntimeComponent_EVALUATION_RUNTIME_COMPONENT_GATEWAY, ProcessIdentity: "ztd gateway (host process)", RuntimeNamespace: "host runtime root", PersistentStore: "Gateway runtime root", Endpoint: cfg.OperatorHTTPURL(), AuthenticatedIdentity: authContext.CLISessionID},
			{Component: evalv1.EvaluationRuntimeComponent_EVALUATION_RUNTIME_COMPONENT_OPERATOR, ProcessIdentity: "ztd data operator (host process)", RuntimeNamespace: "host runtime root", MountedFilesystems: []string{"Operator runtime root", "controlled target directory"}, PersistentStore: "Operator runtime root"},
			{Component: evalv1.EvaluationRuntimeComponent_EVALUATION_RUNTIME_COMPONENT_CONTROLLED_TARGET, ProcessIdentity: target, RuntimeNamespace: "host runtime root", MountedFilesystems: []string{"controlled target directory"}},
		},
	}
}

// platformIdentity returns the build identity the root command attached, the
// release a campaign freezes and a run must execute under.
func platformIdentity(cmd *cobra.Command) evaluation.PlatformIdentity {
	vi := shared.VersionInfoFromCmd(cmd)
	identity := evaluation.PlatformIdentity{Release: vi.Version}
	if rev := version.EffectiveSourceRevision(vi, buildinfo.ReadVCSStamp()); !version.IsUnstampedMetadata(rev) {
		identity.SourceRevision = rev
	}
	return identity
}
