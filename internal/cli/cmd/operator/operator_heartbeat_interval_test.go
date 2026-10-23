// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestValidateHeartbeatInterval(t *testing.T) {
	t.Parallel()
	maxSeconds := int(constants.OperatorHeartbeatMaxInterval.Seconds())

	for _, seconds := range []int{0, 1, 15, 30, 60, 120, maxSeconds} {
		require.NoError(t, validateHeartbeatInterval(seconds), "%d seconds must be accepted", seconds)
	}

	for _, seconds := range []int{-1, maxSeconds + 1, 3600} {
		err := validateHeartbeatInterval(seconds)
		require.Error(t, err, "%d seconds must be rejected", seconds)
		assert.ErrorIs(t, err, constants.ErrOperatorHeartbeatIntervalInvalid)
	}
}

func TestOperatorStartCmd_RejectsHeartbeatIntervalPastMaximum(t *testing.T) {
	t.Parallel()

	cmd := operatorStartCmd()
	require.NoError(t, cmd.Flags().Set("heartbeat-interval", "301"))

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrOperatorHeartbeatIntervalInvalid)
}

func TestOperatorStartCmd_RejectsArgsAfterDoubleDash(t *testing.T) {
	t.Parallel()

	cmd := operatorStartCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--provenance-operator-id", "p", "--", "--capabilities", "observer,provenance"}))

	require.Error(t, cmd.ValidateArgs(cmd.Flags().Args()), "flags after -- must not be silently dropped")
	assert.False(t, cmd.Flags().Changed("capabilities"))
}
