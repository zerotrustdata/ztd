// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sut

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	testBaseURL   = "https://sut.test"
	testAttemptID = "attempt-1"
)

var testPersona = client.Persona{ID: "sut-test-harness", CLISessionID: "cli-session-test"}

// The client tests run over an in-memory RoundTripper, not a listener. The
// tier guard in internal/testutil rejects httptest listeners outside Tier 2,
// and a Tier 2 test may not stand in a fake for the sentinel or the receipts.

// callLog records the order of calls across the fake SUT and the fake sentinel.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func newCallLog() *callLog {
	return &callLog{}
}

func (l *callLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// fakeSUT answers the SUT's three routes in memory. The client's real request,
// status and SSE code runs over it.
type fakeSUT struct {
	log         *callLog
	events      []*evalv1.ScoredTurnEvent // streamed in order, then the stream ends
	rawEvents   string                    // when set, streamed instead of events
	holdOpen    bool                      // keep the stream open until the client cancels it
	postStatus  int                       // 0 answers 202 Accepted
	trace       *evalv1.AgentTrace
	traceStatus int    // 0 answers 200 OK
	traceBody   []byte // when set, served as the trace body instead of trace

	mu           sync.Mutex
	roundTrips   int
	posted       *evalv1.ScoredTurnRequest
	traceFetches int
}

func (s *fakeSUT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.roundTrips++
	s.mu.Unlock()
	switch {
	case req.Method == http.MethodPost && req.URL.Path == constants.SUTTurnsPath:
		return s.answerPost(req)
	case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/events"):
		s.log.add("stream")
		frames := s.rawEvents
		if frames == "" {
			rendered, err := sseFrames(s.events)
			if err != nil {
				return nil, err
			}
			frames = rendered
		}
		return response(req, http.StatusOK, &eventStream{ctx: req.Context(), frames: strings.NewReader(frames), holdOpen: s.holdOpen}), nil
	case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/trace"):
		s.log.add("trace")
		s.mu.Lock()
		s.traceFetches++
		s.mu.Unlock()
		body := s.traceBody
		if body == nil {
			encoded, err := evalv1.MarshalCanonical(s.trace)
			if err != nil {
				return nil, err
			}
			body = encoded
		}
		status := s.traceStatus
		if status == 0 {
			status = http.StatusOK
		}
		return response(req, status, io.NopCloser(bytes.NewReader(body))), nil
	}
	return response(req, http.StatusNotFound, io.NopCloser(strings.NewReader(""))), nil
}

func (s *fakeSUT) answerPost(req *http.Request) (*http.Response, error) {
	s.log.add("post")
	defer req.Body.Close()
	if got := req.Header.Get("Content-Type"); got != constants.MediaTypeJSON {
		return nil, fmt.Errorf("turn posted with content type %q", got)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	turn := &evalv1.ScoredTurnRequest{}
	if err := evalv1.UnmarshalCanonical(raw, turn); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.posted = turn
	s.mu.Unlock()
	status := s.postStatus
	if status == 0 {
		status = http.StatusAccepted
	}
	return response(req, status, io.NopCloser(strings.NewReader(""))), nil
}

func (s *fakeSUT) postedRequest() *evalv1.ScoredTurnRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posted
}

func (s *fakeSUT) traceFetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traceFetches
}

func (s *fakeSUT) roundTripCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roundTrips
}

// eventStream is the SSE body: the frames, then either the end of the stream or,
// when holdOpen is set, a read that waits until the client's context ends.
type eventStream struct {
	ctx      context.Context
	frames   *strings.Reader
	holdOpen bool
}

func (b *eventStream) Read(p []byte) (int, error) {
	n, err := b.frames.Read(p)
	if errors.Is(err, io.EOF) && b.holdOpen {
		<-b.ctx.Done()
		return n, b.ctx.Err()
	}
	return n, err
}

func (b *eventStream) Close() error {
	return nil
}

func response(req *http.Request, status int, body io.ReadCloser) *http.Response {
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		Header:     make(http.Header),
		Body:       body,
		Request:    req,
	}
}

// sseFrames renders events as the data frames the SUT streams.
func sseFrames(events []*evalv1.ScoredTurnEvent) (string, error) {
	var b strings.Builder
	for _, event := range events {
		encoded, err := evalv1.MarshalCanonical(event)
		if err != nil {
			return "", err
		}
		b.WriteString("data: ")
		b.Write(encoded)
		b.WriteString("\n\n")
	}
	return b.String(), nil
}

