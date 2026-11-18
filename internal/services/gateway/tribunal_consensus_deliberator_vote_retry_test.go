// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// retryReply is one scripted vote attempt: a transient or other failure, or an
// answer.
type retryReply struct {
	err error
	// transient makes the attempt fail as the Inference Operator signs a
	// provider's ask to retry later, with a receipt of its own per attempt.
	transient bool
	text      string
	observed  []*operatorv1.EventReceipt
}

// retryCall is one vote attempt as the deliberator dispatched it.
type retryCall struct {
	member     string
	attemptID  string
	retryCount uint32
	decision   bool
}

// retryInferer answers each member's attempts from a script: a member's nth
// call takes its nth reply, and the last reply repeats. A member with no
// script approves at once. It signs nothing real; its receipts exist so the
// verifier's checks run on what the deliberator produced.
type retryInferer struct {
	mu     sync.Mutex
	script map[string][]retryReply
	calls  []retryCall
	seen   map[string]int
}

func newRetryInferer(script map[string][]retryReply) *retryInferer {
	return &retryInferer{script: script, seen: make(map[string]int)}
}

func (r *retryInferer) DispatchInference(_ context.Context, req dispatch.DispatchInferenceRequest) (*dispatch.DispatchInferenceResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, retryCall{member: req.ActingAppID, attemptID: req.ProviderAttemptID, retryCount: req.RetryCount, decision: req.Decision != nil})
	reply := retryReply{text: tribunalApproveAnswer}
	if script := r.script[req.ActingAppID]; len(script) > 0 {
		reply = script[min(r.seen[req.ActingAppID], len(script)-1)]
	}
	r.seen[req.ActingAppID]++
	transaction := "tx-" + req.ProviderAttemptID
	if reply.transient {
		receipt := &operatorv1.EventReceipt{
			TransactionId: "failed-" + req.ProviderAttemptID,
			ActionType:    string(constants.ActionTypeInference),
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FailureCode:   operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT,
		}
		reply.err = governance.WithFailedReceipt(receipt, constants.ErrInferenceBackendTransient)
	}
	if reply.err != nil {
		return nil, dispatch.WithObservationReceipts(reply.observed, reply.err)
	}
	result := &operatorv1.InferenceResult{
		ProviderAttemptId: req.ProviderAttemptID,
		RetryCount:        req.RetryCount,
		Parts:             []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: reply.text}}},
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	receipt := &operatorv1.EventReceipt{TransactionId: transaction, ActionType: string(constants.ActionTypeInference), Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ResultSummary: digest}
	return &dispatch.DispatchInferenceResult{TransactionID: transaction, Result: result, Receipt: receipt, ObservationReceipts: reply.observed}, nil
}

func (r *retryInferer) callsFor(member string) []retryCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []retryCall
	for _, call := range r.calls {
		if call.member == member {
			out = append(out, call)
		}
	}
	return out
}

// transientFailure is an attempt the provider asked to retry later: the
// dispatch fails with a signed FAILED receipt carrying
// RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT.
func transientFailure(observed ...*operatorv1.EventReceipt) retryReply {
	return retryReply{transient: true, observed: observed}
}

// waitRecorder is the injected retry wait: it records each delay it was asked
// to wait and returns at once, so no test sleeps.
type waitRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
	err    error
}

func (w *waitRecorder) wait(_ context.Context, delay time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.delays = append(w.delays, delay)
	return w.err
}

func (w *waitRecorder) recorded() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.delays...)
}

// retryPolicy is the production schedule with the recorder as its wait.
func (w *waitRecorder) retryPolicy() dispatch.TransientRetry {
	return dispatch.TransientRetry{Delays: dispatch.DefaultTransientRetry().Delays, Wait: w.wait}
}

// voteRetryHarness is one deliberator under test with everything it recorded.
type voteRetryHarness struct {
	d        *TribunalConsensusDeliberator
	binding  *models.ConsensusPolicy
	inferer  *retryInferer
	recorder *fakeVoteRecorder
	waits    *waitRecorder
}

