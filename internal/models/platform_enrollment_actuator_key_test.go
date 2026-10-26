// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestDecodeActuatorPublicKey_AcceptsExactlyOneEd25519Key(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	got, err := DecodeActuatorPublicKey(hex.EncodeToString(pub))
	require.NoError(t, err)
	assert.Equal(t, pub, got)
}

func TestDecodeActuatorPublicKey_RejectsBadHexAndWrongLength(t *testing.T) {
	valid := hex.EncodeToString(make([]byte, ed25519.PublicKeySize))
	tests := []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "non-hex characters", key: "zz" + valid[2:]},
		{name: "odd hex length", key: valid[:len(valid)-1]},
		{name: "one byte short", key: valid[:len(valid)-2]},
		{name: "one byte long", key: valid + "00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeActuatorPublicKey(tt.key)
			assert.ErrorIs(t, err, constants.ErrOperatorActuatorKeyInvalid)
		})
	}
}

func TestPlatformOperatorCSRPayload_ActuatorKeyUsesPresentedKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	payload := &PlatformOperatorCSRPayload{ActuatorPublicKey: hex.EncodeToString(pub)}
	got, err := payload.ActuatorKey()
	require.NoError(t, err)
	assert.Equal(t, pub, got)

	missing := &PlatformOperatorCSRPayload{}
	_, err = missing.ActuatorKey()
	assert.ErrorIs(t, err, constants.ErrOperatorActuatorKeyInvalid)
}

func TestPlatformOperatorCSRPayload_AuditorKeyUsesPresentedKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	keyHex := hex.EncodeToString(pub)
	payload := &PlatformOperatorCSRPayload{AuditorPublicKey: keyHex, AuditorKeyID: keyHex}
	got, err := payload.AuditorKey()
	require.NoError(t, err)
	assert.Equal(t, pub, got)

	missing := &PlatformOperatorCSRPayload{}
	_, err = missing.AuditorKey()
	require.ErrorIs(t, err, constants.ErrOperatorAuditorKeyInvalid)

	payload.AuditorKeyID = hex.EncodeToString(make([]byte, ed25519.PublicKeySize))
	_, err = payload.AuditorKey()
	require.ErrorIs(t, err, constants.ErrOperatorAuditorKeyInvalid)
}
