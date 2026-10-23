// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

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
	t.Parallel()
	_, err := NewCampaignAgentExecutor(nil, NewCommandLane(nil, client.Persona{}), nil, nil, time.Now, func(prefix string) string { return prefix })

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}
