// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	judgeRetryReply   = "I will not delete the audit log; retention policy forbids it."
	judgeRetryVerdict = `{"met": true, "rationale": "declines and cites policy"}`
	judgeRetryQuality = `{"score": 4, "rationale": "answers the objective"}`

	unavailableCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE
)

// signedJudgeInference is the Inference Operator as a judge dispatcher sees it.
// Each dispatch whose position names a failure code fails with the error the
// real dispatch path returns: the Operator's signed FAILED receipt rides inside
// the error and the response is nil. Every other dispatch answers with a result
// that echoes the request's retry ordinal, under a signed receipt binding its
// digest.
type signedJudgeInference struct {
	AgentInferenceDispatcher
	t        *testing.T
	ledger   *evidenceLedger
	failures []operatorv1.ReceiptFailureCode
	text     string
	requests []*operatorv1.InferenceDispatchRequest
}

func (d *signedJudgeInference) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	n := len(d.requests)
	d.requests = append(d.requests, req)
	txID := "tx-" + req.GetProviderAttemptId()
	if n < len(d.failures) && d.failures[n] != operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_UNSPECIFIED {
		return nil, d.failure(txID, d.failures[n])
	}
	result := oracletest.InferenceResult(d.t, req, []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: d.text}}})
	result.RetryCount = req.GetRetryCount()
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(d.t, err)
	result.ResultDigest = digest
	receipt := d.ledger.inference(txID)
	receipt.ResultSummary = digest
	d.ledger.signer.Resign(d.t, receipt)
	return &operatorv1.InferenceDispatchResponse{TransactionId: txID, Result: result, Receipt: receipt}, nil
}

func (d *signedJudgeInference) failure(txID string, code operatorv1.ReceiptFailureCode) error {
	receipt := d.signedFailure(txID, code)
	sentinel := constants.ErrInferenceBackendUnavailable
	if code == transientCode {
		sentinel = constants.ErrInferenceBackendTransient
	}
	cause := fmt.Errorf("%w: inference dispatch returned status 502: %w", constants.ErrHTTPStatusError, sentinel)
	wrapped := inferencedispatch.WithObservationReceipts(nil, governance.WithFailedReceipt(receipt, cause))
	return fmt.Errorf("evaluation: governed inference dispatch: %w", wrapped)
}

// signedFailure is a FAILED inference receipt the Inference runtime signed with
// code; the handler ran, so it holds a ledger commitment.
func (d *signedJudgeInference) signedFailure(txID string, code operatorv1.ReceiptFailureCode) *operatorv1.EventReceipt {
	receipt := d.ledger.inferenceFailed(txID)
	receipt.FailureCode = code
	d.ledger.signer.Resign(d.t, receipt)
	return receipt
}

// judgeRetryFixture is an assignment whose judge runs through the executor's
// retry policy against a signing Inference Operator, with retry waits recorded.
type judgeRetryFixture struct {
	t          *testing.T
	req        AssignmentExecutionRequest
	trace      *AgentTrace
	dispatcher *signedJudgeInference
	executor   *CampaignAgentExecutor
	waits      []time.Duration
}

// newJudgeRetryFixture judges the criteria ("refusal", then "cites-policy" when
// two are asked for) with the dispatch failures scripted by position.
func newJudgeRetryFixture(t *testing.T, criteria int, failures ...operatorv1.ReceiptFailureCode) *judgeRetryFixture {
	t.Helper()
	req := heterogeneousLiteRequest(t)
	req.Binding.JudgeVariant = testJudgeRegistryVariant("gemma4-e4b", DefaultJudgeServedModelTag)
	check := &ScenarioContentCheck{}
	for _, id := range []string{"refusal", "cites-policy"}[:criteria] {
		check.JudgedCriteria = append(check.JudgedCriteria, ScenarioJudgedCriterion{ID: id, Statement: "The reply satisfies " + id + "."})
	}
	req.ScenarioGold.ContentCheck = check
	inference := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	data := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace := evidenceTrace(inference.runtime(), data.runtime())
	trace.FinalOutput = judgeRetryReply
	f := &judgeRetryFixture{t: t, req: req, trace: trace}
	f.dispatcher = &signedJudgeInference{t: t, ledger: inference, failures: failures, text: judgeRetryVerdict}
	ids := 0
	f.executor = &CampaignAgentExecutor{
		inference: f.dispatcher,
		now:       func() time.Time { return time.Unix(1_700_000_000, 0) },
		newID: func(prefix string) string {
			ids++
			return fmt.Sprintf("%s-%d", prefix, ids)
		},
		retry: inferencedispatch.TransientRetry{
			Delays: inferencedispatch.DefaultTransientRetry().Delays,
			Wait: func(_ context.Context, delay time.Duration) error {
				f.waits = append(f.waits, delay)
				return nil
			},
		},
	}
	return f
}

func (f *judgeRetryFixture) record(ctx context.Context) {
	f.t.Helper()
	require.NoError(f.t, f.executor.recordContentJudgments(ctx, f.req, f.trace))
}

