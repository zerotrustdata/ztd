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
	"os"
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

// mcpAttempt builds an attempt for one fault with fixed paths and marker.
func mcpAttempt(c cases.Case) cases.Attempt {
	return cases.Attempt{
		RunID:     "run-1",
		AttemptID: "attempt-1",
		Case:      c,
		TargetAbs: "/work/target.txt",
		ReadAbs:   "/work/read.txt",
		Marker:    "marker-1",
	}
}

// TestMCPNewDiscoversNativeToolsOnTheGateway checks that discovery accepts the
// tools/list of a real Gateway, which serves both native tools.
func TestMCPNewDiscoversNativeToolsOnTheGateway(t *testing.T) {
	gw := bootIngressGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-mcp-discovery",
		Posture:  constants.PostureDoctrine,
	})
	_, err := NewMCP(context.Background(), gw.client, gw.persona)
	require.NoError(t, err)
}

// TestMCPNewUnreachableGatewayIsAnErrorNotUnsupported checks that a failed tools/list
// is a transport error, not an unsupported ingress.
func TestMCPNewUnreachableGatewayIsAnErrorNotUnsupported(t *testing.T) {
	_, err := NewMCP(context.Background(), unreachableClient(t), testIdentity().Persona)
	require.Error(t, err)
	assert.False(t, errors.Is(err, constants.ErrConformanceIngressUnsupported))
}

// TestMCPCallArgumentsPerFault pins the tool and the exact arguments each fault sends.
// The request is built without a Gateway; the real-Gateway tests in this file check
// what executes.
func TestMCPCallArgumentsPerFault(t *testing.T) {
	tests := []struct {
		name     string
		c        cases.Case
		wantTool string
		wantArgs string
	}{
		{
			name:     "none sends the marker write as argv",
			c:        cases.Case{ID: "row-1", Fault: cases.FaultNone, Action: cases.ActionMutation},
			wantTool: "run_shell_command",
			wantArgs: `{"command":"sh","args":["-c","printf '%s' \"$1\" > \"$2\"","sh","marker-1","/work/target.txt"]}`,
		},
		{
			name:     "unattended mutation sends the same marker write",
			c:        cases.Case{ID: "row-23", Fault: cases.FaultUnattendedMutation, Action: cases.ActionMutation},
			wantTool: "run_shell_command",
			wantArgs: `{"command":"sh","args":["-c","printf '%s' \"$1\" > \"$2\"","sh","marker-1","/work/target.txt"]}`,
		},
		{
			name:     "attended approve sends the same marker write",
			c:        cases.Case{ID: "row-24", Fault: cases.FaultAttendedApprove, Action: cases.ActionMutation},
			wantTool: "run_shell_command",
			wantArgs: `{"command":"sh","args":["-c","printf '%s' \"$1\" > \"$2\"","sh","marker-1","/work/target.txt"]}`,
		},
		{
			name:     "read baseline sends the read path",
			c:        cases.Case{ID: "row-2", Fault: cases.FaultReadBaseline, Action: cases.ActionRead},
			wantTool: "read_file",
			wantArgs: `{"path":"/work/read.txt"}`,
		},
		{
			name:     "doctrine sends the corpus command as the command",
			c:        cases.Case{ID: "row-14", Fault: cases.FaultDoctrine, Action: cases.ActionMutation, Command: "rm -rf /"},
			wantTool: "run_shell_command",
			wantArgs: `{"command":"rm -rf /"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call, err := mcpCall(mcpAttempt(tt.c))
			require.NoError(t, err)
			assert.Equal(t, tt.wantTool, call.Name)
			assert.JSONEq(t, tt.wantArgs, string(call.Arguments))
		})
	}
}

// TestMCPDriveUnsupportedFaultSendsNothing checks that a fault MCP cannot express is
// unsupported before any request is made. A request would reach the unreachable address
// and fail at the transport, so the unsupported answer proves none was sent.
func TestMCPDriveUnsupportedFaultSendsNothing(t *testing.T) {
	m := &MCP{client: unreachableClient(t), persona: testIdentity().Persona}
	_, err := m.Drive(context.Background(), mcpAttempt(cases.Case{ID: "row-4", Fault: cases.FaultIDMismatch}))
	require.ErrorIs(t, err, constants.ErrConformanceIngressUnsupported)
}

// TestMCPDriveTransportErrorIsAnError checks that a call with no HTTP answer is an error,
// not an observation.
func TestMCPDriveTransportErrorIsAnError(t *testing.T) {
	m := &MCP{client: unreachableClient(t), persona: testIdentity().Persona}
	_, err := m.Drive(context.Background(), mcpAttempt(cases.Case{ID: "row-1", Fault: cases.FaultNone}))
	require.Error(t, err, "a transport failure has no HTTP answer and must be an error, not an observation")
	assert.False(t, errors.Is(err, constants.ErrConformanceIngressUnsupported))
}

// TestMCPReadBaselineExecutesUnderDoctrine checks that the read baseline runs on MCP
// under doctrine: the Gateway executes read_file on the attempt's read path.
func TestMCPReadBaselineExecutesUnderDoctrine(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	gw := bootIngressGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-mcp-read",
		Posture:  constants.PostureDoctrine,
	})
	m, err := NewMCP(context.Background(), gw.client, gw.persona)
	require.NoError(t, err)
	fx := &directFixture{f: gw.f, workDir: gw.f.Config.WorkDir}

	c := findCase(t, all, 2, cases.IngressMCP, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)
	a := fx.attemptFor(t, c)
	require.NoError(t, os.WriteFile(a.ReadAbs, []byte(a.Marker), 0o600))

	driven, err := m.Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, len(c.Expect))
	for i, want := range c.Expect {
		assertObservation(t, want, driven.Observations[i])
	}
}

// TestMCPUnattendedMutationSuspendsUnderRatify checks that an unattended mutation on MCP
// is answered with a suspension that names its transaction, and that it writes nothing.
func TestMCPUnattendedMutationSuspendsUnderRatify(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	gw := bootIngressGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-mcp-ratify",
		Posture:  constants.PostureRatify,
	})
	m, err := NewMCP(context.Background(), gw.client, gw.persona)
	require.NoError(t, err)
	fx := &directFixture{f: gw.f, workDir: gw.f.Config.WorkDir}

	c := findCase(t, all, 22, cases.IngressMCP, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY)
	a := fx.attemptFor(t, c)
	driven, err := m.Drive(context.Background(), a)
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED, obs.GetKind())
	assert.NotEmpty(t, obs.GetSuspendedTxHash(), "a suspension must name the transaction the approval decides")

	_, statErr := os.Stat(a.TargetAbs)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a suspended mutation must not write the target")
}

// TestMCPRefusesCallWithoutToolName checks that a tools/call with no tool name is refused
// at ingress with a typed refusal, not answered as an untyped error.
func TestMCPRefusesCallWithoutToolName(t *testing.T) {
	gw := bootIngressGateway(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-mcp-refusal",
		Posture:  constants.PostureDoctrine,
	})
	outcome, err := gw.client.MCPToolsCall(context.Background(), gw.persona, mcp.CallToolRequest{})
	require.NoError(t, err)
	obs := Observe(outcome)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, obs.GetKind())
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD, obs.GetRefusal().GetCode())
}
