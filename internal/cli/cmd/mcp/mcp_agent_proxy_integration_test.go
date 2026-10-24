// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package mcp

import (
	"testing"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestRunMCPAgentRun_NoArgsReturnsError(t *testing.T) {
	err := runMCPAgentRun(nil, false, "", shared.NewFileSvc, authcmd.PanickingEnrollerFactory())
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrAgentNotFound)
	assert.Contains(t, err.Error(), "specify an agent name")
}

func TestRunMCPAgentRun_UnknownAgentReturnsError(t *testing.T) {
	err := runMCPAgentRun([]string{"unknown-agent"}, false, "", shared.NewFileSvc, authcmd.PanickingEnrollerFactory())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAgentNotFound)
}
