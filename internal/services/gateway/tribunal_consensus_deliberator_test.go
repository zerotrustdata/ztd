// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	tribunalTestBindingID = "consensus-binding"
	tribunalTestOwnerID   = "owner-1"
	tribunalTestModel     = "gemma4:e2b"
	tribunalTestDigest    = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	tribunalApproveAnswer = `{"decision":"approve","reason":"matches target"}`
	tribunalDenyAnswer    = `{"decision":"deny","reason":"unsafe"}`
)

// fakeBinding returns a fixed consensus binding.
type fakeBinding struct {
	policy *models.ConsensusPolicy
	err    error
}

func (f *fakeBinding) GetBoundConsensusApp(context.Context) (*models.ConsensusPolicy, error) {
	return f.policy, f.err
}

// fakePolicies returns every member app owned by tribunalTestOwnerID. Like
// the enrollment store, it keys policies by the app's SPIFFE ID only.
type fakePolicies struct{}

func (fakePolicies) GetAppPolicy(_ context.Context, appID string) (*models.AppPolicy, error) {
	if !strings.HasPrefix(appID, "spiffe://") {
		return nil, nil
	}
	return &models.AppPolicy{AppID: appID, OwnerUserID: tribunalTestOwnerID}, nil
}

// inferReply is one member's scripted inference outcome.
type inferReply struct {
	text string
	err  error
}

// inferCall records one DispatchInference call as the deliberator issued it.
type inferCall struct {
	actingAppID string
	model       string
	modelDigest string
	requestorID string
	schema      string
	format      *operatorv1.InferenceResponseFormat
	messages    []*operatorv1.InferenceMessage
}

// fakeInferer answers each member's vote from a script keyed by ActingAppID.
// A member with no script entry fails, so unexpected calls are visible. With
// barrier set, a call answers only once barrier.want calls are in flight.
// onCall, when set, runs before the reply.
type fakeInferer struct {
	mu      sync.Mutex
	replies map[string]inferReply
	calls   []inferCall
	barrier *callBarrier
	onCall  func(req dispatch.DispatchInferenceRequest)
}

// callBarrier releases its callers once want of them have arrived.
type callBarrier struct {
	want    int
	mu      sync.Mutex
	arrived int
	all     chan struct{}
}

func newCallBarrier(want int) *callBarrier {
	return &callBarrier{want: want, all: make(chan struct{})}
}

// wait reports whether every expected caller arrived before the deadline.
func (b *callBarrier) wait() bool {
	b.mu.Lock()
	b.arrived++
	if b.arrived == b.want {
		close(b.all)
	}
	b.mu.Unlock()
	select {
	case <-b.all:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func (f *fakeInferer) DispatchInference(ctx context.Context, req dispatch.DispatchInferenceRequest) (*dispatch.DispatchInferenceResult, error) {
	if f.onCall != nil {
		f.onCall(req)
	}
	if f.barrier != nil && !f.barrier.wait() {
		return nil, fmt.Errorf("fakeInferer: %s answered before its batch was in flight", req.ActingAppID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, inferCall{
		actingAppID: req.ActingAppID,
		model:       req.Model,
		modelDigest: req.ModelDigest,
		requestorID: req.RequestorUserID,
		schema:      req.RequestSchemaVersion,
		format:      req.ResponseFormat,
		messages:    req.Messages,
	})
	reply, ok := f.replies[req.ActingAppID]
	if !ok {
		return nil, fmt.Errorf("fakeInferer: no scripted reply for %s", req.ActingAppID)
	}
	if reply.err != nil {
		return nil, reply.err
	}
	return &dispatch.DispatchInferenceResult{
		Result: &operatorv1.InferenceResult{
			Parts: []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: reply.text}}},
		},
	}, nil
}

// signCall records one Sign call on the member signer.
type signCall struct {
	memberAppID string
	payload     []byte
}

// fakeSigner signs with real ed25519 keys. Only members given a key by
// newFakeSigner report HasKey and can sign.
type fakeSigner struct {
	keys  map[string]ed25519.PrivateKey
	calls []signCall
}

func newFakeSigner(t *testing.T, memberAppIDs ...string) *fakeSigner {
	t.Helper()
	signer := &fakeSigner{keys: make(map[string]ed25519.PrivateKey, len(memberAppIDs))}
	for _, id := range memberAppIDs {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate ed25519 key for %s: %v", id, err)
		}
		signer.keys[id] = priv
	}
	return signer
}

func (f *fakeSigner) HasKey(memberAppID string) bool {
	_, ok := f.keys[memberAppID]
	return ok
}

