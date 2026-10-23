// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package authcmd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// mockAPIClient implements apiClient for testing.

// setupApproveAPITestEnv creates a full test environment with valid key, cert, and credentials.
func setupApproveAPITestEnv(t *testing.T) (*config.Config, ed25519.PrivateKey, fs.RuntimeFileService) {
	t.Helper()
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cmdtest.WriteSealedKey(t, fileSvc, cfg, cfg.CLIKeyFile(), keyPEM)

	certDER := cmdtest.GenerateApproveTestCertDER(t, priv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	require.NoError(t, fileSvc.WriteFile(context.Background(), cmdtest.MustRel(t, fileSvc, cfg.CLICertFile()), certPEM, constants.PermFilePrivate))

	creds := &auth.Credentials{
		OperatorSessionID: "op-sess-test",
		UserID:            "user-test",
		OperatorID:        "op-test",
		CLISessionID:      "cli-sess-test",
	}
	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, creds))

	return cfg, priv, fileSvc
}

// setupApproveSSETestEnv extends setupApproveAPITestEnv by writing a valid CA
// cert to the trust bundle path so auth.BuildMTLSClient can succeed.

// withEndpointOverride sets the config endpoint override to the given URL and
// resets it on cleanup.

func TestApproveCmd_APIInjection_ClientFactoryError(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg, _, fileSvc := setupApproveAPITestEnv(t)

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) {
		return nil, constants.ErrNotAuthenticated
	}

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{"txhash123"})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotAuthenticated)
}

func TestApproveCmd_NoCredentials_Error(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg, _, fileSvc := setupApproveAPITestEnv(t)
	require.NoError(t, fileSvc.Remove(context.Background(), cmdtest.MustRel(t, fileSvc, cfg.CredentialsFile())))

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return &cmdtest.MockAPIClient{}, nil }

	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{"txhash123"})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotAuthenticated)
}
