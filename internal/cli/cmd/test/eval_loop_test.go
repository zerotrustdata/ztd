// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testcmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestEvalLoopCommandSelectsOnlyOwnedCrossProcessQualification(t *testing.T) {
	var command string
	var args []string
	cmd := testEvalLoopCmdWithRunner(func(_ context.Context, name string, argv ...string) (int, error) {
		command, args = name, argv
		return 0, nil
	})
	cmd.SetArgs([]string{"--posture", "ratify", "--timeout", "7m"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "go", command)
	assert.Contains(t, args, "-tags=e2e")
	assert.Contains(t, args, "^TestEvalLoopCrossProcess$/^ratify$")
	assert.Contains(t, args, "7m0s")
	assert.Equal(t, "./internal/cli/cmd/test", args[len(args)-1])
}

func TestEvalLoopCommandRejectsUnsupportedPostureBeforeStarting(t *testing.T) {
	cmd := testEvalLoopCmdWithRunner(func(context.Context, string, ...string) (int, error) {
		t.Fatal("invalid posture started a child test")
		return 0, nil
	})
	cmd.SetArgs([]string{"--posture", "doctrine"})
	require.ErrorIs(t, cmd.Execute(), constants.ErrInvalidPosture)
}
