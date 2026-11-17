// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package verify

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TestVerifyClaimsEverySupersededVoteAttempt proves a member's retried vote is
// accounted for attempt by attempt. Each superseded attempt is an execution on
// its Operator's ledger, so the case's bracket holds it exactly when the vote
// record claims it, and its receipt is verified like any other.
func TestVerifyClaimsEverySupersededVoteAttempt(t *testing.T) {
	invalid := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE
	pass := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
	for _, test := range []struct {
		name    string
		retries int
		remote  bool
		corrupt func(call *operatorv1.ConsensusVoteInference, signer oracletest.Signer)
		// unclaimed drops the superseded attempts from the evidence while
		// their ledger entries stay in the bracket.
		unclaimed bool
		// observer signs an observation receipt on the first superseded attempt.
		observer bool
		want     evalv1.EvaluationVerdictStatus
	}{
		{name: "two superseded attempts on the case runtime", retries: 2, want: pass},
		{name: "two superseded attempts on a remote inference runtime", retries: 2, remote: true, want: pass},
		{name: "superseded observation on another runtime", retries: 1, observer: true, want: pass},
		{name: "a superseded attempt's ledger entry is not claimed", retries: 2, unclaimed: true, want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "tampered superseded receipt", retries: 2, corrupt: func(c *operatorv1.ConsensusVoteInference, signer oracletest.Signer) {
			c.SupersededAttempts[0].Receipt.Signature = "00"
		}, want: invalid},
		{name: "tampered remote superseded receipt", retries: 2, remote: true, corrupt: func(c *operatorv1.ConsensusVoteInference, signer oracletest.Signer) {
			c.SupersededAttempts[1].Receipt.Signature = "00"
		}, want: invalid},
		{name: "tampered superseded observation", retries: 1, observer: true, corrupt: func(c *operatorv1.ConsensusVoteInference, signer oracletest.Signer) {
			c.SupersededAttempts[0].ObservationReceipts[0].Signature = "00"
		}, want: invalid},
		{name: "superseded receipt is not transient", retries: 2, corrupt: func(c *operatorv1.ConsensusVoteInference, signer oracletest.Signer) {
			// Signed by the Operator, so only the missing transient code is wrong.
			c.SupersededAttempts[0].Receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
			signer.Resign(t, c.SupersededAttempts[0].Receipt)
		}, want: invalid},
		{name: "superseded attempt ID for another ordinal", retries: 2, corrupt: func(c *operatorv1.ConsensusVoteInference, signer oracletest.Signer) {
			c.SupersededAttempts[1].ProviderAttemptId = governance.ConsensusVoteAttemptID(c.SubjectTransactionHash, c.MemberAppId, 0)
		}, want: invalid},
		{name: "retry_count disagrees with the attempts behind it", retries: 2, corrupt: func(c *operatorv1.ConsensusVoteInference, signer oracletest.Signer) {
			c.RetryCount = 1
		}, want: invalid},
		{name: "nested superseded attempts", retries: 2, corrupt: func(c *operatorv1.ConsensusVoteInference, signer oracletest.Signer) {
			c.SupersededAttempts[1].SupersededAttempts = []*operatorv1.ConsensusVoteInference{c.SupersededAttempts[0]}
		}, want: invalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			rec := f.record()
			ev := caseEvidence(t, rec, caseDirect)
			env := &commonv1.GovernanceEnvelope{ActionType: string(constants.ActionTypeFileEdit), Payload: []byte("target")}
			hash, err := govpkg.GenerateMessageID(env)
			require.NoError(t, err)
			env.Id = hash
			env.TransactionHash = hash
			member := constants.TribunalConsensusMemberAppIDs()[0]

			signer, runtime := f.gateway, oracle.GatewayRuntimeID
			if test.remote {
				signer, runtime = oracletest.NewSigner(t), "inference"
				rec.ConsensusAnchors = append(rec.ConsensusAnchors, signer.Anchor(runtime))
			}
			// Each attempt takes the next link in its runtime's ledger.
			ledger := "h0-" + caseDirect
			link := func(name string) (prior, hash string) {
				prior, ledger = ledger, "h-"+name
				return prior, ledger
			}
			actAs := func(receipt *operatorv1.EventReceipt) {
				for _, stage := range receipt.DeterministicStageEvidence {
					stage.ActingAppId = member
				}
				signer.Resign(t, receipt)
			}
			newCall := func(retry uint32, receipt *operatorv1.EventReceipt) *operatorv1.ConsensusVoteInference {
				return &operatorv1.ConsensusVoteInference{
					SubjectTransactionId: hash, SubjectTransactionHash: hash, MemberAppId: member,
					ProviderAttemptId: governance.ConsensusVoteAttemptID(hash, member, retry), RetryCount: retry,
					TransactionId: receipt.GetTransactionId(), Receipt: receipt,
				}
			}

			var superseded []*operatorv1.ConsensusVoteInference
			for retry := range test.retries {
				opts := receiptOpts(caseDirect, runtime, fmt.Sprintf("vote-failed-%d", retry), operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED)
				opts.ActionType = string(constants.ActionTypeInference)
				prior, next := link(fmt.Sprintf("failed-%d", retry))
				failed := signer.ExecutionFailed(t, opts, prior, next)
				failed.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT
				actAs(failed)
				attempt := newCall(uint32(retry), failed)
				attempt.Failure = &operatorv1.InferenceDispatchFailure{Error: "provider asked to retry later", Receipt: failed}
				if test.observer && retry == 0 {
					observer := oracletest.NewSigner(t)
					rec.ConsensusAnchors = append(rec.ConsensusAnchors, observer.Anchor("observer"))
					observation := receiptOpts(caseDirect, "observer", "observation", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED)
					observation.ActionType = string(constants.ActionTypeProviderBoundaryObservation)
					attempt.ObservationReceipts = []*operatorv1.EventReceipt{observer.Executed(t, observation, "observer-before", "observer-after")}
				}
				superseded = append(superseded, attempt)
			}

			retry := uint32(test.retries)
			result := &operatorv1.InferenceResult{ProviderAttemptId: governance.ConsensusVoteAttemptID(hash, member, retry), RetryCount: retry}
			digest, err := models.ComputeInferenceResultDigest(result)
			require.NoError(t, err)
			result.ResultDigest = digest
			opts := receiptOpts(caseDirect, runtime, "vote", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED)
			opts.ActionType = string(constants.ActionTypeInference)
			opts.ResultSummary = digest
			prior, next := link("vote")
			vote := signer.Executed(t, opts, prior, next)
			actAs(vote)
			call := newCall(retry, vote)
			call.Result = result
			call.SupersededAttempts = superseded
			if test.unclaimed {
				call.SupersededAttempts, call.RetryCount = nil, 0
				call.ProviderAttemptId = governance.ConsensusVoteAttemptID(hash, member, 0)
				call.Result.ProviderAttemptId, call.Result.RetryCount = call.ProviderAttemptId, 0
				resultDigest, err := models.ComputeInferenceResultDigest(call.Result)
				require.NoError(t, err)
				call.Result.ResultDigest, vote.ResultSummary = resultDigest, resultDigest
				signer.Resign(t, vote)
			}

			// The case's own receipt follows the last vote attempt on the
			// case runtime's ledger; a remote runtime's ledger is not in it.
			actual := ev.Observations[0].Receipt
			caseLink := "h0-" + caseDirect
			if !test.remote {
				caseLink = ledger
			}
			for _, stage := range actual.DeterministicStageEvidence {
				if stage.GetCommitmentHash() != "" {
					stage.PriorCommitmentHash = caseLink
				}
			}
			f.gateway.Resign(t, actual)

			if test.corrupt != nil {
				test.corrupt(call, signer)
			}
			req, err := protojson.Marshal(env)
			require.NoError(t, err)
			resp, err := protojson.Marshal(&operatorv1.GovernanceDeliberationResult{Envelope: env, ConsensusInferences: []*operatorv1.ConsensusVoteInference{call}})
			require.NoError(t, err)
			ev.Exchanges = []*evalv1.ConformanceExchange{{Method: http.MethodPost, Url: "https://gateway" + constants.APIPaths.GovernanceDeliberations, Status: http.StatusOK, RequestBody: req, ResponseBody: resp}}

			set, err := verifyAgainst(rec, f.applicable)
			require.NoError(t, err)
			verdict := verdictOf(t, set, caseDirect)
			assert.Equal(t, test.want, verdict.GetStatus())
			if test.unclaimed {
				assert.Equal(t, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNCLAIMED_EXECUTION, verdict.GetFailureClass(), "the bracket holds executions no vote record claims")
			}
		})
	}
}
