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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// l2Cases returns the DIRECT catalogue cases for one posture and rows that the
// row-driven tests below need, in catalogue order.
func l2Cases(t *testing.T, posture evalv1.EvaluationGovernancePosture, rows ...int) []cases.Case {
	t.Helper()
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	want := map[int]bool{}
	for _, r := range rows {
		want[r] = true
	}
	var out []cases.Case
	for _, c := range all {
		if c.Ingress == cases.IngressDirect && c.Posture == posture && want[c.Row] {
			out = append(out, c)
		}
	}
	return out
}

// TestL2DeliberatedProofAcceptsRow1AndRejectsEachRow18Fault drives row 1 and the
// six row 18 faults under consensus. The valid proof comes from the Gateway
// deliberation endpoint. The set has three members and quorum three, so keeping
// K-1 votes is below quorum.
func TestL2DeliberatedProofAcceptsRow1AndRejectsEachRow18Fault(t *testing.T) {
	testDeliberatedL2(t, 3)
}

func TestL2SingleApplicationProofAcceptsRow1AndRejectsEachRow18Fault(t *testing.T) {
	testDeliberatedL2(t, 1)
}

func testDeliberatedL2(t *testing.T, members int) {
	t.Helper()
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName:                 "conformance-l2-consensus",
		Posture:                  constants.PostureConsensus,
		ConsensusID:              "conformance-consensus",
		ConsensusNMembers:        members,
		ConsensusNServiceMembers: members,
		ConsensusQuorum:          members,
	})
	deliberator := NewDeliberator(gw.client, gw.persona)
	direct, err := NewDirect(gw.client, gw.persona, gw.target, deliberator, BaseMutators(), L2Mutators(deliberator))
	require.NoError(t, err)
	fx := &directFixture{f: gw.f, direct: direct}

	selected := l2Cases(t, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS, 1, 18)
	require.NotEmpty(t, selected, "catalogue has no row 1 or row 18 DIRECT consensus case")
	for _, c := range selected {
		t.Run(c.ID, func(t *testing.T) {
			driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(c.Expect))
			for i, want := range c.Expect {
				got := driven.Observations[i]
				assertObservation(t, want, got)
				if want.Kind == cases.ExpectExecuted {
					assert.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, got.GetReceipt().GetL2Status(), "answer: %s", answerText(got))
				}
				if c.Row == 18 {
					t.Logf("Row 18 pin fault=%s kind=%s answer=%s", c.Fault, got.GetKind(), answerText(got))
				}
			}
		})
	}
}

// TestL2SyntheticProofExecutesRow19UnderDoctrine drives the six row 18 faults
// under doctrine, where the Gateway does not gate on L2. A synthetic proof
// stands in for the deliberated one. Each fault must execute with L2 not
// required.
func TestL2SyntheticProofExecutesRow19UnderDoctrine(t *testing.T) {
	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-l2-doctrine",
		Posture:  constants.PostureDoctrine,
	})
	synthetic, err := NewSyntheticL2(2)
	require.NoError(t, err)
	direct, err := NewDirect(gw.client, gw.persona, gw.target, nil, BaseMutators(), L2Mutators(synthetic))
	require.NoError(t, err)
	fx := &directFixture{f: gw.f, direct: direct}

	selected := l2Cases(t, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, 19)
	require.Len(t, selected, len(l2Faults6), "catalogue must hold one row 19 DIRECT doctrine case per L2 fault")
	for _, c := range selected {
		t.Run(c.ID, func(t *testing.T) {
			driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(c.Expect))
			for i, want := range c.Expect {
				got := driven.Observations[i]
				assertObservation(t, want, got)
				assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, got.GetReceipt().GetL2Status(), "answer: %s", answerText(got))
			}
		})
	}
}