func (f *fakeSigner) Sign(memberAppID string, payload []byte) ([]byte, error) {
	f.calls = append(f.calls, signCall{memberAppID: memberAppID, payload: payload})
	priv, ok := f.keys[memberAppID]
	if !ok {
		return nil, constants.ErrConsensusMemberKeyUnavailable
	}
	return ed25519.Sign(priv, payload), nil
}

func (f *fakeSigner) publicKey(t *testing.T, memberAppID string) ed25519.PublicKey {
	t.Helper()
	priv, ok := f.keys[memberAppID]
	if !ok {
		t.Fatalf("no key for member %s", memberAppID)
	}
	return priv.Public().(ed25519.PublicKey)
}

// fakeApplicationDeliberator stands in for the application L2 provider.
type fakeApplicationDeliberator struct {
	calls int
	out   []byte
	err   error
}

func (f *fakeApplicationDeliberator) Deliberate(context.Context, []byte) ([]byte, error) {
	f.calls++
	return f.out, f.err
}

func tribunalTestBinding() *models.ConsensusPolicy {
	return &models.ConsensusPolicy{
		ID:           tribunalTestBindingID,
		Provider:     string(constants.ConsensusProviderTribunal),
		MemberAppIDs: constants.TribunalConsensusMemberAppIDs(),
		Quorum:       3,
		ModelTag:     tribunalTestModel,
		ModelDigest:  tribunalTestDigest,
		Enabled:      true,
	}
}

// repliesForAll scripts every consensus member with the same reply.
func repliesForAll(reply inferReply) map[string]inferReply {
	out := make(map[string]inferReply, len(constants.TribunalConsensusMemberAppIDs()))
	for _, id := range constants.TribunalConsensusMemberAppIDs() {
		out[id] = reply
	}
	return out
}

// membersExcept returns every consensus member app ID except skip.
func membersExcept(skip string) []string {
	var out []string
	for _, id := range constants.TribunalConsensusMemberAppIDs() {
		if id != skip {
			out = append(out, id)
		}
	}
	return out
}

// tribunalTestCommand is the shell command carried by tribunalTestEnvelope.
const tribunalTestCommand = "uname -a"

// tribunalTestEnvelope returns a hashed EXECUTE_BASH envelope whose
// transaction hash matches its canonical content.
func tribunalTestEnvelope(t *testing.T) (*commonv1.GovernanceEnvelope, string) {
	t.Helper()
	payload, err := proto.Marshal(&operatorv1.CommandRequested{Command: tribunalTestCommand})
	if err != nil {
		t.Fatalf("marshal command payload: %v", err)
	}
	env := &commonv1.GovernanceEnvelope{
		ActionType:     string(constants.ActionTypeExecuteBash),
		TargetResource: "operator://op-1/shell",
		EventType:      string(constants.EventOperatorCommandRequested),
		Payload:        payload,
	}
	return hashTribunalTestEnvelope(t, env)
}

// hashTribunalTestEnvelope stamps env with its canonical transaction hash.
func hashTribunalTestEnvelope(t *testing.T, env *commonv1.GovernanceEnvelope) (*commonv1.GovernanceEnvelope, string) {
	t.Helper()
	hash, err := govpkg.GenerateMessageID(env)
	if err != nil {
		t.Fatalf("generate message id: %v", err)
	}
	env.Id = hash
	env.TransactionHash = hash
	return env, hash
}

func marshalTestEnvelope(t *testing.T, env *commonv1.GovernanceEnvelope) []byte {
	t.Helper()
	raw, err := protojson.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return raw
}

func unmarshalDeliberatedEnvelope(t *testing.T, raw []byte) *commonv1.GovernanceEnvelope {
	t.Helper()
	var env commonv1.GovernanceEnvelope
	if err := protojson.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal deliberated envelope: %v", err)
	}
	return &env
}

// fakeVoteRecorder keeps every vote event the deliberator records. With err
// set it records nothing and fails.
type fakeVoteRecorder struct {
	events []*storage.Event
	err    error
}

func (f *fakeVoteRecorder) RecordEvent(event *storage.Event) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.events = append(f.events, event)
	return int64(len(f.events)), nil
}

// records decodes the recorded vote events.
func (f *fakeVoteRecorder) records(t *testing.T) []models.ConsensusVoteRecord {
	t.Helper()
	out := make([]models.ConsensusVoteRecord, 0, len(f.events))
	for _, event := range f.events {
		var record models.ConsensusVoteRecord
		if err := json.Unmarshal([]byte(event.ContentText), &record); err != nil {
			t.Fatalf("decode vote record: %v", err)
		}
		out = append(out, record)
	}
	return out
}

