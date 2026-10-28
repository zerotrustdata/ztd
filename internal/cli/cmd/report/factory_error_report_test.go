// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package report

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
)

// failingKeystoreFactory returns a keystore factory that fails with cause.
func failingKeystoreFactory(cause error) func(fs.RuntimeFileService) (*keystore.Keystore, error) {
	return func(fs.RuntimeFileService) (*keystore.Keystore, error) {
		return nil, cause
	}
}

func panickingKeystoreFactory(_ fs.RuntimeFileService) (*keystore.Keystore, error) {
	panic("keystore factory should not be called when fileSvcFactory fails")
}

func TestReportAllCmdWithConfig_FileSvcFactoryError(t *testing.T) {
	errFactory := fmt.Errorf("factory boom")
	cmd := reportAllCmdWithConfig(cmdtest.FailingFileSvcFactory(errFactory), panickingKeystoreFactory)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrFileServiceInit)
	assert.ErrorIs(t, err, errFactory)
}

func TestReportVerifyCmdWithConfig_FileSvcFactoryError(t *testing.T) {
	errFactory := fmt.Errorf("factory boom")
	cmd := reportVerifyCmdWithConfig(cmdtest.FailingFileSvcFactory(errFactory), panickingKeystoreFactory)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrFileServiceInit)
	assert.ErrorIs(t, err, errFactory)
}

func TestReportAllCmdWithConfig_KeystoreFactoryError(t *testing.T) {
	errKeystoreFactory := fmt.Errorf("keystore factory boom")
	fileSvc, _ := cmdtest.NewCmdTestEnv(t)
	cmd := reportAllCmdWithConfig(cmdtest.FileSvcFactoryFor(fileSvc), failingKeystoreFactory(errKeystoreFactory))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrReportVaultUnavailable)
	assert.ErrorIs(t, err, errKeystoreFactory)
}

func TestReportVerifyCmdWithConfig_KeystoreFactoryError(t *testing.T) {
	errKeystoreFactory := fmt.Errorf("keystore factory boom")
	fileSvc, _ := cmdtest.NewCmdTestEnv(t)
	cmd := reportVerifyCmdWithConfig(cmdtest.FileSvcFactoryFor(fileSvc), failingKeystoreFactory(errKeystoreFactory))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrReportVaultUnavailable)
	assert.ErrorIs(t, err, errKeystoreFactory)
}
