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
	"google.golang.org/protobuf/proto"

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

// fakeDispatch answers every POST to the governed dispatch route with one canned
// status and body, and records each request body it receives.
type fakeDispatch struct {
	mu     sync.Mutex
	status int
	body   []byte
	bodies [][]byte
}

func (f *fakeDispatch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != constants.APIPaths.OperatorsCommands {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.bodies = append(f.bodies, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	_, _ = w.Write(f.body)
}

// newFakeDispatch starts a fake dispatch Gateway and a client pointed at it.
func newFakeDispatch(t *testing.T, status int, body []byte) (*fakeDispatch, *client.Client) {
	t.Helper()
	fake := &fakeDispatch{status: status, body: body}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	return fake, c
}

// recorded returns the i-th request body the fake received.
func (f *fakeDispatch) recorded(t *testing.T, i int) []byte {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Less(t, i, len(f.bodies))
	return f.bodies[i]
}

func (f *fakeDispatch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// dispatchResultBody is a canonical 2xx GovernedDispatchResult carrying a receipt.
func dispatchResultBody(t *testing.T) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.GovernedDispatchResult{
		TransactionId: "tx-1",
		Receipt: &operatorv1.ActionReceipt{
			TransactionId: "tx-1",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		},
	})
	require.NoError(t, err)
	return body
}

// decodeDispatchRequest decodes a well-formed dispatch request body.
func decodeDispatchRequest(t *testing.T, body []byte) models.OperatorCommandRequest {
	t.Helper()
	var req models.OperatorCommandRequest
	require.NoError(t, json.Unmarshal(body, &req))
	return req
}

func newTestDispatch(t *testing.T, c *client.Client) *Dispatch {
	t.Helper()
	d, err := NewDispatch(c, testIdentity().Persona, testDispatchTargets())
	require.NoError(t, err)
	return d
}

// TestDispatchDriveSendsRequestPerFault decodes the request each fault sends and
// asserts the session id, event type and payload.
func TestDispatchDriveSendsRequestPerFault(t *testing.T) {
	t.Run("file edit baseline to embedded session", func(t *testing.T) {
		fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
		a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultNone, cases.ActionMutation)

		driven, err := newTestDispatch(t, c).Drive(context.Background(), a)
		require.NoError(t, err)
		require.Len(t, driven.Observations, 1)
		require.Equal(t, 1, fake.count())

		req := decodeDispatchRequest(t, fake.recorded(t, 0))
		assert.Equal(t, "embedded-session", req.TargetOperatorSessionID)
		assert.Equal(t, string(constants.EventOperatorFileEditRequested), req.EventType)
		assert.Equal(t, "cli-session-1", req.CLISessionID)
		edit := &operatorv1.FileEditRequested{}
		require.NoError(t, proto.Unmarshal(req.Payload, edit))
		assert.Equal(t, a.TargetAbs, edit.GetFilePath())
		assert.Equal(t, a.Marker, edit.GetContent())
	})

	for _, fault := range []cases.Fault{cases.FaultAttendedApprove, cases.FaultAttendedDeny} {
		t.Run(string(fault)+" sends the baseline file edit", func(t *testing.T) {
			fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
			a := dispatchAttempt(cases.IngressDispatchOutbound, fault, cases.ActionMutation)

			_, err := newTestDispatch(t, c).Drive(context.Background(), a)
			require.NoError(t, err)

			req := decodeDispatchRequest(t, fake.recorded(t, 0))
			assert.Equal(t, "outbound-session", req.TargetOperatorSessionID)
			assert.Equal(t, string(constants.EventOperatorFileEditRequested), req.EventType)
			edit := &operatorv1.FileEditRequested{}
			require.NoError(t, proto.Unmarshal(req.Payload, edit))
			assert.Equal(t, a.TargetAbs, edit.GetFilePath())
			assert.Equal(t, a.Marker, edit.GetContent())
		})
	}

	t.Run("file edit baseline to outbound session", func(t *testing.T) {
		fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
		a := dispatchAttempt(cases.IngressDispatchOutbound, cases.FaultNone, cases.ActionMutation)

		_, err := newTestDispatch(t, c).Drive(context.Background(), a)
		require.NoError(t, err)

		req := decodeDispatchRequest(t, fake.recorded(t, 0))
		assert.Equal(t, "outbound-session", req.TargetOperatorSessionID)
	})

	t.Run("read baseline sends fs read payload", func(t *testing.T) {
		fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
		a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultReadBaseline, cases.ActionRead)

		_, err := newTestDispatch(t, c).Drive(context.Background(), a)
		require.NoError(t, err)

		req := decodeDispatchRequest(t, fake.recorded(t, 0))
		assert.Equal(t, string(constants.EventOperatorFilesystemReadRequested), req.EventType)
		read := &operatorv1.FsReadRequested{}
		require.NoError(t, proto.Unmarshal(req.Payload, read))
		assert.Equal(t, a.ReadAbs, read.GetPath())
	})

	t.Run("doctrine sends the corpus command", func(t *testing.T) {
		fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
		a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultDoctrine, cases.ActionMutation)

		_, err := newTestDispatch(t, c).Drive(context.Background(), a)
		require.NoError(t, err)

		req := decodeDispatchRequest(t, fake.recorded(t, 0))
		assert.Equal(t, string(constants.EventOperatorCommandRequested), req.EventType)
		command := &operatorv1.CommandRequested{}
		require.NoError(t, proto.Unmarshal(req.Payload, command))
		assert.Equal(t, a.Case.Command, command.GetCommand())
	})

	t.Run("unknown session replaces the target with a fresh id", func(t *testing.T) {
		fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
		a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultUnknownSession, cases.ActionMutation)

		_, err := newTestDispatch(t, c).Drive(context.Background(), a)
		require.NoError(t, err)

		req := decodeDispatchRequest(t, fake.recorded(t, 0))
		assert.NotEmpty(t, req.TargetOperatorSessionID)
		assert.NotEqual(t, "embedded-session", req.TargetOperatorSessionID)
		assert.Equal(t, string(constants.EventOperatorFileEditRequested), req.EventType)
	})

	t.Run("unknown event type keeps the payload and replaces the event", func(t *testing.T) {
		fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
		a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultUnknownEventType, cases.ActionMutation)

		_, err := newTestDispatch(t, c).Drive(context.Background(), a)
		require.NoError(t, err)

		req := decodeDispatchRequest(t, fake.recorded(t, 0))
		assert.Equal(t, unknownEventType, req.EventType)
		assert.NotEmpty(t, req.Payload)
	})

	t.Run("missing field sends an empty payload", func(t *testing.T) {
		fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
		a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultMissingField, cases.ActionMutation)

		_, err := newTestDispatch(t, c).Drive(context.Background(), a)
		require.NoError(t, err)

		req := decodeDispatchRequest(t, fake.recorded(t, 0))
		assert.Empty(t, req.Payload)
		assert.Equal(t, "embedded-session", req.TargetOperatorSessionID)
		assert.Equal(t, string(constants.EventOperatorFileEditRequested), req.EventType)
	})

	t.Run("malformed json sends the truncated body", func(t *testing.T) {
		fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
		a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultMalformedJSON, cases.ActionMutation)

		_, err := newTestDispatch(t, c).Drive(context.Background(), a)
		require.NoError(t, err)

		assert.Equal(t, []byte(malformedDispatchBody), fake.recorded(t, 0))
	})
}

