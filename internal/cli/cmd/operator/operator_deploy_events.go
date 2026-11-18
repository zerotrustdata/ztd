// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"context"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
)

// deploymentEventsConnector opens the deploying CLI's event stream.
type deploymentEventsConnector func(context.Context, fs.RuntimeFileService, *config.Config) (operatordeploy.DeploymentWatcher, func(), error)

func connectGatewayDeploymentEvents(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config) (operatordeploy.DeploymentWatcher, func(), error) {
	creds, err := auth.LoadCredentials(fileSvc, cfg)
	if err != nil || creds == nil || creds.CLISessionID == "" {
		return nil, nil, fmt.Errorf("%w\nEnroll first with:\n./ztd auth enroll user", constants.ErrNotAuthenticated)
	}
	httpClient, err := auth.BuildMTLSClient(fileSvc, cfg, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("operator deploy: build mTLS client: %w", err)
	}
	events, stop, err := operatordeploy.ConnectDeploymentEvents(ctx, httpClient, cfg.OperatorPublicURL(), creds.CLISessionID)
	if err != nil {
		return nil, nil, err
	}
	return events, stop, nil
}
