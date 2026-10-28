// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package consensuscmd

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestConsensusStatusCmd_FileSvcFactoryError(t *testing.T) {
	_, cfg := cmdtest.NewCmdTestEnv(t)
	cmd := cmdWithConfig(
		cmdtest.ConfigLoaderFor(cfg),
		authcmd.PanickingClientFactory(),
		cmdtest.FailingFileSvcFactory(errFactory),
	)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"status"})
	err := cmd.Execute()
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrFileServiceInit)
	assert.ErrorIs(t, err, errFactory)
}

func TestConsensusEnrollTribunalCmd_FileSvcFactoryError(t *testing.T) {
	_, cfg := cmdtest.NewCmdTestEnv(t)
	deps := tribunalEnrollDeps{
		configLoader:   cmdtest.ConfigLoaderFor(cfg),
		fileSvcFactory: cmdtest.FailingFileSvcFactory(errFactory),
		enrollerFactory: func(string, fs.RuntimeFileService, *config.Config, *slog.Logger, ...auth.AppEnrollmentOptions) (authcmd.AppEnroller, error) {
			panic("enrollerFactory should not be called when fileSvcFactory fails")
		},
		memberEnrolled: func(context.Context, fs.RuntimeFileService, *config.Config, string) bool {
			panic("memberEnrolled should not be called when fileSvcFactory fails")
		},
	}
	panickingClient := func() (authcmd.APIClient, error) {
		panic("client factory should not be called when fileSvcFactory fails")
	}
	cmd := enrollTribunalCmd(panickingClient, deps)
	cmd.Flags().Set("model", "qwen2.5:7b")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrFileServiceInit)
	assert.ErrorIs(t, err, errFactory)
}
