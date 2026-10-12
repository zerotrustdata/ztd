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
	"errors"
	"fmt"
	"net/http"
	"net/url"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
)

// DiscoverRemoteOperator lists the authenticated user's Operators through
// GET /api/v1/operators and selects the single active data Operator session
// (see operatorcapability.SelectDataOperator). Zero matches return
// ErrEvaluationTargetUnavailable and multiple matches return
// ErrEvaluationTargetAmbiguous.
func (c *Client) DiscoverRemoteOperator(ctx context.Context) (*operatorv1.OperatorDocument, []byte, error) {
	if c.cfg.UserID == "" {
		return nil, nil, fmt.Errorf("%w: authenticated user id is required for operator discovery", constants.ErrMissingRequiredField)
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
	selected, err := operatorcapability.SelectDataOperator(response.Operators)
	if err != nil {
		sentinel := constants.ErrEvaluationTargetUnavailable
		if errors.Is(err, constants.ErrDataOperatorAmbiguous) {
			sentinel = constants.ErrEvaluationTargetAmbiguous
		}
		return nil, body, fmt.Errorf("%w: %w", sentinel, err)
	}
	for i := range response.Operators {
		if response.Operators[i].GetOperatorSessionId() == selected.OperatorSessionID {
			return response.Operators[i], body, nil
		}
	}
	return nil, body, fmt.Errorf("%w: selected session %s is missing from the operator list", constants.ErrEvaluationTargetUnavailable, selected.OperatorSessionID)
}
