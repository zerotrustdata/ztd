// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newPostureTestFileSvc(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	return fileSvc
}

func writeProfile(t *testing.T, fileSvc fs.RuntimeFileService, cfg serve.GatewayConfig) {
	t.Helper()
	require.NoError(t, serve.WriteLaunchProfile(fileSvc, cfg))
}

func TestValidateRequestedPosture(t *testing.T) {
	t.Parallel()
	for _, ok := range []constants.Posture{"", constants.PostureDoctrine, constants.PostureConsensus, constants.PostureRatify, constants.PostureNotary} {
		require.NoError(t, validateRequestedPosture(ok), "%q", ok)
	}
	for _, bad := range []constants.Posture{constants.Posture("strict"), "DOCTRINE", "notary "} {
		require.ErrorIs(t, validateRequestedPosture(bad), constants.ErrInvalidPosture, "%q", bad)
	}
}

func TestResolveGatewayLaunchConfig(t *testing.T) {
	t.Parallel()
	existing := serve.GatewayConfig{
		Posture:          constants.PostureConsensus,
		HTTPPort:         8080,
		HTTPSPort:        8443,
		LogLevel:         "debug",
		CertIdentityMode: "full",
		AllowedOrigins:   []string{"https://console.example"},
	}

	tests := []struct {
		name        string
		profile     *serve.GatewayConfig
		requested   constants.Posture
		wantPosture constants.Posture
		wantBase    bool // whether the profile's other settings must carry over
	}{
		{name: "no profile, no request defaults to doctrine", wantPosture: constants.PostureDoctrine},
		{name: "no profile, explicit request", requested: constants.PostureNotary, wantPosture: constants.PostureNotary},
		{name: "profile posture is kept when unset", profile: &existing, wantPosture: constants.PostureConsensus, wantBase: true},
		{name: "explicit request overrides profile posture", profile: &existing, requested: constants.PostureRatify, wantPosture: constants.PostureRatify, wantBase: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fileSvc := newPostureTestFileSvc(t)
			if tt.profile != nil {
				writeProfile(t, fileSvc, *tt.profile)
			}

			cfg, err := resolveGatewayLaunchConfig(fileSvc, tt.requested)
			require.NoError(t, err)
			assert.Equal(t, tt.wantPosture, cfg.Posture)
			if tt.wantBase {
				assert.Equal(t, 8443, cfg.HTTPSPort, "the previous gateway's ports must survive")
				assert.Equal(t, []string{"https://console.example"}, cfg.AllowedOrigins)
			} else {
				assert.Equal(t, "localhost", cfg.CertIdentityMode)
			}
		})
	}
}

func TestResolveGatewayLaunchConfig_CorruptProfileFailsClosed(t *testing.T) {
	t.Parallel()
	fileSvc := newPostureTestFileSvc(t)
	require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.PidDirname, constants.OperatorLaunchProfileFilename), []byte(`{not json`), constants.PermFilePrivate))

	_, err := resolveGatewayLaunchConfig(fileSvc, constants.PostureNotary)
	require.ErrorIs(t, err, constants.ErrLaunchProfileCorrupted)
}

func TestRunningGatewayPosture(t *testing.T) {
	t.Parallel()
	t.Run("no explicit request returns the profile posture", func(t *testing.T) {
		t.Parallel()
		fileSvc := newPostureTestFileSvc(t)
		writeProfile(t, fileSvc, serve.GatewayConfig{Posture: constants.PostureRatify, LogLevel: "info"})
		posture, err := runningGatewayPosture(fileSvc, "")
		require.NoError(t, err)
		require.Equal(t, constants.PostureRatify, posture)
	})

	t.Run("matching profile passes", func(t *testing.T) {
		t.Parallel()
		fileSvc := newPostureTestFileSvc(t)
		writeProfile(t, fileSvc, serve.GatewayConfig{Posture: constants.PostureNotary, LogLevel: "info"})
		posture, err := runningGatewayPosture(fileSvc, constants.PostureNotary)
		require.NoError(t, err)
		require.Equal(t, constants.PostureNotary, posture)
	})

	t.Run("different posture fails closed", func(t *testing.T) {
		t.Parallel()
		fileSvc := newPostureTestFileSvc(t)
		writeProfile(t, fileSvc, serve.GatewayConfig{Posture: constants.PostureDoctrine, LogLevel: "info"})
		_, err := runningGatewayPosture(fileSvc, constants.PostureNotary)
		require.ErrorIs(t, err, constants.ErrGatewayPostureMismatch)
	})

	t.Run("unverifiable requested posture fails closed", func(t *testing.T) {
		t.Parallel()
		_, err := runningGatewayPosture(newPostureTestFileSvc(t), constants.PostureNotary)
		require.ErrorIs(t, err, constants.ErrGatewayPostureMismatch)
		require.ErrorIs(t, err, constants.ErrLaunchProfileMissing)
	})

	t.Run("unknown posture without a request fails closed", func(t *testing.T) {
		t.Parallel()
		_, err := runningGatewayPosture(newPostureTestFileSvc(t), "")
		require.ErrorIs(t, err, constants.ErrGatewayPostureUnknown)
		require.ErrorIs(t, err, constants.ErrLaunchProfileMissing)
	})
}

func TestAgentRunCmd_ExposesPostureFlagWithNoHardcodedDefault(t *testing.T) {
	t.Parallel()
	flag := agentRunCmd().Flags().Lookup("posture")
	require.NotNil(t, flag)
	assert.Empty(t, flag.DefValue, "the default comes from the previous gateway or the platform default, not the flag")
}