func newTribunalTestDeliberator(t *testing.T, binding *models.ConsensusPolicy, inferer *fakeInferer, signer *fakeSigner) *TribunalConsensusDeliberator {
	t.Helper()
	return newRecordingTribunalTestDeliberator(t, binding, inferer, signer, &fakeVoteRecorder{})
}

func newRecordingTribunalTestDeliberator(t *testing.T, binding *models.ConsensusPolicy, inferer *fakeInferer, signer *fakeSigner, recorder *fakeVoteRecorder) *TribunalConsensusDeliberator {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	deliberator, err := NewTribunalConsensusDeliberator(&fakeBinding{policy: binding}, fakePolicies{}, inferer, recorder, signer, logger)
	require.NoError(t, err)
	return deliberator
}

type fakeVoteFeed struct {
	bodies [][]byte
	err    error
}

func (f *fakeVoteFeed) PublishLiveEvent(_ context.Context, body []byte) error {
	f.bodies = append(f.bodies, append([]byte(nil), body...))
	return f.err
}

func TestRecordVotePublishesTheAuditRecordOnThePublicFeed(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	env.Posture = string(constants.PostureConsensus)
	env.InvestigationId = "run-1"
	env, _ = hashTribunalTestEnvelope(t, env)
	binding := tribunalTestBinding()
	feed := &fakeVoteFeed{}
	d := newRecordingTribunalTestDeliberator(t, binding, &fakeInferer{}, newFakeSigner(t, binding.MemberAppIDs...), &fakeVoteRecorder{})
	d.voteFeed = func() consensusVoteFeed { return feed }

	err := d.recordVote(context.Background(), env, binding, binding.MemberAppIDs[0], memberVote{
		answer: tribunal.ConsensusVoteAnswer{Decision: tribunal.ConsensusVoteApprove, Reason: "read-only query"},
	})
	require.NoError(t, err)
	require.Len(t, feed.bodies, 1)
	require.NoError(t, publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, feed.bodies[0]))

	event := &evalv1.PublicLiveEvent{}
	require.NoError(t, evalv1.UnmarshalCanonical(feed.bodies[0], event))
	assert.Equal(t, "stage_updated", event.GetKind())
	assert.Equal(t, "ds-live-run-1", event.GetDatasetId())
	assert.Equal(t, "run-1", event.GetRunId())
	require.True(t, strings.HasPrefix(event.GetStageLabel(), tribunalVoteStagePrefix))
	var published models.ConsensusVoteRecord
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(event.GetStageLabel(), tribunalVoteStagePrefix)), &published))
	assert.Equal(t, binding.MemberAppIDs[0], published.MemberAppID)
	assert.Equal(t, models.ConsensusVoteOutcomeApprove, published.Outcome)
	assert.Equal(t, "read-only query", published.Reason)
	assert.Equal(t, d.votes.(*fakeVoteRecorder).records(t)[0], published)

	env.Posture = string(constants.PostureDoctrine)
	require.NoError(t, d.recordVote(context.Background(), env, binding, binding.MemberAppIDs[1], memberVote{
		answer: tribunal.ConsensusVoteAnswer{Decision: tribunal.ConsensusVoteDeny, Reason: "not this posture"},
	}))
	assert.Len(t, feed.bodies, 1)
}

func TestNewTribunalConsensusDeliberatorRejectsMissingRequiredDependencies(t *testing.T) {
	got, err := NewTribunalConsensusDeliberator(nil, fakePolicies{}, &fakeInferer{}, &fakeVoteRecorder{}, nil, slog.New(slog.DiscardHandler))
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	require.Nil(t, got)
}

// assertVoteSignature verifies vote's signature over the L2 vote payload for
// hash, using the member's public key.
func assertVoteSignature(t *testing.T, signer *fakeSigner, hash string, vote *commonv1.L2Vote) {
	t.Helper()
	sig, err := hex.DecodeString(vote.GetConsensusSignature())
	if err != nil {
		t.Fatalf("vote %s signature is not hex: %v", vote.GetSignerKeyId(), err)
	}
	pub := signer.publicKey(t, vote.GetSignerKeyId())
	if !ed25519.Verify(pub, governance.L2VotePayload(hash, vote.GetDecision()), sig) {
		t.Fatalf("vote %s signature does not verify for decision %v", vote.GetSignerKeyId(), vote.GetDecision())
	}
}

