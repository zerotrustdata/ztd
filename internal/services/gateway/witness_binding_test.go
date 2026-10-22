// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software
// is released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

var errBoom = errors.New("boom")

// witnessParentEnv is an admitted INFERENCE carrying an unsigned L2 vote: enough
// to mark a witness parent, never enough to hold quorum.
func witnessParentEnv() *commonv1.GovernanceEnvelope {
	return &commonv1.GovernanceEnvelope{
		ActionType:      string(constants.ActionTypeInference),
		TransactionHash: "parent-hash",
		Governance: &commonv1.GovernanceMetadata{L2: &commonv1.L2Metadata{
			ConsensusSetId: "set",
			Votes:          []*commonv1.L2Vote{{SignerKeyId: "m1", ConsensusSignature: "aa", Decision: true}},
		}},
	}
}

// witnessObservationEnv is a provider-boundary observation naming inferenceID.
func witnessObservationEnv(t *testing.T, inferenceID string) *commonv1.GovernanceEnvelope {
	t.Helper()
	payload, err := proto.Marshal(&evalv1.ProviderBoundaryObservationCommand{
		ProviderAttemptId:      "attempt-1",
		InferenceTransactionId: inferenceID,
		Phase:                  evalv1.ProviderBoundaryObservationPhase_PROVIDER_BOUNDARY_OBSERVATION_PHASE_BEGIN,
	})
	require.NoError(t, err)
	return &commonv1.GovernanceEnvelope{
		ActionType: string(constants.ActionTypeProviderBoundaryObservation),
		Payload:    payload,
	}
}

func TestWithWitnessParent(t *testing.T) {
	withDeliberation := witnessParentEnv()
	withDeliberation.Governance.L2.Deliberation = &commonv1.L2DeliberationAttestation{SignerKeyId: "m1"}

	noVotes := witnessParentEnv()
	noVotes.Governance.L2.Votes = nil

	nonInference := witnessParentEnv()
	nonInference.ActionType = string(constants.ActionTypeFsRead)

	tests := []struct {
		name     string
		admitted *commonv1.GovernanceEnvelope
		want     string
	}{
		{name: "inference with votes marks parent", admitted: witnessParentEnv(), want: "parent-hash"},
		{name: "inference without votes is unmarked", admitted: noVotes, want: ""},
		{name: "inference with deliberation is unmarked", admitted: withDeliberation, want: ""},
		{name: "non-inference with votes is unmarked", admitted: nonInference, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := withWitnessParent(context.Background(), tc.admitted)
			assert.Equal(t, tc.want, witnessParentHash(ctx))
		})
	}
}

func TestWithWitnessParentClonesQuorum(t *testing.T) {
	parent := witnessParentEnv()
	ctx := withWitnessParent(context.Background(), parent)

	// Mutating the admitted envelope after marking must not reach the binding.
	parent.Governance.L2.Votes[0].SignerKeyId = "mutated"

	env := witnessObservationEnv(t, "parent-hash")
	require.NoError(t, attachWitnessBinding(ctx, env))
	require.Len(t, env.GetGovernance().GetL2().GetVotes(), 1)
	assert.Equal(t, "m1", env.GetGovernance().GetL2().GetVotes()[0].GetSignerKeyId())
}

func TestWitnessInferenceTransactionID(t *testing.T) {
	marked := withWitnessParent(context.Background(), witnessParentEnv())

	assert.Equal(t, "explicit-id", witnessInferenceTransactionID(marked, "explicit-id"))
	assert.Equal(t, "parent-hash", witnessInferenceTransactionID(marked, ""))
	assert.Equal(t, "", witnessInferenceTransactionID(context.Background(), ""))
}