// fakeSentinel answers successive sentinel reads from a script and records the
// context state of each read, so a test can see whether the after read ran live.
type fakeSentinel struct {
	log      *callLog
	receipts []*operatorv1.ActionReceipt
	errs     []error // errs[i] fails read i when set
	reads    []sentinelRead
}

type sentinelRead struct {
	executor cases.Executor
	path     string
	ctxErr   error
}

func (s *fakeSentinel) Read(ctx context.Context, ex cases.Executor, readAbs string) (*operatorv1.ActionReceipt, error) {
	i := len(s.reads)
	s.reads = append(s.reads, sentinelRead{executor: ex, path: readAbs, ctxErr: ctx.Err()})
	call := "sentinel:after"
	if i == 0 {
		call = "sentinel:before"
	}
	s.log.add(call)
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if i >= len(s.receipts) {
		return nil, fmt.Errorf("sentinel read %d is not scripted", i)
	}
	return s.receipts[i], nil
}

// fakeReceipts answers GetActionReceipt from memory. An id with no receipt is
// not found, as the Gateway answers 404.
type fakeReceipts struct {
	byID    map[string]*operatorv1.ActionReceipt
	failing map[string]error
	fetches map[string]int
}

func newFakeReceipts() *fakeReceipts {
	return &fakeReceipts{
		byID:    make(map[string]*operatorv1.ActionReceipt),
		failing: make(map[string]error),
		fetches: make(map[string]int),
	}
}

func (f *fakeReceipts) GetActionReceipt(_ context.Context, transactionID string, _ ...client.Persona) (*operatorv1.ActionReceipt, []byte, error) {
	f.fetches[transactionID]++
	if err := f.failing[transactionID]; err != nil {
		return nil, nil, err
	}
	return f.byID[transactionID], nil, nil
}

// steppingClock returns the given instants in order, then keeps returning the last.
func steppingClock(instants ...time.Time) func() time.Time {
	var mu sync.Mutex
	next := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		instant := instants[next]
		if next < len(instants)-1 {
			next++
		}
		return instant
	}
}

// turnRig wires one turn's fakes into a client built over them.
type turnRig struct {
	fixture  *evidenceFixture
	log      *callLog
	sut      *fakeSUT
	sentinel *fakeSentinel
	client   *Client
}

func newTurnRig(t *testing.T) *turnRig {
	t.Helper()
	f := newEvidenceFixture(t)
	log := newCallLog()
	rig := &turnRig{
		fixture:  f,
		log:      log,
		sut:      &fakeSUT{log: log},
		sentinel: &fakeSentinel{log: log},
	}
	c, err := New(Binding{BaseURL: testBaseURL, HTTPClient: &http.Client{Transport: rig.sut}}, f.receipts, testPersona, rig.sentinel)
	require.NoError(t, err)
	rig.client = c
	return rig
}

// emptyBracket scripts the two sentinel reads of a bracket with no links between them.
func (r *turnRig) emptyBracket() {
	r.sentinel.receipts = []*operatorv1.ActionReceipt{
		r.fixture.sentinel("sentinel-before", "h-genesis", "h-before"),
		r.fixture.sentinel("sentinel-after", "h-before", "h-after"),
	}
}

// turnRequest is a valid system-lane turn request for the test attempt.
func turnRequest() *evalv1.ScoredTurnRequest {
	return &evalv1.ScoredTurnRequest{
		CampaignId:            "campaign-1",
		RunId:                 "run-1",
		AttemptId:             testAttemptID,
		Lane:                  evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
		WorkspaceRoot:         "/workspace",
		DataOperatorSessionId: testSessionID,
		UserMessage:           "check the service",
		ToolTurnLimit:         4,
		WallClockLimitMs:      60_000,
	}
}

func progressEvent(attemptID string) *evalv1.ScoredTurnEvent {
	return &evalv1.ScoredTurnEvent{AttemptId: attemptID, Kind: evalv1.ScoredTurnEventKind_SCORED_TURN_EVENT_KIND_PROGRESS, Detail: "tool call"}
}

// terminalEvent is the terminal event of the test attempt.
func terminalEvent() *evalv1.ScoredTurnEvent {
	return &evalv1.ScoredTurnEvent{AttemptId: testAttemptID, Kind: evalv1.ScoredTurnEventKind_SCORED_TURN_EVENT_KIND_TERMINAL}
}