func voteSignerIDs(votes []*commonv1.L2Vote) []string {
	ids := make([]string, 0, len(votes))
	for _, vote := range votes {
		ids = append(ids, vote.GetSignerKeyId())
	}
	return ids
}

func TestTribunalConsensusAllApproveCountsEveryMember(t *testing.T) {
	env, hash := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}

	if len(inferer.calls) != 5 {
		t.Fatalf("inference calls = %d, want 5 (all members queried)", len(inferer.calls))
	}
	called := make([]string, 0, len(inferer.calls))
	for _, call := range inferer.calls {
		called = append(called, call.actingAppID)
	}
	slices.Sort(called)
	want := slices.Sorted(slices.Values(binding.MemberAppIDs))
	if !slices.Equal(called, want) {
		t.Fatalf("called members = %v, want all members %v", called, want)
	}
	if len(signer.calls) != 5 {
		t.Fatalf("sign calls = %d, want 5", len(signer.calls))
	}
	for i, sign := range signer.calls {
		if sign.memberAppID != binding.MemberAppIDs[i] {
			t.Fatalf("sign %d member = %s, want %s (binding order)", i, sign.memberAppID, binding.MemberAppIDs[i])
		}
	}

	deliberated := unmarshalDeliberatedEnvelope(t, out)
	l2 := deliberated.GetGovernance().GetL2()
	if got := l2.GetConsensusSetId(); got != tribunalTestBindingID {
		t.Fatalf("consensus_set_id = %q, want %q", got, tribunalTestBindingID)
	}
	if len(l2.GetVotes()) != 5 {
		t.Fatalf("votes = %d, want 5", len(l2.GetVotes()))
	}
	for _, vote := range l2.GetVotes() {
		if !vote.GetDecision() {
			t.Fatalf("vote from %s is not approve", vote.GetSignerKeyId())
		}
		assertVoteSignature(t, signer, hash, vote)
	}

	rehash, err := govpkg.GenerateMessageID(deliberated)
	if err != nil {
		t.Fatalf("re-hash deliberated envelope: %v", err)
	}
	if rehash != hash {
		t.Fatalf("re-hash = %s, want original hash %s; envelope changed outside Governance.L2", rehash, hash)
	}
	if deliberated.GetTransactionHash() != hash || deliberated.GetId() != hash {
		t.Fatalf("transaction_hash/id changed: id=%s tx=%s", deliberated.GetId(), deliberated.GetTransactionHash())
	}
}

func TestTribunalConsensusAllApproveStopsAtQuorum(t *testing.T) {
	env, hash := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}

	if len(inferer.calls) != 3 {
		t.Fatalf("inference calls = %d, want 3 (short-circuit at quorum)", len(inferer.calls))
	}
	called := make([]string, 0, len(inferer.calls))
	for _, call := range inferer.calls {
		called = append(called, call.actingAppID)
	}
	slices.Sort(called)
	want := slices.Sorted(slices.Values(binding.MemberAppIDs[:3]))
	if !slices.Equal(called, want) {
		t.Fatalf("called members = %v, want the first three %v", called, want)
	}
	if len(signer.calls) != 3 {
		t.Fatalf("sign calls = %d, want 3", len(signer.calls))
	}
	for i, sign := range signer.calls {
		if sign.memberAppID != binding.MemberAppIDs[i] {
			t.Fatalf("sign %d member = %s, want %s (binding order)", i, sign.memberAppID, binding.MemberAppIDs[i])
		}
	}

	deliberated := unmarshalDeliberatedEnvelope(t, out)
	l2 := deliberated.GetGovernance().GetL2()
	if got := l2.GetConsensusSetId(); got != tribunalTestBindingID {
		t.Fatalf("consensus_set_id = %q, want %q", got, tribunalTestBindingID)
	}
	if len(l2.GetVotes()) != 3 {
		t.Fatalf("votes = %d, want 3", len(l2.GetVotes()))
	}
	for _, vote := range l2.GetVotes() {
		if !vote.GetDecision() {
			t.Fatalf("vote from %s is not approve", vote.GetSignerKeyId())
		}
		assertVoteSignature(t, signer, hash, vote)
	}

	rehash, err := govpkg.GenerateMessageID(deliberated)
	if err != nil {
		t.Fatalf("re-hash deliberated envelope: %v", err)
	}
	if rehash != hash {
		t.Fatalf("re-hash = %s, want original hash %s; envelope changed outside Governance.L2", rehash, hash)
	}
	if deliberated.GetTransactionHash() != hash || deliberated.GetId() != hash {
		t.Fatalf("transaction_hash/id changed: id=%s tx=%s", deliberated.GetId(), deliberated.GetTransactionHash())
	}
}

