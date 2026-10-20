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
	"log/slog"
	"slices"
	"time"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
)

// gatewayEvalDeps carries the dependencies the operator-facing eval commands
// use to load the CLI identity, reach the Gateway, and bind operator sessions.
type gatewayEvalDeps struct {
	configLoader      func(string) (*config.Config, error)
	fileSvcFactory    func(string, *slog.Logger) (fs.RuntimeFileService, error)
	authLoader        func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error)
	clientFactory     func(harnessconfig.Config) (*harnessclient.Client, error)
	bindClientFactory func(*config.Config) evalBindClient
	now               func() time.Time
	newID             func() (string, error)
}

// evalBindClient is the part of the enrollment client that reads and
// changes the operator sessions bound to the CLI session.
type evalBindClient interface {
	Bind(ctx context.Context, fileSvc fs.RuntimeFileService, operatorSessionIDs []string) (auth.CLISessionBind, error)
	SessionInfo(ctx context.Context, fileSvc fs.RuntimeFileService) (auth.CLISessionInfo, error)
}

func defaultEvalBindClient(cfg *config.Config) evalBindClient {
	return auth.NewEnrollmentClient(cfg, nil)
}

func gatewayEvalEnvironment(cmd *cobra.Command, deps gatewayEvalDeps) (*config.Config, fs.RuntimeFileService, *auth.ClientAuthContext, error) {
	projectRoot, err := cmd.Flags().GetString("project-root")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("evaluation: read project root: %w", err)
	}
	cfg, err := deps.configLoader(projectRoot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("evaluation: load config: %w", err)
	}
	fileSvc, err := deps.fileSvcFactory(cfg.ProjectRoot, slog.Default())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
	}
	authContext, err := deps.authLoader(fileSvc, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("evaluation: load CLI identity: %w", err)
	}
	return cfg, fileSvc, authContext, nil
}

// evalBoundSessionIDs reads the operator sessions bound to the CLI
// session, primary first.
func evalBoundSessionIDs(ctx context.Context, client evalBindClient, fileSvc fs.RuntimeFileService) ([]string, error) {
	info, err := client.SessionInfo(ctx, fileSvc)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read CLI session bindings: %w", err)
	}
	if len(info.BoundOperatorSessionIDs) > 0 {
		return info.BoundOperatorSessionIDs, nil
	}
	if info.OperatorSessionID != "" {
		return []string{info.OperatorSessionID}, nil
	}
	return nil, nil
}

// evalBindSessions binds the CLI session to operatorSessionIDs in one call
// and stores the CLI session ID in the local credentials. Binding updates the
// CLI session in place, so the ID is unchanged and the returned auth context
// carries the same CLI session the caller already holds.
func evalBindSessions(
	cmd *cobra.Command,
	cfg *config.Config,
	fileSvc fs.RuntimeFileService,
	client evalBindClient,
	authContext *auth.ClientAuthContext,
	operatorSessionIDs []string,
) (*auth.ClientAuthContext, error) {
	bind, err := client.Bind(cmd.Context(), fileSvc, operatorSessionIDs)
	if err != nil {
		return nil, fmt.Errorf("evaluation: bind operator sessions: %w", err)
	}
	creds, err := auth.LoadCredentials(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("evaluation: load credentials after bind: %w", err)
	}
	if creds == nil {
		return nil, fmt.Errorf("%w: local CLI credentials are absent after bind", constants.ErrNotAuthenticated)
	}
	creds.CLISessionID = bind.CLISessionID
	creds.OperatorSessionID = bind.OperatorSessionID
	creds.OperatorID = bind.OperatorID
	if err := auth.SaveCredentials(fileSvc, cfg, creds); err != nil {
		return nil, fmt.Errorf("evaluation: save credentials after bind: %w", err)
	}
	if !output.JSONEnabled(cmd) {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Bound CLI session to %d operator session(s); primary %s\n", len(bind.Bound), bind.OperatorSessionID)
	}
	return &auth.ClientAuthContext{
		OperatorSessionID: bind.OperatorSessionID,
		CLISessionID:      bind.CLISessionID,
		UserID:            bind.UserID,
		OperatorID:        bind.OperatorID,
		ClientCert:        authContext.ClientCert,
		ClientKey:         authContext.ClientKey,
		Certificate:       authContext.Certificate,
	}, nil
}

// evalBindDataOperator makes the data-operator one of the operator
// sessions bound to the CLI session. A CLI session holds many bound operators,
// so an already-bound data-operator leaves the CLI session untouched. When it
// is not bound, autoBind issues one bind call for the still-active bound
// sessions plus the data-operator; otherwise ErrDataOperatorNotBound is
// returned.
func evalBindDataOperator(
	cmd *cobra.Command,
	deps gatewayEvalDeps,
	cfg *config.Config,
	fileSvc fs.RuntimeFileService,
	authContext *auth.ClientAuthContext,
	operators []*operatorv1.OperatorDocument,
	dataSessionID string,
	autoBind bool,
) (*auth.ClientAuthContext, error) {
	client := deps.bindClientFactory(cfg)
	bound, err := evalBoundSessionIDs(cmd.Context(), client, fileSvc)
	if err != nil {
		return nil, err
	}
	if slices.Contains(bound, dataSessionID) {
		return authContext, nil
	}
	if !autoBind {
		return nil, fmt.Errorf("%w: session %s", constants.ErrDataOperatorNotBound, dataSessionID)
	}
	targets := make([]string, 0, len(bound)+1)
	for _, sessionID := range bound {
		if slices.ContainsFunc(operators, func(op *operatorv1.OperatorDocument) bool {
			return op.OperatorSessionId == sessionID && constants.OperatorStatus(op.Status) == constants.OperatorStatusActive
		}) {
			targets = append(targets, sessionID)
		}
	}
	return evalBindSessions(cmd, cfg, fileSvc, client, authContext, append(targets, dataSessionID))
}

func evalListOperators(
	cmd *cobra.Command,
	deps gatewayEvalDeps,
	cfg *config.Config,
	authContext *auth.ClientAuthContext,
) ([]*operatorv1.OperatorDocument, error) {
	gatewayClient, err := deps.clientFactory(nativeEvalClientConfig(cfg, authContext))
	if err != nil {
		return nil, fmt.Errorf("evaluation: initialize gateway client: %w", err)
	}
	operators, _, err := gatewayClient.ListOperators(cmd.Context())
	if err != nil {
		return nil, fmt.Errorf("evaluation: list operators: %w", err)
	}
	return operators, nil
}

// evalPreflightGatewayPosture fails closed unless the live Gateway runs the
// posture the campaign froze and the eval lane can execute it. The posture is
// immutable at runtime, so one read before scheduling or execution suffices.
func evalPreflightGatewayPosture(
	cmd *cobra.Command,
	deps gatewayEvalDeps,
	cfg *config.Config,
	authContext *auth.ClientAuthContext,
	spec *evalv1.EvaluationCampaignSpec,
) error {
	gatewayClient, err := deps.clientFactory(nativeEvalClientConfig(cfg, authContext))
	if err != nil {
		return fmt.Errorf("evaluation: initialize gateway client: %w", err)
	}
	posture, err := gatewayPosture(cmd.Context(), gatewayClient)
	if err != nil {
		return err
	}
	return evaluation.ValidateGatewayPosture(spec, posture)
}