func newVoteRetryHarness(t *testing.T, script map[string][]retryReply) *voteRetryHarness {
	t.Helper()
	binding := tribunalTestBinding()
	h := &voteRetryHarness{binding: binding, inferer: newRetryInferer(script), recorder: &fakeVoteRecorder{}, waits: &waitRecorder{}}
	d, err := NewTribunalConsensusDeliberator(&fakeBinding{policy: binding}, fakePolicies{}, h.inferer, h.recorder, newFakeSigner(t, binding.MemberAppIDs...), h.waits.retryPolicy(), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	h.d = d
	return h
}

// tribunalRetryEnvelope is tribunalTestEnvelope with an expiry, which the
// envelope hash covers. The expiry is cut to whole seconds so it survives the
// wire encoding the deliberator decodes it from.
func tribunalRetryEnvelope(t *testing.T, lifetime time.Duration) (*commonv1.GovernanceEnvelope, string) {
	t.Helper()
	payload, err := proto.Marshal(&operatorv1.CommandRequested{Command: tribunalTestCommand})
	require.NoError(t, err)
	env := &commonv1.GovernanceEnvelope{
		ActionType:     string(constants.ActionTypeExecuteBash),
		TargetResource: "operator://op-1/shell",
		EventType:      string(constants.EventOperatorCommandRequested),
		Payload:        payload,
		ExpiresAt:      timestamppb.New(time.Now().Add(lifetime).Truncate(time.Second)),
	}
	return hashTribunalTestEnvelope(t, env)
}

// deliberate runs one deliberation and returns the votes that counted.
func (h *voteRetryHarness) deliberate(t *testing.T, ctx context.Context, env *commonv1.GovernanceEnvelope) (*governance.ConsensusDeliberation, []string) {
	t.Helper()
	out, err := h.d.Deliberate(ctx, marshalTestEnvelope(t, env))
	require.NoError(t, err)
	return out, voteSignerIDs(unmarshalDeliberatedEnvelope(t, out).GetGovernance().GetL2().GetVotes())
}

// inferenceFor returns member's one vote record.
func inferenceFor(t *testing.T, out *governance.ConsensusDeliberation, member string) *operatorv1.ConsensusVoteInference {
	t.Helper()
	var found *operatorv1.ConsensusVoteInference
	for _, inference := range out.Inferences {
		if inference.GetMemberAppId() == member {
			require.Nil(t, found, "member %s has more than one vote record", member)
			found = inference
		}
	}
	require.NotNil(t, found, "member %s has no vote record", member)
	return found
}

func attemptIDs(env *commonv1.GovernanceEnvelope, member string, retries ...uint32) []string {
	var ids []string
	for _, retry := range retries {
		ids = append(ids, governance.ConsensusVoteAttemptID(env.GetTransactionHash(), member, retry))
	}
	return ids
}

func callIDs(calls []retryCall) []string {
	var ids []string
	for _, call := range calls {
		ids = append(ids, call.attemptID)
	}
	return ids
}

func TestTribunalConsensusVoteRetriesAfterATransientFailureAndCastsTheVote(t *testing.T) {
	observed := &operatorv1.EventReceipt{TransactionId: "observer-tx"}
	h := newVoteRetryHarness(t, nil)
	member := h.binding.MemberAppIDs[0]
	h.inferer.script = map[string][]retryReply{member: {
		transientFailure(observed),
		transientFailure(),
		{text: tribunalApproveAnswer},
	}}
	env, _ := tribunalRetryEnvelope(t, 10*time.Minute)

	out, signers := h.deliberate(t, t.Context(), env)

	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second}, h.waits.recorded())
	assert.Equal(t, h.binding.MemberAppIDs, signers, "the retried member's vote counts beside the other four")
	calls := h.inferer.callsFor(member)
	assert.Equal(t, attemptIDs(env, member, 0, 1, 2), callIDs(calls))
	for i, call := range calls {
		assert.Equal(t, uint32(i), call.retryCount, "the retry ordinal is on the dispatched request")
	}
	for _, other := range h.binding.MemberAppIDs[1:] {
		assert.Len(t, h.inferer.callsFor(other), 1)
	}

	require.Len(t, out.Inferences, len(h.binding.MemberAppIDs), "one vote record per member however many attempts it needed")
	vote := inferenceFor(t, out, member)
	assert.Equal(t, uint32(2), vote.GetRetryCount())
	assert.Equal(t, governance.ConsensusVoteAttemptID(env.GetTransactionHash(), member, 2), vote.GetProviderAttemptId())
	assert.Equal(t, uint32(2), vote.GetResult().GetRetryCount())
	assert.Nil(t, vote.GetFailure())
	require.Len(t, vote.GetSupersededAttempts(), 2)
	for i, superseded := range vote.GetSupersededAttempts() {
		assert.Equal(t, uint32(i), superseded.GetRetryCount())
		assert.Equal(t, governance.ConsensusVoteAttemptID(env.GetTransactionHash(), member, uint32(i)), superseded.GetProviderAttemptId())
		assert.Equal(t, env.GetId(), superseded.GetSubjectTransactionId(), "every attempt names its subject")
		assert.Equal(t, env.GetTransactionHash(), superseded.GetSubjectTransactionHash())
		assert.Equal(t, member, superseded.GetMemberAppId())
		assert.True(t, governance.IsTransientFailureReceipt(superseded.GetReceipt()))
		assert.Equal(t, superseded.GetReceipt().GetTransactionId(), superseded.GetTransactionId())
		assert.NotNil(t, superseded.GetFailure())
		assert.Nil(t, superseded.GetResult())
		assert.Empty(t, superseded.GetSupersededAttempts(), "the chain is flat")
	}
	assert.Equal(t, []*operatorv1.EventReceipt{observed}, vote.GetSupersededAttempts()[0].GetObservationReceipts())
	for _, plain := range h.binding.MemberAppIDs[1:] {
		assert.Zero(t, inferenceFor(t, out, plain).GetRetryCount())
		assert.Empty(t, inferenceFor(t, out, plain).GetSupersededAttempts())
	}
	for _, inference := range out.Inferences {
		require.NoError(t, oracle.ValidateConsensusInference(inference), "the verifier accepts what the deliberator produced")
	}

	records := h.recorder.records(t)
	require.Len(t, records, len(h.binding.MemberAppIDs))
	assert.Equal(t, models.ConsensusVoteOutcomeApprove, records[0].Outcome)
	assert.Equal(t, uint32(2), records[0].RetryCount)
	assert.Equal(t, vote.GetProviderAttemptId(), records[0].ProviderAttemptID, "the audit record names the final attempt")
	assert.Equal(t, vote.GetTransactionId(), records[0].InferenceTransactionID)
	assert.Contains(t, h.recorder.events[0].ContentText, `"retry_count":2`)
	for i, record := range records[1:] {
		assert.Zero(t, record.RetryCount)
		assert.NotContains(t, h.recorder.events[i+1].ContentText, "retry_count", "a vote with no retry writes the record it always has")
	}
}

