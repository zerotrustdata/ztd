// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

type securityRecordingFiles struct {
	fs.RuntimeFileService
	existsPaths []string
	readPaths   []string
	pathErr     error
}

func (f *securityRecordingFiles) Rel(path string) (string, error) {
	if f.pathErr != nil {
		return "", f.pathErr
	}
	return f.RuntimeFileService.Rel(path)
}

func (f *securityRecordingFiles) FileExists(_ context.Context, path string) (bool, error) {
	f.existsPaths = append(f.existsPaths, path)
	return true, nil
}

func (f *securityRecordingFiles) ReadFile(_ context.Context, path string) ([]byte, error) {
	f.readPaths = append(f.readPaths, path)
	return nil, constants.ErrFileReadFailed
}

func TestSecurityValidateExplicitRuntimePathsUseFileService(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "working-directory-relative"}[relative], func(t *testing.T) {
			baseDir := testutil.TempDir(t)
			t.Chdir(baseDir)
			local, err := fs.NewRuntimeFileService(baseDir, slog.Default())
			require.NoError(t, err)
			files := &securityRecordingFiles{RuntimeFileService: local}
			cmd := securityValidateCmdWithConfig(func(string, *slog.Logger) (fs.RuntimeFileService, error) {
				return files, nil
			})
			pkiDir := local.Resolve(constants.PkiDirname)
			secretsDir := local.Resolve(constants.SecretsDirname)
			if relative {
				pkiDir, err = filepath.Rel(baseDir, pkiDir)
				require.NoError(t, err)
				secretsDir, err = filepath.Rel(baseDir, secretsDir)
				require.NoError(t, err)
			}
			require.NoError(t, cmd.Flags().Set("pki-dir", pkiDir))
			require.NoError(t, cmd.Flags().Set("secrets-dir", secretsDir))
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)

			require.ErrorIs(t, cmd.RunE(cmd, nil), constants.ErrValidationFailed)
			assert.ElementsMatch(t, []string{
				filepath.Join(constants.PkiDirname, constants.PkiSubdirRoot, constants.PkiFileRootCA),
				filepath.Join(constants.PkiDirname, constants.PkiSubdirRoot, constants.PkiFileRootCAKey),
				filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle),
				filepath.Join(constants.PkiDirname, constants.ActuatorPubJSONFilename),
				filepath.Join(constants.SecretsDirname, constants.SecretsFileActuatorSigningKey),
				filepath.Join(constants.SecretsDirname, constants.SecretsFileBootstrapDigest),
			}, files.existsPaths)
			assert.ElementsMatch(t, []string{
				filepath.Join(constants.PkiDirname, constants.PkiSubdirRoot, constants.PkiFileRootCA),
				filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle),
			}, files.readPaths)
			assert.Contains(t, output.String(), "failed to read root_ca.crt")
			assert.Contains(t, output.String(), "failed to read trust bundle")

			files.pathErr = constants.ErrPathValidation
			files.existsPaths = nil
			files.readPaths = nil
			err = cmd.RunE(cmd, nil)
			require.ErrorIs(t, err, constants.ErrValidationFailed)
			require.ErrorIs(t, err, constants.ErrPathValidation)
			assert.Empty(t, files.existsPaths)
			assert.Empty(t, files.readPaths)
		})
	}
}

func TestSecurityCmd(t *testing.T) {
	t.Run("security command has correct use and description", func(t *testing.T) {
		cmd := securityCmd()
		assert.Equal(t, "security", cmd.Use)
		assert.Contains(t, cmd.Short, "Security validation")
		assert.Contains(t, cmd.Long, "PKI verification")
	})
}

func TestSecurityValidateCmd(t *testing.T) {
	t.Run("validate command has correct use", func(t *testing.T) {
		cmd := securityValidateCmd()
		assert.Equal(t, "validate", cmd.Use)
		assert.Contains(t, cmd.Short, "Run security validation")
	})

	t.Run("validate has pki-dir flag", func(t *testing.T) {
		cmd := securityValidateCmd()
		flag := cmd.Flags().Lookup("pki-dir")
		assert.NotNil(t, flag)
	})

	t.Run("validate has secrets-dir flag", func(t *testing.T) {
		cmd := securityValidateCmd()
		flag := cmd.Flags().Lookup("secrets-dir")
		assert.NotNil(t, flag)
	})
}

