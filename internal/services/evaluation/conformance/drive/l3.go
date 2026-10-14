// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

const (
	// conformanceL3Origin is the origin the forged WebAuthn client data names. It
	// is not a registered relying party, so the assertion can never verify.
	conformanceL3Origin = "https://conformance.invalid"

	// Byte lengths of the random fields of the invalid L3 proof. The authenticator
	// data is 37 bytes (rpIdHash, flags and signCount), so the proof reaches the
	// signature check instead of failing a field-presence check.
	invalidL3CredentialBytes        = 16
	invalidL3AuthenticatorDataBytes = 37
	invalidL3SignatureBytes         = 64
)

// webAuthnClientData is the typed WebAuthn client data the invalid proof carries.
type webAuthnClientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

// L3Mutators is the set for rows 20 and 21: the L3 proof faults on DIRECT. L3 is
// not hashed, so no mutator here rehashes. Direct attaches L2 before the mutator
// runs, so these mutators never touch governance.l2.
//
// verifyL3Posture treats a proof as present when governance.l3.proof is non-nil.
// The invalid proof is present, so L4 reaches the notary verify step and fails
// it, which is L3_PROOF_INVALID under ratify and notary.
func L3Mutators() MutatorSet {
	return MutatorSet{
		cases.FaultL3Missing: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			if env.Governance != nil {
				env.Governance.L3 = nil
			}
			return nil
		}},
		cases.FaultL3Invalid: {Apply: applyInvalidL3Proof},
		// Row 21 uses the same proof: under doctrine and consensus L3 does not gate,
		// so the failed verify is audited and the envelope still executes.
		cases.FaultL3Garbage: {Apply: applyInvalidL3Proof},
	}
}

// applyInvalidL3Proof writes the invalid proof over the envelope's transaction hash.
func applyInvalidL3Proof(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
	proof, err := invalidL3Proof(env.TransactionHash)
	if err != nil {
		return fmt.Errorf("l3 proof: %w", err)
	}
	if env.Governance == nil {
		env.Governance = &commonv1.GovernanceMetadata{}
	}
	env.Governance.L3 = &commonv1.L3Metadata{Proof: proof}
	return nil
}

// invalidL3Proof builds a proof that is present and fails the passkey verify. The
// credential, authenticator data and signature are random; the client data is a
// well-formed WebAuthn get whose challenge is the transaction hash.
func invalidL3Proof(txHash string) (*commonv1.L3Proof, error) {
	credential, err := randomBase64URL(invalidL3CredentialBytes)
	if err != nil {
		return nil, fmt.Errorf("credential id: %w", err)
	}
	authenticatorData, err := randomBase64URL(invalidL3AuthenticatorDataBytes)
	if err != nil {
		return nil, fmt.Errorf("authenticator data: %w", err)
	}
	signature, err := randomBase64URL(invalidL3SignatureBytes)
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	clientData, err := json.Marshal(webAuthnClientData{
		Type:      "webauthn.get",
		Challenge: base64.RawURLEncoding.EncodeToString([]byte(txHash)),
		Origin:    conformanceL3Origin,
	})
	if err != nil {
		return nil, fmt.Errorf("client data: %w", err)
	}
	return &commonv1.L3Proof{
		ClientDataJson:    string(clientData),
		AuthenticatorData: authenticatorData,
		Signature:         signature,
		CredentialId:      credential,
	}, nil
}

// randomBase64URL returns n random bytes encoded as unpadded base64url, the
// encoding the WebAuthn verifier decodes.
func randomBase64URL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