// validTrace is a stopped trace for the test attempt that claims the given ids.
func validTrace(claims ...string) *evalv1.AgentTrace {
	return &evalv1.AgentTrace{
		AttemptId: testAttemptID,
		Steps: []*evalv1.AgentStep{
			{Index: 0, Role: evalv1.AgentRole_AGENT_ROLE_RESPONDER},
			{Index: 1, Role: evalv1.AgentRole_AGENT_ROLE_RESPONDER},
		},
		ClaimedTransactionIds: claims,
		FinishReason:          evalv1.AgentFinishReason_AGENT_FINISH_REASON_STOPPED,
		FinalReply:            "done",
	}
}

func TestRunTurnPostsAfterStreamConnectsAndContainsClaimedBracket(t *testing.T) {
	rig := newTurnRig(t)
	f := rig.fixture
	before := f.sentinel("sentinel-before", "h-genesis", "h-before")
	after := f.sentinel("sentinel-after", "h-2", "h-after")
	f.link("tx-1", "h-before", "h-1")
	f.link("tx-2", "h-1", "h-2")
	rig.sentinel.receipts = []*operatorv1.ActionReceipt{before, after}
	rig.sut.events = []*evalv1.ScoredTurnEvent{progressEvent(testAttemptID), terminalEvent()}
	rig.sut.trace = validTrace("tx-1", "tx-2")
	rig.client.now = steppingClock(time.Unix(1_700_000_000, 0), time.Unix(1_700_000_000, 0).Add(250*time.Millisecond))

	turn, err := rig.client.RunTurn(context.Background(), turnRequest(), f.op)

	require.NoError(t, err)
	assert.Equal(t, []string{"sentinel:before", "stream", "post", "trace", "sentinel:after"}, rig.log.snapshot())
	assert.Equal(t, 1, rig.sut.traceFetchCount(), "the trace is fetched once")
	assert.Equal(t, 1, f.receipts.fetches["tx-1"], "each claimed receipt is fetched once")
	assert.Equal(t, 1, f.receipts.fetches["tx-2"], "each claimed receipt is fetched once")
	assert.True(t, proto.Equal(turnRequest(), rig.sut.postedRequest()), "the posted request is the turn request")
	assert.Equal(t, 250*time.Millisecond, turn.Elapsed, "elapsed runs from the post to the terminal event")
	require.NotNil(t, turn.Containment)
	assert.Equal(t, evalv1.ContainmentVerdict_CONTAINMENT_VERDICT_CONTAINED, turn.Containment.GetVerdict())
	require.Len(t, turn.Claimed, 2)
	assert.Equal(t, "tx-1", turn.Claimed[0].GetTransactionId())
	assert.Equal(t, "tx-2", turn.Claimed[1].GetTransactionId())
	assert.Equal(t, "sentinel-before", turn.Before.GetTransactionId())
	assert.Equal(t, "sentinel-after", turn.After.GetTransactionId())
}

func TestRunTurnIgnoresStreamEventsAfterTerminal(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sut.events = []*evalv1.ScoredTurnEvent{terminalEvent(), progressEvent("attempt-other")}
	rig.sut.trace = validTrace()

	turn, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

	require.NoError(t, err)
	assert.Equal(t, evalv1.ContainmentVerdict_CONTAINMENT_VERDICT_CONTAINED, turn.Containment.GetVerdict())
}

func TestRunTurnRejectsStreamEventsThatBreakTheContract(t *testing.T) {
	cases := []struct {
		name      string
		events    []*evalv1.ScoredTurnEvent
		rawEvents string
	}{
		{
			name:   "an event for another attempt",
			events: []*evalv1.ScoredTurnEvent{progressEvent("attempt-other")},
		},
		{
			name:   "an event of unspecified kind",
			events: []*evalv1.ScoredTurnEvent{{AttemptId: testAttemptID}},
		},
		{
			name:      "an event that does not decode",
			rawEvents: "data: {\"attempt_id\": 7}\n\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTurnRig(t)
			rig.emptyBracket()
			rig.sut.events = tc.events
			rig.sut.rawEvents = tc.rawEvents

			_, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

			require.ErrorIs(t, err, constants.ErrSUTTraceInvalid)
			assert.Contains(t, rig.log.snapshot(), "sentinel:after", "the after sentinel still brackets the turn")
			assert.NotContains(t, rig.log.snapshot(), "trace")
		})
	}
}

func TestRunTurnFailsWhenStreamClosesBeforeTerminalEvent(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sut.events = []*evalv1.ScoredTurnEvent{progressEvent(testAttemptID)}

	_, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

	require.ErrorIs(t, err, constants.ErrSUTTurnFailed)
	require.Len(t, rig.sentinel.reads, 2)
	assert.NoError(t, rig.sentinel.reads[1].ctxErr, "the after read runs with a live context")
	assert.NotContains(t, rig.log.snapshot(), "trace")
}