func TestTribunalConsensusAllDenyStopsWhenQuorumUnreachable(t *testing.T) {
	env, hash := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalDenyAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}

	// After three denies only two members remain, fewer than quorum 3.
	if len(inferer.calls) != 3 {
		t.Fatalf("inference calls = %d, want 3 (stop once quorum unreachable)", len(inferer.calls))
	}
	votes := unmarshalDeliberatedEnvelope(t, out).GetGovernance().GetL2().GetVotes()
	if len(votes) != 3 {
		t.Fatalf("votes = %d, want 3", len(votes))
	}
	for _, vote := range votes {
		if vote.GetDecision() {
			t.Fatalf("vote from %s is approve, want deny", vote.GetSignerKeyId())
		}
		assertVoteSignature(t, signer, hash, vote)
	}
}

func TestTribunalConsensusInvalidAnswerIsSignedDeny(t *testing.T) {
	env, hash := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: "I think this is probably fine."})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}

	if len(inferer.calls) != 3 {
		t.Fatalf("inference calls = %d, want 3", len(inferer.calls))
	}
	votes := unmarshalDeliberatedEnvelope(t, out).GetGovernance().GetL2().GetVotes()
	if len(votes) != 3 {
		t.Fatalf("votes = %d, want 3 signed deny votes", len(votes))
	}
	for _, vote := range votes {
		if vote.GetDecision() {
			t.Fatalf("invalid answer from %s produced approve", vote.GetSignerKeyId())
		}
		assertVoteSignature(t, signer, hash, vote)
	}
}

func TestTribunalConsensusInferenceErrorCastsNoVoteAndContinues(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	replies := repliesForAll(inferReply{text: tribunalApproveAnswer})
	failing := binding.MemberAppIDs[0]
	replies[failing] = inferReply{err: errors.New("member inference unavailable")}
	inferer := &fakeInferer{replies: replies}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}

	// The failing member is called, then the next three members reach quorum.
	if len(inferer.calls) != 4 {
		t.Fatalf("inference calls = %d, want 4", len(inferer.calls))
	}
	votes := unmarshalDeliberatedEnvelope(t, out).GetGovernance().GetL2().GetVotes()
	got := voteSignerIDs(votes)
	want := binding.MemberAppIDs[1:4]
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("vote signers = %v, want %v", got, want)
	}
	for _, sign := range signer.calls {
		if sign.memberAppID == failing {
			t.Fatalf("member %s signed despite inference failure", failing)
		}
	}
}

func TestTribunalConsensusAsksEachBatchConcurrently(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	// Each call answers only once all three first-batch calls are in flight;
	// serial voting would time out the barrier and cast no vote.
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer}), barrier: newCallBarrier(binding.Quorum)}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}
	votes := unmarshalDeliberatedEnvelope(t, out).GetGovernance().GetL2().GetVotes()
	if got, want := voteSignerIDs(votes), binding.MemberAppIDs[:3]; !slices.Equal(got, want) {
		t.Fatalf("vote signers = %v, want %v in binding order", got, want)
	}
}

func TestTribunalConsensusStopsAskingOnceTheContextEnds(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	replies := repliesForAll(inferReply{text: tribunalApproveAnswer})
	failing := binding.MemberAppIDs[0]
	replies[failing] = inferReply{err: context.Canceled}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inferer := &fakeInferer{replies: replies, onCall: func(req dispatch.DispatchInferenceRequest) {
		if req.ActingAppID == failing {
			cancel()
		}
	}}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	if _, err := d.Deliberate(ctx, marshalTestEnvelope(t, env)); err != nil {
		t.Fatalf("Deliberate: %v", err)
	}
	// The first batch was already in flight; no member is asked after cancel.
	if len(inferer.calls) != 3 {
		t.Fatalf("inference calls = %d, want 3 (no batch after cancel)", len(inferer.calls))
	}
}