func TestAttachWitnessBinding(t *testing.T) {
	t.Run("unmarked context does nothing", func(t *testing.T) {
		env := witnessObservationEnv(t, "parent-hash")
		require.NoError(t, attachWitnessBinding(context.Background(), env))
		assert.Nil(t, env.GetGovernance())
	})

	t.Run("marked context binds parent quorum", func(t *testing.T) {
		ctx := withWitnessParent(context.Background(), witnessParentEnv())
		env := witnessObservationEnv(t, "parent-hash")
		require.NoError(t, attachWitnessBinding(ctx, env))

		l2 := env.GetGovernance().GetL2()
		assert.Equal(t, "parent-hash", l2.GetParentTransactionHash())
		assert.Equal(t, "set", l2.GetConsensusSetId())
		require.Len(t, l2.GetVotes(), 1)
		assert.Equal(t, "m1", l2.GetVotes()[0].GetSignerKeyId())
		assert.Nil(t, l2.GetDeliberation())
	})

	t.Run("command naming another inference fails closed", func(t *testing.T) {
		ctx := withWitnessParent(context.Background(), witnessParentEnv())
		env := witnessObservationEnv(t, "other-hash")
		err := attachWitnessBinding(ctx, env)
		assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
	})

	t.Run("deliberation-attested observation is left untouched", func(t *testing.T) {
		ctx := withWitnessParent(context.Background(), witnessParentEnv())
		env := witnessObservationEnv(t, "parent-hash")
		env.Governance = &commonv1.GovernanceMetadata{L2: &commonv1.L2Metadata{
			ConsensusSetId: string(constants.DocIDConsensusBinding),
			Deliberation:   &commonv1.L2DeliberationAttestation{SignerKeyId: "member"},
		}}
		before := proto.Clone(env.GetGovernance().GetL2())

		require.NoError(t, attachWitnessBinding(ctx, env))
		assert.True(t, proto.Equal(before, env.GetGovernance().GetL2()))
		assert.Empty(t, env.GetGovernance().GetL2().GetParentTransactionHash())
	})
}

// witnessQuorum is a one-member, quorum-one consensus whose member signs votes.
type witnessQuorum struct {
	key ed25519.PrivateKey
}

func newWitnessQuorum(t *testing.T) witnessQuorum {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return witnessQuorum{key: key}
}

func (q witnessQuorum) GetConsensusPolicy(context.Context, string) (*governance.L2ConsensusPolicy, error) {
	return &governance.L2ConsensusPolicy{MemberKeyIDs: []string{"m1"}, Quorum: 1, RequireDistinct: true, Enabled: true}, nil
}

func (q witnessQuorum) signers() governance.SignerStore {
	return &governance.FailClosedSignerStore{Signers: map[string]ed25519.PublicKey{"m1": q.key.Public().(ed25519.PublicKey)}}
}

// vote is m1's signed vote on transactionHash.
func (q witnessQuorum) vote(transactionHash string, decision bool) *commonv1.L2Vote {
	return &commonv1.L2Vote{
		SignerKeyId:        "m1",
		ConsensusSignature: hex.EncodeToString(ed25519.Sign(q.key, governance.L2VotePayload(transactionHash, decision))),
		Decision:           decision,
	}
}

// quorumDispatchService is a consensus-posture DispatchService that verifies
// admitted votes against q.
func quorumDispatchService(q witnessQuorum) *DispatchService {
	svc := &DispatchService{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), posture: constants.PostureConsensus}
	svc.SetL2QuorumSources(q, q.signers())
	return svc
}

