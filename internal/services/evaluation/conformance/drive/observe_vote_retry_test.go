// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestObserveCopiesARetriedVoteWithItsSupersededAttempts(t *testing.T) {
	failed := &operatorv1.ConsensusVoteInference{RetryCount: 0, Receipt: &operatorv1.EventReceipt{TransactionId: "vote-failed"}}
	vote := &operatorv1.ConsensusVoteInference{RetryCount: 1, Receipt: &operatorv1.EventReceipt{TransactionId: "vote"}, SupersededAttempts: []*operatorv1.ConsensusVoteInference{failed}}
	o := &client.GovernedOutcome{
		Observation:         &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
		ConsensusInferences: []*operatorv1.ConsensusVoteInference{vote},
	}

	obs := Observe(o)

	require.Len(t, obs.GetConsensusInferences(), 1, "one record per member")
	assert.True(t, proto.Equal(vote, obs.GetConsensusInferences()[0]))
	obs.GetConsensusInferences()[0].GetSupersededAttempts()[0].Receipt.TransactionId = "changed"
	assert.Equal(t, "vote-failed", failed.GetReceipt().GetTransactionId(), "Observe copies the nested attempts, never aliases them")
}