func TestRunTurnReturnsTurnFailureBeforeAfterSentinelFailure(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sentinel.errs = []error{nil, fmt.Errorf("after sentinel submit failed")}
	rig.sut.events = []*evalv1.ScoredTurnEvent{progressEvent(testAttemptID)}

	_, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

	require.ErrorIs(t, err, constants.ErrSUTTurnFailed, "the turn failure is the first error")
}

func TestRunTurnFailsWhenNoTerminalEventArrivesWithinGrace(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sut.events = []*evalv1.ScoredTurnEvent{progressEvent(testAttemptID)}
	rig.sut.holdOpen = true
	rig.client.terminalGrace = 20 * time.Millisecond
	req := turnRequest()
	req.WallClockLimitMs = 1

	_, err := rig.client.RunTurn(context.Background(), req, rig.fixture.op)

	require.ErrorIs(t, err, constants.ErrSUTTurnFailed)
	require.Len(t, rig.sentinel.reads, 2)
	assert.NoError(t, rig.sentinel.reads[1].ctxErr, "the after read runs with a live context")
	assert.NotContains(t, rig.log.snapshot(), "trace")
}

func TestRunTurnFailsAndClosesStreamWhenPostIsRefused(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sut.postStatus = http.StatusInternalServerError
	rig.sut.holdOpen = true

	start := time.Now()
	_, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

	require.ErrorIs(t, err, constants.ErrSUTTurnFailed)
	assert.ErrorContains(t, err, "status 500")
	assert.Less(t, time.Since(start), 10*time.Second, "a refused post ends the stream without waiting out the grace")
	assert.NotContains(t, rig.log.snapshot(), "trace")
	assert.Contains(t, rig.log.snapshot(), "sentinel:after")
}

func TestRunTurnRejectsTraceForAnotherAttemptDuplicateClaimsAndUnspecifiedFinish(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*evalv1.AgentTrace)
	}{
		{
			name:   "a trace for another attempt",
			mutate: func(tr *evalv1.AgentTrace) { tr.AttemptId = "attempt-other" },
		},
		{
			name:   "a transaction claimed twice",
			mutate: func(tr *evalv1.AgentTrace) { tr.ClaimedTransactionIds = []string{"tx-1", "tx-1"} },
		},
		{
			name:   "an empty claimed transaction id",
			mutate: func(tr *evalv1.AgentTrace) { tr.ClaimedTransactionIds = []string{"tx-1", ""} },
		},
		{
			name: "an unspecified finish reason",
			mutate: func(tr *evalv1.AgentTrace) {
				tr.FinishReason = evalv1.AgentFinishReason_AGENT_FINISH_REASON_UNSPECIFIED
			},
		},
		{
			name: "a step index repeated",
			mutate: func(tr *evalv1.AgentTrace) {
				tr.Steps = []*evalv1.AgentStep{{Index: 3}, {Index: 3}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTurnRig(t)
			rig.emptyBracket()
			rig.sut.events = []*evalv1.ScoredTurnEvent{terminalEvent()}
			trace := validTrace()
			tc.mutate(trace)
			rig.sut.trace = trace

			_, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

			require.ErrorIs(t, err, constants.ErrSUTTraceInvalid)
			assert.Contains(t, rig.log.snapshot(), "sentinel:after")
		})
	}
}

func TestRunTurnRejectsUndecodableTraceBody(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sut.events = []*evalv1.ScoredTurnEvent{terminalEvent()}
	rig.sut.traceBody = []byte("not a trace")

	_, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

	require.ErrorIs(t, err, constants.ErrSUTTraceInvalid)
}

func TestRunTurnFailsWhenTraceFetchIsRefused(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sut.events = []*evalv1.ScoredTurnEvent{terminalEvent()}
	rig.sut.traceStatus = http.StatusNotFound

	_, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

	require.ErrorIs(t, err, constants.ErrSUTTurnFailed)
	assert.Equal(t, 1, rig.sut.traceFetchCount(), "a refused trace is fetched once and not retried")
}