func TestSecurityValidateWithTestEnvironment(t *testing.T) {
	t.Run("validate checks PKI directory structure", func(t *testing.T) {
		tmpDir := testutil.TempDir(t)
		pkiDir := filepath.Join(tmpDir, "pki")
		secretsDir := filepath.Join(tmpDir, "secrets")

		// Create required PKI structure
		require.NoError(t, os.MkdirAll(filepath.Join(pkiDir, "root"), 0755))
		require.NoError(t, os.MkdirAll(filepath.Join(pkiDir, "trust"), 0755))

		// Create dummy files
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, "root", "root_ca.crt"), []byte("dummy cert"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, "trust", "ztdg-ca-bundle.pem"), []byte("dummy bundle"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, constants.ActuatorPubJSONFilename), []byte("{}"), 0644))

		// Create secrets
		require.NoError(t, os.MkdirAll(secretsDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(secretsDir, constants.SecretsFileActuatorSigningKey), []byte("dummy key"), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "bootstrap_digest.json"), []byte("{}"), 0644))

		cmd := securityValidateCmdWithConfig(shared.NewFileSvc)
		cmd.Flags().Set("pki-dir", pkiDir)
		cmd.Flags().Set("secrets-dir", secretsDir)

		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		err := cmd.RunE(cmd, []string{})
		// Will fail on certificate validation (dummy certs), but structure checks should pass
		require.Error(t, err)
	})

	t.Run("validate fails with missing PKI directory", func(t *testing.T) {
		tmpDir := testutil.TempDir(t)
		pkiDir := filepath.Join(tmpDir, "pki")
		secretsDir := filepath.Join(tmpDir, "secrets")

		// Don't create PKI directory
		require.NoError(t, os.MkdirAll(secretsDir, 0700))

		cmd := securityValidateCmdWithConfig(shared.NewFileSvc)
		cmd.Flags().Set("pki-dir", pkiDir)
		cmd.Flags().Set("secrets-dir", secretsDir)

		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		err := cmd.RunE(cmd, []string{})
		// Should fail due to missing PKI structure
		require.Error(t, err)
	})

	t.Run("validate fails with missing secrets directory", func(t *testing.T) {
		tmpDir := testutil.TempDir(t)
		pkiDir := filepath.Join(tmpDir, "pki")
		secretsDir := filepath.Join(tmpDir, "secrets")

		// Create PKI but not secrets
		require.NoError(t, os.MkdirAll(filepath.Join(pkiDir, "root"), 0755))

		cmd := securityValidateCmdWithConfig(shared.NewFileSvc)
		cmd.Flags().Set("pki-dir", pkiDir)
		cmd.Flags().Set("secrets-dir", secretsDir)

		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		err := cmd.RunE(cmd, []string{})
		// Should fail due to missing secrets
		require.Error(t, err)
	})

	t.Run("validate fails when root_ca.key exists on disk (unsealed key anti-pattern)", func(t *testing.T) {
		tmpDir := testutil.TempDir(t)
		pkiDir := filepath.Join(tmpDir, "pki")
		secretsDir := filepath.Join(tmpDir, "secrets")

		require.NoError(t, os.MkdirAll(filepath.Join(pkiDir, "root"), 0755))
		require.NoError(t, os.MkdirAll(filepath.Join(pkiDir, "trust"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, "root", "root_ca.crt"), []byte("dummy cert"), 0644))
		// Writing root_ca.key must trigger failure: unsealed private key on disk
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, "root", "root_ca.key"), []byte("dummy key"), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, "trust", "ztdg-ca-bundle.pem"), []byte("dummy bundle"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(pkiDir, constants.ActuatorPubJSONFilename), []byte("{}"), 0644))

		require.NoError(t, os.MkdirAll(secretsDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(secretsDir, constants.SecretsFileActuatorSigningKey), []byte("dummy key"), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "bootstrap_digest.json"), []byte("{}"), 0644))

		cmd := securityValidateCmdWithConfig(shared.NewFileSvc)
		cmd.Flags().Set("pki-dir", pkiDir)
		cmd.Flags().Set("secrets-dir", secretsDir)

		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		err := cmd.RunE(cmd, []string{})
		require.Error(t, err)
		assert.Contains(t, buf.String(), "exists unsealed on disk")
	})
}
