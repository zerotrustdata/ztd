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
	"net/url"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// maxTurnWallClock bounds a turn's wall-clock limit. A longer limit is a harness
// configuration error, and the bound keeps the deadline arithmetic in range.
const maxTurnWallClock = 24 * time.Hour

// maxSUTBodyBytes bounds one trace body read from the SUT.
const maxSUTBodyBytes = 16 << 20

// Binding is the configured SUT endpoint for a campaign.
type Binding struct {
	BaseURL    string       // https only
	HTTPClient *http.Client // carries the TLS configuration; the caller builds it
}

// ReceiptSource fetches one mirrored receipt by transaction id.
type ReceiptSource interface {
	GetActionReceipt(ctx context.Context, transactionID string, persona ...client.Persona) (*operatorv1.ActionReceipt, []byte, error)
}

// DataOperator is the dedicated session the turn runs against.
type DataOperator struct {
	Document     *operatorv1.OperatorDocument // its enrollment-bound actuator key pins the anchor
	SessionID    string
	SentinelPath string // absolute path of a harness-owned file on that Operator
}

// Client drives scored turns against one SUT binding. It holds no per-turn
// state, so one Client can run turns one after another.
type Client struct {
	binding  Binding
	receipts ReceiptSource
	persona  client.Persona
	sentinel cases.Sentinel

	// now and terminalGrace are fields so tests can control the clock and the
	// grace. New sets them to time.Now and constants.SUTTerminalGrace.
	now           func() time.Time
	terminalGrace time.Duration
}

// New validates the binding and its dependencies. The base URL must be https.
func New(b Binding, receipts ReceiptSource, persona client.Persona, sentinel cases.Sentinel) (*Client, error) {
	base, err := sutBaseURL(b.BaseURL)
	if err != nil {
		return nil, err
	}
	if b.HTTPClient == nil {
		return nil, fmt.Errorf("sut: binding http client: %w", constants.ErrMissingRequiredField)
	}
	if receipts == nil {
		return nil, fmt.Errorf("sut: receipt source: %w", constants.ErrMissingRequiredField)
	}
	if sentinel == nil {
		return nil, fmt.Errorf("sut: sentinel: %w", constants.ErrMissingRequiredField)
	}
	return &Client{
		binding:       Binding{BaseURL: base, HTTPClient: b.HTTPClient},
		receipts:      receipts,
		persona:       persona,
		sentinel:      sentinel,
		now:           time.Now,
		terminalGrace: constants.SUTTerminalGrace,
	}, nil
}

// sutBaseURL requires an https URL with a host and returns it without a trailing slash.
func sutBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("sut: parse base url: %w: %w", constants.ErrSUTTurnFailed, err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("sut: base url scheme %q is not https: %w", parsed.Scheme, constants.ErrSUTTurnFailed)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("sut: base url names no host: %w", constants.ErrSUTTurnFailed)
	}
	return strings.TrimRight(raw, "/"), nil
}

// Turn is everything one scored turn produced. Containment is always set when
// err is nil.
type Turn struct {
	Trace       *evalv1.AgentTrace
	Before      *operatorv1.ActionReceipt
	After       *operatorv1.ActionReceipt
	Claimed     []*operatorv1.ActionReceipt // in claimed-id order, verified
	Containment *evalv1.ContainmentResult
	Elapsed     time.Duration // from the post to the TERMINAL event
}

// RunTurn runs one turn. An error is a harness or SUT failure (never a grade).
// Evidence that fails verification is a Turn whose Containment verdict is
// INVALID_EVIDENCE or BREACHED.
func (c *Client) RunTurn(ctx context.Context, req *evalv1.ScoredTurnRequest, op DataOperator) (*Turn, error) {
	if err := validateTurnRequest(req, op); err != nil {
		return nil, err
	}
	body, err := evalv1.MarshalCanonical(req)
	if err != nil {
		return nil, fmt.Errorf("sut: encode turn request: %w", err)
	}
	before, err := c.sentinel.Read(ctx, cases.ExecutorOutbound, op.SentinelPath)
	if err != nil {
		return nil, fmt.Errorf("sut: read before sentinel: %w", err)
	}

	run, turnErr := c.runStream(ctx, req, body)
	// The after sentinel runs even when the turn failed, so the Operator's
	// ledger is bracketed before the next attempt. It takes the caller's
	// context, never the turn's bounded one.
	after, afterErr := c.sentinel.Read(ctx, cases.ExecutorOutbound, op.SentinelPath)
	if turnErr != nil {
		return nil, turnErr
	}
	if afterErr != nil {
		return nil, fmt.Errorf("sut: read after sentinel: %w", afterErr)
	}
	if err := validateTrace(req, run.trace); err != nil {
		return nil, err
	}
	claimed, containment := verifyEvidence(ctx, c.receipts, c.persona, op, before, after, run.trace.GetClaimedTransactionIds())
	return &Turn{
		Trace:       run.trace,
		Before:      before,
		After:       after,
		Claimed:     claimed,
		Containment: containment,
		Elapsed:     run.elapsed,
	}, nil
}

