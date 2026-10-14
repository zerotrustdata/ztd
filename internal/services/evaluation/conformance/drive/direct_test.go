// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.
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
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// fakeAnswer is one canned answer to a direct envelope POST. A hijacked answer
// closes the connection without writing a response.
type fakeAnswer struct {
	status   int
	body     []byte
	hijacked bool
}

// fakeGateway serves the state root and answers envelope POSTs in order, repeating
// the last answer. It records every envelope body it receives.
type fakeGateway struct {
	mu      sync.Mutex
	answers []fakeAnswer
	state   int
	bodies  [][]byte
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.State:
		if g.state != http.StatusOK {
			w.WriteHeader(g.state)
			_, _ = w.Write([]byte("state unavailable"))
			return
		}
		_ = json.NewEncoder(w).Encode(models.StateResponse{StateMerkleRoot: "root-1"})
	case r.Method == http.MethodPost && r.URL.Path == constants.APIPaths.GovernanceEnvelopes:
		body, _ := io.ReadAll(r.Body)
		idx := len(g.bodies)
		g.bodies = append(g.bodies, body)
		answer := g.answers[len(g.answers)-1]
		if idx < len(g.answers) {
			answer = g.answers[idx]
		}
		if answer.hijacked {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(answer.status)
		_, _ = w.Write(answer.body)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// newFakeDirect starts a fake Gateway and a Direct driver pointed at it.
func newFakeDirect(t *testing.T, g *fakeGateway, l2 L2Source, sets ...MutatorSet) *Direct {
	t.Helper()
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	d, err := NewDirect(c, testIdentity().Persona, testIdentity().Target, l2, sets...)
	require.NoError(t, err)
	return d
}

func receiptBody(t *testing.T) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
		TransactionId: "tx-1",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	})
	require.NoError(t, err)
	return body
}

func rejectionBody(t *testing.T) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.GovernanceRejection{
		Error: "L4 rejected the envelope",
		Receipt: &operatorv1.ActionReceipt{
			TransactionId: "tx-2",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY,
		},
	})
	require.NoError(t, err)
	return body
}

func refusalBody(t *testing.T) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.IngressRefusal{
		Code:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
		Error: "refused before envelope",
	})
	require.NoError(t, err)
	return body
}

// decodeRecorded decodes the i-th envelope body the fake Gateway received.
func decodeRecorded(t *testing.T, g *fakeGateway, i int) *commonv1.GovernanceEnvelope {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	require.Less(t, i, len(g.bodies))
	env := &commonv1.GovernanceEnvelope{}
	require.NoError(t, protojson.Unmarshal(g.bodies[i], env))
	return env
}

func TestDirectDriveReturnsObservationKindPerAnswer(t *testing.T) {
	tests := []struct {
		name   string
		answer fakeAnswer
		want   evalv1.ConformanceObservationKind
	}{
		{"accepted receipt", fakeAnswer{status: http.StatusOK, body: receiptBody(t)}, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
		{"governance rejection", fakeAnswer{status: http.StatusForbidden, body: rejectionBody(t)}, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION},
		{"ingress refusal", fakeAnswer{status: http.StatusBadRequest, body: refusalBody(t)}, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL},
		{"server error is untyped", fakeAnswer{status: http.StatusInternalServerError, body: []byte(`{"error":"internal"}`)}, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{tt.answer}}
			d := newFakeDirect(t, g, nil, BaseMutators())

			driven, err := d.Drive(context.Background(), testAttempt(cases.FaultNone, cases.ActionMutation))
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)
			assert.Equal(t, tt.want, driven.Observations[0].GetKind())
			assert.Equal(t, int32(tt.answer.status), driven.Observations[0].GetHttpStatus())
			assert.Len(t, g.bodies, 1)
		})
	}
}

func TestDirectDriveReplaySubmitsSameEnvelopeTwice(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{
		{status: http.StatusOK, body: receiptBody(t)},
		{status: http.StatusForbidden, body: rejectionBody(t)},
	}}
	d := newFakeDirect(t, g, nil, BaseMutators())

	driven, err := d.Drive(context.Background(), testAttempt(cases.FaultReplay, cases.ActionMutation))
	require.NoError(t, err)
	require.Len(t, driven.Observations, 2)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, driven.Observations[0].GetKind())
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, driven.Observations[1].GetKind())

	require.Len(t, g.bodies, 2)
	first := decodeRecorded(t, g, 0)
	second := decodeRecorded(t, g, 1)
	assert.Equal(t, first.GetId(), second.GetId())
	assert.Equal(t, first.GetNonce(), second.GetNonce())
}

func TestDirectDriveMissingMutatorIsUnsupported(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{status: http.StatusOK, body: receiptBody(t)}}}
	d := newFakeDirect(t, g, nil)

	_, err := d.Drive(context.Background(), testAttempt(cases.FaultExpired, cases.ActionMutation))
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrConformanceIngressUnsupported))
	assert.Empty(t, g.bodies, "an unsupported fault must not reach the Gateway")
}

func TestDirectDriveClosedServerIsErrorNotObservation(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{status: http.StatusOK, body: receiptBody(t)}}}
	server := httptest.NewServer(g)
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	d, err := NewDirect(c, testIdentity().Persona, testIdentity().Target, nil, BaseMutators())
	require.NoError(t, err)
	server.Close()

	driven, err := d.Drive(context.Background(), testAttempt(cases.FaultNone, cases.ActionMutation))
	require.Error(t, err)
	assert.Empty(t, driven.Observations)
}

func TestDirectDriveSubmitTransportFailureIsError(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{hijacked: true}}}
	d := newFakeDirect(t, g, nil, BaseMutators())

	driven, err := d.Drive(context.Background(), testAttempt(cases.FaultNone, cases.ActionMutation))
	require.Error(t, err)
	assert.Empty(t, driven.Observations)
}

