// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tui

import (
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// operatorStatusAt is the status op reads as at now. The Gateway stamps
// stale_at only on an active remote Operator and never stores staleness, so an
// Operator with stale_at reads as stale once now is at or past it, and as
// active before it, whether the listed status was active or stale. A listed
// status that is anything else, or an Operator without stale_at, reads as
// listed. This mirrors operatorStatusAt in the console.
func operatorStatusAt(op *operatorv1.OperatorDocument, now time.Time) constants.OperatorStatus {
	status := constants.OperatorStatus(op.GetStatus())
	staleAt := op.GetStaleAt()
	if staleAt == nil || (status != constants.OperatorStatusActive && status != constants.OperatorStatusStale) {
		return status
	}
	if !now.Before(staleAt.AsTime()) {
		return constants.OperatorStatusStale
	}
	return constants.OperatorStatusActive
}

// displayOperators returns ops with each status derived at now. An Operator
// whose derived status differs from its listed one is copied, so the listed
// documents are never changed by rendering.
func displayOperators(ops []*operatorv1.OperatorDocument, now time.Time) []*operatorv1.OperatorDocument {
	if len(ops) == 0 {
		return ops
	}
	out := make([]*operatorv1.OperatorDocument, len(ops))
	for i, op := range ops {
		derived := string(operatorStatusAt(op, now))
		if derived == op.GetStatus() {
			out[i] = op
			continue
		}
		copied := proto.Clone(op).(*operatorv1.OperatorDocument)
		copied.Status = derived
		out[i] = copied
	}
	return out
}

// nextOperatorStaleAt returns the earliest stale_at after now among ops, and
// false when no listed Operator has a stale_at still ahead.
func nextOperatorStaleAt(ops []*operatorv1.OperatorDocument, now time.Time) (time.Time, bool) {
	var next time.Time
	found := false
	for _, op := range ops {
		staleAt := op.GetStaleAt()
		if staleAt == nil {
			continue
		}
		at := staleAt.AsTime()
		if !at.After(now) {
			continue
		}
		if !found || at.Before(next) {
			next = at
			found = true
		}
	}
	return next, found
}

// applyOperatorHeartbeat sets the heartbeat fields of the listed Operator that
// heartbeat names, in place. A heartbeat for an Operator not in ops changes
// nothing.
func applyOperatorHeartbeat(ops []*operatorv1.OperatorDocument, hb models.OperatorHeartbeatReceivedPayload) {
	for _, op := range ops {
		if op.GetId() != hb.OperatorID {
			continue
		}
		op.LastHeartbeatAt = timestamppb.New(hb.LastHeartbeatAt)
		op.StaleAt = nil
		if hb.StaleAt != nil {
			op.StaleAt = timestamppb.New(*hb.StaleAt)
		}
	}
}
