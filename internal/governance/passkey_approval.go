// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// ParsePasskeyAssertion is the shared flat-to-WebAuthn assertion boundary.
func ParsePasskeyAssertion(assertion *models.WebAuthnAssertionResponse) (*protocol.ParsedCredentialAssertionData, error) {
	if assertion == nil {
		return nil, constants.ErrMissingRequiredField
	}
	body, err := json.Marshal(models.ParsedAssertionResponse{
		ID: assertion.ID, RawID: assertion.RawID, Type: string(protocol.PublicKeyCredentialType),
		Response: models.ParsedAssertionResponseBody{
			ClientDataJSON: assertion.ClientDataJSON, AuthenticatorData: assertion.AuthenticatorData,
			Signature: assertion.Signature, UserHandle: assertion.UserHandle,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal assertion response: %w", err)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return nil, fmt.Errorf("parse credential assertion: %w", err)
	}
	return parsed, nil
}

// VerifyPasskeyApproval verifies an assertion using registration facts supplied
// by the authenticated Gateway. Counter/clone detection remains Gateway-owned.
func VerifyPasskeyApproval(userID, transactionHash string, proof *commonv1.L3Proof) error {
	if err := validateTransactionHash(transactionHash); err != nil {
		return err
	}
	c := proof.GetPasskeyContext()
	if proof == nil || c == nil || userID == "" || c.UserId != userID || c.CredentialId == "" || c.CredentialId != proof.CredentialId ||
		len(c.PublicKeyCose) == 0 || c.RpId == "" || len(c.AllowedOrigins) == 0 ||
		proof.CliSignature != "" || len(proof.CliCertificateDer) != 0 || len(proof.CliIntermediatesDer) != 0 {
		return constants.ErrTxL3ProofInvalid
	}
	parsed, err := ParsePasskeyAssertion(&models.WebAuthnAssertionResponse{
		ID: proof.CredentialId, RawID: proof.CredentialId, ClientDataJSON: proof.ClientDataJson,
		AuthenticatorData: proof.AuthenticatorData, Signature: proof.Signature,
	})
	if err != nil {
		return fmt.Errorf("parse passkey approval: %w: %w", constants.ErrTxL3ProofInvalid, err)
	}
	if err := parsed.Verify(base64.RawURLEncoding.EncodeToString([]byte(transactionHash)), c.RpId, "", c.AllowedOrigins, nil, nil,
		protocol.TopOriginExplicitVerificationMode, false, true, true, c.PublicKeyCose, protocol.SignaturePolicy{}); err != nil {
		return fmt.Errorf("verify passkey approval: %w: %w", constants.ErrTxL3ProofInvalid, err)
	}
	return nil
}