func TestDirectDriveStateRootFailureIsError(t *testing.T) {
	g := &fakeGateway{state: http.StatusInternalServerError, answers: []fakeAnswer{{status: http.StatusOK, body: receiptBody(t)}}}
	d := newFakeDirect(t, g, nil, BaseMutators())

	_, err := d.Drive(context.Background(), testAttempt(cases.FaultNone, cases.ActionMutation))
	require.Error(t, err)
	assert.Empty(t, g.bodies, "no envelope is sent without a state root")
}

func TestDirectDriveMalformedJSONSendsTruncatedBody(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{status: http.StatusBadRequest, body: refusalBody(t)}}}
	d := newFakeDirect(t, g, nil)

	driven, err := d.Drive(context.Background(), testAttempt(cases.FaultMalformedJSON, cases.ActionMutation))
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, driven.Observations[0].GetKind())
	require.Len(t, g.bodies, 1)
	assert.Equal(t, malformedBody, string(g.bodies[0]))
}

func TestDirectDriveUnknownEventTypeIsRehashed(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{status: http.StatusBadRequest, body: refusalBody(t)}}}
	d := newFakeDirect(t, g, nil)

	_, err := d.Drive(context.Background(), testAttempt(cases.FaultUnknownEventType, cases.ActionMutation))
	require.NoError(t, err)
	env := decodeRecorded(t, g, 0)
	assert.Equal(t, unknownEventType, env.GetEventType())
	require.NoError(t, governance.VerifyTransactionHash(env, env.GetTransactionHash()))
}

func TestDirectDriveMissingFieldDropsPayloadAndRehashes(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{status: http.StatusBadRequest, body: refusalBody(t)}}}
	d := newFakeDirect(t, g, nil)

	_, err := d.Drive(context.Background(), testAttempt(cases.FaultMissingField, cases.ActionMutation))
	require.NoError(t, err)
	env := decodeRecorded(t, g, 0)
	assert.Empty(t, env.GetPayload())
	require.NoError(t, governance.VerifyTransactionHash(env, env.GetTransactionHash()))
}

// fakeL2 counts its calls, keeps the hash-check result of each envelope it was
// asked to prove, and returns one fixed set of votes.
type fakeL2 struct {
	calls    int
	hashErrs []error
}

func (f *fakeL2) Proof(_ context.Context, env *commonv1.GovernanceEnvelope) (*commonv1.L2Metadata, error) {
	f.calls++
	f.hashErrs = append(f.hashErrs, governance.VerifyTransactionHash(env, env.GetTransactionHash()))
	return &commonv1.L2Metadata{ConsensusSetId: "set-1"}, nil
}

func TestDirectDriveAttachesL2WithoutChangingHash(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{status: http.StatusOK, body: receiptBody(t)}}}
	l2 := &fakeL2{}
	d := newFakeDirect(t, g, l2, BaseMutators())

	_, err := d.Drive(context.Background(), testAttempt(cases.FaultNone, cases.ActionMutation))
	require.NoError(t, err)
	assert.Equal(t, 1, l2.calls)
	env := decodeRecorded(t, g, 0)
	assert.Equal(t, "set-1", env.GetGovernance().GetL2().GetConsensusSetId())
	require.NoError(t, governance.VerifyTransactionHash(env, env.GetTransactionHash()))
}

// TestDirectDriveTakesL2OverBaseline checks that the L2 proof is requested for
// the valid baseline, before a hash-breaking fault is applied.
func TestDirectDriveTakesL2OverBaseline(t *testing.T) {
	for _, fault := range []cases.Fault{cases.FaultPayloadTampered, cases.FaultMissingField} {
		t.Run(string(fault), func(t *testing.T) {
			g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{status: http.StatusOK, body: receiptBody(t)}}}
			l2 := &fakeL2{}
			d := newFakeDirect(t, g, l2, BaseMutators())

			_, err := d.Drive(context.Background(), testAttempt(fault, cases.ActionMutation))
			require.NoError(t, err)
			require.Len(t, l2.hashErrs, 1)
			require.NoError(t, l2.hashErrs[0], "L2 source saw a broken envelope")
		})
	}
}

func TestDirectDriveSkipsL2ForMutatorThatSetsIt(t *testing.T) {
	g := &fakeGateway{state: http.StatusOK, answers: []fakeAnswer{{status: http.StatusOK, body: receiptBody(t)}}}
	l2 := &fakeL2{}
	selfSigned := MutatorSet{cases.FaultNone: {SetsL2: true, Apply: noop.Apply}}
	d := newFakeDirect(t, g, l2, selfSigned)

	_, err := d.Drive(context.Background(), testAttempt(cases.FaultNone, cases.ActionMutation))
	require.NoError(t, err)
	assert.Equal(t, 0, l2.calls)
	env := decodeRecorded(t, g, 0)
	assert.Nil(t, env.GetGovernance().GetL2())
}

func TestNewDirectRejectsFaultInTwoSets(t *testing.T) {
	c, err := client.New(config.Config{MTLSBaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)

	_, err = NewDirect(c, testIdentity().Persona, testIdentity().Target, nil, BaseMutators(), MutatorSet{
		cases.FaultExpired: {},
	})
	assert.True(t, errors.Is(err, constants.ErrAlreadyExists))
}

func TestNewDirectRejectsNilClient(t *testing.T) {
	_, err := NewDirect(nil, testIdentity().Persona, testIdentity().Target, nil)
	assert.True(t, errors.Is(err, constants.ErrMissingRequiredField))
}
