// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"errors"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/constants"
	approvals "github.com/zerotrustdata/ztd/internal/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

//go:generate mockery --name L3Notary --output ./mocks --dir .

// L3Notary provides L3 (Authorization) verification for human-in-the-loop approval.
// L3 is the final gate that requires human presence before mutations execute.
type L3Notary interface {
	// VerifyL3Proof verifies an L3 proof for a transaction.
	// Returns true if the proof is valid and the transaction should be allowed.
	VerifyL3Proof(ctx context.Context, userID, transactionHash, cliSessionID string, proof *commonv1.L3Proof) (bool, error)
}

// PasskeyVerifier verifies a WebAuthn passkey assertion bound to a transaction
// hash. It is the passkey-domain primitive composed by the gateway L3 notary;
// implementers need not implement L3Notary. Decoupling the passkey domain from
// the L3Notary interface shape means changes to L3Notary no longer ripple into
// passkey domain code.
type PasskeyVerifier interface {
	// VerifyPasskeyProof verifies a WebAuthn assertion against a registered
	// passkey. The challenge is the transaction hash. The cliSessionID parameter
	// is ignored for web sessions but is part of the primitive signature so the
	// composing notary can forward it without a separate adapter.
	VerifyPasskeyProof(ctx context.Context, userID, transactionHash, cliSessionID string, proof *commonv1.L3Proof) (bool, error)
}

// passkeyL3Notary adapts a PasskeyVerifier to the L3Notary interface. It is
// the bridge between the passkey domain primitive and the governance L3 layer;
// the gateway notary composes this so PasskeyService itself never implements
// L3Notary.
type passkeyL3Notary struct {
	verifier PasskeyVerifier
}

func (n *passkeyL3Notary) VerifyL3Proof(ctx context.Context, userID, transactionHash, cliSessionID string, proof *commonv1.L3Proof) (bool, error) {
	return n.verifier.VerifyPasskeyProof(ctx, userID, transactionHash, cliSessionID, proof)
}

// CLISessionVerifier performs CLI session-specific verification including user active
// status, session validity, and certificate revocation. Returns nil if verification passes.
// Returns constants.ErrCLISessionDenied for denials (revoked certs, inactive sessions) and
// other errors for system failures.
type CLISessionVerifier interface {
	VerifyCLISession(ctx context.Context, userID, cliSessionID, certFingerprint string) error
}

// gatewayNotary provides L3 verification for gateway mode using either a passkey
// assertion or a signature from an enrolled CLI certificate.
type gatewayNotary struct {
	cliVerifier     CLISessionVerifier
	passkeyVerifier L3Notary
	logger          *slog.Logger
}

// NewGatewayL3Notary creates a unified L3 notary accepting passkey assertions and
// enrolled CLI certificate signatures. The passkeyVerifier is the passkey-domain primitive
// (e.g. gateway.PasskeyService); it is wrapped in a passkeyL3Notary so it need not
// implement L3Notary itself.
func NewGatewayL3Notary(cliVerifier CLISessionVerifier, passkeyVerifier PasskeyVerifier, logger *slog.Logger) L3Notary {
	return &gatewayNotary{
		cliVerifier:     cliVerifier,
		passkeyVerifier: &passkeyL3Notary{verifier: passkeyVerifier},
		logger:          logger,
	}
}

// VerifyL3Proof verifies an L3 proof in gateway mode.
//
// Exactly one authorization mechanism is accepted: WebAuthn or an enrolled CLI
// certificate signature. The CLI certificate's SPIFFE SAN supplies the approving
// session identity; the envelope submitter session remains independent.
func (v *gatewayNotary) VerifyL3Proof(ctx context.Context, userID, transactionHash, cliSessionID string, proof *commonv1.L3Proof) (bool, error) {
	if proof == nil {
		return false, constants.ErrGatewayL3ProofRequired
	}

	isCLIApproval := proof.CliSignature != "" || len(proof.CliCertificateDer) != 0 || len(proof.CliIntermediatesDer) != 0
	if isCLIApproval {
		if proof.CredentialId != "" || proof.ClientDataJson != "" || proof.AuthenticatorData != "" || proof.Signature != "" || proof.PasskeyContext != nil {
			return false, nil
		}
		approvingSessionID, err := approvals.VerifyCLIApproval(userID, transactionHash, proof)
		if err != nil || v.cliVerifier == nil {
			return false, nil
		}
		if err := v.cliVerifier.VerifyCLISession(ctx, userID, approvingSessionID, proof.MtlsCertFingerprint); err != nil {
			if errors.Is(err, constants.ErrCLISessionDenied) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}

	if proof.CredentialId == "" {
		return false, constants.ErrPasskeyProofRequired
	}
	ok, err := v.passkeyVerifier.VerifyL3Proof(ctx, userID, transactionHash, cliSessionID, proof)
	if err != nil || !ok {
		return false, err
	}

	// Layer 2: CLI mTLS session authentication (additional check for CLI callers)
	if proof.MtlsCertFingerprint != "" && v.cliVerifier != nil {
		if err := v.cliVerifier.VerifyCLISession(ctx, userID, cliSessionID, proof.MtlsCertFingerprint); err != nil {
			if errors.Is(err, constants.ErrCLISessionDenied) {
				return false, nil
			}
			return false, err
		}
	}

	return true, nil
}