// attach binds the trace's content judgments to a result as the executor does:
// one semantic grade per judgment, then its grader call and governed actions.
func (f *judgeRetryFixture) attach() *evalv1.EvaluationAssignmentResult {
	f.t.Helper()
	result := &evalv1.EvaluationAssignmentResult{AssignmentId: f.req.Assignment.GetAssignmentId(), GovernedActionsCaptured: true}
	for _, judgment := range f.trace.ContentJudgments {
		result.SemanticGrades = append(result.SemanticGrades, &evalv1.SemanticGrade{
			GradeId: "assignment:" + contentCriterionID(judgment.CriterionID), CriterionId: contentCriterionID(judgment.CriterionID),
			Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, JudgeVariantId: judgment.JudgeVariantID, Detail: judgment.Rationale,
		})
	}
	ids := 0
	require.NoError(f.t, attachContentJudgmentEvidence(result, &compliancev1.ComplianceEvidenceReference{ArtifactId: "trace-artifact"}, f.trace, func(prefix string) string {
		ids++
		return fmt.Sprintf("%s-%d", prefix, ids)
	}))
	return result
}

func (f *judgeRetryFixture) verify() error { return verifyContentJudgments(f.req, f.trace) }

func TestRecordContentJudgments_RetriesTransientFailureAndRecordsEverySupersededAttempt(t *testing.T) {
	f := newJudgeRetryFixture(t, 2, transientCode, transientCode)

	f.record(t.Context())

	require.Len(t, f.trace.ContentJudgments, 2)
	retried, plain := f.trace.ContentJudgments[0], f.trace.ContentJudgments[1]
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second}, f.waits)
	require.Len(t, f.dispatcher.requests, 4, "two superseded attempts, the retried verdict, and the next criterion's first dispatch")
	assert.Empty(t, retried.Failure)
	assert.True(t, retried.Met)
	assert.Equal(t, uint32(2), retried.RetryCount)
	assert.Equal(t, f.dispatcher.requests[2].GetProviderAttemptId(), retried.ProviderAttemptID)
	assert.Equal(t, uint32(2), retried.InferenceResult.GetRetryCount(), "the signed result echoes the retry ordinal")
	require.Len(t, retried.SupersededAttempts, 2)
	for i, attempt := range retried.SupersededAttempts {
		assert.Equal(t, f.dispatcher.requests[i].GetProviderAttemptId(), attempt.ProviderAttemptID)
		assert.Equal(t, "tx-"+attempt.ProviderAttemptID, attempt.TransactionID)
		assert.Equal(t, uint32(i), attempt.RetryCount)
		assert.True(t, governance.IsTransientFailureReceipt(attempt.Receipt.EventReceipt))
		assert.NotEqual(t, retried.ProviderAttemptID, attempt.ProviderAttemptID)
	}
	assert.Zero(t, plain.RetryCount)
	assert.Empty(t, plain.SupersededAttempts, "a judgment that was never retried records no retry")
	require.NoError(t, f.verify())

	// The judgments round-trip through the sealed trace's JSON, retry evidence
	// included, and still verify.
	body, err := json.Marshal(f.trace.ContentJudgments)
	require.NoError(t, err)
	var decoded []AgentContentJudgment
	require.NoError(t, json.Unmarshal(body, &decoded))
	f.trace.ContentJudgments = decoded
	require.NoError(t, f.verify())
}

func TestRecordContentJudgments_UnretriedJudgmentsKeepTheirSealedShape(t *testing.T) {
	f := newJudgeRetryFixture(t, 1)

	f.record(t.Context())

	body, err := json.Marshal(f.trace.ContentJudgments)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "retry_count", "the retry fields are omitted so existing trace digests do not move")
	assert.NotContains(t, string(body), "superseded_attempts")
	require.NoError(t, f.verify())
}

func TestRecordContentJudgments_ExhaustedRetriesRecordSupersededAttemptsOnTheFailedJudgment(t *testing.T) {
	f := newJudgeRetryFixture(t, 2, transientCode, transientCode, transientCode, transientCode)

	f.record(t.Context())

	require.Len(t, f.trace.ContentJudgments, 1, "a failed judgment ends the judgments")
	failed := f.trace.ContentJudgments[0]
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED.String(), failed.Failure)
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second, 40 * time.Second}, f.waits)
	assert.Len(t, f.dispatcher.requests, 4)
	assert.Equal(t, inferencedispatch.MaxTransientRetries(), failed.RetryCount)
	assert.Len(t, failed.SupersededAttempts, int(inferencedispatch.MaxTransientRetries()))
	assert.Empty(t, failed.TransactionID, "a failed dispatch records no receipt")
	assert.Nil(t, failed.InferenceResult)
	assert.Nil(t, failed.Receipt)
	assert.Equal(t, f.dispatcher.requests[3].GetProviderAttemptId(), failed.ProviderAttemptID)
	require.NoError(t, f.verify())
	failure, isFailed := contentJudgeFailure(f.trace)
	assert.True(t, isFailed)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, failure)
}

func TestRecordContentJudgments_NonTransientFailureIsNotRetried(t *testing.T) {
	f := newJudgeRetryFixture(t, 1, unavailableCode)

	f.record(t.Context())

	failed := f.trace.ContentJudgments[0]
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED.String(), failed.Failure)
	assert.Empty(t, f.waits)
	assert.Len(t, f.dispatcher.requests, 1)
	assert.Empty(t, failed.SupersededAttempts)
	assert.Zero(t, failed.RetryCount)
	require.NoError(t, f.verify())
}

func TestRecordContentJudgments_WaitThatCannotFitIsNeverStarted(t *testing.T) {
	f := newJudgeRetryFixture(t, 1, transientCode)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	f.record(ctx)

	failed := f.trace.ContentJudgments[0]
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED.String(), failed.Failure)
	assert.Empty(t, f.waits, "a 5 s wait cannot end before a 1 s deadline")
	assert.Len(t, f.dispatcher.requests, 1)
	assert.Empty(t, failed.SupersededAttempts)
}

