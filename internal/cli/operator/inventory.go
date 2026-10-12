// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operator

import (
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// IsConnected reports whether an Operator document from GET
// APIPaths.Operators represents a live connection: an unclaimed slot never
// does, and a claimed Operator does while it is active, bound, or stale.
func IsConnected(op *operatorv1.OperatorDocument) bool {
	if op.IsSlot && !op.Claimed {
		return false
	}
	switch constants.OperatorStatus(op.Status) {
	case constants.OperatorStatusActive, constants.OperatorStatusBound, constants.OperatorStatusStale:
		return true
	default:
		return false
	}
}
