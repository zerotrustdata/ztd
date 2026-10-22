// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	pubsubv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/pubsub/v1"
)

// --- stubs for event receipt relay unit tests ---

// stubSignerStore implements governance.SignerStore and EventReceiptSignerResolver for event receipt relay tests.
type stubSignerStore struct {
	keys map[string]ed25519.PublicKey
	err  error
}

func (s *stubSignerStore) GetTrustedSigner(_ context.Context, keyID string) (ed25519.PublicKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.keys == nil {
		return nil, nil
	}
	pub, ok := s.keys[keyID]
	if !ok {
		return nil, nil
	}
	return pub, nil
}

// EventReceiptSignerKey resolves the event receipt's signer key id against keys,
// standing in for the Operator's bound Actuator key.
func (s *stubSignerStore) EventReceiptSignerKey(_ context.Context, _ string, eventReceipt *operatorv1.EventReceipt) (ed25519.PublicKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	pub, ok := s.keys[eventReceipt.GetSignerKeyId()]
	if !ok {
		return nil, fmt.Errorf("stub signer store: %w: %s", constants.ErrEventReceiptSignerNotBound, eventReceipt.GetSignerKeyId())
	}
	return pub, nil
}

// stubEventReceiptRecorder implements EventReceiptRecorder for event receipt relay tests.
type stubEventReceiptRecorder struct {
	mu      sync.Mutex
	records []*models.EventReceiptRecord
	err     error
}

func (r *stubEventReceiptRecorder) RecordEventReceipt(record *models.EventReceiptRecord) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

func (r *stubEventReceiptRecorder) recorded() []*models.EventReceiptRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*models.EventReceiptRecord, len(r.records))
	copy(out, r.records)
	return out
}

// --- helpers ---

// newEventReceiptRelayTestBroker creates a GatewayWebSocketHandler wired with
// stub signer store and event receipt recorder for event receipt relay unit tests.
// signerPub is the operator's actuator public key registered under keyID.
func newEventReceiptRelayTestBroker(t *testing.T, keyID string, signerPub ed25519.PublicKey) (*GatewayWebSocketHandler, *stubEventReceiptRecorder) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	broker := NewGatewayWebSocketHandler(logger)
	signerStore := &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: signerPub}}
	recorder := &stubEventReceiptRecorder{}
	broker.SetEventReceiptRelayDeps(signerStore, recorder)
	return broker, recorder
}

// buildSignedEventReceiptEnvelope builds a GovernanceEnvelope wrapping a signed
// EventReceipt as its payload, mirroring what the operator's
// PubSubResultsService.PublishEventReceipt publishes to the event_receipts:
// channel. signerPriv signs the event receipt via governance.CanonicalizeEventReceipt.
func buildSignedEventReceiptEnvelope(t *testing.T, signerPriv ed25519.PrivateKey, keyID, operatorID, operatorSessionID, _, _ string, requestorUserID, actingAppID, transactionID string) ([]byte, *operatorv1.EventReceipt) {
	t.Helper()
	eventReceipt := &operatorv1.EventReceipt{
		TransactionId:    transactionID,
		TransactionHash:  transactionID,
		Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:    "completed",
		StateRootBefore:  "root-before",
		StateRootAfter:   "root-after",
		ExecutedAtUnixMs: 1700000000000,
		SignerKeyId:      keyID,
	}
	canonical, err := governance.CanonicalizeEventReceipt(eventReceipt)
	require.NoError(t, err)
	sig := ed25519.Sign(signerPriv, canonical)
	eventReceipt.Signature = hex.EncodeToString(sig)
	var digestPayload bytes.Buffer
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], 1)
	digestPayload.Write(count[:])
	binary.BigEndian.PutUint32(count[:], uint32(len(eventReceipt.Signature)))
	digestPayload.Write(count[:])
	digestPayload.WriteString(eventReceipt.Signature)
	digest := sha256.Sum256(digestPayload.Bytes())
	eventReceipt.FinalPersistenceAttestation = &operatorv1.EventReceiptPersistenceAttestation{
		TransactionId:               transactionID,
		EventReceiptSignatureDigest: hex.EncodeToString(digest[:]),
		PersistedAtUnixMs:           1700000000001,
		AuditRecordId:               transactionID,
		SignerKeyId:                 keyID,
	}
	attestationPayload, err := governance.CanonicalizeEventReceiptPersistenceAttestation(eventReceipt.FinalPersistenceAttestation)
	require.NoError(t, err)
	eventReceipt.FinalPersistenceAttestation.Signature = hex.EncodeToString(ed25519.Sign(signerPriv, attestationPayload))

	eventReceiptBytes, err := proto.Marshal(eventReceipt)
	require.NoError(t, err)

	env := &commonv1.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		SourceComponent:   commonv1.Component_COMPONENT_ZTDO,
		OperatorId:        operatorID,
		OperatorSessionId: operatorSessionID,
		ActionType:        string(constants.ActionTypeFileEdit),
		TargetResource:    "/tmp/test.txt",
		EventType:         string(constants.Event.Operator.EventReceipt.Recorded),
		Payload:           eventReceiptBytes,
		RequestorUserId:   requestorUserID,
		ActingAppId:       actingAppID,
	}
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)
	return wire, eventReceipt
}