func TestTribunalConsensusVoteRetriesOnceThenDenyIsAVote(t *testing.T) {
	h := newVoteRetryHarness(t, nil)
	member := h.binding.MemberAppIDs[1]
	h.inferer.script = map[string][]retryReply{member: {transientFailure(), {text: tribunalDenyAnswer}}}
	env, _ := tribunalRetryEnvelope(t, 10*time.Minute)

	out, signers := h.deliberate(t, t.Context(), env)

	assert.Equal(t, []time.Duration{5 * time.Second}, h.waits.recorded())
	assert.Equal(t, h.binding.MemberAppIDs, signers, "a signed deny is a vote")
	assert.Equal(t, models.ConsensusVoteOutcomeDeny, h.recorder.records(t)[1].Outcome)
	assert.Equal(t, uint32(1), inferenceFor(t, out, member).GetRetryCount())
	require.NoError(t, oracle.ValidateConsensusInference(inferenceFor(t, out, member)))
}

func TestTribunalConsensusVoteExhaustedRetriesCastNoVoteAndKeepEveryAttempt(t *testing.T) {
	h := newVoteRetryHarness(t, nil)
	member := h.binding.MemberAppIDs[0]
	h.inferer.script = map[string][]retryReply{member: {transientFailure()}}
	env, _ := tribunalRetryEnvelope(t, 10*time.Minute)

	out, signers := h.deliberate(t, t.Context(), env)

	assert.Equal(t, []time.Duration{5 * time.Second, 20 * time.Second, 40 * time.Second}, h.waits.recorded())
	assert.Equal(t, h.binding.MemberAppIDs[1:], signers, "the exhausted member casts no vote")
	assert.Equal(t, attemptIDs(env, member, 0, 1, 2, 3), callIDs(h.inferer.callsFor(member)), "one initial attempt and no more than MaxTransientRetries retries")
	vote := inferenceFor(t, out, member)
	assert.Equal(t, dispatch.MaxTransientRetries(), vote.GetRetryCount())
	assert.NotNil(t, vote.GetFailure())
	assert.Nil(t, vote.GetResult())
	assert.True(t, governance.IsTransientFailureReceipt(vote.GetReceipt()))
	assert.Len(t, vote.GetSupersededAttempts(), int(dispatch.MaxTransientRetries()))
	require.NoError(t, oracle.ValidateConsensusInference(vote), "an exhausted chain is complete evidence")

	record := h.recorder.records(t)[0]
	assert.Equal(t, models.ConsensusVoteOutcomeNoVote, record.Outcome)
	assert.Equal(t, dispatch.MaxTransientRetries(), record.RetryCount)
	assert.Equal(t, attemptIDs(env, member, 3)[0], record.ProviderAttemptID, "the record names the last attempt made")
	assert.Equal(t, "failed-"+attemptIDs(env, member, 3)[0], record.InferenceTransactionID)
}

