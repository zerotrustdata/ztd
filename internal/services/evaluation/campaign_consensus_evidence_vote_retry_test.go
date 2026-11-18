// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// retriedVoteTrace is a shared-runtime trace whose tool call was admitted
// under a Tribunal vote in which the first member needed retries superseded
// attempts: the provider asked it to retry later that many times, then it
// answered. The second member's one attempt failed, without a retry. Every
// attempt is an execution on the runtime's ledger, and the first superseded
// attempt also has an observation receipt.
func retriedVoteTrace(t *testing.T, retries int) (*AgentTrace, oracletest.Signer) {
	t.Helper()
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	before := ledger.executed("before", string(constants.ActionTypeFsRead))
	model := ledger.inference("turn")
	members := constants.TribunalConsensusMemberAppIDs()
	const hash = "hash-tool"
	actAs := func(receipt *operatorv1.EventReceipt, member string) *operatorv1.EventReceipt {
		for _, stage := range receipt.DeterministicStageEvidence {
			stage.ActingAppId = member
		}
		ledger.signer.Resign(t, receipt)
		return receipt
	}

	var superseded []*operatorv1.ConsensusVoteInference
	for retry := range retries {
		var observed []*operatorv1.EventReceipt
		if retry == 0 {
			observed = []*operatorv1.EventReceipt{ledger.executed("vote-observation", string(constants.ActionTypeProviderBoundaryObservation))}
		}
		transaction := fmt.Sprintf("vote-failed-%d", retry)
		failed := ledger.inferenceFailed(transaction)
		failed.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT
		superseded = append(superseded, &operatorv1.ConsensusVoteInference{
			SubjectTransactionId: "tool", SubjectTransactionHash: hash, MemberAppId: members[0],
			ProviderAttemptId: governance.ConsensusVoteAttemptID(hash, members[0], uint32(retry)), RetryCount: uint32(retry),
			TransactionId: transaction, Receipt: actAs(failed, members[0]), ObservationReceipts: observed,
			Failure: &operatorv1.InferenceDispatchFailure{Error: "provider asked to retry later", Receipt: failed},
		})
	}
	result := &operatorv1.InferenceResult{ProviderAttemptId: governance.ConsensusVoteAttemptID(hash, members[0], uint32(retries)), RetryCount: uint32(retries)}
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	completed := ledger.inference("vote-completed")
	completed.ResultSummary = digest
	actAs(completed, members[0])
	failed := actAs(ledger.inferenceFailed("vote-failed"), members[1])
	tool := ledger.executed("tool", string(constants.ActionTypeMcpCall))
	tool.TransactionHash = hash
	ledger.signer.Resign(t, tool)
	after := ledger.executed("after", string(constants.ActionTypeFsRead))

	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	trace.Roster[0].Bracket = bracketOf(before, after)
	trace.ModelCalls = []AgentModelCall{completedModelCall("turn", model)}
	trace.ToolCalls = []AgentToolCall{allowedToolCall("tool", tool)}
	trace.ToolCalls[0].ConsensusInferences = governance.WrapConsensusInferences([]*operatorv1.ConsensusVoteInference{
		{SubjectTransactionId: "tool", SubjectTransactionHash: hash, MemberAppId: members[0], ProviderAttemptId: result.ProviderAttemptId, RetryCount: uint32(retries),
			TransactionId: "vote-completed", Receipt: completed, Result: result, SupersededAttempts: superseded},
		{SubjectTransactionId: "tool", SubjectTransactionHash: hash, MemberAppId: members[1], ProviderAttemptId: governance.ConsensusVoteAttemptID(hash, members[1], 0),
			TransactionId: "vote-failed", Receipt: failed, Failure: &operatorv1.InferenceDispatchFailure{Receipt: failed}},
	})
	return trace, ledger.signer
}

// retriedVote is the first member's record in trace.
func retriedVote(trace *AgentTrace) *operatorv1.ConsensusVoteInference {
	return trace.ToolCalls[0].ConsensusInferences[0].ConsensusVoteInference
}

