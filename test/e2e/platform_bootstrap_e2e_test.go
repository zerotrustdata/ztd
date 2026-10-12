// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package e2e

import (
	"context"
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// TestPlatform_FullBootstrap verifies that a fully bootstrapped live stack
// (gateway, operator, and the Gateway-served console) is healthy and properly connected.
// It assumes the stack is running and enrolled; it does not start or stop
// processes.
func TestPlatform_FullBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultClientTimeout)
	defer cancel()

	t.Run("GatewayHealth", func(t *testing.T) {
		health, err := e2eClient.GetHealth(ctx, e2eCfg.gatewayHTTPURL)
		require.NoError(t, err, "gateway health check must succeed")
		assert.Equal(t, constants.GatewayModeStatusOK, health.Status, "gateway must report status ok")
	})

	t.Run("OperatorRegistered", func(t *testing.T) {
		operators, err := e2eClient.ListOperators(ctx)
		require.NoError(t, err, "listing operators must succeed")
		require.True(t, operators.Success, "operator list must report success")
		require.NotEmpty(t, operators.Operators, "at least one operator must be registered")
		var remoteOperator *operatorv1.OperatorDocument
		for i := range operators.Operators {
			if operators.Operators[i].Status == string(constants.OperatorStatusActive) && operators.Operators[i].OperatorType == string(constants.OperatorTypeRemote) {
				remoteOperator = operators.Operators[i]
				break
			}
		}
		require.NotNil(t, remoteOperator, "an active remote operator must be registered")
		assert.NotEmpty(t, remoteOperator.OperatorSessionId, "remote operator must have an active session ID")
	})

	t.Run("ConsoleIndex", func(t *testing.T) {
		body, err := e2eClient.GetConsoleIndex(ctx)
		require.NoError(t, err, "console index must be reachable")
		assert.Contains(t, string(body), "<title>ztd Console</title>", "console index must serve the console SPA")
	})
}
