// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	pubsubv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/pubsub/v1"
)

// seedOperatorDocument stores an OperatorDocument carrying the given bound
// Actuator key. An empty key stores an Operator with no bound key.
func seedOperatorDocument(t *testing.T, svc *SignerStoreService, operatorID, actuatorKeyHex string) {
	t.Helper()
	raw, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                operatorID,
		ActuatorPublicKey: actuatorKeyHex,
		ActuatorKeyId:     actuatorKeyHex,
	})
	require.NoError(t, err)
	require.NoError(t, svc.docSvc.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), operatorID, raw))
}

func TestEventReceiptSignerKey_OutboundOperatorResolvesToBoundKey(t *testing.T) {
	svc := setupSignerStore(t)
	pubKey := generateTestPublicKey(t)
	keyID := hex.EncodeToString(pubKey)
	seedOperatorDocument(t, svc, "operator-bound", keyID)

	got, err := svc.EventReceiptSignerKey(t.Context(), "operator-bound", &operatorv1.EventReceipt{SignerKeyId: keyID})
	require.NoError(t, err)
	assert.Equal(t, pubKey, got)
}

func TestEventReceiptSignerKey_OutboundOperatorWithoutBoundKeyIsNotBound(t *testing.T) {
	svc := setupSignerStore(t)
	pubKey := generateTestPublicKey(t)
	seedOperatorDocument(t, svc, "operator-unbound", "")

	_, err := svc.EventReceiptSignerKey(t.Context(), "operator-unbound", &operatorv1.EventReceipt{SignerKeyId: hex.EncodeToString(pubKey)})
	assert.ErrorIs(t, err, constants.ErrEventReceiptSignerNotBound)

	_, err = svc.EventReceiptSignerKey(t.Context(), "operator-missing", &operatorv1.EventReceipt{SignerKeyId: hex.EncodeToString(pubKey)})
	assert.ErrorIs(t, err, constants.ErrEventReceiptSignerNotBound)
}

func TestEventReceiptSignerKey_OutboundOperatorRejectsSignerOutsideBinding(t *testing.T) {
	svc := setupSignerStore(t)
	bound := generateTestPublicKey(t)
	fresh := generateTestPublicKey(t)
	seedOperatorDocument(t, svc, "operator-bound", hex.EncodeToString(bound))

	_, err := svc.EventReceiptSignerKey(t.Context(), "operator-bound", &operatorv1.EventReceipt{SignerKeyId: hex.EncodeToString(fresh)})
	assert.ErrorIs(t, err, constants.ErrEventReceiptSignerNotBound)
}

// TestRelayEventReceipt_BoundOperatorKeyAcceptedUnboundKeyRejected relays two
// event receipts through the real SignerStoreService: one signed by the key bound to
// the publishing Operator, and one signed by a fresh key the Operator never
// bound. Only the bound event receipt is recorded.
func TestRelayEventReceipt_BoundOperatorKeyAcceptedUnboundKeyRejected(t *testing.T) {
	svc := setupSignerStore(t)
	boundPub, boundPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	freshPub, freshPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	boundKeyID := hex.EncodeToString(boundPub)
	freshKeyID := hex.EncodeToString(freshPub)
	seedOperatorDocument(t, svc, "op-001", boundKeyID)

	broker := NewGatewayWebSocketHandler(testutil.NewTestLogger())
	recorder := &stubEventReceiptRecorder{}
	broker.SetEventReceiptRelayDeps(svc, recorder)
	handler := newOperatorSessionHandler(broker, "op-001")
	channel := pubsub.EventReceiptsChannel("op-001", "sess-001")

	publish := func(wire []byte) {
		handler.handleAction(&pubsubv1.PubSubMessage{
			Action:  constants.PubSubActionPublish,
			Channel: channel,
			Data:    wire,
		})
	}

	boundWire, _ := buildSignedEventReceiptEnvelope(t, boundPriv, boundKeyID, "op-001", "sess-001",
		string(constants.ActionTypeFileEdit), "/tmp/bound.txt", "user-001", "spiffe://ztd.local/app/app-001", "tx-bound")
	publish(boundWire)
	require.Len(t, recorder.recorded(), 1, "event receipt signed by the bound key must be recorded")

	freshWire, _ := buildSignedEventReceiptEnvelope(t, freshPriv, freshKeyID, "op-001", "sess-001",
		string(constants.ActionTypeFileEdit), "/tmp/fresh.txt", "user-001", "spiffe://ztd.local/app/app-001", "tx-fresh")
	publish(freshWire)
	assert.Len(t, recorder.recorded(), 1, "event receipt signed by an unbound key must be rejected")
}
