// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// ListOperators returns the authenticated user's Operator documents through
// GET /api/v1/operators.
func (c *Client) ListOperators(ctx context.Context) ([]*operatorv1.OperatorDocument, []byte, error) {
	if c.cfg.UserID == "" {
		return nil, nil, fmt.Errorf("%w: authenticated user id is required for operator list", constants.ErrMissingRequiredField)
	}
	u := c.cfg.MTLSBaseURL + constants.APIPaths.Operators + "?" + url.Values{"user_id": {c.cfg.UserID}}.Encode()
	status, body, err := c.do(ctx, c.auditorPersona(), http.MethodGet, u, nil)
	if err != nil {
		return nil, body, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, body, fmt.Errorf("%w: operator list returned status %d", constants.ErrHTTPStatusError, status)
	}
	response := &models.OperatorSlotResponse{}
	if err := json.Unmarshal(body, response); err != nil {
		return nil, body, fmt.Errorf("%w: decode operator list: %v", constants.ErrInvalidJSONResponse, err)
	}
	if !response.Success {
		return nil, body, fmt.Errorf("%w: operator list response is incomplete", constants.ErrInvalidJSONResponse)
	}
	return response.Operators, body, nil
}

// DiscoverInferenceOperator selects exactly one active inference-capable remote
// Operator. When operatorSessionID is non-empty it must match that session.
func (c *Client) DiscoverInferenceOperator(ctx context.Context, operatorSessionID string) (*operatorv1.OperatorDocument, []byte, error) {
	operators, body, err := c.ListOperators(ctx)
	if err != nil {
		return nil, body, err
	}
	var matches []*operatorv1.OperatorDocument
	for _, op := range operators {
		if op.RuntimeConfig == nil || !operatorcapability.HasActiveCapability(op, constants.OperatorCapabilityInference) {
			continue
		}
		if operatorSessionID != "" && op.GetOperatorSessionId() != operatorSessionID {
			continue
		}
		matches = append(matches, op)
	}
	switch len(matches) {
	case 0:
		if operatorSessionID != "" {
			return nil, body, fmt.Errorf("%w: session %s", constants.ErrInferenceOperatorNotCapable, operatorSessionID)
		}
		return nil, body, constants.ErrInferenceOperatorNotFound
	case 1:
		selected := matches[0]
		return selected, body, nil
	default:
		return nil, body, constants.ErrInferenceOperatorAmbiguous
	}
}

// DispatchInference sends POST /api/v1/inference/dispatch using the configured
// platform-enrolled application identity. The caller must configure Auth with an
// enrolled app cert such as ztd-eval; CLI session headers are not attached.
func (c *Client) DispatchInference(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, []byte, error) {
	if req == nil {
		return nil, nil, constants.ErrMissingRequiredField
	}
	body, err := protojson.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: marshal inference dispatch request: %v", constants.ErrRequestMarshalFailed, err)
	}
	status, respBody, err := c.doInference(ctx, c.inferenceHTTP(), c.appWorkloadPersona(), http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.InferenceDispatch, body, nil)
	if err != nil {
		return nil, respBody, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, respBody, inferenceDispatchFailure(status, respBody)
	}
	response := &operatorv1.InferenceDispatchResponse{}
	if err := protojson.Unmarshal(respBody, response); err != nil {
		return nil, respBody, fmt.Errorf("%w: decode inference dispatch response: %v", constants.ErrInvalidJSONResponse, err)
	}
	return response, respBody, nil
}

// DispatchIntent sends POST /api/v1/operators/intents under the configured
// platform-enrolled application identity, as DispatchInference does: the
// Gateway's Tribunal dispatches every member call as governed inference under
// that identity, then the approved arguments as a governed MCP_CALL
// run_shell_command. A
// deliberated intent is always a TribunalIntentResult. A request refused before
// deliberation is an ErrHTTPStatusError whose text names the IngressRefusal's
// error and code; the typed IngressRefusal is in the returned response body.
func (c *Client) DispatchIntent(ctx context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, []byte, error) {
	if req == nil {
		return nil, nil, constants.ErrMissingRequiredField
	}
	body, err := protojson.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: marshal tribunal intent request: %v", constants.ErrRequestMarshalFailed, err)
	}
	// Every member call may run to the inference deadline, and the approved
	// command may wait for a human decision before its own dispatch deadline.
	httpClient := &http.Client{
		Timeout:   time.Duration(tribunal.MaxMemberCalls)*dispatch.RequestDeadline + mcp.ApprovalRequestTTL + dispatch.RequestDeadline,
		Transport: c.http.Transport,
	}
	status, respBody, err := c.doInference(ctx, httpClient, c.appWorkloadPersona(), http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.OperatorsIntents, body, nil)
	if err != nil {
		return nil, respBody, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		_, refusal, decodeErr := governance.DecodeIngressFailure(respBody)
		if decodeErr != nil || refusal == nil {
			return nil, respBody, fmt.Errorf("%w: tribunal intent returned status %d", constants.ErrHTTPStatusError, status)
		}
		return nil, respBody, fmt.Errorf("%w: tribunal intent returned status %d: %s (%s)", constants.ErrHTTPStatusError, status, refusal.GetError(), refusal.GetCode())
	}
	result := &tribunalv1.TribunalIntentResult{}
	if err := protojson.Unmarshal(respBody, result); err != nil {
		return nil, respBody, fmt.Errorf("%w: decode tribunal intent result: %v", constants.ErrInvalidJSONResponse, err)
	}
	return result, respBody, nil
}