func TestVerifyAgentTraceEvidence_ClaimsSupersededVoteAttempts(t *testing.T) {
	for _, retries := range []int{1, 2, int(inferencedispatch.MaxTransientRetries())} {
		t.Run(fmt.Sprintf("%d superseded attempts, every one claimed", retries), func(t *testing.T) {
			trace, _ := retriedVoteTrace(t, retries)
			require.NoError(t, VerifyAgentTraceEvidence(trace))
		})
	}

	for _, test := range []struct {
		name    string
		retries int
		corrupt func(t *testing.T, trace *AgentTrace, signer oracletest.Signer)
		cause   error
	}{
		{name: "tampered superseded signature", retries: 2, corrupt: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
			retriedVote(trace).SupersededAttempts[1].Receipt.Signature = "00"
		}},
		{name: "superseded observation dropped leaves an unclaimed execution", retries: 2, corrupt: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
			retriedVote(trace).SupersededAttempts[0].ObservationReceipts = nil
		}},
		{name: "tampered superseded observation", retries: 2, corrupt: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
			retriedVote(trace).SupersededAttempts[0].ObservationReceipts[0].Signature = "00"
		}},
		{name: "superseded attempt dropped consistently, its execution unclaimed", retries: 2, corrupt: func(t *testing.T, trace *AgentTrace, signer oracletest.Signer) {
			t.Helper()
			// The vote is rewritten as a self-consistent one-retry chain and its
			// receipt re-signed, so only the ledger can show the dropped attempt.
			vote := retriedVote(trace)
			vote.SupersededAttempts = vote.SupersededAttempts[:1]
			vote.RetryCount = 1
			vote.ProviderAttemptId = governance.ConsensusVoteAttemptID(vote.SubjectTransactionHash, vote.MemberAppId, 1)
			vote.Result.ProviderAttemptId, vote.Result.RetryCount = vote.ProviderAttemptId, 1
			digest, err := models.ComputeInferenceResultDigest(vote.Result)
			require.NoError(t, err)
			vote.Result.ResultDigest, vote.Receipt.ResultSummary = digest, digest
			signer.Resign(t, vote.Receipt)
		}, cause: constants.ErrConformanceUnclaimedExecution},
		{name: "retry_count disagrees with the attempts behind it", retries: 2, cause: constants.ErrEvaluationEvidenceTraceMismatch, corrupt: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
			retriedVote(trace).RetryCount = 1
		}},
		{name: "superseded attempt ID for another ordinal", retries: 2, cause: constants.ErrEvaluationEvidenceTraceMismatch, corrupt: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
			retriedVote(trace).SupersededAttempts[1].ProviderAttemptId = governance.ConsensusVoteAttemptID("hash-tool", retriedVote(trace).MemberAppId, 0)
		}},
		{name: "superseded attempt is not transient", retries: 2, cause: constants.ErrEvaluationEvidenceTraceMismatch, corrupt: func(t *testing.T, trace *AgentTrace, signer oracletest.Signer) {
			t.Helper()
			receipt := retriedVote(trace).SupersededAttempts[0].Receipt
			receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
			// Re-signed by the same Operator: only the transient claim is false.
			signer.Resign(t, receipt)
		}},
		{name: "nested superseded attempts", retries: 2, cause: constants.ErrEvaluationEvidenceTraceMismatch, corrupt: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
			vote := retriedVote(trace)
			vote.SupersededAttempts[1].SupersededAttempts = vote.SupersededAttempts[:1]
		}},
		{name: "more retries than the policy allows", retries: int(inferencedispatch.MaxTransientRetries()) + 1, cause: constants.ErrEvaluationEvidenceTraceMismatch},
		{name: "superseded attempt of another member", retries: 1, cause: constants.ErrEvaluationEvidenceTraceMismatch, corrupt: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
			retriedVote(trace).SupersededAttempts[0].MemberAppId = constants.TribunalConsensusMemberAppIDs()[2]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace, signer := retriedVoteTrace(t, test.retries)
			if test.corrupt != nil {
				test.corrupt(t, trace, signer)
			}
			err := VerifyAgentTraceEvidence(trace)
			require.Error(t, err)
			if test.cause != nil {
				require.ErrorIs(t, err, test.cause)
			}
		})
	}
}