// --- relayEventReceipt table-driven tests ---

func TestRelayEventReceipt_ValidEventReceiptRecordedAndFannedOut(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"
	broker, recorder := newEventReceiptRelayTestBroker(t, keyID, pub)
	handler := newOperatorSessionHandler(broker, "op-001")

	operatorID := "op-001"
	sessionID := "sess-001"
	channel := pubsub.EventReceiptsChannel(operatorID, sessionID)

	// Register a handler to capture fan-out.
	var capturedData []byte
	unregister := broker.RegisterHandler(channel, func(_ string, data []byte) {
		capturedData = data
	})
	defer unregister()

	wire, eventReceipt := buildSignedEventReceiptEnvelope(t, priv, keyID, operatorID, sessionID,
		string(constants.ActionTypeFileEdit), "/tmp/test.txt", "user-001", "spiffe://ztd.local/app/app-001", "tx-001")

	handler.handleAction(&pubsubv1.PubSubMessage{
		Action:  constants.PubSubActionPublish,
		Channel: channel,
		Data:    wire,
	})

	// Verify the event receipt was recorded in the audit store.
	records := recorder.recorded()
	require.Len(t, records, 1, "valid event receipt must be recorded in the audit store")
	rec := records[0]
	assert.Equal(t, eventReceipt.TransactionId, rec.TransactionID)
	assert.Equal(t, operatorID, rec.OperatorID)
	assert.Equal(t, sessionID, rec.OperatorSessionID)
	assert.Equal(t, "user-001", rec.RequestorUserID)
	assert.Equal(t, "spiffe://ztd.local/app/app-001", rec.ActingAppID)
	assert.Equal(t, constants.ActionTypeFileEdit, rec.ActionType)
	assert.Equal(t, "/tmp/test.txt", rec.TargetResource)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, rec.Status)
	assert.Equal(t, eventReceipt.Signature, rec.Signature)
	assert.Equal(t, keyID, rec.SignerKeyID)

	// Verify the original envelope was fanned out verbatim.
	require.NotNil(t, capturedData, "valid event receipt must be fanned out to subscribers")
	assert.Equal(t, wire, capturedData, "fan-out must be the original published data, not a re-marshaled copy")

	// Signer store must have been consulted.
}

