// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
)

const (
	statusEventOwner = "user-owner"
	statusEventOther = "user-other"
)

// recordedOperatorEvent is one operator status event decoded from the SSE
// event store.
type recordedOperatorEvent struct {
	eventType string
	payload   models.OperatorStatusUpdatedPayload
}

// transitionRecorder is an OperatorStatusObserver that keeps what it is told.
type transitionRecorder struct {
	mu          sync.Mutex
	transitions []OperatorStatusTransition
}

func (r *transitionRecorder) OperatorStatusChanged(_ context.Context, t OperatorStatusTransition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transitions = append(r.transitions, t)
}

func (r *transitionRecorder) OperatorHeartbeatRecorded(context.Context, string) {}

func (r *transitionRecorder) recorded() []OperatorStatusTransition {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]OperatorStatusTransition(nil), r.transitions...)
}

// putWebSession persists a web session for userID that expires expiresIn from
// now (negative for an already expired session) and returns its id.
func putWebSession(t *testing.T, store *DocumentStoreService, id, userID string, expiresIn time.Duration) {
	t.Helper()
	body, err := json.Marshal(models.WebSession{
		ID:              id,
		UserID:          userID,
		CreatedAtUnixMs: time.Now().Add(-time.Hour).UnixMilli(),
		ExpiresAtUnixMs: time.Now().Add(expiresIn).UnixMilli(),
	})
	require.NoError(t, err)
	require.NoError(t, store.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionWebSessions), id, body))
}

// putSilentOperator persists an active remote Operator owned by userID whose
// last heartbeat is long past the stale window.
func putSilentOperator(t *testing.T, store *DocumentStoreService, _, _, _ string) {
	t.Helper()
	op := remoteOperator(constants.OperatorStatusActive)
	op.UserId = statusEventOwner
	op.Name = "edge-1"
	op.LastHeartbeatAt = timeAgo(constants.OperatorHeartbeatStaleAfter * 2)
	putOperator(t, store, "op-silent", op, time.Hour)
}

// webSessionEvents returns the operator status events delivered to one web
// session, oldest first.
func webSessionEvents(t *testing.T, ls *GatewayModeService, userID, webSessionID string) []recordedOperatorEvent {
	t.Helper()
	rows, err := ls.sseStore.SSEEventsListSince(SSERoute{UserID: userID, WebSessionID: webSessionID}, 0, 100)
	require.NoError(t, err)

	events := make([]recordedOperatorEvent, 0, len(rows))
	for _, row := range rows {
		var push models.SSEPushPayload
		require.NoError(t, json.Unmarshal([]byte(row.Payload), &push))
		var envelope sseEventEnvelope
		require.NoError(t, json.Unmarshal(push.Event, &envelope))
		assert.Equal(t, row.EventType, envelope.Type)

		var payload models.OperatorStatusUpdatedPayload
		require.NoError(t, json.Unmarshal(envelope.Data, &payload))
		events = append(events, recordedOperatorEvent{eventType: envelope.Type, payload: payload})
	}
	return events
}

// Staleness is derived at read time, so reading a silent Operator stores and
// announces nothing.
func TestOperatorStatusEvents_ReadingASilentOperatorAnnouncesNothing(t *testing.T) {
	store := newDocumentStoreService(t)
	recorder := &transitionRecorder{}
	require.NoError(t, store.BindOperatorStatusObserver(recorder))
	putSilentOperator(t, store, "op-silent", statusEventOwner, "edge-1")

	doc, err := store.DocGet(t.Context(), operatorsCollection, "op-silent")
	require.NoError(t, err)
	require.Equal(t, constants.OperatorStatusStale, docStatus(t, doc))
	_, err = store.DocQuery(t.Context(), operatorsCollection, nil, "", 0)
	require.NoError(t, err)

	assert.Empty(t, recorder.recorded())
}

func TestOperatorStatusEvents_StopAndTerminateReachOwnerWebSession(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	store := ls.GetDocStore()
	putWebSession(t, store, "web-owner", statusEventOwner, time.Hour)

	for _, id := range []string{"op-stopped", "op-terminated"} {
		op := remoteOperator(constants.OperatorStatusActive)
		op.UserId = statusEventOwner
		op.Name = id
		putOperator(t, store, id, op, time.Second)
	}

	require.NoError(t, ls.reg.MarkOperatorStopped(t.Context(), "op-stopped", statusEventOwner, "maintenance"))
	require.NoError(t, ls.reg.TerminateOperator(t.Context(), "op-terminated", statusEventOwner, "decommissioned"))

	events := webSessionEvents(t, ls, statusEventOwner, "web-owner")
	require.Len(t, events, 2)
	assert.Equal(t, string(constants.EventOperatorStatusUpdatedStopped), events[0].eventType)
	assert.Equal(t, "op-stopped", events[0].payload.OperatorID)
	assert.Equal(t, constants.OperatorStatusStopped, events[0].payload.Status)
	assert.Equal(t, string(constants.EventOperatorStatusUpdatedTerminated), events[1].eventType)
	assert.Equal(t, "op-terminated", events[1].payload.OperatorID)
	assert.Equal(t, constants.OperatorStatusTerminated, events[1].payload.Status)
}

