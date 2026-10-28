// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TestOperatorDocumentRoundTripFullyPopulated asserts that a fully populated
// OperatorDocument round-trips through JSON and compares as proto.Equal.
func TestOperatorDocumentRoundTripFullyPopulated(t *testing.T) {
	t.Parallel()

	// Use fixed timestamps for determinism
	createdAt := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	startedAt := time.Date(2026, 1, 15, 11, 30, 0, 0, time.UTC)
	claimedAt := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	lastHeartbeatAt := time.Date(2026, 1, 15, 12, 30, 0, 0, time.UTC)

	heartbeatSnapshot := &operatorv1.HeartbeatResult{
		OperatorId:        "op-1",
		OperatorSessionId: "session-1",
		Timestamp:         "2026-01-15T12:30:00Z",
		Status:            "healthy",
		EventType:         "heartbeat_sent",
		SourceComponent:   "operator",
		SystemFingerprint: "fingerprint-123",
	}

	runtimeConfig := &operatorv1.OperatorRuntimeConfig{
		CloudMode:                          false,
		CloudProvider:                      "aws",
		LogLevel:                           "info",
		HttpPort:                           8080,
		Capabilities:                       []string{string(constants.OperatorCapabilityData), string(constants.OperatorCapabilityInference), string(constants.OperatorCapabilityObserver), string(constants.OperatorCapabilityProvenance)},
		LocalDir:                           "/home/user/.ztd",
		Account:                            "user@example.com",
		InferenceOllamaEndpoint:            "http://localhost:11434",
		ProvenanceOperatorModelStorageRoot: "/models",
		Platform:                           "linux",
	}

	doc := &operatorv1.OperatorDocument{
		Id:                      "op-1",
		UserId:                  "user-1",
		OrganizationId:          "org-1",
		Component:               string(constants.ComponentNameZTDO),
		Name:                    "test-operator",
		Status:                  string(constants.OperatorStatusActive),
		OperatorSessionId:       "session-1",
		BoundWebSessionId:       "web-session-1",
		OperatorCert:            "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
		OperatorCertSerial:      "1234567890",
		SlotNumber:              1,
		IsSlot:                  true,
		Claimed:                 true,
		OperatorType:            string(constants.OperatorTypeRemote),
		SystemFingerprint:       "fingerprint-123",
		CreatedAt:               timestamppb.New(createdAt),
		UpdatedAt:               timestamppb.New(updatedAt),
		StartedAt:               timestamppb.New(startedAt),
		ClaimedAt:               timestamppb.New(claimedAt),
		LastHeartbeatAt:         timestamppb.New(lastHeartbeatAt),
		OperatorCapabilities:    []string{string(constants.OperatorCapabilityData), string(constants.OperatorCapabilityInference)},
		LocalDir:                "/home/user/.ztd",
		Account:                 "user@example.com",
		Port:                    8080,
		StopReason:              "normal_shutdown",
		LatestHeartbeatSnapshot: heartbeatSnapshot,
		CurrentHostname:         "host-1",
		RuntimeConfig:           runtimeConfig,
		ConsumedByOperatorId:    "parent-op-1",
		OperatorCertChain:       "chain-pem",
		TerminationReason:       "user_requested",
		StaleAt:                 timestamppb.New(lastHeartbeatAt.Add(time.Minute)),
	}

	// Marshal to JSON
	data, err := MarshalOperatorDocument(doc)
	require.NoError(t, err)

	// Unmarshal back
	decoded, err := UnmarshalOperatorDocument(data)
	require.NoError(t, err)

	// Compare with proto.Equal
	assert.True(t, proto.Equal(doc, decoded), "fully populated document should round-trip as proto.Equal")
}

