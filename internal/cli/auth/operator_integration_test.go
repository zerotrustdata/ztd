// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package auth

import (
	"fmt"
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestCheckOperatorRunning_Success(t *testing.T) {

	// Start a test server on a random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)

	url := fmt.Sprintf("http://127.0.0.1:%s", port)
	err = CheckOperatorRunningAtURL(url)
	require.NoError(t, err)
}

func TestCheckOperatorRunning_LocalhostReplacement(t *testing.T) {

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)

	url := fmt.Sprintf("http://localhost:%s", port)
	err = CheckOperatorRunningAtURL(url)
	require.NoError(t, err)
}

func TestCheckOperatorRunning_NotRunning(t *testing.T) {
	// Test with a non-existent port to ensure error
	err := CheckOperatorRunningAtURL("http://localhost:99999")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrServiceUnavailable)
}

func TestCheckOperatorRunning_HealthCheckFailed(t *testing.T) {
	// Test with a non-existent port
	err := CheckOperatorRunningAtURL("https://localhost:99999")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrServiceUnavailable)
}

func TestCheckOperatorRunning_InvalidURL(t *testing.T) {
	err := CheckOperatorRunningAtURL("invalid-url")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrGatewayURLRequired)
}

func TestCheckOperatorRunning_URLWithoutProtocol(t *testing.T) {
	err := CheckOperatorRunningAtURL("localhost:" + strconv.Itoa(constants.Ports.OperatorHttp))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrGatewayURLRequired)
}