// inferenceDispatchFailure maps a non-2xx dispatch response to its public
// sentinel through the typed failure code, keeping ErrHTTPStatusError in the
// chain. A body without a known code yields the status error alone. The
// Operator's signed FAILED event receipt, when the body carries one, rides with the
// error (governance.FailedEventReceipt), as do the observers' signed event receipts for
// the attempt (dispatch.ObservationEventReceipts), so evidence verification can
// claim their ledger commitments; they are unverified here and must be
// checked against the signing runtime's anchor before use.
func inferenceDispatchFailure(status int, body []byte) error {
	failure := &operatorv1.InferenceDispatchFailure{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, failure); err != nil {
		return fmt.Errorf("%w: inference dispatch returned status %d", constants.ErrHTTPStatusError, status)
	}
	err := fmt.Errorf("%w: inference dispatch returned status %d", constants.ErrHTTPStatusError, status)
	if sentinel := dispatch.FailureSentinel(failure.GetCode()); sentinel != nil {
		err = fmt.Errorf("%w: inference dispatch returned status %d: %w", constants.ErrHTTPStatusError, status, sentinel)
	}
	return dispatch.WithObservationEventReceipts(failure.GetObservationEventReceipts(), governance.WithFailedEventReceipt(failure.GetEventReceipt(), err))
}

// InferenceDispatchStreamResult carries the terminal governed response and any
// live progress events observed during a streaming dispatch.
type InferenceDispatchStreamResult struct {
	Progress   []*operatorv1.InferenceProgressEvent
	Completion *operatorv1.InferenceDispatchResponse
}

// DispatchInferenceStream sends POST /api/v1/inference/dispatch with stream=true
// and consumes NDJSON InferenceDispatchStreamFrame records until completion.
func (c *Client) DispatchInferenceStream(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*InferenceDispatchStreamResult, error) {
	if req == nil {
		return nil, constants.ErrMissingRequiredField
	}
	req.Stream = true
	body, err := protojson.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal inference dispatch request: %v", constants.ErrRequestMarshalFailed, err)
	}
	accept := constants.HeaderValueApplicationNDJSON
	status, respBody, err := c.doInference(ctx, c.inferenceHTTP(), c.appWorkloadPersona(), http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.InferenceDispatch, body, &accept)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: inference dispatch stream returned status %d", constants.ErrHTTPStatusError, status)
	}
	result := &InferenceDispatchStreamResult{}
	reader := bufio.NewReader(bytes.NewReader(respBody))
	unmarshal := protojson.UnmarshalOptions{DiscardUnknown: true}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return nil, fmt.Errorf("%w: read inference dispatch stream: %v", constants.ErrInvalidJSONResponse, readErr)
		}
		line = strings.TrimSpace(line)
		if line != "" {
			frame := &operatorv1.InferenceDispatchStreamFrame{}
			if err := unmarshal.Unmarshal([]byte(line), frame); err != nil {
				return nil, fmt.Errorf("%w: decode inference dispatch stream frame: %v", constants.ErrInvalidJSONResponse, err)
			}
			switch payload := frame.GetFrame().(type) {
			case *operatorv1.InferenceDispatchStreamFrame_Progress:
				if payload.Progress != nil {
					result.Progress = append(result.Progress, payload.Progress)
				}
			case *operatorv1.InferenceDispatchStreamFrame_Completion:
				result.Completion = payload.Completion
			case *operatorv1.InferenceDispatchStreamFrame_Failure:
				if sentinel := dispatch.FailureSentinel(payload.Failure.GetCode()); sentinel != nil {
					return nil, fmt.Errorf("inference dispatch stream failed: %w", sentinel)
				}
				if payload.Failure.GetReason() == "" {
					return nil, fmt.Errorf("%w: inference dispatch stream failed", constants.ErrInferenceProviderResponseInvalid)
				}
				return nil, fmt.Errorf("%w: %s", constants.ErrInferenceProviderResponseInvalid, payload.Failure.GetReason())
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if result.Completion == nil {
		return nil, fmt.Errorf("%w: inference dispatch stream missing completion", constants.ErrMissingRequiredField)
	}
	return result, nil
}

func (c *Client) inferenceHTTP() *http.Client {
	return &http.Client{
		Timeout:   mcp.ApprovalRequestTTL + dispatch.RequestDeadline,
		Transport: c.http.Transport,
	}
}

func (c *Client) doInference(ctx context.Context, hc *http.Client, p Persona, method, url string, body []byte, accept *string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", constants.HeaderValueApplicationJSON)
	if accept != nil {
		req.Header.Set("Accept", *accept)
	}
	if p.UserAgent != "" {
		req.Header.Set("User-Agent", p.UserAgent)
	}
	if p.OperatorSessionID != "" {
		req.Header.Set(constants.HeaderOperatorSessionID, p.OperatorSessionID)
	}
	start := time.Now()
	resp, err := hc.Do(req)
	ex := Exchange{Persona: p.ID, Method: method, URL: url, At: start}
	attachBody(&ex.ReqBody, &ex.ReqRaw, body)
	if err != nil {
		ex.Err = err.Error()
		ex.LatencyMS = time.Since(start).Milliseconds()
		c.append(ex, c.cfg.Verbose)
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	ex.Status = resp.StatusCode
	ex.LatencyMS = time.Since(start).Milliseconds()
	attachBody(&ex.RespBody, &ex.RespRaw, out)
	c.append(ex, c.cfg.Verbose)
	return resp.StatusCode, out, nil
}

func (c *Client) appWorkloadPersona() Persona {
	return Persona{ID: "eval-inference-probe", UserAgent: "ztd-eval-inference-probe"}
}
