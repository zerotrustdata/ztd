// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// listedOperator is an Operator as the Gateway lists it: stale_at set on an
// active remote Operator, and the stored status it carries.
func listedOperator(id string, status constants.OperatorStatus, staleAt time.Time) *operatorv1.OperatorDocument {
	op := &operatorv1.OperatorDocument{Id: id, Status: string(status), OperatorType: string(constants.OperatorTypeRemote)}
	if !staleAt.IsZero() {
		op.StaleAt = timestamppb.New(staleAt)
	}
	return op
}

func TestOperatorStatusAt_DerivesStaleFromStaleAt(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status constants.OperatorStatus
		staleA time.Time
		want   constants.OperatorStatus
	}{
		{"active before stale_at reads active", constants.OperatorStatusActive, now.Add(time.Minute), constants.OperatorStatusActive},
		{"active at stale_at reads stale", constants.OperatorStatusActive, now, constants.OperatorStatusStale},
		{"active after stale_at reads stale", constants.OperatorStatusActive, now.Add(-time.Second), constants.OperatorStatusStale},
		{"stale listed, heartbeat moved stale_at ahead, reads active", constants.OperatorStatusStale, now.Add(time.Minute), constants.OperatorStatusActive},
		{"no stale_at keeps the listed status", constants.OperatorStatusActive, time.Time{}, constants.OperatorStatusActive},
		{"stopped never reads stale", constants.OperatorStatusStopped, time.Time{}, constants.OperatorStatusStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := listedOperator("op-1", tc.status, tc.staleA)
			assert.Equal(t, tc.want, operatorStatusAt(op, now))
		})
	}
}

func TestDisplayOperators_DerivesStatusWithoutChangingListedDocuments(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	listed := []*operatorv1.OperatorDocument{
		listedOperator("silent", constants.OperatorStatusActive, now.Add(-time.Second)),
		listedOperator("fresh", constants.OperatorStatusActive, now.Add(time.Minute)),
	}

	displayed := displayOperators(listed, now)

	require.Len(t, displayed, 2)
	assert.Equal(t, string(constants.OperatorStatusStale), displayed[0].Status)
	assert.Equal(t, string(constants.OperatorStatusActive), displayed[1].Status)
	assert.Equal(t, string(constants.OperatorStatusActive), listed[0].Status, "the listed document is not changed by rendering")
}

func TestNextOperatorStaleAt_EarliestFutureDeadlineOrNone(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)

	t.Run("earliest future stale_at wins", func(t *testing.T) {
		ops := []*operatorv1.OperatorDocument{
			listedOperator("late", constants.OperatorStatusActive, now.Add(3*time.Minute)),
			listedOperator("past", constants.OperatorStatusStale, now.Add(-time.Minute)),
			listedOperator("early", constants.OperatorStatusActive, now.Add(time.Minute)),
		}
		at, ok := nextOperatorStaleAt(ops, now)
		require.True(t, ok)
		assert.Equal(t, now.Add(time.Minute), at)
	})

	t.Run("none when no Operator has a stale_at", func(t *testing.T) {
		ops := []*operatorv1.OperatorDocument{listedOperator("stopped", constants.OperatorStatusStopped, time.Time{})}
		_, ok := nextOperatorStaleAt(ops, now)
		assert.False(t, ok)
	})

	t.Run("none when every stale_at has passed", func(t *testing.T) {
		ops := []*operatorv1.OperatorDocument{listedOperator("silent", constants.OperatorStatusStale, now.Add(-time.Second))}
		_, ok := nextOperatorStaleAt(ops, now)
		assert.False(t, ok)
	})
}

func TestApplyOperatorHeartbeat_UpdatesListedOperatorInPlace(t *testing.T) {
	fixedTime := time.Date(2026, 6, 15, 10, 30, 0, 0, time.UTC)
	originalTimeNow := timeNow
	t.Cleanup(func() { timeNow = originalTimeNow })
	timeNow = func() time.Time { return fixedTime }

	m := NewModel(Options{})
	m = m.applyOperatorsMsg(OperatorsMsg{Operators: []*operatorv1.OperatorDocument{
		listedOperator("op-1", constants.OperatorStatusActive, fixedTime.Add(time.Minute)),
		listedOperator("op-2", constants.OperatorStatusActive, fixedTime.Add(time.Minute)),
	}})
	require.Len(t, m.operators, 2)
	selected := m.operators[0]

	beat := fixedTime.Add(10 * time.Second)
	stale := beat.Add(2 * time.Minute)
	next, cmd := m.Update(OperatorHeartbeatMsg{Heartbeat: models.OperatorHeartbeatReceivedPayload{
		OperatorID: "op-1", LastHeartbeatAt: beat, StaleAt: &stale,
	}})
	m = next.(Model)

	require.NotNil(t, cmd, "the heartbeat re-arms the stale deadline for the new stale_at")
	assert.Same(t, selected, m.operators[0], "the heartbeat is applied to the listed document in place")
	assert.Equal(t, beat, m.operators[0].GetLastHeartbeatAt().AsTime())
	assert.Equal(t, stale, m.operators[0].GetStaleAt().AsTime())
	assert.Nil(t, m.operators[1].GetLastHeartbeatAt(), "other Operators are untouched")
}

