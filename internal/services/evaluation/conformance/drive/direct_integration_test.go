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
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	agentconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	"github.com/zerotrustdata/ztd/internal/uuid"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// integrationRows are the DIRECT doctrine rows the Tier 2 test drives. Row 13
// contributes its first corpus entry only.
var integrationRows = map[int]bool{1: true, 3: true, 4: true, 6: true, 8: true, 10: true, 11: true, 12: true, 13: true, 15: true}

// directFixture is one live Gateway with its CLI identity and a DIRECT driver.
type directFixture struct {
	f       *fixtures.GatewayFixture
	direct  *Direct
	ident   *fixtures.ClientIdentity
	workDir string
}

// newDirectFixture boots a Gateway under posture and builds a DIRECT driver
// that submits as the bootstrapped CLI identity to the embedded Operator.
func newDirectFixture(t *testing.T, posture constants.Posture) *directFixture {
	t.Helper()
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-direct-" + string(posture),
		Posture:  posture,
	})
	f.WaitForReady(t)
	ident := fixtures.BootstrapCLIIdentity(t, f, "conformance-direct-host")

	cliCert, err := tls.X509KeyPair([]byte(ident.CLICertificate), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ident.CLIPrivateKey}))
	require.NoError(t, err)
	caBundle := append(append([]byte{}, testutil.ReadRootCA(t, f.PKIDir)...), testutil.ReadOperatorCA(t, f.PKIDir)...)
	caPath := filepath.Join(testutil.TempDir(t), "ca.pem")
	require.NoError(t, os.WriteFile(caPath, caBundle, 0o600))

	url := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())
	c, err := client.New(agentconfig.Config{
		MTLSBaseURL:   url,
		PublicBaseURL: url,
		Auth:          agentconfig.Auth{Certificate: &cliCert, CABundle: caPath},
	})
	require.NoError(t, err)

	persona := client.Persona{ID: "conformance-direct", UserID: ident.UserID, CLISessionID: ident.CLISessionID}
	target := operator.Target{OperatorID: ident.OperatorID, OperatorSessionID: ident.OperatorSessionID}
	direct, err := NewDirect(c, persona, target, nil, BaseMutators())
	require.NoError(t, err)

	// The embedded Operator confines file edits to its working directory, so the
	// case targets live under the fixture's test-results directory beneath it.
	return &directFixture{f: f, direct: direct, ident: ident, workDir: f.Config.WorkDir}
}

// attemptFor builds a fresh attempt for one case, with targets under the
// fixture work directory and removed when the test ends.
func (fx *directFixture) attemptFor(t *testing.T, c cases.Case) cases.Attempt {
	t.Helper()
	attemptID, err := uuid.NewString()
	require.NoError(t, err)
	runID, err := uuid.NewString()
	require.NoError(t, err)
	results := filepath.Join(fx.workDir, "test-results")
	parent := filepath.Join(results, "conformance-direct")
	dir := filepath.Join(parent, runID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		// The work directory is the package directory: drop the parents once
		// empty so no run leaves directories in the source tree.
		_ = os.Remove(parent)
		_ = os.Remove(results)
	})
	return cases.Attempt{
		RunID:     runID,
		AttemptID: attemptID,
		Case:      c,
		TargetAbs: filepath.Join(dir, "target.txt"),
		ReadAbs:   filepath.Join(dir, "read.txt"),
		Marker:    cases.Marker(runID, attemptID),
	}
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
	c, err := client.New(agentconfig.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	d, err := NewDirect(c, testIdentity().Persona, testIdentity().Target, l2, sets...)
	require.NoError(t, err)
	return d
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
	c, err := client.New(agentconfig.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
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
