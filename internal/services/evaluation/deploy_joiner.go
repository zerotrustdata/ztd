// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// GatewayDeployJoinerConfig wires a Gateway SSE deployment watcher and session
// binder for RosterDeployJoiner.
type GatewayDeployJoinerConfig struct {
	HTTPClient    *http.Client
	BaseURL       string
	CLISessionID  string
	GatewayClient *harnessclient.Client
	BoundSessions func(ctx context.Context) ([]string, error)
	BindSessions  func(ctx context.Context, operatorSessionIDs []string) error
}

// GatewayDeployJoiner watches operator_deploy launches over the CLI's SSE
// stream, resolves the newly enrolled Operator's runtime identity and anchor,
// and binds its OperatorSessionID to the CLI session so CommandLane and
// Sentinel can dispatch to it.
type GatewayDeployJoiner struct {
	events        *operatordeploy.DeploymentEvents
	stop          func()
	stopOnce      sync.Once
	gatewayClient *harnessclient.Client
	boundSessions func(ctx context.Context) ([]string, error)
	bindSessions  func(ctx context.Context, operatorSessionIDs []string) error

	mu      sync.Mutex
	watches map[string]*operatordeploy.DeploymentWatch
	bindMu  sync.Mutex
}

// NewGatewayDeployJoiner connects to the Gateway CLI SSE stream and returns a
// RosterDeployJoiner ready to watch operator_deploy launches.
func NewGatewayDeployJoiner(ctx context.Context, cfg GatewayDeployJoinerConfig) (*GatewayDeployJoiner, error) {
	if cfg.HTTPClient == nil || cfg.BaseURL == "" || cfg.CLISessionID == "" || cfg.GatewayClient == nil || cfg.BoundSessions == nil || cfg.BindSessions == nil {
		return nil, fmt.Errorf("evaluation: gateway deploy joiner: %w", constants.ErrMissingRequiredField)
	}
	events, stop, err := operatordeploy.ConnectDeploymentEvents(ctx, cfg.HTTPClient, cfg.BaseURL, cfg.CLISessionID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: connect deployment events: %w", err)
	}
	return &GatewayDeployJoiner{
		events:        events,
		stop:          stop,
		gatewayClient: cfg.GatewayClient,
		boundSessions: cfg.BoundSessions,
		bindSessions:  cfg.BindSessions,
		watches:       make(map[string]*operatordeploy.DeploymentWatch),
	}, nil
}

// WatchDeployment registers deploymentID before dispatch so live SSE events
// are never missed.
func (j *GatewayDeployJoiner) WatchDeployment(_ context.Context, deploymentID string) error {
	if deploymentID == "" {
		return fmt.Errorf("evaluation: watch deployment: %w", constants.ErrMissingRequiredField)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.watches[deploymentID] = j.events.Watch(deploymentID)
	return nil
}

// AwaitJoinedOperator waits for the launched Operator to become active, resolves
// its enrollment-bound anchor, and binds its session to the CLI session.
func (j *GatewayDeployJoiner) AwaitJoinedOperator(ctx context.Context, deploymentID, hostname string) (CampaignDataOperator, error) {
	j.mu.Lock()
	watch := j.watches[deploymentID]
	j.mu.Unlock()
	if watch == nil {
		return CampaignDataOperator{}, fmt.Errorf("evaluation: deployment %s was not watched before dispatch: %w", deploymentID, constants.ErrOperatorDeployFailed)
	}
	sessionID, err := watch.AwaitReady(ctx)
	if err != nil {
		return CampaignDataOperator{}, err
	}
	operators, _, err := j.gatewayClient.ListOperators(ctx)
	if err != nil {
		return CampaignDataOperator{}, fmt.Errorf("evaluation: list operators after deploy: %w", err)
	}
	var doc *operatorv1.OperatorDocument
	for _, op := range operators {
		if op.GetOperatorSessionId() == sessionID {
			doc = op
			break
		}
	}
	var matched *operatorcapability.DataOperatorStatus
	for _, status := range operatorcapability.ActiveDataFleet(operators) {
		if status.OperatorSessionID == sessionID {
			copyStatus := status
			matched = &copyStatus
			break
		}
	}
	if doc == nil || matched == nil {
		return CampaignDataOperator{}, fmt.Errorf("evaluation: deployed operator session %s is not an active data operator: %w", sessionID, constants.ErrOperatorDeployFailed)
	}
	anchor, err := oracle.BoundAnchor(doc)
	if err != nil {
		return CampaignDataOperator{}, fmt.Errorf("evaluation: bind anchor for deployed operator %s: %w", doc.GetId(), err)
	}

	j.bindMu.Lock()
	bound, err := j.boundSessions(ctx)
	if err == nil {
		targets := make([]string, 0, len(bound)+1)
		for _, id := range bound {
			if slices.ContainsFunc(operators, func(op *operatorv1.OperatorDocument) bool {
				return op.GetOperatorSessionId() == id && constants.OperatorStatus(op.GetStatus()) == constants.OperatorStatusActive
			}) {
				targets = append(targets, id)
			}
		}
		if !slices.Contains(targets, sessionID) {
			targets = append(targets, sessionID)
		}
		err = j.bindSessions(ctx, targets)
	}
	j.bindMu.Unlock()
	if err != nil {
		return CampaignDataOperator{}, fmt.Errorf("evaluation: bind deployed operator session %s: %w", sessionID, err)
	}

	wd := matched.WorkingDirectory
	if wd == "" {
		wd = doc.GetRuntimeConfig().GetLocalDir()
	}
	if wd == "" {
		wd = doc.GetLocalDir()
	}
	opHost := matched.Hostname
	if opHost == "" {
		opHost = hostname
	}
	return CampaignDataOperator{
		CampaignOperatorRuntime: CampaignOperatorRuntime{
			OperatorID:        doc.GetId(),
			OperatorSessionID: sessionID,
			Anchor:            anchor,
		},
		WorkingDirectory: wd,
		Hostname:         opHost,
		OS:               matched.OS,
		Architecture:     matched.Architecture,
	}, nil
}

// Close closes the underlying Gateway SSE stream.
func (j *GatewayDeployJoiner) Close() {
	j.stopOnce.Do(func() {
		if j.stop != nil {
			j.stop()
		}
	})
}
