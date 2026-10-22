// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package operator provides native CLI helpers for governed operator command
// dispatch through the gateway POST /api/v1/operators/commands surface.
package operator

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// CommandPoster posts a JSON body to a Gateway API path and returns the raw
// 2xx body, or an *api.StatusError carrying the non-2xx body.
type CommandPoster interface {
	Post(path string, body any) ([]byte, error)
}

// Target names the Operator session a run dispatches to.
type Target struct {
	OperatorSessionID string
	OperatorID        string
}

// RunResult is the decoded outcome for one operator session dispatch.
type RunResult struct {
	OperatorSessionID string `json:"operator_session_id"`
	OperatorID        string `json:"operator_id"`
	Success           bool   `json:"success"`
	TransactionID     string `json:"transaction_id,omitempty"`
	ExitCode          int32  `json:"exit_code,omitempty"`
	Stdout            string `json:"stdout,omitempty"`
	Stderr            string `json:"stderr,omitempty"`
	Error             string `json:"error,omitempty"`
	// RejectionCode is the signed event receipt's rejection_code when the executing
	// Operator rejected the command at L4.
	RejectionCode string `json:"rejection_code,omitempty"`
	// RefusalCode is the IngressRefusal code when the Gateway refused the
	// request before an envelope was built.
	RefusalCode string `json:"refusal_code,omitempty"`
	// SignerKeyID, EventReceiptStatus come from the executing Operator's verified
	// final event receipt on success.
	SignerKeyID        string `json:"signer_key_id,omitempty"`
	EventReceiptStatus string `json:"event_receipt_status,omitempty"`
	// ConsensusVotes are the recorded L2 member votes behind an
	// L2_QUORUM_NOT_MET rejection; ConsensusVotesError is why they could not
	// be read. Set by AttachConsensusVotes.
	ConsensusVotes      []models.ConsensusVoteRecord `json:"consensus_votes,omitempty"`
	ConsensusVotesError string                       `json:"consensus_votes_error,omitempty"`
	// Event receipt, Rejection and Refusal are the typed bodies behind the fields
	// above, for callers that need the signed evidence itself.
	EventReceipt *operatorv1.EventReceipt        `json:"-"`
	Rejection    *operatorv1.GovernanceRejection `json:"-"`
	Refusal      *operatorv1.IngressRefusal      `json:"-"`
	// TransportErr is the Post error when the Gateway sent no HTTP answer, so
	// callers can tell an unreachable Gateway from an untyped response.
	TransportErr error `json:"-"`
	// StartedAt is when the dispatch request began; DurationMs is its
	// monotonic-clock elapsed time through response decoding.
	StartedAt  time.Time `json:"started_at"`
	DurationMs float64   `json:"duration_ms"`
}

