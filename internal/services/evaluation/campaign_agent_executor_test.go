// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
)

const (
	agentTestWorkingDirectory = "/srv/ztd-operator"
	agentTestCampaignID       = "campaign-1"
)

// TestCampaignAgentExecutor_RequiresLedgerSentinelToConstruct needs no fixture:
// the constructor's nil check rejects every missing dependency with the typed
// sentinel before any of them is used.
func TestCampaignAgentExecutor_RequiresLedgerSentinelToConstruct(t *testing.T) {
	_, err := NewCampaignAgentExecutor(nil, NewCommandLane(nil, client.Persona{}), nil, nil, time.Now, func(prefix string) string { return prefix })

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestAgentCallContext_DrainsDeadlineButHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	callCtx, finish := agentCallContext(ctx)
	defer finish()
	<-ctx.Done()
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.NoError(t, callCtx.Err(), "an admitted call can still collect its signed outcome")
	deadline, ok := callCtx.Deadline()
	require.True(t, ok, "the drain remains bounded")
	assert.True(t, deadline.After(time.Now()))
	finish()
	require.ErrorIs(t, callCtx.Err(), context.Canceled)

	ctx, cancel = context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	callCtx, finish = agentCallContext(ctx)
	defer finish()
	cancel()
	select {
	case <-callCtx.Done():
		require.ErrorIs(t, callCtx.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("explicit cancellation must interrupt an admitted call")
	}
}
