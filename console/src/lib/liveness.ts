// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Operator liveness, read the way the Gateway reads it. The Gateway never
// stores or announces staleness: it stamps stale_at (last sign of life plus
// the window for the declared heartbeat interval) on every active remote
// Operator at read time, and pushes ztd.v1.operator.heartbeat.received with
// the new last_heartbeat_at and stale_at each time it records a heartbeat.
// The console keeps those two fields current and derives the status from
// stale_at, so a silent Operator turns stale and a recovered one turns active
// without a re-list.

import type { Operator, OperatorStatus } from './types';

/** Payload of ztd.v1.operator.heartbeat.received (models.OperatorHeartbeatReceivedPayload). */
export interface OperatorHeartbeat {
  operator_id: string;
  last_heartbeat_at: string;
  stale_at?: string;
}

/** Reads an operator.heartbeat.received event's data, or null when it is not one. */
export function parseOperatorHeartbeat(data: Record<string, unknown>): OperatorHeartbeat | null {
  const { operator_id, last_heartbeat_at, stale_at } = data;
  if (typeof operator_id !== 'string' || typeof last_heartbeat_at !== 'string') return null;
  return { operator_id, last_heartbeat_at, stale_at: typeof stale_at === 'string' ? stale_at : undefined };
}

/**
 * The status op reads as at now (epoch ms). Only an active remote Operator
 * carries stale_at, and it reads as stale once now is past it; every other
 * Operator reads as its stored status.
 */
export function operatorStatusAt(op: Operator, now: number): OperatorStatus {
  if (!op.stale_at) return op.status;
  return now > Date.parse(op.stale_at) ? 'stale' : 'active';
}

/** Returns operators with hb's last_heartbeat_at and stale_at applied to its Operator. */
export function applyOperatorHeartbeat(operators: Operator[], hb: OperatorHeartbeat): Operator[] {
  return operators.map((op) =>
    op.id === hb.operator_id ? { ...op, last_heartbeat_at: hb.last_heartbeat_at, stale_at: hb.stale_at } : op,
  );
}

/** The earliest stale_at (epoch ms) still ahead of now, or null when no Operator can turn stale. */
export function nextStaleAt(operators: Operator[], now: number): number | null {
  let next: number | null = null;
  for (const op of operators) {
    if (!op.stale_at) continue;
    const at = Date.parse(op.stale_at);
    if (at >= now && (next === null || at < next)) next = at;
  }
  return next;
}
