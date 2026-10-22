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
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// integrationRows are the DIRECT doctrine rows the Tier 2 test drives. Row 13
// contributes its first corpus entry only.
var integrationRows = map[int]bool{1: true, 3: true, 4: true, 5: true, 6: true, 8: true, 10: true, 11: true, 12: true, 13: true, 15: true, 17: true}

// directFixture is one live Gateway and, for DIRECT tests, a driver that submits to
// its embedded Operator.
type directFixture struct {
	f      *fixtures.GatewayFixture
	direct *Direct
}

// newDirectFixture boots a Gateway under posture and builds a DIRECT driver
// that submits as the bootstrapped CLI identity to the embedded Operator.
func newDirectFixture(t *testing.T, posture constants.Posture) *directFixture {
	t.Helper()
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-direct-" + string(posture),
		Posture:  posture,
	})
	direct, err := NewDirect(gw.client, gw.persona, gw.target, nil, BaseMutators())
	require.NoError(t, err)
	return &directFixture{f: gw.f, direct: direct}
}

// attemptFor builds a fresh attempt for one case, with targets under the
// fixture work directory and removed when the test ends. The embedded Operator
// confines file edits to its working directory, so the targets live beneath it.
func (fx *directFixture) attemptFor(t *testing.T, c cases.Case) cases.Attempt {
	t.Helper()
	attemptID, err := uuid.NewString()
	require.NoError(t, err)
	runID, err := uuid.NewString()
	require.NoError(t, err)
	results := filepath.Join(fx.f.Config.WorkDir, "test-results")
	parent := filepath.Join(results, "conformance-direct")
	dir := filepath.Join(parent, runID)
	require.NoError(t, os.MkdirAll(dir, constants.PermDirStandard))
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		// The work directory is the package directory: drop the parents once
		// empty so no run leaves directories in the source tree.
		_ = os.Remove(parent)
		_ = os.Remove(results)
	})
	marker := cases.Marker(runID, attemptID)
	// Read cases need the file to exist; the content is the marker so a read
	// receipt can be checked against it.
	readAbs := filepath.Join(dir, "read.txt")
	require.NoError(t, os.WriteFile(readAbs, []byte(marker), constants.PermFilePrivate))
	return cases.Attempt{
		RunID:     runID,
		AttemptID: attemptID,
		Case:      c,
		TargetAbs: filepath.Join(dir, "target.txt"),
		ReadAbs:   readAbs,
		Marker:    marker,
	}
}

// directCase is a DIRECT mutation case for one fault, for tests that do not
// take their case from the catalogue.
func directCase(fault cases.Fault) cases.Case {
	return cases.Case{ID: "direct-" + string(fault), Ingress: cases.IngressDirect, Fault: fault, Action: cases.ActionMutation}
}

// answerText renders the human text of an answer for assertion messages.
func answerText(got *evalv1.ConformanceObservation) string {
	switch {
	case got.GetRefusal() != nil:
		return got.GetRefusal().GetCode().String() + ": " + got.GetRefusal().GetError()
	case got.GetRejection() != nil:
		return got.GetRejection().GetReceipt().GetRejectionCode().String() + ": " + got.GetRejection().GetError()
	default:
		return got.GetKind().String()
	}
}

// TestDirectDriveMatchesCatalogueOnDoctrineRows drives the selected DIRECT rows
// against a real doctrine Gateway and asserts the observation kind and code
// that the catalogue expects.
func TestDirectDriveMatchesCatalogueOnDoctrineRows(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)

	fx := newDirectFixture(t, constants.PostureDoctrine)
	drove13 := false
	for _, c := range all {
		if c.Ingress != cases.IngressDirect || c.Posture != evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE {
			continue
		}
		if !integrationRows[c.Row] || (c.Row == 13 && drove13) {
			continue
		}
		if c.Row == 13 {
			drove13 = true
		}
		t.Run(c.ID, func(t *testing.T) {
			driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(c.Expect))
			for i, want := range c.Expect {
				assertObservation(t, want, driven.Observations[i])
			}
		})
	}
	require.True(t, drove13, "catalogue has no row-13 DIRECT doctrine case")
}

// assertObservation checks one observation against one catalogue expectation.
func assertObservation(t *testing.T, want cases.Expect, got *evalv1.ConformanceObservation) {
	t.Helper()
	switch want.Kind {
	case cases.ExpectExecuted:
		assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, got.GetKind(), "answer: %s", answerText(got))
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, got.GetReceipt().GetStatus(), "answer: %s", answerText(got))
	case cases.ExpectRejected:
		assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, got.GetKind(), "answer: %s", answerText(got))
		assert.Equal(t, want.RejectionCode, got.GetRejection().GetReceipt().GetRejectionCode(), "answer: %s", answerText(got))
	case cases.ExpectRefused:
		assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, got.GetKind(), "answer: %s", answerText(got))
		assert.Equal(t, want.RefusalCode, got.GetRefusal().GetCode(), "answer: %s", answerText(got))
	default:
		t.Fatalf("unexpected expectation kind %q", want.Kind)
	}
}

