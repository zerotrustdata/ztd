// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestCheckOperatorRunning_NotRunning(t *testing.T) {
	t.Parallel()

	// Test with a non-existent port to ensure error
	err := CheckOperatorRunningAtURL("http://localhost:99999")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrServiceUnavailable)
}

func TestCheckOperatorRunning_HealthCheckFailed(t *testing.T) {
	t.Parallel()

	// Test with a non-existent port
	err := CheckOperatorRunningAtURL("https://localhost:99999")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrServiceUnavailable)
}

func TestCheckOperatorRunning_InvalidURL(t *testing.T) {
	t.Parallel()

	err := CheckOperatorRunningAtURL("invalid-url")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrGatewayURLRequired)
}

func TestCheckOperatorRunning_URLWithoutProtocol(t *testing.T) {
	t.Parallel()

	err := CheckOperatorRunningAtURL("localhost:" + strconv.Itoa(constants.Ports.OperatorHttp))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrGatewayURLRequired)
}

func TestGetLocalOSUser(t *testing.T) {
	t.Parallel()

	user := getLocalOSUser()
	require.NotNil(t, user)
	assert.NotEmpty(t, user.Username)
	assert.NotEmpty(t, user.UID)
	assert.NotEmpty(t, user.GID)

	// On Windows, SID should be populated
	if runtime.GOOS == "windows" {
		assert.NotEmpty(t, user.SID)
	}
}
