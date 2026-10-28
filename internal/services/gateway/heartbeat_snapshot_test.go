// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zerotrustdata/ztd/internal/constants"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestHeartbeatResultFromEnvelope_BinaryPayload(t *testing.T) {
	heartbeat := &operatorv1.HeartbeatResult{
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
		Timestamp:         "2026-09-25T12:00:00Z",
		Status:            "automatic",
		SystemIdentity: &operatorv1.SystemIdentity{
			Hostname:     "worker-1",
			Os:           "linux",
			Architecture: "amd64",
			CurrentUser:  "root",
		},
	}
	payload, err := proto.Marshal(heartbeat)
	require.NoError(t, err)

	env := &commonv1.GovernanceEnvelope{
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
		Payload:           payload,
	}

	decoded, err := heartbeatResultFromEnvelope(env)
	require.NoError(t, err)
	assert.Equal(t, "worker-1", decoded.SystemIdentity.Hostname)
	assert.Equal(t, "automatic", decoded.Status)
}

func TestHeartbeatResultFromEnvelope_IntentDataProtojson(t *testing.T) {
	heartbeat := &operatorv1.HeartbeatResult{
		OperatorId:        "op-1",
		OperatorSessionId: "session-1",
		Timestamp:         "2026-09-25T12:00:00Z",
		Status:            "automatic",
		EventType:         "heartbeat",
		SystemFingerprint: "fingerprint-1",
		SystemIdentity: &operatorv1.SystemIdentity{
			Hostname:     "protojson-host",
			Architecture: "arm64",
			CpuCount:     12,
		},
		NetworkInfo: &operatorv1.NetworkInfo{
			PublicIp:   "192.0.2.1",
			Interfaces: []string{"eth0", "wlan0"},
		},
		PerformanceMetrics: &operatorv1.PerformanceMetrics{
			CpuPercent:   12.5,
			MemoryUsedMb: 2048,
		},
	}
	intentJSON, err := protojson.Marshal(heartbeat)
	require.NoError(t, err)

	intentStruct := &structpb.Struct{}
	require.NoError(t, protojson.Unmarshal(intentJSON, intentStruct))

	env := &commonv1.GovernanceEnvelope{
		OperatorId: "op-1",
		IntentData: intentStruct,
	}

	decoded, err := heartbeatResultFromEnvelope(env)
	require.NoError(t, err)
	assert.True(t, proto.Equal(heartbeat, decoded), "the JSON fallback must preserve the full typed heartbeat")
}

func TestHeartbeatResultFromEnvelope_RejectsEmptyEnvelope(t *testing.T) {
	_, err := heartbeatResultFromEnvelope(&commonv1.GovernanceEnvelope{
		OperatorId: "op-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no payload")
}

func TestHeartbeatResultFromEnvelope_RejectsEmptyBinaryPayload(t *testing.T) {
	_, err := heartbeatResultFromEnvelope(&commonv1.GovernanceEnvelope{
		OperatorId: "op-1",
		Payload:    []byte{0x00},
	})
	require.Error(t, err)
}

func TestHeartbeatResultFromEnvelope_RejectsOperatorIDMismatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(t *testing.T) *commonv1.GovernanceEnvelope
	}{
		{
			name: "binary payload",
			env: func(t *testing.T) *commonv1.GovernanceEnvelope {
				t.Helper()
				payload, err := proto.Marshal(&operatorv1.HeartbeatResult{OperatorId: "op-2", Status: "automatic"})
				require.NoError(t, err)
				return &commonv1.GovernanceEnvelope{OperatorId: "op-1", Payload: payload}
			},
		},
		{
			name: "intent_data",
			env: func(t *testing.T) *commonv1.GovernanceEnvelope {
				t.Helper()
				data, err := structpb.NewStruct(map[string]any{"operator_id": "op-2", "status": "automatic"})
				require.NoError(t, err)
				return &commonv1.GovernanceEnvelope{OperatorId: "op-1", IntentData: data}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := heartbeatResultFromEnvelope(tc.env(t))
			require.ErrorIs(t, err, constants.ErrHeartbeatIdentityMismatch)
			assert.Nil(t, got)
		})
	}
}

func TestHeartbeatResultFromEnvelope_AllowsOmittedPayloadOperatorID(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(t *testing.T) *commonv1.GovernanceEnvelope
	}{
		{
			name: "binary payload",
			env: func(t *testing.T) *commonv1.GovernanceEnvelope {
				t.Helper()
				payload, err := proto.Marshal(&operatorv1.HeartbeatResult{Status: "automatic"})
				require.NoError(t, err)
				return &commonv1.GovernanceEnvelope{OperatorId: "op-1", Payload: payload}
			},
		},
		{
			name: "intent_data",
			env: func(t *testing.T) *commonv1.GovernanceEnvelope {
				t.Helper()
				data, err := structpb.NewStruct(map[string]any{"status": "automatic"})
				require.NoError(t, err)
				return &commonv1.GovernanceEnvelope{OperatorId: "op-1", IntentData: data}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := heartbeatResultFromEnvelope(tc.env(t))
			require.NoError(t, err)
			assert.Equal(t, "automatic", got.GetStatus())
		})
	}
}

func TestMarshalHeartbeatSnapshot_UsesProtoFieldNames(t *testing.T) {
	heartbeat := &operatorv1.HeartbeatResult{
		Status: "automatic",
		SystemIdentity: &operatorv1.SystemIdentity{
			Hostname: "stored-host",
		},
		PerformanceMetrics: &operatorv1.PerformanceMetrics{
			CpuPercent: 12.5,
		},
	}

	snapshot, err := marshalHeartbeatSnapshot(heartbeat)
	require.NoError(t, err)

	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(snapshot, &stored))
	assert.Contains(t, stored, "system_identity")
	assert.Contains(t, stored, "performance_metrics")
	assert.NotContains(t, stored, "systemIdentity")
	assert.NotContains(t, stored, "performanceMetrics")

	var systemIdentity map[string]string
	require.NoError(t, json.Unmarshal(stored["system_identity"], &systemIdentity))
	assert.Equal(t, "stored-host", systemIdentity["hostname"])
}

func TestCurrentHostnameFromHeartbeat(t *testing.T) {
	assert.Empty(t, currentHostnameFromHeartbeat(nil))
	assert.Empty(t, currentHostnameFromHeartbeat(&operatorv1.HeartbeatResult{}))
	assert.Equal(t, "edge-node", currentHostnameFromHeartbeat(&operatorv1.HeartbeatResult{
		SystemIdentity: &operatorv1.SystemIdentity{Hostname: "edge-node"},
	}))
}
