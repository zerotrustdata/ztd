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
	t.Parallel()
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

func TestEvalLoopCommandSelectsLiveTargetWithTestBinaryFlags(t *testing.T) {
	t.Parallel()
	var args []string
	cmd := testEvalLoopCmdWithRunner(func(_ context.Context, _ string, argv ...string) (int, error) {
		args = argv
		return 0, nil
	})
	cmd.SetArgs([]string{"--posture", "notary", "--live-ollama-endpoint", "http://192.0.2.10:11434", "--live-model", "qwen3:4b"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, args, "-tags=e2e,live")
	assert.Contains(t, args, "^TestEvalLoopLive$/^notary$")
	assert.Contains(t, args, "40m0s", "one selected posture gets one live share")
	require.Equal(t, []string{"./internal/cli/cmd/test", "-args", "-eval-loop-live-endpoint=http://192.0.2.10:11434", "-eval-loop-live-model=qwen3:4b"}, args[len(args)-4:])
}

func TestEvalLoopCommandGivesEachLivePostureItsOwnDeadline(t *testing.T) {
	t.Parallel()
	var args []string
	cmd := testEvalLoopCmdWithRunner(func(_ context.Context, _ string, argv ...string) (int, error) {
		args = argv
		return 0, nil
	})
	cmd.SetArgs([]string{"--live-ollama-endpoint", "http://192.0.2.10:11434", "--live-model", "qwen3:4b"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, args, "^TestEvalLoopLive$")
	assert.Contains(t, args, "1h20m0s")
}

func TestEvalLoopCommandRejectsIncompleteLiveTargetBeforeStarting(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{
		{"--live-ollama-endpoint", "http://192.0.2.10:11434"},
		{"--live-model", "qwen3:4b"},
		{"--live-ollama-endpoint", "192.0.2.10:11434", "--live-model", "qwen3:4b"},
	} {
		cmd := testEvalLoopCmdWithRunner(func(context.Context, string, ...string) (int, error) {
			t.Fatal("invalid live target started a child test")
			return 0, nil
		})
		cmd.SetArgs(flags)
		require.ErrorIs(t, cmd.Execute(), constants.ErrValidationFailed, flags)
	}
}

func TestEvalLoopCommandRejectsUnsupportedPostureBeforeStarting(t *testing.T) {
	t.Parallel()
	cmd := testEvalLoopCmdWithRunner(func(context.Context, string, ...string) (int, error) {
		t.Fatal("invalid posture started a child test")
		return 0, nil
	})
	cmd.SetArgs([]string{"--posture", "doctrine"})
	require.ErrorIs(t, cmd.Execute(), constants.ErrInvalidPosture)
}
