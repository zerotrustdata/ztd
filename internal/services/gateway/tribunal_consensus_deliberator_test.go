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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
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

// fakePolicies returns every member app owned by tribunalTestOwnerID.
type fakePolicies struct{}

func (fakePolicies) GetAppPolicy(_ context.Context, appID string) (*models.AppPolicy, error) {
	return &models.AppPolicy{AppID: appID, OwnerUserID: tribunalTestOwnerID}, nil
}

// inferReply is one member's scripted inference outcome.
type inferReply struct {
	text string
	err  error
}

// inferCall records one DispatchInference call as the deliberator issued it.
type inferCall struct {
	actingAppID  string
	model        string
	modelDigest  string
	requestorID  string
	deliberating string
}

// fakeInferer answers each member's vote from a script keyed by ActingAppID.
// A member with no script entry fails, so unexpected calls are visible.
type fakeInferer struct {
	replies map[string]inferReply
	calls   []inferCall
}

func (f *fakeInferer) DispatchInference(ctx context.Context, req dispatch.DispatchInferenceRequest) (*dispatch.DispatchInferenceResult, error) {
	deliberating, _ := ctx.Value(consensusDeliberationKey{}).(string)
	f.calls = append(f.calls, inferCall{
		actingAppID:  req.ActingAppID,
		model:        req.Model,
		modelDigest:  req.ModelDigest,
		requestorID:  req.RequestorUserID,
		deliberating: deliberating,
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

// tribunalTestEnvelope returns a hashed envelope whose transaction hash
// matches its canonical content.
func tribunalTestEnvelope(t *testing.T) (*commonv1.GovernanceEnvelope, string) {
	t.Helper()
	env := &commonv1.GovernanceEnvelope{
		ActionType:     string(constants.ActionTypeInference),
		TargetResource: "operator://op-1/inference",
		EventType:      string(constants.EventOperatorCommandRequested),
	}
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

func newTribunalTestDeliberator(binding *models.ConsensusPolicy, inferer *fakeInferer, signer *fakeSigner) *TribunalConsensusDeliberator {
	logger := slog.New(slog.DiscardHandler)
	return NewTribunalConsensusDeliberator(&fakeBinding{policy: binding}, fakePolicies{}, inferer, signer, logger)
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

func TestTribunalConsensusAllApproveStopsAtQuorum(t *testing.T) {
	env, hash := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(binding, inferer, signer)

	out, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
	if err != nil {
		t.Fatalf("Deliberate: %v", err)
	}

	if len(inferer.calls) != 3 {
		t.Fatalf("inference calls = %d, want 3 (short-circuit at quorum)", len(inferer.calls))
	}
	for i, call := range inferer.calls {
		if call.actingAppID != binding.MemberAppIDs[i] {
			t.Fatalf("call %d acting app = %s, want %s", i, call.actingAppID, binding.MemberAppIDs[i])
		}
	}
	if len(signer.calls) != 3 {
		t.Fatalf("sign calls = %d, want 3", len(signer.calls))
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
	d := newTribunalTestDeliberator(binding, inferer, signer)

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
	d := newTribunalTestDeliberator(binding, inferer, signer)

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
	d := newTribunalTestDeliberator(binding, inferer, signer)

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

func TestTribunalConsensusMemberWithoutKeyCastsNoVote(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	binding := tribunalTestBinding()
	missing := binding.MemberAppIDs[1]
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, membersExcept(missing)...)
	d := newTribunalTestDeliberator(binding, inferer, signer)

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

func TestTribunalConsensusHashMismatchFailsBeforeAnyWork(t *testing.T) {
	env, _ := tribunalTestEnvelope(t)
	env.TargetResource = "operator://op-1/tampered"
	binding := tribunalTestBinding()
	inferer := &fakeInferer{replies: repliesForAll(inferReply{text: tribunalApproveAnswer})}
	signer := newFakeSigner(t, binding.MemberAppIDs...)
	d := newTribunalTestDeliberator(binding, inferer, signer)

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
	d := newTribunalTestDeliberator(binding, inferer, signer)

	if _, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env)); err != nil {
		t.Fatalf("Deliberate: %v", err)
	}
	if len(inferer.calls) == 0 {
		t.Fatal("no inference calls recorded")
	}
	for i, call := range inferer.calls {
		if call.deliberating != call.actingAppID {
			t.Fatalf("call %d ctx deliberating member = %q, want acting app %q", i, call.deliberating, call.actingAppID)
		}
		if call.model != tribunalTestModel {
			t.Fatalf("call %d model = %q, want %q", i, call.model, tribunalTestModel)
		}
		if call.modelDigest != tribunalTestDigest {
			t.Fatalf("call %d model digest = %q, want %q", i, call.modelDigest, tribunalTestDigest)
		}
		if call.requestorID != tribunalTestOwnerID {
			t.Fatalf("call %d requestor = %q, want owner %q", i, call.requestorID, tribunalTestOwnerID)
		}
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
			d := newTribunalTestDeliberator(binding, inferer, signer)

			_, err := d.Deliberate(context.Background(), marshalTestEnvelope(t, env))
			if !errors.Is(err, constants.ErrTxL2ConsensusNotConfigured) {
				t.Fatalf("Deliberate error = %v, want ErrTxL2ConsensusNotConfigured", err)
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
		tribunal:    newTribunalTestDeliberator(binding, inferer, signer),
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
		tribunal:    newTribunalTestDeliberator(binding, inferer, signer),
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
				router.tribunal = newTribunalTestDeliberator(tribunalTestBinding(), inferer, signer)
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
