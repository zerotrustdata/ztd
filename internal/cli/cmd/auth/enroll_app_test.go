// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package authcmd

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

type appEnrollerFunc func(context.Context, io.Writer) (*models.PlatformEnrollmentCompleteResponse, error)

func (f appEnrollerFunc) Enroll(ctx context.Context, out io.Writer) (*models.PlatformEnrollmentCompleteResponse, error) {
	return f(ctx, out)
}

func TestEnrollAppCmd_SigningKeyOption(t *testing.T) { //nolint:paralleltest // mutates package-level state

	for _, signing := range []bool{false, true} {
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		called := false
		factory := func(name string, _ fs.RuntimeFileService, _ *config.Config, _ *slog.Logger, opts ...auth.AppEnrollmentOptions) (AppEnroller, error) {
			require.Equal(t, "reviewer", name)
			require.Len(t, opts, 1)
			require.Equal(t, signing, opts[0].GenerateSigningKey)
			return appEnrollerFunc(func(context.Context, io.Writer) (*models.PlatformEnrollmentCompleteResponse, error) {
				called = true
				return &models.PlatformEnrollmentCompleteResponse{}, nil
			}), nil
		}
		cmd := enrollAppCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc), factory)
		args := []string{"reviewer"}
		if signing {
			args = append(args, "--signing-key")
		}
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute())
		require.True(t, called)
	}
}
