// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// TestOperatorStatusEventRegistry_MatchesPublishedStatuses holds the registry
// to what Publish does: each status it maps has a registry entry declaring the
// sse transport the event is delivered on, and each status it rejects has no
// registry entry at all.
func TestOperatorStatusEventRegistry_MatchesPublishedStatuses(t *testing.T) {
	t.Parallel()

	require.Len(t, operatorStatusEvents, 5)
	for status, eventType := range operatorStatusEvents {
		entry, ok := constants.Registry.Lookup(eventType)
		require.True(t, ok, "%s has no registry entry", eventType)
		assert.Equal(t, []string{"sse"}, entry.Transport, "%s (status %s) transport", eventType, status)
		assert.Equal(t, []string{"gateway"}, entry.Producers, "%s producers", eventType)
	}

	for _, status := range []constants.OperatorStatus{
		constants.OperatorStatusBound,
		constants.OperatorStatusAvailable,
		constants.OperatorStatusUnavailable,
	} {
		_, ok := constants.Registry.Lookup(constants.EventType("ztd.v1.operator.status.updated." + string(status)))
		assert.False(t, ok, "status %s has no Gateway producer and must have no registry entry", status)
	}
}