func TestOperatorStatusEvents_StopByNonOwnerIsNotPushed(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	store := ls.GetDocStore()
	putWebSession(t, store, "web-owner", statusEventOwner, time.Hour)
	op := remoteOperator(constants.OperatorStatusActive)
	op.UserId = statusEventOwner
	putOperator(t, store, "op-1", op, time.Second)

	require.ErrorIs(t, ls.reg.MarkOperatorStopped(t.Context(), "op-1", statusEventOther, ""), constants.ErrRegistrationOperatorNotBelongToUser)

	assert.Empty(t, webSessionEvents(t, ls, statusEventOwner, "web-owner"))
}

func assertOfflineAnnouncement(t *testing.T, ls *GatewayModeService, ownerID, operatorID, name string) {
	t.Helper()
	events := webSessionEvents(t, ls, ownerID, "web-owner")
	require.Len(t, events, 1)
	assert.Equal(t, string(constants.EventOperatorStatusUpdatedOffline), events[0].eventType,
		"an enrolled Operator is announced offline; active waits for its command subscription")
	assert.Equal(t, operatorID, events[0].payload.OperatorID)
	assert.Equal(t, constants.OperatorStatusOffline, events[0].payload.Status)
	assert.Equal(t, name, events[0].payload.Name)
	assert.False(t, events[0].payload.Timestamp.IsZero())
	assert.Empty(t, webSessionEvents(t, ls, ownerID, "web-owner-expired"))
	assert.Empty(t, webSessionEvents(t, ls, statusEventOther, "web-other"))
}

func TestOperatorStatusEvents_EnrollmentAnnouncesOfflineToOwner(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)
	operatorCSR, operatorKey, cliCSR, cliKey := generateOperatorCSRsAndKeys(t)
	_, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentOperator, "operator-1", "operator.local", "", operatorCSR, cliCSR)
	putWebSession(t, env.docStore, "web-owner", env.ownerID, time.Hour)
	putWebSession(t, env.docStore, "web-owner-expired", env.ownerID, -time.Minute)
	putWebSession(t, env.docStore, "web-other", statusEventOther, time.Hour)
	require.Empty(t, webSessionEvents(t, env.svc, env.ownerID, "web-owner"))

	resp, err := env.enrollSvc.Complete(context.Background(), token, models.PlatformEnrollmentProofs{
		Operator: signCompletionTranscript(t, approved, operatorKey),
		CLI:      signCompletionTranscript(t, approved, cliKey),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Operator)
	assertOfflineAnnouncement(t, env.svc, env.ownerID, resp.Operator.OperatorID, "operator.local")
}

