// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// retriedVote is one member's vote that needed two retries: the provider asked
// to retry later twice, then the member answered. It is one record, carrying
// its failed attempts as superseded_attempts.
func retriedVote() *operatorv1.ConsensusVoteInference {
	failed := func(retry uint32) *operatorv1.ConsensusVoteInference {
		receipt := &operatorv1.EventReceipt{
			TransactionId: "vote-failed",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FailureCode:   operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT,
		}
		return &operatorv1.ConsensusVoteInference{
			SubjectTransactionId: "subject", MemberAppId: "tribunal-axiom", RetryCount: retry,
			TransactionId: receipt.TransactionId, Receipt: receipt, Failure: &operatorv1.InferenceDispatchFailure{Error: "backend_transient", Receipt: receipt},
		}
	}
	return &operatorv1.ConsensusVoteInference{
		SubjectTransactionId: "subject", MemberAppId: "tribunal-axiom", RetryCount: 2,
		TransactionId: "vote", Receipt: &operatorv1.EventReceipt{TransactionId: "vote"}, Result: &operatorv1.InferenceResult{RetryCount: 2},
		SupersededAttempts: []*operatorv1.ConsensusVoteInference{failed(0), failed(1)},
	}
}

// TestGovernedOutcomeKeepsARetriedVoteAsOneSuccessfulRecord proves every
// ingress passes a vote through whole: one record per member, nested attempts
// intact, and nothing in the outcome reads a retried-then-answered vote as a
// failure.
func TestGovernedOutcomeKeepsARetriedVoteAsOneSuccessfulRecord(t *testing.T) {
	vote := retriedVote()
	votes := []*operatorv1.ConsensusVoteInference{vote}
	receipt := &operatorv1.EventReceipt{TransactionId: "subject"}
	dispatched := &operatorv1.GovernedDispatchResult{Receipt: receipt, ConsensusInferences: votes}
	rejection := &operatorv1.GovernanceRejection{Receipt: receipt, ConsensusInferences: votes}
	refusal := &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED, ConsensusInferences: votes}
	callBody, err := json.Marshal(mcp.CallToolResult{Receipt: canonicalProto(t, receipt), ConsensusInferences: governance.WrapConsensusInferences(votes)})
	require.NoError(t, err)
	a2aBody, err := json.Marshal(mcp.A2ASuccessResponse{Result: canonicalProto(t, receipt), ConsensusInferences: governance.WrapConsensusInferences(votes)})
	require.NoError(t, err)
	for _, test := range []struct {
		name    string
		outcome *GovernedOutcome
		kind    evalv1.ConformanceObservationKind
	}{
		{"dispatch", decodeHTTPOutcome(http.StatusOK, canonicalProto(t, dispatched), &operatorv1.GovernedDispatchResult{}), evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
		{"rejection", decodeHTTPOutcome(http.StatusForbidden, canonicalProto(t, rejection), &operatorv1.GovernedDispatchResult{}), evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION},
		{"refusal", decodeHTTPOutcome(http.StatusServiceUnavailable, canonicalProto(t, refusal), &operatorv1.GovernedDispatchResult{}), evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL},
		{"MCP", decodeMCPResult(callBody), evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
		{"A2A", decodeA2AResult(a2aBody), evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
		{"Tribunal intent", IntentCommandOutcome(&tribunalv1.TribunalIntentResult{CommandOutcome: &tribunalv1.TribunalIntentResult_Dispatched{Dispatched: dispatched}}), evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, test.outcome.Typed())
			assert.Equal(t, test.kind, test.outcome.Observation.GetKind(), "the vote's superseded failures do not change the outcome's kind")
			require.Len(t, test.outcome.ConsensusInferences, 1, "one record per member, never one per attempt")
			got := test.outcome.ConsensusInferences[0]
			assert.True(t, proto.Equal(vote, got), "the record arrives whole, with its superseded attempts")
			assert.Nil(t, got.GetFailure())
			assert.NotNil(t, got.GetResult())
			assert.Len(t, got.GetSupersededAttempts(), 2)
		})
	}
}