func TestTribunalConsensusVoteDoesNotRetryAFailureThatIsNotTransient(t *testing.T) {
	failed := &operatorv1.EventReceipt{
		TransactionId: "vote-tx-0",
		ActionType:    string(constants.ActionTypeInference),
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED,
	}
	for name, reply := range map[string]retryReply{
		"signed non-transient failure":      {err: governance.WithFailedReceipt(failed, constants.ErrInferenceGenerateFailed)},
		"transient error with no receipt":   {err: constants.ErrInferenceBackendTransient},
		"unavailable provider, no receipt":  {err: constants.ErrInferenceBackendUnavailable},
		"transient error on a plain wrap":   {err: fmt.Errorf("dispatch: %w", constants.ErrInferenceBackendTransient)},
		"outcome unknown after the timeout": {err: constants.ErrInferenceOutcomeUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			h := newVoteRetryHarness(t, nil)
			member := h.binding.MemberAppIDs[0]
			h.inferer.script = map[string][]retryReply{member: {reply}}
			env, _ := tribunalRetryEnvelope(t, 10*time.Minute)

			out, signers := h.deliberate(t, t.Context(), env)

			assert.Empty(t, h.waits.recorded())
			assert.Len(t, h.inferer.callsFor(member), 1)
			assert.Equal(t, h.binding.MemberAppIDs[1:], signers)
			vote := inferenceFor(t, out, member)
			assert.Zero(t, vote.GetRetryCount())
			assert.Empty(t, vote.GetSupersededAttempts())
			assert.Equal(t, models.ConsensusVoteOutcomeNoVote, h.recorder.records(t)[0].Outcome)
		})
	}
}