// RunSummary aggregates dispatch latency for one fan-out. Percentiles use the
// nearest-rank method over successful dispatches only, so fast-failing
// dispatches cannot make the distribution look better than it is.
type RunSummary struct {
	Targets     int     `json:"targets"`
	Succeeded   int     `json:"succeeded"`
	Failed      int     `json:"failed"`
	WallMs      float64 `json:"wall_ms"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	P99Ms       float64 `json:"p99_ms"`
	MaxMs       float64 `json:"max_ms"`
	Concurrency int     `json:"concurrency"`
}

// SummarizeRun builds the RunSummary for results produced with the given
// concurrency over wall of elapsed time.
func SummarizeRun(results []RunResult, wall time.Duration, concurrency int) RunSummary {
	summary := RunSummary{
		Targets:     len(results),
		WallMs:      DurationMs(wall),
		Concurrency: concurrency,
	}
	durations := make([]float64, 0, len(results))
	for _, result := range results {
		if !result.Success {
			summary.Failed++
			continue
		}
		summary.Succeeded++
		durations = append(durations, result.DurationMs)
	}
	if len(durations) == 0 {
		return summary
	}
	sort.Float64s(durations)
	summary.P50Ms = nearestRank(durations, 50)
	summary.P95Ms = nearestRank(durations, 95)
	summary.P99Ms = nearestRank(durations, 99)
	summary.MaxMs = durations[len(durations)-1]
	return summary
}

// DurationMs converts d to fractional milliseconds.
func DurationMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// nearestRank returns the percentile p (1..100) of ascending-sorted values.
func nearestRank(sorted []float64, p int) float64 {
	rank := int(math.Ceil(float64(p) / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// MarshalExecuteBashPayload builds the EXECUTE_BASH CommandRequested protobuf payload.
func MarshalExecuteBashPayload(command, executionID, justification string) ([]byte, error) {
	payload, err := proto.Marshal(&operatorv1.CommandRequested{
		Command:       command,
		ExecutionId:   executionID,
		Justification: justification,
	})
	if err != nil {
		return nil, fmt.Errorf("operator dispatch: marshal execute bash payload: %w", err)
	}
	return payload, nil
}

// BuildExecuteBashDispatchRequest assembles a governed shell dispatch request.
// The command and execution id are checked here because they are only visible
// before they are marshaled into the payload; every other field is checked by
// models.OperatorCommandRequest.Validate before the request is returned.
func BuildExecuteBashDispatchRequest(operatorSessionID, command, executionID, cliSessionID string) (models.OperatorCommandRequest, error) {
	command = strings.TrimSpace(command)
	if command == "" || executionID == "" {
		return models.OperatorCommandRequest{}, constants.ErrMissingRequiredField
	}
	payload, err := MarshalExecuteBashPayload(command, executionID, "ztd operator run")
	if err != nil {
		return models.OperatorCommandRequest{}, err
	}
	request := models.OperatorCommandRequest{
		TargetOperatorSessionID: operatorSessionID,
		EventType:               string(constants.Event.Operator.Command.Requested),
		Payload:                 payload,
		TargetResource:          "cli",
		CLISessionID:            cliSessionID,
	}
	if err := request.Validate(); err != nil {
		return models.OperatorCommandRequest{}, fmt.Errorf("operator dispatch: validate execute bash request: %w", err)
	}
	return request, nil
}

// ParseCommandResult decodes the governed operator command result from a dispatch
// response. The gateway blocks until a terminal command.completed/failed event
// arrives; an empty payload after success indicates a protocol mismatch.
func ParseCommandResult(response *operatorv1.GovernedDispatchResult) (*operatorv1.CommandResult, error) {
	if response == nil {
		return nil, fmt.Errorf("%w: dispatch response is nil", constants.ErrMissingRequiredField)
	}
	if len(response.GetResultPayload()) == 0 {
		return nil, fmt.Errorf(
			"operator dispatch: empty command result payload (event_type=%s action_type=%s)",
			response.GetEventType(),
			response.GetActionType(),
		)
	}
	result := &operatorv1.CommandResult{}
	if err := proto.Unmarshal(response.GetResultPayload(), result); err != nil {
		return nil, fmt.Errorf("operator dispatch: decode command result: %w", err)
	}
	return result, nil
}

// RunOnce dispatches one EXECUTE_BASH command and decodes the typed outcome.
// A 2xx body is the GovernedDispatchResult, which carries the executing
// Operator's final event receipt. A non-2xx body is decoded by
// governance.DecodeIngressFailure into a signed rejection or a typed refusal;
// an untyped body leaves the transport error as the result's error.
func RunOnce(client CommandPoster, target Target, command, executionID, cliSessionID string) (result RunResult) {
	result = RunResult{
		OperatorSessionID: target.OperatorSessionID,
		OperatorID:        target.OperatorID,
		StartedAt:         time.Now(),
	}
	// result is a named return so the deferred stamp lands on the value the
	// caller receives on every return path.
	defer func() { result.DurationMs = DurationMs(time.Since(result.StartedAt)) }()

	request, err := BuildExecuteBashDispatchRequest(target.OperatorSessionID, command, executionID, cliSessionID)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	raw, err := client.Post(constants.APIPaths.OperatorsCommands, request)
	if err != nil {
		result.Error = err.Error()
		var statusErr *api.StatusError
		if !errors.As(err, &statusErr) {
			result.TransportErr = err
			return result
		}
		applyIngressFailure(&result, []byte(statusErr.Body))
		return result
	}

	response := &operatorv1.GovernedDispatchResult{}
	if err := protojson.Unmarshal(raw, response); err != nil {
		result.Error = fmt.Errorf("operator dispatch: decode governed dispatch result: %w: %w", constants.ErrInvalidJSONResponse, err).Error()
		return result
	}
	result.TransactionID = response.GetTransactionId()
	result.EventReceipt = response.GetEventReceipt()
	if result.EventReceipt == nil {
		result.Error = fmt.Errorf("operator dispatch: %w: dispatch result carries no event receipt", constants.ErrMissingRequiredField).Error()
		return result
	}
	result.SignerKeyID = result.EventReceipt.GetSignerKeyId()
	result.EventReceiptStatus = result.EventReceipt.GetStatus().String()

	commandResult, err := ParseCommandResult(response)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.ExitCode = commandResult.GetReturnCode()
	result.Stdout = commandResult.GetStdout()
	result.Stderr = commandResult.GetStderr()
	if commandResult.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		result.Error = commandResult.GetError()
		if result.Error == "" {
			result.Error = "dispatch failed"
		}
		return result
	}
	result.Success = true
	return result
}

// applyIngressFailure records a non-2xx dispatch body on result. A signed
// rejection sets the transaction and rejection code; a typed refusal sets the
// refusal code. An untyped body changes nothing, so the transport error stays.
func applyIngressFailure(result *RunResult, body []byte) {
	rejection, refusal, err := governance.DecodeIngressFailure(body)
	if err != nil {
		return
	}
	switch {
	case rejection != nil:
		result.Rejection = rejection
		result.TransactionID = rejection.GetEventReceipt().GetTransactionId()
		result.RejectionCode = rejection.GetEventReceipt().GetRejectionCode().String()
		if summary := rejection.GetEventReceipt().GetResultSummary(); summary != "" {
			result.Error = summary
		}
	case refusal != nil:
		result.Refusal = refusal
		result.RefusalCode = refusal.GetCode().String()
		if refusal.GetError() != "" {
			result.Error = refusal.GetError()
		}
	}
}