func TestTribunalConsensusMemberWithoutKeyCastsNoVote(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	missing := binding.MemberAppIDs[1]
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, membersExcept(missing)...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}

	for _, call := range inferer.calls {
		if call.actingAppID == missing {
			t.Fatalf("inference ran for member %s without a key", missing)
		}
	}
	if len(inferer.calls) != 3 {
		t.Fatalf("inference calls = %d, want 3", len(inferer.calls))
	}
	for _, sign := range signer.calls {
		if sign.memberAppID == missing {
			t.Fatalf("Sign called for member %s without a key", missing)
		}
	}
	votes := unmarshalDeliberatedEnvelope(t, out).GetGovernance().GetL2().GetVotes()
	for _, vote := range votes {
		if vote.GetSignerKeyId() == missing {
			t.Fatalf("keyless member %s produced a vote", missing)
		}
	}
	if len(votes) != 3 {
		t.Fatalf("votes = %d, want 3", len(votes))
	}
}

func TestTribunalConsensusRecordsEveryMemberVote(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	env.OperatorSessionId = "session-1"
	hash, err := govpkg.GenerateMessageID(env)
	if err != nil {
		t.Fatalf("generate message id: %v", err)
	}
	env.Id, env.TransactionHash = hash, hash
	binding := tribunalTestBinding()
	replies := repliesForAll(inferReply{text: tribunalApproveAnswer})
	replies[binding.MemberAppIDs[0]] = inferReply{text: tribunalDenyAnswer}
	replies[binding.MemberAppIDs[1]] = inferReply{text: "probably fine"}
	replies[binding.MemberAppIDs[3]] = inferReply{err: errors.New("member inference unavailable")}
	inferer := &fakeInferer{replies: replies}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	recorder := &fakeVoteRecorder{}
	d := newRecordingTribunalTestDeliberator(t, binding, inferer, signer, recorder)

	if _, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env)); err != nil {
		t.Fatalf("Deliberate: %v", err)
	}

	// First batch: deny, invalid-answer deny, approve. The second batch asks the
	// two remaining members together: no vote, approve. Quorum is not met.
	for _, event := range recorder.events {
		if event.Type != constants.EventConsensusVoteRecorded || event.TransactionID != hash || event.OperatorSessionID != "session-1" {
			t.Fatalf("vote event = type %s tx %s session %s, want %s on %s/session-1",
				event.Type, event.TransactionID, event.OperatorSessionID, constants.EventConsensusVoteRecorded, hash)
		}
	}
	records := recorder.records(t)
	want := []struct {
		outcome models.ConsensusVoteOutcome
		reason  string
		failed  bool
	}{
		{outcome: models.ConsensusVoteOutcomeDeny, reason: "unsafe"},
		{outcome: models.ConsensusVoteOutcomeDeny, failed: true},
		{outcome: models.ConsensusVoteOutcomeApprove, reason: "matches target"},
		{outcome: models.ConsensusVoteOutcomeNoVote, failed: true},
		{outcome: models.ConsensusVoteOutcomeApprove, reason: "matches target"},
	}
	if len(records) != len(want) {
		t.Fatalf("vote records = %d, want %d: %+v", len(records), len(want), records)
	}
	for i, record := range records {
		if record.MemberAppID != binding.MemberAppIDs[i] || record.Outcome != want[i].outcome || record.Reason != want[i].reason || (record.Failure != "") != want[i].failed {
			t.Fatalf("record %d = %+v, want member %s outcome %s reason %q failed %v", i, record, binding.MemberAppIDs[i], want[i].outcome, want[i].reason, want[i].failed)
		}
		if record.TransactionHash != hash || record.ConsensusSetID != tribunalTestBindingID || record.Quorum != 3 || record.ModelTag != tribunalTestModel || record.ProviderAttemptID == "" {
			t.Fatalf("record %d context = %+v", i, record)
		}
	}
}

func TestTribunalConsensusUnrecordedVoteDoesNotCount(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newRecordingTribunalTestDeliberator(t, binding, inferer, signer, &fakeVoteRecorder{err: constants.ErrAuditStoreDisabled})

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}
	if votes := unmarshalDeliberatedEnvelope(t, out).GetGovernance().GetL2().GetVotes(); len(votes) != 0 {
		t.Fatalf("votes = %d, want 0 when no vote can be recorded", len(votes))
	}
}

func TestTribunalConsensusHashMismatchFailsBeforeAnyWork(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	env.TargetResource = "operator://op-1/tampered"
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	_, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if !errors.Is(err, constants.ErrTxTransactionHashMismatch) {
		t.Fatalf("Deliberate error = %v, want ErrTxTransactionHashMismatch", err)
	}
	if len(signer.calls) != 0 {
		t.Fatalf("sign calls = %d, want 0", len(signer.calls))
	}
	if len(inferer.calls) != 0 {
		t.Fatalf("inference calls = %d, want 0", len(inferer.calls))
	}
}