// TestOperatorDocumentJSONFormat asserts that marshaled JSON uses snake_case
// keys and RFC3339 strings for all five timestamp fields.
func TestOperatorDocumentJSONFormat(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 1, 15, 10, 30, 45, 123456000, time.UTC)
	updatedAt := time.Date(2026, 1, 15, 11, 0, 45, 654321000, time.UTC)
	startedAt := time.Date(2026, 1, 15, 11, 30, 45, 0, time.UTC)
	claimedAt := time.Date(2026, 1, 15, 12, 0, 45, 0, time.UTC)
	lastHeartbeatAt := time.Date(2026, 1, 15, 12, 30, 45, 0, time.UTC)

	doc := &operatorv1.OperatorDocument{
		Id:              "op-1",
		UserId:          "user-1",
		Component:       string(constants.ComponentNameZTDO),
		Status:          string(constants.OperatorStatusActive),
		CreatedAt:       timestamppb.New(createdAt),
		UpdatedAt:       timestamppb.New(updatedAt),
		StartedAt:       timestamppb.New(startedAt),
		ClaimedAt:       timestamppb.New(claimedAt),
		LastHeartbeatAt: timestamppb.New(lastHeartbeatAt),
	}

	data, err := MarshalOperatorDocument(doc)
	require.NoError(t, err)

	// Decode into map to check field names
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))

	// Check snake_case keys exist
	assert.Contains(t, decoded, "id")
	assert.Contains(t, decoded, "user_id")
	assert.Contains(t, decoded, "component")
	assert.Contains(t, decoded, "status")
	assert.Contains(t, decoded, "created_at")
	assert.Contains(t, decoded, "updated_at")
	assert.Contains(t, decoded, "started_at")
	assert.Contains(t, decoded, "claimed_at")
	assert.Contains(t, decoded, "last_heartbeat_at")

	// Check timestamps are RFC3339 formatted strings
	createdAtStr := decoded["created_at"].(string)
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.*Z$`, createdAtStr)

	updatedAtStr := decoded["updated_at"].(string)
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.*Z$`, updatedAtStr)

	startedAtStr := decoded["started_at"].(string)
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.*Z$`, startedAtStr)

	claimedAtStr := decoded["claimed_at"].(string)
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.*Z$`, claimedAtStr)

	lastHeartbeatAtStr := decoded["last_heartbeat_at"].(string)
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}.*Z$`, lastHeartbeatAtStr)
}

// TestOperatorDocumentBooleanDefaults asserts that is_slot and claimed are
// present as false when unset in the JSON output.
func TestOperatorDocumentBooleanDefaults(t *testing.T) {
	t.Parallel()

	doc := &operatorv1.OperatorDocument{
		Id:        "op-1",
		UserId:    "user-1",
		Component: string(constants.ComponentNameZTDO),
		Status:    string(constants.OperatorStatusActive),
		// IsSlot and Claimed left unset (false by default in proto3)
	}

	data, err := MarshalOperatorDocument(doc)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))

	// Check they are explicitly present as false
	assert.Contains(t, decoded, "is_slot")
	assert.Equal(t, false, decoded["is_slot"])

	assert.Contains(t, decoded, "claimed")
	assert.Equal(t, false, decoded["claimed"])
}

// TestOperatorDocumentInvalidOperatorCapabilities asserts that invalid capabilities in
// operator_capabilities trigger validation errors.
func TestOperatorDocumentInvalidOperatorCapabilities(t *testing.T) {
	t.Parallel()

	invalidData := []byte(`{
		"id": "op-1",
		"user_id": "user-1",
		"component": "ztdo",
		"status": "active",
		"operator_capabilities": ["data", "invalid_capability"]
	}`)

	_, err := UnmarshalOperatorDocument(invalidData)
	require.Error(t, err, "should reject invalid capability in operator_capabilities")
	assert.Contains(t, err.Error(), "invalid_capability")
}

// TestOperatorDocumentInvalidCapabilityRuntimeConfig asserts that invalid capabilities in
// runtime_config.capabilities trigger validation errors via UnmarshalOperatorRuntimeConfig.
func TestOperatorDocumentInvalidCapabilityRuntimeConfig(t *testing.T) {
	t.Parallel()

	invalidData := []byte(`{
		"capabilities": ["data", "unknown_capability"]
	}`)

	_, err := UnmarshalOperatorRuntimeConfig(invalidData)
	require.Error(t, err, "should reject invalid capability in runtime_config.capabilities")
	assert.Contains(t, err.Error(), "unknown_capability")
}

// TestOperatorDocumentLegacyStoredRow asserts that legacy stored JSON (with
// RFC3339 timestamps including microseconds, +00:00 suffix, omitted false fields
// in runtime_config, and unknown keys) decodes without error.
func TestOperatorDocumentLegacyStoredRow(t *testing.T) {
	t.Parallel()

	// Legacy format with microseconds, +00:00 suffix, omitted false fields, unknown key
	legacyData := []byte(`{
		"id": "op-legacy-1",
		"user_id": "user-1",
		"organization_id": "org-1",
		"component": "ztdo",
		"name": "legacy-op",
		"status": "active",
		"operator_session_id": "session-1",
		"created_at": "2026-01-15T10:30:00.123456+00:00",
		"updated_at": "2026-01-15T11:00:00.654321+00:00",
		"operator_capabilities": ["data", "inference"],
		"runtime_config": {
			"cloud_mode": false,
			"http_port": 8080,
			"capabilities": ["data"]
		},
		"latest_heartbeat_snapshot": {
			"operator_id": "op-legacy-1",
			"timestamp": "2026-01-15T12:00:00Z",
			"status": "healthy"
		},
		"unknown_field": "this should be ignored"
	}`)

	doc, err := UnmarshalOperatorDocument(legacyData)
	require.NoError(t, err, "legacy stored row should decode without error")
	assert.Equal(t, "op-legacy-1", doc.GetId())
	assert.Equal(t, "user-1", doc.GetUserId())
	assert.Equal(t, "legacy-op", doc.GetName())
	assert.Len(t, doc.GetOperatorCapabilities(), 2)
	assert.NotNil(t, doc.GetRuntimeConfig())
	assert.NotNil(t, doc.GetLatestHeartbeatSnapshot())
}

// TestOperatorDocumentFromStoreWithMissingMetadata asserts that
// OperatorDocumentFromStore populates id and created_at from Document
// metadata when they are missing from the stored JSON data.
func TestOperatorDocumentFromStoreWithMissingMetadata(t *testing.T) {
	t.Parallel()

	// Document with id and created_at in metadata
	createdAt := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	doc := &Document{
		ID:        "doc-id-from-metadata",
		CreatedAt: createdAt,
		Data: map[string]json.RawMessage{
			"user_id":   json.RawMessage(`"user-1"`),
			"component": json.RawMessage(`"ztdo"`),
			"status":    json.RawMessage(`"active"`),
			// id and created_at intentionally omitted from Data
		},
	}

	opDoc, err := OperatorDocumentFromStore(doc)
	require.NoError(t, err)

	// The values should come from Document metadata
	assert.Equal(t, "doc-id-from-metadata", opDoc.GetId())
	assert.NotNil(t, opDoc.GetCreatedAt())
}

// TestOperatorSlotResponseRoundTrip asserts that OperatorSlotResponse with
// multiple operators round-trips through encoding/json.
func TestOperatorSlotResponseRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	op1 := &operatorv1.OperatorDocument{
		Id:        "op-1",
		UserId:    "user-1",
		Component: string(constants.ComponentNameZTDO),
		Status:    string(constants.OperatorStatusActive),
		CreatedAt: timestamppb.New(now),
	}

	op2 := &operatorv1.OperatorDocument{
		Id:        "op-2",
		UserId:    "user-1",
		Component: string(constants.ComponentNameZTDO),
		Status:    string(constants.OperatorStatusOffline),
		CreatedAt: timestamppb.New(now),
	}

	resp := OperatorSlotResponse{
		Success:   true,
		Operators: []*operatorv1.OperatorDocument{op1, op2},
	}

	// Marshal
	data, err := json.Marshal(resp)
	require.NoError(t, err)

	// Unmarshal
	var decoded OperatorSlotResponse
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, resp.Success, decoded.Success)
	assert.Len(t, decoded.Operators, 2)
	assert.True(t, proto.Equal(op1, decoded.Operators[0]))
	assert.True(t, proto.Equal(op2, decoded.Operators[1]))
}

// TestOperatorResponseRoundTrip asserts that OperatorResponse round-trips
// through encoding/json, including the nil operator case.
func TestOperatorResponseRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("with operator", func(t *testing.T) {
		now := time.Now().UTC()
		op := &operatorv1.OperatorDocument{
			Id:        "op-1",
			UserId:    "user-1",
			Component: string(constants.ComponentNameZTDO),
			Status:    string(constants.OperatorStatusActive),
			CreatedAt: timestamppb.New(now),
		}

		resp := OperatorResponse{
			Success:  true,
			Operator: op,
		}

		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var decoded OperatorResponse
		require.NoError(t, json.Unmarshal(data, &decoded))

		assert.Equal(t, resp.Success, decoded.Success)
		assert.True(t, proto.Equal(op, decoded.Operator))
	})

	t.Run("nil operator", func(t *testing.T) {
		resp := OperatorResponse{
			Success:  false,
			Operator: nil,
		}

		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var decoded OperatorResponse
		require.NoError(t, json.Unmarshal(data, &decoded))

		assert.Equal(t, resp.Success, decoded.Success)
		assert.Nil(t, decoded.Operator)
	})
}

// TestOperatorDocumentGoldenFixture marshals a fully populated OperatorDocument
// and compares to the checked-in golden fixture shared with the console
// contract test. A proto field change updates the fixture in the same change.
func TestOperatorDocumentGoldenFixture(t *testing.T) {
	t.Parallel()

	// Use fixed timestamps for determinism
	createdAt := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	startedAt := time.Date(2026, 1, 15, 11, 30, 0, 0, time.UTC)
	claimedAt := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	lastHeartbeatAt := time.Date(2026, 1, 15, 12, 30, 0, 0, time.UTC)

	heartbeatSnapshot := &operatorv1.HeartbeatResult{
		OperatorId:        "op-golden-1",
		OperatorSessionId: "session-golden-1",
		Timestamp:         "2026-01-15T12:30:00Z",
		Status:            "healthy",
		EventType:         "heartbeat_sent",
		SourceComponent:   "operator",
		SystemFingerprint: "fingerprint-golden-123",
	}

	runtimeConfig := &operatorv1.OperatorRuntimeConfig{
		CloudMode:                          false,
		CloudProvider:                      "aws",
		LogLevel:                           "info",
		HttpPort:                           8080,
		Capabilities:                       []string{string(constants.OperatorCapabilityData), string(constants.OperatorCapabilityInference), string(constants.OperatorCapabilityObserver), string(constants.OperatorCapabilityProvenance)},
		LocalDir:                           "/home/user/.ztd",
		Account:                            "user@example.com",
		InferenceOllamaEndpoint:            "http://localhost:11434",
		ProvenanceOperatorModelStorageRoot: "/models",
		Platform:                           "linux",
		HeartbeatIntervalMs:                30000,
	}

	doc := &operatorv1.OperatorDocument{
		Id:                      "op-golden-1",
		UserId:                  "user-golden-1",
		OrganizationId:          "org-golden-1",
		Component:               string(constants.ComponentNameZTDO),
		Name:                    "golden-operator",
		Status:                  string(constants.OperatorStatusActive),
		OperatorSessionId:       "session-golden-1",
		BoundWebSessionId:       "web-session-golden-1",
		OperatorCert:            "-----BEGIN CERTIFICATE-----\nMIICljCCAX4CCQCKz0Rx...\n-----END CERTIFICATE-----",
		OperatorCertSerial:      "1234567890abcdef",
		SlotNumber:              1,
		IsSlot:                  true,
		Claimed:                 true,
		OperatorType:            string(constants.OperatorTypeRemote),
		SystemFingerprint:       "fingerprint-golden-123",
		CreatedAt:               timestamppb.New(createdAt),
		UpdatedAt:               timestamppb.New(updatedAt),
		StartedAt:               timestamppb.New(startedAt),
		ClaimedAt:               timestamppb.New(claimedAt),
		LastHeartbeatAt:         timestamppb.New(lastHeartbeatAt),
		OperatorCapabilities:    []string{string(constants.OperatorCapabilityData), string(constants.OperatorCapabilityInference)},
		LocalDir:                "/home/user/.ztd",
		Account:                 "user@example.com",
		Port:                    8080,
		StopReason:              "normal_shutdown",
		LatestHeartbeatSnapshot: heartbeatSnapshot,
		CurrentHostname:         "host-golden-1",
		RuntimeConfig:           runtimeConfig,
		ConsumedByOperatorId:    "parent-op-golden-1",
		OperatorCertChain:       "-----BEGIN CERTIFICATE-----\nchain...\n-----END CERTIFICATE-----",
		TerminationReason:       "user_requested",
		StaleAt:                 timestamppb.New(lastHeartbeatAt.Add(time.Minute)),
		// RFC 8032 test vector 1 public key; key_id == hex(pub).
		ActuatorPublicKey: "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a",
		ActuatorKeyId:     "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a",
		AuditorPublicKey:  "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c",
		AuditorKeyId:      "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c",
	}

	// Marshal to JSON
	data, err := MarshalOperatorDocument(doc)
	require.NoError(t, err)

	// Pretty-print with indentation
	var formatted map[string]any
	require.NoError(t, json.Unmarshal(data, &formatted))
	prettyData, err := json.MarshalIndent(formatted, "", "  ")
	require.NoError(t, err)

	// Add trailing newline
	prettyData = append(prettyData, '\n')

	fixturePath := "../../protocol/test-fixtures/operator-document.json"

	// Read and compare
	storedData, err := os.ReadFile(fixturePath)
	require.NoError(t, err, "failed to read golden fixture")

	assert.Equal(t, strings.ReplaceAll(string(storedData), "\r\n", "\n"), strings.ReplaceAll(string(prettyData), "\r\n", "\n"), "operator document JSON should match golden fixture")
}
