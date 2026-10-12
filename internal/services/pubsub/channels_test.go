// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestCmdChannel(t *testing.T) {
	t.Parallel()
	t.Run("formats channel with operator ID and session ID", func(t *testing.T) {
		t.Parallel()
		ch := CmdChannel("op-1", "session-1")
		assert.Equal(t, "cmd:op-1:session-1", ch)
	})

	t.Run("formats with empty strings", func(t *testing.T) {
		t.Parallel()
		ch := CmdChannel("", "")
		assert.Equal(t, "cmd::", ch)
	})

	t.Run("uses correct prefix", func(t *testing.T) {
		t.Parallel()
		ch := CmdChannel("op-1", "session-1")
		assert.Contains(t, ch, constants.ChannelPrefixCmd)
	})
}

func TestResultsChannel(t *testing.T) {
	t.Parallel()
	t.Run("formats channel with operator ID and session ID", func(t *testing.T) {
		t.Parallel()
		ch := ResultsChannel("op-1", "session-1")
		assert.Equal(t, "results:op-1:session-1", ch)
	})

	t.Run("formats with empty strings", func(t *testing.T) {
		t.Parallel()
		ch := ResultsChannel("", "")
		assert.Equal(t, "results::", ch)
	})

	t.Run("uses correct prefix", func(t *testing.T) {
		t.Parallel()
		ch := ResultsChannel("op-1", "session-1")
		assert.Contains(t, ch, constants.ChannelPrefixResults)
	})
}

func TestHeartbeatChannel(t *testing.T) {
	t.Parallel()
	t.Run("formats channel with operator ID and session ID", func(t *testing.T) {
		t.Parallel()
		ch := HeartbeatChannel("op-1", "session-1")
		assert.Equal(t, "heartbeat:op-1:session-1", ch)
	})

	t.Run("formats with empty strings", func(t *testing.T) {
		t.Parallel()
		ch := HeartbeatChannel("", "")
		assert.Equal(t, "heartbeat::", ch)
	})

	t.Run("uses correct prefix", func(t *testing.T) {
		t.Parallel()
		ch := HeartbeatChannel("op-1", "session-1")
		assert.Contains(t, ch, constants.ChannelPrefixHeartbeat)
	})
}

func TestReceiptsChannel(t *testing.T) {
	t.Parallel()
	t.Run("formats channel with operator ID and session ID", func(t *testing.T) {
		t.Parallel()
		ch := ReceiptsChannel("op-1", "session-1")
		assert.Equal(t, "receipts:op-1:session-1", ch)
	})

	t.Run("formats with empty strings", func(t *testing.T) {
		t.Parallel()
		ch := ReceiptsChannel("", "")
		assert.Equal(t, "receipts::", ch)
	})

	t.Run("uses correct prefix", func(t *testing.T) {
		t.Parallel()
		ch := ReceiptsChannel("op-1", "session-1")
		assert.Contains(t, ch, constants.ChannelPrefixReceipts)
	})
}

func TestRenewalChannel(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "renewal:op-1:session-1", RenewalChannel("op-1", "session-1"))
}

func TestChannelsAreDistinct(t *testing.T) {
	t.Parallel()
	opID := "op-1"
	sessionID := "session-1"
	channels := []string{
		CmdChannel(opID, sessionID),
		ResultsChannel(opID, sessionID),
		HeartbeatChannel(opID, sessionID),
		ReceiptsChannel(opID, sessionID),
		RenewalChannel(opID, sessionID),
	}

	seen := make(map[string]bool, len(channels))
	for _, ch := range channels {
		assert.False(t, seen[ch], "duplicate channel %s", ch)
		seen[ch] = true
	}
}
