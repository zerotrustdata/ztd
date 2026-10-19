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

// postObserveProducer posts a typed producer request to path. The app persona
// attaches no CLI session header, so the Gateway authenticates the call as an
// app workload.
func (c *Client) postObserveProducer(ctx context.Context, path string, req any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%w: marshal observe producer request: %w", constants.ErrRequestMarshalFailed, err)
	}
	status, _, err := c.do(ctx, c.appWorkloadPersona(), http.MethodPost, c.cfg.MTLSBaseURL+path, body)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("%w: observe producer %s returned status %d", constants.ErrHTTPStatusError, path, status)
	}
	return nil
}
