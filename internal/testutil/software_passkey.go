// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/models"
)

// SoftwarePasskeyOrigin is the browser origin the software authenticator
// reports. The Gateway accepts it for the "localhost" RP ID.
const SoftwarePasskeyOrigin = "https://localhost"

// SoftwarePasskey is an in-memory ES256 WebAuthn authenticator. It produces
// real assertions that the Gateway's WebAuthn verifier checks end to end:
// RP ID hash, user-verified flag, challenge, origin, and signature.
type SoftwarePasskey struct {
	CredentialID []byte

	key       *ecdsa.PrivateKey
	signCount uint32
}

// NewSoftwarePasskey generates a fresh P-256 credential.
func NewSoftwarePasskey(t *testing.T) *SoftwarePasskey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	credentialID := make([]byte, 16)
	_, err = rand.Read(credentialID)
	require.NoError(t, err)
	return &SoftwarePasskey{CredentialID: credentialID, key: key}
}

// Credential is the stored form of this passkey, with its public key COSE-encoded.
func (p *SoftwarePasskey) Credential(t *testing.T) models.PasskeyCredential {
	t.Helper()
	publicKey, err := p.key.PublicKey.ECDH()
	require.NoError(t, err)
	point := publicKey.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType:   int64(webauthncose.EllipticKey),
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: point[1:33],
		YCoord: point[33:65],
	})
	require.NoError(t, err)
	return models.PasskeyCredential{
		ID:              p.CredentialID,
		PublicKey:       cose,
		AttestationType: "none",
		Authenticator: models.Authenticator{
			AAGUID: make([]byte, 16),
		},
		CreatedAtUnixMs: time.Now().UnixMilli(),
	}
}

// Assert signs a user-verified webauthn.get assertion over challenge for rpID.
func (p *SoftwarePasskey) Assert(t *testing.T, rpID string, challenge protocol.URLEncodedBase64) models.WebAuthnAssertionResponse {
	t.Helper()
	clientData, err := json.Marshal(protocol.CollectedClientData{
		Type:      protocol.AssertCeremony,
		Challenge: base64.RawURLEncoding.EncodeToString(challenge),
		Origin:    SoftwarePasskeyOrigin,
	})
	require.NoError(t, err)

	p.signCount++
	rpIDHash := sha256.Sum256([]byte(rpID))
	authData := make([]byte, 0, 37)
	authData = append(authData, rpIDHash[:]...)
	authData = append(authData, byte(protocol.FlagUserPresent|protocol.FlagUserVerified))
	authData = binary.BigEndian.AppendUint32(authData, p.signCount)

	clientDataHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientDataHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	require.NoError(t, err)

	credentialID := base64.RawURLEncoding.EncodeToString(p.CredentialID)
	return models.WebAuthnAssertionResponse{
		ID:                credentialID,
		RawID:             credentialID,
		ClientDataJSON:    base64.RawURLEncoding.EncodeToString(clientData),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(authData),
		Signature:         base64.RawURLEncoding.EncodeToString(signature),
	}
}