func TestOperatorStatusEvents_SlotClaimAnnouncesOfflineToOwner(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)
	owner, err := env.userSvc.GetByID(t.Context(), env.ownerID)
	require.NoError(t, err)
	slot, err := env.svc.reg.createSlot(t.Context(), env.ownerID, owner.OrganizationID)
	require.NoError(t, err)
	putWebSession(t, env.docStore, "web-owner", env.ownerID, time.Hour)
	putWebSession(t, env.docStore, "web-owner-expired", env.ownerID, -time.Minute)
	putWebSession(t, env.docStore, "web-other", statusEventOther, time.Hour)
	require.Empty(t, webSessionEvents(t, env.svc, env.ownerID, "web-owner"))
	operatorCSR, _, _, _ := generateOperatorCSRsAndKeys(t)

	resp, err := env.svc.reg.RegisterDeviceCSR(t.Context(), env.ownerID, owner.OrganizationID, models.OperatorRegistrationRequest{
		SystemFingerprint: "slot-claim-fingerprint",
		Hostname:          "slot-host",
		CSR:               operatorCSR,
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Equal(t, slot.Id, resp.OperatorID, "claim the pre-created offline slot")
	assertOfflineAnnouncement(t, env.svc, env.ownerID, slot.Id, slot.Name)
}

func TestOperatorStatusEvents_HeartbeatFromHealthyOperatorIsNotPushed(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	store := ls.GetDocStore()
	putWebSession(t, store, "web-owner", statusEventOwner, time.Hour)
	op := remoteOperator(constants.OperatorStatusActive)
	op.UserId = statusEventOwner
	putOperator(t, store, "op-healthy", op, time.Second)

	publishTestHeartbeat(t, ls, "op-healthy")

	assert.Empty(t, webSessionEvents(t, ls, statusEventOwner, "web-owner"))
}

// connectWebSession registers the in-process handler an open dashboard's SSE
// stream registers for webSessionID, and returns the heartbeat events it has
// received so far.
func connectWebSession(t *testing.T, ls *GatewayModeService, webSessionID string) func() []models.OperatorHeartbeatReceivedPayload {
	t.Helper()
	var mu sync.Mutex
	var received []models.OperatorHeartbeatReceivedPayload
	unregister := ls.pubsub.RegisterHandler(pubsub.SSEWebChannel(webSessionID), func(_ string, data []byte) {
		var published models.SSEPublishedEvent
		var push models.SSEPushPayload
		var envelope sseEventEnvelope
		var payload models.OperatorHeartbeatReceivedPayload
		if !assert.NoError(t, json.Unmarshal(data, &published)) ||
			!assert.NoError(t, json.Unmarshal(published.Payload, &push)) ||
			!assert.NoError(t, json.Unmarshal(push.Event, &envelope)) {
			return
		}
		if envelope.Type != string(constants.EventOperatorHeartbeatReceived) {
			return
		}
		assert.Zero(t, published.ID, "heartbeat events are ephemeral")
		if !assert.NoError(t, json.Unmarshal(envelope.Data, &payload)) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		received = append(received, payload)
	})
	t.Cleanup(unregister)
	return func() []models.OperatorHeartbeatReceivedPayload {
		mu.Lock()
		defer mu.Unlock()
		return append([]models.OperatorHeartbeatReceivedPayload(nil), received...)
	}
}

// A recorded heartbeat reaches the owner's connected dashboards, and only
// those, as an ephemeral event carrying the last_heartbeat_at and stale_at a
// re-list returns.
func TestOperatorStatusEvents_HeartbeatReachesOwnersConnectedDashboards(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	store := ls.GetDocStore()
	putWebSession(t, store, "web-owner", statusEventOwner, time.Hour)
	putWebSession(t, store, "web-owner-expired", statusEventOwner, -time.Minute)
	putWebSession(t, store, "web-other", statusEventOther, time.Hour)
	owner := connectWebSession(t, ls, "web-owner")
	ownerExpired := connectWebSession(t, ls, "web-owner-expired")
	other := connectWebSession(t, ls, "web-other")
	op := remoteOperator(constants.OperatorStatusActive)
	op.UserId = statusEventOwner
	putOperator(t, store, "op-1", op, time.Second)

	publishTestHeartbeat(t, ls, "op-1")

	listed, err := store.operatorDocument(t.Context(), "op-1")
	require.NoError(t, err)
	require.NotNil(t, listed.GetStaleAt())
	got := owner()
	require.Len(t, got, 1)
	assert.Equal(t, "op-1", got[0].OperatorID)
	assert.Equal(t, listed.GetLastHeartbeatAt().AsTime(), got[0].LastHeartbeatAt)
	require.NotNil(t, got[0].StaleAt)
	assert.Equal(t, listed.GetStaleAt().AsTime(), *got[0].StaleAt)
	assert.Empty(t, ownerExpired())
	assert.Empty(t, other())
	assert.Empty(t, webSessionEvents(t, ls, statusEventOwner, "web-owner"), "nothing is stored")
}

// liveSSEEvent is one event a stream handler received on a pub/sub channel.
type liveSSEEvent struct {
	id        int64
	eventType string
	data      json.RawMessage
}

// connectSSEChannel registers the in-process handler an open SSE stream
// registers for channel, and returns the events it has received so far.
func connectSSEChannel(t *testing.T, ls *GatewayModeService, channel string) func() []liveSSEEvent {
	t.Helper()
	var mu sync.Mutex
	var received []liveSSEEvent
	unregister := ls.pubsub.RegisterHandler(channel, func(_ string, data []byte) {
		var published models.SSEPublishedEvent
		var push models.SSEPushPayload
		var envelope sseEventEnvelope
		if !assert.NoError(t, json.Unmarshal(data, &published)) ||
			!assert.NoError(t, json.Unmarshal(published.Payload, &push)) ||
			!assert.NoError(t, json.Unmarshal(push.Event, &envelope)) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		received = append(received, liveSSEEvent{id: published.ID, eventType: envelope.Type, data: envelope.Data})
	})
	t.Cleanup(unregister)
	return func() []liveSSEEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]liveSSEEvent(nil), received...)
	}
}