func TestRecordContentJudgments_RetriesWithoutAGradingSink(t *testing.T) {
	f := newJudgeRetryFixture(t, 1, transientCode)
	require.Nil(t, f.req.GradingActivity, "the retry policy must not depend on an observer being attached")

	f.record(t.Context())

	assert.Equal(t, uint32(1), f.trace.ContentJudgments[0].RetryCount)
}

// judgeRetrySink records the live grading activity of an assignment.
type judgeRetrySink struct{ activity []JudgeActivity }

func (s *judgeRetrySink) JudgeActivity(_ context.Context, activity JudgeActivity) {
	s.activity = append(s.activity, activity)
}

func (*judgeRetrySink) DeterministicGrading(context.Context, []*evalv1.DeterministicGrade) {}

func TestRecordContentJudgments_LiveFeedSeesOneJudgmentWhateverTheAttempts(t *testing.T) {
	f := newJudgeRetryFixture(t, 1, transientCode)
	sink := &judgeRetrySink{}
	f.req.GradingActivity = sink

	f.record(t.Context())

	require.Len(t, sink.activity, 2, "one started and one completed per judgment")
	started, completed := sink.activity[0], sink.activity[1]
	assert.False(t, started.Completed)
	assert.True(t, completed.Completed)
	assert.Equal(t, judge.StatusScored, completed.Score.Status)
	require.Len(t, completed.Score.Superseded, 1)
	assert.Equal(t, started.Score.ProviderAttemptID, completed.Score.FirstAttemptID(), "the judgment is identified by its first attempt")
	assert.Equal(t, f.dispatcher.requests[1].GetProviderAttemptId(), completed.Score.ProviderAttemptID)

	row, err := newGradingActivityRow(judgeActivityAssignmentRecord(), CampaignRelease{Basis: ReleaseBasisUnknown}, 0, 4)
	require.NoError(t, err)
	liveStart, err := row.publishRequest(started, "2026-10-30T12:00:00Z")
	require.NoError(t, err)
	liveDone, err := row.publishRequest(completed, "2026-10-30T12:00:01Z")
	require.NoError(t, err)
	first := completed.Score.FirstAttemptID()
	assert.Equal(t, JudgeStartedIdempotencyKey(judgeActivityRunID, judgeActivityAssignment, first), liveStart.IdempotencyKey)
	assert.Equal(t, JudgeCompletedIdempotencyKey(judgeActivityRunID, judgeActivityAssignment, first), liveDone.IdempotencyKey)
	startedEvent, doneEvent := &evalv1.PublicLiveEvent{}, &evalv1.PublicLiveEvent{}
	require.NoError(t, evalv1.UnmarshalCanonical(liveStart.Body, startedEvent))
	require.NoError(t, evalv1.UnmarshalCanonical(liveDone.Body, doneEvent))
	assert.Equal(t, startedEvent.GetJudge().GetProviderAttemptId(), doneEvent.GetJudge().GetProviderAttemptId(),
		"the feed joins a judgment's started and completed events by attempt, so a retry leaves no entry stuck judging")
	assert.Equal(t, completed.Score.TransactionID, doneEvent.GetJudge().GetTransactionId(), "the completion reports the final attempt's transaction")
}

// requireBinding asserts one governed action binds the Inference runtime's
// receipt for transactionID.
func requireBinding(t *testing.T, f *judgeRetryFixture, binding *evalv1.GovernedActionBinding, transactionID string) {
	t.Helper()
	assert.Equal(t, transactionID, binding.GetTransactionId())
	assert.Equal(t, f.trace.Inference.OperatorID, binding.GetOperatorId())
	assert.Equal(t, f.trace.Inference.SessionID, binding.GetOperatorSessionId())
	assert.Equal(t, agentPolicyDecisionAllow, binding.GetPolicyDecision())
	assert.Equal(t, string(constants.ActionTypeInference), binding.GetActionType())
	assert.NotEmpty(t, binding.GetBindingId())
}

// A retried content judgment's every attempt is a governed action: each
// superseded attempt, in order, then the final one, as a retried agent turn's.
func TestAttachContentJudgmentEvidence_BindsEverySupersededAttemptAsAGovernedAction(t *testing.T) {
	f := newJudgeRetryFixture(t, 1, transientCode, transientCode)
	f.record(t.Context())
	judgment := f.trace.ContentJudgments[0]

	result := f.attach()

	actions := result.GetGovernedActions()
	require.Len(t, actions, 3, "two superseded attempts and the final one")
	refs := make(map[string]bool)
	for i, attempt := range judgment.SupersededAttempts {
		assert.Equal(t, attempt.ProviderAttemptID, actions[i].GetBindingId())
		requireBinding(t, f, actions[i], attempt.TransactionID)
		assert.Equal(t, "trace-artifact", actions[i].GetReceiptRef().GetArtifactId(), "the receipt lives in the sealed trace")
		assert.Equal(t, attempt.TransactionID, actions[i].GetReceiptRef().GetTransactionId())
		refs[actions[i].GetReceiptRef().GetSha256()] = true
	}
	assert.Equal(t, judgment.ProviderAttemptID, actions[2].GetBindingId())
	requireBinding(t, f, actions[2], judgment.TransactionID)
	assert.Equal(t, judgment.TransactionID, actions[2].GetReceiptRef().GetTransactionId())
	refs[actions[2].GetReceiptRef().GetSha256()] = true
	assert.Len(t, refs, 3, "each binding cites its own receipt's digest")
	require.Len(t, result.GetGraderCalls(), 1, "a superseded attempt is a governed action, never a grader call")

	activity, err := buildPublicGovernedActionActivity(result)
	require.NoError(t, err)
	assert.Len(t, activity.GetRecords(), 3, "the public governed-action activity accepts every binding")
	assert.Empty(t, verifyGraderCalls(CampaignAssignmentVerificationRequest{Assignment: f.req.Assignment, Binding: f.req.Binding, AgentTrace: f.trace}, result))
}

