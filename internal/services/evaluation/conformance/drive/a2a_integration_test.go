// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package drive

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// a2aAttempt builds an attempt for one fault with the fixed marker used by the A2A tests.
func a2aAttempt(c cases.Case) cases.Attempt {
	return cases.Attempt{
		RunID:     "run-1",
		AttemptID: "attempt-1",
		Case:      c,
		Marker:    "marker-1",
	}
}

// TestA2APayloadPerFault pins the payload each fault sends. The payload is built without a
// Gateway; the real-Gateway tests in this file check what the stub receives.
func TestA2APayloadPerFault(t *testing.T) {
	tests := []struct {
		name        string
		c           cases.Case
		wantPayload string
	}{
		{
			name:        "none sends the marker",
			c:           cases.Case{ID: "row-1", Fault: cases.FaultNone, Action: cases.ActionMutation},
			wantPayload: `{"marker":"marker-1"}`,
		},
		{
			name:        "unattended mutation sends the marker",
			c:           cases.Case{ID: "row-23", Fault: cases.FaultUnattendedMutation, Action: cases.ActionMutation},
			wantPayload: `{"marker":"marker-1"}`,
		},
		{
			name:        "attended approve sends the marker",
			c:           cases.Case{ID: "row-24", Fault: cases.FaultAttendedApprove, Action: cases.ActionMutation},
			wantPayload: `{"marker":"marker-1"}`,
		},
		{
			name:        "doctrine sends the corpus command as cmd",
			c:           cases.Case{ID: "row-14", Fault: cases.FaultDoctrine, Action: cases.ActionMutation, Command: "rm -rf /"},
			wantPayload: `{"cmd":"rm -rf /"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := a2aPayload(a2aAttempt(tt.c))
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantPayload, string(payload))
		})
	}
}

// TestA2AAllowedCallReachesStubAndClosesAttempt checks that an allowed call runs on a real
// Gateway: the Gateway forwards the skill to the stub, the answer is a receipt, and the
// attempt is closed once Drive returns.
func TestA2AAllowedCallReachesStubAndClosesAttempt(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	stub := startStub(t)
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName:         "conformance-a2a-allowed",
		Posture:          constants.PostureDoctrine,
		A2ADownstreamURL: stub.URL(),
	})
	d := NewA2A(gw.client, gw.persona, stub)
	fx := &directFixture{f: gw.f}

	c := findCase(t, all, 1, cases.IngressA2A, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)
	a := fx.attemptFor(t, c)
	driven, err := d.Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, len(c.Expect))
	for i, want := range c.Expect {
		assertObservation(t, want, driven.Observations[i])
	}

	require.Len(t, driven.A2ACalls, 1, "exactly one stub call must carry the marker")
	assert.Equal(t, ConformanceA2ASkill, driven.A2ACalls[0].GetSkillName())
	want, err := json.Marshal(cases.MarkerSkillPayload{Marker: a.Marker})
	require.NoError(t, err)
	assert.JSONEq(t, string(want), driven.A2ACalls[0].GetPayloadJson())
	assert.Empty(t, stub.current, "the attempt must be closed after Drive returns")
}

// TestA2ARejectedCallReachesNoStub checks that an L1 doctrine rejection on a real Gateway
// answers with a signed rejection and never reaches the A2A downstream.
func TestA2ARejectedCallReachesNoStub(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	stub := startStub(t)
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName:         "conformance-a2a-rejected",
		Posture:          constants.PostureDoctrine,
		A2ADownstreamURL: stub.URL(),
	})
	d := NewA2A(gw.client, gw.persona, stub)
	fx := &directFixture{f: gw.f}

	c := findCase(t, all, 14, cases.IngressA2A, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)
	driven, err := d.Drive(context.Background(), fx.attemptFor(t, c))
	require.NoError(t, err)
	require.Len(t, driven.Observations, len(c.Expect))
	for i, want := range c.Expect {
		assertObservation(t, want, driven.Observations[i])
	}
	assert.Empty(t, driven.A2ACalls, "an L1 rejection must not reach the A2A downstream")
}

// TestA2AUnattendedMutationSuspendsUnderRatify checks that an unattended A2A mutation is
// answered with a suspension that names its transaction, and that the suspended call never
// reaches the downstream.
func TestA2AUnattendedMutationSuspendsUnderRatify(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	stub := startStub(t)
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName:         "conformance-a2a-ratify",
		Posture:          constants.PostureRatify,
		A2ADownstreamURL: stub.URL(),
	})
	d := NewA2A(gw.client, gw.persona, stub)
	fx := &directFixture{f: gw.f}

	c := findCase(t, all, 22, cases.IngressA2A, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY)
	driven, err := d.Drive(context.Background(), fx.attemptFor(t, c))
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED, obs.GetKind())
	assert.NotEmpty(t, obs.GetSuspendedTxHash(), "a suspension must name the transaction the approval decides")
	assert.Empty(t, driven.A2ACalls, "a suspended call must not reach the A2A downstream")
}

// TestA2ARefusesCallWithoutSkillName checks that an a2a/call with no skill is refused at
// ingress with a typed refusal.
func TestA2ARefusesCallWithoutSkillName(t *testing.T) {
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-a2a-refusal",
		Posture:  constants.PostureDoctrine,
	})
	outcome, err := gw.client.A2ACall(context.Background(), gw.persona, mcp.A2ACallRequest{})
	require.NoError(t, err)
	obs := Observe(outcome)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, obs.GetKind())
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD, obs.GetRefusal().GetCode())
}

// TestA2AUnsupportedFaultSendsNothing checks that a fault A2A cannot express is unsupported
// before any stub attempt opens or any request is made. The unreachable address would turn
// any request into a transport error.
func TestA2AUnsupportedFaultSendsNothing(t *testing.T) {
	stub := startStub(t)
	d := NewA2A(unreachableClient(t), testIdentity().Persona, stub)

	_, err := d.Drive(context.Background(), a2aAttempt(cases.Case{ID: "row-4", Fault: cases.FaultIDMismatch}))
	require.ErrorIs(t, err, constants.ErrConformanceIngressUnsupported)
	assert.Empty(t, stub.current, "an unsupported fault must not open a stub attempt")
}

// TestA2ATransportErrorIsAnErrorAndClosesAttempt checks that a call with no HTTP answer is
// an error, not an observation, and that the stub attempt is still closed.
func TestA2ATransportErrorIsAnErrorAndClosesAttempt(t *testing.T) {
	stub := startStub(t)
	d := NewA2A(unreachableClient(t), testIdentity().Persona, stub)

	_, err := d.Drive(context.Background(), a2aAttempt(cases.Case{ID: "row-1", Fault: cases.FaultNone, Action: cases.ActionMutation}))
	require.Error(t, err)
	assert.False(t, errors.Is(err, constants.ErrConformanceIngressUnsupported))
	assert.Empty(t, stub.current, "a failed call must still close the stub attempt")
}