func TestApplyOperatorHeartbeat_ReadsStaleAsActiveAgain(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	m := NewModel(Options{})
	m.operators = []*operatorv1.OperatorDocument{listedOperator("op-1", constants.OperatorStatusStale, now.Add(-time.Second))}
	require.Equal(t, string(constants.OperatorStatusStale), displayOperators(m.operators, now)[0].Status)

	stale := now.Add(time.Minute)
	applyOperatorHeartbeat(m.operators, models.OperatorHeartbeatReceivedPayload{
		OperatorID: "op-1", LastHeartbeatAt: now, StaleAt: &stale,
	})

	assert.Equal(t, string(constants.OperatorStatusActive), displayOperators(m.operators, now)[0].Status)
}

func TestOperatorHeartbeat_IsDecodedFromStreamAndNotLedgered(t *testing.T) {
	data := `{"type":"ztd.v1.operator.heartbeat.received","data":{"operator_id":"op-1","last_heartbeat_at":"2026-06-15T10:00:00Z","stale_at":"2026-06-15T10:01:30Z"}}`
	msgs := translateSSEEvents(string(constants.EventOperatorHeartbeatReceived), data)
	require.Len(t, msgs, 1)
	hb, ok := msgs[0].(OperatorHeartbeatMsg)
	require.True(t, ok, "a heartbeat is not written to the ledger")
	assert.Equal(t, "op-1", hb.Heartbeat.OperatorID)
	require.NotNil(t, hb.Heartbeat.StaleAt)
	assert.Equal(t, time.Date(2026, 6, 15, 10, 1, 30, 0, time.UTC), *hb.Heartbeat.StaleAt)
}

func TestOperatorStaleDeadline_ArmsOneGenerationAndIgnoresSuperseded(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)

	t.Run("no stale_at arms no deadline", func(t *testing.T) {
		m := NewModel(Options{})
		m.operators = []*operatorv1.OperatorDocument{listedOperator("op-1", constants.OperatorStatusActive, time.Time{})}
		next, cmd := m.armOperatorStaleDeadline(now)
		assert.Nil(t, cmd)
		assert.Equal(t, uint64(1), next.(Model).operatorStaleGen)
	})

	t.Run("a superseded deadline is ignored, the current one re-renders and re-arms", func(t *testing.T) {
		m := NewModel(Options{})
		m.operators = []*operatorv1.OperatorDocument{listedOperator("op-1", constants.OperatorStatusActive, now.Add(time.Minute))}
		armed, cmd := m.armOperatorStaleDeadline(now)
		require.NotNil(t, cmd)
		m = armed.(Model)
		superseded := m.operatorStaleGen

		rearmed, cmd := m.armOperatorStaleDeadline(now)
		require.NotNil(t, cmd)
		m = rearmed.(Model)
		require.NotEqual(t, superseded, m.operatorStaleGen)

		ignored, cmd := m.Update(OperatorStaleDeadlineMsg{Generation: superseded})
		assert.Nil(t, cmd, "a deadline from an earlier generation does nothing")
		assert.Equal(t, m.operatorStaleGen, ignored.(Model).operatorStaleGen)

		_, cmd = m.Update(OperatorStaleDeadlineMsg{Generation: m.operatorStaleGen})
		assert.Nil(t, cmd, "no later stale_at, so nothing is re-armed")
	})
}

func TestOperatorView_RendersDerivedStaleStatusAfterStaleAt(t *testing.T) {
	fixedTime := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	originalTimeNow := timeNow
	t.Cleanup(func() { timeNow = originalTimeNow })
	timeNow = func() time.Time { return fixedTime }

	m := NewModel(Options{})
	m.view = viewOperators
	m.operators = []*operatorv1.OperatorDocument{listedOperator("op-1", constants.OperatorStatusActive, fixedTime.Add(-time.Second))}
	m.operatorsLoaded = true

	assert.True(t, strings.Contains(m.View(), string(constants.OperatorStatusStale)), "the silent Operator renders stale")
	assert.Equal(t, string(constants.OperatorStatusActive), m.operators[0].Status, "the model keeps the listed status")
}