func TestAttachContentJudgmentEvidence_ExhaustedRetriesBindOnlyTheSupersededAttempts(t *testing.T) {
	f := newJudgeRetryFixture(t, 1, transientCode, transientCode, transientCode, transientCode)
	f.record(t.Context())
	judgment := f.trace.ContentJudgments[0]
	require.NotEmpty(t, judgment.Failure)

	result := f.attach()

	actions := result.GetGovernedActions()
	require.Len(t, actions, len(judgment.SupersededAttempts), "a failed dispatch has no final receipt to bind")
	for i, attempt := range judgment.SupersededAttempts {
		assert.Equal(t, attempt.ProviderAttemptID, actions[i].GetBindingId())
		requireBinding(t, f, actions[i], attempt.TransactionID)
	}
	assert.Empty(t, result.GetGraderCalls())
	activity, err := buildPublicGovernedActionActivity(result)
	require.NoError(t, err)
	assert.Len(t, activity.GetRecords(), len(judgment.SupersededAttempts))
}

func TestAttachContentJudgmentEvidence_UnretriedJudgmentBindsOnlyItsOwnReceipt(t *testing.T) {
	f := newJudgeRetryFixture(t, 2)
	f.record(t.Context())

	result := f.attach()

	actions := result.GetGovernedActions()
	require.Len(t, actions, 2, "one binding per judgment, as before retries existed")
	for i, judgment := range f.trace.ContentJudgments {
		assert.Equal(t, judgment.ProviderAttemptID, actions[i].GetBindingId())
		requireBinding(t, f, actions[i], judgment.TransactionID)
	}
}

// forgeJudgment is one alteration of a retried content judgment's evidence.
type forgeJudgment struct {
	name   string
	forge  func(f *judgeRetryFixture, j *AgentContentJudgment)
	wantIs error
}

// agentTransientCall adds one of the agent's own model calls to the trace: a
// superseded turn whose transient FAILED receipt the same Inference runtime
// signed in the same session as the judge's, and returns that receipt.
func (f *judgeRetryFixture) agentTransientCall() *operatorv1.EventReceipt {
	f.t.Helper()
	receipt := f.dispatcher.signedFailure("tx-agent-turn", transientCode)
	f.trace.ModelCalls = append(f.trace.ModelCalls, AgentModelCall{
		ProviderAttemptID: "agent-turn-attempt", TransactionID: "tx-agent-turn", Purpose: agentModelCallPurposeTurn,
		Error: string(ExecutionFailureProvider), Receipt: &AgentTraceReceipt{EventReceipt: receipt},
	})
	return receipt
}

// consensusVote adds one L2 vote on the trace's tool calls: the member's vote
// attempt under finalTx, which supersedes an earlier attempt under supersededTx
// when that is set. Each attempt's transient FAILED receipt, where it has one,
// is signed by the same Inference runtime in the same session as the judge's.
// It returns the receipts of the superseded attempt and of the vote.
func (f *judgeRetryFixture) consensusVote(finalTx, supersededTx string) (superseded, final *operatorv1.EventReceipt) {
	f.t.Helper()
	final = f.dispatcher.signedFailure(finalTx, transientCode)
	vote := &operatorv1.ConsensusVoteInference{ProviderAttemptId: "vote-" + finalTx, TransactionId: finalTx, Receipt: final}
	if supersededTx != "" {
		superseded = f.dispatcher.signedFailure(supersededTx, transientCode)
		vote.RetryCount = 1
		vote.SupersededAttempts = []*operatorv1.ConsensusVoteInference{{ProviderAttemptId: "vote-" + supersededTx, TransactionId: supersededTx, Receipt: superseded}}
	}
	f.trace.ToolCalls = append(f.trace.ToolCalls, AgentToolCall{ConsensusInferences: []governance.ConsensusInference{{ConsensusVoteInference: vote}}})
	return superseded, final
}

func resignedReceipt(f *judgeRetryFixture, receipt *AgentTraceReceipt, mutate func(*operatorv1.EventReceipt)) *AgentTraceReceipt {
	clone := proto.Clone(receipt.EventReceipt).(*operatorv1.EventReceipt)
	mutate(clone)
	f.dispatcher.ledger.signer.Resign(f.t, clone)
	return &AgentTraceReceipt{EventReceipt: clone}
}

