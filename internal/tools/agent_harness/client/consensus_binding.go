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

// ConsensusBinding reads the current binding through the authenticated owner route.
func (c *Client) ConsensusBinding(ctx context.Context) (*models.ConsensusPolicy, error) {
	status, raw, err := c.do(ctx, c.auditorPersona(), http.MethodGet, c.cfg.MTLSBaseURL+constants.APIPaths.AuthPlatformEnrollmentConsensus, nil)
	if err != nil {
		return nil, fmt.Errorf("read consensus binding: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("read consensus binding: status %d: %w", status, constants.ErrHTTPStatusError)
	}
	var binding models.ConsensusPolicy
	if err := json.Unmarshal(raw, &binding); err != nil {
		return nil, fmt.Errorf("decode consensus binding: %w", constants.ErrInvalidJSONResponse)
	}
	return &binding, nil
}