func TestTribunalConsensusCallsCarryMemberIdentityAndPinnedModel(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	if _, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env)); err != nil {
		t.Fatalf("Deliberate: %v", err)
	}
	if len(inferer.calls) == 0 {
		t.Fatal("no inference calls recorded")
	}
	for i, call := range inferer.calls {
		if call.model != tribunalTestModel {
			t.Fatalf("call %d model = %q, want %q", i, call.model, tribunalTestModel)
		}
		if call.modelDigest != tribunalTestDigest {
			t.Fatalf("call %d model digest = %q, want %q", i, call.modelDigest, tribunalTestDigest)
		}
		if call.requestorID != tribunalTestOwnerID {
			t.Fatalf("call %d requestor = %q, want owner %q", i, call.requestorID, tribunalTestOwnerID)
		}
		if call.schema != constants.InferenceRequestSchemaVersion {
			t.Fatalf("call %d request schema = %q, want %q", i, call.schema, constants.InferenceRequestSchemaVersion)
		}
		if call.format.GetJsonSchema() != tribunal.ConsensusVoteJSONSchema {
			t.Fatalf("call %d response format = %v, want the consensus vote JSON schema", i, call.format)
		}
	}
}

func TestTribunalConsensusVotePromptCarriesTypedPayload(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	if _, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env)); err != nil {
		t.Fatalf("Deliberate: %v", err)
	}
	if len(inferer.calls) == 0 {
		t.Fatal("no inference calls recorded")
	}
	for i, call := range inferer.calls {
		var user string
		for _, msg := range call.messages {
			if msg.GetRole() != operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER {
				continue
			}
			for _, part := range msg.GetParts() {
				user += part.GetText()
			}
		}
		if !strings.Contains(user, "<payload>") || !strings.Contains(user, tribunalTestCommand) {
			t.Fatalf("call %d user prompt does not carry the command payload:\n%s", i, user)
		}
	}
}

func TestTribunalConsensusSubjectCarriesEnvelopeContext(t *testing.T) {
	t.Parallel()
	payload, err := proto.Marshal(&operatorv1.OllamaModelResidencyRequested{ExecutionId: "exec-1"})
	require.NoError(t, err)
	env := &commonv1.GovernanceEnvelope{
		ActionType:        string(constants.ActionTypeOllamaModelResidency),
		TargetResource:    "operator://operator-1/inference",
		EventType:         string(constants.Event.Operator.OllamaModelResidency.Requested),
		OperatorId:        "operator-1",
		OperatorSessionId: "session-2",
		CaseId:            "case-3",
		InvestigationId:   "investigation-4",
		TaskId:            "task-5",
		Payload:           payload,
	}

	subject, err := consensusSubject(env)
	require.NoError(t, err)
	assert.Equal(t, "{}", subject.IntentJSON)
	assert.Equal(t, "operator-1", subject.OperatorID)
	assert.Equal(t, "session-2", subject.OperatorSessionID)
	assert.Equal(t, "case-3", subject.CaseID)
	assert.Equal(t, "investigation-4", subject.InvestigationID)
	assert.Equal(t, "task-5", subject.TaskID)

	_, prompt := tribunal.ConsensusVotePrompt(tribunal.MemberAxiom, subject)
	assert.Contains(t, prompt, "Action type: "+subject.ActionType)
	assert.Contains(t, prompt, "Operator session ID: session-2")
	assert.Contains(t, prompt, "<intent_data>\n{}\n</intent_data>")
}

func TestTribunalConsensusUndecodablePayloadFailsBeforeAnyWork(t *testing.T) {
	env, _ := hashTribunalTestEnvelope(t, &commonv1.GovernanceEnvelope{
		ActionType:     string(constants.ActionTypeExecuteBash),
		TargetResource: "operator://op-1/shell",
		EventType:      string(constants.EventOperatorCommandRequested),
		Payload:        []byte{0xff, 0xff, 0xff},
	})
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(t, binding, inferer, signer)

	if _, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env)); err == nil {
		t.Fatal("Deliberate succeeded on an undecodable payload, want error")
	}
	if len(inferer.calls) != 0 || len(signer.calls) != 0 {
		t.Fatalf("inference calls = %d, sign calls = %d, want 0 and 0", len(inferer.calls), len(signer.calls))
	}
}