func TestTribunalConsensusDecisionVoteIsNeverRetried(t *testing.T) {
	h := newVoteRetryHarness(t, nil)
	h.binding.VoteKind = models.ConsensusVoteKindDecision
	h.binding.InferenceOperatorID = "dedicated-decisions"
	for _, id := range h.binding.MemberAppIDs {
		h.binding.VoteThresholds = append(h.binding.VoteThresholds, models.ConsensusDecisionThreshold{MemberAppID: id, Threshold: .9})
	}
	member := h.binding.MemberAppIDs[0]
	h.inferer.script = map[string][]retryReply{member: {transientFailure()}}
	env, _ := tribunalRetryEnvelope(t, 10*time.Minute)

	out, _ := h.deliberate(t, t.Context(), env)

	assert.Empty(t, h.waits.recorded())
	calls := h.inferer.callsFor(member)
	require.Len(t, calls, 1)
	assert.True(t, calls[0].decision)
	assert.Zero(t, inferenceFor(t, out, member).GetRetryCount())
}

func TestTribunalConsensusVoteRetryWaitIsBoundedByTheEnvelopeExpiry(t *testing.T) {
	member := tribunalTestBinding().MemberAppIDs[0]
	script := map[string][]retryReply{member: {transientFailure()}}
	for _, test := range []struct {
		name     string
		lifetime time.Duration
		waits    []time.Duration
		attempts int
	}{
		// The first wait ends at 5 s. L4 and L5 need DispatchTimeout (30 s)
		// before the envelope expires, so an envelope with 34 s left (4 s once
		// the margin is held back) cannot afford it.
		{name: "no wait fits before the margin", lifetime: 34 * time.Second, waits: nil, attempts: 1},
		// 45 s leaves 15 s: the 5 s wait fits and the 20 s wait does not.
		{name: "the second wait would run into the margin", lifetime: 45 * time.Second, waits: []time.Duration{5 * time.Second}, attempts: 2},
		{name: "ample lifetime", lifetime: 10 * time.Minute, waits: []time.Duration{5 * time.Second, 20 * time.Second, 40 * time.Second}, attempts: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newVoteRetryHarness(t, script)
			env, _ := tribunalRetryEnvelope(t, test.lifetime)

			out, _ := h.deliberate(t, t.Context(), env)

			assert.Equal(t, test.waits, h.waits.recorded())
			assert.Len(t, h.inferer.callsFor(member), test.attempts)
			vote := inferenceFor(t, out, member)
			assert.Equal(t, uint32(test.attempts-1), vote.GetRetryCount())
			assert.Len(t, vote.GetSupersededAttempts(), test.attempts-1)
			assert.Equal(t, models.ConsensusVoteOutcomeNoVote, h.recorder.records(t)[0].Outcome)
			require.NoError(t, oracle.ValidateConsensusInference(vote))
		})
	}
}

func TestTribunalConsensusVoteWithoutAnExpiryIsNeverRetried(t *testing.T) {
	h := newVoteRetryHarness(t, nil)
	member := h.binding.MemberAppIDs[0]
	h.inferer.script = map[string][]retryReply{member: {transientFailure()}}
	env, _ := tribunalTestEnvelope(t)
	require.Nil(t, env.GetExpiresAt())

	h.deliberate(t, t.Context(), env)

	assert.Empty(t, h.waits.recorded(), "L4 rejects an envelope with no expiry, so no wait can be shown to fit")
	assert.Len(t, h.inferer.callsFor(member), 1)
}