func TestAdmit(t *testing.T) {
	q := newWitnessQuorum(t)
	approved := witnessParentEnv()
	approved.Governance.L2.Votes = []*commonv1.L2Vote{q.vote("parent-hash", true)}
	denied := witnessParentEnv()
	denied.Governance.L2.Votes = []*commonv1.L2Vote{q.vote("parent-hash", false)}
	forged := witnessParentEnv()

	t.Run("nil OnAdmitted is a no-op", func(t *testing.T) {
		require.NoError(t, quorumDispatchService(q).admit(context.Background(), DispatchRequest{}, approved))
	})

	t.Run("OnAdmitted runs under the parent binding", func(t *testing.T) {
		var got string
		req := DispatchRequest{OnAdmitted: func(ctx context.Context) error {
			got = witnessParentHash(ctx)
			return nil
		}}
		require.NoError(t, quorumDispatchService(q).admit(context.Background(), req, approved))
		assert.Equal(t, "parent-hash", got)
	})

	t.Run("OnAdmitted error is wrapped", func(t *testing.T) {
		req := DispatchRequest{OnAdmitted: func(context.Context) error { return errBoom }}
		err := quorumDispatchService(q).admit(context.Background(), req, approved)
		assert.ErrorIs(t, err, errBoom)
	})

	// A state change is L2-gated under consensus, so its votes decide whether
	// OnAdmitted runs.
	stateChange := func(env *commonv1.GovernanceEnvelope) *commonv1.GovernanceEnvelope {
		env.ActionType = string(constants.ActionTypeExecuteBash)
		return env
	}
	for name, admitted := range map[string]*commonv1.GovernanceEnvelope{"denied quorum": stateChange(denied), "unverifiable votes": stateChange(forged)} {
		t.Run(name+" on a state change never runs OnAdmitted", func(t *testing.T) {
			ran := false
			req := DispatchRequest{OnAdmitted: func(context.Context) error { ran = true; return nil }}
			require.NoError(t, quorumDispatchService(q).admit(context.Background(), req, admitted))
			assert.False(t, ran)
		})
	}

	t.Run("without quorum sources an L2-gated envelope never runs OnAdmitted", func(t *testing.T) {
		ran := false
		req := DispatchRequest{OnAdmitted: func(context.Context) error { ran = true; return nil }}
		svc := &DispatchService{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), posture: constants.PostureConsensus}
		gated := witnessParentEnv()
		gated.ActionType = string(constants.ActionTypeExecuteBash)
		require.NoError(t, svc.admit(context.Background(), req, gated))
		assert.False(t, ran)
	})

	t.Run("an envelope L2 does not gate runs OnAdmitted", func(t *testing.T) {
		ran := false
		req := DispatchRequest{OnAdmitted: func(context.Context) error { ran = true; return nil }}
		svc := &DispatchService{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), posture: constants.PostureDoctrine}
		require.NoError(t, svc.admit(context.Background(), req, stateChange(witnessParentEnv())))
		assert.True(t, ran)
	})

	t.Run("an INFERENCE is not a state change, so it runs OnAdmitted under consensus without votes", func(t *testing.T) {
		var parent string
		ran := false
		req := DispatchRequest{OnAdmitted: func(ctx context.Context) error { ran, parent = true, witnessParentHash(ctx); return nil }}
		unvoted := witnessParentEnv()
		unvoted.Governance.L2 = nil
		require.NoError(t, quorumDispatchService(q).admit(context.Background(), req, unvoted))
		assert.True(t, ran)
		assert.Empty(t, parent, "an un-voted INFERENCE binds no witness parent")
	})
}

// TestDispatchObservation_WitnessBoundSkipsDeliberation proves that under a
// witness parent mark, DispatchObservation publishes the observation bound to
// the parent's quorum without deliberating it, and that a command naming
// another inference fails closed before publish.
func TestDispatchObservation_WitnessBoundSkipsDeliberation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	observe := func(t *testing.T, inferenceID string) (*commonv1.GovernanceEnvelope, error) {
		t.Helper()
		broker := NewGatewayWebSocketHandler(logger)
		svc := NewDispatchService(
			logger, broker,
			&stubStateRootProvider{root: "root-abc"},
			&stubOperatorSessionValidator{op: op},
			constants.PostureConsensus,
			testDoctrine(),
			failingL2Deliberator{},
			nil,
			nil,
			nil,
		)
		ctx, cancel := context.WithCancel(withWitnessParent(context.Background(), witnessParentEnv()))
		defer cancel()
		var published *commonv1.GovernanceEnvelope
		unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, data []byte) {
			published = &commonv1.GovernanceEnvelope{}
			if err := protojson.Unmarshal(data, published); err != nil {
				t.Errorf("decode published observation: %v", err)
			}
			cancel()
		})
		defer unreg()
		_, err := svc.DispatchObservation(ctx, ObservationDispatchRequest{
			TargetOperatorSessionID: op.OperatorSessionId,
			EventType:               string(constants.Event.Operator.ProviderBoundaryObservation.Requested),
			Payload:                 witnessObservationEnv(t, inferenceID).GetPayload(),
			RequestorUserID:         "user-001",
		})
		return published, err
	}

	t.Run("bound observation publishes without deliberation", func(t *testing.T) {
		published, err := observe(t, "parent-hash")
		assert.NotErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
		require.NotNil(t, published, "witness-bound observation must publish")
		l2 := published.GetGovernance().GetL2()
		assert.Equal(t, "parent-hash", l2.GetParentTransactionHash())
		require.Len(t, l2.GetVotes(), 1)
		assert.Equal(t, "m1", l2.GetVotes()[0].GetSignerKeyId())
	})

	t.Run("observation naming another inference fails closed", func(t *testing.T) {
		published, err := observe(t, "other-hash")
		assert.ErrorIs(t, err, constants.ErrTxL2SignatureInvalid)
		assert.Nil(t, published)
	})
}

// votingL2Deliberator attaches m1's signed vote, decision, to the envelope it
// is given.
type votingL2Deliberator struct {
	quorum   witnessQuorum
	decision bool
}

