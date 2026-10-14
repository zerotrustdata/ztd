// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cmdtest

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// NewCmdTestEnv returns a pre-aligned (fileSvc, cfg) pair for hermetic command tests.
func NewCmdTestEnv(t *testing.T) (fs.RuntimeFileService, *config.Config) {
	t.Helper()
	return SetupTestConfig(t, testutil.TempDir(t))
}

// NewEvidenceCmdTestEnv is NewCmdTestEnv with the evidence file service the
// evaluation and compliance report commands compose: every file under
// .ztd/data/eval/ and .ztd/reports/ is sealed by a runtime vault held in an
// in-memory key store.
func NewEvidenceCmdTestEnv(t *testing.T) (fs.RuntimeFileService, *config.Config) {
	t.Helper()
	fileSvc, cfg := NewCmdTestEnv(t)
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())
	sealed, err := evaluation.NewEvidenceFileService(fileSvc, func() (evaluation.EvidenceVault, error) { return ks.OpenVault() })
	require.NoError(t, err)
	return sealed, cfg
}

// SetupTestConfig builds a runtime file service and config rooted at tmpDir.
func SetupTestConfig(t *testing.T, tmpDir string) (fs.RuntimeFileService, *config.Config) {
	t.Helper()

	fileSvc, err := fs.NewRuntimeFileService(tmpDir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

	cfg, err := config.Load(tmpDir)
	require.NoError(t, err)
	cfg.Keyring = keystoretest.NewMemoryKeyring()
	require.Equal(t, cfg.RuntimeDir, fileSvc.Resolve(""))
	require.NoError(t, fileSvc.WriteFile(context.Background(), cfg.DefaultTrustBundleRelPath(), []byte("dummy-trust-bundle"), constants.PermFilePublic))
	return fileSvc, cfg
}

// WriteSealedKey seals keyPEM under cfg's master key and writes it at the
// absolute runtime path keyPath, as enrollment stores CLI and app keys.
func WriteSealedKey(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config, keyPath string, keyPEM []byte) {
	t.Helper()
	require.NoError(t, auth.SealIdentityKey(context.Background(), fileSvc, cfg, keyPath, keyPEM))
}

// SilentCobraCommand returns a command whose stdout and stderr are discarded.
func SilentCobraCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd
}

// FileSvcFactoryFor returns a file service factory that always returns fileSvc.
func FileSvcFactoryFor(fileSvc fs.RuntimeFileService) func(string, *slog.Logger) (fs.RuntimeFileService, error) {
	return func(string, *slog.Logger) (fs.RuntimeFileService, error) { return fileSvc, nil }
}

// MustRel converts an absolute runtime path to a relative path.
func MustRel(t *testing.T, fileSvc fs.RuntimeFileService, absPath string) string {
	t.Helper()
	rel, err := fileSvc.RelFromAbs(absPath)
	require.NoError(t, err)
	return rel
}

// FailingFileSvcFactory returns a file service factory that always returns err.
func FailingFileSvcFactory(err error) func(string, *slog.Logger) (fs.RuntimeFileService, error) {
	return func(string, *slog.Logger) (fs.RuntimeFileService, error) { return nil, err }
}

// ConfigLoaderFor returns a config loader that always returns cfg.
func ConfigLoaderFor(cfg *config.Config) func(string) (*config.Config, error) {
	return func(string) (*config.Config, error) { return cfg, nil }
}

// EnableGlobalJSON attaches cmd under a synthetic root with the global --json flag enabled.
func EnableGlobalJSON(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	root := cmd.Root()
	if root == cmd {
		wrapper := &cobra.Command{Use: "ztd"}
		wrapper.PersistentFlags().Bool("json", false, "Emit machine-readable JSON output")
		wrapper.AddCommand(cmd)
		root = wrapper
	}
	require.NoError(t, root.PersistentFlags().Set("json", "true"))
}

// GlobalJSONRoot wraps cmd in a synthetic ztd root with --json enabled.
func GlobalJSONRoot(t *testing.T, cmd *cobra.Command) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "ztd"}
	root.PersistentFlags().Bool("json", false, "Emit machine-readable JSON output")
	root.AddCommand(cmd)
	require.NoError(t, root.PersistentFlags().Set("json", "true"))
	return root
}

// UseCLISourceRoot points source-tree discovery at dir for the rest of t.
func UseCLISourceRoot(t *testing.T, dir string) {
	t.Helper()
	previous := shared.CLISourceRoot
	shared.CLISourceRoot = func() (string, error) { return dir, nil }
	t.Cleanup(func() { shared.CLISourceRoot = previous })
}
