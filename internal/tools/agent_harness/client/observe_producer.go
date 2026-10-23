// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// RequestRunStop posts a run-scoped stop request to the mTLS producer endpoint
// POST /api/v1/observe/producer/stop under the app workload identity. The
// Gateway routes the resulting ai.eval.stop.requested event to the CLI session
// named in the request. A non-2xx response is returned as an error.
func (c *Client) RequestRunStop(ctx context.Context, req models.ObserveProducerStopRequest) error {
	return c.postObserveProducer(ctx, constants.APIPaths.ObserveProducerStop, req)
}

// UpdateRunState posts a run lifecycle update to the mTLS producer endpoint
// POST /api/v1/observe/producer/run-state under the app workload identity. The
// Gateway emits app.run.status.updated to the CLI session named in the request.
// A non-2xx response is returned as an error.
func (c *Client) UpdateRunState(ctx context.Context, req models.ObserveProducerRunStateRequest) error {
	return c.postObserveProducer(ctx, constants.APIPaths.ObserveProducerRunState, req)
}

// ObserveProducerRejectedError is a producer request the Gateway answered with
// a non-2xx status and a typed rejection. It unwraps to
// constants.ErrHTTPStatusError and to the rejection's cause, so callers match
// the sentinel or errors.As the refused transition or stale update.
type ObserveProducerRejectedError struct {
	Path      string
	Status    int
	Rejection models.ObserveProducerRejection
}

func (e *ObserveProducerRejectedError) Error() string {
	return fmt.Sprintf("%s: observe producer %s returned status %d: %s", constants.ErrHTTPStatusError, e.Path, e.Status, e.Rejection.Message)
}

func (e *ObserveProducerRejectedError) Unwrap() []error {
	return []error{constants.ErrHTTPStatusError, e.Rejection.Cause()}
}

// postObserveProducer posts a typed producer request to path. The app persona
// attaches no CLI session header, so the Gateway authenticates the call as an
// app workload. A non-2xx response must carry a typed rejection body.
func (c *Client) postObserveProducer(ctx context.Context, path string, req any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%w: marshal observe producer request: %w", constants.ErrRequestMarshalFailed, err)
	}
	status, respBody, err := c.do(ctx, c.appWorkloadPersona(), http.MethodPost, c.cfg.MTLSBaseURL+path, body)
	if err != nil {
		return err
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	var rejection models.ObserveProducerRejection
	if err := json.Unmarshal(respBody, &rejection); err != nil {
		return fmt.Errorf("%w: observe producer %s returned status %d: %w: %w", constants.ErrHTTPStatusError, path, status, constants.ErrInvalidJSONResponse, err)
	}
	return &ObserveProducerRejectedError{Path: path, Status: status, Rejection: rejection}
}