func TestRunTurnRejectsInvalidRequestBeforeAnyIO(t *testing.T) {
	maxMs := uint64(maxTurnWallClock / time.Millisecond)
	cases := []struct {
		field  string
		mutate func(*evalv1.ScoredTurnRequest, *DataOperator)
	}{
		{"attempt_id", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) { r.AttemptId = "" }},
		{"campaign_id", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) { r.CampaignId = "" }},
		{"run_id", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) { r.RunId = "" }},
		{"workspace_root", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) { r.WorkspaceRoot = "" }},
		{"user_message", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) { r.UserMessage = "" }},
		{"lane", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) {
			r.Lane = evalv1.EvaluationLane_EVALUATION_LANE_UNSPECIFIED
		}},
		{"lane", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) {
			r.Lane = evalv1.EvaluationLane_EVALUATION_LANE_PLATFORM
		}},
		{"designated_role", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) {
			r.Lane = evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE
		}},
		{"tool_turn_limit", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) { r.ToolTurnLimit = 0 }},
		{"wall_clock_limit_ms", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) { r.WallClockLimitMs = 0 }},
		{"wall_clock_limit_ms", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) { r.WallClockLimitMs = maxMs + 1 }},
		{"data_operator.document", func(_ *evalv1.ScoredTurnRequest, op *DataOperator) { op.Document = nil }},
		{"data_operator.session_id", func(_ *evalv1.ScoredTurnRequest, op *DataOperator) { op.SessionID = "" }},
		{"data_operator.sentinel_path", func(_ *evalv1.ScoredTurnRequest, op *DataOperator) { op.SentinelPath = "" }},
		{"data_operator_session_id", func(r *evalv1.ScoredTurnRequest, _ *DataOperator) {
			r.DataOperatorSessionId = "session-other"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			rig := newTurnRig(t)
			rig.emptyBracket()
			req := turnRequest()
			op := rig.fixture.op
			tc.mutate(req, &op)

			_, err := rig.client.RunTurn(context.Background(), req, op)

			require.ErrorIs(t, err, constants.ErrMissingRequiredField)
			assert.ErrorContains(t, err, "sut: invalid turn request: "+tc.field)
			assert.Zero(t, rig.sut.roundTripCount(), "an invalid request makes no HTTP call")
			assert.Empty(t, rig.sentinel.reads, "an invalid request makes no sentinel read")
		})
	}
}

func TestRunTurnStopsBeforePostWhenBeforeSentinelFails(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sentinel.errs = []error{fmt.Errorf("before sentinel submit failed")}

	_, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

	require.Error(t, err)
	assert.ErrorContains(t, err, "read before sentinel")
	assert.Equal(t, []string{"sentinel:before"}, rig.log.snapshot())
	assert.Zero(t, rig.sut.roundTripCount())
}

func TestRunTurnReturnsNoGradeWhenAfterSentinelFails(t *testing.T) {
	rig := newTurnRig(t)
	rig.emptyBracket()
	rig.sentinel.errs = []error{nil, fmt.Errorf("after sentinel submit failed")}
	rig.sut.events = []*evalv1.ScoredTurnEvent{terminalEvent()}
	rig.sut.trace = validTrace()

	turn, err := rig.client.RunTurn(context.Background(), turnRequest(), rig.fixture.op)

	require.Error(t, err)
	assert.Nil(t, turn, "a failed after read yields an error, never a containment verdict")
	assert.ErrorContains(t, err, "read after sentinel")
}

func TestNewRejectsBaseURLThatIsNotHTTPS(t *testing.T) {
	scenarios := []struct {
		name string
		raw  string
	}{
		{"plain http scheme", "http://sut.test"},
		{"no scheme", "sut.test"},
		{"no host", "https://"},
		{"other scheme", "ftp://sut.test"},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := newEvidenceFixture(t)
			_, err := New(Binding{BaseURL: sc.raw, HTTPClient: &http.Client{}}, f.receipts, testPersona, &fakeSentinel{log: newCallLog()})
			require.ErrorIs(t, err, constants.ErrSUTTurnFailed)
		})
	}
}

func TestNewRequiresHTTPClientReceiptSourceAndSentinel(t *testing.T) {
	f := newEvidenceFixture(t)
	valid := Binding{BaseURL: testBaseURL, HTTPClient: &http.Client{}}
	scenarios := []struct {
		name      string
		binding   Binding
		receipts  ReceiptSource
		sentinels cases.Sentinel
	}{
		{"nil http client", Binding{BaseURL: testBaseURL}, f.receipts, &fakeSentinel{log: newCallLog()}},
		{"nil receipt source", valid, nil, &fakeSentinel{log: newCallLog()}},
		{"nil sentinel", valid, f.receipts, nil},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			_, err := New(sc.binding, sc.receipts, testPersona, sc.sentinels)
			require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		})
	}
}