func TestRelayEventReceipt_InvalidSignatureRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"
	broker, recorder := newEventReceiptRelayTestBroker(t, keyID, pub)
	handler := newOperatorSessionHandler(broker, "op-001")

	operatorID := "op-001"
	sessionID := "sess-001"
	channel := pubsub.EventReceiptsChannel(operatorID, sessionID)

	var called bool
	unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
	defer unregister()

	wire, _ := buildSignedEventReceiptEnvelope(t, priv, keyID, operatorID, sessionID,
		string(constants.ActionTypeFileEdit), "/tmp/test.txt", "user-001", "app-1", "tx-002")

	// Tamper with the wire data: re-sign with a different key so the
	// signature no longer matches the registered public key.
	_, otherPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	env := &commonv1.GovernanceEnvelope{}
	require.NoError(t, protojson.Unmarshal(wire, env))
	rcpt := &operatorv1.EventReceipt{}
	require.NoError(t, proto.Unmarshal(env.Payload, rcpt))
	canonical, err := governance.CanonicalizeEventReceipt(rcpt)
	require.NoError(t, err)
	badSig := ed25519.Sign(otherPriv, canonical)
	rcpt.Signature = hex.EncodeToString(badSig)
	badPayload, err := proto.Marshal(rcpt)
	require.NoError(t, err)
	env.Payload = badPayload
	badWire, err := protojson.Marshal(env)
	require.NoError(t, err)

	handler.handleAction(&pubsubv1.PubSubMessage{
		Action:  constants.PubSubActionPublish,
		Channel: channel,
		Data:    badWire,
	})

	assert.Empty(t, recorder.recorded(), "invalid signature must not be recorded")
	assert.False(t, called, "invalid signature must not be fanned out")
}

func TestRelayEventReceipt_MissingOrInvalidPersistenceAttestationRejected(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	const keyID = "actuator-key-persistence"
	const operatorID = "op-persistence"
	const sessionID = "session-persistence"
	channel := pubsub.EventReceiptsChannel(operatorID, sessionID)

	tests := []struct {
		name   string
		mutate func(*operatorv1.EventReceipt)
	}{
		{name: "missing attestation", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.FinalPersistenceAttestation = nil
		}},
		{name: "invalid attestation signature", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.FinalPersistenceAttestation.Signature = "invalid"
		}},
		{name: "mismatched event receipt digest", mutate: func(eventReceipt *operatorv1.EventReceipt) {
			eventReceipt.FinalPersistenceAttestation.EventReceiptSignatureDigest = "different-digest"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker, recorder := newEventReceiptRelayTestBroker(t, keyID, publicKey)
			handler := newOperatorSessionHandler(broker, operatorID)
			var called bool
			unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
			t.Cleanup(unregister)
			wire, _ := buildSignedEventReceiptEnvelope(t, privateKey, keyID, operatorID, sessionID,
				string(constants.ActionTypeFileEdit), "/tmp/test.txt", "user-persistence", "app-persistence", "tx-persistence")
			envelope := &commonv1.GovernanceEnvelope{}
			require.NoError(t, protojson.Unmarshal(wire, envelope))
			eventReceipt := &operatorv1.EventReceipt{}
			require.NoError(t, proto.Unmarshal(envelope.Payload, eventReceipt))
			tt.mutate(eventReceipt)
			envelope.Payload, err = proto.Marshal(eventReceipt)
			require.NoError(t, err)
			wire, err = protojson.Marshal(envelope)
			require.NoError(t, err)

			handler.handleAction(&pubsubv1.PubSubMessage{Action: constants.PubSubActionPublish, Channel: channel, Data: wire})

			assert.Empty(t, recorder.recorded())
			assert.False(t, called)
		})
	}
}

func TestRelayEventReceipt_UnknownSignerKeyRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	// Register a different key than the one that signs the event receipt.
	broker, recorder := newEventReceiptRelayTestBroker(t, "registered-key", pub)
	handler := newOperatorSessionHandler(broker, "op-001")

	operatorID := "op-001"
	sessionID := "sess-001"
	channel := pubsub.EventReceiptsChannel(operatorID, sessionID)

	var called bool
	unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
	defer unregister()

	// Sign with "unknown-key" which is not in the signer store.
	wire, _ := buildSignedEventReceiptEnvelope(t, priv, "unknown-key", operatorID, sessionID,
		string(constants.ActionTypeFileEdit), "/tmp/test.txt", "user-001", "app-1", "tx-003")

	handler.handleAction(&pubsubv1.PubSubMessage{
		Action:  constants.PubSubActionPublish,
		Channel: channel,
		Data:    wire,
	})

	assert.Empty(t, recorder.recorded(), "unknown signer key must not be recorded")
	assert.False(t, called, "unknown signer key must not be fanned out")
}

