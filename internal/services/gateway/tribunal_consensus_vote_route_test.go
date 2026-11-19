// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// voteRouteOperators lists the same Operators for every owner.
type voteRouteOperators struct {
	ops []*operatorv1.OperatorDocument
}

func (l voteRouteOperators) ListUserOperators(context.Context, string) ([]*operatorv1.OperatorDocument, error) {
	return l.ops, nil
}

func voteRouteOperator(sessionID string, hostedClasses ...string) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                "op-" + sessionID,
		OperatorSessionId: sessionID,
		Status:            string(constants.OperatorStatusActive),
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
			Capabilities:             []string{string(constants.OperatorCapabilityInference)},
			InferenceHostedProviders: hostedClasses,
		},
	}
}

// TestTribunalVoteRoute_UsesTheVoteDispatchSelection resolves each member's
// vote route through the real dispatch selection: a hosted binding routes only
// to an active Inference Operator that serves its class, and a local binding
// to one that serves the local provider.
func TestTribunalVoteRoute_UsesTheVoteDispatchSelection(t *testing.T) {
	stale := voteRouteOperator("sess-stale", constants.InferenceProviderGemini)
	stale.Status = string(constants.OperatorStatusStale)
	decisionOnly := voteRouteOperator("sess-decision")
	decisionOnly.RuntimeConfig.InferenceDecisionOnly = true
	for name, tc := range map[string]struct {
		providerClass string
		ops           []*operatorv1.OperatorDocument
		want          error
	}{
		"hosted binding with an Operator serving its class": {constants.InferenceProviderGemini, []*operatorv1.OperatorDocument{voteRouteOperator("sess-local"), voteRouteOperator("sess-gemini", constants.InferenceProviderGemini)}, nil},
		"hosted binding with no Operator serving its class": {constants.InferenceProviderGemini, []*operatorv1.OperatorDocument{voteRouteOperator("sess-local"), voteRouteOperator("sess-openai", constants.InferenceProviderOpenAI)}, constants.ErrInferenceOperatorNotFound},
		"hosted binding whose serving Operator is stale":    {constants.InferenceProviderGemini, []*operatorv1.OperatorDocument{stale}, constants.ErrInferenceOperatorNotFound},
		"local binding with an Operator":                    {"", []*operatorv1.OperatorDocument{voteRouteOperator("sess-local")}, nil},
		"local binding with no Operator":                    {"", nil, constants.ErrInferenceOperatorNotFound},
		"local binding with only a decision-only Operator":  {"", []*operatorv1.OperatorDocument{decisionOnly}, constants.ErrInferenceOperatorNotFound},
		"local binding with two Operators and none pinned":  {"", []*operatorv1.OperatorDocument{voteRouteOperator("sess-a"), voteRouteOperator("sess-b")}, constants.ErrInferenceOperatorAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			binding := tribunalTestBinding()
			binding.ModelProviderClass = tc.providerClass
			inference := dispatch.NewDispatchService(nil, voteRouteOperators{ops: tc.ops}, slog.New(slog.DiscardHandler))
			d, err := NewTribunalConsensusDeliberator(&fakeBinding{policy: binding}, fakePolicies{}, inference, &fakeVoteRecorder{}, nil, dispatch.DefaultTransientRetry(), slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			err = d.VoteRoute(t.Context(), binding, binding.MemberAppIDs[0])
			if tc.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// TestTribunalVoteRoute_DecisionVoteRoutesOnlyToThePinnedOperator checks a
// decision binding's route against its pinned Operator's decision models.
func TestTribunalVoteRoute_DecisionVoteRoutesOnlyToThePinnedOperator(t *testing.T) {
	binding := tribunalTestBinding()
	binding.VoteKind = models.ConsensusVoteKindDecision
	binding.InferenceOperatorID = "op-sess-decision"
	pinned := voteRouteOperator("sess-decision")
	pinned.RuntimeConfig.InferenceDecisionBackend = "ollama-systemone"
	pinned.RuntimeConfig.InferenceDecisionModels = []string{binding.ModelTag}
	lister := &voteRouteOperators{ops: []*operatorv1.OperatorDocument{pinned}}
	d, err := NewTribunalConsensusDeliberator(&fakeBinding{policy: binding}, fakePolicies{}, dispatch.NewDispatchService(nil, lister, slog.New(slog.DiscardHandler)), &fakeVoteRecorder{}, nil, dispatch.DefaultTransientRetry(), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, d.VoteRoute(t.Context(), binding, binding.MemberAppIDs[0]))

	pinned.RuntimeConfig.InferenceDecisionModels = []string{"another-model"}
	require.ErrorIs(t, d.VoteRoute(t.Context(), binding, binding.MemberAppIDs[0]), constants.ErrInferenceOperatorNotFound)

	pinned.RuntimeConfig.InferenceDecisionModels = []string{binding.ModelTag}
	binding.AllowHosted = true
	require.ErrorIs(t, d.VoteRoute(t.Context(), binding, binding.MemberAppIDs[0]), constants.ErrInferenceOperatorNotFound)
}

// TestTribunalVoteRoute_MemberWithoutAnOwnerDoesNotRoute keeps the owner
// failure distinct from a routing failure.
func TestTribunalVoteRoute_MemberWithoutAnOwnerDoesNotRoute(t *testing.T) {
	binding := tribunalTestBinding()
	d, err := NewTribunalConsensusDeliberator(&fakeBinding{policy: binding}, ownerlessPolicies{}, dispatch.NewDispatchService(nil, voteRouteOperators{ops: []*operatorv1.OperatorDocument{voteRouteOperator("sess-local")}}, slog.New(slog.DiscardHandler)), &fakeVoteRecorder{}, nil, dispatch.DefaultTransientRetry(), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.ErrorIs(t, d.VoteRoute(t.Context(), binding, binding.MemberAppIDs[0]), constants.ErrConsensusAppNotEnrolled)
}

type ownerlessPolicies struct{}

func (ownerlessPolicies) GetAppPolicy(context.Context, string) (*models.AppPolicy, error) {
	return nil, nil
}
