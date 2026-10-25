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

	// REAL ztd packages — not forks, so the wire format matches the verifier.
	"github.com/zerotrustdata/ztd/internal/constants"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Canonical GovernanceEnvelope action types. SCREAMING_SNAKE per protocol/constants. Pinned
// here so Agent Harness has no internal/ import; verify against MapEventTypeToActionType.
const (
	ActionMcpCall   = "MCP_CALL"
	ProtocolVersion = "2"
)

// SubmitEnvelope submits a pre-built GovernanceEnvelope directly to the admission API.
// This is used when the caller has already constructed the envelope (e.g., from fixtures).
func (c *Client) SubmitEnvelope(ctx context.Context, p Persona, envelope *commonv1.GovernanceEnvelope) (*GovernedOutcome, error) {
	wire, err := protojson.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrPubSubMarshalEnvelope, err)
	}
	return c.SubmitEnvelopeBody(ctx, p, wire)
}

// SubmitEnvelopeBody posts body to the direct envelope ingress unchanged and
// classifies the answer. It performs no validation: conformance cases use it to
// send malformed requests.
func (c *Client) SubmitEnvelopeBody(ctx context.Context, p Persona, body []byte) (*GovernedOutcome, error) {
	status, raw, err := c.do(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.GovernanceEnvelopes, body)
	if err != nil {
		return nil, fmt.Errorf("agent harness: submit governance envelope: %w", err)
	}
	return decodeHTTPOutcome(status, raw, &operatorv1.EventReceipt{}), nil
}
