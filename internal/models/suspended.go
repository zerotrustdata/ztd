// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"time"
)

// SuspendedTransaction represents a transaction awaiting L3 human approval.
type SuspendedTransaction struct {
	TransactionHash         string
	Envelope                json.RawMessage
	CreatedAt               time.Time
	ExpiresAt               time.Time
	ToolName                string
	ToolArguments           json.RawMessage
	UserID                  string
	OperatorID              string
	OperatorSessionID       string // Operator session the envelope targets (envelope.OperatorSessionId); owns the approval audit chain
	SubmitterCLISessionID   string // CLI session that submitted the transaction
	Approved                bool
	ApprovedAt              *time.Time
	ApprovedBy              string // CLI session ID or user ID of approver
	ApprovalSignature       string // Signature over transaction_hash by approver
	ExpectedCertFingerprint string // Expected mTLS cert fingerprint for verification
	ApprovalPublicKey       string // Approver public key as supplied in ApprovalProof; CLI approvals verify against the ECDSA P-256 mTLS certificate named by ExpectedCertFingerprint
	// ClaimedAt is set while an approved transaction is being executed. A claimed row is never
	// recorded as expired: its execution was attempted.
	ClaimedAt *time.Time
	// Passkey WebAuthn fields (present for all approvals going forward)
	PasskeyCredentialID      string // WebAuthn credential ID
	PasskeyClientDataJSON    string // WebAuthn clientDataJSON
	PasskeyAuthenticatorData string // WebAuthn authenticatorData
	PasskeySignature         string // WebAuthn signature
}

// ApprovalProof holds all fields needed to record an L3 approval.
// CLI fields (ECDSA P-256 signature and certificate fingerprint) are present for CLI approvals; passkey WebAuthn fields
// are present for all approvals going forward.
type ApprovalProof struct {
	ApprovedBy        string
	CliSignature      string
	CertFingerprint   string
	ApprovalPublicKey string
	CredentialID      string
	ClientDataJSON    string
	AuthenticatorData string
	Signature         string
}