func TestRelayEventReceipt_ChannelEnvelopeIdentityMismatchRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"
	broker, recorder := newEventReceiptRelayTestBroker(t, keyID, pub)
	handler := newOperatorSessionHandler(broker, "op-001")

	// Publish to op-001's channel but the envelope claims op-002.
	channel := pubsub.EventReceiptsChannel("op-001", "sess-001")

	var called bool
	unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
	defer unregister()

	wire, _ := buildSignedEventReceiptEnvelope(t, priv, keyID, "op-002", "sess-002",
		string(constants.ActionTypeFileEdit), "/tmp/test.txt", "user-001", "app-1", "tx-004")

	handler.handleAction(&pubsubv1.PubSubMessage{
		Action:  constants.PubSubActionPublish,
		Channel: channel,
		Data:    wire,
	})

	assert.Empty(t, recorder.recorded(), "channel/envelope identity mismatch must not be recorded")
	assert.False(t, called, "channel/envelope identity mismatch must not be fanned out")
}

func TestRelayEventReceipt_MalformedProtoJSONRejected(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"
	broker, recorder := newEventReceiptRelayTestBroker(t, keyID, pub)
	handler := newOperatorSessionHandler(broker, "op-001")

	channel := pubsub.EventReceiptsChannel("op-001", "sess-001")

	var called bool
	unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
	defer unregister()

	handler.handleAction(&pubsubv1.PubSubMessage{
		Action:  constants.PubSubActionPublish,
		Channel: channel,
		Data:    []byte("{invalid json"),
	})

	assert.Empty(t, recorder.recorded(), "malformed protojson must not be recorded")
	assert.False(t, called, "malformed protojson must not be fanned out")
}

func TestRelayEventReceipt_MalformedPayloadRejected(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"
	broker, recorder := newEventReceiptRelayTestBroker(t, keyID, pub)
	handler := newOperatorSessionHandler(broker, "op-001")

	operatorID := "op-001"
	sessionID := "sess-001"
	channel := pubsub.EventReceiptsChannel(operatorID, sessionID)

	var called bool
	unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
	defer unregister()

	// Build an envelope with a valid identity but a malformed payload
	// (not a valid EventReceipt proto).
	env := &commonv1.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		SourceComponent:   commonv1.Component_COMPONENT_ZTDO,
		OperatorId:        operatorID,
		OperatorSessionId: sessionID,
		EventType:         string(constants.Event.Operator.EventReceipt.Recorded),
		ActionType:        string(constants.ActionTypeFileEdit),
		Payload:           []byte("not-a-valid-protobuf"),
	}
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)

	handler.handleAction(&pubsubv1.PubSubMessage{
		Action:  constants.PubSubActionPublish,
		Channel: channel,
		Data:    wire,
	})

	assert.Empty(t, recorder.recorded(), "malformed payload must not be recorded")
	assert.False(t, called, "malformed payload must not be fanned out")
}

func TestRelayEventReceipt_MissingPayloadRejected(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"
	broker, recorder := newEventReceiptRelayTestBroker(t, keyID, pub)
	handler := newOperatorSessionHandler(broker, "op-001")

	operatorID := "op-001"
	sessionID := "sess-001"
	channel := pubsub.EventReceiptsChannel(operatorID, sessionID)

	var called bool
	unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
	defer unregister()

	// Envelope with no payload bytes.
	env := &commonv1.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		SourceComponent:   commonv1.Component_COMPONENT_ZTDO,
		OperatorId:        operatorID,
		OperatorSessionId: sessionID,
		EventType:         string(constants.Event.Operator.EventReceipt.Recorded),
		ActionType:        string(constants.ActionTypeFileEdit),
	}
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)

	handler.handleAction(&pubsubv1.PubSubMessage{
		Action:  constants.PubSubActionPublish,
		Channel: channel,
		Data:    wire,
	})

	assert.Empty(t, recorder.recorded(), "missing payload must not be recorded")
	assert.False(t, called, "missing payload must not be fanned out")
}

