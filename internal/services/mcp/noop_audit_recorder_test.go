// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import "github.com/zerotrustdata/ztd/internal/services/storage"

// noopAuditEventRecorder satisfies mcp.AuditEventRecorder for tests that build a
// GatewayService but do not exercise audit recording. Production code never
// defaults to it: a nil EventsStore is a construction error.
type noopAuditEventRecorder struct{}

func (noopAuditEventRecorder) RecordEvent(*storage.Event) (int64, error) { return 0, nil }

func (noopAuditEventRecorder) RecordEventOnce(*storage.Event) (int64, bool, error) {
	return 0, true, nil
}
