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
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newTestDispatch(t *testing.T, c *client.Client) *Dispatch {
	t.Helper()
	d, err := NewDispatch(c, testIdentity().Persona, testDispatchTargets())
	require.NoError(t, err)
	return d
}

// TestDispatchDriveMatchesCatalogueOnEmbeddedRows drives the embedded DISPATCH
// rows against a real Gateway and asserts the observation the catalogue expects.
// Doctrine covers file edit, doctrine refusal, unknown session and the row 17
// body faults. Ratify covers the FS_READ baseline (row 2), which is ratify and
// notary only.
func TestDispatchDriveMatchesCatalogueOnEmbeddedRows(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	rows := map[int]bool{1: true, 2: true, 14: true, 16: true, 17: true}

	postures := []struct {
		posture constants.Posture
		eval    evalv1.EvaluationGovernancePosture
	}{
		{constants.PostureDoctrine, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE},
		{constants.PostureRatify, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY},
	}
	for _, p := range postures {
		t.Run(string(p.posture), func(t *testing.T) {
			fx := newDirectFixture(t, p.posture)
			dispatch, err := NewDispatch(fx.direct.client, fx.direct.id.Persona, map[cases.Executor]operator.Target{
				cases.ExecutorEmbedded: fx.direct.id.Target,
			})
			require.NoError(t, err)

			ran := 0
			for _, c := range all {
				if c.Ingress != cases.IngressDispatchEmbedded || c.Posture != p.eval || !rows[c.Row] {
					continue
				}
				ran++
				t.Run(c.ID, func(t *testing.T) {
					driven, err := dispatch.Drive(context.Background(), fx.attemptFor(t, c))
					require.NoError(t, err)
					require.Len(t, driven.Observations, len(c.Expect))
					for i, want := range c.Expect {
						assertObservation(t, want, driven.Observations[i])
					}
				})
			}
			require.NotZero(t, ran, "catalogue has no embedded DISPATCH case under %s", p.posture)
		})
	}
}

// TestDispatchDriveRejectsUnsupportedCases checks that a fault or ingress the
// dispatch driver cannot express is refused before any request is sent. The
// client points at a closed port, so a request would fail with a transport
// error rather than this refusal.
func TestDispatchDriveRejectsUnsupportedCases(t *testing.T) {
	tests := []struct {
		name    string
		ingress cases.Ingress
		fault   cases.Fault
	}{
		{name: "tampered payload is not a dispatch fault", ingress: cases.IngressDispatchEmbedded, fault: cases.FaultPayloadTampered},
		{name: "direct ingress is not dispatched here", ingress: cases.IngressDirect, fault: cases.FaultNone},
		{name: "cli ingress is not dispatched here", ingress: cases.IngressCLI, fault: cases.FaultNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := dispatchAttempt(tt.ingress, tt.fault, cases.ActionMutation)

			_, err := newTestDispatch(t, unreachableClient(t)).Drive(context.Background(), a)
			require.Error(t, err)
			assert.True(t, errors.Is(err, constants.ErrConformanceIngressUnsupported), "got %v", err)
		})
	}
}

func TestNewDispatchAcceptsOneExecutorAndRefusesMissingTargets(t *testing.T) {
	c := unreachableClient(t)
	persona := testIdentity().Persona

	missingOutbound := map[cases.Executor]operator.Target{
		cases.ExecutorEmbedded: testDispatchTargets()[cases.ExecutorEmbedded],
	}
	dispatch, err := NewDispatch(c, persona, missingOutbound)
	require.NoError(t, err)
	_, err = dispatch.Drive(context.Background(), dispatchAttempt(cases.IngressDispatchOutbound, cases.FaultNone, cases.ActionMutation))
	require.ErrorIs(t, err, constants.ErrConformanceIngressUnsupported)

	missingEmbedded := map[cases.Executor]operator.Target{
		cases.ExecutorOutbound: testDispatchTargets()[cases.ExecutorOutbound],
	}
	_, err = NewDispatch(c, persona, missingEmbedded)
	require.NoError(t, err)
	_, err = NewDispatch(c, persona, nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)

	_, err = NewDispatch(nil, persona, testDispatchTargets())
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "nil client: %v", err)
}

// TestDispatchDriveTransportFailureIsError checks that an unreachable Gateway
// returns an error rather than an observation.
func TestDispatchDriveTransportFailureIsError(t *testing.T) {
	a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultNone, cases.ActionMutation)

	driven, err := newTestDispatch(t, unreachableClient(t)).Drive(context.Background(), a)
	require.Error(t, err)
	assert.Empty(t, driven.Observations)
}