func (d votingL2Deliberator) Deliberate(_ context.Context, envelopeBytes []byte) ([]byte, error) {
	env := &commonv1.GovernanceEnvelope{}
	if err := protojson.Unmarshal(envelopeBytes, env); err != nil {
		return nil, err
	}
	env.Governance.L2 = &commonv1.L2Metadata{
		ConsensusSetId: "set",
		Votes:          []*commonv1.L2Vote{d.quorum.vote(env.GetTransactionHash(), d.decision)},
	}
	return protojson.Marshal(env)
}

// TestDispatchService_OnAdmittedRunsWithDeliberatedQuorumBeforePublish proves
// that Dispatch runs OnAdmitted before publish, that a hook error stops
// publish, and that an L2-gated state change runs OnAdmitted only after its
// deliberation reaches quorum: a deliberation short of quorum publishes the
// envelope without running OnAdmitted. An INFERENCE is not a state change, so
// it is never deliberated and its hook runs unbound to a witness parent.
func TestDispatchService_OnAdmittedRunsWithDeliberatedQuorumBeforePublish(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	q := newWitnessQuorum(t)
	inference := DispatchRequest{EventType: string(constants.Event.Operator.Inference.Requested)}
	command := DispatchRequest{EventType: string(constants.Event.Operator.Command.Requested)}
	dispatchDecided := func(t *testing.T, base DispatchRequest, decision bool, hook func(ctx context.Context, published bool) error) (bool, error) {
		t.Helper()
		payload := inferencePayload(t)
		if base.EventType == command.EventType {
			payload = commandPayload(t)
		}
		broker := NewGatewayWebSocketHandler(logger)
		svc := NewDispatchService(
			logger, broker,
			&stubStateRootProvider{root: "root-abc"},
			&stubOperatorSessionValidator{op: op},
			constants.PostureConsensus,
			testDoctrine(),
			votingL2Deliberator{quorum: q, decision: decision},
			nil,
			nil,
			nil,
		)
		svc.SetL2QuorumSources(q, q.signers())
		dispatchCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		published := false
		unreg := broker.RegisterHandler(pubsub.CmdChannel(op.Id, op.OperatorSessionId), func(_ string, _ []byte) {
			// No result will arrive; end the wait once the envelope publishes.
			published = true
			cancel()
		})
		defer unreg()
		_, err := svc.Dispatch(dispatchCtx, DispatchRequest{
			TargetOperatorSessionID: op.OperatorSessionId,
			EventType:               base.EventType,
			Payload:                 payload,
			RequestorUserID:         "user-001",
			OnAdmitted: func(ctx context.Context) error {
				return hook(ctx, published)
			},
		})
		return published, err
	}

	t.Run("an INFERENCE hook runs unbound before publish", func(t *testing.T) {
		var parentHash string
		var publishedAtHook bool
		ran := false
		published, _ := dispatchDecided(t, inference, false, func(ctx context.Context, published bool) error {
			ran, parentHash, publishedAtHook = true, witnessParentHash(ctx), published
			return nil
		})
		assert.True(t, ran, "an INFERENCE is never deliberated, so a denying deliberator cannot stop its witnesses")
		assert.Empty(t, parentHash, "an un-voted INFERENCE binds no witness parent")
		assert.False(t, publishedAtHook, "OnAdmitted must run before publish")
		assert.True(t, published)
	})

	t.Run("hook error prevents publish", func(t *testing.T) {
		published, err := dispatchDecided(t, inference, true, func(context.Context, bool) error { return errBoom })
		assert.ErrorIs(t, err, errBoom)
		assert.False(t, published, "envelope must not be published when OnAdmitted fails")
	})

	t.Run("a state change hook runs after quorum, before publish", func(t *testing.T) {
		var publishedAtHook bool
		ran := false
		published, _ := dispatchDecided(t, command, true, func(_ context.Context, published bool) error {
			ran, publishedAtHook = true, published
			return nil
		})
		assert.True(t, ran)
		assert.False(t, publishedAtHook, "OnAdmitted must run before publish")
		assert.True(t, published)
	})

	t.Run("quorum-denied deliberation publishes without running the hook", func(t *testing.T) {
		ran := false
		published, _ := dispatchDecided(t, command, false, func(context.Context, bool) error { ran = true; return nil })
		assert.False(t, ran, "nothing may begin for a state change short of quorum")
		assert.True(t, published, "the executing Operator must sign the L2 rejection")
	})
}