// TestDirectProbeWrongTargetByPosture records the observation for the row 9
// fault (wrong operator session) under each posture. It pins nothing: the
// tracker pins row 9 from this output.
func TestDirectProbeWrongTargetByPosture(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureDoctrine, constants.PostureConsensus, constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			fx := newDirectFixture(t, posture)
			c := cases.Case{ID: "row-9-probe-" + string(posture), Row: 9, Ingress: cases.IngressDirect, Fault: cases.FaultWrongTarget, Action: cases.ActionMutation}
			driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)
			obs := driven.Observations[0]
			t.Logf("Row 9 probe posture=%s kind=%s http=%d refusal=%s rejection=%s",
				posture, obs.GetKind(), obs.GetHttpStatus(), obs.GetRefusal().GetCode(), obs.GetRejection().GetReceipt().GetRejectionCode())
			assert.NotEqual(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED, obs.GetKind())
		})
	}
}

// TestDirectDriveReturnsObservationKindPerAnswer checks the kind and HTTP status
// the real Gateway answers with for an accepted envelope, a tampered envelope
// and a malformed body. A 500 answer is classified in the client package.
func TestDirectDriveReturnsObservationKindPerAnswer(t *testing.T) {
	fx := newDirectFixture(t, constants.PostureDoctrine)
	tests := []struct {
		name   string
		fault  cases.Fault
		want   evalv1.ConformanceObservationKind
		status int32
	}{
		{"accepted receipt", cases.FaultNone, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, http.StatusOK},
		{"governance rejection", cases.FaultPayloadTampered, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, http.StatusForbidden},
		{"ingress refusal", cases.FaultMalformedJSON, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, directCase(tt.fault)))
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)
			obs := driven.Observations[0]
			assert.Equal(t, tt.want, obs.GetKind(), "answer: %s", answerText(obs))
			assert.Equal(t, tt.status, obs.GetHttpStatus())
		})
	}
}

func TestDirectDriveMissingMutatorIsUnsupported(t *testing.T) {
	fx := newDirectFixture(t, constants.PostureDoctrine)
	d, err := NewDirect(fx.direct.client, fx.direct.id.Persona, fx.direct.id.Target, nil)
	require.NoError(t, err)

	driven, err := d.Drive(context.Background(), fx.attemptFor(t, directCase(cases.FaultExpired)))
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrConformanceIngressUnsupported))
	assert.Empty(t, driven.Observations, "an unsupported fault must not submit an envelope")
}

// TestDirectDriveUnreachableGatewayIsErrorNotObservation points the driver at a
// loopback port with nothing listening. The state root fetch fails before any
// envelope is built, so the driver returns an error and no observation.
func TestDirectDriveUnreachableGatewayIsErrorNotObservation(t *testing.T) {
	d, err := NewDirect(unreachableClient(t), testIdentity().Persona, testIdentity().Target, nil, BaseMutators())
	require.NoError(t, err)

	driven, err := d.Drive(context.Background(), testAttempt(cases.FaultNone, cases.ActionMutation))
	require.Error(t, err)
	assert.Empty(t, driven.Observations)
}

// TestDirectDriveL2Consensus covers the L2 source under consensus, where the
// deliberation endpoint is real. The endpoint refuses an envelope that a body
// fault has already broken, so a body fault that reaches the Gateway shows the
// proof was requested over the valid baseline, before the fault.
func TestDirectDriveL2Consensus(t *testing.T) {
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName:                 "conformance-direct-l2",
		Posture:                  constants.PostureConsensus,
		ConsensusID:              "conformance-direct-l2",
		ConsensusNMembers:        1,
		ConsensusNServiceMembers: 1,
		ConsensusQuorum:          1,
	})
	deliberator := NewDeliberator(gw.client, gw.persona)

	t.Run("proof is requested over the valid baseline", func(t *testing.T) {
		direct, err := NewDirect(gw.client, gw.persona, gw.target, deliberator, BaseMutators())
		require.NoError(t, err)
		fx := &directFixture{f: gw.f, direct: direct}
		for _, fault := range []cases.Fault{cases.FaultPayloadTampered, cases.FaultMissingField} {
			t.Run(string(fault), func(t *testing.T) {
				driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, directCase(fault)))
				require.NoError(t, err, "a proof requested after the fault would be refused by the deliberation endpoint")
				require.Len(t, driven.Observations, 1)
				assert.NotEqual(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED, driven.Observations[0].GetKind(), "answer: %s", answerText(driven.Observations[0]))
			})
		}
	})

	t.Run("mutator that sets L2 is not given the deliberated proof", func(t *testing.T) {
		selfSigned := MutatorSet{cases.FaultNone: {SetsL2: true, Apply: noop.Apply}}
		direct, err := NewDirect(gw.client, gw.persona, gw.target, deliberator, selfSigned)
		require.NoError(t, err)
		fx := &directFixture{f: gw.f, direct: direct}

		driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, directCase(cases.FaultNone)))
		require.NoError(t, err)
		require.Len(t, driven.Observations, 1)
		obs := driven.Observations[0]
		assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, obs.GetKind(), "answer: %s", answerText(obs))
		assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_PROOF_MISSING, obs.GetRejection().GetReceipt().GetRejectionCode(), "answer: %s", answerText(obs))
	})
}
