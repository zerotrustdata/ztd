// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The events the console reacts to, named from the generated registry
// (src/generated/events.ts, produced by `make constants-generate` from
// protocol/constants/events.json). Unknown types are ignored, never guessed at.

import { EventType as E } from '../generated/events';

export const Ev = {
  ApprovalsChanged: E.PLATFORM_APPROVALS_CHANGED,

  /** Prefix of ztd.v1.operator.status.updated.<state>. */
  OperatorStatusPrefix: E.OPERATOR_STATUS_UPDATED_ACTIVE.slice(0, -'active'.length),
  /** Ephemeral; carries the new last_heartbeat_at and stale_at (lib/liveness.ts). */
  OperatorHeartbeatReceived: E.OPERATOR_HEARTBEAT_RECEIVED,
} as const;