// turnRun is the trace one turn returned and the time from post to terminal event.
type turnRun struct {
	trace   *evalv1.AgentTrace
	elapsed time.Duration
}

// runStream opens the SUT's event stream, posts the turn once the stream has
// connected, reads to the terminal event and fetches the trace once. Every
// wait is bounded by the turn's wall-clock limit plus the terminal grace.
func (c *Client) runStream(ctx context.Context, req *evalv1.ScoredTurnRequest, body []byte) (turnRun, error) {
	deadline := time.Duration(req.GetWallClockLimitMs())*time.Millisecond + c.terminalGrace
	bounded, cancelBounded := context.WithTimeout(ctx, deadline)
	defer cancelBounded()
	streamCtx, cancelStream := context.WithCancel(bounded)
	defer cancelStream()

	var (
		posted     bool
		postedAt   time.Time
		postDone   = make(chan error, 1)
		terminal   bool
		terminalAt time.Time
		streamErr  error
	)
	attemptID := req.GetAttemptId()
	events := sse.NewClient(c.binding.BaseURL+fmt.Sprintf(constants.SUTTurnEventsPath, url.PathEscape(attemptID)), c.binding.HTTPClient)
	events.SetOnConnect(func() {
		// The callback must not block, so the post runs beside the stream. A
		// failed post ends the stream, and its answer is read below.
		posted = true
		postedAt = c.now()
		go func() {
			err := c.postTurn(bounded, body)
			if err != nil {
				cancelStream()
			}
			postDone <- err
		}()
	})
	handle := func(_, data string) {
		// Events after the terminal event are not read.
		if terminal || streamErr != nil {
			return
		}
		event := &evalv1.ScoredTurnEvent{}
		if err := evalv1.UnmarshalCanonical([]byte(data), event); err != nil {
			streamErr = fmt.Errorf("sut: decode turn event: %w: %w", constants.ErrSUTTraceInvalid, err)
			cancelStream()
			return
		}
		if event.GetAttemptId() != attemptID {
			streamErr = fmt.Errorf("sut: turn event names attempt %s, want %s: %w", event.GetAttemptId(), attemptID, constants.ErrSUTTraceInvalid)
			cancelStream()
			return
		}
		switch event.GetKind() {
		case evalv1.ScoredTurnEventKind_SCORED_TURN_EVENT_KIND_PROGRESS:
		case evalv1.ScoredTurnEventKind_SCORED_TURN_EVENT_KIND_TERMINAL:
			terminal = true
			terminalAt = c.now()
			cancelStream()
		default:
			streamErr = fmt.Errorf("sut: turn event kind %s: %w", event.GetKind(), constants.ErrSUTTraceInvalid)
			cancelStream()
		}
	}

	connectErr := events.ConnectOnce(streamCtx, handle)
	cancelStream()
	if !terminal {
		// The turn has failed, so abandon any post still in flight.
		cancelBounded()
	}
	var postErr error
	if posted {
		postErr = <-postDone
	}
	switch {
	case streamErr != nil:
		return turnRun{}, streamErr
	case !terminal:
		if cause := errors.Join(connectErr, postErr); cause != nil {
			return turnRun{}, fmt.Errorf("sut: turn ended before its terminal event: %w: %w", constants.ErrSUTTurnFailed, cause)
		}
		return turnRun{}, fmt.Errorf("sut: turn ended before its terminal event: %w", constants.ErrSUTTurnFailed)
	case postErr != nil:
		return turnRun{}, fmt.Errorf("sut: post turn: %w: %w", constants.ErrSUTTurnFailed, postErr)
	}

	trace, err := c.fetchTrace(bounded, attemptID)
	if err != nil {
		return turnRun{}, err
	}
	return turnRun{trace: trace, elapsed: terminalAt.Sub(postedAt)}, nil
}

// postTurn posts the canonical turn request. An answer outside 2xx is an error.
func (c *Client) postTurn(ctx context.Context, body []byte) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.binding.BaseURL+constants.SUTTurnsPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build turn request: %w", err)
	}
	httpReq.Header.Set("Content-Type", constants.MediaTypeJSON)
	resp, err := c.binding.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send turn request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// fetchTrace fetches the attempt's trace once and decodes it.
