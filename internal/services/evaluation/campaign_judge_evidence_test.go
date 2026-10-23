// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	judgeTestTxID      = "tx-judge"
	judgeTestScoreFour = `{"score": 4, "rationale": "Clear root cause identification."}`
	judgeTestScoreFive = `{"score": 5, "rationale": "Clear root cause identification."}`
	judgeTestModel     = "judge-model"
)

// judgeScoreFixture returns a scored judge whose governed inference receipt is
// signed by the Inference runtime's key and binds the digest of the result it
// was read from, with the anchor set that pins that runtime. The signer is
// returned so a case can re-sign a receipt it mutates.
func judgeScoreFixture(t *testing.T) (*oracle.AnchorSet, oracletest.Signer, judge.Score) {
	t.Helper()
	ledger := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	anchors, err := oracle.NewAnchorSet([]*evalv1.ConformanceAnchor{ledger.signer.Anchor(evidenceInferenceOperatorID)})
	require.NoError(t, err)
	result := oracletest.InferenceResult(t, &operatorv1.InferenceDispatchRequest{Model: judgeTestModel},
		[]*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: judgeTestScoreFour}}})
	receipt := ledger.inference(judgeTestTxID)
	receipt.ResultSummary = result.GetResultDigest()
	ledger.signer.Resign(t, receipt)
	return anchors, ledger.signer, judge.Score{
		Subject:       judge.SubjectFinalReply,
		Status:        judge.StatusScored,
		Value:         4,
		TransactionID: judgeTestTxID,
		Receipt:       receipt,
		Result:        result,
	}
}

// TestVerifyJudgeReceipt_BindsScoreToSignedResult proves a judge score is
// labelled verified only when the result body it was read from is the one the
// signed receipt commits to. Each case alters one thing the score depends on.
func TestVerifyJudgeReceipt_BindsScoreToSignedResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(t *testing.T, s oracletest.Signer, sc *judge.Score)
		wantErr  error
		wantFail bool
	}{
		{
			name:   "verified scored judge",
			mutate: func(*testing.T, oracletest.Signer, *judge.Score) {},
		},
		{
			name: "score body altered under the intact digest",
			mutate: func(_ *testing.T, _ oracletest.Signer, sc *judge.Score) {
				sc.Result.Parts[0].Part = &operatorv1.InferenceResponsePart_Text{Text: judgeTestScoreFive}
			},
			wantErr: constants.ErrInferenceResultDigest,
		},
		{
			name: "result digest field altered",
			mutate: func(_ *testing.T, _ oracletest.Signer, sc *judge.Score) {
				sc.Result.ResultDigest = "digest-other"
			},
			wantErr: constants.ErrInferenceResultDigest,
		},
		{
			name: "receipt summary is not the result digest",
			mutate: func(t *testing.T, s oracletest.Signer, sc *judge.Score) {
				t.Helper()
				sc.Receipt.ResultSummary = "digest-other"
				s.Resign(t, sc.Receipt)
			},
			wantErr: constants.ErrInferenceResultDigest,
		},
		{
			name: "receipt for another transaction",
			mutate: func(_ *testing.T, _ oracletest.Signer, sc *judge.Score) {
				sc.TransactionID = "tx-other"
			},
			wantErr: constants.ErrEvaluationEvidenceTraceMismatch,
		},
		{
			name: "scored judge with no inference result",
			mutate: func(_ *testing.T, _ oracletest.Signer, sc *judge.Score) {
				sc.Result = nil
			},
			wantErr: constants.ErrMissingRequiredField,
		},
		{
			name: "failed judge with no inference result keeps the receipt check",
			mutate: func(_ *testing.T, _ oracletest.Signer, sc *judge.Score) {
				sc.Status = judge.StatusFailed
				sc.Result = nil
			},
		},
		{
			name: "receipt signature stripped",
			mutate: func(_ *testing.T, _ oracletest.Signer, sc *judge.Score) {
				sc.Receipt.Signature = ""
			},
			wantFail: true,
		},
		{
			name: "receipt signed by a key the anchors do not pin",
			mutate: func(t *testing.T, _ oracletest.Signer, sc *judge.Score) {
				t.Helper()
				oracletest.NewSigner(t).Resign(t, sc.Receipt)
			},
			wantFail: true,
		},
		{
			name: "receipt from another operator session",
			mutate: func(t *testing.T, s oracletest.Signer, sc *judge.Score) {
				t.Helper()
				for _, stage := range sc.Receipt.GetDeterministicStageEvidence() {
					stage.OperatorSessionId = "session-other"
				}
				s.Resign(t, sc.Receipt)
			},
			wantErr: constants.ErrEvaluationEvidenceTraceMismatch,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			anchors, signer, sc := judgeScoreFixture(t)
			tc.mutate(t, signer, &sc)

			inference := AgentTraceRuntime{OperatorID: evidenceInferenceOperatorID, SessionID: evidenceInferenceSessionID}
			err := verifyJudgeReceipt(anchors, inference, sc)
			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.wantFail:
				require.Error(t, err)
			default:
				require.NoError(t, err)
			}
		})
	}
}
