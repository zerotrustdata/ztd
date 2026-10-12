// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sse

import (
	"encoding/json"

	"github.com/zerotrustdata/ztd/internal/models"
)

// Event is one Gateway event carried by an SSE frame.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	// Raw is the whole inner event object. Most producers emit {type, data};
	// approval.completed is a flat models.ApprovalCompletedEvent, so its
	// fields are read from Raw.
	Raw json.RawMessage `json:"-"`
}

// DecodeEvent unwraps a Gateway SSE frame. Stored and live events are a
// models.SSEPushPayload whose Event holds the inner event; stream sentinels
// (error, truncated) are the inner event themselves. The Gateway omits the
// event: line (R14), so eventType is only a fallback for the inner type.
// It reports false for a frame that is not a JSON event.
func DecodeEvent(eventType, data string) (Event, bool) {
	var push models.SSEPushPayload
	if err := json.Unmarshal([]byte(data), &push); err != nil {
		return Event{}, false
	}
	inner := push.Event
	if len(inner) == 0 {
		inner = json.RawMessage(data)
	}
	var ev Event
	if err := json.Unmarshal(inner, &ev); err != nil {
		return Event{}, false
	}
	ev.Raw = inner
	if ev.Type == "" {
		ev.Type = eventType
	}
	return ev, true
}
