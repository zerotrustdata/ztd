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
	"net/http"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// fakePoster answers every Post with one canned body or error and records the
// command each dispatch carried.
type fakePoster struct {
	body     []byte
	err      error
	paths    []string
	commands []string
}

func (p *fakePoster) Post(path string, body any) ([]byte, error) {
	p.paths = append(p.paths, path)
	req, ok := body.(operator.DispatchRequest)
	if !ok {
		return nil, fmt.Errorf("fake poster: unexpected body %T", body)
	}
	command := &operatorv1.CommandRequested{}
	if err := proto.Unmarshal(req.Payload, command); err != nil {
		return nil, fmt.Errorf("fake poster: decode command: %w", err)
	}
	p.commands = append(p.commands, command.GetCommand())
	return p.body, p.err
}

// commandResultDispatchBody is a canonical 2xx GovernedDispatchResult whose
// terminal result completed, so RunOnce reports success with a receipt.
func commandResultDispatchBody(t *testing.T) []byte {
	t.Helper()
	result, err := proto.Marshal(&operatorv1.CommandResult{
		Status:     operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ReturnCode: 0,
		Stdout:     "ok",
	})
	require.NoError(t, err)
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.GovernedDispatchResult{
		TransactionId: "tx-cli-1",
		ResultPayload: result,
		Receipt: &operatorv1.ActionReceipt{
			TransactionId: "tx-cli-1",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		},
	})
	require.NoError(t, err)
	return body
}

// statusErr is the transport-level error RunOnce receives for a non-2xx answer.
func statusErr(status int, body []byte) error {
	return &api.StatusError{StatusCode: status, Body: string(body)}
}

func newTestCLI(t *testing.T, poster operator.CommandPoster) *CLI {
	t.Helper()
	c, err := NewCLI(poster, testDispatchTargets()[cases.ExecutorOutbound], testIdentity().Persona.CLISessionID)
	require.NoError(t, err)
	return c
}

// TestCLIDriveMapsRunOnceAnswers checks that each RunOnce answer kind becomes the
// matching observation, with http_status left at 0.
func TestCLIDriveMapsRunOnceAnswers(t *testing.T) {
	tests := []struct {
		name   string
		poster func(t *testing.T) *fakePoster
		want   evalv1.ConformanceObservationKind
		assert func(t *testing.T, obs *evalv1.ConformanceObservation)
	}{
		{
			name:   "success is a receipt",
			poster: func(t *testing.T) *fakePoster { return &fakePoster{body: commandResultDispatchBody(t)} },
			want:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
			assert: func(t *testing.T, obs *evalv1.ConformanceObservation) {
				assert.Equal(t, "tx-cli-1", obs.GetReceipt().GetTransactionId())
			},
		},
		{
			name: "signed rejection",
			poster: func(t *testing.T) *fakePoster {
				return &fakePoster{err: statusErr(http.StatusForbidden, rejectionBody(t))}
			},
			want: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
			assert: func(t *testing.T, obs *evalv1.ConformanceObservation) {
				assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY, obs.GetRejection().GetReceipt().GetRejectionCode())
			},
		},
		{
			name: "typed refusal",
			poster: func(t *testing.T) *fakePoster {
				return &fakePoster{err: statusErr(http.StatusBadRequest, refusalBody(t))}
			},
			want: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
			assert: func(t *testing.T, obs *evalv1.ConformanceObservation) {
				assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST, obs.GetRefusal().GetCode())
			},
		},
		{
			name: "plain text is untyped",
			poster: func(*testing.T) *fakePoster {
				return &fakePoster{err: statusErr(http.StatusInternalServerError, []byte("plain text"))}
			},
			want: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
			assert: func(t *testing.T, obs *evalv1.ConformanceObservation) {
				assert.Contains(t, string(obs.GetUntypedBody()), "plain text")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poster := tt.poster(t)
			a := dispatchAttempt(cases.IngressCLI, cases.FaultNone, cases.ActionMutation)
			driven, err := newTestCLI(t, poster).Drive(context.Background(), a)
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)
			obs := driven.Observations[0]
			assert.Equal(t, tt.want, obs.GetKind())
			assert.Equal(t, int32(0), obs.GetHttpStatus(), "the CLI answer has no HTTP status")
			require.Len(t, poster.paths, 1, "one submission")
			tt.assert(t, obs)
		})
	}
}

// TestCLIDriveTransportFailureIsError checks that a Gateway that sends no HTTP
// answer aborts the attempt instead of becoming an UNTYPED observation.
func TestCLIDriveTransportFailureIsError(t *testing.T) {
	poster := &fakePoster{err: syscall.ECONNREFUSED}
	a := dispatchAttempt(cases.IngressCLI, cases.FaultNone, cases.ActionMutation)
	driven, err := newTestCLI(t, poster).Drive(context.Background(), a)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
	assert.Empty(t, driven.Observations)
}

// TestCLIDriveCommandPerFault checks the command each fault sends: the corpus
// command for a doctrine case, and the marker write for the mutation cases.
func TestCLIDriveCommandPerFault(t *testing.T) {
	t.Run("doctrine sends the corpus command", func(t *testing.T) {
		poster := &fakePoster{body: commandResultDispatchBody(t)}
		a := dispatchAttempt(cases.IngressCLI, cases.FaultDoctrine, cases.ActionMutation)

		_, err := newTestCLI(t, poster).Drive(context.Background(), a)
		require.NoError(t, err)
		require.Equal(t, []string{a.Case.Command}, poster.commands)
	})

	for _, fault := range []cases.Fault{cases.FaultNone, cases.FaultAttendedApprove, cases.FaultAttendedDeny} {
		t.Run("mutation "+string(fault)+" sends the marker command", func(t *testing.T) {
			poster := &fakePoster{body: commandResultDispatchBody(t)}
			a := dispatchAttempt(cases.IngressCLI, fault, cases.ActionMutation)

			_, err := newTestCLI(t, poster).Drive(context.Background(), a)
			require.NoError(t, err)
			require.Equal(t, []string{cases.MarkerCommand(a)}, poster.commands)
		})
	}
}

// TestCLIDriveRejectsUnsupportedCases checks that other faults and other
// ingresses are refused before any dispatch is posted.
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
			poster := &fakePoster{body: commandResultDispatchBody(t)}
			a := dispatchAttempt(tt.ingress, tt.fault, cases.ActionMutation)

			_, err := newTestCLI(t, poster).Drive(context.Background(), a)
			assert.True(t, errors.Is(err, constants.ErrConformanceIngressUnsupported), "got %v", err)
			assert.Empty(t, poster.paths, "no dispatch is posted for an unsupported case")
		})
	}
}

// TestNewCLIRequiresPosterTargetAndSession checks each required input.
func TestNewCLIRequiresPosterTargetAndSession(t *testing.T) {
	target := testDispatchTargets()[cases.ExecutorOutbound]
	poster := &fakePoster{}

	_, err := NewCLI(nil, target, "cli-session-1")
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "nil poster: %v", err)

	_, err = NewCLI(poster, operator.Target{OperatorID: "outbound-operator"}, "cli-session-1")
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "empty target session: %v", err)

	_, err = NewCLI(poster, target, "")
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField), "empty cli session: %v", err)
}
