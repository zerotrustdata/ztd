// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestGatewayModeService_HandleHeartbeatPublish(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})

	t.Run("Valid heartbeat updates operator document", func(t *testing.T) {
		opDoc := &operatorv1.OperatorDocument{
			Id:     "op-123",
			Status: string(constants.OperatorStatusActive),
		}
		opBytes, err := models.MarshalOperatorDocument(opDoc)
		require.NoError(t, err)
		err = ls.GetDocStore().DocSet(t.Context(), "operators", "op-123", opBytes)
		require.NoError(t, err)

		heartbeat := &operatorv1.HeartbeatResult{
			OperatorId: "op-123",
			Status:     "automatic",
			SystemIdentity: &operatorv1.SystemIdentity{
				Hostname: "worker-1",
			},
		}
		payload, err := proto.Marshal(heartbeat)
		require.NoError(t, err)

		envelope := &commonv1.GovernanceEnvelope{
			OperatorId: "op-123",
			Payload:    payload,
		}
		heartbeatBytes, err := protojson.Marshal(envelope)
		require.NoError(t, err)

		ls.handleHeartbeatPublish("test-channel", heartbeatBytes)

		updatedDoc, err := ls.GetDocStore().DocGet(t.Context(), "operators", "op-123")
		require.NoError(t, err)
		assert.NotNil(t, updatedDoc)
		assert.Contains(t, updatedDoc.Data, "latest_heartbeat_snapshot")
		assert.Contains(t, updatedDoc.Data, "current_hostname")

		var snapshot map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(updatedDoc.Data["latest_heartbeat_snapshot"], &snapshot))
		var identity map[string]string
		require.NoError(t, json.Unmarshal(snapshot["system_identity"], &identity))
		assert.Equal(t, "worker-1", identity["hostname"])
		var currentHostname string
		require.NoError(t, json.Unmarshal(updatedDoc.Data["current_hostname"], &currentHostname))
		assert.Equal(t, "worker-1", currentHostname)
	})

	t.Run("Malformed JSON logs and returns", func(t *testing.T) {
		ls.handleHeartbeatPublish("test-channel", []byte("{invalid json"))
	})

	t.Run("Missing operator_id returns without write", func(t *testing.T) {
		envelope := &commonv1.GovernanceEnvelope{
			IntentData: &structpb.Struct{},
		}
		heartbeatBytes, err := protojson.Marshal(envelope)
		require.NoError(t, err)

		ls.handleHeartbeatPublish("test-channel", heartbeatBytes)
	})
}

func publishTestHeartbeat(t *testing.T, ls *GatewayModeService, operatorID string) {
	t.Helper()
	payload, err := proto.Marshal(&operatorv1.HeartbeatResult{
		OperatorId:     operatorID,
		Status:         "automatic",
		SystemIdentity: &operatorv1.SystemIdentity{Hostname: "worker-1"},
	})
	require.NoError(t, err)
	wire, err := protojson.Marshal(&commonv1.GovernanceEnvelope{OperatorId: operatorID, Payload: payload})
	require.NoError(t, err)
	ls.handleHeartbeatPublish("test-channel", wire)
}

func TestGatewayModeService_HandleHeartbeatPublish_LeavesBoundRootUnchanged(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	store := ls.GetDocStore()
	putOperator(t, store, "op-root", remoteOperator(constants.OperatorStatusActive), time.Hour)
	publishTestHeartbeat(t, ls, "op-root")

	boundBefore, err := ls.stateRootSvc.GetCurrentStateRoot(t.Context())
	require.NoError(t, err)
	observedBefore, err := ls.stateRootSvc.GetObservedStateRoot(t.Context())
	require.NoError(t, err)

	publishTestHeartbeat(t, ls, "op-root")

	boundAfter, err := ls.stateRootSvc.GetCurrentStateRoot(t.Context())
	require.NoError(t, err)
	observedAfter, err := ls.stateRootSvc.GetObservedStateRoot(t.Context())
	require.NoError(t, err)
	assert.Equal(t, boundBefore, boundAfter, "heartbeats must not move the bound root")
	assert.NotEqual(t, observedBefore, observedAfter, "heartbeat telemetry is committed in the observed root")

	doc, err := store.DocGet(t.Context(), operatorsCollection, "op-root")
	require.NoError(t, err)
	assert.Contains(t, doc.Data, "last_heartbeat_at")
	assert.Contains(t, doc.Data, "latest_heartbeat_snapshot")
}

func TestGatewayModeService_HandleHeartbeatPublish_StampsGatewayClockAndReadsActive(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	store := ls.GetDocStore()

	silent := remoteOperator(constants.OperatorStatusActive)
	silent.LastHeartbeatAt = timeAgo(constants.OperatorHeartbeatStaleAfter * 10)
	putOperator(t, store, "op-silent", silent, time.Hour)
	doc, err := store.DocGet(t.Context(), operatorsCollection, "op-silent")
	require.NoError(t, err)
	require.JSONEq(t, `"stale"`, string(doc.Data["status"]), "the fixture must read as stale before the heartbeat")

	before := time.Now().UTC()
	publishTestHeartbeat(t, ls, "op-silent")

	doc, err = store.DocGet(t.Context(), operatorsCollection, "op-silent")
	require.NoError(t, err)
	require.NotNil(t, doc)

	var status constants.OperatorStatus
	require.NoError(t, json.Unmarshal(doc.Data["status"], &status))
	assert.Equal(t, constants.OperatorStatusActive, status, "one heartbeat must read as active")
	assert.Equal(t, constants.OperatorStatusActive, persistedOperatorStatus(t, store, "op-silent"), "a heartbeat writes no status")

	var stamped time.Time
	require.NoError(t, json.Unmarshal(doc.Data["last_heartbeat_at"], &stamped))
	assert.False(t, stamped.Before(before.Add(-time.Second)), "last_heartbeat_at must be the Gateway receive time")
}

func TestGatewayModeService_HandleHeartbeatPublish_DoesNotReviveStoppedOrTerminated(t *testing.T) {
	for _, status := range []constants.OperatorStatus{
		constants.OperatorStatusStopped,
		constants.OperatorStatusTerminated,
	} {
		t.Run(string(status), func(t *testing.T) {
			ls := newTestGatewayService(t, testGatewayOpts{})
			store := ls.GetDocStore()
			putOperator(t, store, "op-1", remoteOperator(status), time.Hour)

			publishTestHeartbeat(t, ls, "op-1")

			assert.Equal(t, status, persistedOperatorStatus(t, store, "op-1"))
		})
	}
}
