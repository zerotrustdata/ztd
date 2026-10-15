// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// Deliberate posts a final envelope to the Gateway deliberation endpoint and
// returns the L2 proof it attached. Any non-200 answer is an error wrapping
// constants.ErrIngressL2DeliberationFailed.
func (c *Client) Deliberate(ctx context.Context, p Persona, env *commonv1.GovernanceEnvelope) (*commonv1.L2Metadata, error) {
	wire, err := protojson.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("deliberate: marshal envelope: %w", err)
	}
	status, raw, err := c.do(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.ConsensusDeliberate, wire)
	if err != nil {
		return nil, fmt.Errorf("deliberate: post envelope: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("deliberate: status %d: %w", status, constants.ErrIngressL2DeliberationFailed)
	}

	var signed commonv1.GovernanceEnvelope
	if err := protojson.Unmarshal(raw, &signed); err != nil {
		return nil, fmt.Errorf("deliberate: decode envelope: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
	}
	if signed.GetTransactionHash() != env.GetTransactionHash() {
		return nil, fmt.Errorf("deliberate: proof is for transaction %q, sent %q: %w",
			signed.GetTransactionHash(), env.GetTransactionHash(), constants.ErrIngressL2DeliberationFailed)
	}
	l2 := signed.GetGovernance().GetL2()
	if len(l2.GetVotes()) == 0 {
		return nil, fmt.Errorf("deliberate: envelope carries no L2 votes: %w", constants.ErrIngressL2DeliberationFailed)
	}
	return l2, nil
}
