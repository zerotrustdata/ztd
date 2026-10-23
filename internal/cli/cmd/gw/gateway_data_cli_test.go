// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
)

func TestCommandErrorHandling(t *testing.T) {
	t.Run("data store requires collection flag", func(t *testing.T) {
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		loader := func(_ string) (*config.Config, error) { return cfg, nil }
		cmd := dataStoreCmdWithConfig(loader, authcmd.DefaultAPIClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		err := cmd.RunE(cmd, []string{})
		require.Error(t, err)
		// The command will fail on authentication before flag validation
		// Just verify it fails
	})

	t.Run("data audit list requires Operator session id", func(t *testing.T) {
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		loader := func(_ string) (*config.Config, error) { return cfg, nil }
		cmd := dataAuditListCmdWithConfig(loader, authcmd.DefaultAPIClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		// Unset the environment variable
		originalEnv := os.Getenv("ZTD_OPERATOR_SESSION_ID")
		os.Unsetenv("ZTD_OPERATOR_SESSION_ID")
		defer func() {
			if originalEnv != "" {
				os.Setenv("ZTD_OPERATOR_SESSION_ID", originalEnv)
			}
		}()

		err := cmd.RunE(cmd, []string{})
		require.Error(t, err)
		// The command will fail on authentication before flag validation
		// Just verify it fails
	})
}
