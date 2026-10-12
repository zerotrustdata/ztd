// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import "encoding/json"

// OperatorBootstrapFrameType names one frame on the Operator bootstrap
// websocket. The exchange is fixed:
//
//	Operator -> Gateway  request   (token, enrollment request, runtime config)
//	Gateway  -> Operator created   (request ID, fingerprints, approval URL)
//	Operator -> Gateway  complete  (proof-of-possession, sent at once)
//	Gateway  -> Operator decision  (the owner's committed decision)
//	Gateway  -> Operator bundle    (the PEM bundle, delivered once)
//
// The proofs sign the request ID, so they follow created; the Gateway holds
// them through the decision and issues as soon as approval commits. The
// Gateway closes the socket after bundle or error. A dropped socket is
// redialed with the same in-memory token, keys, and CSRs; the Gateway
// resumes the request the token owns, in any state.
type OperatorBootstrapFrameType string

const (
	OperatorBootstrapFrameRequest  OperatorBootstrapFrameType = "request"
	OperatorBootstrapFrameCreated  OperatorBootstrapFrameType = "created"
	OperatorBootstrapFrameDecision OperatorBootstrapFrameType = "decision"
	OperatorBootstrapFrameComplete OperatorBootstrapFrameType = "complete"
	OperatorBootstrapFrameBundle   OperatorBootstrapFrameType = "bundle"
	OperatorBootstrapFrameError    OperatorBootstrapFrameType = "error"
)

// OperatorBootstrapFrame is one JSON text frame on the bootstrap websocket.
// Exactly the field named by Type is set.
type OperatorBootstrapFrame struct {
	Type     OperatorBootstrapFrameType        `json:"type"`
	Request  *OperatorBootstrapRequest         `json:"request,omitempty"`
	Created  *PlatformEnrollmentCreateResponse `json:"created,omitempty"`
	Decision *PlatformEnrollmentStatusResponse `json:"decision,omitempty"`
	Complete *PlatformEnrollmentProofs         `json:"complete,omitempty"`
	Bundle   *OperatorBootstrapBundle          `json:"bundle,omitempty"`
	Error    *OperatorBootstrapError           `json:"error,omitempty"`
}

// OperatorBootstrapRequest opens the exchange. Token is the requester's raw
// token; Enrollment.TokenHash must be its hash. The token scopes the whole
// socket, so a redial resumes the request it owns. RuntimeConfig is the
// canonical JSON OperatorRuntimeConfig (MarshalOperatorRuntimeConfig) the
// Gateway records for the issued Operator.
type OperatorBootstrapRequest struct {
	Token         string                          `json:"token"`
	Enrollment    PlatformEnrollmentCreateRequest `json:"enrollment"`
	RuntimeConfig json.RawMessage                 `json:"runtime_config"`
}

// OperatorBootstrapBundle is the Operator's whole identity and runtime grant.
// The Operator holds it in memory only; a restart enrolls again. L2Trust is
// nil when the Gateway has no consensus policy configured.
type OperatorBootstrapBundle struct {
	Credentials        PlatformEnrollmentOperatorCredentials `json:"credentials"`
	MaxConcurrentTasks int                                   `json:"max_concurrent_tasks"`
	MaxMemoryMB        int                                   `json:"max_memory_mb"`
	L2Trust            *OperatorL2Trust                      `json:"l2_trust,omitempty"`
}

// OperatorL2Trust is the L2 consensus policy the Gateway deliberates under and
// the public key of each member the Gateway still trusts. The Operator's L4
// Warden verifies L2 votes against it; a member with no key here cannot count
// toward quorum.
type OperatorL2Trust struct {
	ConsensusSetID  string           `json:"consensus_set_id"`
	MemberKeyIDs    []string         `json:"member_key_ids"`
	Quorum          int              `json:"quorum"`
	RequireDistinct bool             `json:"require_distinct"`
	Enabled         bool             `json:"enabled"`
	Signers         []OperatorSigner `json:"signers"`
}

// OperatorSigner is one trusted L2 signer: its key ID and hex-encoded raw
// Ed25519 public key.
type OperatorSigner struct {
	KeyID        string `json:"key_id"`
	PublicKeyHex string `json:"public_key_hex"`
}

// OperatorBootstrapError ends the exchange. Retryable tells the Operator to
// redial with the same in-memory request; otherwise enrollment has failed.
type OperatorBootstrapError struct {
	Retryable bool   `json:"retryable"`
	Message   string `json:"message"`
}
