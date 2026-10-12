// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"crypto/ed25519"
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

// VoterPool is Agent Harness's L2 consensus voter pool: N agents that each
// vote on a transaction hash. When memberKeys is populated (per-member seed
// mode), each vote is signed with the member's own Ed25519 private key and
// the corresponding public key is registered as a trusted signer for that
// member. When memberKeys is nil (single-key mode), all votes share one
// signature from the pool key.
type VoterPool struct {
	KeyID        string
	ConsensusID  string
	MemberKeyIDs []string // when set, Vote produces one vote per member key ID
	priv         ed25519.PrivateKey
	pub          ed25519.PublicKey
	agents       []string
	memberKeys   map[string]ed25519.PrivateKey // per-member private keys (optional)
	memberPubs   map[string]ed25519.PublicKey  // per-member public keys (optional)
}

// MemberPubHex returns the hex-encoded public key for a specific member.
// Returns empty string if the member is not found or per-member keys are not
// in use.
func (e *VoterPool) MemberPubHex(appID string) string {
	if e.memberPubs == nil {
		return ""
	}
	pub, ok := e.memberPubs[appID]
	if !ok {
		return ""
	}
	return hex.EncodeToString(pub)
}

// HasPerMemberKeys returns true when the pool uses distinct per-member
// keys rather than a single shared key.
func (e *VoterPool) HasPerMemberKeys() bool {
	return len(e.memberKeys) > 0
}

// PubHex is the consensus public key for trusted-signer registration.
func (e *VoterPool) PubHex() string { return hex.EncodeToString(e.pub) }

// SeedHex returns the hex-encoded Ed25519 seed for persistence (demo bootstrap).
func (e *VoterPool) SeedHex() string { return hex.EncodeToString(e.priv.Seed()) }

// AgentCount is the number of mock voters in the pool.
func (e *VoterPool) AgentCount() int { return len(e.agents) }

// Vote produces the L2 metadata for a decision over the transaction hash.
// decision==true means "the pool agreed this mutation is safe."
// When per-member keys are in use, each vote is signed with the member's own
// private key. Otherwise, all votes share the pool's single signature.
func (e *VoterPool) Vote(txHash string, decision bool) *commonv1.L2Metadata {
	basis := fmt.Sprintf("%s|%v", txHash, decision) // matches l2_consensus.SignDecision

	var votes []*commonv1.L2Vote
	if len(e.MemberKeyIDs) > 0 {
		votes = make([]*commonv1.L2Vote, 0, len(e.MemberKeyIDs))
		for _, keyID := range e.MemberKeyIDs {
			var sigHex string
			if e.memberKeys != nil {
				if mk, ok := e.memberKeys[keyID]; ok {
					sig := ed25519.Sign(mk, []byte(basis))
					sigHex = hex.EncodeToString(sig)
				}
			} else {
				sig := ed25519.Sign(e.priv, []byte(basis))
				sigHex = hex.EncodeToString(sig)
			}
			votes = append(votes, &commonv1.L2Vote{
				SignerKeyId:        keyID,
				ConsensusSignature: sigHex,
				Decision:           decision,
			})
		}
	} else {
		sig := ed25519.Sign(e.priv, []byte(basis))
		sigHex := hex.EncodeToString(sig)
		votes = []*commonv1.L2Vote{{
			SignerKeyId:        e.KeyID,
			ConsensusSignature: sigHex,
			Decision:           decision,
		}}
	}

	return &commonv1.L2Metadata{
		ConsensusSetId: e.ConsensusID,
		Votes:          votes,
	}
}

// MaximalEnvelope is the input for an official governance envelope submission.
type MaximalEnvelope struct {
	OperatorID        string
	OperatorSessionID string
	ToolName          string
	ArgumentsJSON     string
	TargetResource    string
	StateRoot         string
	VoterPool         *VoterPool // attach L2 when non-nil
	TTL               time.Duration
}

// SubmitEnvelope submits a pre-built GovernanceEnvelope directly to the admission API.
// This is used when the caller has already constructed the envelope (e.g., from fixtures).
func (c *Client) SubmitEnvelope(ctx context.Context, p Persona, envelope *commonv1.GovernanceEnvelope) (status int, body []byte, err error) {
	wire, err := protojson.Marshal(envelope)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", constants.ErrPubSubMarshalEnvelope, err)
	}

	status, body, err = c.do(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.GovernanceEnvelopes, wire)
	return status, body, err
}

// SubmitMaximal builds a real GovernanceEnvelope wrapping an MCP_CALL, computes the
// canonical transaction hash with ztd's own hasher, attaches L2 (and optionally
// mock L3), marshals it as protojson, and POSTs it to the admission API.
// Returns the computed hash so callers can drive the OOB approve flow.
func (c *Client) SubmitMaximal(ctx context.Context, p Persona, m MaximalEnvelope) (txHash string, status int, body []byte, err error) {
	// 1. Typed protobuf payload — the "what". The Operator decodes this exact type.
	call := &operatorv1.McpCallRequested{
		ToolName:      m.ToolName,
		ArgumentsJson: m.ArgumentsJSON,
		ExecutionId:   fmt.Sprintf("agent-harness-%d", time.Now().UnixNano()),
	}
	payloadBytes, err := proto.Marshal(call)
	if err != nil {
		return "", 0, nil, fmt.Errorf("%w: %w", constants.ErrRequestMarshalFailed, err)
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
		ActionType:        ActionMcpCall,
		TargetResource:    m.TargetResource,
		EventType:         "operator.mcp.call.requested",
		Payload:           payloadBytes,
		IntentData:        intent,
		StateMerkleRoot:   m.StateRoot,
		Nonce:             hex.EncodeToString(nonce),
	}

	// 3. Canonical hash — REAL hasher. id == transaction_hash == SHA256(canonical).
	txHash, err = governance.GenerateMessageID(env)
	if err != nil {
		return "", 0, nil, fmt.Errorf("%w: %w", constants.ErrTxTransactionHashMissing, err)
	}
	env.Id = txHash
	env.TransactionHash = txHash

	// 4. Governance proofs over the hash.
	env.Governance = &commonv1.GovernanceMetadata{L1: &commonv1.L1Metadata{Validated: true}}
	if m.VoterPool != nil {
		env.Governance.L2 = m.VoterPool.Vote(txHash, true)
	}

	// 5. protojson is the canonical client-facing wire format (NOT encoding/json).
	wire, err := protojson.Marshal(env)
	if err != nil {
		return txHash, 0, nil, fmt.Errorf("%w: %w", constants.ErrPubSubMarshalEnvelope, err)
	}

	status, body, err = c.do(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.GovernanceEnvelopes, wire)
	return txHash, status, body, err
}
