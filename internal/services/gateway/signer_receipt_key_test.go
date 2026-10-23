// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestReceiptSignerKey_EmbeddedOperatorResolvesToGatewayActuatorKey(t *testing.T) {
	t.Parallel()

	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := hex.EncodeToString(pub)
	svc := NewSignerStoreService(nil, nil, nil, pub, keyID)

	got, err := svc.ReceiptSignerKey(t.Context(), string(constants.DocIDEmbeddedOperator),
		&operatorv1.ActionReceipt{SignerKeyId: keyID})
	require.NoError(t, err)
	assert.Equal(t, pub, got)
}

func TestReceiptSignerKey_EmbeddedOperatorRejectsMismatchedSignerKeyID(t *testing.T) {
	t.Parallel()

	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	other, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	svc := NewSignerStoreService(nil, nil, nil, pub, hex.EncodeToString(pub))

	_, err = svc.ReceiptSignerKey(t.Context(), string(constants.DocIDEmbeddedOperator),
		&operatorv1.ActionReceipt{SignerKeyId: hex.EncodeToString(other)})
	assert.ErrorIs(t, err, constants.ErrReceiptSignerNotBound)
}

func TestReceiptSignerKey_EmbeddedOperatorWithoutGatewayKeyIsNotBound(t *testing.T) {
	t.Parallel()

	svc := NewSignerStoreService(nil, nil, nil, nil, "")

	_, err := svc.ReceiptSignerKey(t.Context(), string(constants.DocIDEmbeddedOperator),
		&operatorv1.ActionReceipt{SignerKeyId: ""})
	assert.ErrorIs(t, err, constants.ErrReceiptSignerNotBound)
}