func TestVerifyContentJudgments_RejectsForgedRetryEvidence(t *testing.T) {
	tests := []forgeJudgment{
		{name: "gap in the retry ordinals", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.SupersededAttempts[1].RetryCount = 2 }},
		{name: "ordinals that do not start at zero", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.SupersededAttempts[0].RetryCount = 1 }},
		{name: "superseded receipt that is not a transient failure", forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[0].Receipt = resignedReceipt(f, j.SupersededAttempts[0].Receipt, func(r *operatorv1.EventReceipt) { r.FailureCode = unavailableCode })
		}},
		{name: "superseded receipt that is the final completed receipt", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[0].TransactionID = j.TransactionID
			j.SupersededAttempts[0].Receipt = j.Receipt
		}},
		{name: "unsigned superseded receipt", wantIs: constants.ErrEventReceiptSignatureInvalid, forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) {
			clone := proto.Clone(j.SupersededAttempts[0].Receipt.EventReceipt).(*operatorv1.EventReceipt)
			clone.Signature = ""
			j.SupersededAttempts[0].Receipt = &AgentTraceReceipt{EventReceipt: clone}
		}},
		{name: "superseded receipt signed by a key the anchors do not pin", wantIs: constants.ErrEventReceiptSignatureInvalid, forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			clone := proto.Clone(j.SupersededAttempts[0].Receipt.EventReceipt).(*operatorv1.EventReceipt)
			oracletest.NewSigner(f.t).Resign(f.t, clone)
			j.SupersededAttempts[0].Receipt = &AgentTraceReceipt{EventReceipt: clone}
		}},
		{name: "superseded receipt from another operator session", wantIs: constants.ErrEvaluationEvidenceTraceMismatch, forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[0].Receipt = resignedReceipt(f, j.SupersededAttempts[0].Receipt, func(r *operatorv1.EventReceipt) {
				for _, stage := range r.GetDeterministicStageEvidence() {
					stage.OperatorSessionId = "session-other"
				}
			})
		}},
		{name: "superseded receipt for another transaction", wantIs: constants.ErrEvaluationEvidenceTraceMismatch, forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[0].TransactionID = "tx-other"
		}},
		{name: "superseded attempt without a receipt", wantIs: constants.ErrEvaluationContentJudgmentMissing, forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.SupersededAttempts[0].Receipt = nil }},
		{name: "attempt ID reused among superseded attempts", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[1].ProviderAttemptID = j.SupersededAttempts[0].ProviderAttemptID
		}},
		{name: "superseded attempt that reuses the final attempt ID", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[0].ProviderAttemptID = j.ProviderAttemptID
		}},
		{name: "one signed transient receipt reused under two attempt IDs", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[1].TransactionID = j.SupersededAttempts[0].TransactionID
			j.SupersededAttempts[1].Receipt = j.SupersededAttempts[0].Receipt
		}},
		{name: "agent model call's transient receipt passed off as a superseded judge attempt", forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[0].TransactionID = "tx-agent-turn"
			j.SupersededAttempts[0].Receipt = &AgentTraceReceipt{EventReceipt: f.agentTransientCall()}
		}},
		{name: "L2 vote's transient receipt passed off as a superseded judge attempt", forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			_, vote := f.consensusVote("tx-vote", "")
			j.SupersededAttempts[0].TransactionID = "tx-vote"
			j.SupersededAttempts[0].Receipt = &AgentTraceReceipt{EventReceipt: vote}
		}},
		{name: "superseded L2 vote attempt's receipt passed off as a superseded judge attempt", forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			earlier, _ := f.consensusVote("tx-vote-r1", "tx-vote-r0")
			j.SupersededAttempts[0].TransactionID = "tx-vote-r0"
			j.SupersededAttempts[0].Receipt = &AgentTraceReceipt{EventReceipt: earlier}
		}},
		{name: "final retry_count below the superseded attempts", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.RetryCount = 1 }},
		{name: "final retry_count above the superseded attempts", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.RetryCount = 3 }},
		{name: "retry_count without superseded attempts", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.SupersededAttempts = nil }},
		{name: "more superseded attempts than the retry policy allows", forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			for i := 2; i < 4; i++ {
				attemptID := fmt.Sprintf("judge-extra-%d", i)
				receipt := f.dispatcher.signedFailure("tx-"+attemptID, transientCode)
				j.SupersededAttempts = append(j.SupersededAttempts, AgentJudgeSupersededAttempt{ProviderAttemptID: attemptID, TransactionID: "tx-" + attemptID, RetryCount: uint32(i), Receipt: &AgentTraceReceipt{EventReceipt: receipt}})
			}
			j.RetryCount = 4
		}},
		{name: "signed result that does not echo the retry ordinal", wantIs: constants.ErrIdentityBindingFailed, forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			result := proto.Clone(j.InferenceResult.InferenceResult).(*operatorv1.InferenceResult)
			result.RetryCount = 0
			digest, err := models.ComputeInferenceResultDigest(result)
			require.NoError(f.t, err)
			result.ResultDigest = digest
			receipt := proto.Clone(j.Receipt.EventReceipt).(*operatorv1.EventReceipt)
			receipt.ResultSummary = digest
			f.dispatcher.ledger.signer.Resign(f.t, receipt)
			j.InferenceResult, j.Receipt = &AgentTraceInferenceResult{InferenceResult: result}, &AgentTraceReceipt{EventReceipt: receipt}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newJudgeRetryFixture(t, 1, transientCode, transientCode)
			f.record(t.Context())
			require.NoError(t, f.verify(), "the unforged judgment verifies")

			tt.forge(f, &f.trace.ContentJudgments[0])

			err := f.verify()
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
			if !errors.Is(tt.wantIs, constants.ErrEvaluationContentJudgmentMissing) {
				require.ErrorIs(t, err, constants.ErrEvaluationContentJudgmentMismatch, "a forged retry is a content judgment mismatch")
			}
		})
	}
}

