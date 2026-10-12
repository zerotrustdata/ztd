// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

// OperatorRenewalFrameType names one frame on an Operator's renewal pub/sub
// channel (pubsub.RenewalChannel). The exchange is:
//
//	Operator -> Gateway  request  (a CSR for a fresh key)
//	Gateway  -> Operator renewed  (the new certificate and the current L2 trust)
//	Gateway  -> Operator error    (the request was refused)
//
// The Gateway intercepts every request and never fans it out; its reply is
// published on the same channel and carries the request's RequestID. The
// Operator publishes only over its mTLS connection, so the renewed
// certificate names the same organization, Operator, and session as the one
// that authenticated the request.
type OperatorRenewalFrameType string

const (
	OperatorRenewalFrameRequest OperatorRenewalFrameType = "request"
	OperatorRenewalFrameRenewed OperatorRenewalFrameType = "renewed"
	OperatorRenewalFrameError   OperatorRenewalFrameType = "error"
)

// OperatorRenewalFrame is one JSON frame on the renewal channel. Exactly the
// field named by Type is set.
type OperatorRenewalFrame struct {
	Type      OperatorRenewalFrameType `json:"type"`
	RequestID string                   `json:"request_id"`
	Request   *OperatorRenewalRequest  `json:"request,omitempty"`
	Renewed   *OperatorRenewal         `json:"renewed,omitempty"`
	Error     *OperatorRenewalError    `json:"error,omitempty"`
}

// OperatorRenewalRequest carries the PEM CSR for the Operator's next key. The
// CSR names no subject alternative names: the Gateway sets the identity.
type OperatorRenewalRequest struct {
	CSR string `json:"csr"`
}

// OperatorRenewal is the renewed identity. Certificate is the PEM leaf and
// CertificateChain the PEM intermediates and root. L2Trust is the Gateway's
// L2 trust at renewal time, nil when it has no consensus policy; it replaces
// the trust the Operator holds.
type OperatorRenewal struct {
	Certificate      string           `json:"certificate"`
	CertificateChain string           `json:"certificate_chain"`
	L2Trust          *OperatorL2Trust `json:"l2_trust,omitempty"`
}

// OperatorRenewalError refuses one request. Retryable tells the Operator a
// later attempt may succeed; otherwise its identity cannot be renewed.
type OperatorRenewalError struct {
	Retryable bool   `json:"retryable"`
	Message   string `json:"message"`
}
