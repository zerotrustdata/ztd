// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// errNoAnswer is the transport error requestSpy returns. The spy never answers for a Gateway.
var errNoAnswer = errors.New("request spy: no answer")

// requestSpy records each dispatch the CLI driver posts and sends no Gateway answer.
// The request checks in this file read only what was posted, so the spy stands in
// for the transport and never speaks for the Gateway. Real answers are covered by
// cli_integration_test.go against a live Gateway.
type requestSpy struct {
	paths    []string
	commands []string
}

func (s *requestSpy) Post(path string, body any) ([]byte, error) {
	s.paths = append(s.paths, path)
	req, ok := body.(models.OperatorCommandRequest)
	if !ok {
		return nil, fmt.Errorf("request spy: unexpected body %T", body)
	}
	command := &operatorv1.CommandRequested{}
	if err := proto.Unmarshal(req.Payload, command); err != nil {
		return nil, fmt.Errorf("request spy: decode command: %w", err)
	}
	s.commands = append(s.commands, command.GetCommand())
	return nil, errNoAnswer
}

func newTestCLI(t *testing.T, poster operator.CommandPoster) *CLI {
	t.Helper()
	c, err := NewCLI(poster, testDispatchTargets()[cases.ExecutorOutbound], testIdentity().Persona.CLISessionID)
	require.NoError(t, err)
	return c
}

// TestCLIObservationMapsEachRunResultKind checks that each RunOnce result kind becomes
// the matching observation, with http_status left at 0 and an untyped body kept as
// text. RunOnce's own decoding is covered in internal/cli/operator; this checks the
// CLI's mapping of those results to observation kinds.
func TestCLIObservationMapsEachRunResultKind(t *testing.T) {
	eventReceipt := &operatorv1.EventReceipt{TransactionId: "tx-1"}
	rejection := &operatorv1.GovernanceRejection{EventReceipt: &operatorv1.EventReceipt{TransactionId: "tx-2"}}
	refusal := &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST}
	tests := []struct {
		name             string
		result           operator.RunResult
		want             evalv1.ConformanceObservationKind
		wantEventReceipt *operatorv1.EventReceipt
		wantRejected     *operatorv1.GovernanceRejection
		wantRefusal      *operatorv1.IngressRefusal
		wantBody         string
	}{
		{name: "event_receipt", result: operator.RunResult{EventReceipt: eventReceipt}, want: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_EVENT_RECEIPT, wantEventReceipt: eventReceipt},
		{name: "rejection", result: operator.RunResult{Rejection: rejection}, want: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, wantRejected: rejection},
		{name: "refusal", result: operator.RunResult{Refusal: refusal}, want: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, wantRefusal: refusal},
		{name: "no typed field is untyped", result: operator.RunResult{Error: "status 502: <html>bad gateway</html>"}, want: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED, wantBody: "status 502: <html>bad gateway</html>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := cliObservation(tt.result)
			assert.Equal(t, tt.want, obs.GetKind())
			assert.Equal(t, int32(0), obs.GetHttpStatus(), "the CLI answer has no HTTP status")
			assert.Same(t, tt.wantEventReceipt, obs.GetEventReceipt())
			assert.Same(t, tt.wantRejected, obs.GetRejection())
			assert.Same(t, tt.wantRefusal, obs.GetRefusal())
			assert.Equal(t, tt.wantBody, string(obs.GetUntypedBody()))
		})
	}
}

// TestCLIDriveCommandPerFault checks the command each fault sends: the corpus command
// for a doctrine case, and the marker write for the mutation cases. The spy sends no
// answer, so Drive returns the transport error after the request is recorded.
func TestCLIDriveCommandPerFault(t *testing.T) {
	t.Run("doctrine sends the corpus command", func(t *testing.T) {
		spy := &requestSpy{}
		a := dispatchAttempt(cases.IngressCLI, cases.FaultDoctrine)

		_, err := newTestCLI(t, spy).Drive(context.Background(), a)
		require.ErrorIs(t, err, errNoAnswer)
		require.Equal(t, []string{a.Case.Command}, spy.commands)
	})

	for _, fault := range []cases.Fault{cases.FaultNone, cases.FaultAttendedApprove, cases.FaultAttendedDeny} {
		t.Run("mutation "+string(fault)+" sends the marker command", func(t *testing.T) {
			spy := &requestSpy{}
			a := dispatchAttempt(cases.IngressCLI, fault)

			_, err := newTestCLI(t, spy).Drive(context.Background(), a)
			require.ErrorIs(t, err, errNoAnswer)
			require.Equal(t, []string{cases.MarkerCommand(a)}, spy.commands)
		})
	}
}

// TestCLIDriveRejectsUnsupportedCases checks that other faults and other ingresses are
// refused before any dispatch is posted.
func TestCLIDriveRejectsUnsupportedCases(t *testing.T) {
	tests := []struct {
		name    string
		ingress cases.Ingress
		fault   cases.Fault
	}{
		{name: "fault the CLI cannot express", ingress: cases.IngressCLI, fault: cases.FaultMalformedJSON},
		{name: "dispatch ingress is not the CLI", ingress: cases.IngressDispatchOutbound, fault: cases.FaultNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &requestSpy{}
			a := dispatchAttempt(tt.ingress, tt.fault)

			_, err := newTestCLI(t, spy).Drive(context.Background(), a)
			assert.True(t, errors.Is(err, constants.ErrConformanceIngressUnsupported), "got %v", err)
			assert.Empty(t, spy.paths, "no dispatch is posted for an unsupported case")
		})
	}
}

// TestNewCLIRequiresPosterTargetAndSession checks each required input.
func TestNewCLIRequiresPosterTargetAndSession(t *testing.T) {
	target := testDispatchTargets()[cases.ExecutorOutbound]
	spy := &requestSpy{}

	_, err := NewCLI(nil, target, "cli-session-1")
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "nil poster: %v", err)

	_, err = NewCLI(spy, operator.Target{OperatorID: "outbound-operator"}, "cli-session-1")
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "empty target session: %v", err)

	_, err = NewCLI(spy, target, "")
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "empty cli session: %v", err)
}