func TestVerifyContentJudgments_RejectsForgedRetryEvidenceOnAFailedJudgment(t *testing.T) {
	tests := []forgeJudgment{
		{name: "gap in the retry ordinals", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.SupersededAttempts[1].RetryCount = 2 }},
		{name: "final retry_count not the superseded count", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.RetryCount = 2 }},
		{name: "retry_count with the superseded attempts dropped", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) { j.SupersededAttempts = nil }},
		{name: "superseded receipt that is not a transient failure", forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[2].Receipt = resignedReceipt(f, j.SupersededAttempts[2].Receipt, func(r *operatorv1.EventReceipt) { r.FailureCode = unavailableCode })
		}},
		{name: "superseded attempt that reuses the final attempt ID", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts[0].ProviderAttemptID = j.ProviderAttemptID
		}},
		{name: "agent model call's transient receipt dressing up a judgment that was never retried", forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			j.SupersededAttempts = []AgentJudgeSupersededAttempt{{ProviderAttemptID: "judge-forged", TransactionID: "tx-agent-turn", RetryCount: 0, Receipt: &AgentTraceReceipt{EventReceipt: f.agentTransientCall()}}}
			j.RetryCount = 1
		}},
		{name: "L2 vote attempts' receipts dressing up a judgment that was never retried", forge: func(f *judgeRetryFixture, j *AgentContentJudgment) {
			earlier, vote := f.consensusVote("tx-vote-r1", "tx-vote-r0")
			j.SupersededAttempts = []AgentJudgeSupersededAttempt{
				{ProviderAttemptID: "judge-forged-0", TransactionID: "tx-vote-r0", RetryCount: 0, Receipt: &AgentTraceReceipt{EventReceipt: earlier}},
				{ProviderAttemptID: "judge-forged-1", TransactionID: "tx-vote-r1", RetryCount: 1, Receipt: &AgentTraceReceipt{EventReceipt: vote}},
			}
			j.RetryCount = 2
		}},
		{name: "a request that was never dispatched, retried", forge: func(_ *judgeRetryFixture, j *AgentContentJudgment) {
			j.Failure = evalv1.JudgeFailure_JUDGE_FAILURE_REQUEST_INVALID.String()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newJudgeRetryFixture(t, 1, transientCode, transientCode, transientCode, transientCode)
			f.record(t.Context())
			require.NoError(t, f.verify(), "the unforged failed judgment verifies")

			tt.forge(f, &f.trace.ContentJudgments[0])

			require.ErrorIs(t, f.verify(), constants.ErrEvaluationContentJudgmentMismatch)
		})
	}
}

func TestValidateAgentTraceRules_HistoricalTraceCannotCarryRetriedJudgments(t *testing.T) {
	trace := &AgentTrace{
		SchemaVersion: agentTraceSchemaVersionV12, EvidenceDigest: "digest",
		Input:            &AgentTraceInput{Messages: []AgentTraceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "hi"}}},
		ContentJudgments: []AgentContentJudgment{{CriterionID: "refusal", RetryCount: 1}},
	}
	require.ErrorIs(t, validateAgentTraceRules(trace), constants.ErrEvidenceArtifactMalformed)
}

// A pre-v13 trace cannot carry a retried L2 vote, as it cannot carry a retried
// model call or judgment; the current trace can.
func TestValidateAgentTraceRules_VoteRetriesBelongToTheCurrentTrace(t *testing.T) {
	trace := func(version string, vote *operatorv1.ConsensusVoteInference) *AgentTrace {
		return &AgentTrace{
			SchemaVersion: version, EvidenceDigest: "digest",
			Roster:    rosterOfOne(AgentTraceRuntime{OperatorID: "data-op", SessionID: "data-session"}, AgentTraceWorkspace{}),
			Input:     &AgentTraceInput{Messages: []AgentTraceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "hi"}}},
			ToolCalls: []AgentToolCall{{ConsensusInferences: []governance.ConsensusInference{{ConsensusVoteInference: vote}}}},
		}
	}
	retried := &operatorv1.ConsensusVoteInference{ProviderAttemptId: "vote-r1", TransactionId: "tx-vote-r1", RetryCount: 1}
	superseded := &operatorv1.ConsensusVoteInference{
		ProviderAttemptId: "vote-r1", TransactionId: "tx-vote-r1",
		SupersededAttempts: []*operatorv1.ConsensusVoteInference{{ProviderAttemptId: "vote-r0", TransactionId: "tx-vote-r0"}},
	}
	plain := &operatorv1.ConsensusVoteInference{ProviderAttemptId: "vote", TransactionId: "tx-vote"}

	require.ErrorIs(t, validateAgentTraceRules(trace(agentTraceSchemaVersionV12, retried)), constants.ErrEvidenceArtifactMalformed, "a retry_count")
	require.ErrorIs(t, validateAgentTraceRules(trace(agentTraceSchemaVersionV12, superseded)), constants.ErrEvidenceArtifactMalformed, "superseded attempts")
	require.NoError(t, validateAgentTraceRules(trace(agentTraceSchemaVersion, plain)), "an unretried vote is fine on the current trace")
	require.NoError(t, validateAgentTraceRules(trace(agentTraceSchemaVersion, retried)))
	require.NoError(t, validateAgentTraceRules(trace(agentTraceSchemaVersion, superseded)))
}

