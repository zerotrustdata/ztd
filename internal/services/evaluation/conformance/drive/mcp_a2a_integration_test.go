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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestMCPA2A drives row 1 (a mutation) and row 14 (the first doctrine corpus entry)
// on MCP and on A2A against a doctrine Gateway. The mutation must execute with the
// marker written: on MCP the target file holds it, on A2A exactly one stub call
// carries it. The doctrine case must be rejected by L1 before it runs, with no stub call.
func TestMCPA2A(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)

	// Registered before the fixture so the stub closes after the Gateway stops.
	stub, err := StartA2AStub("127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, stub.Close(ctx))
	})

	gw := bootLiveGateway(t, fixtures.GatewayFixtureOptions{
		TestName:         "conformance-mcp-a2a",
		Posture:          constants.PostureDoctrine,
		A2ADownstreamURL: stub.URL(),
	})
	mcpDriver, err := NewMCP(context.Background(), gw.client, gw.persona)
	require.NoError(t, err)
	drivers := map[cases.Ingress]cases.Driver{
		cases.IngressMCP: mcpDriver,
		cases.IngressA2A: NewA2A(gw.client, gw.persona, stub),
	}

	// attemptFor only reads the fixture's work directory, so a bare fixture is enough.
	fx := &directFixture{f: gw.f}
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE

	// First row-1 and first row-14 case for each ingress under doctrine.
	mutations := map[cases.Ingress]*cases.Case{}
	doctrineCases := map[cases.Ingress]*cases.Case{}
	for i := range all {
		c := all[i]
		if c.Posture != doctrine || (c.Ingress != cases.IngressMCP && c.Ingress != cases.IngressA2A) {
			continue
		}
		switch {
		case c.Row == 1 && mutations[c.Ingress] == nil:
			mutations[c.Ingress] = &all[i]
		case c.Row == 14 && doctrineCases[c.Ingress] == nil:
			doctrineCases[c.Ingress] = &all[i]
		}
	}

	for _, ingress := range []cases.Ingress{cases.IngressMCP, cases.IngressA2A} {
		mutation := mutations[ingress]
		require.NotNil(t, mutation, "catalogue has no row-1 %s doctrine case", ingress)
		doctrineCase := doctrineCases[ingress]
		require.NotNil(t, doctrineCase, "catalogue has no row-14 %s doctrine case", ingress)
		driver := drivers[ingress]

		t.Run(mutation.ID, func(t *testing.T) {
			a := fx.attemptFor(t, *mutation)
			driven, err := driver.Drive(context.Background(), a)
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(mutation.Expect))
			for i, want := range mutation.Expect {
				assertObservation(t, want, driven.Observations[i])
			}
			if ingress == cases.IngressMCP {
				written, err := os.ReadFile(a.TargetAbs)
				require.NoError(t, err)
				require.Equal(t, a.Marker, string(written), "the executed mutation must write exactly the marker")
				return
			}
			require.Len(t, driven.A2ACalls, 1, "exactly one stub call must carry the marker")
			assert.Equal(t, ConformanceA2ASkill, driven.A2ACalls[0].GetSkillName())
			want, err := json.Marshal(cases.MarkerSkillPayload{Marker: a.Marker})
			require.NoError(t, err)
			assert.JSONEq(t, string(want), driven.A2ACalls[0].GetPayloadJson())
		})

		t.Run(doctrineCase.ID, func(t *testing.T) {
			driven, err := driver.Drive(context.Background(), fx.attemptFor(t, *doctrineCase))
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(doctrineCase.Expect))
			for i, want := range doctrineCase.Expect {
				assertObservation(t, want, driven.Observations[i])
			}
			assert.Empty(t, driven.A2ACalls, "an L1 rejection must not reach the A2A downstream")
		})
	}
}