func TestTribunalConsensusUnconfiguredBindingFailsClosed(t *testing.T) {
	cases := map[string]func(*models.ConsensusPolicy){
		"nil binding": nil,
		"disabled": func(p *models.ConsensusPolicy) {
			p.Enabled = false
		},
		"application provider": func(p *models.ConsensusPolicy) {
			p.Provider = string(constants.ConsensusProviderApplication)
		},
		"invalid provider": func(p *models.ConsensusPolicy) {
			p.Provider = "unknown"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env, _ := tribunalTestEnvelope(t)
			var binding *models.ConsensusPolicy
			if mutate != nil {
				binding = tribunalTestBinding()
				mutate(binding)
			}
			inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
			signer := newFakeSigner(t, tribunalTestBinding().MemberAppIDs...)
			d := newTribunalTestDeliberator(t, binding, inferer, signer)

			_, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
			if !errors.Is(err, constants.ErrTxL2ConsensusNotConfigured) {
				t.Fatalf("Deliberate error = %v, want ErrTxL2ConsensusNotConfigured", err)
			}
			if name == "invalid provider" && !errors.Is(err, constants.ErrConsensusProviderInvalid) {
				t.Fatalf("Deliberate error = %v, want ErrConsensusProviderInvalid in the error chain", err)
			}
			if len(inferer.calls) != 0 {
				t.Fatalf("inference calls = %d, want 0", len(inferer.calls))
			}
		})
	}
}

func TestConsensusDeliberatorRouterTribunalProviderRoutesToTribunal(t *testing.T) {
	env, hash := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	app := &fakeApplicationDeliberator{}
	router := &consensusDeliberatorRouter{
		store:       &fakeBinding{policy: binding},
		application: app,
		tribunal:    newTribunalTestDeliberator(t, binding, inferer, signer),
	}

	out, err := router.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("router Deliberate: %v", err)
	}
	if app.calls != 0 {
		t.Fatalf("application deliberator calls = %d, want 0", app.calls)
	}
	if len(inferer.calls) != 3 {
		t.Fatalf("inference calls = %d, want 3", len(inferer.calls))
	}
	votes := unmarshalDeliberatedEnvelope(t, out).GetGovernance().GetL2().GetVotes()
	if len(votes) != 3 {
		t.Fatalf("votes = %d, want 3", len(votes))
	}
	for _, vote := range votes {
		assertVoteSignature(t, signer, hash, vote)
	}
}

func TestConsensusDeliberatorRouterApplicationProviderRoutesToApplication(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	binding.Provider = string(constants.ConsensusProviderApplication)
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	wantOut := []byte(`{"sentinel":true}`)
	app := &fakeApplicationDeliberator{out: wantOut}
	router := &consensusDeliberatorRouter{
		store:       &fakeBinding{policy: binding},
		application: app,
		tribunal:    newTribunalTestDeliberator(t, binding, inferer, signer),
	}

	out, err := router.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("router Deliberate: %v", err)
	}
	if app.calls != 1 {
		t.Fatalf("application deliberator calls = %d, want 1", app.calls)
	}
	if string(out) != string(wantOut) {
		t.Fatalf("router output = %s, want application output %s", out, wantOut)
	}
	if len(inferer.calls) != 0 || len(signer.calls) != 0 {
		t.Fatalf("tribunal ran for application provider: inference=%d sign=%d", len(inferer.calls), len(signer.calls))
	}
}

func TestConsensusDeliberatorRouterFailsClosedWithoutBindingOrTribunal(t *testing.T) {
	cases := map[string]struct {
		binding  *models.ConsensusPolicy
		tribunal bool
	}{
		"nil binding":                  {binding: nil, tribunal: true},
		"tribunal without deliberator": {binding: tribunalTestBinding(), tribunal: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, _ := tribunalTestEnvelope(t)
			inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
			signer := newFakeSigner(t, tribunalTestBinding().MemberAppIDs...)
			app := &fakeApplicationDeliberator{}
			router := &consensusDeliberatorRouter{
				store:       &fakeBinding{policy: tc.binding},
				application: app,
			}
			if tc.tribunal {
				router.tribunal = newTribunalTestDeliberator(t, tribunalTestBinding(), inferer, signer)
			}

			_, err := router.Deliberate(context.Background(), marshalTestEnvelope(t, env))
			if !errors.Is(err, constants.ErrTxL2ConsensusNotConfigured) {
				t.Fatalf("router Deliberate error = %v, want ErrTxL2ConsensusNotConfigured", err)
			}
			if app.calls != 0 {
				t.Fatalf("application deliberator calls = %d, want 0", app.calls)
			}
			if len(inferer.calls) != 0 {
				t.Fatalf("inference calls = %d, want 0", len(inferer.calls))
			}
		})
	}
}