// The owner's connected CLI sessions hear a status transition live, and a CLI
// of another user does not.
func TestOperatorStatusPublisher_PublishReachesOwnerConnectedCLISession(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	store := ls.GetDocStore()
	putCLISession(t, store, "cli-owner", statusEventOwner)
	putCLISession(t, store, "cli-other", statusEventOther)
	owner := connectSSEChannel(t, ls, pubsub.SSECLIChannel("cli-owner"))
	other := connectSSEChannel(t, ls, pubsub.SSECLIChannel("cli-other"))
	publisher := NewOperatorStatusPublisher(store, NewSSEEventPublisher(ls.sseStore, ls.pubsub), ls.logger)

	require.NoError(t, publisher.Publish(t.Context(), OperatorStatusTransition{
		OperatorID: "op-1",
		UserID:     statusEventOwner,
		Name:       "edge-1",
		Status:     constants.OperatorStatusStopped,
	}))

	got := owner()
	require.Len(t, got, 1)
	assert.Equal(t, string(constants.EventOperatorStatusUpdatedStopped), got[0].eventType)
	var payload models.OperatorStatusUpdatedPayload
	require.NoError(t, json.Unmarshal(got[0].data, &payload))
	assert.Equal(t, "op-1", payload.OperatorID)
	assert.Equal(t, constants.OperatorStatusStopped, payload.Status)
	assert.Empty(t, other())
}

// A recorded heartbeat reaches the owner's connected CLI sessions, and only
// those, as an ephemeral event carrying the same fields a re-list returns.
func TestOperatorStatusPublisher_HeartbeatReachesOwnerConnectedCLISession(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	store := ls.GetDocStore()
	putCLISession(t, store, "cli-owner", statusEventOwner)
	putCLISession(t, store, "cli-owner-idle", statusEventOwner)
	putCLISession(t, store, "cli-other", statusEventOther)
	owner := connectSSEChannel(t, ls, pubsub.SSECLIChannel("cli-owner"))
	other := connectSSEChannel(t, ls, pubsub.SSECLIChannel("cli-other"))
	op := remoteOperator(constants.OperatorStatusActive)
	op.UserId = statusEventOwner
	putOperator(t, store, "op-1", op, time.Second)
	publisher := NewOperatorStatusPublisher(store, NewSSEEventPublisher(ls.sseStore, ls.pubsub), ls.logger)

	require.NoError(t, publisher.PublishHeartbeat(t.Context(), "op-1"))

	listed, err := store.operatorDocument(t.Context(), "op-1")
	require.NoError(t, err)
	require.NotNil(t, listed.GetStaleAt())
	got := owner()
	require.Len(t, got, 1)
	assert.Equal(t, string(constants.EventOperatorHeartbeatReceived), got[0].eventType)
	assert.Zero(t, got[0].id, "heartbeat events are ephemeral")
	var payload models.OperatorHeartbeatReceivedPayload
	require.NoError(t, json.Unmarshal(got[0].data, &payload))
	assert.Equal(t, "op-1", payload.OperatorID)
	assert.Equal(t, listed.GetLastHeartbeatAt().AsTime(), payload.LastHeartbeatAt)
	require.NotNil(t, payload.StaleAt)
	assert.Equal(t, listed.GetStaleAt().AsTime(), *payload.StaleAt)
	assert.Empty(t, other())
}

func TestOperatorStatusPublisher_RejectsStatusWithoutGatewayEvent(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	publisher := NewOperatorStatusPublisher(ls.GetDocStore(), NewSSEEventPublisher(ls.sseStore, ls.pubsub), ls.logger)

	for _, status := range []constants.OperatorStatus{
		constants.OperatorStatusBound,
		constants.OperatorStatusAvailable,
		constants.OperatorStatusUnavailable,
	} {
		err := publisher.Publish(t.Context(), OperatorStatusTransition{OperatorID: "op-1", UserID: statusEventOwner, Status: status})
		assert.ErrorIs(t, err, constants.ErrOperatorStatusEventUnsupported, status)
	}
}

func TestDocumentStore_BindOperatorStatusObserver_RejectsNilAndSecondBinding(t *testing.T) {
	store := newDocumentStoreService(t)

	require.ErrorIs(t, store.BindOperatorStatusObserver(nil), constants.ErrOperatorStatusObserverNil)
	require.NoError(t, store.BindOperatorStatusObserver(&transitionRecorder{}))
	assert.ErrorIs(t, store.BindOperatorStatusObserver(&transitionRecorder{}), constants.ErrOperatorStatusObserverBound)
}
