// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	// REAL ztd packages — not forks. This is what guarantees the transaction
	// hash and wire format match the verifier. GovernanceEnvelope is the alias for
	// ztd.common.v1.GovernanceEnvelope; GenerateMessageID is the canonical hasher.

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Canonical GovernanceEnvelope action types. SCREAMING_SNAKE per protocol/constants. Pinned
// here so Agent Harness has no internal/ import; verify against MapEventTypeToActionType.
const (
	ActionMcpCall   = "MCP_CALL"
	ActionA2aCall   = "A2A_CALL"
	ProtocolVersion = "2"
)

// MaximalEnvelope is the input for an official governance envelope submission.
type MaximalEnvelope struct {
	OperatorID        string
	OperatorSessionID string
	ToolName          string
	ArgumentsJSON     string
	TargetResource    string
	StateRoot         string
	TTL               time.Duration
}

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
	return decodeHTTPOutcome(status, raw, &operatorv1.ActionReceipt{}), nil
}

// buildMaximalEnvelope builds the signed-ready GovernanceEnvelope for an MCP_CALL
// and returns it with its canonical transaction hash.
func buildMaximalEnvelope(p Persona, m MaximalEnvelope) (*governance.GovernanceEnvelope, string, error) {
	// 1. Typed protobuf payload — the "what". The Operator decodes this exact type.
	call := &operatorv1.McpCallRequested{
		ToolName:      m.ToolName,
		ArgumentsJson: m.ArgumentsJSON,
		ExecutionId:   fmt.Sprintf("agent-harness-%d", time.Now().UnixNano()),
	}
	payloadBytes, err := proto.Marshal(call)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", constants.ErrRequestMarshalFailed, err)
	}

	// JSON-first mirror of intent for consumers that read intent_data.
	intent, _ := structpb.NewStruct(map[string]any{
		"payload_type": "McpCallRequested",
		"tool_name":    m.ToolName,
		"arguments":    m.ArgumentsJSON,
	})

	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)

	ttl := m.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}

	// 2. The envelope — the "how". GovernanceEnvelope is the canonical type.
	env := &governance.GovernanceEnvelope{
		ProtocolVersion:   ProtocolVersion,
		Timestamp:         timestamppb.Now(),
		ExpiresAt:         timestamppb.New(time.Now().Add(ttl)),
		SourceComponent:   commonv1.Component_COMPONENT_CLIENT,
		OperatorId:        m.OperatorID,
		OperatorSessionId: m.OperatorSessionID,
		RequestorUserId:   p.UserID,
		ActionType:        ActionMcpCall,
		TargetResource:    m.TargetResource,
		EventType:         string(constants.EventOperatorMcpCallRequested),
		Payload:           payloadBytes,
		IntentData:        intent,
		StateMerkleRoot:   m.StateRoot,
		Nonce:             hex.EncodeToString(nonce),
	}

	// 3. Canonical hash — REAL hasher. id == transaction_hash == SHA256(canonical).
	txHash, err := governance.GenerateMessageID(env)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", constants.ErrTxTransactionHashMissing, err)
	}
	env.Id = txHash
	env.TransactionHash = txHash

	// 4. Record doctrine validation; required L2/L3 proofs are verified at ingress.
	env.Governance = &commonv1.GovernanceMetadata{L1: &commonv1.L1Metadata{Validated: true}}
	return env, txHash, nil
}

// SubmitMaximal builds a real GovernanceEnvelope wrapping an MCP_CALL, computes the
// canonical transaction hash with ztd's own hasher, marshals it as protojson,
// and POSTs it to the admission API.
// Returns the computed hash so callers can drive the OOB approve flow.
func (c *Client) SubmitMaximal(ctx context.Context, p Persona, m MaximalEnvelope) (string, *GovernedOutcome, error) {
	env, txHash, err := buildMaximalEnvelope(p, m)
	if err != nil {
		return "", nil, err
	}

	// protojson is the canonical client-facing wire format (NOT encoding/json).
	wire, err := protojson.Marshal(env)
	if err != nil {
		return txHash, nil, fmt.Errorf("%w: %w", constants.ErrPubSubMarshalEnvelope, err)
	}

	outcome, err := c.SubmitEnvelopeBody(ctx, p, wire)
	return txHash, outcome, err
}
