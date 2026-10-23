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

func TestConformanceLoopCommandSelectsOnePostureWithItsOwnDeadline(t *testing.T) {
	t.Parallel()
	var command string
	var args []string
	cmd := testConformanceLoopCmdWithRunner(func(_ context.Context, name string, argv ...string) (int, error) {
		command, args = name, argv
		return 0, nil
	})
	cmd.SetArgs([]string{"--posture", "consensus"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "go", command)
	assert.Contains(t, args, "-tags=e2e")
	assert.Contains(t, args, "^TestConformanceLoopCrossProcess$/^consensus$")
	assert.Contains(t, args, "12m0s")
	assert.Equal(t, "./internal/cli/cmd/test", args[len(args)-1])
}

func TestConformanceLoopCommandRunsEveryPostureByDefault(t *testing.T) {
	t.Parallel()
	var args []string
	cmd := testConformanceLoopCmdWithRunner(func(_ context.Context, _ string, argv ...string) (int, error) {
		args = argv
		return 0, nil
	})
	cmd.SetArgs(nil)
	require.NoError(t, cmd.Execute())
	assert.Contains(t, args, "^TestConformanceLoopCrossProcess$")
	assert.Contains(t, args, "48m0s", "each of the four postures gets its own share")

	cmd = testConformanceLoopCmdWithRunner(func(_ context.Context, _ string, argv ...string) (int, error) {
		args = argv
		return 0, nil
	})
	cmd.SetArgs([]string{"--timeout", "7m"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, args, "7m0s")
}

func TestConformanceLoopCommandRejectsInvalidInputBeforeStarting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		flags []string
		want  error
	}{
		{[]string{"--posture", "strict"}, constants.ErrInvalidPosture},
		{[]string{"--timeout", "0s"}, constants.ErrValidationFailed},
	} {
		cmd := testConformanceLoopCmdWithRunner(func(context.Context, string, ...string) (int, error) {
			t.Fatal("invalid input started a child test")
			return 0, nil
		})
		cmd.SetArgs(tc.flags)
		require.ErrorIs(t, cmd.Execute(), tc.want, tc.flags)
	}
}

func TestConformanceLoopCommandFailsOnChildFailure(t *testing.T) {
	t.Parallel()
	cmd := testConformanceLoopCmdWithRunner(func(context.Context, string, ...string) (int, error) {
		return 1, nil
	})
	cmd.SetArgs([]string{"--posture", "doctrine"})
	require.ErrorIs(t, cmd.Execute(), constants.ErrE2ETestsFailed)
}