func TestRelayEventReceipt_RelayDisabledWhenDepsNotConfigured(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	broker := NewGatewayWebSocketHandler(logger)
	// Do NOT call SetEventReceiptRelayDeps — relay is disabled.
	handler := newOperatorSessionHandler(broker, "op-001")

	channel := pubsub.EventReceiptsChannel("op-001", "sess-001")

	var called bool
	unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
	defer unregister()

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	wire, _ := buildSignedEventReceiptEnvelope(t, priv, "actuator-key-1", "op-001", "sess-001",
		string(constants.ActionTypeFileEdit), "/tmp/test.txt", "user-001", "app-1", "tx-005")
	_ = pub

	assert.NotPanics(t, func() {
		handler.handleAction(&pubsubv1.PubSubMessage{
			Action:  constants.PubSubActionPublish,
			Channel: channel,
			Data:    wire,
		})
	})
	assert.False(t, called, "event_receipts: relay must be dropped when deps are not configured")
}

func TestRelayEventReceipt_SignerStoreErrorRejected(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	broker := NewGatewayWebSocketHandler(logger)
	signerStore := &stubSignerStore{err: fmt.Errorf("signer store unavailable")}
	recorder := &stubEventReceiptRecorder{}
	broker.SetEventReceiptRelayDeps(signerStore, recorder)
	handler := newOperatorSessionHandler(broker, "op-001")

	operatorID := "op-001"
	sessionID := "sess-001"
	channel := pubsub.EventReceiptsChannel(operatorID, sessionID)

	var called bool
	unregister := broker.RegisterHandler(channel, func(_ string, _ []byte) { called = true })
	defer unregister()

	wire, _ := buildSignedEventReceiptEnvelope(t, priv, keyID, operatorID, sessionID,
		string(constants.ActionTypeFileEdit), "/tmp/test.txt", "user-001", "app-1", "tx-006")

	handler.handleAction(&pubsubv1.PubSubMessage{
		Action:  constants.PubSubActionPublish,
		Channel: channel,
		Data:    wire,
	})

	assert.Empty(t, recorder.recorded(), "signer store error must not record the event receipt")
	assert.False(t, called, "signer store error must not fan out")
}

func TestRelayEventReceipt_RecorderErrorStillFansOut(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "actuator-key-1"
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	broker := NewGatewayWebSocketHandler(logger)
	signerStore := &stubSignerStore{keys: map[string]ed25519.PublicKey{keyID: pub}}
	recorder := &stubEventReceiptRecorder{err: fmt.Errorf("audit store write failed")}
	broker.SetEventReceiptRelayDeps(signerStore, recorder)
	handler := newOperatorSessionHandler(broker, "op-001")

	operatorID := "op-001"
	sessionID := "sess-001"
	channel := pubsub.EventReceiptsChannel(operatorID, sessionID)

	var capturedData []byte
	unregister := broker.RegisterHandler(channel, func(_ string, data []byte) {
		capturedData = data
	})
	defer unregister()

	wire, _ := buildSignedEventReceiptEnvelope(t, priv, keyID, operatorID, sessionID,
		string(constants.ActionTypeFileEdit), "/tmp/test.txt", "user-001", "app-1", "tx-007")

	assert.NotPanics(t, func() {
		handler.handleAction(&pubsubv1.PubSubMessage{
			Action:  constants.PubSubActionPublish,
			Channel: channel,
			Data:    wire,
		})
	})
	// The recorder returned an error, but the event receipt should still be
	// fanned out to subscribers (best-effort recording, not a gate).
	require.NotNil(t, capturedData, "recorder error must not block fan-out")
	assert.Equal(t, wire, capturedData)
}

func TestSetEventReceiptRelayDeps(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	broker := NewGatewayWebSocketHandler(logger)

	broker.eventReceiptRelayMu.RLock()
	assert.Nil(t, broker.eventReceiptSignerStore, "signer store must be nil before SetEventReceiptRelayDeps")
	assert.Nil(t, broker.eventReceiptAuditRecorder, "recorder must be nil before SetEventReceiptRelayDeps")
	broker.eventReceiptRelayMu.RUnlock()

	signerStore := &stubSignerStore{keys: map[string]ed25519.PublicKey{"k1": {1, 2, 3}}}
	recorder := &stubEventReceiptRecorder{}
	broker.SetEventReceiptRelayDeps(signerStore, recorder)

	broker.eventReceiptRelayMu.RLock()
	assert.NotNil(t, broker.eventReceiptSignerStore)
	assert.NotNil(t, broker.eventReceiptAuditRecorder)
	broker.eventReceiptRelayMu.RUnlock()
}