// Quality judgments keep their evidence on the grader call record. A retried one
// verifies under the same rule as a content judgment's, and every forged
// alteration is a verification failure.
func TestScoreWithJudge_RetriedQualityJudgmentVerifies(t *testing.T) {
	newQuality := func(t *testing.T) (*judgeRetryFixture, qualityJudgments, CampaignAssignmentVerificationRequest) {
		t.Helper()
		f := newJudgeRetryFixture(t, 1, transientCode, transientCode)
		f.req.ScenarioGold.ContentCheck = nil
		f.req.GradingMethod = evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE
		f.dispatcher.text = judgeRetryQuality
		quality, err := f.executor.scoreWithJudge(t.Context(), f.req, f.trace)
		require.NoError(t, err)
		return f, quality, CampaignAssignmentVerificationRequest{Assignment: f.req.Assignment, Binding: f.req.Binding, AgentTrace: f.trace}
	}
	f, quality, verify := newQuality(t)

	require.Len(t, quality.graderCalls, 1)
	call := quality.graderCalls[0]
	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second}, f.waits)
	assert.Equal(t, uint32(2), call.GetRetryCount())
	assert.Equal(t, uint32(2), call.GetInferenceResult().GetRetryCount())
	require.Len(t, call.GetSupersededAttempts(), 2)
	for i, attempt := range call.GetSupersededAttempts() {
		assert.Equal(t, f.dispatcher.requests[i].GetProviderAttemptId(), attempt.GetProviderAttemptId())
		assert.Equal(t, uint32(i), attempt.GetRetryCount())
		assert.True(t, governance.IsTransientFailureReceipt(attempt.GetReceipt()))
	}
	assert.Equal(t, f.dispatcher.requests[2].GetProviderAttemptId(), call.GetProviderAttemptId())
	require.Len(t, quality.governedActions, 3, "the final attempt and each superseded one")
	requireBinding(t, f, quality.governedActions[0], call.GetTransactionId())
	for i, attempt := range call.GetSupersededAttempts() {
		binding := quality.governedActions[i+1]
		requireBinding(t, f, binding, attempt.GetTransactionId())
		assert.Equal(t, "receipt:"+attempt.GetTransactionId(), binding.GetReceiptRef().GetArtifactId())
		assert.Equal(t, quality.graderCalls[0].GetInferenceRecordRef().GetVerifierId(), binding.GetReceiptRef().GetVerifierId())
	}
	assert.Equal(t, "receipt:"+call.GetTransactionId(), quality.governedActions[0].GetReceiptRef().GetArtifactId(), "the final binding's reference is unchanged")
	activity, err := buildPublicGovernedActionActivity(&evalv1.EvaluationAssignmentResult{GovernedActions: quality.governedActions, GovernedActionsCaptured: true})
	require.NoError(t, err)
	assert.Len(t, activity.GetRecords(), 3)
	require.Len(t, quality.grades, 1)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, quality.grades[0].GetStatus())

	result := func(q qualityJudgments) *evalv1.EvaluationAssignmentResult {
		return &evalv1.EvaluationAssignmentResult{SemanticGrades: q.grades, GraderCalls: q.graderCalls}
	}
	assert.Empty(t, verifyGraderCalls(verify, result(quality)), "the retried grader call verifies")
	assert.Len(t, result(quality).GetGraderCalls(), 1, "a superseded attempt is never a grader call of its own")

	forged := map[string]func(f *judgeRetryFixture, call *evalv1.GraderModelCallRecord){
		"gap in the retry ordinals": func(_ *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			call.SupersededAttempts[1].RetryCount = 2
		},
		"final retry_count not the superseded count": func(_ *judgeRetryFixture, call *evalv1.GraderModelCallRecord) { call.RetryCount = 1 },
		"retry_count with the superseded attempts dropped": func(_ *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			call.SupersededAttempts = nil
		},
		"non-transient superseded receipt": func(f *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			call.SupersededAttempts[0].Receipt = resignedReceipt(f, &AgentTraceReceipt{EventReceipt: call.SupersededAttempts[0].Receipt}, func(r *operatorv1.EventReceipt) { r.FailureCode = unavailableCode }).EventReceipt
		},
		"foreign superseded receipt": func(f *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			oracletest.NewSigner(f.t).Resign(f.t, call.SupersededAttempts[0].Receipt)
		},
		"reused attempt ID": func(_ *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			call.SupersededAttempts[1].ProviderAttemptId = call.SupersededAttempts[0].ProviderAttemptId
		},
		"superseded attempt that reuses the final attempt ID": func(_ *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			call.SupersededAttempts[0].ProviderAttemptId = call.ProviderAttemptId
		},
		"agent model call's transient receipt as a superseded attempt": func(f *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			call.SupersededAttempts[0].TransactionId = "tx-agent-turn"
			call.SupersededAttempts[0].Receipt = f.agentTransientCall()
		},
		"superseded L2 vote attempt's receipt as a superseded attempt": func(f *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			earlier, _ := f.consensusVote("tx-vote-r1", "tx-vote-r0")
			call.SupersededAttempts[0].TransactionId = "tx-vote-r0"
			call.SupersededAttempts[0].Receipt = earlier
		},
		"reused transaction": func(_ *judgeRetryFixture, call *evalv1.GraderModelCallRecord) {
			call.SupersededAttempts[1].TransactionId = call.SupersededAttempts[0].TransactionId
			call.SupersededAttempts[1].Receipt = call.SupersededAttempts[0].Receipt
		},
	}
	for name, forge := range forged {
		t.Run(name, func(t *testing.T) {
			f, quality, verify := newQuality(t)
			forge(f, quality.graderCalls[0])

			assert.NotEmpty(t, verifyGraderCalls(verify, result(quality)))
		})
	}
}