// TestDispatchDriveMapsOutcomeKinds checks that each typed answer becomes the
// matching observation kind, and that a non-typed body becomes UNTYPED.
func TestDispatchDriveMapsOutcomeKinds(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   func(t *testing.T) []byte
		want   evalv1.ConformanceObservationKind
	}{
		{
			name:   "receipt",
			status: http.StatusOK,
			body:   dispatchResultBody,
			want:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		},
		{
			name:   "rejection",
			status: http.StatusForbidden,
			body:   rejectionBody,
			want:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		},
		{
			name:   "refusal",
			status: http.StatusBadRequest,
			body:   refusalBody,
			want:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
		{
			name:   "plain text is untyped",
			status: http.StatusInternalServerError,
			body:   func(*testing.T) []byte { return []byte("internal failure") },
			want:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, c := newFakeDispatch(t, tt.status, tt.body(t))
			a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultNone, cases.ActionMutation)

			driven, err := newTestDispatch(t, c).Drive(context.Background(), a)
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)
			assert.Equal(t, tt.want, driven.Observations[0].GetKind())
			assert.Equal(t, 1, fake.count(), "one submission, one observation")
		})
	}
}

// TestDispatchDriveRejectsUnsupportedCases checks that a fault or ingress the
// dispatch driver cannot express is refused before any request is sent.
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
			fake, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
			a := dispatchAttempt(tt.ingress, tt.fault, cases.ActionMutation)

			_, err := newTestDispatch(t, c).Drive(context.Background(), a)
			require.Error(t, err)
			assert.True(t, errors.Is(err, constants.ErrConformanceIngressUnsupported), "got %v", err)
			assert.Equal(t, 0, fake.count(), "no request is sent for an unsupported case")
		})
	}
}

func TestNewDispatchAcceptsOneExecutorAndRefusesMissingTargets(t *testing.T) {
	_, c := newFakeDispatch(t, http.StatusOK, dispatchResultBody(t))
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

// TestDispatchDriveTransportFailureIsError checks that a closed Gateway returns an
// error rather than an observation.
func TestDispatchDriveTransportFailureIsError(t *testing.T) {
	server := httptest.NewServer(&fakeDispatch{status: http.StatusOK, body: dispatchResultBody(t)})
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	server.Close()

	a := dispatchAttempt(cases.IngressDispatchEmbedded, cases.FaultNone, cases.ActionMutation)
	driven, err := newTestDispatch(t, c).Drive(context.Background(), a)
	require.Error(t, err)
	assert.Empty(t, driven.Observations)
}
