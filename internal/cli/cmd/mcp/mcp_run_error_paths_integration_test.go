// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestStartGatewayIfNeeded_ConfigLoadError(t *testing.T) {
	t.Run("returns wrapped error when config load fails", func(t *testing.T) {
		cmdtest.ChdirTemp(t)

		originalLoad := shared.ConfigLoad
		shared.ConfigLoad = func(string) (*config.Config, error) {
			return nil, fmt.Errorf("no config on disk")
		}
		t.Cleanup(func() { shared.ConfigLoad = originalLoad })

		err := startGatewayIfNeeded(shared.NewFileSvc, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mcp: load config")
	})
}

func TestLaunchAgentWithGovernance_ConfigLoadError(t *testing.T) {
	t.Run("returns ErrGatewayNotReady when config load fails", func(t *testing.T) {
		cmdtest.ChdirTemp(t)

		originalLoad := shared.ConfigLoad
		shared.ConfigLoad = func(string) (*config.Config, error) {
			return nil, fmt.Errorf("no config")
		}
		t.Cleanup(func() { shared.ConfigLoad = originalLoad })

		err := launchAgentWithGovernance("claude", nil, false, "", shared.NewFileSvc, authcmd.PanickingEnrollerFactory())
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrGatewayNotReady)
	})
}

func TestLaunchAgentWithGovernance_UnknownAgentReturnsBeforeEnrollment(t *testing.T) {
	cmdtest.ChdirTemp(t)
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.PidDirname, constants.OperatorPIDFilename), []byte(strconv.Itoa(os.Getpid())), constants.PermFilePrivate))

	originalLoad := shared.ConfigLoad
	shared.ConfigLoad = func(string) (*config.Config, error) {
		return cfg, nil
	}
	t.Cleanup(func() { shared.ConfigLoad = originalLoad })

	err := launchAgentWithGovernance("unknown-agent-xyz", nil, false, "", cmdtest.FileSvcFactoryFor(fileSvc), authcmd.PanickingEnrollerFactory())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAgentNotFound)
	assert.NotContains(t, err.Error(), "enrollerFactory should not be called on this code path")
}
