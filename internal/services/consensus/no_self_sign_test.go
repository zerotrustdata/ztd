// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package consensus

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	govsvc "github.com/zerotrustdata/ztd/internal/services/governance"
)

// TestNoSelfSign_ConsensusWithoutDeliberator is a trust-boundary regression
// guard for L2. Under a posture that requires L2 consensus, when no voter is
// configured the coordinator must NOT fabricate votes on behalf of absent
// members. It must fail closed with no quorum, return no envelope, and leave
// the caller's envelope without any L2 votes.
func TestNoSelfSign_ConsensusWithoutDeliberator(t *testing.T) {
	t.Parallel()
	doctrine := govsvc.NewL1Doctrine()
	svc := NewConsensusService("test-consensus", nil, doctrine, slog.New(slog.DiscardHandler), newTestResponder())

	env := makeEnvelope(t, string(constants.ActionTypeFetchLogs), []byte("fetch logs"))

	result, err := svc.Deliberate(env)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrConsensusNoSigningMembers)
	assert.Nil(t, result, "no deliberation result may be produced without voters")

	if env.Governance != nil && env.Governance.L2 != nil {
		assert.Empty(t, env.Governance.L2.Votes, "no L2 votes may be self-signed for absent voters")
	}
}
