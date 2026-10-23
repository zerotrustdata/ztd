// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"bytes"
	"os"
	"testing"

	"github.com/zerotrustdata/ztd/internal/cli/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestAgentVerify_PassesForEveryRegisteredAgentWithoutTheAgentInstalled(t *testing.T) {
	t.Setenv("PATH", "")
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	scratchFactory := agentScratchFileSvcFactory(t)

	for _, integration := range agent.All() {
		t.Run(string(integration.ID), func(t *testing.T) {
			cmd := agentVerifyCmd(scratchFactory)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)

			require.NoError(t, runAgentVerify(cmd, string(integration.ID), scratchFactory))
			assert.Contains(t, out.String(), "PASS "+string(integration.ID))
		})
	}

	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "verify must not touch the user's real agent config")
}

func TestAgentVerify_UnknownAgentFailsClosed(t *testing.T) {
	scratchFactory := agentScratchFileSvcFactory(t)
	err := runAgentVerify(agentVerifyCmd(scratchFactory), "cursor", scratchFactory)
	require.ErrorIs(t, err, constants.ErrAgentNotFound)
}
