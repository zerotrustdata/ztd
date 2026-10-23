// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestValidateGovernedRequest_RejectsUnregisteredMcpEventSpelling(t *testing.T) {
	t.Parallel()
	// The pre-registry MCP event spelling must stay outside the registry.

	_, err := constants.ValidateGovernedRequest(constants.EventType("operator.mcp.call.requested"))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrTxUnknownEventType)
}

func TestActionConstants(t *testing.T) {
	t.Parallel()
	if ActionMcpCall != "MCP_CALL" {
		t.Errorf("ActionMcpCall = %s, want MCP_CALL", ActionMcpCall)
	}
	if ProtocolVersion != "2" {
		t.Errorf("ProtocolVersion = %s, want 2", ProtocolVersion)
	}
}
