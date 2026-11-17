// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	retrySubjectID   = "subject-tx"
	retrySubjectHash = "subject-hash"
)

func retryMember() string { return constants.TribunalConsensusMemberAppIDs()[0] }

// transientAttempt is a superseded vote attempt: the provider asked to retry
// later, and the Inference Operator signed that as a FAILED receipt.
func transientAttempt(retry uint32) *operatorv1.ConsensusVoteInference {
	transaction := "failed-" + string(rune('a'+retry))
	return &operatorv1.ConsensusVoteInference{
		SubjectTransactionId:   retrySubjectID,
		SubjectTransactionHash: retrySubjectHash,
		MemberAppId:            retryMember(),
		ProviderAttemptId:      governance.ConsensusVoteAttemptID(retrySubjectHash, retryMember(), retry),
		RetryCount:             retry,
		TransactionId:          transaction,
		Receipt: &operatorv1.EventReceipt{
			TransactionId: transaction,
			ActionType:    string(constants.ActionTypeInference),
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FailureCode:   operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT,
		},
		Failure: &operatorv1.InferenceDispatchFailure{Error: "provider asked to retry later"},
	}
}

// retriedVote is a vote that needed retries superseded attempts. completed
// ends it on an answer; otherwise the last attempt fails like the others.
func retriedVote(t *testing.T, retries uint32, completed bool) *operatorv1.ConsensusVoteInference {
	t.Helper()
	vote := transientAttempt(retries)
	for retry := range retries {
		vote.SupersededAttempts = append(vote.SupersededAttempts, transientAttempt(retry))
	}
	if !completed {
		return vote
	}
	result := &operatorv1.InferenceResult{ProviderAttemptId: vote.ProviderAttemptId, RetryCount: retries}
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	vote.TransactionId = "completed"
	vote.Result = result
	vote.Failure = nil
	vote.Receipt = &operatorv1.EventReceipt{
		TransactionId: "completed",
		ActionType:    string(constants.ActionTypeInference),
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary: digest,
	}
	return vote
}

func TestValidateConsensusInference_AcceptsAVoteThatNeededRetries(t *testing.T) {
	t.Parallel()
	for name, vote := range map[string]*operatorv1.ConsensusVoteInference{
		"no retry, completed":            retriedVote(t, 0, true),
		"no retry, failed":               retriedVote(t, 0, false),
		"one retry, completed":           retriedVote(t, 1, true),
		"every retry, completed":         retriedVote(t, inferencedispatch.MaxTransientRetries(), true),
		"every retry, exhausted":         retriedVote(t, inferencedispatch.MaxTransientRetries(), false),
		"last retry could not be placed": withoutReceipt(retriedVote(t, 2, false)),
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidateConsensusInference(vote))
		})
	}
}

// withoutReceipt is a final attempt the Gateway could not even dispatch: it
// has a failure and no receipt.
func withoutReceipt(vote *operatorv1.ConsensusVoteInference) *operatorv1.ConsensusVoteInference {
	vote.Receipt = nil
	vote.TransactionId = ""
	return vote
}

func TestValidateConsensusInference_RejectsForgedRetryChains(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		corrupt func(vote *operatorv1.ConsensusVoteInference)
	}{
		{name: "superseded attempt ID for another ordinal", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[1].ProviderAttemptId = governance.ConsensusVoteAttemptID(retrySubjectHash, retryMember(), 0)
		}},
		{name: "superseded attempt ID has no retry suffix", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].ProviderAttemptId = governance.ConsensusVoteAttemptID(retrySubjectHash, retryMember(), 2)
		}},
		{name: "final attempt ID is for another ordinal", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.ProviderAttemptId = governance.ConsensusVoteAttemptID(retrySubjectHash, retryMember(), 0)
			v.Result.ProviderAttemptId = v.ProviderAttemptId
			digest, err := models.ComputeInferenceResultDigest(v.Result)
			require.NoError(t, err)
			v.Result.ResultDigest, v.Receipt.ResultSummary = digest, digest
		}},
		{name: "superseded attempt's ordinal is not its index", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[1].RetryCount = 0
		}},
		{name: "superseded attempt is not transient", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[1].Receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
		}},
		{name: "superseded attempt is a governance rejection", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].Receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED
		}},
		{name: "superseded attempt completed", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].Receipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
			v.SupersededAttempts[0].Receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_UNSPECIFIED
		}},
		{name: "superseded attempt has a result", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].Result = &operatorv1.InferenceResult{}
		}},
		{name: "superseded attempt records no failure", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].Failure = nil
		}},
		{name: "superseded attempt has no receipt", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].Receipt = nil
		}},
		{name: "superseded attempt's receipt is another transaction", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].TransactionId = "someone-else"
		}},
		{name: "superseded attempt's receipt is not an inference", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].Receipt.ActionType = string(constants.ActionTypeExecuteBash)
		}},
		{name: "nested superseded attempts", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[1].SupersededAttempts = []*operatorv1.ConsensusVoteInference{transientAttempt(0)}
		}},
		{name: "one receipt fills two attempts", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[1].Receipt.TransactionId = v.SupersededAttempts[0].Receipt.TransactionId
			v.SupersededAttempts[1].TransactionId = v.SupersededAttempts[0].TransactionId
		}},
		{name: "superseded receipt is the final attempt's", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].Receipt.TransactionId = v.TransactionId
			v.SupersededAttempts[0].TransactionId = v.TransactionId
		}},
		{name: "superseded attempt of another member", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].MemberAppId = constants.TribunalConsensusMemberAppIDs()[1]
		}},
		{name: "superseded attempt of another subject hash", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].SubjectTransactionHash = "other-hash"
		}},
		{name: "superseded attempt of another subject transaction", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts[0].SubjectTransactionId = "other-tx"
		}},
		{name: "retry_count above the attempts behind it", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.RetryCount = 3
		}},
		{name: "retry_count below the attempts behind it", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.RetryCount = 1
		}},
		{name: "an attempt dropped from the chain", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.SupersededAttempts = v.SupersededAttempts[1:]
		}},
		{name: "result echoes another retry_count", corrupt: func(v *operatorv1.ConsensusVoteInference) {
			v.Result.RetryCount = 1
			digest, err := models.ComputeInferenceResultDigest(v.Result)
			require.NoError(t, err)
			v.Result.ResultDigest, v.Receipt.ResultSummary = digest, digest
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			vote := retriedVote(t, 2, true)
			require.NoError(t, ValidateConsensusInference(vote), "the fixture is a valid chain before it is forged")
			test.corrupt(vote)
			require.ErrorIs(t, ValidateConsensusInference(vote), constants.ErrEvaluationEvidenceTraceMismatch)
		})
	}
}

func TestValidateConsensusInference_RetryCountOfAVoteWithNoAttemptsBehindIt(t *testing.T) {
	t.Parallel()
	vote := retriedVote(t, 0, true)
	vote.RetryCount = 1
	require.ErrorIs(t, ValidateConsensusInference(vote), constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestValidateConsensusInference_RejectsMoreRetriesThanThePolicyAllows(t *testing.T) {
	t.Parallel()
	// Every attempt is individually well formed and the chain is consistent;
	// only the bound the retry policy sets is exceeded.
	require.ErrorIs(t, ValidateConsensusInference(retriedVote(t, inferencedispatch.MaxTransientRetries()+1, true)), constants.ErrEvaluationEvidenceTraceMismatch)
	require.ErrorIs(t, ValidateConsensusInference(retriedVote(t, inferencedispatch.MaxTransientRetries()+1, false)), constants.ErrEvaluationEvidenceTraceMismatch)
}