func (c *Client) fetchTrace(ctx context.Context, attemptID string) (*evalv1.AgentTrace, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.binding.BaseURL+fmt.Sprintf(constants.SUTTurnTracePath, url.PathEscape(attemptID)), nil)
	if err != nil {
		return nil, fmt.Errorf("sut: build trace request: %w", err)
	}
	httpReq.Header.Set("Accept", constants.MediaTypeJSON)
	resp, err := c.binding.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sut: fetch trace: %w: %w", constants.ErrSUTTurnFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("sut: fetch trace: status %d: %w", resp.StatusCode, constants.ErrSUTTurnFailed)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSUTBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("sut: read trace: %w: %w", constants.ErrSUTTurnFailed, err)
	}
	trace := &evalv1.AgentTrace{}
	if err := evalv1.UnmarshalCanonical(raw, trace); err != nil {
		return nil, fmt.Errorf("sut: decode trace: %w: %w", constants.ErrSUTTraceInvalid, err)
	}
	return trace, nil
}

// validateTurnRequest rejects a request the harness built wrongly. It runs
// before any I/O, so a bad request makes no HTTP call and no sentinel read.
func validateTurnRequest(req *evalv1.ScoredTurnRequest, op DataOperator) error {
	required := []struct{ field, value string }{
		{"attempt_id", req.GetAttemptId()},
		{"campaign_id", req.GetCampaignId()},
		{"run_id", req.GetRunId()},
		{"workspace_root", req.GetWorkspaceRoot()},
		{"user_message", req.GetUserMessage()},
		{"data_operator.session_id", op.SessionID},
		{"data_operator.sentinel_path", op.SentinelPath},
	}
	for _, field := range required {
		if field.value == "" {
			return invalidTurnRequest(field.field)
		}
	}
	switch req.GetLane() {
	case evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM:
	case evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE:
		if req.GetDesignatedRole() == evalv1.AgentRole_AGENT_ROLE_UNSPECIFIED {
			return invalidTurnRequest("designated_role")
		}
	default:
		return invalidTurnRequest("lane")
	}
	if req.GetToolTurnLimit() == 0 {
		return invalidTurnRequest("tool_turn_limit")
	}
	if ms := req.GetWallClockLimitMs(); ms == 0 || ms > uint64(maxTurnWallClock/time.Millisecond) {
		return invalidTurnRequest("wall_clock_limit_ms")
	}
	if op.Document == nil {
		return invalidTurnRequest("data_operator.document")
	}
	if req.GetDataOperatorSessionId() != op.SessionID {
		return invalidTurnRequest("data_operator_session_id")
	}
	return nil
}

// invalidTurnRequest names the request field the harness got wrong.
func invalidTurnRequest(field string) error {
	return fmt.Errorf("sut: invalid turn request: %s: %w", field, constants.ErrMissingRequiredField)
}

// validateTrace checks the trace's own shape against the request. Per-step role
// and output checks belong to the persona grader, not here.
func validateTrace(req *evalv1.ScoredTurnRequest, trace *evalv1.AgentTrace) error {
	if trace.GetAttemptId() != req.GetAttemptId() {
		return fmt.Errorf("sut: trace names attempt %s, want %s: %w", trace.GetAttemptId(), req.GetAttemptId(), constants.ErrSUTTraceInvalid)
	}
	if trace.GetFinishReason() == evalv1.AgentFinishReason_AGENT_FINISH_REASON_UNSPECIFIED {
		return fmt.Errorf("sut: trace finish reason is unspecified: %w", constants.ErrSUTTraceInvalid)
	}
	claimed := make(map[string]struct{}, len(trace.GetClaimedTransactionIds()))
	for _, id := range trace.GetClaimedTransactionIds() {
		if id == "" {
			return fmt.Errorf("sut: trace claims an empty transaction id: %w", constants.ErrSUTTraceInvalid)
		}
		if _, repeated := claimed[id]; repeated {
			return fmt.Errorf("sut: trace claims transaction %s twice: %w", id, constants.ErrSUTTraceInvalid)
		}
		claimed[id] = struct{}{}
	}
	indexes := make(map[uint32]struct{}, len(trace.GetSteps()))
	for _, step := range trace.GetSteps() {
		if _, repeated := indexes[step.GetIndex()]; repeated {
			return fmt.Errorf("sut: trace repeats step index %d: %w", step.GetIndex(), constants.ErrSUTTraceInvalid)
		}
		indexes[step.GetIndex()] = struct{}{}
	}
	return nil
}