func TestTribunalConsensusVoteRetryWaitIsBoundedByTheContextDeadline(t *testing.T) {
	member := tribunalTestBinding().MemberAppIDs[0]
	script := map[string][]retryReply{member: {transientFailure(), {text: tribunalApproveAnswer}}}

	t.Run("deadline inside the first wait", func(t *testing.T) {
		h := newVoteRetryHarness(t, script)
		env, _ := tribunalRetryEnvelope(t, 10*time.Minute)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()

		_, signers := h.deliberate(t, ctx, env)

		assert.Empty(t, h.waits.recorded(), "a wait that would end after the deadline is never started")
		assert.Len(t, h.inferer.callsFor(member), 1)
		assert.NotContains(t, signers, member)
	})
	t.Run("deadline beyond the wait", func(t *testing.T) {
		h := newVoteRetryHarness(t, script)
		env, _ := tribunalRetryEnvelope(t, 10*time.Minute)
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		_, signers := h.deliberate(t, ctx, env)

		assert.Equal(t, []time.Duration{5 * time.Second}, h.waits.recorded())
		assert.Contains(t, signers, member)
	})
	t.Run("a wait that ends in the context error dispatches nothing", func(t *testing.T) {
		h := newVoteRetryHarness(t, script)
		h.waits.err = context.Canceled
		env, _ := tribunalRetryEnvelope(t, 10*time.Minute)

		out, signers := h.deliberate(t, t.Context(), env)

		assert.Equal(t, []time.Duration{5 * time.Second}, h.waits.recorded())
		assert.Len(t, h.inferer.callsFor(member), 1)
		assert.NotContains(t, signers, member)
		assert.Empty(t, inferenceFor(t, out, member).GetSupersededAttempts())
	})
}

func TestTribunalConsensusVoteRetriesEachMemberIndependently(t *testing.T) {
	h := newVoteRetryHarness(t, nil)
	ids := h.binding.MemberAppIDs
	h.inferer.script = map[string][]retryReply{
		ids[0]: {transientFailure(), {text: tribunalApproveAnswer}},
		ids[2]: {transientFailure(), transientFailure(), {text: tribunalApproveAnswer}},
	}
	env, _ := tribunalRetryEnvelope(t, 10*time.Minute)

	out, signers := h.deliberate(t, t.Context(), env)

	assert.Equal(t, ids, signers)
	assert.Equal(t, uint32(1), inferenceFor(t, out, ids[0]).GetRetryCount())
	assert.Equal(t, uint32(2), inferenceFor(t, out, ids[2]).GetRetryCount())
	assert.ElementsMatch(t, []time.Duration{5 * time.Second, 5 * time.Second, 20 * time.Second}, h.waits.recorded())
	distinct := make(map[string]bool)
	for _, call := range h.inferer.calls {
		assert.False(t, distinct[call.attemptID], "attempt ID %s was dispatched twice", call.attemptID)
		distinct[call.attemptID] = true
	}
}

func TestNewTribunalConsensusDeliberatorRequiresARetryWait(t *testing.T) {
	got, err := NewTribunalConsensusDeliberator(&fakeBinding{}, fakePolicies{}, &fakeInferer{}, &fakeVoteRecorder{}, nil, dispatch.TransientRetry{Delays: dispatch.DefaultTransientRetry().Delays}, slog.New(slog.DiscardHandler))
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	assert.Nil(t, got)
}

func TestRecordVoteWithRetriesPublishesAValidPublicRecord(t *testing.T) {
	env, _ := tribunalRetryEnvelope(t, 10*time.Minute)
	env.Posture = string(constants.PostureConsensus)
	env.InvestigationId = "run-1"
	env, _ = hashTribunalTestEnvelope(t, env)
	binding := tribunalTestBinding()
	feed := &fakeVoteFeed{}
	d := newRecordingTribunalTestDeliberator(t, binding, &fakeInferer{}, newFakeSigner(t, binding.MemberAppIDs...), &fakeVoteRecorder{})
	d.voteFeed = func() consensusVoteFeed { return feed }

	require.NoError(t, d.recordVote(t.Context(), env, binding, binding.MemberAppIDs[0], memberVote{
		answer:     tribunal.ConsensusVoteAnswer{Decision: tribunal.ConsensusVoteApprove, Reason: "read-only query"},
		attemptID:  governance.ConsensusVoteAttemptID(env.GetTransactionHash(), binding.MemberAppIDs[0], 2),
		retryCount: 2,
	}))

	require.Len(t, feed.bodies, 1)
	require.NoError(t, publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, feed.bodies[0]))
	assert.Contains(t, string(feed.bodies[0]), "retry_count", "the public vote record carries the retry ordinal")
}