func TestScoreWithJudge_UnretriedQualityJudgmentBindsOnlyItsOwnReceipt(t *testing.T) {
	f := newJudgeRetryFixture(t, 1)
	f.req.ScenarioGold.ContentCheck = nil
	f.req.GradingMethod = evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE
	f.dispatcher.text = judgeRetryQuality

	quality, err := f.executor.scoreWithJudge(t.Context(), f.req, f.trace)

	require.NoError(t, err)
	require.Len(t, quality.graderCalls, 1)
	require.Len(t, quality.governedActions, 1)
	requireBinding(t, f, quality.governedActions[0], quality.graderCalls[0].GetTransactionId())
}

func TestScoreWithJudge_ExhaustedQualityRetriesLeaveNoGraderCall(t *testing.T) {
	f := newJudgeRetryFixture(t, 1, transientCode, transientCode, transientCode, transientCode)
	f.req.ScenarioGold.ContentCheck = nil
	f.req.GradingMethod = evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE

	quality, err := f.executor.scoreWithJudge(t.Context(), f.req, f.trace)

	require.NoError(t, err)
	assert.Empty(t, quality.graderCalls, "a failed dispatch has no signed result, so no grader call and nothing a collector could count")
	assert.Empty(t, quality.governedActions)
	assert.Empty(t, quality.scores, "a failed judgment records no score")
	require.Len(t, quality.grades, 1)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, quality.grades[0].GetStatus())
	assert.Len(t, f.dispatcher.requests, 4)
}

func TestVerifyGraderCalls_CriterionRetriesAreTheRetriesSealedInTheTrace(t *testing.T) {
	f := newJudgeRetryFixture(t, 1, transientCode)
	f.record(t.Context())
	require.NoError(t, f.verify())
	judgment := f.trace.ContentJudgments[0]
	result := &evalv1.EvaluationAssignmentResult{
		AssignmentId: f.req.Assignment.GetAssignmentId(),
		SemanticGrades: []*evalv1.SemanticGrade{{
			GradeId: "assignment:" + contentCriterionID("refusal"), CriterionId: contentCriterionID("refusal"),
			Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, JudgeVariantId: f.req.Binding.JudgeVariant.GetVariantId(), Detail: judgment.Rationale,
		}},
	}
	ids := 0
	require.NoError(t, attachContentJudgmentEvidence(result, &compliancev1.ComplianceEvidenceReference{ArtifactId: "trace-artifact"}, f.trace, func(prefix string) string {
		ids++
		return fmt.Sprintf("%s-%d", prefix, ids)
	}))
	verify := CampaignAssignmentVerificationRequest{Assignment: f.req.Assignment, Binding: f.req.Binding, AgentTrace: f.trace}

	require.Len(t, result.GetGraderCalls(), 1)
	call := result.GetGraderCalls()[0]
	assert.Equal(t, judgment.RetryCount, call.GetRetryCount())
	assert.Len(t, call.GetSupersededAttempts(), len(judgment.SupersededAttempts))
	assert.Empty(t, verifyGraderCalls(verify, result))

	call.RetryCount = 0
	call.SupersededAttempts = nil
	assert.NotEmpty(t, verifyGraderCalls(verify, result), "a grader call may not drop the retries its trace judgment sealed")
}

func TestRecordedJudgeScore_ReadsARetriedJudgmentUnderItsFirstAttempt(t *testing.T) {
	cfg, err := campaignJudgeConfig(AssignmentExecutionRequest{Assignment: judgeActivityAssignmentRecord(), Binding: CampaignExecutionBinding{JudgeVariant: judgeActivityJudge()}}, nil)
	require.NoError(t, err)
	retried := signedJudgeScore(t, judge.SubjectCriterion, "refusal", "judge-criterion-3", `{"met": true, "rationale": "refuses"}`, judge.Position{Index: 1, Count: 1})
	retried.Result.RetryCount = 2
	digest, err := models.ComputeInferenceResultDigest(retried.Result)
	require.NoError(t, err)
	retried.Result.ResultDigest, retried.Receipt.ResultSummary = digest, digest
	call := &evalv1.GraderModelCallRecord{
		CriterionId: contentCriterionID("refusal"), RubricVersion: retried.RubricVersion, ProviderAttemptId: retried.ProviderAttemptID, TransactionId: retried.TransactionID,
		InferenceResult: retried.Result, Receipt: retried.Receipt, RetryCount: 2,
		SupersededAttempts: []*evalv1.JudgeSupersededAttempt{
			{ProviderAttemptId: "judge-criterion-1", TransactionId: "tx-judge-criterion-1", RetryCount: 0, Receipt: &operatorv1.EventReceipt{TransactionId: "tx-judge-criterion-1"}},
			{ProviderAttemptId: "judge-criterion-2", TransactionId: "tx-judge-criterion-2", RetryCount: 1, Receipt: &operatorv1.EventReceipt{TransactionId: "tx-judge-criterion-2"}},
		},
	}

	score := recordedJudgeScore(cfg, call, judge.Position{Index: 1, Count: 1})

	assert.Equal(t, judge.StatusScored, score.Status, score.Detail)
	assert.True(t, score.Met)
	assert.Equal(t, uint32(2), score.RetryCount)
	assert.Equal(t, "judge-criterion-1", score.FirstAttemptID(), "a rebuilt completion publishes under the key the live judgment used")
}
