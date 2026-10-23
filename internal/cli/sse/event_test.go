// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sse

import "testing"

func TestDecodeEvent(t *testing.T) {
	tests := []struct {
		name, eventType, data string
		ok                    bool
		wantType, wantData    string
	}{
		{"push payload", "", `{"user_id":"u","event":{"type":"a.b","data":{"x":1}}}`, true, "a.b", `{"x":1}`},
		{"type from event line", "c.d", `{"event":{"data":{}}}`, true, "c.d", `{}`},
		{"sentinel without envelope", "", `{"type":"error","data":{"reason":"r"}}`, true, "error", `{"reason":"r"}`},
		{"not json", "", `keepalive`, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, ok := DecodeEvent(tt.eventType, tt.data)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if ev.Type != tt.wantType || string(ev.Data) != tt.wantData {
				t.Fatalf("event = %q %s, want %q %s", ev.Type, ev.Data, tt.wantType, tt.wantData)
			}
			if len(ev.Raw) == 0 {
				t.Fatal("raw inner event not kept")
			}
		})
	}
}
