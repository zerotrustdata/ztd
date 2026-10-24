//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// Exercise the production constructors: injecting an already wrapped evidence
// service into gatewayCleanCmdWithConfig hides a missing production vault.
func TestGatewayDestructiveCommands_BackUpSealedEvidenceBeforeCleanup(t *testing.T) {
	for _, command := range []struct {
		name string
		new  func() *cobra.Command
	}{
		{name: "clean", new: gatewayCleanCmd},
		{name: "reset", new: gatewayResetCmd},
	} {
		for _, vaultAvailable := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/vault_available=%t", command.name, vaultAvailable), func(t *testing.T) {
				ctx := testutil.Context(t)
				keyring := keystoretest.NewMemoryKeyring()
				files, cfg := cmdtest.NewEvidenceCmdTestEnvWithKeyring(t, keyring)
				raw := evaluation.SealedFiles(files)
				record := filepath.Join(constants.EvaluationDataPath, constants.EvaluationRunsDirname, "run-1", constants.ConformanceRecordFilename)
				body := []byte(`{"run":"pre-clean-backup"}`)
				require.NoError(t, files.WriteFile(ctx, record, body, constants.PermFilePrivate))
				stored, err := raw.ReadFile(ctx, record)
				require.NoError(t, err)
				assert.NotContains(t, string(stored), string(body))

				originalLoader, originalFiles, originalEvidence := shared.ConfigLoad, shared.NewFileSvc, shared.NewEvidenceFileSvc
				t.Cleanup(func() {
					shared.ConfigLoad, shared.NewFileSvc, shared.NewEvidenceFileSvc = originalLoader, originalFiles, originalEvidence
				})
				shared.ConfigLoad = cmdtest.ConfigLoaderFor(cfg)
				cleanupBlocked := fmt.Errorf("stop test before service or trust-store changes")
				fileCalls := 0
				shared.NewFileSvc = func(string, *slog.Logger) (fs.RuntimeFileService, error) {
					fileCalls++
					if fileCalls > 1 {
						return nil, cleanupBlocked
					}
					return raw, nil
				}
				opts := keystore.Options{Keyring: keyring}
				if !vaultAvailable {
					opts.Keyring = keystoretest.NewMemoryKeyring()
				}
				shared.NewEvidenceFileSvc = shared.EvidenceFileSvcFactory(opts)

				cmd := command.new()
				cmd.SetContext(ctx)
				cmd.SetIn(strings.NewReader("y\ny\n"))
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				err = cmd.RunE(cmd, nil)

				// A backup failure must never reach cleanup. A successful backup
				// reaches the next factory, where this test prevents any mutation.
				if !vaultAvailable {
					require.ErrorIs(t, err, constants.ErrDestructiveBackup)
					require.ErrorIs(t, err, constants.ErrEvaluationEvidenceVaultRequired)
					assert.Equal(t, 1, fileCalls)
					assert.NotContains(t, output.String(), "Backed up evaluation evidence")
				} else {
					require.ErrorIs(t, err, cleanupBlocked)
					require.ErrorIs(t, err, constants.ErrFileServiceInit)
					assert.Equal(t, 2, fileCalls)
					assert.Contains(t, output.String(), "Backed up evaluation evidence")
				}
				after, readErr := raw.ReadFile(ctx, record)
				require.NoError(t, readErr)
				assert.Equal(t, stored, after, "the original runtime must remain intact")
				backupDir := shared.DefaultEvalBackupDir(cfg)
				snapshots, globErr := filepath.Glob(filepath.Join(backupDir, constants.EvaluationBackupDirPrefix+"*"))
				require.NoError(t, globErr)
				if !vaultAvailable {
					assert.Empty(t, snapshots)
					return
				}
				require.Len(t, snapshots, 1)
				fresh, _ := cmdtest.NewEvidenceCmdTestEnvWithKeyring(t, keyring)
				_, err = evaluation.NewEvalBackup(fresh, time.Now).Restore(ctx, snapshots[0], false)
				require.NoError(t, err)
				restored, err := fresh.ReadFile(ctx, record)
				require.NoError(t, err)
				assert.Equal(t, body, restored)
			})
		}
	}
}
