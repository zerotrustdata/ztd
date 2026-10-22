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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func (c *Client) GetEventReceipt(ctx context.Context, transactionID string, persona ...Persona) (*operatorv1.EventReceipt, []byte, error) {
	p := c.auditorPersona()
	if len(persona) > 0 {
		p = persona[0]
	}
	u := c.cfg.MTLSBaseURL + constants.APIPaths.AuditEventReceipts + "?tx_id=" + url.QueryEscape(transactionID)
	status, body, err := c.do(ctx, p, http.MethodGet, u, nil)
	if err != nil {
		return nil, body, err
	}
	if status == http.StatusNotFound {
		return nil, body, nil
	}
	if status >= 400 {
		return nil, body, fmt.Errorf("gateway returned status %d for transaction %s: %s", status, transactionID, string(body))
	}
	eventReceipt := &operatorv1.EventReceipt{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, eventReceipt); err != nil {
		return nil, body, fmt.Errorf("decode canonical action event_receipt: %w", err)
	}
	return eventReceipt, body, nil
}

// GetTrustedSignerPublicKey fetches the Ed25519 public key for a governance
// trusted signer by key ID from the Gateway's signer endpoint. It is used by
// the harness to independently verify event receipt and persistence attestation
// signatures outside the Gateway relay trust boundary. Returns
// constants.ErrTrustedSignerKeyNotFound when the gateway has no record of the
// requested key ID.
func (c *Client) GetTrustedSignerPublicKey(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	u := c.cfg.MTLSBaseURL + constants.APIPaths.GovernanceSignersByID + url.PathEscape(keyID)
	status, body, err := c.do(ctx, c.auditorPersona(), http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch trusted signer %s: %w", keyID, err)
	}
	if status == http.StatusNotFound {
		return nil, constants.ErrTrustedSignerKeyNotFound
	}
	if status >= 400 {
		return nil, fmt.Errorf("gateway returned status %d for signer %s: %s", status, keyID, string(body))
	}

	var doc struct {
		ID        string `json:"id"`
		PublicKey string `json:"public_key_hex"`
		Enabled   bool   `json:"enabled"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode trusted signer response: %w", err)
	}
	if doc.ID == "" || !doc.Enabled || doc.PublicKey == "" {
		return nil, constants.ErrTrustedSignerKeyNotFound
	}
	pubBytes, err := hex.DecodeString(doc.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode signer public key hex: %w", err)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: invalid public key size: %d", constants.ErrTrustedSignerKeyNotFound, len(pubBytes))
	}
	return ed25519.PublicKey(pubBytes), nil
}

// auditorPersona builds a persona carrying the config's CLI session and user
// identity. The CLI session reaches the Gateway only when the Operator identity is
// the CLI identity (see do); on an Operator certificate it is never sent.
func (c *Client) auditorPersona() Persona {
	return Persona{
		ID:           "agent-harness-auditor",
		CLISessionID: c.cfg.CLISessionID,
		UserID:       c.cfg.UserID,
	}
}

// DiscoverOperator resolves one active operator identity from the canonical registry.
// Explicit ID/session constraints must match the same record. Without constraints,
// only data workers qualify; an ambiguous target must be selected explicitly.
func (c *Client) DiscoverOperator(ctx context.Context) (string, string, error) {
	operators, _, err := c.ListOperators(ctx)
	if err != nil {
		return "", "", fmt.Errorf("discover operator: %w", err)
	}
	var matches []*operatorv1.OperatorDocument
	for _, op := range operators {
		if op.GetId() == "" || op.GetOperatorSessionId() == "" || op.GetStatus() != string(constants.OperatorStatusActive) {
			continue
		}
		if c.cfg.OperatorID != "" && op.GetId() != c.cfg.OperatorID {
			continue
		}
		if c.cfg.OperatorSessionID != "" && op.GetOperatorSessionId() != c.cfg.OperatorSessionID {
			continue
		}
		if c.cfg.OperatorID == "" && c.cfg.OperatorSessionID == "" && !operatorcapability.IsDataOperator(op) {
			continue
		}
		matches = append(matches, op)
	}
	if c.cfg.OperatorID == "" && c.cfg.OperatorSessionID == "" {
		matches = operatorcapability.PreferEmbeddedDataOperator(matches)
	}
	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("%w: operator id=%q session=%q", constants.ErrEvaluationTargetUnavailable, c.cfg.OperatorID, c.cfg.OperatorSessionID)
	case 1:
		return matches[0].GetId(), matches[0].GetOperatorSessionId(), nil
	default:
		return "", "", fmt.Errorf("%w: %d active operators; specify --operator-id or --operator-session", constants.ErrEvaluationTargetAmbiguous, len(matches))
	}
}
