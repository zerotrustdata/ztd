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
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

var errBoom = errors.New("boom")

// witnessParentEnv is an admitted INFERENCE holding an L2 quorum.
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

func TestAdmit(t *testing.T) {
	t.Run("nil OnAdmitted is a no-op", func(t *testing.T) {
		require.NoError(t, admit(context.Background(), DispatchRequest{}, witnessParentEnv()))
	})

	t.Run("OnAdmitted runs under the parent binding", func(t *testing.T) {
		var got string
		req := DispatchRequest{OnAdmitted: func(ctx context.Context) error {
			got = witnessParentHash(ctx)
			return nil
		}}
		require.NoError(t, admit(context.Background(), req, witnessParentEnv()))
		assert.Equal(t, "parent-hash", got)
	})

	t.Run("OnAdmitted error is wrapped", func(t *testing.T) {
		req := DispatchRequest{OnAdmitted: func(context.Context) error { return errBoom }}
		err := admit(context.Background(), req, witnessParentEnv())
		assert.ErrorIs(t, err, errBoom)
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

// votingL2Deliberator attaches one affirmative vote to the envelope it is given.
type votingL2Deliberator struct{}

func (votingL2Deliberator) Deliberate(_ context.Context, envelopeBytes []byte) ([]byte, error) {
	env := &commonv1.GovernanceEnvelope{}
	if err := protojson.Unmarshal(envelopeBytes, env); err != nil {
		return nil, err
	}
	env.Governance.L2 = &commonv1.L2Metadata{
		ConsensusSetId: "set",
		Votes:          []*commonv1.L2Vote{{SignerKeyId: "m1", ConsensusSignature: "aa", Decision: true}},
	}
	return protojson.Marshal(env)
}

// TestDispatchService_OnAdmittedRunsWithDeliberatedQuorumBeforePublish proves
// that Dispatch runs OnAdmitted after L2 deliberation and before publish, under
// a ctx bound to the deliberated INFERENCE, and that a hook error stops publish.
func TestDispatchService_OnAdmittedRunsWithDeliberatedQuorumBeforePublish(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	op := &operatorv1.OperatorDocument{Id: "op-001", OperatorSessionId: "sess-001"}
	dispatch := func(t *testing.T, hook func(ctx context.Context, published bool) error) (bool, error) {
		t.Helper()
		broker := NewGatewayWebSocketHandler(logger)
		svc := NewDispatchService(
			logger, broker,
			&stubStateRootProvider{root: "root-abc"},
			&stubOperatorSessionValidator{op: op},
			constants.PostureConsensus,
			testDoctrine(),
			votingL2Deliberator{},
			nil,
			nil,
			nil,
		)
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
			EventType:               string(constants.Event.Operator.Inference.Requested),
			Payload:                 inferencePayload(t),
			RequestorUserID:         "user-001",
			OnAdmitted: func(ctx context.Context) error {
				return hook(ctx, published)
			},
		})
		return published, err
	}

	t.Run("hook sees the deliberated parent before publish", func(t *testing.T) {
		var parentHash string
		var publishedAtHook bool
		published, _ := dispatch(t, func(ctx context.Context, published bool) error {
			parentHash, publishedAtHook = witnessParentHash(ctx), published
			return nil
		})
		assert.NotEmpty(t, parentHash, "OnAdmitted must run under the deliberated INFERENCE")
		assert.False(t, publishedAtHook, "OnAdmitted must run before publish")
		assert.True(t, published)
	})

	t.Run("hook error prevents publish", func(t *testing.T) {
		published, err := dispatch(t, func(context.Context, bool) error { return errBoom })
		assert.ErrorIs(t, err, errBoom)
		assert.False(t, published, "envelope must not be published when OnAdmitted fails")
	})
}
